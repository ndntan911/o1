# Plan 15 — Billing, Plans, and Entitlements Design

**Status:** Draft for approval
**Date:** 2026-11-18
**Depends on:** Plans 1–14 (auth, workspaces, monitoring, alerts, status pages, security pass)
**Scope owner:** o1 uptime monitor

## 1. Intent

o1 is a working product with a live deployment, but every workspace is free and
every limit is a hard-coded constant. There is no way to charge for it, no plan
distinction, and no self-serve upgrade. That is the missing half of a SaaS
story: the code is credible, the business model is not.

Plan 15 adds **self-serve subscriptions and plan entitlements**: a pricing page,
provider-hosted checkout, webhook-driven subscription state, a 14-day Pro
trial, quota enforcement in the services, a billing screen, and a sandbox
provider so the live demo can walk the whole flow without a real card.

Success condition: a visitor can open the dashboard, pick Pro, complete a
sandbox checkout, see the plan change, hit a quota, upgrade, and cancel — with
state driven by webhooks and a scheduled expiry pass, never by the success
redirect.

## 2. Current state

- Workspaces have `monitor_limit` (default 5). Monitor creation enforces it;
  there is no admin endpoint that changes it.
- Alert channels and API keys have **hard-coded caps**: 10 per kind / 30 total
  channels (`alerts/service.go`) and 20 active keys (`apikeys/service.go`).
- There is no billing table, pricing page, checkout, webhook endpoint, or
  scheduled billing pass.
- CSRF is global and only rejects state-changing requests with a non-allowed
  `Origin`; provider webhooks send no `Origin`, so they pass the check. There
  is no per-route exclusion mechanism.
- Audit writes use a best-effort helper outside caller transactions; billing
  needs a transactional insert instead.
- The maintainer is based in Vietnam. Stripe does not support VN entities for
  receiving payments; merchant-of-record providers (Paddle, Lemon Squeezy) do.
- Auth, tenant isolation, audit events, the admin console, and the scheduler all
  exist and are the integration points.

## 3. Goals

1. Three plans with explicit entitlements: Free, Pro, Team. Billing entity is
   the **workspace**.
2. Every workspace has a subscription row; Free is `provider = 'internal'`,
   `plan = 'free'`, status `active`. Migration backfills all workspaces.
3. `workspaces.monitor_limit` becomes a derived cache of the plan. A plan change
   updates it in the same transaction. Alert and API-key caps read the plan.
4. Provider-hosted checkout: card data never touches o1.
5. Webhooks are the source of truth, idempotent by `(provider, event_id)`, and
   processed in the **same transaction** as the state change.
6. A 14-day local trial on Pro, once per workspace, no card, starting when the
   checkout is completed. A scheduler pass advances trial end, `past_due`
   expiry, and cancellation at period end.
7. Restriction semantics are explicit: reads always work; only **creation** is
   blocked when restricted. Editing, deleting, pausing, revoking keys, and
   managing billing remain available.
8. A billing screen: plan, status, renewal/trial end, usage, upgrade, cancel,
   and a visible "sandbox" badge when no real provider is configured.
9. Audit events for plan changes, cancellation, and failed payments, written
   inside the billing transaction.
10. One email to workspace admins on `payment_failed`, using the existing
    sender.

**Non-goals**

- Card forms or any PAN handling in o1.
- Paid→paid plan changes in v1: checkout rejects a workspace that already has a
  paid plan; cancel first, then subscribe. The `Provider` interface therefore
  has no change-plan method.
- Invoices in v1: no invoice list or links. The billing screen links to the
  provider dashboard only when a real provider is configured (follow-up).
- Usage-based billing, seats, annual plans, coupons, referrals, tax handling
  beyond the merchant of record, regional gateways.
- Reconciliation against the provider API. A missed webhook is fixed by a
  provider retry or an audited admin plan change; a reconciliation job is a
  follow-up.
- Paywalling security features (2FA, audit log, SSRF protection).

**Anti-goals**

- Trusting `/billing/success` to activate a plan.
- Unverified webhooks, or processing an event twice.
- Locking reads or taking the public status page offline on payment failure.
- Duplicating quota logic between monitor, alert, and key services.
- A sandbox checkout that can grant a plan on a deployment with a real provider.

## 4. Approaches

**A. Hosted checkout + signed webhooks + scheduled expiry (chosen).** Smallest
PCI surface; exercises idempotency, a state machine, and time-driven state.

**B. Custom card form.** Rejected: PCI scope, tokenization work, no extra
portfolio signal.

**C. Manual invoicing only.** Rejected as the primary path; kept as an audited
admin override.

**Provider modes.** `O1_BILLING_PROVIDER` is a deployment setting:

- `sandbox` (the only mode Plan 15 ships): an in-app checkout page simulates
  success, failure, and cancellation and emits the same internal events.
  Requires an authenticated workspace admin; visibly labelled. Refuses to run
  if a provider secret is configured.
- `lemonsqueezy` / `paddle`: the interface and config fields exist, but the
  first real adapter is a follow-up once an account and keys are available.
  Startup rejects those values in Plan 15 with a clear "not implemented yet"
  error rather than half-working payments.

`billing.Provider` has `CreateCheckout`, `ParseWebhook`, and `Cancel`. The
adapter guarantees that the subscription object carries `workspace_id` in
metadata so an `updated` event that arrives before `created` can still be
mapped.

## 5. Plans and data model

Seeded plans (prices are display-only; the provider is the billing source):

| Plan | Monitors | Channels | API keys | Trial | Price |
|---|---|---|---|---|---|
| free | 5 | 3 | 2 | — | $0 |
| pro | 50 | 15 | 10 | 14 days | $12/mo |
| team | 200 | 30 | 20 | 14 days | $39/mo |

Absolute caps (30 channels, 20 keys) remain as hard ceilings; plan limits are
lower and enforced first.

Migration `00010_billing.sql`:

```text
plans
  key text pk, name text, monitor_limit int, channel_limit int,
  api_key_limit int, price_cents int, currency text, trial_days int

subscriptions
  workspace_id uuid pk references workspaces
  provider text not null              -- internal | sandbox | paddle | lemonsqueezy
  provider_customer_id text
  provider_subscription_id text unique
  plan_key text not null references plans (key)
  status text not null                -- trialing | active | past_due | canceled | expired
  current_period_end timestamptz
  trial_end timestamptz
  past_due_since timestamptz
  cancel_at_period_end boolean not null default false
  created_at, updated_at

billing_events
  id uuid pk, provider text not null, provider_event_id text not null,
  kind text not null, payload jsonb not null, processed_at timestamptz,
  created_at timestamptz not null default now(),
  unique (provider, provider_event_id)
```

No invoice table in v1. `workspaces.monitor_limit` stays as the cache used by
the existing monitor path.

## 6. Flows

**Checkout.** `POST /workspaces/{id}/billing/checkout {plan}` (workspace admin)
→ reject if the subscription is already paid → provider session with
`workspace_id` metadata → redirect URL. The success page polls `GET /billing`;
the redirect itself changes nothing.

**Webhook.** `POST /billing/webhook/{provider}` (no session) → verify signature
→ **one transaction**: insert `billing_events` (unique key fails on duplicate →
return 200), apply state change, update `workspaces.monitor_limit`, insert the
audit row → commit. A processing error rolls everything back and returns 500 so
the provider retries. Invalid signature → 400, payload not logged.

**Scheduled pass.** The scheduler runs a billing maintenance pass once per hour:
`trialing` past `trial_end` → `active` on Free limits; `past_due` older than 7
days (`past_due_since`) → `expired`; `canceled` past `current_period_end` →
`expired`. Each transition is audited. Stored status is authoritative between
passes.

**State machine.**

| State | Reads | Create | Edit/delete/pause/revoke | Billing actions |
|---|---|---|---|---|
| trialing, active | yes | yes | yes | yes |
| past_due ≤7 days | yes | yes + banner | yes | yes |
| past_due >7 days | yes | **no** | yes | yes |
| canceled (to period end) | yes | yes | yes | yes |
| expired | yes | **no** | yes | yes |

Over-limit workspaces keep existing resources; the UI marks them and the create
path returns `402 payment_required` with the plan needed.

**Trial.** Local to o1, once per workspace, Pro only, 14 days, no card, starts
when the checkout webhook lands (`status=trialing`, `trial_end=now+14d`). After
the trial, the workspace drops to Free limits; existing monitors remain.

**Cancel.** `cancel_at_period_end=true`; support stays until
`current_period_end`, then the scheduled pass expires it and applies Free
limits.

**Admin override.** Global administrators get
`PUT /api/v1/admin/workspaces/{id}/plan {plan_key}` for support and demo
recovery. It writes the subscription row, the limit cache, and an audit event in
one transaction. Webhooks for `internal`/manual plans are not accepted; the next
provider event for a paid subscription wins.

## 7. API and UI

- `GET /api/v1/plans` — public pricing data.
- `GET /api/v1/workspaces/{id}/billing` — plan, status, dates, limits, usage,
  provider mode.
- `POST .../billing/checkout` — `{plan: "pro"|"team"}`.
- `POST .../billing/cancel` — cancel at period end.
- `POST /api/v1/billing/webhook/{provider}` — signature only.
- Admin: `PUT /api/v1/admin/workspaces/{id}/plan`.
- Dashboard: **Settings → Billing**, a plan badge, and a quota message on
  monitor creation. Sandbox checkout page at `/billing/sandbox`.

## 8. Security

- Webhook signature verification per provider, timestamp tolerance, and replay
  rejection via the unique event key.
- The webhook is rate limited per IP. It has no session and no CSRF token; the
  signature is the authentication.
- Sandbox mode exists only when no provider secret is configured; it requires
  an admin session and is labelled in the UI and audit detail.
- Plan changes are admin-only and audited in the same transaction as the state
  change (direct `security_events` insert, not the best-effort helper).
- No card data or provider secret reaches the browser.

## 9. Verification

- Sandbox end-to-end: checkout → webhook → plan and limit change → over-limit
  `402` → cancel → scheduled expiry. Duplicate webhook returns 200 with no
  second side effect; failed processing returns 500 and rolls back.
- State-machine tests for every transition and for edits remaining allowed in
  restricted states.
- Plan limits replace the alert/key constants; tests cover all three services.
- Webhook tests: valid, invalid signature, stale timestamp, unknown event,
  event arriving before `created`.
- Playwright: pricing → sandbox checkout → badge/limit change; a second
  workspace stays Free; viewer cannot check out.
- The real adapter is unit-tested against recorded payloads and a fake HTTP
  server; no test needs a live provider.

## 10. Risks

- **Provider availability (VN).** Merchant of record first; sandbox keeps the
  demo honest until an account exists.
- **Missed webhook.** No reconciliation in v1; provider retries plus an audited
  admin override. Documented in the runbook.
- **Out-of-order events.** The processor upserts by provider subscription id and
  ignores an event whose period end is older than the stored one, except for
  cancellation and payment-failure events, which always apply.
- **Over-limit demo data.** The demo workspace is Team in the seed so the
  existing 5 monitors remain valid; downgrade behaviour is tested separately.
- **Sandbox confusion.** Labelled in the UI, audit, and `PORTFOLIO.md`.

## 11. Resolved decisions

- Plan 15 ships **sandbox only**, behind the `billing.Provider` interface. The
  first real merchant-of-record adapter (Lemon Squeezy preferred for its
  simpler HMAC webhooks, Paddle as the alternative) is a follow-up once an
  account and keys exist. This keeps the live demo honest and avoids shipping
  untested payment code.
- Invoices, portal links, and provider reconciliation are follow-ups, not v1.

## 12. Out of scope, tracked as follow-ups

- The first real merchant-of-record adapter (Lemon Squeezy or Paddle).
- Paid→paid plan changes with proration.
- Invoices/portal links and provider API reconciliation.
- The second provider adapter, annual plans, seats, usage-based pricing.
- Dunning sequences, coupons, referral credits.
