# Uptime & Incident Monitoring — Thiết kế hệ thống

- **Ngày:** 2026-11-04
- **Trạng thái:** Draft chờ duyệt
- **Phạm vi:** V1 — sản phẩm chạy production, một người xây
- **Stack:** React (TypeScript) + Go

## 1. Mục tiêu và lý do

Xây một dịch vụ giám sát uptime thật, chạy production: người dùng tạo monitor HTTP, hệ thống probe định kỳ, phát hiện downtime, mở incident, gửi cảnh báo, và công bố status page.

Đây là project portfolio fullstack React + Go. Yêu cầu không phải "demo trông đẹp" mà là **hệ thống đạt chuẩn production**: bảo mật, chịu lỗi, quan sát được, có test, CI/CD và deploy thật. Người dùng mục tiêu chưa được xác định; V1 không làm billing hay go-to-market.

## 2. Goals

- Multi-tenant: workspace, thành viên, phân quyền.
- Monitor HTTP(S) với interval, timeout, header, expected status; probe từ một region.
- Phát hiện sự cố theo ngưỡng xác nhận; incident có timeline; tự resolve khi hồi phục.
- Gửi cảnh báo qua email, webhook, Slack, Telegram; có retry và chống gửi trùng.
- Dashboard React: danh sách monitor, uptime/latency, incident, quản lý workspace.
- Public status page theo workspace.
- Nền tảng production: auth an toàn, rate limit, audit log, API key, observability, backup, CI/CD, test đầy đủ.
- Chịu tải mục tiêu: 1.000 monitor ở interval 60s (~17 probe/giây) trên một VPS; thiết kế sẵn để scale 10.000 monitor.

## 3. Non-goals (V1)

- Thanh toán, gói cước, invoice.
- Nhiều region probe (kiến trúc phải mở đường, nhưng V1 chỉ một region).
- Check TCP/UDP/ICMP, browser/synthetic check, cron/heartbeat check.
- Maintenance window và incident tạo tay. V1 có acknowledge incident.
- Custom domain cho status page (chỉ slug).
- Mobile app, SSO/SAML, OAuth social login (có thể thêm ở M4).
- On-call schedule, escalation policy, phone/SMS alert.
- AI/LLM features.

## 4. Anti-goals

- Không phải microservice hoá sớm. Nếu phải chọn giữa "khoe hệ phân tán" và "hoàn thành chỉn chu", chọn hoàn thành.
- Không tự viết thư viện queue, auth, hay chart. Dùng thư viện trưởng thành.
- Không thêm feature mới khi checklist production chưa xong.
- Không deploy lên VPS rồi bỏ đó: phải có backup, restore drill, alert cho chính hệ thống.

## 5. Ràng buộc và giả định

- Một người làm, part-time. Ước lượng 10–14 tuần.
- Ngân sách hạ tầng mục tiêu ≤ 30 USD/tháng.
- Deploy: **1 VPS chạy cả app và Postgres**, backup offsite ra S3-compatible storage. Đây là lựa chọn có chủ đích; rủi ro single point được chấp nhận và giảm thiểu (xem §8.6).
- Trình duyệt mục tiêu: Chrome/Firefox/Safari bản mới.
- Ngôn ngữ UI: tiếng Anh (có thể i18n sau). Tài liệu code: tiếng Anh.

## 6. Các hướng đã cân nhắc

### A. Modular monolith, Postgres-first — **đã chọn**
Một repo Go, ba binary (`api`, `scheduler`, `worker`) dùng chung package nội bộ; Postgres làm cả dữ liệu lẫn job queue (River); React SPA cùng domain; session cookie. Deploy Docker Compose trên VPS + Caddy.

- Ưu: ít mảnh ghép, giao dịch nhất quán, dễ vận hành và hoàn thành; vẫn đủ chiều sâu kỹ thuật.
- Nhược: chưa có multi-region; một VPS là single point (giảm thiểu bằng backup + restore drill + cảnh báo ngoài).

### B. Microservice từ đầu — loại
Tách `api`, `scheduler`, `prober`, `notifier`, queue NATS/Redis Streams. Mạnh về câu chuyện scale nhưng ops nặng, dễ dở dang.

### C. Hybrid monolith + prober tách region — loại ở V1
Giữ ranh giới để tách prober sau. Không làm ngay vì thêm hệ phân tán phải vận hành và debug.

**Quyết định:** A. Ranh giới code phải cho phép tách `prober`/`notifier` thành service riêng mà không viết lại nghiệp vụ.

## 7. Kiến trúc

### 7.1 Thành phần

```
Internet
   │
   ▼
[Caddy] ── TLS, static SPA, reverse proxy /api
   │
   ▼
[api]  (Go, HTTP)         [scheduler] (Go)        [worker] (Go)
   │                          │                      │
   │                          └── enqueue job ───────┤
   │                                                 ▼
   └──────────────────────► [Postgres 16] ◄──── River queue
                                │
                                ├── dữ liệu nghiệp vụ
                                └── probe_results (partition theo tháng)
```

- **api**: REST JSON, session auth, phục vụ SPA (qua Caddy), public status page, API key.
- **scheduler**: quét monitor đến hạn, enqueue job probe; không gọi mạng ra ngoài.
- **worker**: chạy probe, ghi kết quả, cập nhật state machine, mở/đóng incident, gửi alert qua River job riêng.
- **Postgres**: nguồn sự thật duy nhất; River dùng chung transaction với nghiệp vụ.
- **Redis**: không bắt buộc ở V1. Thêm sau cho rate limit phân tán/cache khi chạy nhiều instance.

Ba binary dùng chung module `internal/`. Chạy chung một image Docker, chọn mode bằng command.

### 7.2 Luồng probe

1. Scheduler mỗi 10s: `SELECT ... FROM monitor_state WHERE next_check_at <= now() FOR UPDATE SKIP LOCKED LIMIT 500`.
2. Với mỗi monitor, enqueue River job `probe` với unique key `probe:<monitor_id>:<scheduled_unix>` (idempotent), cập nhật `next_check_at = now() + interval + jitter(0–10%)`.
3. Worker nhận job, thực hiện HTTP request an toàn (mục 7.6), ghi `probe_results`.
4. Trong một transaction: cập nhật `monitor_state` (consecutive failures/successes, last status).
5. Nếu vượt ngưỡng: mở/đóng `incidents`, ghi `incident_events`, enqueue job `alert` cho từng channel đang bật.
6. Alert worker gửi, ghi `alert_deliveries`, retry exponential backoff, dedup theo `(incident_id, channel_id, event)`.

### 7.3 Incident

- Incident có trạng thái `open` → `acknowledged` → `resolved`.
- Hệ thống tự mở/đóng; người dùng chỉ có thể acknowledge.
- Acknowledge không dừng probe và không đóng incident; nó chỉ **tạm dừng alert lặp** cho incident đó (vẫn gửi alert resolve).
- `incidents` lưu `acknowledged_at`, `acknowledged_by`; `incident_events` ghi lại thao tác acknowledge kèm actor.
- Status page public hiển thị incident đang mở/acknowledged giống nhau (đều là "ongoing"); không lộ danh tính người acknowledge.

### 7.4 State machine của monitor

- `unknown` → `up` khi probe đầu tiên thành công.
- `unknown` → `down` khi `consecutive_failures >= confirmation_threshold` (monitor mới fail ngay từ đầu vẫn mở incident).
- `up` → `down` khi `consecutive_failures >= confirmation_threshold` (mặc định 2, cấu hình 1–5).
- `down` → `up` khi `consecutive_successes >= recovery_threshold` (mặc định 2, cấu hình 1–5).
- `paused`: dừng probe và alert; giữ nguyên state, counters và incident đang mở. Khi resume, probe ngay và tiếp tục counters (không reset) để tránh đóng/mở incident giả.
- Mỗi lần đổi trạng thái ghi `incident_events` + audit log.
- Latency chậm không tự mở incident ở V1; chỉ ghi nhận và hiển thị.

### 7.5 Multi-tenancy

- Mọi bảng nghiệp vụ có `workspace_id`; mọi query đều scope theo workspace lấy từ session/API key.
- Role: `owner`, `admin`, `member`, `viewer`. `viewer` chỉ đọc. Chỉ `owner` xoá workspace và quản lý billing (tương lai).
- Không dùng Postgres RLS ở V1 (thêm phức tạp); thay vào đó tập trung quyền ở tầng service + test cách ly tenant.
- Slug workspace và status page unique toàn hệ thống.

### 7.6 Probe an toàn (SSRF)

Bắt buộc trước khi ra production:

- Chỉ cho phép `http`/`https`, port 80/443 (cấu hình được cho port khác).
- Resolve DNS, từ chối loopback, private, link-local, multicast, ULA; kiểm tra lại IP tại thời điểm dial bằng `DialContext` tùy biến để chống DNS rebinding.
- Giới hạn redirect (tối đa 5), từ chối redirect tới địa chỉ nội bộ.
- Timeout tổng và timeout từng phase; giới hạn body đọc 1 MB; đóng body đúng cách.
- Giới hạn header/body người dùng nhập; không cho override `Host`; cho phép `Authorization` cho API check.
- User-Agent riêng; theo dõi IP nguồn để phát hiện lạm dụng.
- Giới hạn số monitor/workspace và tần suất tạo monitor.
- **Port:** V1 chỉ 80/443. Danh sách port cho phép là cấu hình toàn hệ thống (env), không phải setting từng monitor; UI chỉ hiện port mặc định của scheme.
- **Webhook alert cũng đi qua SSRF guard**: URL webhook do tenant nhập cũng bị chặn địa chỉ nội bộ. Hệ quả: không hỗ trợ webhook nội bộ/self-host trong V1 — ghi rõ trong UI và docs.
- **TLS:** chứng chỉ không hợp lệ được tính là probe fail; chưa có opt-out ở V1.
- **Điều kiện thành công:** status code nằm trong danh sách cho phép (mặc định `2xx`, có thể nhập danh sách hoặc range). Không kiểm tra nội dung body ở V1. Hỗ trợ method GET/HEAD/POST/PUT/PATCH/DELETE, body và content-type tuỳ chọn.

### 7.7 Auth

- Email + password. Hash bằng Argon2id; kiểm tra độ mạnh tối thiểu.
- Session server-side trong Postgres; cookie `HttpOnly`, `Secure`, `SameSite=Lax`; token lưu dạng hash; hết hạn trượt 30 ngày; thu hồi được.
- CSRF: kiểm tra `Origin`/`Sec-Fetch-Site` cho mọi request thay đổi trạng thái + double-submit token cho form nhạy cảm.
- Xác thực email, quên/đặt lại mật khẩu bằng token một lần dùng, có hết hạn.
- Rate limit đăng nhập/đăng ký/reset theo IP + email.
- 2FA TOTP ở M3 (bắt buộc cho owner workspace nếu bật).
- API key: prefix hiển thị, phần secret chỉ hiện một lần, lưu hash, scope theo workspace, có `last_used_at`.

### 7.8 Dữ liệu

Bảng chính:

- `users`, `sessions`, `email_verification_tokens`, `password_reset_tokens`
- `workspaces`, `workspace_members`, `invitations`
- `monitors`, `monitor_state`
- `probe_results` (partition theo tháng, giữ raw 90 ngày)
- `monitor_hourly_stats` (rollup: up_count, down_count, avg/p95 latency)
- `incidents`, `incident_events`
- `alert_channels`, `monitor_alert_channels`, `alert_deliveries`
- `status_pages`, `status_page_monitors`
- `api_keys`
- `audit_logs`

Quy ước: UUID v7 cho khoá chính; `timestamptz`; soft delete chỉ khi cần; index theo `(workspace_id, created_at)` và `(monitor_id, checked_at)`.

**Secret của tenant** (header/Authorization của monitor, URL webhook, token Slack/Telegram, SMTP credential): mã hoá tại tầng ứng dụng bằng AES-256-GCM, khoá từ env/secret manager, có key version để rotate. API response, log, audit log và backup đều phải redact; status page không bao giờ hiện URL/header của monitor. URL có query string được coi là có thể chứa token nên chỉ hiện rút gọn cho người ngoài workspace.

**Uptime contract:** uptime = số probe thành công / (thành công + thất bại) trong cửa sổ đang xem. Trạng thái `unknown`, `paused`, và khoảng trống do missed probe **không** tính là down; hiển thị là "no data" và loại khỏi mẫu số. Cửa sổ ≤ 7 ngày đọc raw; > 7 ngày đọc rollup giờ/ngày. Public status page hiển thị 90 ngày dạng daily bar.

Retention: raw probe 90 ngày, rollup 13 tháng, audit log 12 tháng; job dọn dẹp chạy hằng ngày.

### 7.9 API

- REST JSON, prefix `/api/v1`, OpenAPI 3.1 là nguồn sự thật.
- Lỗi chuẩn `application/problem+json` (RFC 9457) với `code`, `message`, `fields`.
- Phân trang cursor; idempotency key cho POST tạo tài nguyên quan trọng.
- Versioning: không phá vỡ trong `/v1`; thay đổi lớn mở `/v2`.
- Public status page dùng endpoint riêng, cache ngắn (10–30s), rate limit theo IP.

### 7.10 Frontend

- React 19 + TypeScript + Vite.
- TanStack Router + TanStack Query; form `react-hook-form` + `zod`.
- Tailwind CSS + shadcn/ui; chart dùng Recharts.
- Cấu trúc: `features/<domain>` (monitors, incidents, alerts, status-pages, settings), `components/ui`, `lib/api`.
- Optimistic update cho pause/resume; polling hoặc SSE cho trạng thái realtime (SSE ở M3).
- Accessibility: keyboard, focus ring, contrast; Lighthouse ≥ 90 ở các trang chính.
- Không tự viết design system từ đầu.

### 7.11 Chống lạm dụng (open signup)

Vì đăng ký công khai, các kiểm soát sau là **bắt buộc ngay M1**, không đợi đến hardening:

- Email phải verify trước khi được tạo monitor.
- Quota mặc định: 1 workspace cá nhân, 5 monitor, interval tối thiểu 60 giây; nâng bằng tay khi cần.
- Rate limit: tạo monitor 10/giờ/workspace; giới hạn tổng số probe/giây toàn hệ thống.
- Trang admin tối thiểu: xem workspace, suspend/unsuspend, xem monitor đáng ngờ.
- Ghi log mọi lần tạo monitor và URL đích (query string đã redact) để điều tra.
- SSRF guard phải xong **trước khi mở signup ra public**.
- Cảnh báo tự động khi một workspace tạo monitor dồn dập hoặc cố probe vào địa chỉ nội bộ (bị chặn).

## 8. Yêu cầu production

### 8.1 Bảo mật
- Security headers (CSP, HSTS, X-Content-Type-Options, Referrer-Policy), CORS chặt.
- Rate limit theo IP và workspace; giới hạn request body.
- Secrets qua env/secret manager, không commit; rotate được.
- Dependency scanning (govulncheck, npm audit, Dependabot), image scan (Trivy).
- Audit log cho hành động nhạy cảm: đăng nhập, đổi quyền, xoá monitor, tạo API key.
- Nguyên tắc: mọi input đều không tin cậy; mọi query đều có tenant scope.

### 8.2 Độ tin cậy
- Graceful shutdown cho cả ba binary; readiness/liveness endpoint.
- Migration có version, chạy trước khi deploy, không phá vỡ tương thích ngược.
- Backup Postgres hằng ngày + WAL archiving; **restore drill mỗi tháng** ghi lại kết quả.
- Idempotency cho probe job và alert job.
- Alert delivery: retry tối đa 5 lần trong 30 phút, sau đó đánh dấu failed và hiện trong UI.
- Self-monitoring: dead man's switch bên ngoài ping `/healthz`; nếu hệ thống chết, chủ dự án nhận được cảnh báo.
- Không mất dữ liệu khi worker restart giữa chừng.

### 8.3 Observability
- Log JSON có cấu trúc, kèm `request_id`, `workspace_id` (không log secret/PII).
- Prometheus metrics: request latency/error, queue depth, probe duration, alert success/failure, incidents open.
- OpenTelemetry tracing cho API và worker; sample hợp lý.
- Sentry cho lỗi backend và frontend.
- Dashboard + alert cho chính hệ thống (queue backlog, error rate, DB connections).
- **Nơi chạy:** dùng hosted free tier (Grafana Cloud cho metrics/logs/traces, Sentry cho errors) thay vì tự host Prometheus/Grafana trên VPS — giữ VPS cho app và nằm trong ngân sách. Nếu vượt free tier, hạ retention trước khi tăng chi phí.

### 8.4 Test
- Unit test cho state machine, SSRF guard, auth, tính toán uptime.
- Integration test với Postgres thật (testcontainers-go), gồm cách ly tenant.
- API contract test với OpenAPI.
- E2E Playwright: đăng ký → tạo monitor → nhận incident (dùng probe target giả) → resolve.
- Load test k6: 1.000 monitor/60s, kiểm tra p95 và queue backlog.
- `go test -race`, coverage mục tiêu ≥ 70% phần nghiệp vụ.
- Frontend: Vitest + Testing Library cho logic; Playwright cho luồng chính.

### 8.5 CI/CD
- GitHub Actions: lint (golangci-lint, eslint, tsc), test, build, scan, build image, push GHCR.
- Deploy: SSH + `docker compose pull && up -d`, chạy migration, health check, rollback image tag trước nếu fail.
- Branch protection; không deploy trực tiếp từ laptop.
- Môi trường: local (compose), staging, production.

### 8.6 Hạ tầng
- VPS (Hetzner CX22/CX32) + Caddy; **Postgres chạy cùng VPS với app** (lựa chọn đã chốt).
- Vì cùng VPS là single point: bắt buộc WAL archiving + base backup hằng ngày ra S3 offsite, restore drill hằng tháng, alert dung lượng đĩa/CPU/RAM, và runbook khôi phục rõ ràng.
- Docker Compose; image pin theo digest; healthcheck.
- Backup mã hoá đẩy ra S3-compatible storage; kiểm tra restore tự động.
- Domain + TLS tự động; DNS có TTL thấp để failover thủ công.
- Khi vượt ~1.000 monitor hoặc cần SLA cao hơn, chuyển Postgres sang managed — schema và query không phải đổi.

## 9. Phasing

### M1 — Nền tảng (tuần 1–4)
Repo, CI, docker compose local; auth (đăng ký/đăng nhập/verify/reset), workspace + member; monitor CRUD; scheduler + worker probe HTTP; **SSRF guard + quota + trang admin suspend**; dashboard danh sách monitor; migration + seed.

### M2 — Vòng lặp sự cố (tuần 5–7)
State machine, incident + timeline + acknowledge; alert email/webhook/Slack/Telegram; alert delivery + retry; public status page; test integration cho toàn luồng.

### M3 — Production hardening (tuần 8–11)
2FA; API key; audit log; observability (metrics/trace/Sentry); backup + restore drill; rate limit; load test; E2E; security review.

### M4 — Polish (tuần 12–14)
Chart uptime/latency, SSE realtime, invite flow hoàn chỉnh, docs (README, ADR, runbook, OpenAPI), demo seed, Lighthouse, landing page.

## 10. Tiêu chí "xong" của V1

- Deploy production, có domain, TLS, uptime ≥ 99.9% trong 2 tuần.
- 1.000 monitor chạy ổn định, p95 probe latency < 2s, không backlog.
- Restore từ backup thành công trong môi trường sạch.
- Alert đến trong < 60s từ lúc vượt ngưỡng (với channel hoạt động).
- Không có lỗ hổng mức high/critical từ scan.
- E2E và integration test xanh trong CI.
- README + runbook đủ để người khác vận hành.

## 11. Quyết định của người viết (có thể bác)

- **River thay vì Redis queue**: ít mảnh ghép, enqueue cùng transaction với nghiệp vụ. Đổi sang Redis Streams chỉ khi cần throughput rất cao.
- **Session cookie thay vì JWT**: thu hồi tức thì, đơn giản hơn cho SPA cùng domain.
- **Partition + rollup tự làm thay vì TimescaleDB**: giảm phụ thuộc; đủ cho quy mô mục tiêu.
- **Một region ở V1**: đủ để học và vận hành; multi-region để sau.
- **Tiếng Anh cho UI/code**: dễ dùng lại và phỏng vấn quốc tế.
- **Không billing ở V1**: chưa xác định người dùng, tránh scope chết.
- **Monitor mới fail ngay vẫn mở incident**: `unknown → down` theo cùng ngưỡng xác nhận, tránh monitor "câm".
- **Pause giữ nguyên state/counters/incident**, chỉ tạm dừng probe và alert.
- **Không cho port tuỳ ý ở V1**: chỉ 80/443, allowlist toàn hệ thống qua env; giảm bề mặt scan.
- **Webhook cũng bị SSRF guard**: đổi lại không hỗ trợ webhook nội bộ; ghi rõ trong docs.
- **Secret tenant mã hoá tại ứng dụng (AES-GCM)**, redact mọi nơi; chưa dùng KMS ở V1.
- **Uptime loại trừ no-data**, không coi missed probe là down; raw ≤ 7 ngày, rollup > 7 ngày.
- **Observability dùng hosted free tier** (Grafana Cloud + Sentry), không self-host trên VPS.
- **Open signup + bắt buộc verify email**, kèm quota/rate limit/admin suspend ngay M1 vì rủi ro lạm dụng prober.
- **Incident có acknowledge**, không có maintenance window hay incident tạo tay ở V1.
- **Postgres chạy cùng VPS** theo lựa chọn của bạn; bù lại bằng backup offsite + restore drill + alert tài nguyên, và có đường chuyển sang managed sau.

## 12. Điều còn để builder quyết

- Thư viện HTTP router (chi hay stdlib) và cách sinh OpenAPI.
- Có dùng `sqlc` hay viết query tay.
- Nhà cung cấp email (Resend/Postmark/SES) và template engine.
- Chọn VPS hay PaaS cho staging.
- Chi tiết UI (màu, layout) sau khi có wireframe.
