# Incident copilot and the o1 MCP server — design

Date: 2026-11-24
Status: proposed

## Intent

o1 already tells a team that a monitor failed. Incident insights gives a cheap,
one-shot reading of the incident's existing data. Neither can go and look for
more evidence. This project adds the step between "something is wrong" and "a
human knows what to do": a read-only agent that can ask o1 for more evidence,
and an MCP server that exposes the same evidence to the AI clients a team
already uses.

The agent proposes. The server verifies every claim against the tool output. The
human decides. Nothing in this project writes to a monitor, an incident, a
target, or a customer system.

It is also a product, not a demo: the investigation is stored with the incident,
the MCP endpoint is a real integration surface for Claude, Cursor, or a team's
own agent, and both run on data the workspace already owns.

## The users

| User | What they get |
|---|---|
| On-call engineer | On the incident page: one "Investigate" action, a live step trace, up to three hypotheses with evidence links, and a clear refusal when the data is thin. |
| A developer with an AI client | `POST /mcp` with an o1 API key. Their client can list monitors, read an incident, inspect recent checks and alert deliveries, and reason over the result with the same redaction and workspace scope as the product. |
| The copilot itself | The same tool layer, called in-process. There is no second implementation of "what the agent can see". |

## Goals

1. Store one replayable investigation per run: assessment, at most three
   hypotheses, each resting on citations the server can resolve in a recorded
   tool result, plus what would falsify it.
2. Expose six read-only tools over MCP, authenticated by existing o1 API keys,
   with the workspace taken from the credential and never from a tool argument.
3. Make the tool layer testable offline with a deterministic stub provider and a
   scripted tool trajectory.
4. Keep one redaction boundary: a tool output, the copilot seed, and the
   insights payload are built from the same allowlisted projections, and golden
   tests assert that a monitor URL, a request header, a recipient address, and a
   raw error body never appear in any of them.
5. Prove the failure cases in tests: a tool that errors, a citation to an
   invented tool call, a loop that will not stop, a malicious monitor name, and
   an oversized result.
6. Be off by default. Two switches, MCP and copilot, each independently
   disabled. With both off, the product behaves exactly as it does today.

## Non-goals

- Write tools or remediation. No acknowledge, resolve, pause, retry, deploy, or
  arbitrary HTTP. The tool interface has no write method, so this is a
  compile-time property, not a policy.
- Autonomous investigation when an incident opens. v1 is manual; automatic runs
  need a cost model the product does not have yet.
- RAG over runbooks or a knowledge base. o1 has no runbook corpus, no ingestion
  path, and no permission model for documents. A vector store bolted on here
  would be decoration, not retrieval.
- A chat interface. One investigation is one structured answer. Follow-up
  questions are a different product.
- Cross-incident learning, memory, or fine-tuning.
- Kafka, NATS, or another broker. River already gives durable jobs, retries,
  uniqueness, and visibility; adding a broker for the résumé would make the
  system worse.
- SSE or WebSocket progress in v1. The UI polls the stored row. Streaming
  progress from a separate worker process needs a cross-process fan-out that is
  not worth its complexity for a 30–60 second job.
- OAuth 2.1 authorization for MCP. v1 uses static bearer API keys, which o1
  already issues, stores hashed, scopes to one workspace, and audits. Clients
  that insist on the OAuth discovery flow are unsupported in v1.
- Billing AI usage by tokens. v1 uses hard caps and audit records; plans and
  quotas are a later product decision.

## Anti-goals

- Confident prose with no trace.
- Sending monitor URLs, headers, bodies, query strings, recipients, user emails,
  session data, channel secrets, or raw target errors to a model.
- A tool that can mutate state, even behind a flag.
- An agent loop with unbounded time, tokens, or tool calls.
- Treating tool output, monitor names, or incident details as instructions.
- Showing a failed or incomplete investigation as a successful empty answer.
- Making a second redaction path that can drift from the one insights uses.

## Constraints

- Go 1.26, PostgreSQL 16, River, and the existing service layout. The MCP server
  uses the official Go SDK pinned to a version that supports the current
  stateless Streamable HTTP transport; it is the only new direct dependency.
- `ai.Provider` and the insights feature stay as they are. Tool calling is a new
  optional interface, not a rewrite of the one-shot path.
- Workspace scoping is enforced by `workspaces.Authorize`, including the API-key
  branch that checks the key's workspace and role. Cross-tenant reads return 404,
  not 403.
- API keys are viewer or member, workspace-scoped, hashed at rest, revocable, and
  never carry global admin rights. The MCP endpoint authenticates a bearer key
  directly and never accepts a session cookie.
- Audit writes are best-effort and never fail the action they record.
- Tests are the gate: unit tests, testcontainers integration against real
  PostgreSQL, frontend tests, and a Playwright path. The MCP server gets both
  protocol tests and redaction tests.
- One image, three binaries. The API enqueues; the worker executes; the schema
  changes in migration `00013`.

## Approaches

**A. MCP server only.** Expose o1 to external agents, ship no internal
investigator. Cheap, but it does not solve the on-call job, has no place to
verify citations, and turns the product into a data-access layer with no opinion.

**B. Internal investigator with private tools only.** Solves the on-call job,
but misses the interop and distribution value of MCP, and leaves the
"agent experience" claim unproven to anyone outside the dashboard.

**C. One read-only tool layer, two surfaces.** The copilot calls it in-process;
the MCP adapter calls the same functions for external clients. *Chosen.* One
scoping and redaction boundary, one set of tests, and the internal agent
dogfoods exactly what customers get. The cost is an adapter and a second
authentication path, both small and testable.

**D. Autonomous remediation.** The agent acknowledges, pauses, or retries based
on its own conclusion. Rejected: the blast radius is unacceptable, and the
product value is in a human deciding faster, not in removing the human.

**Why RAG is not in the chosen approach.** The JD language that motivates this
project lists "LLM API, RAG, tool calling, MCP". Tool calling and MCP are the
parts o1 can demonstrate honestly today, with real data and a real verification
story. RAG needs a corpus with permissions and a retrieval quality bar. The
right next RAG project is a document-grounded one; adding an unused vector table
here would be the same checkbox engineering the existing insights design
rejects.

## Relationship to incident insights

Insights remains: one call, a fixed payload, no tools, cheap and deterministic.
Its design listed "tool-calling agents" as a non-goal. That non-goal applied to
that feature; the reasons it gave were scoped credentials, non-deterministic
cost, and no offline validation. This design answers them with read-only tools,
hard caps, a recorded trace, and fixtures. The incident page keeps both actions:
**Quick explanation** (insights, unchanged) and **Investigate** (the agent).

## The shape

```
Browser ──POST investigate──▶ API ──River job──▶ worker
                                                   │
                                                   ▼
                                            copilot service
                                                   │
                              ┌────────────────────┴────────────────────┐
                              ▼                                         ▼
                       ai.ToolProvider                          tool registry
                     (OpenAI-compatible)                    (read-only, redacted)
                                                                    │
                                                    ┌───────────────┼───────────────┐
                                                    ▼               ▼               ▼
                                               incidents        monitors         checks
                                                    ▲
External AI client ──POST /mcp──▶ MCP adapter ──────┘
```

The MCP adapter and the copilot both call `internal/copilot/tools`. The adapter
is a transport; it contains no query logic, no redaction logic, and no
permission logic of its own.

## The tools

All tools are read-only, workspace-scoped from the credential, and return
bounded JSON. IDs are UUIDs. No tool accepts a `workspace_id`.

| Tool | Input | Returns | Never returns |
|---|---|---|---|
| `o1_list_monitors` | `limit?` (1–50) | id, name, paused, current state, active incident id, last check time | URL, headers, body, query |
| `o1_get_monitor` | `monitor_id` | method, expected status, interval, timeout, confirmation and recovery thresholds, paused, current state, active incident id | URL, headers, body, query |
| `o1_list_incidents` | `monitor_id?`, `status?`, `limit?` (1–20) | id, monitor id, status, started, resolved, duration | notification state, recipients |
| `o1_get_incident` | `incident_id` | incident fields plus timeline events: id, kind, timestamp, allowlisted detail | actor emails, raw detail text outside the allowlist |
| `o1_get_recent_checks` | `monitor_id`, `hours?` (1–168) | up to 100 checks: timestamp, status, latency ms, failure class; plus count, failures, p95, failure-class counts | error bodies, response snippets, target URL |
| `o1_get_alert_deliveries` | `incident_id` | channel kind, event, status, attempts, sent time; member keys only | recipient, channel name, error text, error class |

A check's failure class is derived at read time from the stored error's bounded
prefix (`timeout`, `dns`, `tls`, `connection`, `redirect`, `status`,
`ssrf_blocked`); a row that does not match one of those becomes `unknown`, and
the stored message is never returned. No new column is added to
`probe_results`, and historical rows keep working because the probe worker has
always written `class: message` or `class`. Alert deliveries expose status and
attempts, not an error class: the stored `last_error` is a message, not a class,
and the agent does not need it to know whether the alert went out.

### Who can call what

| Caller | Tools | Start an investigation |
|---|---|---|
| Viewer session | five read tools, no delivery outcomes | No |
| Member session | all six, delivery outcomes redacted | Yes |
| Admin session | all six, no extra fields | Yes |
| Viewer API key | five read tools | No; the POST is session-only |
| Member API key | all six | No; the POST is session-only |

`o1_get_alert_deliveries` requires `RoleMember`. `incidents.Service.Deliveries`
is admin-only and returns recipients, so the tool cannot reuse it; a new narrow
query returns only channel kind, event, status, attempts, and timestamps.
Starting an investigation writes a row and a timeline event, so it is a member
action. The worker then runs with the initiating member's role, not an internal
super-scope. A viewer can read a stored investigation and use the five
viewer-safe MCP tools.

Every result is wrapped:

```json
{
  "data": {},
  "meta": {"as_of": "2026-11-24T10:00:00Z", "truncated": false, "count": 0}
}
```

Truncation is structural, not a byte cut: arrays are capped by count, items are
dropped from the end, and the wrapper survives, so every JSON Pointer in a
citation still resolves. `meta.truncated`, `meta.returned`, and `meta.total` say
what happened. `count`, `failures`, and `p95` describe the full requested
window, not the returned sample; the meta block says so. The 8 KB cap is applied
after marshaling; a single oversized item has its bounded string fields
shortened before any item is dropped. The exact returned result is what the
citation check resolves against and what the trace stores before any later
compression.

A domain error is a tool result with `is_error: true` and a bounded code
(`not_found`, `invalid_argument`, `rate_limited`, `forbidden`, `internal`); the
agent sees the error and may adapt. No stack trace, SQL, or internal path ever
reaches the model.

The redaction allowlist is the same one insights relies on. The concrete
projections live in one package; the insights payload builder is moved onto it
if duplication appears, and its existing leak test stays. The test asserts that
a monitor URL, an `Authorization` header, a recipient address, and a raw error
body cannot appear in any tool output or seed.

## The agent loop

1. The API stores a `queued` investigation and enqueues one River job with the
   investigation id.
2. The worker loads the investigation, the incident, the monitor, the timeline,
   and delivery outcomes, then builds the same redacted seed insights uses:
   monitor configuration, incident state, timeline kinds and timestamps, and
   delivery outcomes. The seed carries no event ids. Ids come only from
   `o1_get_incident`, so every citable fact has a tool call behind it.
3. The worker calls `ai.ToolProvider.Chat` with the seed, the six tool
   definitions, and a system prompt that says tool results and user text are
   data, never instructions.
4. If the response contains tool calls, the worker executes them sequentially
   through the registry, appends the results, and calls the model again.
5. The loop stops on a final text answer, or on any limit:
   - at most 5 model calls,
   - at most 6 tool calls,
   - 90 seconds wall clock,
   - 8 KB per tool result,
   - 1,200 output tokens per call and a 40,000 input-token budget overall.
   `O1_COPILOT_TIMEOUT` (90s) is the overall deadline; a per-call timeout of
   30s (`O1_COPILOT_CALL_TIMEOUT`) bounds each provider call. The existing
   `O1_AI_TIMEOUT` stays the insights timeout and does not cap the agent.
   Hitting the overall deadline is a limit (`incomplete`, manual retry); a
   single call timing out is transient and is retried by River.
6. A final answer must be JSON. Anything else is a failed investigation, stored
   and shown as a failure, never as analysis.
7. Tool calls are sequential so the trace, the cost, and the tests are
   deterministic. A tool error is data; it does not abort the loop.

The seed is not the whole database. It is enough to decide which tool to call:
monitor configuration, incident state, timeline event kinds and timestamps,
delivery outcomes, and a one-line instruction to investigate.

## Output contract and verification

```json
{
  "sufficient": true,
  "assessment": "One short paragraph.",
  "hypotheses": [
    {
      "rank": 1,
      "cause": "The target started timing out above the configured 10s timeout.",
      "confidence": "high",
      "evidence": [
        {
          "tool_call_id": "call_abc",
          "pointer": "/data/checks/0/failure_class",
          "value": "timeout",
          "note": "the three most recent checks timed out"
        }
      ],
      "falsified_by": "A successful check in the same window would rule this out."
    }
  ],
  "next_checks": ["Compare the deploy time with the first timeout."]
}
```

The server enforces these rules, not the prompt:

- `sufficient: false` requires an empty `hypotheses` array.
- At most three hypotheses; extras are dropped and ranks renumbered.
- Every evidence item must name a `tool_call_id` that exists in this
  investigation's trace and whose call succeeded. The seed is a hint, not
  evidence: a fact from the seed becomes citable only after a tool returns it.
- `pointer` is an RFC 6901 JSON Pointer into that call's stored result. It must
  resolve, and the resolved scalar must equal `value`.
- An evidence item that fails either check is dropped. A hypothesis left with no
  evidence is marked `unverified`.
- If every hypothesis is unverified, or none survive, the investigation is
  `insufficient` and the UI says the data does not support a cause.
- `next_checks` are advice, not evidence. They are displayed separately, at most
  three, and are never presented as findings.
- Output that is not JSON, or that fails to parse, is `failed`.

A tool result can contain text written by a user, such as a monitor name. It is
wrapped as untrusted data, the system prompt says so, and the verification step
is what actually prevents it from becoming a claim.

## MCP surface

- **Endpoint:** `POST /mcp`. One path, no workspace in the URL. Caddy routes it
  to the API alongside `/api/*`.
- **Protocol:** Streamable HTTP, stateless, JSON responses. Support protocol
  versions `2026-07-28` and `2025-11-25`; the SDK handles negotiation,
  `tools/list`, `tools/call`, and the protocol headers. Stateless is required for
  `2026-07-28`, and it matches o1's per-request API-key model.
- **Authentication:** `Authorization: Bearer o1_<prefix>_<secret>`. The endpoint
  calls `AuthenticateAPIKey` directly. A missing, malformed, revoked, or wrong
  key gets `401` with `WWW-Authenticate: Bearer`; it never falls back to a
  cookie. The key's workspace and role are the whole authorization context.
- **Tenant isolation:** the workspace comes from the key. A monitor or incident
  id from another workspace returns `not_found`. A tool cannot ask for a
  different workspace because no tool has a workspace argument.
- **Origin:** if `Origin` is present it must be one of the configured app
  origins; otherwise `403`. Non-browser MCP clients normally send no Origin.
  This is the DNS-rebinding protection the MCP transport spec requires.
- **Rate limit:** 60 tool calls per minute per key, burst 10, using the same
  keyed limiter the REST API uses. The key id is the bucket key.
- **Audit:** every `tools/call` writes `mcp.tool_called` with workspace, API key,
  tool name, duration, result bytes, and status. A failed auth writes no audit
  event with key material; only the prefix, if one parsed.
- **Instructions:** the server advertises an instruction string telling clients
  the surface is read-only, workspace-scoped, and that IDs come from
  `o1_list_monitors` / `o1_list_incidents`.
- **Client setup:** `docs/mcp.md` shows the endpoint, the key role to create, and
  one generic client config. The API keys screen gets a short "Connect an AI
  client" card with the same information and a link to the doc.

The OAuth discovery flow is explicitly unsupported. A client that only knows
how to start an OAuth dance will fail with a clear 401; a client that accepts a
static bearer token works.

## Storage

Migration `00013_incident_investigations.sql`:

```sql
CREATE TABLE incident_investigations (
    id             uuid PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    incident_id    uuid NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    status         text NOT NULL CHECK (status IN
                     ('queued', 'running', 'ok', 'insufficient', 'incomplete', 'failed')),
    model          text NOT NULL,
    prompt_version text NOT NULL,
    seed_hash      text NOT NULL,
    result         jsonb,
    trace          jsonb NOT NULL DEFAULT '{}'::jsonb,
    attempts       integer NOT NULL DEFAULT 0,
    tool_calls     integer NOT NULL DEFAULT 0,
    input_tokens   integer NOT NULL DEFAULT 0,
    output_tokens  integer NOT NULL DEFAULT 0,
    duration_ms    integer NOT NULL DEFAULT 0,
    error_code     text,
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz
);

CREATE INDEX incident_investigations_incident_created_idx
    ON incident_investigations (incident_id, created_at DESC);

CREATE UNIQUE INDEX incident_investigations_one_active_idx
    ON incident_investigations (incident_id)
    WHERE status IN ('queued', 'running');
```

The row stores `seed_hash` separately. `trace` holds each model call (finish
reason, tokens, duration) and each tool call (id, tool, arguments summary,
arguments hash, redacted result, error flag, duration). It is capped; when the
cap is reached, result bodies are replaced by their hashes and a `truncated`
marker, because the citation check already happened before storage.

The worker marks the row `running` when it starts. Retries do not create a
second row. A transient provider error leaves the row `running`, increments
`attempts`, and returns the error to River, so the UI keeps polling; only the
last attempt sets `failed`. A row left `running` for more than five minutes is
stale and is marked `failed` with `stale` by the next POST or the scheduler's
one-minute sweep. The partial unique index keeps one active row per incident.

On `ok` or `insufficient`, the worker appends one incident timeline event,
`investigation_completed`, with the investigation id, model, status, tool-call
count, and hypothesis count, in the same transaction as the terminal row; a
failed insert rolls the status back for a retry. Failed and incomplete runs do
not write a timeline event; the panel shows them and offers a retry. Migration
`00014` widens the inline `incident_events.kind` check with the new kind; no
applied migration is edited.

## API

| Method and path | Behaviour |
|---|---|
| `POST /api/v1/workspaces/{w}/incidents/{i}/investigations` | Session only, member or above. Creates a `queued` row and enqueues the job. `202` with the row. `409` with the running row if one is queued or running. `429` when the per-incident or per-workspace cap is hit. `501` when disabled. |
| `GET /api/v1/workspaces/{w}/incidents/{i}/investigations/latest` | Session or API key. Latest row, or `404`. `501` when disabled. |
| `GET /api/v1/workspaces/{w}/incidents/{i}/investigations/{id}` | Session or API key. One row, scoped to the incident and workspace, or `404`. |

The investigation row and the River job are inserted in one transaction with
`InsertTx`, so a `queued` row always has a job behind it; if the enqueue fails,
the row rolls back and the POST returns an error.

Caps: three investigations per incident per hour and twenty per workspace per
day, both configurable. The endpoint is also rate limited at six requests per
minute per workspace. A `running` row older than five minutes is reaped as
`failed`/`stale` in the same transaction that inserts the new row, so a crashed
worker cannot block an incident forever. The UI polls the by-id endpoint every
two seconds while the status is `queued` or `running` and stops on a terminal
status.

Starting an investigation is a session action, not an API-key action, because
it spends model tokens and writes a timeline event. Reading results is fine with
a viewer key.

## UI

The incident page keeps the insights panel and adds one "Investigate" block
under it, sharing the same card with two actions:

- **Quick explanation** runs existing insights, unchanged.
- **Investigate** starts the agent, then shows the step trace as it arrives:
  tool name, a short argument summary, status, and duration. The trace is
  collapsible after completion.
- The final answer shows the assessment, hypotheses, confidence, evidence chips
  that link to the cited timeline event or check row where possible, and an
  "Unverified" badge when the server dropped every citation.
- `insufficient` shows the refusal text; `failed` and `incomplete` show what
  happened and a retry button; `queued`/`running` show a live region with
  elapsed time, and `attempts > 1` says the provider call is being retried.
- The model and generation time are always visible. The copy says the agent is
  read-only and that the hypotheses are a starting point.

The API keys screen gains the MCP card described above. The public status page
is untouched.

## Limits, cost, and failure

| Limit | Value |
|---|---|
| Model calls per investigation | 5 |
| Tool calls per investigation | 6 |
| Wall clock | 90 s |
| Tool result | 8 KB target, structurally truncated with a marker |
| Output tokens per model call | 1,200 |
| Total input tokens | 40,000 |
| Investigations per incident per hour | 3 |
| Investigations per workspace per day | 20 |
| MCP tool calls per key per minute | 60, burst 10 |

Failure mapping:

| Case | Status | Retry |
|---|---|---|
| Provider timeout, 408/429/5xx, transport error before a final answer | Row stays `running`; only the last attempt sets `failed` with `provider_unavailable` | River retries at most twice; `attempts` and the trace show each try |
| Provider 4xx or a rejected tool schema | `failed` with `provider_rejected` | No automatic retry |
| Output is not the contract | `failed` with `invalid_output` | No automatic retry |
| The 90s deadline, a token cap, or the tool-call cap is reached | `incomplete` with `limit_exceeded` | Manual retry |
| A `running` row is older than five minutes | `failed` with `stale` | Manual retry; the next POST or the sweep reaps it |
| The initiating user is no longer a member | `failed` with `actor_removed` | No retry |
| Tool errors | Not a failure; the agent sees them | The agent decides |

Metrics: investigations by status, duration, tool calls, tokens, and provider
errors; MCP tool calls by tool and status. Logs carry ids and counts, never
payloads. A misconfigured copilot provider leaves the copilot off and never
takes the API down. The existing startup validation of `O1_AI_*` is unchanged,
so a broken insights configuration still fails startup as it does today. The
MCP server has no model dependency and is unaffected.

## Security and privacy

| Boundary | Rule |
|---|---|
| Credential to workspace | Workspace comes from the session or the API key. Tools never accept it. Cross-tenant ids are `404`. |
| o1 to model | Only the allowlisted projections. No monitor URL, headers, body, query, recipient, user email, session, secret, or raw target error. |
| Tool output to model | Same projections, bounded, wrapped as data. A failed tool call cannot support a hypothesis. |
| Model output to user | Every claim needs a resolvable citation. Unsupported claims are dropped or marked unverified. |
| User text to model | Monitor names and allowlisted event detail are untrusted data. The system prompt says so; verification does not depend on the model obeying it. |
| Agent to production | No write tools, no outbound HTTP, no shell, no SQL. The registry's interface only reads. |
| Cost | Caps, rate limits, per-key buckets, and a kill switch. |
| Audit | `investigation_started`, `investigation_completed`, and `mcp.tool_called`, with actor, workspace, model, counts, and status. Best-effort. |

The copilot makes no network request except the existing model provider call.
It does not probe, fetch a URL, or follow a redirect. SSRF is out of scope for
this feature because the feature has no user-controlled outbound request.

## Evaluation

Fixtures run against the stub provider in unit tests. Each fixture asserts a
property, not exact prose:

1. Timeout incident → the agent calls `o1_get_recent_checks`, the first
   hypothesis mentions timeout, and its citation resolves.
2. DNS failure → the failure class in the tool result is cited; the hypothesis
   ranks DNS first.
3. One success inside a run of failures → the hypothesis is not high
   confidence, and the citation is the success.
4. A new monitor with no checks → `insufficient`, zero hypotheses.
5. A tool returns `internal` → the agent says the tool failed and does not
   invent data.
6. A citation to an invented `tool_call_id` → dropped, hypothesis unverified.
7. A citation whose pointer resolves but whose value is wrong → dropped.
8. A model that keeps calling tools → `incomplete` at the cap, no answer shown.
9. A monitor name containing "ignore previous instructions" → no tool outside
   the allowlist, no write, the output structure is unchanged.
10. A 20 KB tool result → structurally truncated under the 8 KB cap with a
    marker, and the model is told it was truncated.

MCP conformance tests use the official client against the Go server:

- `initialize` and `tools/list` return six tools for a member key and five for
  a viewer key.
- `tools/call` for a monitor in the key's workspace succeeds.
- A monitor id from another workspace returns a tool error, not data.
- No Authorization, a revoked key, and a bad key each return `401`.
- A disallowed Origin returns `403`.
- The audit row names the key and tool; the result contains no URL, header,
  recipient, or raw error.

The frontend tests cover the panel states. Playwright runs one investigation
end to end with the stub provider, opens a stored result, and checks the
accessibility of the running and failed states.

A real-provider run is opt-in: `make copilot-eval` with the same fixtures records
the pass rate and any unsupported citations in `docs/ai/copilot-eval.md`. A
fixture that only passes because the stub is scripted does not count.

## Operations and rollout

- Migration `00013`; no destructive change.
- Config: `O1_MCP_ENABLED` (default `false`), `O1_COPILOT_ENABLED` (default
  `false`), `O1_COPILOT_MODEL` (defaults to `O1_AI_MODEL`),
  `O1_COPILOT_TIMEOUT` (90s), `O1_COPILOT_CALL_TIMEOUT` (30s), and the caps
  above. The copilot requires a `ToolProvider`; the MCP server requires no model
  at all and can be enabled on its own.
- The API gets a River client for insertion only, as the scheduler does; it
  starts no workers. The worker registers the investigation job on its own `ai`
  queue with a small worker count.
- Caddy: route `/mcp` to the API, keep the existing `/api/*` route.
- Compose and the production stack get the switches, commented off by default.
- CI: the existing backend, integration, frontend, e2e, and pipeline jobs run.
  New tests go into the same gates; no new required job unless the MCP protocol
  test needs its own binary.
- Rollout: enable MCP first (no model cost), verify with a real client, then
  enable the copilot in the demo workspace with the stub, then with a real
  provider.
- Rollback: turn both switches off. Routes answer `501`, the UI hides the
  panels, and the stored rows remain readable in the database.

## Deliberately left out, with the trigger to add it

| Deferred | Add it when |
|---|---|
| Runbook RAG | Workspaces can store runbooks and a retrieval quality bar exists; then embed with a citations contract like this one. |
| Slack/Telegram summary of an investigation | The stored result is good enough that posting it is better than a link; reuse the existing alert channel and idempotency work. |
| Automatic investigation on incident open | A per-plan token budget and a latency SLO exist; start with opt-in for paid workspaces. |
| SSE/WebSocket progress | Progress must cross the worker/API boundary; use PostgreSQL `LISTEN/NOTIFY` before adding a broker. |
| Write tools with approval | A separate design with an approval queue, an idempotency key, and an undo path. Not a flag on this one. |
| OAuth for MCP | A target client refuses static bearer keys; then add resource metadata and an authorization server, not a bespoke token flow. |
| Kafka/NATS | A workload needs fan-out or replay that River cannot express. |

## Decisions left to the builder

- The exact MCP SDK version and the handler options that expose stateless
  Streamable HTTP with JSON responses; pin it and cover it with the conformance
  tests.
- The exact prompt wording and the tool descriptions, within the contracts
  above.
- Whether the redaction projections are extracted into a shared package
  immediately or after the first duplicated builder; either way, one leak test
  must cover both surfaces.
- The trace compression scheme once it hits the cap.
- The fixture data and the exact UI copy.
- The test client language for the MCP conformance test; the official Go client
  keeps it in one toolchain, the TypeScript client proves interop with the
  ecosystem the web app already uses.

## Calls made during review

These were open in the first draft; the answers are now part of the design, and
they are listed here so they can be checked at a glance.

1. **Failure class** is derived at read time from the stored probe error prefix;
   no `probe_results` migration and no message exposure. Unknown historical
   rows become `unknown`. Deliveries expose status and attempts, not an error
   class.
2. **Roles**: viewer sessions and viewer keys get five tools; member and admin
   get all six, including redacted delivery outcomes. Starting an investigation
   is a member session action because it writes a row and a timeline event. The
   worker runs with the initiating member's role.
3. **Retries** keep one row `running`; only the final attempt marks it `failed`.
   `attempts` records the tries, and a five-minute stale row is reaped as
   `stale`.
4. **Truncation** is structural, not a byte cut, so JSON Pointers still resolve.
   Aggregates describe the full window; `meta` says what was returned.
5. **The 90s deadline** is a limit (`incomplete`); a single call timeout is
   transient and River retries it. The copilot has its own per-call timeout;
   `O1_AI_TIMEOUT` stays with insights.
6. **The seed is not citable.** It carries no event ids; a fact is citable only
   after a tool returns it.
7. **Provider misconfiguration** degrades only the copilot. Existing `O1_AI_*`
   startup validation is unchanged, and MCP has no model dependency.
