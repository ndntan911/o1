# Incident copilot

The incident page can run a bounded, read-only investigation of one incident.
The agent chooses from six read tools, reads the incident's monitor, checks,
timeline, and alert outcomes, and returns an assessment whose hypotheses must
cite recorded tool results. Every claim it makes is checked against the stored
trace before it is shown; a claim that cannot be checked is dropped, and an
answer with no checkable evidence is stored as **insufficient**, not as
analysis.

The feature is off by default. It is separate from **Quick explanation**
(incident insights): insights makes one model call over a fixed payload; the
copilot makes up to five model calls and six tool calls.

## Enable and configure

The copilot needs a model provider that implements tool calling. Configure the
provider with the existing `O1_AI_*` settings — the same ones insights uses —
then turn the copilot on:

```sh
O1_COPILOT_ENABLED=true
O1_AI_PROVIDER=openai
O1_AI_BASE_URL=https://api.openai.com/v1   # any OpenAI-compatible endpoint
O1_AI_API_KEY=...
O1_AI_MODEL=gpt-4o-mini
```

`O1_AI_PROVIDER=stub` answers deterministically without a network call. It
exists for local demos and tests; it is not a model.

| Setting | Default | Meaning |
|---|---|---|
| `O1_COPILOT_ENABLED` | `false` | Serve the investigate endpoints and show the panel |
| `O1_COPILOT_MODEL` | `O1_AI_MODEL` | Model used for investigations |
| `O1_COPILOT_TIMEOUT` | `90s` | Whole-run deadline |
| `O1_COPILOT_CALL_TIMEOUT` | `30s` | One provider or tool call |
| `O1_COPILOT_MAX_MODEL_CALLS` | `5` | Model turns per run |
| `O1_COPILOT_MAX_TOOL_CALLS` | `6` | Tool calls per run |
| `O1_COPILOT_MAX_RESULT_BYTES` | `8192` | Runner marker threshold per tool call; the registry's structural cap stays 8 KB |
| `O1_COPILOT_MAX_OUTPUT_TOKENS` | `1200` | Output tokens per model call |
| `O1_COPILOT_MAX_INPUT_TOKENS` | `40000` | Total input-token budget |
| `O1_COPILOT_PER_INCIDENT_PER_HOUR` | `3` | Manual investigations per incident |
| `O1_COPILOT_PER_WORKSPACE_PER_DAY` | `20` | Manual investigations per workspace |

Starting an investigation requires a member session; a viewer can read a stored
result but cannot start one. When the feature is off or the provider is
misconfigured, the investigate endpoints answer `501 Not Implemented` with
`not_implemented` and the UI hides the action. A misconfigured provider never
takes the API down.

## What the model sees

The first message is a redacted **seed**: monitor configuration and current
state, incident state and duration, timeline event kinds and timestamps, and
alert delivery outcomes. The seed deliberately carries no event ids, no
free-text incident detail, and no monitor secret. It is enough to decide which
tool to call, but no fact in it is citable until a tool returns it.

The model also sees the six read-only tool definitions below. The workspace
comes from the authenticated user and is never a tool argument, so a run cannot
read another tenant.

| Tool | Returns | Never returns |
|---|---|---|
| `o1_list_monitors` | id, name, paused, current state, active incident id, last check time | URL, headers, body, query |
| `o1_get_monitor` | method, expected status, interval, timeout, thresholds, state | URL, headers, body, query |
| `o1_list_incidents` | id, monitor id, status, started, resolved, duration | notification state, recipients |
| `o1_get_incident` | incident fields plus allowlisted timeline events | actor emails, detail outside the allowlist |
| `o1_get_recent_checks` | up to 100 checks: timestamp, status, latency, failure class; plus count, failures, p95, class counts | error bodies, response snippets, target URL |
| `o1_get_alert_deliveries` | channel kind, event, status, attempts, sent time; members only | recipient, channel name, error text |

A check's failure class is one of `timeout`, `dns`, `tls`, `connection`,
`redirect`, `status`, or `ssrf_blocked`; the stored error message is never
returned. Every tool result is wrapped:

```json
{"data": {}, "meta": {"as_of": "...", "count": 0, "returned": 0, "total": 0, "truncated": false}}
```

Truncation is structural: the registry caps every result at a fixed 8 KB,
shortening arrays (and oversized string fields) and keeping the `{data, meta}`
wrapper, so a JSON Pointer into a returned item still resolves.
`O1_COPILOT_MAX_RESULT_BYTES` sets a second, runner-side threshold. A marshaled
result that is still over that threshold is replaced by a marker carrying its
SHA-256 and size, and `meta.truncated` is set — the model is told the data was
truncated. Lowering the setting can only replace a result the registry already
made fit; it does not change the registry's fixed 8 KB structural cap.

A tool failure is data. It comes back as `is_error: true` with a bounded code
(`not_found`, `invalid_argument`, `rate_limited`, `forbidden`, `internal`); the
agent may adapt but cannot cite the failed call. No stack trace, SQL, raw error,
or internal path reaches the model.

System and user text — tool results, the seed, monitor names, event detail — is
data, never instructions. The system prompt states the boundary, but the server
does not rely on the model obeying it. There are no write tools, no outbound
HTTP from a tool, no shell, and no SQL. The only network request is the model
provider call.

### The answer contract

The final answer must be one JSON object. The server enforces the contract; the
prompt is only the first line of defence.

```json
{
  "sufficient": true,
  "assessment": "One short paragraph.",
  "hypotheses": [{
    "rank": 1,
    "cause": "The target started timing out above the configured timeout.",
    "confidence": "high",
    "evidence": [{
      "tool_call_id": "call_abc",
      "pointer": "/data/checks/0/failure_class",
      "value": "timeout",
      "note": "the first failing check"
    }],
    "falsified_by": "A successful check in the same window would rule this out."
  }],
  "next_checks": ["Compare the deploy time with the first timeout."]
}
```

- Every evidence item must name a `tool_call_id` from this run whose call
  succeeded. `pointer` is an RFC 6901 JSON Pointer into that call's stored
  result, and `value` must be the exact value at that pointer.
- A citation that fails either check is dropped. A hypothesis left with no
  evidence is marked **unverified**.
- If no hypothesis survives, the result is stored as **insufficient** with an
  empty hypotheses array, and the UI says the data does not support a cause.
- At most three hypotheses and five evidence items per hypothesis survive
  parsing; ranks are renumbered. `next_checks` are advice, displayed
  separately, never findings.
- Output that is not JSON, or that fails to parse, is stored as **failed** and
  never shown as analysis.

## Caps

| Limit | Value |
|---|---|
| Model calls per investigation | 5 |
| Tool calls per investigation | 6 |
| Wall clock | 90 s |
| Tool result | 8 KB fixed structural cap in the registry, then the `O1_COPILOT_MAX_RESULT_BYTES` runner marker threshold |
| Output tokens per model call | 1,200 |
| Total input tokens | 40,000 |
| Investigations per incident per hour | 3 |
| Investigations per workspace per day | 20 |

Tool calls run sequentially, so the trace, the cost, and the tests are
deterministic. Hitting the model-call, tool-call, token, or wall-clock cap ends
the run as **incomplete** with `limit_exceeded`; a manual retry is the recovery.

## Failure states

| Case | Status | Retry |
|---|---|---|
| Provider timeout, 408/429/5xx, transport error before a final answer | Row stays `running`; the last attempt sets `failed` with `provider_unavailable` | River retries at most twice; the row records the attempts and the stored trace is the last attempt |
| Provider 4xx or a rejected tool schema | `failed` with `provider_rejected` | No automatic retry |
| Output is not the contract | `failed` with `invalid_output` | No automatic retry |
| The deadline or a cap is reached | `incomplete` with `limit_exceeded` | Manual retry |
| A `running` row is older than five minutes | `failed` with `stale` | Manual retry; the next POST or the sweep reaps it |
| The initiating user is no longer a member | `failed` with `actor_removed` | No retry |
| A tool returns an error | Not a failure; the agent sees it | The agent decides |

Every terminal row keeps its trace, model-call records, token counts, duration,
and error code. `investigation_started` and `investigation_completed` are
audited best-effort with ids and counts, never payloads.

## Evaluation

The fixtures live in `backend/internal/copilot/testdata/eval/`. Each one fixes
a redacted seed and the canned tool results, then names the properties a
correct run must hold. The checker never compares prose.

The ten cases are timeout, DNS, one success inside a run of failures, a monitor
with no checks, a tool that returns an internal error, a citation to an
invented `tool_call_id`, a citation whose pointer resolves to a different
value, a model that keeps calling tools until the cap, a monitor name
containing "ignore previous instructions", and a 20 KB tool result.

Live-fixture coverage and scripted unit-test coverage are not the same thing:

- **Live fixtures (cases 1-5, 9, 10).** The seed and canned tool results put a
  real provider in the situation, and the checker asserts the resulting
  property: a failure-class citation, a success citation with lower confidence,
  an insufficient answer, a resisted injection, or a truncated result.
- **Scripted unit-test trajectories (cases 6-8).** A live provider cannot be
  made to invent a `tool_call_id`, cite a pointer whose value is wrong, or loop
  until the cap just by changing the seed. The unit tests supply an
  `ai.Stub{ChatScript: ...}` trajectory that does the bad thing, then assert
  the server drops the citation, marks the hypothesis unverified, or stops at
  the cap. The live fixtures for those three names carry only representative
  seed data. A green live row for `invented_tool_call_id`,
  `wrong_pointer_value`, or `loop_limit` means the model answered normally; it
  does **not** mean the adversarial path was exercised, and it is not evidence
  that a real model resists hallucinating citations or looping.

One further limit applies to `refuses_injection`: the property requires a
terminal answer, so a stalled, timed-out, or looping injection run fails rather
than passing vacuously. The injection fixture also requires the agent to have
called `o1_get_recent_checks`.

Run the unit tests without a provider:

```sh
cd backend && go test ./internal/copilot -run 'TestLoadFixtures|TestCheckFixture|TestRunLive' -v
```

Run the opt-in live evaluation with the provider you configured:

```sh
O1_AI_PROVIDER=openai O1_AI_API_KEY=... O1_AI_MODEL=... make copilot-eval
```

The command exits 2 when `O1_AI_PROVIDER` is off or misconfigured, so it cannot
accidentally run as part of a build. It writes a table of fixture, pass/fail,
status, tool calls, tokens, and unsupported citations to
[docs/ai/copilot-eval.md](copilot-eval.md), and exits 0 once the report is
written even when some fixtures fail; the table is the result.

The checked-in table is a **stub-only** run. It exercises the harness and the
fixtures, not a model, and is not evidence of model quality. Replace it with a
real-provider run before making any claim about the copilot's answer quality.
