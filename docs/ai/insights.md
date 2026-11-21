# Incident insights

The incident page can ask a model what might have caused an incident. It answers with
three ranked hypotheses at most, each tied to evidence that exists in the incident's own
records, plus a line saying what would prove the hypothesis wrong. When the incident does
not contain enough evidence, the answer says so and offers nothing.

It is off by default.

## What the model sees

Only the incident's own data, and nothing that could leak secrets or customer content:

| Sent | Never sent |
|---|---|
| Monitor name, method, expected status, interval, timeout, confirmation and recovery thresholds | Monitor URL, headers, body, query string |
| Incident status, start and resolution times, duration, reminder count | Alert channel secrets, signing keys, bot tokens |
| Timeline events (id, kind, timestamp, non-secret detail) | Recipient email addresses |
| Delivery outcomes (channel kind, event, status, attempts) | User names, emails, sessions |

`internal/insights/payload.go` is the redaction boundary, and its test asserts that a
monitor URL, an `Authorization` header and a recipient address never appear in the encoded
payload.

## What the model is not allowed to claim

The server validates the answer; the prompt is only the first line of defence.

- A citation to an event id that is not in the timeline is dropped, and the hypothesis is
  marked **unverified** in the UI.
- A hypothesis left with no verifiable evidence is marked **unverified**.
- `sufficient: true` with no hypotheses is downgraded to an insufficient answer.
- More than three hypotheses are cut to three.
- Output that is not JSON is stored as a failed generation and never shown as analysis.

## Enable it

```sh
O1_AI_PROVIDER=openai
O1_AI_BASE_URL=https://api.openai.com/v1   # any OpenAI-compatible endpoint
O1_AI_API_KEY=...
O1_AI_MODEL=gpt-4o-mini
O1_AI_TIMEOUT=15s
O1_AI_MAX_TOKENS=700
```

`O1_AI_PROVIDER=stub` answers deterministically without a network call; that is how the
feature is demonstrated locally and how the unit tests exercise the pipeline. `off` hides
the panel and makes both endpoints return 404.

A misconfigured provider never takes the API down: the router logs nothing, leaves the
feature off, and keeps serving.

## Cost and blast radius

- One completion per request, capped output tokens, a 15s timeout (configurable).
- The panel is rate limited at 6 requests per minute with a burst of 2.
- Re-running over unchanged data is refused with 409 unless `?force=true`.
- Every generation writes an audit event with the model, prompt version, status and token
  counts. Failures to audit never change the response.
- Results are stored in `incident_insights`; reopening an incident costs nothing.

## Evaluation

The fixtures in `internal/insights/insights_test.go` cover the cases that matter more than
style:

1. A timeout incident cites the real event and is not marked unverified.
2. An invented event id is dropped and the hypothesis becomes unverified.
3. A confident answer with no hypotheses is downgraded.
4. Six hypotheses are cut to three, and their ranks are renumbered.
5. Fenced JSON is accepted; prose is rejected.
6. A payload carrying a monitor URL, a header and a recipient address leaks none of them.
7. A monitor name containing an instruction is wrapped as untrusted data, and the system
   prompt says such text is never an instruction.

Point 6 and 7 are the ones to run against a real provider before trusting the feature with
customer data: point into `O1_AI_PROVIDER=openai` and confirm the answer still refuses to
follow text inside the monitor name.
