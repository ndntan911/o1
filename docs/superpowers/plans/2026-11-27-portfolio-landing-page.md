# Portfolio Landing Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve a public, statically rendered portfolio landing page at `/` so recruiters and engineers can understand o1 and Google can index real HTML instead of the authenticated SPA shell.

**Architecture:** A second Vite HTML entry (`web/landing.html`) with one CSS entry and no executable JavaScript. Caddy serves it for the exact path `/`, serves real `sitemap.xml`/`robots.txt` files, and keeps the SPA fallback for the app and `/status/*`. The router gains an authenticated `/app` home so login and `/login` redirects never land on marketing.

**Tech Stack:** Vite 8 multi-page build, Tailwind 4 (existing tokens/font), Caddy 2, React 19 + TanStack Router, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-27-portfolio-landing-page-design.md`

## Global Constraints

- All page copy is **English**, matching the product, README, and `PORTFOLIO.md`.
- No new npm/runtime dependency, no SSR framework, no second service, no CMS.
- No analytics, tracking scripts, or third-party embeds.
- `web/landing.html` contains **no executable JavaScript**; exactly one non-executable `<script type="application/ld+json">` block is allowed.
- Do not change the existing CSP script hash; the SPA shell theme script is untouched.
- Canonical origin is exactly `https://o1.work.gd`; repository is `https://github.com/ndntan911/o1`.
- Customer status pages keep their current crawlability; do not add `noindex` to `/status/*`, and do not list customer status pages in the sitemap.
- Every claim on the page must come from `PORTFOLIO.md` or the README; do not invent customers, metrics, names, or dates.
- Run frontend commands from `web/`.

## Review Focus

- A request for `/sitemap.xml` when the file is absent must return 404, never the SPA shell. (Task 2 test + HTTP check.)
- Login with `?redirect=/` or a bare `/login` must reach the app at `/app`, never the marketing page. (Task 3 tests + Task 4 e2e.)
- Deep links (`/w/...`), `/status/demo`, and authenticated app routes must keep working after `/` becomes static. (Task 4 e2e + HTTP checks.)
- The landing page must still show content with JavaScript disabled. (Task 1 source test + Task 4 no-JS Playwright test.)
- Images referenced by the page must exist inside the Docker build context (`web/`), not only under `docs/`. (Task 1 asset copy + Task 2 HTTP check.)

---

### Task 1: Static landing page entry

**Files:**
- Create: `web/landing.html`
- Create: `web/src/landing/landing.css`
- Create: `web/src/landing.test.ts`
- Create: `web/public/portfolio/monitors-dark.png`
- Create: `web/public/portfolio/status-dark.png`
- Modify: `web/vite.config.ts`

**Interfaces:**
- Consumes: existing theme tokens/font from `web/src/styles.css`; existing `web/public/og.png`.
- Produces: a build output `dist/landing.html` and dev/preview route `/`; stable link text `Open the live demo` → `/login`, `View the status page` → `/status/demo`; section id `main`.

- [ ] **Step 1: Copy the screenshots into the web build context**

Run from the repository root:

```sh
mkdir -p web/public/portfolio
cp docs/portfolio/screenshots/monitors-dark.png web/public/portfolio/monitors-dark.png
cp docs/portfolio/screenshots/status-dark.png web/public/portfolio/status-dark.png
```

Keep the `docs/portfolio/screenshots/` copies as the source of truth; the landing page uses the `web/public/portfolio/` copies because the Docker build context is `./web`.

- [ ] **Step 2: Write the failing source-level SEO test**

Create `web/src/landing.test.ts`:

```ts
/// <reference types="node" />
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { describe, expect, test } from 'vitest';

const read = (path: string) =>
	readFile(fileURLToPath(new URL(path, import.meta.url)), 'utf8');

describe('portfolio landing page', () => {
	test('ships complete search metadata and the demo CTAs', async () => {
		const html = await read('../landing.html');
		const doc = new DOMParser().parseFromString(html, 'text/html');

		expect(doc.title).toBe(
			'o1 — uptime and incident monitoring for small teams',
		);

		const description =
			doc.querySelector('meta[name="description"]')?.getAttribute('content') ??
			'';
		expect(description.length).toBeGreaterThanOrEqual(120);
		expect(description.length).toBeLessThanOrEqual(170);
		expect(description).toContain('uptime');

		expect(
			doc.querySelector('link[rel="canonical"]')?.getAttribute('href'),
		).toBe('https://o1.work.gd/');
		expect(
			doc
				.querySelector('meta[property="og:image"]')
				?.getAttribute('content'),
		).toBe('https://o1.work.gd/og.png');
		expect(
			doc
				.querySelector('meta[name="twitter:card"]')
				?.getAttribute('content'),
		).toBe('summary_large_image');
		expect(doc.querySelectorAll('h1')).toHaveLength(1);

		const demo = doc.querySelector('a[href="/login"]');
		expect(demo?.textContent).toContain('Open the live demo');
		expect(doc.querySelector('a[href="/status/demo"]')).not.toBeNull();
	});

	test('includes JSON-LD but no executable JavaScript', async () => {
		const html = await read('../landing.html');
		const doc = new DOMParser().parseFromString(html, 'text/html');

		const scripts = [...doc.querySelectorAll('script')];
		expect(scripts).toHaveLength(1);
		expect(scripts[0].getAttribute('type')).toBe('application/ld+json');

		const data = JSON.parse(scripts[0].textContent ?? '');
		expect(data['@type']).toBe('SoftwareApplication');
		expect(data.name).toBe('o1');
		expect(data.codeRepository).toBe('https://github.com/ndntan911/o1');
	});

	test('every image has alt text and intrinsic dimensions', async () => {
		const html = await read('../landing.html');
		const doc = new DOMParser().parseFromString(html, 'text/html');
		const images = [...doc.querySelectorAll('img')];

		expect(images.length).toBeGreaterThan(0);
		for (const image of images) {
			expect(image.getAttribute('alt')?.trim()).not.toBe('');
			expect(image.getAttribute('width')).toMatch(/^\d+$/);
			expect(image.getAttribute('height')).toMatch(/^\d+$/);
		}
	});
});
```

- [ ] **Step 3: Run the test and verify it fails**

Run: `cd web && npm test -- --run src/landing.test.ts`
Expected: FAIL because `../landing.html` does not exist.

- [ ] **Step 4: Create `web/landing.html`**

Use this exact head and section skeleton, then write the body copy from `PORTFOLIO.md`:

```html
<!doctype html>
<html lang="en">
	<head>
		<meta charset="UTF-8" />
		<meta name="viewport" content="width=device-width, initial-scale=1.0" />
		<link rel="icon" type="image/svg+xml" href="/favicon.svg" />
		<title>o1 — uptime and incident monitoring for small teams</title>
		<meta
			name="description"
			content="o1 is a self-hosted uptime monitor for small teams: SSRF-guarded HTTP probes, incidents, email/webhook/Slack/Telegram alerts, and public status pages."
		/>
		<link rel="canonical" href="https://o1.work.gd/" />
		<meta property="og:type" content="website" />
		<meta property="og:site_name" content="o1 uptime" />
		<meta property="og:title" content="o1 — uptime and incident monitoring for small teams" />
		<meta property="og:description" content="A full product, not a demo: SSRF-guarded probes, 90 days of history, exactly-once alerts, and public status pages. Go + PostgreSQL + React." />
		<meta property="og:url" content="https://o1.work.gd/" />
		<meta property="og:image" content="https://o1.work.gd/og.png" />
		<meta property="og:image:width" content="1200" />
		<meta property="og:image:height" content="630" />
		<meta property="og:image:alt" content="o1 uptime — uptime and incident monitoring for small teams" />
		<meta name="twitter:card" content="summary_large_image" />
		<link rel="stylesheet" href="/src/landing/landing.css" />
		<script type="application/ld+json">
			{
				"@context": "https://schema.org",
				"@type": "SoftwareApplication",
				"name": "o1",
				"description": "Uptime and incident monitoring for small teams: HTTP probes, incidents, multi-channel alerts, and public status pages.",
				"applicationCategory": "DeveloperApplication",
				"operatingSystem": "Web",
				"url": "https://o1.work.gd/",
				"codeRepository": "https://github.com/ndntan911/o1",
				"author": {
					"@type": "Person",
					"url": "https://github.com/ndntan911"
				}
			}
		</script>
	</head>
	<body>
		<a class="skip-link" href="#main">Skip to content</a>
		<header><!-- wordmark, section anchors, GitHub link, Open the live demo --></header>
		<main id="main">
			<section aria-labelledby="hero-title">
				<h1 id="hero-title">Uptime and incident monitoring for small teams.</h1>
				<!-- one paragraph: full product, probes, incidents, alerts, status pages -->
				<!-- links: Open the live demo (/login), View the status page (/status/demo) -->
			</section>
			<figure>
				<img src="/portfolio/monitors-dark.png" width="1440" height="900"
					alt="o1 monitors dashboard in dark mode showing HTTP monitors and current status" />
				<figcaption>Monitors, live status, and response history.</figcaption>
			</figure>
			<section aria-labelledby="what-title">
				<h2 id="what-title">What it does</h2>
				<!-- six items: probes, incidents, alerts, status pages, teams/RBAC/2FA/API keys, audit -->
			</section>
			<section aria-labelledby="decisions-title">
				<h2 id="decisions-title">Four decisions worth reading</h2>
				<!-- SSRF-safe probing; partitioned 90-day history + safe retention; alert retry classification/idempotency; tenant isolation + attributable automation -->
			</section>
			<section aria-labelledby="practice-title">
				<h2 id="practice-title">Engineering practice</h2>
				<!-- blocking CI, digest-pinned releases + rollback, observability, threat model/load baseline -->
			</section>
			<section aria-labelledby="demo-title">
				<h2 id="demo-title">Try it</h2>
				<!-- read-only demo credentials; synthetic-history note; /login and /status/demo links -->
			</section>
			<section aria-labelledby="stack-title">
				<h2 id="stack-title">Stack</h2>
				<!-- Go 1.26, PostgreSQL, River, React 19, TanStack, Tailwind 4, Compose, Caddy, GitHub Actions, Prometheus/Grafana/Loki -->
			</section>
		</main>
		<footer><!-- GitHub, status page, source --></footer>
	</body>
</html>
```

Copy the hard-decisions text and links from the matching sections of `PORTFOLIO.md`. Link source decisions to `https://github.com/ndntan911/o1/blob/main/<path>` for the files named there. Include the demo credentials `demo-viewer@o1.work.gd` / `o1-demo-viewer-2026` and state that seeded history is synthetic. Use one `<h1>` only. For the Engineering practice section, use the current test counts from `PORTFOLIO.md`; Task 4 recounts them after the new tests land.

- [ ] **Step 5: Create `web/src/landing/landing.css`**

```css
@import '../styles.css';

@layer components {
	.skip-link {
		@apply sr-only;
	}
	.skip-link:focus {
		@apply not-sr-only fixed top-4 left-4 z-50 rounded-lg bg-background px-3 py-2 ring-1 ring-ring;
	}
}
```

Use Tailwind utility classes in `landing.html` for layout, typography, cards, and responsive behavior. Do not add a theme toggle or any JavaScript. The page uses the existing light tokens; if dark support is wanted, copy the `.dark` variable block from `src/styles.css` into a `@media (prefers-color-scheme: dark)` block in `landing.css` (still no JavaScript).

- [ ] **Step 6: Make Vite build the landing entry and serve it at `/` in dev/preview**

Modify `web/vite.config.ts`:

```ts
import type { Plugin } from 'vite';

function landingAtRoot(): Plugin {
	const rewrite = (req: { url?: string }) => {
		if (req.url === '/') req.url = '/landing.html';
	};
	return {
		name: 'landing-at-root',
		configureServer(server) {
			server.middlewares.use((req, _res, next) => {
				rewrite(req);
				next();
			});
		},
		configurePreviewServer(server) {
			server.middlewares.use((req, _res, next) => {
				rewrite(req);
				next();
			});
		},
	};
}

export default defineConfig({
	plugins: [landingAtRoot(), react(), tailwindcss()],
	build: {
		rollupOptions: {
			input: {
				main: fileURLToPath(new URL('./index.html', import.meta.url)),
				landing: fileURLToPath(new URL('./landing.html', import.meta.url)),
			},
		},
	},
	// keep the existing resolve, server, and test blocks unchanged
});
```

- [ ] **Step 7: Run the test and the build**

Run: `cd web && npm test -- --run src/landing.test.ts`
Expected: PASS.

Run: `cd web && npm run build`
Expected: PASS and `web/dist/landing.html` exists.

- [ ] **Step 8: Commit**

```bash
git add web/landing.html web/src/landing/landing.css web/src/landing.test.ts \
  web/public/portfolio/monitors-dark.png web/public/portfolio/status-dark.png \
  web/vite.config.ts docs/superpowers/specs/2026-11-27-portfolio-landing-page-design.md \
  docs/superpowers/plans/2026-11-27-portfolio-landing-page.md
git commit -m "feat(web): add static portfolio landing page"
```

---

### Task 2: Caddy routing and crawler files

**Files:**
- Modify: `web/Caddyfile`
- Modify: `web/public/robots.txt`
- Create: `web/public/sitemap.xml`
- Modify: `web/src/landing.test.ts` (add crawler/Caddy tests)

**Interfaces:**
- Consumes: `dist/landing.html` from Task 1.
- Produces: `GET /` → landing HTML, `GET /landing.html` → 301 `/`, `GET /sitemap.xml` → real XML, `X-Robots-Tag: noindex` on app/auth paths.

- [ ] **Step 1: Add failing tests for crawler files and Caddy routing**

Append to `web/src/landing.test.ts`:

```ts
describe('crawler files and static routing', () => {
	test('sitemap lists only the landing page and demo status page', async () => {
		const xml = await read('../public/sitemap.xml');
		const urls = [...xml.matchAll(/<loc>([^<]+)<\/loc>/g)].map(
			(match) => match[1],
		);

		expect(urls).toEqual([
			'https://o1.work.gd/',
			'https://o1.work.gd/status/demo',
		]);
	});

	test('robots.txt points at the sitemap and keeps app routes out', async () => {
		const robots = await read('../public/robots.txt');

		expect(robots).toContain('Sitemap: https://o1.work.gd/sitemap.xml');
		expect(robots).toContain('Allow: /status/');
		for (const path of ['/app', '/w/', '/workspaces', '/api/']) {
			expect(robots).toContain(`Disallow: ${path}`);
		}
	});

	test('Caddy serves the landing page and a real sitemap', async () => {
		const caddy = await read('../Caddyfile');

		expect(caddy).toMatch(/rewrite \* \/landing\.html/);
		expect(caddy).toMatch(/redir @landing-file \/ 301/);
		expect(caddy).toMatch(/handle \/sitemap\.xml/);
		expect(caddy).toContain('X-Robots-Tag "noindex"');
	});
});
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `cd web && npm test -- --run src/landing.test.ts`
Expected: FAIL on the missing sitemap, robots `Sitemap:` line, and Caddy handlers.

- [ ] **Step 3: Create `web/public/sitemap.xml`**

```xml
<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
	<url>
		<loc>https://o1.work.gd/</loc>
		<lastmod>2026-11-27</lastmod>
		<changefreq>monthly</changefreq>
		<priority>1.0</priority>
	</url>
	<url>
		<loc>https://o1.work.gd/status/demo</loc>
		<lastmod>2026-11-27</lastmod>
		<changefreq>daily</changefreq>
		<priority>0.5</priority>
	</url>
</urlset>
```

- [ ] **Step 4: Update `web/public/robots.txt`**

```text
# o1 serves the dashboard as a single-page app, so every unknown path returns
# the shell. The landing page at / and the public status pages are the only
# indexable surfaces; keep crawlers out of the API and authenticated app.
User-agent: *
Allow: /
Allow: /status/
Disallow: /api/
Disallow: /account/
Disallow: /app
Disallow: /login
Disallow: /register
Disallow: /verify-email
Disallow: /forgot-password
Disallow: /reset-password
Disallow: /workspaces
Disallow: /w/
Disallow: /admin
Disallow: /billing
Disallow: /onboarding

Sitemap: https://o1.work.gd/sitemap.xml
```

- [ ] **Step 5: Add the explicit Caddy handlers**

In `web/Caddyfile`, before the final generic `handle` block, add:

```caddyfile
	# The portfolio landing page is a real static file, not the SPA shell.
	@landing path /
	handle @landing {
		rewrite * /landing.html
		file_server
	}
	@landing-file path /landing.html
	redir @landing-file / 301

	# A missing sitemap must 404, never fall through to index.html.
	handle /sitemap.xml {
		header Content-Type application/xml
		file_server
	}

	# Authenticated surfaces must not be indexed even if a crawler finds a link.
	@app path /login /register /verify-email /forgot-password /reset-password /account/* /app /workspaces* /w/* /admin* /billing* /onboarding*
	header @app X-Robots-Tag "noindex"
```

Leave `/`, `/status/*`, `/api/*`, health checks, and the generic
`try_files {path} /index.html` fallback otherwise unchanged.

- [ ] **Step 6: Run the tests**

Run: `cd web && npm test -- --run src/landing.test.ts`
Expected: PASS.

- [ ] **Step 7: Verify the build output and HTTP behavior through Caddy**

Run: `cd web && npm run build`
Expected: PASS; `web/dist/landing.html`, `web/dist/sitemap.xml`, and
`web/dist/robots.txt` exist.

From the repository root:

```sh
docker compose up -d --build
curl -sI http://localhost:5173/ | head -1
curl -s http://localhost:5173/ | grep -q 'Uptime and incident monitoring for small teams'
curl -sI http://localhost:5173/landing.html | grep -i '^location: /'
curl -sI http://localhost:5173/sitemap.xml | grep -i '^content-type: application/xml'
curl -sI http://localhost:5173/login | grep -i '^x-robots-tag: noindex'
curl -sI http://localhost:5173/portfolio/monitors-dark.png | head -1
```

Expected: 200 for `/`, `/sitemap.xml`, and the image; `301` with `Location: /`
for `/landing.html`; `noindex` on `/login`; `/status/demo` still returns the SPA
shell.

- [ ] **Step 8: Commit**

```bash
git add web/Caddyfile web/public/robots.txt web/public/sitemap.xml web/src/landing.test.ts
git commit -m "feat(web): serve landing page and crawler files from Caddy"
```

---

### Task 3: Authenticated `/app` home and login redirects

**Files:**
- Modify: `web/src/features/auth/guards.ts`
- Modify: `web/src/features/auth/guards.test.ts`
- Modify: `web/src/features/auth/LoginScreen.test.tsx`
- Modify: `web/src/router.tsx`
- Modify: `web/src/components/layout/AppShell.tsx`

**Interfaces:**
- Consumes: `readLastWorkspaceSlug()`, `findWorkspace()`, `meQueryOptions()`.
- Produces: `safeRedirect(undefined | '' | '/') === '/app'`; authenticated route `/app` that redirects to `/w/$workspaceSlug/monitors` or `/workspaces`; app logo links to the current workspace monitors.

- [ ] **Step 1: Update `guards.test.ts` to the new fallback**

Change the expectations in `safeRedirect rejects external and protocol-relative paths`:

```ts
test('safeRedirect rejects external and protocol-relative paths', () => {
	expect(safeRedirect(undefined)).toBe('/app');
	expect(safeRedirect('')).toBe('/app');
	expect(safeRedirect('https://evil.example')).toBe('/app');
	expect(safeRedirect('//evil.example')).toBe('/app');
	expect(safeRedirect('/\\evil.example')).toBe('/app');
	expect(safeRedirect('javascript:alert(1)')).toBe('/app');
	expect(safeRedirect('/w/acme/monitors')).toBe('/w/acme/monitors');
	expect(safeRedirect('/')).toBe('/app');
});
```

Update the control-character test's expected values from `/` to `/app`.

- [ ] **Step 2: Update the login tests that assert the old `/` target**

In `web/src/features/auth/LoginScreen.test.tsx`, wherever the test renders
`<LoginScreen redirectTo="/" />` and asserts `navigate` was called with
`{ to: '/' }`, assert `{ to: '/app' }` instead. There are four such assertions
around the password, TOTP, and recovery-code paths. Leave the
`redirectTo="/w/acme/monitors"` case unchanged.

- [ ] **Step 3: Run the tests and verify they fail**

Run: `cd web && npm test -- --run src/features/auth/guards.test.ts src/features/auth/LoginScreen.test.tsx`
Expected: FAIL on the `/app` expectations.

- [ ] **Step 4: Implement `safeRedirect`**

In `web/src/features/auth/guards.ts`, set the fallback to `/app` and map an
explicit `/` target to it:

```ts
export function safeRedirect(value: string | undefined): string {
	const fallback = '/app';
	if (!value) return fallback;
	if (!value.startsWith('/')) return fallback;
	if (value.startsWith('//')) return fallback;
	if (value.includes('\\')) return fallback;
	if (hasControlCharacter(value)) return fallback;
	if (value === '/') return fallback;
	return value;
}
```

- [ ] **Step 5: Add the `/app` route and update redirects in `router.tsx`**

Replace `homeRoute` with:

```ts
const appHomeRoute = createRoute({
	getParentRoute: () => appLayoutRoute,
	path: '/app',
	beforeLoad: async ({ context }) => {
		const me = await context.queryClient.ensureQueryData(meQueryOptions());
		const lastSlug = readLastWorkspaceSlug();
		const lastWorkspace = lastSlug ? findWorkspace(me, lastSlug) : undefined;
		const workspace = lastWorkspace ?? me.workspaces[0];

		if (!workspace) throw redirect({ to: '/workspaces' });

		throw redirect({
			to: '/w/$workspaceSlug/monitors',
			params: { workspaceSlug: workspace.slug },
		});
	},
});
```

Change the authenticated redirect in `publicLayoutRoute.beforeLoad` from
`{ to: '/' }` to `{ to: '/app' }`, and the non-admin redirect in `adminRoute`
from `{ to: '/' }` to `{ to: '/app' }`. Register `appHomeRoute` in the route
tree where `homeRoute` was registered.

- [ ] **Step 6: Point the app logo at the current workspace**

In `web/src/components/layout/AppShell.tsx`, replace both `<Link to="/">`
wordmarks (desktop sidebar and mobile header). Use a conditional render rather
than spreading a union into `Link`; keep each location's existing
`className`:

```tsx
{currentWorkspace ? (
	<Link
		to="/w/$workspaceSlug/monitors"
		params={{ workspaceSlug: currentWorkspace.slug }}
		className="mb-4 px-2 font-heading text-lg font-medium"
	>
		o1 uptime
	</Link>
) : (
	<Link to="/workspaces" className="mb-4 px-2 font-heading text-lg font-medium">
		o1 uptime
	</Link>
)}
```

Repeat in the mobile header with its existing
`className="font-heading text-base font-medium"`.

- [ ] **Step 7: Run the tests and typecheck**

Run: `cd web && npm test -- --run src/features/auth/guards.test.ts src/features/auth/LoginScreen.test.tsx`
Expected: PASS.

Run: `cd web && npm run typecheck`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add web/src/features/auth/guards.ts web/src/features/auth/guards.test.ts \
  web/src/features/auth/LoginScreen.test.tsx web/src/router.tsx \
  web/src/components/layout/AppShell.tsx
git commit -m "feat(web): route authenticated users through /app"
```

---

### Task 4: End-to-end coverage, copy accuracy, and docs

**Files:**
- Modify: `web/tests/e2e/smoke.spec.ts`
- Modify: `web/README.md`
- Modify: `PORTFOLIO.md` (test counts, only if they changed)

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: e2e proof that `/` is static, JS-disabled content is visible, CTAs reach `/login`, and existing theme tests still work.

- [ ] **Step 1: Update the two theme tests to a route that still serves the SPA shell**

In `web/tests/e2e/smoke.spec.ts`, change `page.goto('/', ...)` to
`page.goto('/login', ...)` in:

- `test('applies the stored theme before the app loads')`
- `test('ignores an invalid stored theme')`

Their `#root` and `html.dark` assertions stay the same.

- [ ] **Step 2: Add the landing and no-JS e2e tests**

Before the registration test, add:

```ts
test('serves the portfolio landing page at the root', async ({
	page,
	request,
}) => {
	const response = await request.get('/');
	expect(response.status()).toBe(200);
	expect(response.headers()['content-type']).toContain('text/html');
	const html = await response.text();
	expect(html).toContain('Uptime and incident monitoring for small teams.');
	expect(html).not.toContain('id="root"');

	const sitemap = await request.get('/sitemap.xml');
	expect(sitemap.status()).toBe(200);
	expect(sitemap.headers()['content-type']).toContain('xml');
	expect(await sitemap.text()).toContain('<urlset');

	const image = await request.get('/portfolio/monitors-dark.png');
	expect(image.status()).toBe(200);

	await page.goto('/');
	await expect(page.getByRole('heading', { level: 1 })).toContainText(
		'Uptime and incident monitoring for small teams.',
	);
	await expectNoSeriousA11yViolations(page);
	await page.getByRole('link', { name: 'Open the live demo' }).click();
	await expect(page).toHaveURL(/\/login$/);
	await expect(page.getByLabel('Email')).toBeVisible();
});

test.describe('landing page without JavaScript', () => {
	test.use({ javaScriptEnabled: false });

	test('keeps the content and demo link visible', async ({ page }) => {
		await page.goto('/');
		await expect(page.getByRole('heading', { level: 1 })).toContainText(
			'Uptime and incident monitoring for small teams.',
		);
		await expect(
			page.getByRole('link', { name: 'Open the live demo' }),
		).toBeVisible();
	});
});
```

- [ ] **Step 3: Run the targeted e2e tests**

From `web/`, with the Compose stack running:

```sh
npx playwright test -g "portfolio landing|landing page without JavaScript|applies the stored theme|ignores an invalid stored theme"
```

Expected: PASS.

- [ ] **Step 4: Add the Search Console runbook to `web/README.md`**

Append a short "SEO / Search Console" section:

```md
## SEO / Search Console

`landing.html` is the public page at `/`; it is a static Vite entry with no
executable JavaScript. `public/sitemap.xml` lists only the landing page and the
demo status page, and `public/robots.txt` points at it.

After deploying a new origin or domain:

1. Add the property in Google Search Console and verify it by DNS TXT (preferred)
   or by placing the provided verification file in `web/public/`.
2. Submit `https://<domain>/sitemap.xml` and request indexing for `/`.
3. Confirm `/`, `/sitemap.xml`, and `/robots.txt` are fetchable without
   `X-Robots-Tag: noindex`.
4. A single page can rank for brand and long-tail queries; competitive terms need
   content and backlinks over time.
```

- [ ] **Step 5: Recompute the test counts only if they changed**

If the landing copy quotes exact test counts, recount and update both the copy
and `PORTFOLIO.md` so they stay true. Use the counts already in `PORTFOLIO.md`
only if they still match; otherwise replace both with the new exact values.
If the count method is ambiguous, use "hundreds of Go and frontend tests" in the
landing copy and leave `PORTFOLIO.md` alone.

- [ ] **Step 6: Run the complete frontend checks**

```sh
cd web
npm run format:check
npm run lint
npm run typecheck
npm test -- --run
npm run build
```

Expected: all PASS. If `format:check` fails, run `npm run format`, then repeat.

- [ ] **Step 7: Commit**

```bash
git add web/tests/e2e/smoke.spec.ts web/README.md PORTFOLIO.md
git commit -m "test(web): cover the portfolio landing page end to end"
```

---

## Self-review notes

- Spec §8.1 (static page/build) → Task 1.
- Spec §8.2 (Caddy, robots, sitemap, noindex) → Task 2.
- Spec §8.3 (`/app`, login redirects, logo) → Task 3.
- Spec §8.4 (metadata/JSON-LD) → Task 1; sitemap/robots → Task 2.
- Spec §8.5 (Search Console runbook) → Task 4, Step 4.
- Spec §9 (tests/acceptance) → Tasks 1–4.
- Known stale-doc risk: test counts in `PORTFOLIO.md` → Task 4, Step 5.
