# o1 MCP Server

The o1 API exposes its read-only tool surface over the Model Context Protocol
(MCP) at:

```
POST /mcp
```

The endpoint is disabled by default. Set `O1_MCP_ENABLED=true` on the API
process to enable it. While it is disabled, `POST /mcp` returns
`501 Not Implemented` with the problem code `not_implemented`.

## Authentication

`/mcp` accepts **only** an API key in the standard Bearer header:

```http
Authorization: Bearer o1_<prefix>_<secret>
```

Session cookies are never accepted. A missing, malformed, unknown, or revoked
key returns `401 Unauthorized` with:

```http
WWW-Authenticate: Bearer
Content-Type: application/problem+json
```

Create keys in the dashboard or with `POST
/api/v1/workspaces/{workspaceID}/api-keys`. The raw secret is shown once; store
it like any other credential. Authentication failures log only the parsed key
prefix: the key id is unknown until a key resolves. Origin-rejection and
success logs may also include the key id. No log or audit row contains the
secret.

### Origin check

Requests with no `Origin` header are accepted, which covers command-line and
agent clients. If `Origin` is present, it must exactly match one of the origins
in `O1_ALLOWED_ORIGINS`; otherwise the endpoint returns
`403 Forbidden`. This mirrors the API's CSRF rule and protects browser-based
clients.

### Rate limit

Each API key is limited to **60 calls per minute** with a burst of 10. Calls
over the limit return `429 Too Many Requests` with `Retry-After`.

## Transport and protocol

The server is stateless and returns JSON responses (not SSE). The target MCP
protocol versions are **2026-07-28** and **2025-11-25**. SDK v1.7.0 exposes no
supported-version option and negotiates its full known version list, so older
protocol versions may also work; the server does not refuse them. Every request
is authenticated and scoped independently; there is no `Mcp-Session-Id` state
to carry between requests. `GET` and `DELETE` on the endpoint are not needed.

The server sends these instructions during discovery/initialization:

> The o1 MCP surface is read-only and scoped to the API key's workspace. Every
> tool returns only data the key's role may read. Get monitor ids from
> `o1_list_monitors` and incident ids from `o1_list_incidents`; never guess ids.

## Workspace scoping

The API key belongs to exactly one workspace and carries a `viewer` or `member`
role. The workspace is never taken from tool arguments, so a key cannot read
another tenant. Ids from another workspace are reported as a bounded
`not_found` tool error.

| Key role | Visible tools | Can read alert deliveries |
|---|---|---|
| `viewer` | 5 | No |
| `member` | 6 | Yes |

The tool list is built per request from the key's current role. A viewer key
does not see `o1_get_alert_deliveries` and the tool is not registered on that
key's server. A direct call is rejected by the MCP SDK before any tool handler
runs; no delivery data is returned.

## Tools

All tools are read-only. Results use a `{data, meta}` envelope; `meta` carries
`as_of`, counts, and truncation state. Secrets are projected out before the
result is serialized.

| Tool | Role | Reads |
|---|---|---|
| `o1_list_monitors` | viewer | Workspace monitors with health and any active incident id. Never the URL, headers, body, or query. |
| `o1_get_monitor` | viewer | One monitor's probe configuration, health, and active incident id. Never the URL, headers, body, or query. |
| `o1_list_incidents` | viewer | Incidents newest first, defaulting to active. Never notification state or recipients. |
| `o1_get_incident` | viewer | One incident and its allowlisted timeline events. Never actor emails or non-allowlisted detail. |
| `o1_get_recent_checks` | viewer | Probe checks with bounded failure classes and full-window aggregates. Never raw probe errors, response bodies, or the target URL. |
| `o1_get_alert_deliveries` | member | Delivery outcomes for one incident: channel kind, event, status, attempts, timestamps. Never recipients, channel names, or error text. |

Every tool call writes a best-effort `mcp.tool_called` security event with the
workspace, actor, API key id, tool name, status, measured duration, and result
size. No key material, target URL, recipient, or raw error is stored in that
event.

## Unsupported: OAuth

o1 does **not** implement the MCP OAuth authorization flow. The endpoint does
not publish OAuth protected-resource or authorization-server metadata, and it
does not accept OAuth access tokens. The `WWW-Authenticate: Bearer` challenge
is informational only: create an o1 API key and send it as the Bearer token.
Clients that require OAuth discovery or dynamic client registration cannot use
this endpoint.

## Generic client configuration

Most MCP clients accept a JSON server entry like the following. Replace the
host and key with your deployment and a real workspace key.

```json
{
  "mcpServers": {
    "o1": {
      "type": "http",
      "url": "https://o1.example.com/mcp",
      "headers": {
        "Authorization": "Bearer o1_ab12cd34ef56_your-secret-here"
      }
    }
  }
}
```

For a local API process, use `http://localhost:8080/mcp` (or the address in
`O1_HTTP_ADDR`). The key must belong to the workspace you want to query; there
is no cross-workspace mode.

Verify the connection by listing tools: a viewer sees five tools and a member
sees six. Then call `o1_list_monitors` or `o1_list_incidents` to obtain ids for
the more specific tools.
