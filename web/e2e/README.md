# Frontend browser tests

The Playwright suite validates the real Ongrid frontend against a running local environment.

```bash
cd web
npm run test:e2e
```

The suite uses the locally installed Google Chrome by default. Set `E2E_BROWSER_CHANNEL` to override it.

The test runner reads `ONGRID_ADMIN_EMAIL` and `ONGRID_ADMIN_PASSWORD` from the environment. For the native local setup it also loads the ignored `deploy/.env` file. Override the target with `E2E_BASE_URL`.

Read-only navigation and resource views run against the live local environment. Deterministic create/test workflows intercept their API calls in the browser, so they validate UI behavior and request payloads without changing local data or calling external Slack services.

Reports, screenshots, traces, videos, and the temporary login state are written to `output/playwright/`.
