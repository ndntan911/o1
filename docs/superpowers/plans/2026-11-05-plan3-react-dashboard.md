# Plan 3 — React Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the self-service React dashboard for the existing o1 uptime-monitoring backend.

**Architecture:** A Vite React SPA in `web/` calls the Go API through a same-origin `/api` proxy. TanStack Query owns server state, TanStack Router owns typed routes and guards, and feature folders own their API calls, schemas, and screens. In Compose, a Caddy container serves the built SPA and proxies `/api` to the `api` service.

**Tech Stack:** React 19, TypeScript, Vite, TanStack Router/Query, react-hook-form + zod, Tailwind CSS + shadcn/ui, Recharts, Vitest + Testing Library + MSW, Playwright, Caddy, Node 24.

**Spec:** `docs/superpowers/specs/2026-11-05-plan3-react-dashboard-design.md`

## Global Constraints

- UI language is English.
- Same origin `http://localhost:5173` for the SPA; API base path `/api/v1`; no CORS headers, no CSRF token, no cookie-attribute changes. The existing Origin allowlist is configured, not replaced.
- Reuse the HttpOnly `o1_session` cookie.
- Never send masked header values (`***`) or hidden bodies back to the API.
- Errors are `application/problem+json`; surface `code` and `fields`.
- Roles: `viewer` < `member` < `admin` < `owner`; monitor mutations need `member+`; member management needs `admin+`.
- Cursor pagination uses the opaque `next_cursor`; omit the cursor from infinite-query keys.
- Monitor windows: `1h`, `6h`, `24h`, `7d`.
- Backend bounds: password 12–128; workspace name 1–80; monitor name 1–100; URL ≤ 2048; interval 60–86400; timeout 1–30; thresholds 1–5; headers ≤ 20; body ≤ 16 KiB; content type ≤ 200.
- Node 24 LTS; commit `web/package-lock.json`; CI uses `npm ci`.
- Every task ends with lint, typecheck, tests, and a commit. Backend tasks also run `go vet ./...` and the touched package tests. From Task 3 onward, the frontend check sequence is `npm run format:check`, `npm run lint`, `npm run typecheck`, `npm test -- --run`, and `npm run build`; later tasks may scope `npm test` to their folder while iterating.
- Screen tests mock `@tanstack/react-router` (`Link` renders an anchor;
  `useNavigate`, `useParams`, `useSearch`, and `useRouteContext` return test
  values) unless they render the real route tree with
  `createMemoryHistory`.

## Review Focus

1. Editing a monitor must never round-trip `***` header values or a hidden body; a wrong payload destroys secrets.
2. A 401 from a background refetch on a public auth page must not cause a redirect loop or discard typed form input.
3. A workspace slug absent from `me.workspaces` must render not-found without leaking existence or crashing.
4. Infinite queries must stop when `next_cursor` is absent and must not loop on an empty-string cursor.
5. Uptime with zero checks or a null value must render "No data", never `NaN%` or `0%` down.

---

## File Structure

```text
web/
  Caddyfile
  Dockerfile
  .dockerignore
  package.json
  package-lock.json
  index.html
  vite.config.ts
  tsconfig.json
  tsconfig.app.json
  tsconfig.node.json
  eslint.config.js
  .prettierrc.json
  .prettierignore
  playwright.config.ts
  components.json
  src/
    main.tsx
    router.tsx
    styles.css
    vite-env.d.ts
    components/
      ui/                  # shadcn primitives
      layout/{RootLayout,AppShell,Sidebar,WorkspaceSwitcher,UserMenu}.tsx
      feedback/{EmptyState,ErrorPanel,ProblemAlert,ConfirmDialog,Skeletons}.tsx
    features/
      auth/{api,schemas,guards,PublicLayout,LoginScreen,RegisterScreen,VerifyEmailScreen,ForgotPasswordScreen,ResetPasswordScreen}.ts(x)
      workspaces/{api,schemas,WorkspaceListScreen,WorkspaceCreateDialog,MembersScreen,AddMemberDialog}.ts(x)
      monitors/{api,schemas,form,hooks,status,MonitorListScreen,MonitorCreateScreen,MonitorEditScreen,MonitorDetailScreen,MonitorForm,HeadersField,SummaryCards,ResultsTable,LatencyChart}.ts(x)
      admin/{api,hooks,AdminWorkspacesScreen,AdminMonitorsScreen,SecurityEventsScreen,SuspendDialog,WorkspaceCombobox,EventDetail}.ts(x)
    lib/
      api/{client,problems,types}.ts
      query/{client,keys,me,pagination}.ts
      roles.ts
      format.ts
      forms.ts
      last-workspace.ts
      workspace-status.ts
      auth-redirect.ts
      cn.ts
    test/
      setup.ts
      render.tsx
      fixtures.ts
      msw/{server,handlers}.ts
  tests/e2e/smoke.spec.ts
```

Feature code owns its API calls, schemas, hooks, and screens. Shared code lives only in `components/ui` and `lib`.

---

### Task 1: Expose `is_admin` in `GET /me`

**Files:**
- Modify: `backend/internal/api/router.go`
- Test: `backend/internal/api/integration_test.go`

**Interfaces:**
- Consumes: `auth.Principal.IsAdmin` (already loaded by `Authenticate`).
- Produces: `GET /api/v1/me` JSON `user.is_admin: boolean`.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/api/integration_test.go` (add `context` to the imports):

```go
func TestAPI_MeExposesAdminFlag(t *testing.T) {
	pool, cleanup := testutil.NewPostgres(t)
	defer cleanup()

	cfg := testutil.TestConfig(t)
	const email = "admin-flag@example.com"
	const password = "correct horse battery staple"
	userID := testutil.CreateVerifiedUser(t, pool, email, password)

	router := api.NewRouter(api.Dependencies{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Pool:   pool,
		Config: cfg,
	})
	server := httptest.NewServer(router)
	defer server.Close()

	c := newAPIClient(t, server.URL, cfg.AppBaseURL)
	resp, _ := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password,
	})
	requireStatus(t, resp, http.StatusOK)

	readAdmin := func() bool {
		resp, body := c.do(http.MethodGet, "/api/v1/me", nil)
		requireStatus(t, resp, http.StatusOK)
		out := decodeJSON[struct {
			User struct {
				IsAdmin bool `json:"is_admin"`
			} `json:"user"`
		}](t, body)
		return out.User.IsAdmin
	}

	if readAdmin() {
		t.Fatal("new user is admin, want false")
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET is_admin = true WHERE id = $1`, userID); err != nil {
		t.Fatalf("promote user: %v", err)
	}
	if !readAdmin() {
		t.Fatal("promoted user is not admin, want true")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/api -run TestAPI_MeExposesAdminFlag -v`

Expected: FAIL on `promoted user is not admin, want true` because `meUser` has no `is_admin`.

- [ ] **Step 3: Add the field**

In `backend/internal/api/router.go`, change:

```go
type meUser struct {
	ID      uuid.UUID `json:"id"`
	Email   string    `json:"email"`
	IsAdmin bool      `json:"is_admin"`
}
```

In `MeHandler`, set `IsAdmin: principal.IsAdmin` in the `meUser` literal.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd backend && go test ./internal/api -run TestAPI_MeExposesAdminFlag -v`

Expected: PASS.

- [ ] **Step 5: Run vet and the package tests**

Run: `cd backend && go vet ./... && go test ./internal/api -short`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/router.go backend/internal/api/integration_test.go
git commit -m "feat: expose admin flag in me response"
```

---

### Task 2: Fix the Origin allowlist default and Compose forwarding

**Files:**
- Modify: `backend/internal/config/config.go`
- Test: `backend/internal/config/config_test.go`
- Modify: `compose.yaml`
- Modify: `README.md`

**Interfaces:**
- Consumes: `O1_ALLOWED_ORIGINS`, `O1_APP_BASE_URL`.
- Produces: when `O1_ALLOWED_ORIGINS` is empty, `Config.AllowedOrigins == []string{AppBaseURL}`; Compose forwards the variable to `api`, `scheduler`, and `worker`.

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/config/config_test.go` (add `reflect`):

```go
func TestLoad_DefaultsAllowedOriginsToAppBaseURL(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_APP_BASE_URL", "http://localhost:5173")
	t.Setenv("O1_ALLOWED_ORIGINS", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://localhost:5173"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, want) {
		t.Fatalf("allowed origins = %v, want %v", cfg.AllowedOrigins, want)
	}
}

func TestLoad_UsesExplicitAllowedOrigins(t *testing.T) {
	t.Setenv("O1_DATABASE_URL", "postgres://example")
	t.Setenv("O1_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("O1_APP_BASE_URL", "http://localhost:5173")
	t.Setenv("O1_ALLOWED_ORIGINS", "http://a.example, http://b.example")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://a.example", "http://b.example"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, want) {
		t.Fatalf("allowed origins = %v, want %v", cfg.AllowedOrigins, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/config -run 'TestLoad_(Defaults|UsesExplicit)AllowedOrigins' -v`

Expected: `TestLoad_DefaultsAllowedOriginsToAppBaseURL` FAILS because the slice is empty; the explicit test PASSES.

- [ ] **Step 3: Implement the fallback**

In `backend/internal/config/config.go`, after the loop that splits `O1_ALLOWED_ORIGINS`, add:

```go
if len(cfg.AllowedOrigins) == 0 && cfg.AppBaseURL != "" {
	cfg.AllowedOrigins = []string{cfg.AppBaseURL}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./internal/config -v`

Expected: PASS.

- [ ] **Step 5: Forward the variable through Compose**

In `compose.yaml`, inside the shared `&backend-env` block, add below `O1_APP_BASE_URL`:

```yaml
      O1_ALLOWED_ORIGINS: ${O1_ALLOWED_ORIGINS:-}
```

In `README.md`, replace the `O1_ALLOWED_ORIGINS` bullet with:

```markdown
- `O1_ALLOWED_ORIGINS` — comma-separated Origin allowlist for unsafe requests.
  Defaults to `O1_APP_BASE_URL`. Compose forwards this variable; leave it empty
  to use the dashboard origin.
```

- [ ] **Step 6: Verify Compose resolution**

Run: `docker compose config | grep O1_ALLOWED_ORIGINS`

Expected: three lines (api, scheduler, worker), each with
`O1_ALLOWED_ORIGINS: http://localhost:5173` when `.env` is unset or the
configured value when set.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/config/config.go backend/internal/config/config_test.go compose.yaml README.md
git commit -m "fix: default allowed origins to app base url"
```

---

### Task 3: Scaffold the frontend toolchain

**Files:**
- Create: `web/` (Vite React TypeScript app)
- Create: `web/src/test/setup.ts`, `web/src/test/render.tsx`, `web/src/test/msw/server.ts`, `web/src/test/msw/handlers.ts`, `web/src/test/smoke.test.tsx`
- Create: `web/src/lib/cn.ts`
- Create: `web/src/styles.css`
- Modify: `web/vite.config.ts`, `web/tsconfig.app.json`, `web/package.json`

**Interfaces:**
- Produces: `npm run dev|build|lint|typecheck|test|format|e2e`.
- Produces: `renderWithProviders(ui: React.ReactElement, options?: { queryClient?: QueryClient }): RenderResult`.
- Produces: MSW `server` with an initially empty `handlers` array.
- Produces: `cn(...inputs: ClassValue[]): string`.

- [ ] **Step 1: Write the failing smoke test**

Create `web/src/test/smoke.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react';
import { Button } from '@/components/ui/button';

test('renders the shared button primitive', () => {
  render(<Button>Save</Button>);
  expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument();
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test -- --run src/test/smoke.test.tsx`

Expected: FAIL because `web/` and the app do not exist.

- [ ] **Step 3: Scaffold Vite and install dependencies**

From the repository root:

```bash
npm create vite@latest web -- --template react-ts
cd web
npm install
npm install @tanstack/react-router @tanstack/react-query react-hook-form @hookform/resolvers zod recharts sonner lucide-react clsx tailwind-merge class-variance-authority
npm install -D tailwindcss @tailwindcss/vite vitest jsdom @testing-library/react @testing-library/jest-dom @testing-library/user-event msw @types/node prettier eslint-config-prettier
npm install -D @playwright/test @axe-core/playwright
```

Run the shadcn initializer and add only the primitives this plan uses:

```bash
npx shadcn@latest init
npx shadcn@latest add button input label card dialog dropdown-menu select badge table sheet switch textarea alert skeleton sonner tabs separator
```

If the CLI prompts, choose CSS variables and a neutral base color. If it
cannot run non-interactively, copy the same primitives from the shadcn/ui
docs; their public APIs are what later tasks use.

The initializer writes `src/lib/utils.ts` with a `cn` helper. Either keep that
file and have later tasks import `cn` from `@/lib/utils`, or delete it and set
`"aliases": { "utils": "@/lib/cn" }` in `components.json` before adding the
primitives. This plan uses `@/lib/cn`; do not keep two `cn` implementations.

- [ ] **Step 4: Configure Vite, TypeScript, scripts, and styles**

Replace `web/vite.config.ts` with:

```ts
import { fileURLToPath, URL } from 'node:url';
import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    proxy: { '/api': { target: 'http://localhost:8080' } },
  },
  test: {
    environment: 'jsdom',
    environmentOptions: { jsdom: { url: 'http://localhost:5173' } },
    setupFiles: ['./src/test/setup.ts'],
    css: true,
  },
});
```

In `web/tsconfig.app.json`, add:

```json
"baseUrl": ".",
"paths": { "@/*": ["src/*"] }
```

In `web/package.json`, set the scripts:

```json
"dev": "vite",
"build": "tsc -b && vite build",
"lint": "eslint .",
"typecheck": "tsc -b",
"test": "vitest",
"format": "prettier --write .",
"format:check": "prettier --check .",
"e2e": "playwright test"
```

Replace `web/src/styles.css` with `@import "tailwindcss";` plus the CSS
variables the shadcn initializer wrote. Delete the Vite starter `App.tsx`,
`App.css`, and image assets. Replace `main.tsx` with a minimal placeholder so
the scaffold still builds before Task 6 replaces it:

```tsx
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <main className="p-8">o1 dashboard</main>
  </StrictMode>,
);
```

If ESLint reports `react-refresh/only-export-components` on shadcn primitives,
add an override for `src/components/ui/**` rather than weakening the rule
globally.

Create `web/.prettierrc.json` with `{ "singleQuote": true, "trailingComma": "all" }`
and `web/.prettierignore` with `dist`, `node_modules`, `playwright-report`, and
`test-results`. Run `npm run format` once so the scaffold and shadcn files are
already clean for `format:check`.

Create `web/src/lib/cn.ts`:

```ts
import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
```

- [ ] **Step 5: Add the test harness**

Create `web/src/test/setup.ts`:

```ts
import '@testing-library/jest-dom/vitest';
import { afterAll, afterEach, beforeAll } from 'vitest';
import { cleanup } from '@testing-library/react';
import { server } from './msw/server';

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  cleanup();
  server.resetHandlers();
});
afterAll(() => server.close());
```

Create `web/src/test/msw/server.ts`:

```ts
import { setupServer } from 'msw/node';
import { handlers } from './handlers';

export const server = setupServer(...handlers);
```

Create `web/src/test/msw/handlers.ts`:

```ts
export const handlers = [];
```

Create `web/src/test/render.tsx`:

```tsx
import type { ReactElement } from 'react';
import { render, type RenderResult } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
}

export function renderWithProviders(
  ui: ReactElement,
  options: { queryClient?: QueryClient } = {},
): RenderResult {
  const queryClient = options.queryClient ?? createTestQueryClient();
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  );
}
```

- [ ] **Step 6: Run the smoke test and the full checks**

Run:

```bash
cd web
npm test -- --run src/test/smoke.test.tsx
npm run lint
npm run typecheck
npm run build
```

Expected: all PASS, `dist/` produced.

- [ ] **Step 7: Commit**

```bash
git add web
git commit -m "chore: scaffold react dashboard"
```

---

### Task 4: Typed API client and shared utilities

**Files:**
- Create: `web/src/lib/api/types.ts`
- Create: `web/src/lib/api/problems.ts`
- Create: `web/src/lib/api/client.ts`
- Create: `web/src/lib/query/client.ts`
- Create: `web/src/lib/query/keys.ts`
- Create: `web/src/lib/query/me.ts`
- Create: `web/src/lib/query/pagination.ts`
- Create: `web/src/lib/format.ts`
- Create: `web/src/lib/forms.ts`
- Create: `web/src/lib/workspace-status.ts`
- Create: `web/src/lib/auth-redirect.ts`
- Create: `web/src/lib/api/problems.test.ts`, `web/src/lib/api/client.test.ts`
- Create: `web/src/lib/format.test.ts`, `web/src/lib/forms.test.ts`,
  `web/src/lib/workspace-status.test.ts`, `web/src/lib/auth-redirect.test.ts`
- Create: `web/src/lib/query/pagination.test.ts`

**Interfaces:**
- Produces: `ApiProblem`, `ApiError`, `isApiProblem`, `parseProblem(response)`, `apiRequest<T>(path, options)`, `setUnauthenticatedHandler(handler)`.
- Produces: all API response types listed below.
- Produces: `queryKeys`, `createQueryClient()`, `queryClient`, `getMe()`, `meQueryOptions()`, `useMe()`.
- Produces: `nextPageParam(last: { next_cursor?: string }): string | undefined`.
- Produces: `formatUptime(uptime, checks)`, `formatLatency(ms)`, `formatRelativeTime(iso, now?)`.
- Produces: `applyProblemToForm(problem, form, fieldMap): string[]`.
- Produces: `markWorkspaceSuspended()`, `clearWorkspaceSuspended()`, `getWorkspaceSuspended()`, `useWorkspaceSuspended()`, `resetWorkspaceStatusForTests()`.
- Produces: `shouldRedirectToLogin(pathname)` and `buildLoginSearch(pathname, search)`, which keep background 401s from redirecting public auth pages.

- [ ] **Step 1: Write the failing tests**

Create `web/src/lib/api/problems.test.ts`:

```ts
import { describe, expect, test } from 'vitest';
import { parseProblem } from './problems';

describe('parseProblem', () => {
  test('parses an RFC 9457 problem with fields', async () => {
    const response = new Response(
      JSON.stringify({
        type: 'about:blank',
        title: 'Validation Error',
        status: 400,
        code: 'validation_error',
        fields: { url: 'url is not allowed' },
      }),
      { status: 400, headers: { 'Content-Type': 'application/problem+json' } },
    );
    await expect(parseProblem(response)).resolves.toMatchObject({
      code: 'validation_error',
      fields: { url: 'url is not allowed' },
    });
  });

  test('falls back to internal_error for a non-problem response', async () => {
    const response = new Response('boom', { status: 500 });
    await expect(parseProblem(response)).resolves.toMatchObject({
      status: 500,
      code: 'internal_error',
    });
  });
});
```

Create `web/src/lib/api/client.test.ts`:

```ts
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { apiRequest, setUnauthenticatedHandler } from './client';
import type { Me } from './types';

const ORIGIN = 'http://localhost:5173';
const problem = (body: object, status: number) =>
  HttpResponse.json(body, {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });

describe('apiRequest', () => {
  beforeEach(() => setUnauthenticatedHandler(null));

  test('returns JSON for a safe request', async () => {
    server.use(
      http.get(`${ORIGIN}/api/v1/me`, () =>
        HttpResponse.json<Me>({
          user: { id: 'u1', email: 'a@example.com', is_admin: false },
          workspaces: [],
        }),
      ),
    );
    await expect(apiRequest<Me>('/me')).resolves.toMatchObject({
      user: { email: 'a@example.com' },
    });
  });

  test('throws ApiError with the problem code and fields', async () => {
    server.use(
      http.post(`${ORIGIN}/api/v1/workspaces`, () =>
        problem(
          { type: 'about:blank', title: 'Validation Error', status: 400, code: 'validation_error', fields: { name: 'bad' } },
          400,
        ),
      ),
    );
    await expect(
      apiRequest('/workspaces', { method: 'POST', body: { name: '' } }),
    ).rejects.toMatchObject({
      problem: { code: 'validation_error', fields: { name: 'bad' } },
    });
  });

  test('calls the handler for unauthenticated but not invalid_credentials', async () => {
    const handler = vi.fn();
    setUnauthenticatedHandler(handler);
    server.use(
      http.get(`${ORIGIN}/api/v1/me`, () =>
        problem(
          { type: 'about:blank', title: 'Authentication required', status: 401, code: 'unauthenticated' },
          401,
        ),
      ),
      http.post(`${ORIGIN}/api/v1/auth/login`, () =>
        problem(
          { type: 'about:blank', title: 'Invalid email or password', status: 401, code: 'invalid_credentials' },
          401,
        ),
      ),
    );

    await expect(apiRequest('/me')).rejects.toBeDefined();
    await expect(
      apiRequest('/auth/login', { method: 'POST', body: {} }),
    ).rejects.toBeDefined();
    expect(handler).toHaveBeenCalledTimes(1);
  });
});
```

Create `web/src/lib/format.test.ts`:

```ts
import { describe, expect, test } from 'vitest';
import { formatLatency, formatRelativeTime, formatUptime } from './format';

describe('formatUptime', () => {
  test('renders no data for null or zero checks', () => {
    expect(formatUptime(null, 0)).toBe('No data');
    expect(formatUptime(0.99, 0)).toBe('No data');
  });
  test('renders one decimal', () => {
    expect(formatUptime(0.999, 10)).toBe('99.9%');
    expect(formatUptime(1, 10)).toBe('100.0%');
  });
});

describe('formatLatency', () => {
  test('rounds and labels milliseconds', () => {
    expect(formatLatency(12.6)).toBe('13 ms');
    expect(formatLatency(null)).toBe('—');
  });
});

describe('formatRelativeTime', () => {
  test('uses a relative hint against a fixed clock', () => {
    const now = new Date('2026-11-05T12:00:00Z');
    expect(formatRelativeTime('2026-11-05T11:58:00Z', now)).toBe('2 minutes ago');
  });
});
```

Create `web/src/lib/query/pagination.test.ts`:

```ts
import { expect, test } from 'vitest';
import { nextPageParam } from './pagination';

test('nextPageParam ignores absent and empty cursors', () => {
  expect(nextPageParam({})).toBeUndefined();
  expect(nextPageParam({ next_cursor: '' })).toBeUndefined();
  expect(nextPageParam({ next_cursor: 'abc' })).toBe('abc');
});
```

Create `web/src/lib/auth-redirect.test.ts`:

```ts
import { expect, test } from 'vitest';
import { buildLoginSearch, shouldRedirectToLogin } from './auth-redirect';

test('public auth pages never trigger a login redirect', () => {
  expect(shouldRedirectToLogin('/login')).toBe(false);
  expect(shouldRedirectToLogin('/register')).toBe(false);
  expect(shouldRedirectToLogin('/verify-email')).toBe(false);
  expect(shouldRedirectToLogin('/forgot-password')).toBe(false);
  expect(shouldRedirectToLogin('/reset-password')).toBe(false);
  expect(buildLoginSearch('/login', '')).toEqual({});
});

test('app pages build a redirect target', () => {
  expect(shouldRedirectToLogin('/w/acme/monitors')).toBe(true);
  expect(buildLoginSearch('/w/acme/monitors', '?page=2')).toEqual({
    redirect: '/w/acme/monitors?page=2',
  });
});
```

Create `web/src/lib/workspace-status.test.ts`:

```ts
import { beforeEach, expect, test } from 'vitest';
import {
  clearWorkspaceSuspended,
  getWorkspaceSuspended,
  markWorkspaceSuspended,
  resetWorkspaceStatusForTests,
} from './workspace-status';

beforeEach(() => resetWorkspaceStatusForTests());

test('suspension store toggles', () => {
  expect(getWorkspaceSuspended()).toBe(false);
  markWorkspaceSuspended();
  expect(getWorkspaceSuspended()).toBe(true);
  clearWorkspaceSuspended();
  expect(getWorkspaceSuspended()).toBe(false);
});
```

Create `web/src/lib/forms.test.ts` with a small react-hook-form form; assert
that `applyProblemToForm` calls `setError` for a mapped field and returns the
unmapped messages.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/lib`

Expected: FAIL because the modules do not exist.

- [ ] **Step 3: Implement the types**

Create `web/src/lib/api/types.ts` with these exported types:

```ts
export type Role = 'viewer' | 'member' | 'admin' | 'owner';
export type HttpMethod = 'GET' | 'HEAD' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
export type MonitorStatus = 'up' | 'down' | 'unknown' | 'paused';
export type ResultsWindow = '1h' | '6h' | '24h' | '7d';

export interface User { id: string; email: string; is_admin: boolean; }
export interface Workspace { id: string; name: string; slug: string; role: Role; }
export interface Me { user: User; workspaces: Workspace[]; }
export interface Member { user_id: string; email: string; role: Role; }

export interface LastCheck {
  checked_at: string;
  status_code: number | null;
  latency_ms: number | null;
  error: string | null;
}

export interface Summary {
  window: string;
  uptime: number | null;
  checks: number;
  up: number;
  down: number;
  avg_latency_ms: number;
  p95_latency_ms: number;
  min_latency_ms: number;
  max_latency_ms: number;
}

export interface Monitor {
  id: string;
  workspace_id: string;
  name: string;
  url: string;
  method: HttpMethod;
  headers: Record<string, string>;
  has_body: boolean;
  content_type: string | null;
  expected_status: string;
  interval_seconds: number;
  timeout_seconds: number;
  confirmation_threshold: number;
  recovery_threshold: number;
  paused: boolean;
  status: MonitorStatus;
  last_check: LastCheck | null;
  summary_24h: Summary | null;
  created_at: string;
  updated_at: string;
}

export interface ProbeResult {
  id: string;
  checked_at: string;
  scheduled_for: string;
  status: 'up' | 'down';
  status_code: number | null;
  latency_ms: number;
  error: string | null;
}

export interface CreateMonitorInput {
  name: string;
  url: string;
  method: HttpMethod;
  headers?: Record<string, string>;
  body?: string | null;
  content_type?: string | null;
  expected_status: string;
  interval_seconds: number;
  timeout_seconds: number;
  confirmation_threshold: number;
  recovery_threshold: number;
}

export interface UpdateMonitorInput {
  name?: string;
  url?: string;
  method?: HttpMethod;
  headers?: Record<string, string>;
  body?: string;
  content_type?: string;
  expected_status?: string;
  interval_seconds?: number;
  timeout_seconds?: number;
  confirmation_threshold?: number;
  recovery_threshold?: number;
}

export interface MonitorListResponse { monitors: Monitor[]; next_cursor?: string; }
export interface ResultsResponse { results: ProbeResult[]; next_cursor?: string; }
export interface WorkspaceListResponse { workspaces: Workspace[]; }
export interface MemberListResponse { members: Member[]; }

export interface AdminWorkspace {
  id: string;
  name: string;
  slug: string;
  created_by: string;
  created_at: string;
  monitor_limit: number;
  suspended_at: string | null;
  suspended_by: string | null;
  suspended_reason: string | null;
  member_count: number;
  monitor_count: number;
}

export interface AdminMonitor {
  id: string;
  workspace_id: string;
  workspace_name: string;
  workspace_slug: string;
  name: string;
  url: string;
  method: HttpMethod;
  expected_status: string;
  interval_seconds: number;
  timeout_seconds: number;
  paused: boolean;
  status: MonitorStatus;
  last_checked_at: string | null;
  last_status_code: number | null;
  last_latency_ms: number | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
}

export interface SecurityEvent {
  id: string;
  workspace_id: string | null;
  actor_user_id: string | null;
  kind: string;
  detail: unknown;
  created_at: string;
}

export interface AdminWorkspaceListResponse { workspaces: AdminWorkspace[]; next_cursor?: string; }
export interface AdminMonitorListResponse { monitors: AdminMonitor[]; next_cursor?: string; }
export interface SecurityEventListResponse { events: SecurityEvent[]; next_cursor?: string; }

export interface AdminWorkspaceFilters { query: string; suspended?: boolean; }
export interface AdminMonitorFilters { query: string; workspaceId?: string; }
export interface SecurityEventFilters { kind?: string; workspaceId?: string; }
```

- [ ] **Step 4: Implement the problem parser and client**

`web/src/lib/api/problems.ts` exports:

```ts
export interface ApiProblem {
  type: string;
  title: string;
  status: number;
  detail?: string;
  code: string;
  fields?: Record<string, string>;
}

export function isProblemBody(value: unknown): boolean;
export async function parseProblem(response: Response): Promise<ApiProblem>;
```

`parseProblem` reads the JSON once, accepts `application/problem+json` or any
JSON body with a string `code`, and falls back to
`{ type: 'about:blank', title: response.statusText || 'Request failed', status, code: 'internal_error' }`
when parsing fails.

`web/src/lib/api/client.ts`:

```ts
export interface ApiRequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  body?: unknown;
  query?: Record<string, string | number | boolean | null | undefined>;
  signal?: AbortSignal;
}

export class ApiError extends Error {
  readonly problem: ApiProblem;
  constructor(problem: ApiProblem) {
    super(problem.detail ?? problem.title);
    this.name = 'ApiError';
    this.problem = problem;
  }
}

export function isApiProblem(error: unknown): error is ApiError;

export function setUnauthenticatedHandler(handler: (() => void) | null): void;

export async function apiRequest<T>(
  path: string,
  options: ApiRequestOptions = {},
): Promise<T>;
```

Behavior:

- Build `new URL(`/api/v1${path}`, window.location.origin)`, append defined query
  values with `searchParams.set`.
- `credentials: 'same-origin'`.
- Set `Content-Type: application/json` only when `body !== undefined`.
- `204` and empty bodies resolve to `undefined as T`.
- Non-OK responses parse the problem and throw `ApiError`; if
  `problem.code === 'unauthenticated'`, call the registered handler once; if
  `problem.code === 'workspace_suspended'`, call `markWorkspaceSuspended()`.
- On a successful unsafe request, call `clearWorkspaceSuspended()`.

- [ ] **Step 5: Implement query plumbing and utilities**

`web/src/lib/query/client.ts` exports `createQueryClient()` and the singleton
`queryClient` with `retry: false`, `staleTime: 10_000`, and
`refetchOnWindowFocus: true`.

`web/src/lib/query/keys.ts` exports the key object from the design:

```ts
export const queryKeys = {
  me: ['me'] as const,
  monitors: (workspaceId: string) => ['monitors', workspaceId] as const,
  monitor: (workspaceId: string, monitorId: string) => ['monitor', workspaceId, monitorId] as const,
  results: (workspaceId: string, monitorId: string, window: ResultsWindow) => ['results', workspaceId, monitorId, window] as const,
  summary: (workspaceId: string, monitorId: string, window: ResultsWindow) => ['summary', workspaceId, monitorId, window] as const,
  members: (workspaceId: string) => ['members', workspaceId] as const,
  adminWorkspaces: (filters: AdminWorkspaceFilters) => ['admin', 'workspaces', filters] as const,
  adminMonitors: (filters: AdminMonitorFilters) => ['admin', 'monitors', filters] as const,
  adminSecurityEvents: (filters: SecurityEventFilters) => ['admin', 'security-events', filters] as const,
};
```

`web/src/lib/query/me.ts`:

```ts
export function getMe(): Promise<Me> {
  return apiRequest<Me>('/me');
}

export const meQueryOptions = () =>
  queryOptions({ queryKey: queryKeys.me, queryFn: getMe, staleTime: 60_000 });

export function useMe() {
  return useQuery(meQueryOptions());
}
```

`web/src/lib/query/pagination.ts`:

```ts
export function nextPageParam(last: { next_cursor?: string }): string | undefined {
  return last.next_cursor || undefined;
}
```

`web/src/lib/format.ts`:

```ts
export function formatUptime(uptime: number | null | undefined, checks: number | undefined): string;
export function formatLatency(ms: number | null | undefined): string;
export function formatRelativeTime(iso: string, now?: Date): string;
```

`formatRelativeTime` uses `Intl.RelativeTimeFormat('en', { numeric: 'auto' })`
and picks seconds, minutes, hours, or days from the absolute difference.

`web/src/lib/forms.ts`:

```ts
export function applyProblemToForm<T extends FieldValues>(
  problem: ApiProblem,
  form: UseFormReturn<T>,
  fieldMap: Record<string, keyof T>,
): string[];
```

It calls `form.setError(field, { message })` for mapped keys and returns the
messages of unmapped keys.

`web/src/lib/workspace-status.ts` implements a `useSyncExternalStore` boolean
store with `subscribe`, `getWorkspaceSuspended`, `markWorkspaceSuspended`,
`clearWorkspaceSuspended`, `useWorkspaceSuspended`, and
`resetWorkspaceStatusForTests`. The server snapshot returns `false`; the test
reset only mutates the module-level value.

`web/src/lib/auth-redirect.ts`:

```ts
const PUBLIC_AUTH_PATHS = [
  '/login',
  '/register',
  '/verify-email',
  '/forgot-password',
  '/reset-password',
];

export function shouldRedirectToLogin(pathname: string): boolean {
  return !PUBLIC_AUTH_PATHS.some(
    (path) => pathname === path || pathname.startsWith(`${path}/`),
  );
}

export function buildLoginSearch(
  pathname: string,
  search: string,
): { redirect?: string } {
  return shouldRedirectToLogin(pathname)
    ? { redirect: `${pathname}${search}` }
    : {};
}
```

- [ ] **Step 6: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/lib
npm run lint
npm run typecheck
npm run build
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/lib
git commit -m "feat: add typed api client and shared ui utilities"
```

---

### Task 5: Auth data layer, schemas, roles, and guards

**Files:**
- Create: `web/src/features/auth/schemas.ts`
- Create: `web/src/features/auth/api.ts`
- Create: `web/src/features/auth/guards.ts`
- Create: `web/src/lib/roles.ts`
- Create: `web/src/lib/last-workspace.ts`
- Create: `web/src/features/auth/schemas.test.ts`, `api.test.ts`, `guards.test.ts`
- Create: `web/src/lib/roles.test.ts`, `last-workspace.test.ts`

**Interfaces:**
- Produces: `loginSchema`, `registerSchema`, `forgotPasswordSchema`, `resetPasswordSchema`, `verifyEmailSchema`, and their inferred types.
- Produces: `register`, `verifyEmail`, `resendVerification`, `login`, `logout`, `forgotPassword`, `resetPassword`.
- Produces: `ROLE_RANK`, `roleAtLeast`, `canManageMembers`, `canManageMonitors`, `assignableRoles`.
- Produces: `safeRedirect`, `findWorkspace`, `isUnauthenticated`.
- Produces: `LAST_WORKSPACE_KEY`, `readLastWorkspaceSlug`, `writeLastWorkspaceSlug`.

- [ ] **Step 1: Write the failing tests**

`web/src/features/auth/schemas.test.ts`:

```ts
import { describe, expect, test } from 'vitest';
import { loginSchema, registerSchema, resetPasswordSchema } from './schemas';

describe('auth schemas', () => {
  test('trims and lowercases emails', () => {
    const parsed = loginSchema.parse({ email: '  Alice@Example.COM ', password: 'x' });
    expect(parsed.email).toBe('alice@example.com');
  });

  test('register requires matching passwords of at least 12 characters', () => {
    const base = { email: 'a@example.com', password: 'short', confirmPassword: 'short' };
    expect(registerSchema.safeParse(base).success).toBe(false);
    expect(
      registerSchema.safeParse({
        email: 'a@example.com',
        password: 'correct horse battery staple',
        confirmPassword: 'different password value',
      }).success,
    ).toBe(false);
  });

  test('reset requires matching passwords', () => {
    expect(
      resetPasswordSchema.safeParse({
        password: 'correct horse battery staple',
        confirmPassword: 'correct horse battery staple',
      }).success,
    ).toBe(true);
  });
});
```

`web/src/lib/roles.test.ts`:

```ts
import { expect, test } from 'vitest';
import { assignableRoles, canManageMembers, canManageMonitors, roleAtLeast } from './roles';

test('role hierarchy', () => {
  expect(roleAtLeast('admin', 'member')).toBe(true);
  expect(roleAtLeast('viewer', 'member')).toBe(false);
  expect(canManageMembers('admin')).toBe(true);
  expect(canManageMembers('member')).toBe(false);
  expect(canManageMonitors('viewer')).toBe(false);
  expect(canManageMonitors('member')).toBe(true);
});

test('assignable roles never include owner', () => {
  expect(assignableRoles('owner')).toEqual(['viewer', 'member', 'admin']);
  expect(assignableRoles('admin')).toEqual(['viewer', 'member']);
  expect(assignableRoles('member')).toEqual([]);
});
```

`web/src/features/auth/guards.test.ts`:

```ts
import { expect, test } from 'vitest';
import { findWorkspace, safeRedirect } from './guards';
import type { Me } from '@/lib/api/types';

test('safeRedirect rejects external and protocol-relative paths', () => {
  expect(safeRedirect(undefined)).toBe('/');
  expect(safeRedirect('https://evil.example')).toBe('/');
  expect(safeRedirect('//evil.example')).toBe('/');
  expect(safeRedirect('/\\evil.example')).toBe('/');
  expect(safeRedirect('javascript:alert(1)')).toBe('/');
  expect(safeRedirect('/w/acme/monitors')).toBe('/w/acme/monitors');
});

test('findWorkspace matches only workspace slugs', () => {
  const me: Me = {
    user: { id: 'u1', email: 'a@example.com', is_admin: false },
    workspaces: [{ id: 'w1', name: 'Acme', slug: 'acme', role: 'owner' }],
  };
  expect(findWorkspace(me, 'acme')?.id).toBe('w1');
  expect(findWorkspace(me, 'missing')).toBeUndefined();
});
```

`web/src/features/auth/api.test.ts`: use MSW to assert each function calls the
right method/path/body; for example, `login` POSTs `{email,password}` to
`/api/v1/auth/login`, `resetPassword` POSTs `{token,new_password}`, and
`verifyEmail` POSTs `{token}`.

`web/src/lib/last-workspace.test.ts` asserts a missing key returns `null` and a
written slug round-trips, clearing `localStorage` in `beforeEach`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/auth src/lib/roles.test.ts src/lib/last-workspace.test.ts`

Expected: FAIL because the modules do not exist.

- [ ] **Step 3: Implement**

`web/src/features/auth/schemas.ts`:

```ts
export const emailSchema = z
  .string()
  .trim()
  .toLowerCase()
  .email('Enter a valid email address');

export const passwordSchema = z
  .string()
  .min(12, 'Password must be at least 12 characters')
  .max(128, 'Password must be at most 128 characters');

export const loginSchema = z.object({
  email: emailSchema,
  password: z.string().min(1, 'Password is required'),
});

export const registerSchema = z
  .object({
    email: emailSchema,
    password: passwordSchema,
    confirmPassword: z.string(),
  })
  .refine((v) => v.password === v.confirmPassword, {
    path: ['confirmPassword'],
    message: 'Passwords do not match',
  });

export const forgotPasswordSchema = z.object({ email: emailSchema });

export const resetPasswordSchema = z
  .object({ password: passwordSchema, confirmPassword: z.string() })
  .refine((v) => v.password === v.confirmPassword, {
    path: ['confirmPassword'],
    message: 'Passwords do not match',
  });

export const verifyEmailSchema = z.object({ token: z.string().min(1) });
```

`web/src/features/auth/api.ts` uses `apiRequest` for each endpoint, with these
exact paths and bodies:

- `POST /auth/register` `{email,password}` → `{id,email}`
- `POST /auth/verify-email` `{token}`
- `POST /auth/verify-email/resend` `{email}`
- `POST /auth/login` `{email,password}` → `{user: {id,email}}` (the login
  response does not include `is_admin`; refetch `/me`)
- `POST /auth/logout` (no body)
- `POST /auth/password/forgot` `{email}`
- `POST /auth/password/reset` `{token,new_password}`

`web/src/lib/roles.ts` implements the rank map and helpers exactly as the tests
assert.

`web/src/features/auth/guards.ts` implements `safeRedirect`, `findWorkspace`,
and `isUnauthenticated(error): boolean` (returns true for an `ApiError` whose
problem code is `unauthenticated`). `safeRedirect` accepts only a pathname that
starts with a single `/` and contains no backslash or control characters;
everything else returns `/`.

`web/src/lib/last-workspace.ts`:

```ts
export const LAST_WORKSPACE_KEY = 'o1.lastWorkspaceSlug';

export function readLastWorkspaceSlug(): string | null;
export function writeLastWorkspaceSlug(slug: string): void;
```

Both wrap `localStorage` in `try/catch`.

- [ ] **Step 4: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/features/auth src/lib/roles.test.ts src/lib/last-workspace.test.ts
npm run lint
npm run typecheck
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/auth web/src/lib/roles.ts web/src/lib/roles.test.ts web/src/lib/last-workspace.ts web/src/lib/last-workspace.test.ts
git commit -m "feat: add auth data layer and role helpers"
```

---

### Task 6: Auth screens, public layout, and the router shell

**Files:**
- Create: `web/src/features/auth/PublicLayout.tsx`
- Create: `web/src/features/auth/LoginScreen.tsx`, `RegisterScreen.tsx`, `VerifyEmailScreen.tsx`, `ForgotPasswordScreen.tsx`, `ResetPasswordScreen.tsx`
- Create: `web/src/components/feedback/ProblemAlert.tsx`
- Create: `web/src/components/layout/RootLayout.tsx`
- Create: `web/src/router.tsx`
- Modify: `web/src/main.tsx`
- Create: `web/src/features/auth/LoginScreen.test.tsx`, `RegisterScreen.test.tsx`, `VerifyEmailScreen.test.tsx`, `ForgotPasswordScreen.test.tsx`, `ResetPasswordScreen.test.tsx`

**Interfaces:**
- Consumes: Task 4 client/utilities and Task 5 auth API/schemas.
- Produces: `router` (a typed TanStack Router instance) used by `main.tsx`.
- Produces `ProblemAlert` with `{ problem?: ApiProblem | null; className?: string }`.
- Produces `RootLayout` rendering `<Outlet />` and the global `<Toaster />`.
- Produces screen props: `LoginScreen({ redirectTo?: string })`,
  `VerifyEmailScreen({ token?: string })`, `ResetPasswordScreen({ token?: string })`,
  `RegisterScreen()`, `ForgotPasswordScreen()`.
- Produces: public routes `/login`, `/register`, `/verify-email`,
  `/forgot-password`, `/reset-password`.
- Produces: an authenticated placeholder route at `/` and `/workspaces` that
  later tasks replace.

- [ ] **Step 1: Write the failing tests**

Add a reusable router mock snippet at the top of each screen test:

```tsx
const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>();
  return {
    ...actual,
    useNavigate: () => navigate,
    Link: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
      <a {...props}>{children}</a>
    ),
  };
});
```

`LoginScreen.test.tsx` asserts:

- Submitting a malformed email shows `Enter a valid email address` and makes
  no API call.
- A valid submit calls `POST /api/v1/auth/login` and navigates to `redirectTo`.
- A `401 invalid_credentials` response shows `Invalid email or password`.
- A `403 email_not_verified` response shows the verification message and a
  resend control.

`RegisterScreen.test.tsx` asserts:

- Password mismatch shows `Passwords do not match`.
- A successful register shows `Check your email`.
- A `409 email_taken` response shows `Email already registered`.

`VerifyEmailScreen.test.tsx` asserts:

- With a token, it calls `POST /api/v1/auth/verify-email` and shows
  `Email verified`.
- A `400 token_invalid` response shows `This verification link is invalid or expired`.
- With no token, it shows `This verification link is missing its token` and
  makes no API call.

`ForgotPasswordScreen.test.tsx` asserts that any valid email shows the same
neutral `If an account exists, we sent a reset link.` message.

`ResetPasswordScreen.test.tsx` asserts mismatch validation, success copy, and
the expired-token recovery link.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/auth`

Expected: FAIL because the screens do not exist.

- [ ] **Step 3: Implement the public layout and screens**

`PublicLayout` renders a centered card, a link home, and `<Outlet />`.
`ProblemAlert` renders the problem `title`, optional `detail`, and the stable
`code` in small text so support can quote it; it never renders `fields` as a
list because forms own field errors.

Each screen uses `useForm` with `zodResolver`, `FormField`-style markup with
`Label`, `Input`, and inline error text tied by `aria-describedby`. The submit
button shows a pending label and is disabled while submitting. API errors go
through `ProblemAlert`, except field errors, which are set on the form.

`LoginScreen` on success awaits
`queryClient.invalidateQueries({ queryKey: queryKeys.me })`, then navigates to
`safeRedirect(redirectTo)`.

`RegisterScreen` keeps a local `submittedEmail` state; after success it renders
the "Check your email" panel with a resend button that calls
`resendVerification(submittedEmail)`.

`VerifyEmailScreen` verifies once on mount when `token` is present, using a
`useRef` guard. It renders one of: verifying, verified, invalid/missing token.

`ForgotPasswordScreen` always renders the neutral success message after a
successful submission.

`ResetPasswordScreen` reads `token` from props; after success it shows a link to
`/login`; `token_invalid` shows a link to `/forgot-password`.

- [ ] **Step 4: Build the router shell**

`web/src/router.tsx`:

```tsx
const rootRoute = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootLayout,
});

const publicLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'public',
  beforeLoad: async ({ context }) => {
    try {
      await context.queryClient.ensureQueryData(meQueryOptions());
      throw redirect({ to: '/' });
    } catch (error) {
      if (isRedirect(error)) throw error;
      if (!isUnauthenticated(error)) throw error;
    }
  },
  component: PublicLayout,
});

// child routes: /login, /register, /verify-email, /forgot-password,
// /reset-password. The verify and reset routes validate
// `{ token?: string }` and pass it to the screen.

const routeTree = rootRoute.addChildren([
  publicLayoutRoute.addChildren([
    loginRoute,
    registerRoute,
    verifyEmailRoute,
    forgotPasswordRoute,
    resetPasswordRoute,
  ]),
  appLayoutRoute.addChildren([homeRoute, workspacesRoute, workspaceRoute]),
]);
```

Add an authenticated layout route with `id: 'app'` whose `beforeLoad` calls
`ensureQueryData(meQueryOptions())` and, on `unauthenticated`, redirects to
`/login` with `search: buildLoginSearch(pathname, search)`, preserving the
intended destination. For this task its
component renders a simple header with the user's email, a Log out button, and
`<Outlet />`; Task 7 replaces it with `AppShell`.

Add `/` and `/workspaces` placeholder routes inside the app layout so login has
a destination. Task 7 replaces them.

`web/src/main.tsx`:

```tsx
import { buildLoginSearch, shouldRedirectToLogin } from './lib/auth-redirect';

setUnauthenticatedHandler(() => {
  queryClient.clear();
  const { pathname, search } = window.location;
  if (shouldRedirectToLogin(pathname)) {
    void router.navigate({
      to: '/login',
      search: buildLoginSearch(pathname, search),
      replace: true,
    });
  }
});

const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: 'intent',
});

declare module '@tanstack/react-router' {
  interface Register { router: typeof router }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
```

`RootLayout` renders `<Outlet />` and `<Toaster richColors position="top-right" />`.

- [ ] **Step 5: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/features/auth
npm run lint
npm run typecheck
npm run build
```

Expected: PASS and a successful production build.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/auth web/src/components/feedback/ProblemAlert.tsx web/src/components/layout/RootLayout.tsx web/src/lib/auth-redirect.ts web/src/lib/auth-redirect.test.ts web/src/router.tsx web/src/main.tsx
git commit -m "feat: add auth screens and route guards"
```

---

### Task 7: App shell, workspace management, and members

**Files:**
- Create: `web/src/components/layout/AppShell.tsx`, `Sidebar.tsx`, `WorkspaceSwitcher.tsx`, `UserMenu.tsx`
- Create: `web/src/components/feedback/EmptyState.tsx`, `ErrorPanel.tsx`, `ConfirmDialog.tsx`, `Skeletons.tsx`
- Create: `web/src/features/workspaces/api.ts`, `schemas.ts`, `WorkspaceListScreen.tsx`, `WorkspaceCreateDialog.tsx`, `MembersScreen.tsx`, `AddMemberDialog.tsx`
- Modify: `web/src/router.tsx`
- Create: `web/src/features/workspaces/WorkspaceListScreen.test.tsx`, `MembersScreen.test.tsx`, `schemas.test.ts`
- Create: `web/src/components/layout/WorkspaceSwitcher.test.tsx`

**Interfaces:**
- Consumes: `useMe()`, roles helpers, `ProblemAlert`.
- Produces: `createWorkspace(name)`, `listMembers(workspaceId)`,
  `addMember(workspaceId, input)`.
- Produces: `workspaceNameSchema` (1–80 characters).
- Produces: `useCreateWorkspace()`, `useMembers(workspaceId)`,
  `useAddMember(workspaceId)`, `useLogout()`.
- Produces shared feedback contracts:

```ts
interface EmptyStateProps {
  title: string;
  description?: string;
  action?: React.ReactNode;
}

interface ErrorPanelProps {
  title?: string;
  message: string;
  onRetry?: () => void;
}

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  description?: string;
  confirmLabel: string;
  pending?: boolean;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}

function PageSkeleton(): React.ReactElement;
```
- Produces routes `/workspaces`, `/w/:workspaceSlug/members`, and the app shell
  with desktop sidebar and mobile sheet.

- [ ] **Step 1: Write the failing tests**

`WorkspaceListScreen.test.tsx` asserts:

- Renders a card per workspace with its role and a link to its monitors.
- The empty state shows `No workspaces yet` and a `New workspace` action.
- Creating a workspace POSTs `{name}` to `/api/v1/workspaces`, invalidates
  `['me']`, and navigates to the new workspace's monitor list.
- `workspace_suspended` from a mutation shows the persistent suspension banner.

`MembersScreen.test.tsx` asserts:

- Renders member emails and roles.
- An owner sees role options `viewer`, `member`, `admin` but never `owner`.
- An admin sees only `viewer` and `member`.
- A viewer or member sees no add-member action.
- Adding a member POSTs `{email, role}` and refreshes the list.
- `404 not_found` shows `No account with that email`.

`WorkspaceSwitcher.test.tsx` asserts that selecting a workspace navigates to
`/w/:slug/monitors` and that "All workspaces" navigates to `/workspaces`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/workspaces src/components/layout`

Expected: FAIL because the modules do not exist.

- [ ] **Step 3: Implement the workspace data layer**

`features/workspaces/api.ts`:

```ts
export function createWorkspace(name: string): Promise<Workspace>;
export function listMembers(workspaceId: string): Promise<Member[]>;
export function addMember(
  workspaceId: string,
  input: { email: string; role: Role },
): Promise<void>;
```

`features/workspaces/schemas.ts`:

```ts
export const workspaceNameSchema = z
  .string()
  .trim()
  .min(1, 'Workspace name is required')
  .max(80, 'Workspace name must be at most 80 characters');

export const addMemberSchema = z.object({
  email: z.string().trim().toLowerCase().email('Enter a valid email address'),
  role: z.enum(['viewer', 'member', 'admin']),
});
```

`useMembers` uses `queryKeys.members(workspaceId)` and a 60-second stale time.
`useAddMember` invalidates that key. `useCreateWorkspace` writes the returned
workspace into the `me` cache with `setQueryData`, awaits
`invalidateQueries({ queryKey: queryKeys.me })`, and only then navigates to the
new workspace. This avoids the workspace route guard missing the new slug.
`useLogout` calls `logout()`, then does a full-page navigation
with `window.location.assign('/login')` so no stale router state or background
`/me` refetch races the logout redirect.

- [ ] **Step 4: Implement the layout**

`AppShell`:

- Uses `useQuery(meQueryOptions())`.
- Reads the optional `workspaceSlug` param with
  `useParams({ strict: false })` and finds the current workspace in `me`.
- Desktop: fixed left sidebar with `WorkspaceSwitcher`, links to Monitors and
  Members when a workspace is active, an Admin section when
  `me.user.is_admin`, and `UserMenu`.
- Mobile: a top bar with a sheet trigger whose accessible name is
  `Open navigation` containing the same navigation.
- Renders the suspension banner when `useWorkspaceSuspended()` is true.
- Wraps `<Outlet />` in `<Suspense fallback={<PageSkeleton />}>`.
- Includes a skip-to-content link targeting `#main-content`.

`WorkspaceSwitcher` uses the shadcn `DropdownMenu`; links use TanStack
Router's `Link` with the workspace slug.

`UserMenu` shows the email and a `Log out` button.

- [ ] **Step 5: Implement the workspace screens and routes**

`WorkspaceListScreen({ workspaces })` renders cards/table and the create
dialog. The `New workspace` action is a button that opens
`WorkspaceCreateDialog`. `WorkspaceCreateDialog` uses `react-hook-form` +
`workspaceNameSchema`, with a `Name` field and a `Create workspace` submit
button.

`MembersScreen({ workspace })` renders the member table and the add dialog.
The add action is `Add member`; `AddMemberDialog` has `Email` and `Role`
fields and an `Add member` submit button. Role options come from
`assignableRoles(workspace.role)`; hide the action when that array is empty.

Add to `router.tsx`:

- `/workspaces` inside the app layout. The route component calls `useMe()` and
  passes `data.workspaces` to `WorkspaceListScreen`, so invalidating `me` after
  a create re-renders the list.
- `/w/$workspaceSlug` route with `beforeLoad` that resolves the workspace from
  `me.workspaces` and throws `notFound()` when absent; return `{ workspace }`.
- `/w/$workspaceSlug/monitors` and `/w/$workspaceSlug/members` children.
  Members passes `workspace` from `useRouteContext({ from: '/w/$workspaceSlug' })`.
- The index route `/` redirects to the last-used workspace from
  `readLastWorkspaceSlug()` or the first workspace, else `/workspaces`.
- When the workspace route renders, call `writeLastWorkspaceSlug(workspace.slug)`.

Use `React.lazy` for screen modules and a `Suspense` boundary in `AppShell`.

- [ ] **Step 6: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/features/workspaces src/components/layout
npm run lint
npm run typecheck
npm run build
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/components/layout web/src/components/feedback web/src/features/workspaces web/src/router.tsx
git commit -m "feat: add app shell and workspace management"
```

---

### Task 8: Monitor list with pause, resume, and delete

**Files:**
- Create: `web/src/features/monitors/api.ts`
- Create: `web/src/features/monitors/hooks.ts`
- Create: `web/src/features/monitors/status.ts`
- Create: `web/src/features/monitors/MonitorListScreen.tsx`
- Modify: `web/src/router.tsx`
- Create: `web/src/features/monitors/MonitorListScreen.test.tsx`, `hooks.test.ts`, `status.test.ts`

**Interfaces:**
- Produces: `listMonitors(workspaceId, cursor?)`, `getMonitor`,
  `createMonitor`, `updateMonitor`, `deleteMonitor`, `pauseMonitor`,
  `resumeMonitor`, `getResults`, `getSummary`.
- Produces: `useMonitors(workspaceId)`, `useMonitor`, `useCreateMonitor`,
  `useUpdateMonitor`, `usePauseMonitor`, `useResumeMonitor`,
  `useDeleteMonitor`, `useResults`, `useSummary`.
- `useCreateMonitor(workspaceId)` calls `createMonitor` and invalidates
  `queryKeys.monitors(workspaceId)` and `queryKeys.me`.
- `useUpdateMonitor(workspaceId, monitorId)` calls `updateMonitor` and
  invalidates `queryKeys.monitor(workspaceId, monitorId)`,
  `queryKeys.monitors(workspaceId)`, and the matching summary/results keys.
- `usePauseMonitor(workspaceId, monitorId)` and
  `useResumeMonitor(workspaceId, monitorId)` are separate hooks so the list
  and detail screens can share the optimistic logic.
- Produces: `statusLabel(status)`, `statusVariant(status)`, and
  `updateMonitorInList(pages, monitorId, patch)`.
- Produces: `MonitorListScreen({ workspace })` and the monitor list route.

- [ ] **Step 1: Write the failing tests**

`status.test.ts` asserts the four labels and badge variants.

`hooks.test.ts` asserts `updateMonitorInList` patches the matching monitor in
every page and leaves others untouched.

`MonitorListScreen.test.tsx` asserts:

- Renders name, the URL exactly as returned by the API (no client-side
  reconstruction), status label, last check, and uptime.
- A null `summary_24h` or zero checks renders `No data`.
- `Load more` appends the next page from `next_cursor`.
- Pause calls `POST /pause` and optimistically changes the badge to `paused`.
- A failed pause restores the previous badge.
- Resume calls `POST /resume`, optimistically clears `paused`, and refetches.
- A failed resume restores the paused badge.
- Delete asks for confirmation in a `ConfirmDialog` titled `Delete {name}?`
  with confirm label `Delete monitor`, then calls `DELETE`.
- A viewer sees no `New monitor`, pause, or delete controls.
- The empty state shows `No monitors yet`.

Use MSW handlers with one page then a second page keyed on the `cursor` query
parameter; assert the second request contains the cursor from the first
response.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/monitors`

Expected: FAIL because the modules do not exist.

- [ ] **Step 3: Implement the monitor API and hooks**

API paths:

- `GET /workspaces/{id}/monitors?cursor=&limit=50`
- `POST /workspaces/{id}/monitors`
- `GET|PATCH|DELETE /workspaces/{id}/monitors/{monitorId}`
- `POST /workspaces/{id}/monitors/{monitorId}/pause`
- `POST /workspaces/{id}/monitors/{monitorId}/resume`
- `GET /workspaces/{id}/monitors/{monitorId}/results?window=&cursor=&limit=100`
- `GET /workspaces/{id}/monitors/{monitorId}/summary?window=`

`useMonitors` uses `useInfiniteQuery`:

```ts
useInfiniteQuery({
  queryKey: queryKeys.monitors(workspaceId),
  queryFn: ({ pageParam, signal }) => listMonitors(workspaceId, pageParam, signal),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: nextPageParam,
  refetchInterval: 15_000,
});
```

`usePauseMonitor` optimistically updates `queryKeys.monitor` and every matching
row in `queryKeys.monitors`, with rollback in `onError` and invalidation in
`onSettled`. `useDeleteMonitor` invalidates the list and navigates back to it.

`useResults` is an infinite query with `initialPageParam: undefined` and
`nextPageParam`; `useSummary` is a normal query with a 30-second refetch
interval. Both are used by Task 10 but live here.

- [ ] **Step 4: Implement the list screen and route**

`MonitorListScreen({ workspace })`:

- Search input filters the already-loaded pages by name or URL (client-side).
- Table on `md+`, stacked cards below `md`.
- Status badge uses text plus color.
- Row actions: View, Pause/Resume, Delete, with accessible names `View {name}`,
  `Pause {name}` / `Resume {name}`, and `Delete {name}`. Hide mutation actions
  unless `canManageMonitors(workspace.role)`.
- Primary action `New monitor` links to `/w/$workspaceSlug/monitors/new`.
- `Load more` button appears only when `hasNextPage`; it is disabled while
  fetching.
- Empty state explains HTTP monitors and links to the create form.
- Page-level `ProblemAlert` for quota, rate-limit, and suspended errors.

Add the route `/w/$workspaceSlug/monitors` and pass the workspace from route
context.

- [ ] **Step 5: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/features/monitors
npm run lint
npm run typecheck
npm run build
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/monitors web/src/router.tsx
git commit -m "feat: add monitor list with pause resume and delete"
```

---

### Task 9: Monitor create/edit form with secret safety

**Files:**
- Create: `web/src/features/monitors/schemas.ts`
- Create: `web/src/features/monitors/form.ts`
- Create: `web/src/features/monitors/HeadersField.tsx`
- Create: `web/src/features/monitors/MonitorForm.tsx`
- Create: `web/src/features/monitors/MonitorCreateScreen.tsx`
- Create: `web/src/features/monitors/MonitorEditScreen.tsx`
- Modify: `web/src/router.tsx`
- Create: `web/src/features/monitors/form.test.ts`, `MonitorForm.test.tsx`, `MonitorEditScreen.test.tsx`

**Interfaces:**
- Consumes: Task 8 monitor API/hooks and Task 4 utilities.
- Produces: `monitorFormSchema` and `MonitorFormValues`.
- Produces: `defaultMonitorValues`, `monitorToFormValues`,
  `buildCreatePayload`, `buildUpdatePayload`, `headerRowsToRecord`.
- Produces: `SecretEditState = { replaceHeaders: boolean; bodyMode: 'keep' | 'replace' | 'clear' }`.
- Produces `MonitorForm` with this exact contract:

```ts
interface MonitorFormProps {
  mode: 'create' | 'edit';
  initialValues: MonitorFormValues;
  monitor?: Monitor;
  submitLabel: string;
  submitting: boolean;
  problem?: ApiProblem | null;
  onSubmit: (values: MonitorFormValues, secrets: SecretEditState) => Promise<void>;
}
```

- Produces: `MonitorCreateScreen({ workspace })`,
  `MonitorEditScreen({ workspace, monitorId })`.

- [ ] **Step 1: Write the failing payload tests**

`web/src/features/monitors/form.test.ts` must pin the secret rule:

```ts
const initial = monitorToFormValues(monitorWithMaskedHeaders);
const dirty = { name: true };

test('edit omits headers and body unless explicitly replaced', () => {
  expect(
    buildUpdatePayload(
      { ...initial, name: 'Renamed' },
      initial,
      dirty,
      { replaceHeaders: false, bodyMode: 'keep' },
    ),
  ).toEqual({ name: 'Renamed' });
});

test('edit never sends masked header values', () => {
  const payload = buildUpdatePayload(
    { ...initial, headers: [{ name: 'Authorization', value: 'Bearer fresh' }] },
    initial,
    dirty,
    { replaceHeaders: true, bodyMode: 'keep' },
  );
  expect(payload.headers).toEqual({ Authorization: 'Bearer fresh' });
  expect(JSON.stringify(payload)).not.toContain('***');
});

test('enabling replacement with zero rows clears all headers', () => {
  const payload = buildUpdatePayload(
    { ...initial, headers: [] },
    initial,
    dirty,
    { replaceHeaders: true, bodyMode: 'keep' },
  );
  expect(payload.headers).toEqual({});
});

test('body keep omits body and content type; clear sends an empty body', () => {
  expect(
    buildUpdatePayload(initial, initial, {}, { replaceHeaders: false, bodyMode: 'keep' }),
  ).toEqual({});
  expect(
    buildUpdatePayload(initial, initial, {}, { replaceHeaders: false, bodyMode: 'clear' }),
  ).toEqual({ body: '' });
});
```

`MonitorEditScreen.test.tsx` asserts that editing a monitor with masked headers
and submitting without enabling replacement PATCHes a body with no `headers`
key and no `***` substring.

`MonitorForm.test.tsx` asserts that the `Replace headers` switch reveals the
editable rows, that enabling it with zero rows shows
`Saving will remove all headers.`, and that GET/HEAD hides the body editor.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/monitors/form.test.ts src/features/monitors/MonitorEditScreen.test.tsx`

Expected: FAIL because the modules do not exist.

- [ ] **Step 3: Implement the schema and payload builders**

`monitorFormSchema` mirrors the backend bounds:

```ts
export const monitorFormSchema = z.object({
  name: z.string().trim().min(1, 'Name is required').max(100),
  url: z
    .string()
    .trim()
    .min(1, 'URL is required')
    .max(2048)
    .url('Enter a valid URL')
    .refine(
      (value) => /^https?:\/\//i.test(value),
      'URL must start with http:// or https://',
    ),
  method: z.enum(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE']),
  expected_status: z.string().trim().min(1, 'Expected status is required'),
  interval_seconds: z.number().int().min(60).max(86400),
  timeout_seconds: z.number().int().min(1).max(30),
  confirmation_threshold: z.number().int().min(1).max(5),
  recovery_threshold: z.number().int().min(1).max(5),
  headers: z
    .array(z.object({ name: z.string().min(1), value: z.string() }))
    .max(20),
  body: z.string().max(16 * 1024),
  content_type: z.string().max(200),
});
```

`MonitorFormValues` is `z.infer<typeof monitorFormSchema>`, and
`HeaderRow = MonitorFormValues['headers'][number]`, so the schema and the
types cannot drift. Use `useForm<MonitorFormValues>({ resolver: zodResolver(monitorFormSchema) })`
and register numeric inputs with `{ valueAsNumber: true }`.

```ts
export type SecretEditState = {
  replaceHeaders: boolean;
  bodyMode: 'keep' | 'replace' | 'clear';
};

export function defaultMonitorValues(): MonitorFormValues;
export function monitorToFormValues(monitor: Monitor): MonitorFormValues;
export function headerRowsToRecord(rows: HeaderRow[]): Record<string, string>;
export function buildCreatePayload(values: MonitorFormValues): CreateMonitorInput;
export function buildUpdatePayload(
  values: MonitorFormValues,
  initial: MonitorFormValues,
  dirty: Partial<Record<keyof MonitorFormValues, boolean>>,
  secrets: SecretEditState,
): UpdateMonitorInput;
```

`defaultMonitorValues()` returns
`{ name: '', url: '', method: 'GET', expected_status: '2xx',
interval_seconds: 60, timeout_seconds: 10, confirmation_threshold: 2,
recovery_threshold: 2, headers: [], body: '', content_type: '' }`. The backend
rejects zero intervals, timeouts, and thresholds, so these defaults are
required for a valid create.

`monitorToFormValues` must set `headers: []` and `body: ''`, never copy
`monitor.headers` (whose values are `***`) or a hidden body into form state.
The edit UI reads existing header names from `Object.keys(monitor.headers)`
only for the read-only summary.

`buildCreatePayload` maps header rows to a `Record<string,string>`, sends
`body`/`content_type` only when the method accepts a body and the fields are
non-empty, and includes all required fields.

`buildUpdatePayload` sends only dirty plain fields, plus the explicit secret
choices:

- `replaceHeaders` false → omit `headers`.
- `replaceHeaders` true → send `headers` (including `{}`).
- `bodyMode` `keep` → omit `body` and `content_type`.
- `bodyMode` `clear` → send `{ body: '' }`.
- `bodyMode` `replace` → send `body`; send `content_type` only when `body` is
  non-empty. An empty body sends `{ body: '' }`, which clears both the body and
  its content type.

- [ ] **Step 4: Implement the form UI and screens**

`MonitorForm`:

- `HeadersField` owns the add/remove header rows, the `Replace headers` switch,
  and the zero-row warning; it receives the form's `headers` field and edit-mode
  header names.

- `useForm` + `zodResolver`, with `defaultValues` from
  `defaultMonitorValues()` or `monitorToFormValues(monitor)`.
- Numeric inputs use `{ valueAsNumber: true }`.
- Method select hides/disables the body editor for GET/HEAD.
- Edit mode lists the existing header names from `Object.keys(monitor.headers)`
  as read-only text. Their values are never loaded into form state, and the
  replacement rows start empty behind a `Replace headers` switch. Enabling the
  switch with zero rows shows `Saving will remove all headers.`
- Edit mode shows `Body configured` when `has_body` is true and a `Body`
  control with Keep / Replace / Clear. Keep never sends the hidden body.
- Server `validation_error.fields` are applied via `applyProblemToForm` from a
  `useEffect` on the `problem` prop, using a map from API names to form field
  names. Unmapped messages render in a `ProblemAlert`.
- Page-level alerts for `quota_exceeded`, `monitor_rate_limited`, and
  `workspace_suspended`.

`MonitorCreateScreen` calls `useCreateMonitor`, navigates to the new monitor's
detail page on success, and shows `Create monitor` as the submit label.
`MonitorEditScreen` shows `Save changes` as the submit label.

`MonitorEditScreen` fetches the monitor, renders the form with
`monitorToFormValues`, and on submit calls
`buildUpdatePayload(values, initial, dirtyFields, secrets)` where `initial` is
the same `monitorToFormValues(monitor)` result and `dirtyFields` comes from
`formState.dirtyFields`. It passes the payload to `useUpdateMonitor`, then
navigates back to the detail page.

Add routes `/w/$workspaceSlug/monitors/new` and
`/w/$workspaceSlug/monitors/$monitorId/edit`. Both routes' `beforeLoad`
requires `canManageMonitors(workspace.role)` and otherwise throws
`redirect({ to: '/w/$workspaceSlug/monitors', params: { workspaceSlug } })`,
so a viewer cannot reach the form by URL.

- [ ] **Step 5: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/features/monitors
npm run lint
npm run typecheck
npm run build
```

Expected: PASS, including the no-`***` assertion.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/monitors web/src/router.tsx
git commit -m "feat: add monitor create and edit forms"
```

---

### Task 10: Monitor detail with summary, chart, and paginated results

**Files:**
- Create: `web/src/features/monitors/MonitorDetailScreen.tsx`
- Create: `web/src/features/monitors/SummaryCards.tsx`
- Create: `web/src/features/monitors/ResultsTable.tsx`
- Create: `web/src/features/monitors/LatencyChart.tsx`
- Modify: `web/src/router.tsx`
- Create: `web/src/features/monitors/MonitorDetailScreen.test.tsx`, `SummaryCards.test.tsx`, `ResultsTable.test.tsx`

**Interfaces:**
- Consumes: Task 8 `useMonitor`, `useSummary`, `useResults`,
  `usePauseMonitor`, `useResumeMonitor`, `useDeleteMonitor`; Task 4 formatters.
- Produces: `MonitorDetailScreen({ workspace, monitorId })`.
- Produces `SummaryCards` with this contract:

```ts
interface SummaryCardsProps {
  summary: Summary | null | undefined;
}
```

- Produces `ResultsTable` with this contract:

```ts
interface ResultsTableProps {
  results: ProbeResult[];
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  onLoadMore: () => void;
}
```

- Produces `LatencyChart` with this contract:

```ts
interface LatencyChartProps {
  results: ProbeResult[];
}
```

- Produces route `/w/$workspaceSlug/monitors/$monitorId`.

- [ ] **Step 1: Write the failing tests**

`SummaryCards.test.tsx` asserts:

- Null uptime or zero checks renders `No data`, never `NaN%`.
- Renders checks, up, down, average latency, and p95 latency.
- Latency values are rounded to whole milliseconds.

`ResultsTable.test.tsx` asserts:

- Renders checked-at, status, status code, latency, and error.
- Shows `No results in this window` for an empty list.
- Calls `onLoadMore` from the `Load more` button and hides the button when
  `hasNextPage` is false.

`MonitorDetailScreen.test.tsx` asserts:

- The window selector switches between `1h`, `6h`, `24h`, and `7d` and refetches
  summary/results with the chosen `window` query parameter.
- A monitor with `status: 'paused'` shows `Paused` and a Resume action.
- A null `last_check` shows `Never checked`.
- The paused/unknown status is never labeled `Down`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/monitors`

Expected: FAIL because the components do not exist.

- [ ] **Step 3: Implement the components**

`SummaryCards` renders six cards: uptime, checks, up, down, average latency,
p95 latency. Uptime uses `formatUptime(summary.uptime, summary.checks)`.

`ResultsTable` renders a table on `md+` and stacked rows below `md`. Error text
is truncated with a title tooltip. `Load more` uses the infinite query's
`fetchNextPage` and `hasNextPage`.

`LatencyChart` is lazy-loaded from `MonitorDetailScreen` using `React.lazy` and
`Suspense`. It renders a Recharts `LineChart` over the loaded results in
chronological order, with an accessible summary (for example
`Latency over the loaded results`) and an empty state when there are no points.

`MonitorDetailScreen`:

- Header with name, URL, status badge, last check, and actions. The action
  buttons have accessible names `Pause {name}` / `Resume {name}`, `Edit`, and
  `Delete`.
- Window selector as a segmented control with accessible labels. The window is
  a validated search param `?window=1h|6h|24h|7d` defaulting to `24h`, so the
  selection is shareable and drives the summary/results query keys.
- Summary cards and chart for the selected window.
- Results table with cursor pagination.
- Delete uses the same confirmation copy as the list: title
  `Delete {name}?` and confirm label `Delete monitor`.
- `Never checked` for null `last_check`.
- Page-level alerts for errors; retry button for 5xx/network errors.

Add the route and pass `workspace` and `monitorId`.

- [ ] **Step 4: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/features/monitors
npm run lint
npm run typecheck
npm run build
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/monitors web/src/router.tsx
git commit -m "feat: add monitor detail with results and latency chart"
```

---

### Task 11: Admin workspaces, monitors, and security events

**Files:**
- Create: `web/src/features/admin/api.ts`, `hooks.ts`
- Create: `web/src/features/admin/AdminWorkspacesScreen.tsx`, `AdminMonitorsScreen.tsx`, `SecurityEventsScreen.tsx`
- Create: `web/src/features/admin/SuspendDialog.tsx`, `WorkspaceCombobox.tsx`, `EventDetail.tsx`
- Modify: `web/src/router.tsx`
- Create: `web/src/features/admin/AdminWorkspacesScreen.test.tsx`, `AdminMonitorsScreen.test.tsx`, `SecurityEventsScreen.test.tsx`, `WorkspaceCombobox.test.tsx`

**Interfaces:**
- Produces: `listAdminWorkspaces(filters, cursor?)`,
  `suspendWorkspace(id, reason)`, `unsuspendWorkspace(id)`,
  `listAdminMonitors(filters, cursor?)`, `listSecurityEvents(filters, cursor?)`.
- Produces: `useAdminWorkspaces`, `useSuspendWorkspace`,
  `useUnsuspendWorkspace`, `useAdminMonitors`, `useSecurityEvents`.
- Exact hook signatures:

```ts
useAdminWorkspaces(filters: AdminWorkspaceFilters)
useAdminMonitors(filters: AdminMonitorFilters)
useSecurityEvents(filters: SecurityEventFilters)
useSuspendWorkspace()
useUnsuspendWorkspace()
```

The suspend/unsuspend mutations invalidate `['admin', 'workspaces']` and
`['admin', 'security-events']`.
- Produces screens and routes `/admin/workspaces`, `/admin/monitors`,
  `/admin/security-events`.
- Produces: `SECURITY_EVENT_KINDS` with the five backend kinds.
- Produces `WorkspaceCombobox` with this contract:

```ts
interface WorkspaceComboboxProps {
  value: string | null;
  onChange: (workspaceId: string | null) => void;
  label?: string;      // default: "Workspace"
  placeholder?: string; // default: "All workspaces"
  allowClear?: boolean; // default: true
}

interface SuspendDialogProps {
  workspace: AdminWorkspace;
  onClose: () => void;
}

interface EventDetailProps {
  detail: unknown;
}
```

- Pins these labels: the search placeholder is `Search workspaces` or
  `Search monitors`, the status filter is `Status` with options `All`, `Active`,
  `Suspended`, the suspend dialog fields are `Reason` and the buttons
  `Suspend workspace` / `Cancel`, the unsuspend confirm button is
  `Unsuspend workspace`, the security-events kind filter is `Kind` with
  `All kinds` as the default option, and the event detail toggle is `Details`.
- Pins the admin navigation: group heading `Admin` with links
  `Workspaces`, `Monitors`, and `Security events`.

- [ ] **Step 1: Write the failing tests**

`AdminWorkspacesScreen.test.tsx` asserts:

- Renders workspace name, slug, member/monitor counts, and suspension state.
- Search updates the `q` query parameter; the suspended filter sends
  `suspended=true` or `suspended=false`.
- Suspend opens a dialog, requires a reason, POSTs
  `/api/v1/admin/workspaces/{id}/suspend` with `{reason}`, and refreshes the list.
- Unsuspend asks for confirmation and POSTs `/unsuspend`.

`AdminMonitorsScreen.test.tsx` asserts:

- Renders the redacted URL returned by the API without trying to reconstruct a
  query string.
- The workspace combobox filters by `workspace_id`.

`SecurityEventsScreen.test.tsx` asserts:

- Renders the five kinds in the kind filter and sends the selected `kind`.
- The workspace combobox sends `workspace_id`.
- The event detail viewer renders the JSON `detail` and does not show a
  `***` value as a real secret.

`WorkspaceCombobox.test.tsx` asserts debounced search calls
`GET /api/v1/admin/workspaces?q=...`, renders matches, and returns the selected
workspace id.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npm test -- --run src/features/admin`

Expected: FAIL because the modules do not exist.

- [ ] **Step 3: Implement the admin data layer**

`SECURITY_EVENT_KINDS`:

```ts
export const SECURITY_EVENT_KINDS = [
  'monitor_created',
  'monitor_create_rate_limited',
  'ssrf_blocked',
  'workspace_suspended',
  'workspace_unsuspended',
] as const;
```

Query functions build query strings with `q`, `suspended`, `workspace_id`,
`kind`, `cursor`, and `limit`. Use `nextPageParam` for infinite queries and a
60-second stale time. Omit empty filters entirely.

- [ ] **Step 4: Implement the screens and routes**

`AdminWorkspacesScreen`:

- Debounced search (300 ms) and a status filter (All / Active / Suspended).
- Table with name/slug, created date, member count, monitor count, status, and
  suspension reason. Status text is `Active` or `Suspended`.
- Row actions are `Suspend` and `Unsuspend`. `Suspend` opens `SuspendDialog`;
  the reason is required and at most 500 characters. `Unsuspend` uses
  `ConfirmDialog` with confirm label `Unsuspend workspace`.

`AdminMonitorsScreen`:

- Debounced search and `WorkspaceCombobox`.
- Table with workspace, name, redacted URL, status, last check, and latency.
- Cursor `Load more`.

`SecurityEventsScreen`:

- Kind select from `SECURITY_EVENT_KINDS`, plus `All kinds`.
- `WorkspaceCombobox`.
- Newest-first list with kind, time, workspace, and `EventDetail`.
- `EventDetail` renders the `detail` JSON in a collapsible `<pre>`; it never
  fetches extra data to reconstruct hidden values.

Add the `/admin` route with `beforeLoad` that redirects non-admins to `/`, and
the three child routes. The admin layout renders an `<h1>Admin</h1>`; each page
renders an `<h2>` with its name. Add the Admin navigation group to `Sidebar` and
the mobile sheet only when `me.user.is_admin`, with a group heading `Admin` and
links `Workspaces`, `Monitors`, and `Security events`.

- [ ] **Step 5: Run the tests and checks**

Run:

```bash
cd web
npm test -- --run src/features/admin
npm run lint
npm run typecheck
npm run build
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/admin web/src/components/layout web/src/router.tsx
git commit -m "feat: add admin workspace monitor and security event pages"
```

---

### Task 12: Deployment, CI, and the Playwright smoke flow

**Files:**
- Create: `web/Dockerfile`, `web/Caddyfile`, `web/.dockerignore`
- Modify: `compose.yaml`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`
- Create: `web/playwright.config.ts`
- Create: `web/tests/e2e/smoke.spec.ts`, `web/tests/e2e/helpers.ts`

**Interfaces:**
- Produces: `web` Compose service on `5173:80` serving the SPA and proxying
  `/api` to `api:8080`.
- Produces: frontend CI job (`lint`, `typecheck`, unit tests, build, Docker
  build) and an E2E job against Compose.
- Produces: one Playwright path covering register → verify → workspace →
  monitor CRUD → admin suspend/unsuspend.

- [ ] **Step 1: Write the failing E2E and Playwright config**

`web/playwright.config.ts`:

```ts
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests/e2e',
  timeout: 120_000,
  fullyParallel: false,
  reporter: 'list',
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:5173',
    trace: 'retain-on-failure',
  },
});
```

`web/tests/e2e/helpers.ts`:

```ts
import type { APIRequestContext } from '@playwright/test';

const MAILPIT_API = 'http://localhost:8025/api/v1';

export async function waitForVerificationLink(
  request: APIRequestContext,
  email: string,
): Promise<string> {
  // Poll MAILPIT_API/messages, then read MAILPIT_API/message/{ID}.
  // Return the first /verify-email?token=... URL found in Text or HTML.
}
```

It polls `GET http://localhost:8025/api/v1/messages?limit=50` every 500 ms for
up to 30 seconds, finds a message whose `To[].Address` equals `email`, fetches
`GET http://localhost:8025/api/v1/message/{ID}`, and extracts the first
`http://localhost:5173/verify-email?token=...` link from `Text` or `HTML`. It
throws a descriptive error on timeout.

`web/tests/e2e/smoke.spec.ts` (imports included; `waitForVerificationLink`
polls Mailpit for up to 30 seconds):

```ts
import { execFileSync } from 'node:child_process';
import { expect, test, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { waitForVerificationLink } from './helpers';

async function expectNoSeriousA11yViolations(page: Page) {
  const results = await new AxeBuilder({ page }).analyze();
  const violations = results.violations.filter(
    (violation) =>
      violation.impact === 'serious' || violation.impact === 'critical',
  );
  expect(violations).toEqual([]);
}

test('register, verify, create a monitor, and administer the workspace', async ({ page, request }) => {
  const email = `e2e-${Date.now()}@example.com`;
  const password = 'correct horse battery staple';

  await page.goto('/register');
  await expectNoSeriousA11yViolations(page);
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByLabel('Confirm password').fill(password);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page.getByText('Check your email')).toBeVisible();

  await page.goto(await waitForVerificationLink(request, email));
  await expect(page.getByText('Email verified')).toBeVisible();
  await page.getByRole('link', { name: 'Sign in' }).click();
  await expectNoSeriousA11yViolations(page);
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();

  await page.getByRole('button', { name: 'New workspace' }).click();
  await page.getByLabel('Name').fill('E2E Workspace');
  await page.getByRole('button', { name: 'Create workspace' }).click();
  await expectNoSeriousA11yViolations(page);

  await page.getByRole('link', { name: 'New monitor' }).click();
  await page.getByLabel('Name').fill('Example');
  await page.getByLabel('URL').fill('https://example.com');
  await page.getByRole('button', { name: 'Create monitor' }).click();
  await expect(page.getByText('Example')).toBeVisible();
  await expectNoSeriousA11yViolations(page);
  await page.getByRole('button', { name: 'Pause Example' }).click();
  await expect(page.getByText('Paused', { exact: true }).first()).toBeVisible();
  await page.getByRole('button', { name: 'Resume Example' }).click();

  await page.getByRole('button', { name: 'Edit' }).click();
  await page.getByLabel('Name').fill('Example edited');
  await page.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByText('Example edited')).toBeVisible();

  await page.getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('button', { name: 'Delete monitor' }).click();
  await expect(page.getByText('No monitors yet')).toBeVisible();

  // Promote through direct SQL; there is no product API for this.
  execFileSync('docker', [
    'compose', '-f', '../compose.yaml', 'exec', '-T', 'postgres',
    'psql', '-U', 'o1', '-d', 'o1',
    '-c', `UPDATE users SET is_admin = true WHERE email = '${email}'`,
  ]);
  await page.reload();
  await page.goto('/admin/workspaces');
  await expect(page.getByRole('heading', { name: 'Admin' })).toBeVisible();
  await page.getByRole('button', { name: 'Suspend' }).first().click();
  await page.getByLabel('Reason').fill('E2E suspension');
  await page.getByRole('button', { name: 'Suspend workspace' }).click();
  await expect(
    page.getByRole('row', { name: /E2E Workspace/ }).getByText('Suspended'),
  ).toBeVisible();

  await page.getByRole('button', { name: 'Unsuspend' }).first().click();
  await page.getByRole('button', { name: 'Unsuspend workspace' }).click();
  await expect(
    page.getByRole('row', { name: /E2E Workspace/ }).getByText('Active'),
  ).toBeVisible();
});
```

After the main flow, set `page.setViewportSize({ width: 390, height: 844 })`,
open the mobile navigation sheet with `getByRole('button', { name: 'Open navigation' })`,
and assert the `Monitors` link is reachable. Run
`expectNoSeriousA11yViolations(page)` on `/login`, the monitor list, and the
monitor detail.

If Mailpit has no message for the address after 30 seconds, keep the login and
admin assertions: register through the API instead, run
`UPDATE users SET email_verified_at = now() WHERE email = '...'` through the
same `docker compose exec` helper, then continue with the browser login. Do not
add a verification bypass endpoint.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx playwright test`

Expected: FAIL because no stack, Dockerfile, or `web` service exists.

- [ ] **Step 3: Add the web image and Compose service**

`web/Dockerfile`:

```dockerfile
FROM node:24-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM caddy:2-alpine
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /app/dist /srv
EXPOSE 80
```

`web/Caddyfile`:

```caddyfile
:80 {
	root * /srv
	encode gzip zstd

	@api path /api/*
	handle @api {
		reverse_proxy api:8080
	}

	handle {
		try_files {path} /index.html
		file_server
	}
}
```

`web/.dockerignore`:

```text
node_modules
dist
playwright-report
test-results
```

`compose.yaml`:

```yaml
  web:
    build:
      context: ./web
    depends_on:
      - api
    ports:
      - "5173:80"
```

Keep `O1_APP_BASE_URL=http://localhost:5173`; the API port `8080` stays exposed
for direct health/API access.

- [ ] **Step 4: Add the CI jobs**

Append to `.github/workflows/ci.yml`:

```yaml
  frontend:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: web
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: "24"
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: npm ci
      - run: npm run format:check
      - run: npm run lint
      - run: npm run typecheck
      - run: npm test -- --run
      - run: npm run build
      - run: docker build -t o1-web:ci .

  e2e:
    runs-on: ubuntu-latest
    needs: frontend
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: "24"
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: npm ci
        working-directory: web
      - run: npx playwright install --with-deps chromium
        working-directory: web
      - run: docker compose up -d --build
      - name: Wait for the stack
        run: |
          for i in $(seq 1 60); do
            curl -fsS http://localhost:5173 >/dev/null && \
              curl -fsS http://localhost:8080/readyz >/dev/null && exit 0
            sleep 2
          done
          docker compose logs
          exit 1
      - run: npx playwright test
        working-directory: web
      - if: always()
        run: docker compose logs
      - if: always()
        run: docker compose down -v
```

`@axe-core/playwright` is already a dev dependency from Task 3.

- [ ] **Step 5: Update the README**

Add a "Dashboard" section:

- `docker compose up --build` serves the dashboard at
  `http://localhost:5173` and the API at `http://localhost:8080`.
- The dashboard proxies `/api` through Caddy; no CORS setup is needed.
- Local frontend development: `cd web && npm install && npm run dev`.
- Admin promotion: register, verify, set `O1_BOOTSTRAP_ADMIN_EMAIL`, then
  recreate `api`.
- The dashboard is not a status page; incidents, alerts, and public status
  pages are later milestones.

- [ ] **Step 6: Run the full verification**

Run:

```bash
docker compose config
docker compose up -d --build
(cd web && npm run build && npx playwright test)
docker compose down -v
```

Expected: the E2E smoke test passes, including the admin suspension and the
axe checks.

- [ ] **Step 7: Commit**

```bash
git add web/Dockerfile web/Caddyfile web/.dockerignore web/playwright.config.ts web/tests web/package.json web/package-lock.json compose.yaml .github/workflows/ci.yml README.md
git commit -m "feat: add dashboard deployment ci and e2e smoke"
```

---

## Self-Review

**Spec coverage:**

- Auth flows → Tasks 5–6.
- Workspace/member management → Task 7.
- Monitor CRUD/pause/resume → Tasks 8–10.
- Admin workspaces/monitors/security events → Task 11.
- Backend `is_admin` and Origin fix → Tasks 1–2.
- Same-origin delivery, Caddy, Compose, CI → Task 12.
- Secret safety, error mapping, pagination, uptime contract, accessibility →
  Review Focus tests in Tasks 4, 6, 8, 9, 10, and 12.

**Review Focus ownership:**

1. Secret edit → Task 9 `form.test.ts` and `MonitorEditScreen.test.tsx`.
2. Background 401 → Task 4 `auth-redirect.test.ts` plus the Task 6
   `main.tsx` wiring.
3. Missing workspace slug → Task 5 `findWorkspace` test plus the Task 7
   `notFound()` route guard.
4. Cursor pagination → Task 4 `nextPageParam` test plus the Task 8 load-more
   test.
5. Uptime no-data → Task 4 `formatUptime` test plus Task 10 `SummaryCards`.

**Type consistency:** `ApiProblem`, `Me`, `Workspace`, `Monitor`,
`ProbeResult`, `Summary`, `AdminWorkspace`, `AdminMonitor`, and `SecurityEvent`
are defined once in `web/src/lib/api/types.ts` and consumed by later tasks.
`nextPageParam`, `formatUptime`, `applyProblemToForm`, and `statusLabel` keep
the same names across tasks.

**Proportion:** The plan pins interfaces, payload rules, routes, and test
assertions; component bodies are left to the implementer where the spec and
tests determine them.
