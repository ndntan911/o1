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
