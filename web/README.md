# o1 dashboard

The React dashboard for o1. See the [root README](../README.md) for the project
overview, architecture, configuration, and backend setup.

## Commands

Run these from `web/`:

```sh
npm run dev          # start Vite against the Compose API
npm test -- --run    # run the unit and component tests once
npm run lint         # ESLint
npm run typecheck    # TypeScript project references and e2e config
npm run build        # typecheck and production build
npm run e2e          # Playwright smoke tests (requires the Compose stack)
```

`npm run format` and `npm run format:check` run Prettier.
