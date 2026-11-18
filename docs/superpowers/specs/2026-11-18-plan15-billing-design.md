# Plan 15 — Billing, Plans, and Entitlements Design

**Status:** Draft for approval
**Date:** 2026-11-18
**Depends on:** Plans 1–14 (auth, workspaces, monitoring, alerts, status pages, security pass)
**Scope owner:** o1 uptime monitor

## 1. Intent

o1 is a working product with a live deployment, but every workspace is free and
unlimited apart from the hard-coded `monitor_limit`. There is no way to charge
for it, no plan distinction, and no self-serve upgrade. That is the missing
half of a SaaS story: the code is credible, the business model is not.

Plan 15 adds **self-serve subscriptions and plan entitlements**: a pricing page,
provider-hosted checkout, webhook-driven subscription state, a 14-day Pro
trial, quota enforcement, a billing screen in the dashboard, and a sandbox
provider so the live demo can walk the whole flow without a real card.

Success condition: a visitor can open the dashboard, pick Pro, complete a
sandbox checkout, see the plan change, hit a quota, upgrade, and cancel — with
all state driven by webhooks, not by the success redirect.

## 2. Current state

- Workspaces have `monitor_limit` (default 5, admin-tunable) and no other
  entitlement. Monitor creation already enforces it.
- There is no billing table, no pricing page, no checkout, no webhook endpoint.
- Auth, tenant isolation, audit events, and the admin console are shipped and
  are the integration points.
- The live demo runs on one host with a managed PostgreSQL; a payment provider
  is not configured.
- The maintainer is based in Vietnam. Stripe does not support VN entities for
  receiving payments; merchant-of-record providers (Paddle, Lemon Squeezy) do.
  This shapes the provider choice and is not hidden in the design.

## 3. Goals

1. Plans with explicit entitlements: Free, Pro, Team. Billing entity is the
   **workspace** (each workspace has its own subscription).
2. Provider-hosted checkout: card data never touches o1.
3. Subscription state derived from signed webhooks and idempotent by provider
   event ID, with the success redirect used only for UX.
4. A 14-day trial that downgrades to Free when it ends, and a 7-day grace period
   for `past_due` before write restrictions.
5. Quota enforcement at the existing monitor-creation path, plus per-plan
   limits for alert channels and API keys.
6. A billing screen: current plan, renewal date, invoices (links), upgrade,
   cancel-at-period-end, and a prominent demo/sandbox badge when no real
   provider is configured.
7. Audit events for plan changes, cancellation, and failed payments.

**Non-goals**

- Card forms or any PAN handling in o1 (hosted checkout only).
- Usage-based/metered billing, seats, annual invoicing, POs, or tax handling
  beyond what the merchant-of-record does.
- Dunning email sequences beyond one `payment_failed` notification.
- Cryptocurrency, bank transfer, or regional gateways in v1.
- Multi-currency beyond the provider's checkout currency.
- Changing tenant isolation or the auth model.

**Anti-goals**

- Trusting the redirect (`/billing/success`) to activate a plan.
- Unverified webhooks, or processing the same event twice.
- Paywalling security features (2FA, audit log, SSRF protection).
- A billing failure that locks reads or takes the status page offline.
- A hidden "contact sales" dead end for the only paid path.

## 4. Approaches

**A. Hosted checkout + signed webhooks (chosen).** The server creates a
provider checkout session, redirects, and treats webhooks as the source of
truth. Smallest PCI surface, standard practice, and exercises real idempotency
and state-machine work.

**B. Custom card form.** Rejected: PCI scope, tokenization work, and no
portfolio value beyond what A already proves.

**C. Manual invoicing / admin-granted plans.** Rejected as the primary path: no
self-serve story, and the interesting code (webhooks, entitlements) disappears.
Kept as an admin escape hatch.

**Provider abstraction.** `billing.Provider` with `CreateCheckout`,
`ParseWebhook`, and `Cancel` is implemented by:

- `sandbox`: an in-app checkout page that simulates success/failure and emits
  the same internal events. Enabled when no provider key is configured, clearly
  labelled in the UI. This is what the live demo uses.
- `lemonsqueezy` (first real adapter) or `paddle`: merchant-of-record, supports
  VN sellers, handles VAT. One adapter ships in Plan 15; the other is a
  follow-up.
- `stripe` is intentionally not the default because of the VN constraint; the
  interface keeps it possible.

## 5. Data model

Migration `00010_billing.sql`.

```text
plans                  -- static, seeded by migration
  key text pk          -- free | pro | team
  name text
  monitor_limit int
  channel_limit int
  api_key_limit int
  price_cents int      -- display only; the provider is the source of truth
  currency text
  trial_days int

subscriptions
  workspace_id uuid pk references workspaces
  provider text not null              -- sandbox | lemonsqueezy | paddle | stripe
  provider_customer_id text
  provider_subscription_id text unique
  plan_key text not null references plans
  status text not null                -- trialing | active | past_due | canceled | expired
  current_period_end timestamptz
  trial_end timestamptz
  cancel_at_period_end boolean not null default false
  created_at, updated_at

billing_events
  id uuid pk
  provider text not null
  provider_event_id text not null
  kind text not null
  payload jsonb not null
  processed_at timestamptz
  created_at timestamptz
  unique (provider, provider_event_id)  -- idempotency key

billing_invoices         -- optional cache for the billing screen
  provider_invoice_id text pk
  workspace_id uuid not null
  amount_cents int, currency text, status text,
  hosted_url text, period_start timestamptz, period_end timestamptz
```

The `workspaces.monitor_limit` column stays and is **derived** from the plan:
a subscription change updates it in the same transaction. Quota checks keep
using the existing path, so enforcement is not duplicated.

## 6. Flows

**Checkout.** `POST /api/v1/workspaces/{id}/billing/checkout` (admin only) →
provider session with `workspace_id` in metadata → redirect URL. The success
page polls `GET /billing` until the webhook lands; it never activates anything.

**Webhook.** `POST /api/v1/billing/webhook/{provider}` (unauthenticated,
signature-verified) → verify → insert into `billing_events` (unique key) →
process in a transaction → update subscription + workspace limits + audit →
return 2xx. A duplicate event returns 2xx without side effects. Invalid
signature returns 400 and is logged without the payload.

**State machine.** `trialing → active → past_due → canceled/expired`, plus
`active ⇄ past_due` on payment recovery. Rules:

| State | Reads | Writes (monitor/alerts/keys) | Status page |
|---|---|---|---|
| trialing, active | yes | yes | yes |
| past_due (≤7 days) | yes | yes, with a banner | yes |
| past_due (>7 days) | yes | new resources blocked | yes |
| canceled (until period end) | yes | yes | yes |
| expired | yes | new resources blocked | yes |

Existing resources are never deleted on downgrade; limits apply to new
creation, and the UI marks over-limit workspaces.

**Trial.** Pro for 14 days on first checkout, no card required (sandbox and
provider trial where supported). Ending the trial moves to Free; the workspace
keeps its existing monitors but cannot add past the Free limit.

**Cancel.** Sets `cancel_at_period_end`; access continues to period end, then
`expired` and Free limits apply.

## 7. API and UI

- `GET /api/v1/plans` — public pricing data.
- `GET /api/v1/workspaces/{id}/billing` — plan, status, period end, limits,
  usage counts, provider mode.
- `POST .../billing/checkout` — `{plan: "pro"|"team"}`.
- `POST .../billing/portal` — provider customer portal when supported, else the
  in-app billing screen.
- `POST .../billing/cancel` — cancel at period end.
- `POST /api/v1/billing/webhook/{provider}` — no session; signature only.
- Dashboard: **Settings → Billing** page and a quota warning on monitor
  creation. Admin console: plan override for support (audited).

## 8. Security

- Webhook signatures verified with a per-provider secret; replay rejected by the
  unique `(provider, event_id)` key and a timestamp tolerance.
- No card data, no provider secret in the browser. Sandbox mode is explicit and
  server-side; a `sandbox` subscription cannot be created against a workspace
  that already has a real provider subscription.
- The webhook route is rate limited per IP and excluded from session/CSRF
  middleware, which is why signature verification is mandatory.
- Billing changes are admin-only within a workspace and audited
  (`plan_changed`, `subscription_canceled`, `payment_failed`).

## 9. Verification

- Fake/sandbox provider integration test: checkout → webhook → plan change →
  quota enforcement → cancel → expiry, including duplicate and out-of-order
  events.
- Postgres integration tests for the state machine and limits.
- Webhook signature tests: valid, invalid, stale timestamp, duplicate ID.
- Playwright: pricing page → sandbox checkout → plan badge and limit change →
  a second workspace stays Free.
- No test may depend on a live provider; the real adapter is unit-tested with a
  recorded payload and a fake HTTP server.

## 10. Risks

- **Provider availability (VN).** Merchant-of-record first; the sandbox keeps
  the demo honest if no account is ready. If a provider contract is required
  before shipping, the real adapter can lag without blocking the plan.
- **Webhook ordering.** `subscription.updated` can arrive before
  `subscription.created`; the processor upserts by provider subscription ID and
  ignores events older than the stored period end.
- **Quota bypass.** Enforcing only in the UI would be cosmetic; enforcement
  stays in the service layer, and the API tests cover it.
- **Demo confusion.** Sandbox checkouts are labelled in the UI and in
  `PORTFOLIO.md` so nobody thinks real money moved.

## 11. Open questions

1. Which merchant-of-record to target first: **Paddle** or **Lemon Squeezy**?
   Both support VN sellers; Paddle has stronger invoicing/VAT coverage, Lemon
   Squeezy is simpler to integrate. The adapter interface makes this reversible.
2. Should the live demo allow *any* visitor to run a sandbox checkout, or gate
   it to the demo viewer account? Proposal: any logged-in admin of a workspace,
   including the demo owner; the viewer account cannot change plans.
3. Is a provider customer portal needed in v1, or is in-app cancel enough?
   Proposal: in-app cancel only; the portal is a follow-up.

## 12. Out of scope, tracked as follow-ups

- The second provider adapter, annual plans, seats, and usage-based pricing.
- Tax/VAT reporting beyond the provider's dashboard.
- Dunning sequences and card-retry tuning.
- Referral credits and coupons.
