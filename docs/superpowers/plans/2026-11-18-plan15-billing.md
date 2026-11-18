# Plan 15 — Billing and Plans Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn o1 into a self-serve product with plans, quotas, a sandbox checkout, webhook-driven subscription state, and a scheduled expiry pass — with no card data ever touching the server.

**Architecture:** A `billing` package owns plans, subscriptions, entitlements, and a `Provider` interface. Plan 15 ships one provider (`sandbox`) that reuses the same event processor as a real webhook; plan changes are written in one transaction with the audit row and the `workspaces.monitor_limit` cache. The scheduler advances time-based states hourly.

**Tech Stack:** Go 1.26.11, PostgreSQL 16, sqlc v1.31.1, goose migrations, River/scheduler, React 19 + TanStack Query, Vitest/MSW, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-18-plan15-billing-design.md`

## Global Constraints

- Plans: free 5 monitors / 3 channels / 2 keys; pro 50/15/10; team 200/30/20.
- Absolute caps stay: 30 channels total (10 per kind) and 20 active API keys.
- `O1_BILLING_PROVIDER` accepts only `sandbox` in Plan 15; any other value fails startup with `not implemented yet`.
- Reads are never blocked by billing state. Only **new resource creation** is blocked when `past_due` > 7 days or `expired`.
- No paid→paid change, no invoices, no provider portal, no reconciliation in v1.
- Webhook processing and subscription state changes happen in one transaction, including the audit insert.
- No card data or provider secret reaches the browser.
- Sandbox routes return `404` when a real provider is configured.

## Review Focus

1. A duplicate or out-of-order webhook must not double-apply or regress a newer state — test the unique key and the period-end guard.
2. A processing failure must roll back the event row so the provider retry can reprocess — test the 500 path.
3. Restricted workspaces must still edit, delete, pause, revoke keys, and manage billing — test each service, not just creation.
4. Plan limits must replace the hard-coded alert/key caps, not stack a second layer — test free/pro/team values.
5. Sandbox endpoints must disappear when a provider secret is configured — test the 404 and the startup refusal.

---

### Task 1: Billing schema and generated queries

**Files:**
- Create: `backend/internal/db/migrations/00010_billing.sql`
- Create: `backend/internal/db/queries/billing.sql`
- Modify: `backend/sqlc.yaml` only if new identifiers need renaming
- Generated: `backend/internal/db/sqlc/billing.sql.go`, `models.go`
- Test: `backend/internal/billing/schema_test.go`

**Interfaces:**
- Produces tables `plans`, `subscriptions`, `billing_events`; queries `GetPlan`, `ListPlans`, `GetSubscription`, `UpsertSubscription`, `InsertBillingEvent`, `MarkBillingEventProcessed`, `GetBillingEvent`, `UpdateWorkspaceMonitorLimit`, `ListExpiredSubscriptions`.

- [ ] **Step 1: Write the failing schema test**

`internal/billing/schema_test.go` (integration, `testutil.NewPostgres`):

```go
func TestBillingSchemaBackfillsFreeSubscriptions(t *testing.T) {
	pool, _ := testutil.NewPostgres(t)
	ctx := context.Background()

	userID := testutil.CreateVerifiedUser(t, pool, "billing@example.com", "correct horse battery staple")
	wsID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id,name,slug,created_by) VALUES ($1,'Billing',$2,$3)`, wsID, "billing", userID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	var plans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM plans`).Scan(&plans); err != nil || plans != 3 {
		t.Fatalf("plans = %d (err %v), want 3", plans, err)
	}
	var plan, status string
	if err := pool.QueryRow(ctx,
		`SELECT plan_key, status FROM subscriptions WHERE workspace_id = $1`, wsID).Scan(&plan, &status); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if plan != "free" || status != "active" {
		t.Fatalf("subscription = %s/%s, want free/active", plan, status)
	}
	var limit int
	if err := pool.QueryRow(ctx, `SELECT monitor_limit FROM workspaces WHERE id = $1`, wsID).Scan(&limit); err != nil || limit != 5 {
		t.Fatalf("monitor_limit = %d, want 5", limit)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && go test -count=1 ./internal/billing/ -run TestBillingSchema`
Expected: relation `plans` does not exist.

- [ ] **Step 3: Write the migration**

`00010_billing.sql` (goose `-- +goose Up` / `Down`): create `plans`, `subscriptions`, `billing_events` exactly as in spec §5, with:

```sql
CREATE TABLE plans (
  key text PRIMARY KEY,
  name text NOT NULL,
  monitor_limit integer NOT NULL CHECK (monitor_limit > 0),
  channel_limit integer NOT NULL CHECK (channel_limit > 0),
  api_key_limit integer NOT NULL CHECK (api_key_limit > 0),
  price_cents integer NOT NULL CHECK (price_cents >= 0),
  currency text NOT NULL DEFAULT 'usd',
  trial_days integer NOT NULL DEFAULT 0 CHECK (trial_days >= 0)
);

INSERT INTO plans (key,name,monitor_limit,channel_limit,api_key_limit,price_cents,currency,trial_days) VALUES
 ('free','Free',5,3,2,0,'usd',0),
 ('pro','Pro',50,15,10,1200,'usd',14),
 ('team','Team',200,30,20,3900,'usd',14);

CREATE TABLE subscriptions (
  workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  provider text NOT NULL DEFAULT 'internal',
  provider_customer_id text,
  provider_subscription_id text UNIQUE,
  plan_key text NOT NULL REFERENCES plans(key),
  status text NOT NULL CHECK (status IN ('trialing','active','past_due','canceled','expired')),
  current_period_end timestamptz,
  trial_end timestamptz,
  past_due_since timestamptz,
  cancel_at_period_end boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO subscriptions (workspace_id, provider, plan_key, status)
SELECT id, 'internal', 'free', 'active' FROM workspaces
ON CONFLICT (workspace_id) DO NOTHING;

-- Every future workspace starts on Free, including ones created by tests or
-- the admin API, so no code path can forget the subscription row.
CREATE FUNCTION create_default_subscription() RETURNS trigger AS $$
BEGIN
  INSERT INTO subscriptions (workspace_id, provider, plan_key, status)
  VALUES (NEW.id, 'internal', 'free', 'active')
  ON CONFLICT (workspace_id) DO NOTHING;
  RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER workspaces_default_subscription
AFTER INSERT ON workspaces
FOR EACH ROW EXECUTE FUNCTION create_default_subscription();

CREATE TABLE billing_events (
  id uuid PRIMARY KEY,
  provider text NOT NULL,
  provider_event_id text NOT NULL,
  kind text NOT NULL,
  payload jsonb NOT NULL,
  processed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_event_id)
);
```

- [ ] **Step 4: Add the queries and regenerate**

`queries/billing.sql` implements the interface list above. Run `cd backend && sqlc generate`.
Expected: generated file compiles; `git diff --stat` shows only billing-related files.

- [ ] **Step 5: Verify and commit**

Run: `go test -count=1 ./internal/billing/ -run TestBillingSchema && go build ./...`
Expected: PASS.

```sh
git add backend/internal/db backend/sqlc.yaml backend/internal/billing/schema_test.go
git commit -m "feat: add the billing schema and plans"
```

---

### Task 2: Entitlements replace the hard-coded caps

**Files:**
- Create: `backend/internal/billing/limits.go`
- Modify: `backend/internal/alerts/service.go` (channel caps), `backend/internal/apikeys/service.go` (key cap)
- Modify: `backend/internal/app` / `backend/internal/api/router.go` wiring
- Test: `backend/internal/billing/limits_test.go`, existing alerts/apikeys service tests

**Interfaces:**
- Produces:
  ```go
  type Limits struct { Monitors, Channels, APIKeys int }
  type LimitSource interface {
      Limits(ctx context.Context, workspaceID uuid.UUID) (Limits, error)
  }
  func (s *Service) Limits(ctx context.Context, workspaceID uuid.UUID) (Limits, error)
  ```
- `alerts.New` and `apikeys.New` gain a `LimitSource` dependency; `nil` keeps today's caps.

- [ ] **Step 1: Write failing limit tests**

`limits_test.go`: create a workspace with the free plan, assert `Limits` = `{5,3,2}`; switch its subscription to `pro` in SQL and assert `{50,15,10}`; an unknown plan falls back to free. In `alerts/service_test.go`, a free workspace can create 3 channels and the 4th returns `ErrLimit`; pro allows 15 but the absolute cap stops at 10 per kind. In `apikeys/service_test.go`, free tops out at 2 active keys.

- [ ] **Step 2: Run to verify red**

Run: `cd backend && go test ./internal/billing/ ./internal/alerts/ ./internal/apikeys/ -run 'Limits|Limit'`
Expected: compile failure / cap assertion failures.

- [ ] **Step 3: Implement**

`Service.Limits` reads `subscriptions` joined to `plans`, clamps to the absolute caps (`min(plan.Channels, 30)`, `min(plan.APIKeys, 20)`), and falls back to free on no row. Services call it only when creating (not editing/deleting). Error mapping: `402 payment_required` at the handler layer.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/billing/ ./internal/alerts/ ./internal/apikeys/ && go test -short ./...`
Expected: PASS.

```sh
git add backend/internal/billing backend/internal/alerts backend/internal/apikeys backend/internal/app backend/internal/api
git commit -m "feat: enforce plan limits on channels and api keys"
```

---

### Task 3: Billing service, sandbox provider, and state machine

**Files:**
- Create: `backend/internal/billing/provider.go`, `sandbox.go`, `service.go`, `state.go`
- Test: `backend/internal/billing/service_test.go`, `sandbox_test.go`

**Interfaces:**
- ```go
  type Provider interface {
      Name() string
      CreateCheckout(ctx context.Context, in CheckoutInput) (CheckoutSession, error)
      ParseWebhook(r *http.Request) (Event, error)
  }
  type CheckoutInput struct { WorkspaceID uuid.UUID; PlanKey string; AdminEmail string }
  type CheckoutSession struct { URL string }
  type Event struct {
      ID, Kind string
      ProviderSubscriptionID, ProviderCustomerID, WorkspaceID string
      PlanKey, Status string
      CurrentPeriodEnd, TrialEnd *time.Time
      CancelAtPeriodEnd *bool
  }
  ```
- `Service.Checkout(ctx, actorID, workspaceID, planKey) (CheckoutSession, error)`
- `Service.ApplyEvent(ctx, provider string, ev Event) error` (one transaction: event insert, state, limit cache, audit)
- `Service.Cancel(ctx, actorID, workspaceID) error`
- `Service.AdminSetPlan(ctx, actorID, workspaceID, planKey) error`
- `Service.Get(ctx, actorID, workspaceID) (View, error)`

- [ ] **Step 1: Write failing service tests**

Cover: free → pro checkout creates a `trialing` subscription when the plan has `trial_days > 0`; duplicate `ApplyEvent` with the same id is a no-op; an event with an older `CurrentPeriodEnd` is ignored; `Cancel` sets `cancel_at_period_end`; `AdminSetPlan("team")` updates the subscription and `workspaces.monitor_limit`; checkout on an already-paid workspace returns `ErrAlreadySubscribed`; audit rows exist for plan change and cancel.

- [ ] **Step 2: Run to verify red**

Run: `cd backend && go test -count=1 ./internal/billing/ -run 'Service|Sandbox'`
Expected: undefined types.

- [ ] **Step 3: Implement service and sandbox**

Sandbox never leaves the process: `CreateCheckout` returns `/billing/sandbox?token=<signed>` where the token is HMAC over `workspace|plan|exp` with `O1_BILLING_WEBHOOK_SECRET`; `ParseWebhook` verifies the same HMAC and decodes the event JSON. State transitions live in `state.go` as pure functions so the scheduler and webhook path share them.

- [ ] **Step 4: Verify and commit**

Run: `go test -count=1 ./internal/billing/`
Expected: PASS.

```sh
git add backend/internal/billing
git commit -m "feat: add the billing service and sandbox provider"
```

---

### Task 4: Webhook endpoint and config

**Files:**
- Create: `backend/internal/billing/handlers.go`
- Modify: `backend/internal/config/config.go`, `backend/internal/api/router.go`
- Test: `backend/internal/billing/handlers_test.go`, `backend/internal/config/config_test.go`

**Interfaces:**
- Config: `BillingProvider string` (default `sandbox`), `BillingWebhookSecret string` (required outside development; in development defaults to a derived value), `BillingSandboxEnabled bool`.
- `Service.ApplyEvent` consumed by `POST /api/v1/billing/webhook/{provider}`.

- [ ] **Step 1: Failing tests**

Handler tests: valid signed sandbox event → 200 and state applied; duplicate → 200 and one side effect; bad signature → 400; unknown provider → 404; a `sandbox` provider value with a real provider configured → startup/subscription rejection. Config tests: `O1_BILLING_PROVIDER=lemonsqueezy` fails with `not implemented yet`; `sandbox` requires no key; production rejects an empty webhook secret.

- [ ] **Step 2: Verify red**

Run: `cd backend && go test ./internal/billing/ ./internal/config/ -run 'Webhook|Billing'`
Expected: failures/undefined.

- [ ] **Step 3: Implement**

Route sits outside session auth, is IP rate limited, and returns problem+json errors. Sandbox secret: `O1_BILLING_WEBHOOK_SECRET` with a development default derived from `O1_SECRET_KEY`; production requires it explicitly.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/billing/ ./internal/config/ && go test -short ./...`
Expected: PASS.

```sh
git add backend/internal/billing backend/internal/config backend/internal/api
git commit -m "feat: add the billing webhook and provider config"
```

---

### Task 5: Scheduled expiry pass

**Files:**
- Create: `backend/internal/billing/maintenance.go`
- Modify: `backend/cmd/scheduler/main.go`
- Test: `backend/internal/billing/maintenance_test.go`

**Interfaces:**
- `func (s *Service) RunMaintenance(ctx context.Context) (Result, error)` where `Result{Expired, TrialEnded, PastDueExpired int}`.
- Called once per scheduler tick after rollups/retention.

- [ ] **Step 1: Failing tests**

Freeze `now` via `Service.now`. Assert: `trialing` past `trial_end` → `active` on free limits (`monitor_limit=5`); `past_due` with `past_due_since` 8 days ago → `expired`; `canceled` past `current_period_end` → `expired`; an `active` subscription is untouched. Each transition writes an audit row.

- [ ] **Step 2: Verify red**

Run: `go test -count=1 ./internal/billing/ -run Maintenance`
Expected: undefined.

- [ ] **Step 3: Implement and wire**

Add the pass after `SetMaintenance` in `cmd/scheduler/main.go`. It runs hourly, guarded by `pg_try_advisory_lock` (`0x6f310004`), and is idempotent.

- [ ] **Step 4: Verify and commit**

Run: `go test -count=1 ./internal/billing/ && go build ./...`
Expected: PASS.

```sh
git add backend/internal/billing backend/cmd/scheduler
git commit -m "feat: expire trials and past-due subscriptions"
```

---

### Task 6: API routes and authorization

**Files:**
- Modify: `backend/internal/api/router.go`, `backend/internal/billing/handlers.go`
- Test: `backend/internal/billing/handlers_test.go`, `backend/internal/api/router_test.go`

**Interfaces:**
- Routes:
  - `GET /api/v1/plans` — anonymous.
  - `GET /api/v1/workspaces/{id}/billing` — viewer.
  - `POST /api/v1/workspaces/{id}/billing/checkout` — workspace admin.
  - `POST /api/v1/workspaces/{id}/billing/cancel` — workspace admin.
  - `POST /api/v1/workspaces/{id}/billing/sandbox/complete` — workspace admin, sandbox only.
  - `PUT /api/v1/admin/workspaces/{id}/plan` — global admin.
  - `POST /api/v1/billing/webhook/{provider}` — signature only.

- [ ] **Step 1: Failing route tests**

Viewer can read billing and cannot checkout/cancel (403); cross-tenant billing read returns 404; sandbox complete returns 404 when `O1_BILLING_PROVIDER` is not `sandbox`; global admin plan override changes the plan and writes an audit event; a monitor create over the free limit returns `402 payment_required` with the plan name.

- [ ] **Step 2: Verify red**

Run: `cd backend && go test ./internal/api/ ./internal/billing/ -run 'Billing|Plan'`
Expected: 404/403 mismatches.

- [ ] **Step 3: Implement handlers and wiring**

Handlers use `workspaces.Authorize` (admin for mutations, viewer for read) and the existing problem+json helpers. Add the routes to `router_test.go`'s protected-route table.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/api/ ./internal/billing/ && go test -short ./...`
Expected: PASS.

```sh
git add backend/internal/api backend/internal/billing
git commit -m "feat: expose the billing API"
```

---

### Task 7: Dashboard billing screen and sandbox checkout

**Files:**
- Create: `web/src/features/billing/{api.ts,hooks.ts,types.ts,BillingScreen.tsx,SandboxCheckoutScreen.tsx}`
- Modify: `web/src/router.tsx`, `web/src/components/layout/Sidebar.tsx`, monitor create error handling
- Test: `web/src/features/billing/BillingScreen.test.tsx`, `SandboxCheckoutScreen.test.tsx`, `web/src/router.test.tsx`

**Interfaces:**
- `GET /plans`, `GET/POST billing` per Task 6; `useBilling(workspaceId)`, `useCheckout()`, `useCancel()`.

- [ ] **Step 1: Failing UI tests**

MSW-backed tests: free workspace shows "Free" with an Upgrade button; clicking Upgrade posts checkout and navigates to the sandbox URL; the sandbox screen completes and invalidates billing; `402` on monitor create shows the plan name and an upgrade link; viewer does not see the billing nav entry.

- [ ] **Step 2: Verify red**

Run: `cd web && npm test -- --run src/features/billing`
Expected: module not found.

- [ ] **Step 3: Implement**

Route `/w/$workspaceSlug/settings/billing` guarded by admin (same pattern as alerts). Show plan, status, dates, usage counters, upgrade cards, and a "sandbox" badge when the provider mode says so. Add the nav item next to Settings.

- [ ] **Step 4: Verify and commit**

Run: `cd web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run`
Expected: PASS.

```sh
git add web/src
git commit -m "feat: add the billing screen and sandbox checkout"
```

---

### Task 8: Demo data, docs, and configuration

**Files:**
- Modify: `backend/internal/demo/seed.go` (demo workspace on `team`)
- Modify: `README.md`, `deploy/production/env.example`, `PORTFOLIO.md`, `deploy/production/README.md`
- Test: `backend/internal/demo/seed_test.go` (subscription assertion)

**Interfaces:**
- Demo seed sets `subscriptions.plan_key = 'team'`, status `active`, and `workspaces.monitor_limit = 200` in the same transaction.
- Env docs: `O1_BILLING_PROVIDER`, `O1_BILLING_WEBHOOK_SECRET`, plan price ids are unused in sandbox.

- [ ] **Step 1: Failing test**

Extend `TestSeedCreatesReusableDemoWorkspace`: after seed, the demo workspace's subscription plan is `team` and `monitor_limit` is 200.

- [ ] **Step 2: Verify red**

Run: `cd backend && go test ./internal/demo/ -run TestSeed`
Expected: assertion failure (free/5).

- [ ] **Step 3: Implement and document**

Seed update, README "Billing and plans" section, env example block, PORTFOLIO one paragraph. Note sandbox mode and that no real money moves.

- [ ] **Step 4: Verify and commit**

Run: `cd backend && go test -count=1 ./internal/demo/`
Expected: PASS.

```sh
git add backend/internal/demo README.md deploy/production/env.example PORTFOLIO.md deploy/production/README.md
git commit -m "docs: document billing and seed the demo plan"
```

---

### Task 9: End-to-end and final gate

**Files:**
- Modify: `web/tests/e2e/smoke.spec.ts` (billing flow)
- Modify: `README.md` development checks if needed

- [ ] **Step 1: Add the Playwright flow**

Owner logs in, opens Billing, upgrades to Pro via sandbox, sees the plan badge and the new monitor limit, cancels, and confirms `cancel_at_period_end` in the UI. A viewer cannot see the nav item.

- [ ] **Step 2: Run the whole gate**

```sh
cd backend && gofmt -l . && go vet ./... && go test -short ./... && golangci-lint run ./...
cd ../web && npm run format:check && npm run lint && npm run typecheck && npm test -- --run
cd .. && bash deploy/scripts/test-workflows.sh && docker compose config >/dev/null
```
Expected: all exit 0.

- [ ] **Step 3: Run integration and e2e**

```sh
cd backend && go test -count=1 ./internal/billing/ ./internal/demo/ ./internal/api/
docker compose up -d --build
cd web && npx playwright test
```
Expected: PASS.

- [ ] **Step 4: Commit**

```sh
git add web/tests/e2e README.md
git commit -m "test: cover the billing flow end to end"
```
