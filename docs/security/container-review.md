# Container Review — Caddy Base Image and Scan Exceptions

**Reviewed:** 2026-11-18
**Images:** `caddy:2-alpine` resolved to **v2.11.9** (`caddy build-info` reports
Go 1.27.3 and `golang.org/x/net v0.60.0`) and `o1-web` built from
`web/Dockerfile`.
**Next review:** 2027-01-12, or when `caddy:2-alpine` changes its dependency
set, whichever comes first.

## Findings

### DS-0002 — resolved

The exception said the web image had to run as root to bind 80/443. That is no
longer true:

- `caddy:2-alpine` ships `/data` and `/config` as mode `1777`, so an arbitrary
  uid can write its autosave file, instance UUID, and certificate storage.
  Verified by running the image as `1000:1000` with fresh named volumes: the
  process wrote `/config/caddy/autosave.json` and `/data/caddy/*` and served
  `/login` with status 200.
- Docker grants `NET_BIND_SERVICE` to a non-root container when the capability
  is added. Verified with `--user 1000:1000 --cap-add NET_BIND_SERVICE` on
  container port 80: Caddy started and served requests.
- `web/Dockerfile` now ends with `USER 1000:1000`, and both Compose files add
  `cap_add: [NET_BIND_SERVICE]` to the web service.

The `DS-0002` entry is removed from `.trivyignore`; `trivy fs --scanners
secret,misconfig --severity HIGH,CRITICAL` reports zero findings.

**Migration for an existing host.** Volumes created by a root-running Caddy
contain root-owned certificate files (`0600`), which a non-root Caddy cannot
read. Before deploying the first non-root release, while the old stack is still
running:

```sh
docker run --rm \
  -v o1-production_caddy_data:/data \
  -v o1-production_caddy_config:/config \
  alpine chown -R 1000:1000 /data /config
```

Adjust the volume prefix if `O1_COMPOSE_PROJECT` changes the Compose project
name. Without this step Caddy starts but re-requests certificates, which risks
ACME rate limits.

### CVE-2026-78661 — accepted, unchanged

`caddy:2-alpine` v2.11.9 still links `golang.org/x/net v0.60.0`; the fix ships
in v0.62.0 and Caddy has not released with it yet. The Caddyfile enables no
`templates` directive and parses no request bodies as HTML, so the reported
`x/net/html` denial of service is not reachable in this deployment. The
exception stays in `.trivyignore` with the review date moved to 2027-01-12 and
the observed versions recorded.

## Verification commands

```sh
docker pull caddy:2-alpine
docker run --rm --entrypoint caddy caddy:2-alpine version
docker run --rm --entrypoint caddy caddy:2-alpine build-info | grep 'golang.org/x/net'
docker run --rm -v "$PWD:/repo" -w /repo aquasec/trivy:0.76.2 fs \
  --scanners secret,misconfig --severity HIGH,CRITICAL --exit-code 1 .
```
