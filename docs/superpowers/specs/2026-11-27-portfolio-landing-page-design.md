# Public portfolio landing page — design

**Date:** 2026-11-27
**Status:** proposed
**Depends on:** Plan 6 (public status page), Plan 14 (security/CSP), existing web image
**Touches:** `web/landing.html`, `web/src/landing/*`, `web/vite.config.ts`,
`web/Caddyfile`, `web/public/robots.txt`, `web/public/sitemap.xml`,
`web/src/router.tsx`, `web/src/features/auth/*`, tests

## 1. Intent

The live site at `https://o1.work.gd/` currently serves the authenticated dashboard
shell. An anonymous visitor is redirected to `/login` by client-side routing, and
`robots.txt` disallows `/login`. Google therefore has no public, crawlable page
describing o1.

Add a **public, statically rendered portfolio landing page at `/`**. It is aimed
at recruiters and engineers evaluating the project, not at paying customers. It
should explain what o1 is, show the live demo and status page, and point at the
parts of the engineering worth discussing. Search engines must receive real HTML
content in the first response, without executing JavaScript.

## 2. Current state (verified)

- `web/` is a Vite + React SPA. Caddy's generic handler is
  `try_files {path} /index.html`, so every unknown path returns the SPA shell with
  HTTP 200.
- `/` is `homeRoute`, behind `appLayoutRoute`; unauthenticated users are redirected
  to `/login` in the browser. `robots.txt` disallows `/login`.
- `https://o1.work.gd/sitemap.xml` currently returns the SPA shell as
  `text/html`, because no file exists and the fallback catches it.
- `index.html` has static title/description/OG tags for the app; there is no
  per-page metadata and no JSON-LD.
- `robots.txt` allows `/$` and `/status/`, and disallows the auth pages and
  `/api/`. It does not disallow the authenticated app routes (`/w/`,
  `/workspaces`, …).
- The repo is public at `https://github.com/ndntan911/o1`; `PORTFOLIO.md`
  already publishes the read-only demo credentials
  (`demo-viewer@o1.work.gd` / `o1-demo-viewer-2026`) and screenshots under
  `docs/portfolio/screenshots/`.

## 3. Goals

- `GET /` returns a complete, useful HTML document as a static file. No JavaScript
  is required to read the content, headings, links, or metadata.
- The page describes o1 accurately for a recruiter/engineer: product, hard
  engineering decisions, stack, test/CI practice, live demo, status page, source.
- Search metadata is complete and correct: title, description, canonical, Open
  Graph/Twitter tags, `SoftwareApplication`/`WebSite` JSON-LD, one `<h1>`,
  descriptive image alt text.
- `robots.txt` and `sitemap.xml` are real files with correct content types, and
  `/sitemap.xml` never falls back to the SPA shell.
- The authenticated app continues to work at its existing URLs, and login still
  lands the user in the app rather than on the marketing page.
- All existing web tests, typecheck, lint, build, and Playwright smoke tests keep
  passing.

## 4. Non-goals

- No pricing page, signup funnel, blog, docs site, or CMS.
- No SSR framework or new runtime service. This stays one static multi-page Vite
  build inside the existing `web` image.
- No customer status pages in the sitemap. Their existing crawlability is left
  unchanged (`robots.txt` already allows `/status/`); this change does not add
  `noindex` to them. The demo status page may be listed. The mechanism is a
  static sitemap, not a database-driven one.
- No analytics, tracking scripts, or third-party embeds (they would change the
  CSP and add consent/privacy work).

## 5. Anti-goals

- A React page whose text only appears after hydration.
- Serving marketing HTML at app routes, or breaking deep links such as
  `/w/{workspace}/monitors/{id}`.
- A sitemap that lists URLs which redirect to `/login` or return the SPA shell.
- Copy that overclaims: no invented customers, metrics, names, or dates. Test
  counts and stack details come from `PORTFOLIO.md`/README and must stay in sync.

## 6. Audience and content

**Audience:** a recruiter or engineer who clicks a link from a CV, GitHub, or a
message. They have under a minute. They want to know what was built, whether it
is real, and what was technically hard.

**Primary action:** open the read-only dashboard demo at `/login` (the login
screen already has a “fill demo account” affordance). **Secondary actions:** view
`/status/demo`, read the source on GitHub, jump to a specific hard decision.

Proposed page outline (top to bottom):

1. **Header** — `o1 uptime`, anchors to sections, GitHub link, “Open demo” button.
2. **Hero** — “Uptime and incident monitoring for small teams.” One paragraph:
   a full product, not a demo; HTTP probes, incidents, multi-channel alerts,
   public status pages; Go + PostgreSQL + React, self-hosted. Primary/secondary
   CTAs and a short “what makes it interesting” line.
3. **Product screenshot** — `monitors-dark.png` (`<img>` with width/height and
   alt text; eager loading) with a caption.
4. **What it does** — six short items: monitors/probes, incidents, alerts
   (email, webhook, Slack, Telegram), public status pages, teams/RBAC/2FA/API
   keys, audit trail.
5. **Four decisions worth reading** — condensed from `PORTFOLIO.md`, each linking
   to the matching source path on GitHub:
   SSRF-safe probing; 90-day partitioned history + safe retention; alert retry
   classification/idempotency; tenant isolation and attributable automation.
6. **Engineering practice** — test counts (538 Go test functions, 390 frontend
   tests), blocking CI, digest-pinned releases and rollback, observability,
   threat model/load baseline. Link `PORTFOLIO.md` for the long version.
7. **Try it** — read-only demo credentials in a `<code>` block, note that seeded
   history is synthetic, buttons to `/login` and `/status/demo`.
8. **Stack** — compact definition list: Go 1.26, PostgreSQL, River, React 19,
   TanStack Router/Query, Tailwind 4, Docker Compose, Caddy, GitHub Actions,
   Prometheus/Grafana/Loki.
9. **Footer** — GitHub, status page, “source”, year. No fake contact details.

Language: **English**, matching the product, README, and portfolio writeup.

## 7. Approaches considered

| Approach | Why not / why chosen |
|---|---|
| **Static HTML page as a second Vite entry (chosen)** | Real content in the first byte; no hydration or new dependency; reuses Tailwind/theme tokens; small blast radius. |
| Prerender the existing React tree at build time | Reuses components, but adds a prerender plugin/snapshot step, hydration edge cases, CSP-hash maintenance, and more build fragility for one page. |
| SPA route with dynamically injected metadata | Cheapest to write, but depends on Google rendering JavaScript; weak and slow indexing. Contradicts the goal. |
| Separate marketing site/subdomain (Astro/Next) | Clean long-term, but adds DNS, TLS, a second deploy pipeline, and another service for a single page. Rejected as overkill. |

## 8. How it is built

### 8.1 Static page and build

- Add `web/landing.html`: semantic HTML, `<html lang="en">`, one `<h1>`, section
  landmarks, skip link, `<link rel="stylesheet" href="/src/landing/landing.css">`.
  It contains **no executable JavaScript**: no inline handlers, no module or
  classic script. Exactly one non-executable `<script type="application/ld+json">`
  block is allowed for structured data. Browsers do not execute that data block,
  so the existing CSP script hash is unchanged; if a browser ever blocks it, add
  the block's SHA-256 hash to `script-src` rather than allowing `unsafe-inline`.
- Add `web/src/landing/landing.css` that imports `../styles.css` (Tailwind,
  Geist font, theme tokens) and adds landing-specific component styles. Use
  `prefers-color-scheme`/the existing `.dark` tokens; no theme-toggle JS.
- Add `build.rollupOptions.input = { main: index.html, landing: landing.html }`
  in `web/vite.config.ts`. `index.html` remains the SPA entry and output name.
- Add a small dev/preview middleware that rewrites an exact `/` request to
  `/landing.html`, so `npm run dev` and `npm run preview` show the page. It must
  not rewrite `/login`, `/status/…`, or any other path.
- Copy the two or three images used by the page from
  `docs/portfolio/screenshots/` into `web/public/portfolio/` (the Docker build
  context is `./web`). Downscale/optimise before committing; keep the originals
  as the source of truth and note the refresh step in the page's README comment.
- Keep `index.html` unchanged except for any wording that must stay consistent.

### 8.2 Caddy routing

Add explicit handlers before the generic SPA fallback in `web/Caddyfile`:

- exact `/` → rewrite to `/landing.html`, `file_server`.
- `/landing.html` → `301` to `/` (canonical URL).
- `/sitemap.xml` → `file_server` only; a missing file must return 404, never the
  SPA shell. Set `Content-Type: application/xml`.
- `/robots.txt` continues to be served as a real file.
- Everything else keeps `try_files {path} /index.html`.

Add `X-Robots-Tag: noindex` to the auth/app path matchers (`/login`, `/register`,
`/verify-email`, `/forgot-password`, `/reset-password`, `/account/*`, `/app`,
`/workspaces*`, `/w/*`, `/admin*`, `/billing*`, `/onboarding*`). Do not apply it
to `/` or `/status/*`; customer status pages keep exactly the crawlability they
have today. Keep the existing CSP, HSTS, and frame protections. The landing
page introduces no executable script (only the JSON-LD data block), so the
existing script hash is unchanged.

### 8.3 App routing changes

Because `/` is now static, the router never sees it in production. Preserve the
existing “resume the last workspace” behaviour with a new authenticated home
route rather than regressing to a workspace list:

- Add an `/app` route (child of `appLayoutRoute`, no UI). Its `beforeLoad` does
  what `homeRoute` did: read `readLastWorkspaceSlug()`, resolve it against the
  user's workspaces, fall back to the first workspace, then redirect to
  `/w/$workspaceSlug/monitors`; with no workspace, redirect to `/workspaces`.
- Remove `homeRoute` (the `/` variant). `/` is served by Caddy in production and
  by the dev middleware in development.
- `publicLayoutRoute` sends an authenticated visitor to `/app`, not `/`.
- `safeRedirect` maps an explicit `/` target to `/app` (login must never land on
  marketing); `safeRedirect(undefined)` resolves to `/app` too. Update
  `guards.test.ts` and the login tests that assert `to: '/'`.
- The non-admin redirect in `router.tsx` goes to `/app`, not `/`.
- `AppShell`'s logo links to the current workspace's monitors (fallback
  `/workspaces`), so clicking the logo no longer leaves the app for marketing.
- `PublicLayout`'s logo may keep linking to `/` (the landing page).
- Deep links and the `?redirect=` login flow keep working; only the `/` fallback
  is remapped to `/app`.

### 8.4 SEO artifacts

- `web/landing.html` head:
  - `<title>o1 — uptime and incident monitoring for small teams</title>`
  - 150–160 character meta description naming the product category and stack.
  - `<link rel="canonical" href="https://o1.work.gd/">`
  - OG/Twitter tags, `og:image` = `/og.png` (existing 1200×630 asset).
  - JSON-LD: `SoftwareApplication` (name, description, applicationCategory
    `DeveloperApplication`, operatingSystem `Web`, url, codeRepository, author
    with the GitHub profile URL) and `WebSite`. Only facts present in the repo.
- `web/public/sitemap.xml`: `https://o1.work.gd/` and
  `https://o1.work.gd/status/demo`, with `lastmod`. No customer status pages.
- `web/public/robots.txt`: keep `Allow: /` and `Allow: /status/`; add the missing
  app-route disallows (`/app`, `/w/`, `/workspaces`, `/admin`, `/billing`,
  `/onboarding`) and `Sitemap: https://o1.work.gd/sitemap.xml`.
- Do not add a `google-site-verification` meta tag without the token from the
  owner; the runbook supports either the DNS TXT record or an HTML file in
  `web/public/`.

### 8.5 Search Console runbook (manual, documented in the PR/README)

1. Add `o1.work.gd` as a property in Google Search Console; verify by DNS TXT
   (preferred) or by placing the provided verification file in `web/public/`.
2. Submit `https://o1.work.gd/sitemap.xml`; request indexing for `/`.
3. After deploy, confirm `Googlebot` can fetch `/`, `/sitemap.xml`, and
   `/robots.txt` with the expected content types and no `X-Robots-Tag: noindex`.
4. Set expectations: a single page can rank for brand and long-tail queries, but
   competitive terms need content and backlinks over time. SEO is not “set and
   forget”.
5. Optional: repeat for Bing Webmaster Tools.

## 9. Tests and acceptance

- **Unit (vitest):** a new `web/src/landing.test.ts` asserts: title, description,
  canonical, OG/Twitter tags, exactly one `<h1>`, JSON-LD parses and names the
  product, every `<img>` has non-empty alt/width/height, no executable script
  (only the single `application/ld+json` data block); `sitemap.xml` is
  well-formed, lists only intended URLs; `robots.txt` contains the `Sitemap:`
  line and app disallows; `Caddyfile` contains the `/`, `/landing.html`, and
  `/sitemap.xml` handlers.
- **Build:** `npm run build` emits `dist/landing.html`, `dist/sitemap.xml`,
  `dist/robots.txt`, and the existing `dist/index.html`.
- **HTTP (Compose/Caddy):**
  - `GET /` → 200 `text/html`, contains the `<h1>` and demo credentials, does not
    contain `id="root"`.
  - `GET /landing.html` → 301 to `/`.
  - `GET /sitemap.xml` → 200 `application/xml`, not HTML.
  - `GET /robots.txt` → 200 `text/plain`.
  - `GET /login` and `GET /status/demo` → unchanged SPA shell, app still works.
  - `GET /w/...`, `/workspaces`, and `/app` carry `X-Robots-Tag: noindex`.
  - Logging in with `?redirect=/` lands on `/app` (then the last workspace),
    never on the marketing page.
- **Playwright:** extend the smoke test with one assertion that `/` shows the
  landing page and that “Open demo” reaches the login screen.
- **Manual:** Lighthouse/axe pass on `/`; keyboard-only pass; 320 px layout;
  dark and light via `prefers-color-scheme`; no console errors.

## 10. Risks and mitigations

| Risk | Mitigation |
|---|---|
| SPA fallback shadows the new static files | Explicit Caddy handlers with 404 for a missing sitemap; HTTP assertions above. |
| Authenticated users get bounced to marketing | Change post-login/`/login` redirects to `/workspaces`; Playwright check. |
| The page drifts from the real product (test counts, stack) | Copy claims from `PORTFOLIO.md`/README by hand; the design names the source. |
| Landing CSS bloats or conflicts with the app | Landing imports the shared tokens; app CSS entry is untouched; build output checked. |
| Google still does not rank it | GSC submission + honest expectation setting; optional later content/backlinks work. |
| Demo credentials become stale | They are already public in `PORTFOLIO.md` and `LoginScreen`; the page points at `/login`, where the fill button lives. |

## 11. Left to the builder

- Exact copy, section spacing, and visual polish within the existing theme.
- Which two or three screenshots to use and their compression settings.
- Whether to include a small “back to top” link or a dark/light toggle (a toggle
  would require JS and a CSP-hash update; the default is no toggle).
- Exact `lastmod` values and whether to include the demo status page in the
  sitemap (the default is yes).
- The GitHub URLs for source links (the repository is public).
