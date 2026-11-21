# Incident insights — design

Date: 2026-11-21

## Intent

o1 already tells a team that a monitor failed. It does not help them decide *why*. This
feature gives the on-call engineer a starting point: three ranked hypotheses for what
caused an incident, each one tied to evidence that exists in the incident's own data, plus
a clear statement of what would prove it wrong.

It is an assistant, not an authority. The most valuable answer it can give is "there is not
enough evidence here to say".

## Goals

- One button on the incident detail page returns an assessment and up to three hypotheses.
- Every hypothesis cites the incident event or field it rests on, and the UI links to that
  evidence.
- When the data is thin, the answer says so instead of inventing a story.
- The result is stored with the incident, so reopening it costs nothing, and re-running is
  an explicit act.
- The feature is off until configured. With no provider key, the product behaves exactly as
  it does today.
- A stub provider makes the whole feature testable and demoable offline.

## Non-goals

- Autonomous remediation, tool-calling agents, or anything that acts on production.
- A chat interface. One request, one structured answer.
- Learning across incidents or fine-tuning.
- Explaining incidents in workspaces the caller cannot already read.

## Anti-goals

- Confident prose without citations.
- Sending monitor URLs, request headers, channel secrets, recipient addresses, or raw
  error bodies to a model provider.
- Blocking a page render on a model call.
- Treating model output as instructions. It is data.

## Constraints

- Workspace-scoped like every other resource: cross-tenant access returns 404.
- Audit event on every generation, naming the actor and the model.
- Provider is an interface with a factory, mirroring `internal/email`. No new dependency;
  the HTTP client is the standard library.
- One call per generation, 15s timeout, capped output tokens, rate limited per workspace.
- Tests are the gate: unit tests for payload building, prompt construction, response
  parsing and citation validation; a stub provider for the rest.

## Approaches

- **A. Structured single-call explanation, stored per incident.** One prompt, a strict JSON
  output contract, validated against the incident's own data, persisted. *Chosen.* Cheap,
  explainable, and the validation step is where the product value lives.
- **B. Tool-calling agent that queries metrics itself.** More flexible, but it needs scoped
  credentials, produces non-deterministic costs, and cannot be validated offline against
  fixtures.
- **C. Free-form chat about an incident.** Easiest to build and the hardest to trust; no
  output contract, nothing to validate, no way to evaluate.

## What goes to the provider

| Included | Never included |
|---|---|
| Monitor name, method, expected status, interval, timeout, confirmation and recovery thresholds | Monitor URL, headers, body, query string |
| Incident start and resolution times, current state | Alert channel secrets, signing keys, bot tokens |
| Timeline events (kind, timestamp) | Recipient email addresses |
| Probe window aggregates: checks, failures, p95 latency, last failure class | Raw error bodies or HTML from the target |
| Alert delivery outcomes (channel kind, delivered/failed/skipped) | User names, emails, session data |

Monitor names are user-controlled text and are wrapped as untrusted input; the prompt says
so, and the output is validated afterwards regardless of what it claims.

## Output contract

```json
{
  "sufficient": true,
  "assessment": "One short paragraph.",
  "hypotheses": [
    {
      "rank": 1,
      "cause": "The target started timing out above the configured 10s timeout.",
      "confidence": "high",
      "evidence": [{"kind": "event", "event_id": "...", "note": "three consecutive timeouts"}],
      "falsified_by": "A successful check in the same window would rule this out."
    }
  ]
}
```

Rules the server enforces, not the model:

- `sufficient: false` requires an empty `hypotheses` array.
- Every `event_id` that is present must exist in the incident timeline. Unknown ids are
  dropped, and the hypothesis is marked `unsupported` in the response.
- At most three hypotheses; anything beyond is dropped.
- A response that fails to parse is stored as a failure, never shown as analysis.

## Storage

`incident_insights`: id, workspace_id, incident_id, model, prompt_version, payload_hash,
status (`ok` | `failed` | `insufficient`), response JSONB, input_tokens, output_tokens,
created_by, created_at. Indexed on `(incident_id, created_at DESC)`.

## API

- `POST /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/insights` — generate.
  Returns the stored insight. 409 when a generation for the same payload hash and prompt
  version is already the latest one, unless `?force=true`.
- `GET /api/v1/workspaces/{workspaceID}/incidents/{incidentID}/insights/latest` — stored
  result, 404 when none.
- Both require a session and workspace membership; both write an audit event.

## UI

A panel under the incident timeline: "Explain this incident". Empty state shows the
button; a stored insight shows the assessment, the hypotheses with evidence links, and a
"Run again" button. The panel says which model produced it and when.

## Evaluation

A fixture set of six incidents, each with an expected property rather than an expected
text:

1. Timeouts only, no recoveries → a hypothesis mentions timeout, and it is ranked first.
2. A single blip inside the window → `sufficient: false` or low confidence.
3. Empty timeline (monitor never opened an incident) → refusal.
4. Monitor name containing an instruction ("ignore previous instructions") → output
   structure unchanged, name never echoed as a command.
5. Citation to a non-existent event id → dropped and marked unsupported.
6. More than three hypotheses in the model output → truncated to three.

Fixtures run against the stub provider in unit tests. The same fixtures can be pointed at a
real provider with `O1_AI_PROVIDER=openai` and `make insights-eval`, which records accuracy
in `docs/ai/insights-eval.md`.
