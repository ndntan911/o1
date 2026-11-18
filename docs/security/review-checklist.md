# Security Review Checklist

Run this before a release that touches authentication, tenancy, probing, alert
delivery, the deploy path, or a scan exception. Each item states what to run
and what a pass looks like. It is deliberately mechanical: the threat model
(`threat-model.md`) explains why these matter.

## 1. Dependency and image gates

```sh
cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/...
```

Pass: `0 vulnerabilities`, exit 0. This is the blocking CI scope; `./...` is
advisory and may report testcontainers tooling.

```sh
docker run --rm -v "$PWD:/repo" -w /repo aquasec/trivy:0.76.2 fs \
  --scanners secret,misconfig --severity HIGH,CRITICAL --exit-code 1 .
```

Pass: exit 0. Release images are scanned separately by `release.yml`.

Exceptions live in `.trivyignore`. Every entry needs a reason and a date;
today the only entry is `CVE-2026-78661` (re-review 2027-01-12, see
`container-review.md`). A new entry without both fields fails this check by
inspection.

## 2. Response headers and CSP

```sh
curl -sSI https://<host>/ | grep -Ei 'strict-transport|content-security|x-content-type|x-frame|referrer'
curl -sSI https://<host>/api/v1/me | grep -Ei 'content-security|referrer|x-request-id'
```

Pass: HSTS, `Content-Security-Policy` with a `sha256-` script hash, nosniff,
`frame-ancestors 'none'`, no-referrer, and `application/problem+json` on the
401.

The inline theme script hash changes if `web/index.html` changes. The unit test
`web/src/meta.test.ts` recomputes it and fails when the Caddyfile hash drifts.
Regenerate with:

```sh
cd web && npm run build
node -e 'const{readFileSync}=require("fs"),{createHash}=require("crypto");const h=readFileSync("dist/index.html","utf8").match(/<script>([\s\S]*?)<\/script>/)[1];console.log("sha256-"+createHash("sha256").update(h,"utf8").digest("base64"))'
```

Check the browser console for `securitypolicyviolation` on `/login`, the
dashboard, and a public status page. Sentry ingest needs
`O1_CSP_EXTRA_CONNECT` when `VITE_SENTRY_DSN` is baked in.

## 3. Sessions and cookies

```sh
cd backend && go test ./internal/auth/ -run 'Cookie|TOTP|Recovery' -v
```

Pass: session cookie is `HttpOnly`, `Secure` in production, `SameSite=Lax`;
unsafe requests with a foreign `Origin` are rejected; TOTP time steps and
recovery codes are single-use.

## 4. Rate limits

```sh
cd backend && go test ./internal/config/ -run RateLimit -v
```

Pass: defaults are public 60/min burst 20 and API key 600/min burst 60; `0` or
malformed values fail startup. Production `.env` leaves all four variables
unset.

## 5. SSRF and probe policy

```sh
cd backend && go test ./internal/probe/ -v
```

Manual check on a staging workspace: creating a monitor for `http://127.0.0.1`
or a private address is rejected, a redirect to a private address is blocked,
and a cross-host redirect drops user headers.

## 6. Alert secrets and delivery

- Create a webhook channel with a signing secret, then reload the page: the
  secret must show as configured, never in plaintext.
- `docker exec -it o1-postgres-1 psql -U o1 -d o1 -c "SELECT config FROM alert_channels"`:
  values are ciphertext, not URLs or tokens.
- Delivery rows and logs contain no secret; a failed send records a class, not
  the request body.

## 7. Audit trail

Create a monitor and a failed login, then read `/api/v1/admin/security-events`
as a global admin. Pass: the monitor event carries workspace, actor, and (when
a key was used) the key; the login event carries only IP and a redacted reason,
never the email or password.

## 8. Containers

```sh
docker inspect o1-web-1 --format '{{.Config.User}} caps={{.HostConfig.CapAdd}}'
```

Pass: `1000:1000` and `[CAP_NET_BIND_SERVICE]`. The API, scheduler, and worker
images are distroless and run as `nonroot`.

## 9. Data lifecycle

- `O1_RETENTION_DRY_RUN` defaults to `true`; confirm the scheduler logs the
  candidates it would drop and changes nothing.
- Before enabling deletion, confirm the managed database has automated backups
  and point-in-time recovery. Rollups are aggregates, not a backup.
- Confirm `rollup_state.watermark` advances and hourly counts match raw counts
  for a completed partition.

## 10. Deploy path

- `bash deploy/scripts/test-workflows.sh`, `test-deploy.sh`,
  `test-compose-config.sh`, `test-bootstrap.sh` all pass.
- Images are digest-pinned; `deploy.sh` rejects a tag.
- The production Environment still requires reviewers; the tag commit is an
  ancestor of `main`.
- The unprivileged-Caddy volume migration has been applied on hosts created
  before the non-root release (see `../deploy/production/README.md`).

## 11. Load baseline

Re-run `deploy/loadtest/*.js` when a read path or the host shape changes, and
update `load-test.md` with the new numbers and the first bottleneck.
