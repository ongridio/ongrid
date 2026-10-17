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

Live navigation follows the `/kubernetes` redirect to `/clusters` and uses tab roles for device/topology panels. The Kubernetes detail test requires an enrolled cluster from the real `/api/v1/k8s/clusters` API; an empty list produces an explicit skip. Report that skip separately from passing tests. The device plugin page intentionally excludes top-level `profiles` and `autoapm` rows.
