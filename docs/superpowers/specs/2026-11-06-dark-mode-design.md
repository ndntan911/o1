# Dark Mode Design

**Status:** Draft for approval
**Date:** 2026-11-06
**Scope:** small frontend-only change to the existing React dashboard.

## 1. Intent

Let signed-in users choose **Light**, **Dark**, or **System** appearance. The choice
persists in the browser, applies before first paint, follows OS changes while
`System` is selected, and every existing screen, chart, toast, and native control
remains readable in both themes.

## 2. Current state

- `web/src/styles.css` already defines the full shadcn token set for `:root` and
  `.dark`, plus `@custom-variant dark (&:is(.dark *))`.
- `next-themes` is already a dependency; `web/src/components/ui/sonner.tsx`
  calls `useTheme()` but no provider exists, so it always falls back to
  `system`.
- There is no theme provider, no toggle, no persistence key, and no
  pre-paint theme script.
- `LatencyChart` sets the line color from `var(--chart-2)` but leaves Recharts
  axes and tooltip at their light-mode defaults.
- `web/index.html` has no theme initialization.

## 3. Goals and non-goals

**Goals**

1. `System` is the default and resolves through `prefers-color-scheme`.
2. A visible, keyboard-accessible Light / Dark / System control.
3. The choice survives reloads and applies before the first paint (no flash).
4. Dark mode covers all existing screens, dialogs, menus, toasts, tables, forms,
   and the latency chart.
5. The app reports the correct `color-scheme` so native scrollbars and form
   controls match.

**Non-goals**

- Storing the preference on the account or syncing it across devices.
- Per-workspace themes, scheduled themes, high-contrast themes, or custom accent
  colors.
- A full visual redesign or new color palette; the existing dark tokens are the
  source of truth.
- Any backend, API, migration, cookie, or CORS change.

## 4. Approaches

**A. `next-themes` provider with a three-state toggle (chosen).** Wrap the app in
`ThemeProvider` from the already-installed library, use `attribute="class"`,
`defaultTheme="system"`, `storageKey="o1-theme"`, and add a small inline script
in `index.html` that reads the same key before paint. The library already
handles OS-change listeners, the `system` resolution, and `color-scheme`, and
the Sonner toaster starts receiving the real theme. Smallest correct change.

**B. Hand-rolled React context + `matchMedia` + `localStorage`.** Avoids leaning
on the library but duplicates its logic and leaves Sonner's `useTheme()` broken
or needing a parallel context. Rejected: more code, same behavior.

**C. Account-level preference.** Adds a user-preference column, API, and cache
invalidation so the theme follows someone across devices. Meaningfully larger
and explicitly not what was approved. Rejected for this change.

## 5. Behavior

- Initial theme is `o1-theme` from `localStorage` when present and valid;
  otherwise `system`.
- `System` follows the OS and updates live when the OS preference changes. An
  explicit `Light` or `Dark` choice wins until the user picks `System` again.
- The toggle is a three-button segmented control (`Sun` Light, `Moon` Dark,
  `Monitor` System) with visible text labels, `aria-pressed`, and the existing
  focus ring. It sits in `UserMenu` above the email and Log out action, so it is
  reachable on desktop and in the mobile navigation sheet. No duplicate control
  is added to the mobile header.
- Selecting a theme updates the `dark` class on `<html>`, sets
  `localStorage["o1-theme"]`, and updates `document.documentElement.style.colorScheme`.
- The inline boot script applies the same class and `color-scheme` before React
  mounts, so a dark-mode user never sees a white flash.
- Toasts follow the resolved theme through the existing `useTheme()` call.
- The latency chart uses `var(--muted-foreground)` for axis ticks and the
  popover/border/foreground tokens for the tooltip, so it is legible in both
  themes.

## 6. Files

- `web/index.html`: inline pre-paint theme script (storage key `o1-theme`).
- `web/src/main.tsx`: wrap `RouterProvider` in `ThemeProvider`.
- `web/src/components/layout/ThemeToggle.tsx`: new control.
- `web/src/components/layout/ThemeToggle.test.tsx`: new tests.
- `web/src/components/layout/UserMenu.tsx`: render the toggle.
- `web/src/components/layout/UserMenu.test.tsx`: new, if not already covered.
- `web/src/features/monitors/LatencyChart.tsx`: theme-aware axes and tooltip.
- `web/tests/e2e/smoke.spec.ts`: toggle, persistence, and a dark-mode axe pass.
- `README.md`: one short note in the dashboard section.

No query keys, routes, API types, backend files, or migrations change.

## 7. Tests

**Unit (Vitest + Testing Library)**

- Defaults to `System` with no stored key; resolves dark when `matchMedia`
  reports dark.
- Choosing Dark adds `dark` to `<html>`, writes `o1-theme=dark`, and reports
  `color-scheme: dark`; choosing Light removes it.
- Choosing System reacts to a `matchMedia` change event.
- A stored `dark` value is honored on remount; invalid stored values fall back
  to `system`.
- Each option exposes pressed state and an accessible name; the control is
  keyboard operable.
- `UserMenu` renders the theme control.

**E2E (Playwright)**

- On an authenticated page, choose Dark and assert `<html>` has `dark`.
- Reload and assert dark persists.
- Run the existing axe check on the dark page.
- Return to System so the rest of the smoke run is unaffected.

**Checks**

- `npm run format:check`, `npm run lint`, `npm run typecheck`, `npm test -- --run`,
  `npm run build`.
- One composed Playwright smoke run if host ports permit; otherwise report that
  it did not run.

## 8. Acceptance criteria

1. First visit with no stored preference follows the OS.
2. Light, Dark, and System all work and persist across reloads without a flash.
3. The control is reachable and operable by keyboard on desktop and mobile.
4. Every existing screen and the latency chart are legible in dark mode.
5. Toasts and native controls follow the active theme.
6. No backend, API, or data changes.

## 9. Risks and builder latitude

- The exact control shape (segmented buttons versus a list) is the builder's
  call if the three states, labels, pressed state, and keyboard behavior hold.
- The inline script must stay in sync with `storageKey` and `attribute`; the
  builder may centralize those constants only if the inline script can still
  read them without a bundler step.
- Recharts styling is pinned in behavior, not in a specific prop shape.
- If the installed `next-themes` injects a working pre-paint script in this Vite
  setup, the manual `index.html` script may be omitted only after verifying no
  flash in a real browser; the acceptance criterion is no flash, not the script.
