# tests/e2e — end-to-end test harness

Catalog of what should be covered: [`docs/test/e2e-catalog.md`](../../docs/test/e2e-catalog.md).
Each test here implements one numbered item from that catalog and updates
the "实现" column.

## Run

```bash
make test-e2e             # all e2e (uses fakes; no secrets required)
make test-e2e-live        # opt-in to live external endpoints (needs secrets.local.env)
go test -tags=e2e ./tests/e2e/...    # equivalent to `make test-e2e`
```

Tests use the `e2e` Go build tag so they are excluded from `go test ./...`
and `make test` — those stay fast unit-only.

### Prerequisites

- **Docker daemon** reachable from the test runner. The harness brings up
  a `mysql:8.0` container via testcontainers-go.
- **≥ 4 GiB allocated to Docker.** `mysql:8.0` cold-start on a 2 GiB
  Docker Desktop install routinely takes longer than the container-start
  API deadline. Bump Docker Desktop's memory ("Settings → Resources →
  Memory") if you see `start container: context deadline exceeded`.
- The harness sets `TESTCONTAINERS_RYUK_DISABLED=true` by default — the
  reaper sidecar is the #1 source of mac-side slowness, and each test's
  `t.Cleanup(env.Stop)` already tears the container down. Override with
  `TESTCONTAINERS_RYUK_DISABLED=false` in CI if you want ryuk back.

## Test environment

Each test starts a fresh `testenv.Env` (or reuses one via `TestMain`):

```
env := testenv.Start(t)
defer env.Stop()

resp, err := env.Login("admin@ongrid.local", env.AdminPassword)
```

`testenv.Start` brings up:

| Component | How | Notes |
|---|---|---|
| **MySQL** | testcontainers-go `mysql` module | one shared container per package via `TestMain` |
| **manager** binary | built once, `os/exec`-spawned per test | `cfg.HTTPPort` random; healthz polled |
| **Fake LLM** | in-process `httptest.Server` | OpenAI / Anthropic protocol compat |
| **Fake Slack webhook** | `httptest.Server` recording POSTed payloads | `env.SlackCaptures()` returns slice |
| **Fake Telegram** | `httptest.Server` for `getUpdates` + `sendMessage` | inject inbound events via `env.TelegramPush(update)` |
| **Fake Prom / Loki** | minimal `query_range` responder | inject metric/log series via `env.PromPushSeries(...)` |
| Frontier (tunnel) | **omitted** | edge-side flows use the `edgesim` helper which calls the in-process tunnel handler directly, no real WS |

No Prometheus / Loki / Tempo binaries are required — those side cars are
mocked at the HTTP layer. The manager binary is the only real one.

## Secrets — what does and doesn't go in git

The hard rule: **no real token ever lands in this repo**. Everything that
hits a real external service is opt-in and skipped when its secret is missing.

### Three modes

| Mode | What runs | Secret needed | Where secrets live |
|---|---|---|---|
| **default** (`make test-e2e`) | mocked-external tests | none | — |
| **live-X** (`E2E_LIVE_SLACK=1` etc) | one external integration replaced with the real endpoint | only the specific one | `tests/e2e/secrets.local.env` (gitignored) **or** env |
| **full live** (`E2E_LIVE_ALL=1`) | every external integration live | every secret | same |

### `RequireSecret` pattern

Inside a test that talks to a real external service:

```go
token := testenv.RequireSecret(t, "SLACK_INCOMING_WEBHOOK_URL")
// if the env var (or secrets.local.env) is empty, t.Skip with a clear reason
// — so CI without secrets just skips, no failure, no leak
```

The Skip message format is uniform:

```
SKIP: e2e/notify_slack_live — needs SLACK_INCOMING_WEBHOOK_URL (set in env or tests/e2e/secrets.local.env; see secrets.example.env for the template)
```

### Loading order

`testenv.RequireSecret` looks in:

1. `os.Getenv(name)` — first hit
2. `tests/e2e/secrets.local.env` (gitignored dotenv) — second hit
3. otherwise → `t.Skip` with the standard message

Order matters: env always wins, so CI can inject via `secrets:` (GitHub
Actions) without touching the file.

### Template

`tests/e2e/secrets.example.env` is the **committed** template listing every
secret the suite knows about, with a short comment for each. Copy to
`secrets.local.env` and fill the ones you have:

```bash
cp tests/e2e/secrets.example.env tests/e2e/secrets.local.env
$EDITOR tests/e2e/secrets.local.env   # fill the ones you have
```

`secrets.local.env` is in `.gitignore`. CI either sets env vars directly
or mounts a secret file at the same path.

### What's exempt from the "no real token" rule

- **Test users / passwords inside the test MySQL container** — random per
  run, never persists. The container is torn down at end.
- **Fake LLM tokens** the manager binary sends to the in-process fake
  server — they're test fixtures, not real credentials.

## Live mode in CI

Default CI runs `make test-e2e` only (no live mode). A nightly job runs
`make test-e2e-live` with secrets injected, so live-mode regressions
get caught within 24 hours even though every PR stays fast and
credential-free.

## File layout

```
tests/e2e/
├── README.md               (this file)
├── .gitignore              (secrets.local.env, *.local.*)
├── secrets.example.env     (committed template)
├── secrets.local.env       (gitignored, dev's actual secrets)
├── testenv/
│   ├── env.go              (Start / Stop / healthz, MySQL container, manager binary)
│   ├── fakes.go            (LLM / Slack / Telegram / Prom / Loki fakes)
│   ├── secrets.go          (RequireSecret with file+env loader)
│   └── http.go             (auth helpers, JSON wrappers)
├── auth_login_test.go      (B1)
├── notify_slack_test.go    (G3)
└── settings_reveal_test.go (O1)
```

New tests follow the same naming as the catalog: `<area>_<short>_test.go`,
one numbered case per file (so a regression doesn't take down five at
once and `go test -run` works on the catalog number).

## Conventions for writing a new e2e

1. Pick a row from `docs/test/e2e-catalog.md` — implement that one row.
2. Reuse `testenv.Start(t)` — don't roll your own bootstrap.
3. **Default to fakes**. If your test needs a real external endpoint to
   meaningfully assert, wrap it in `testenv.RequireSecret` so it skips
   without secrets.
4. **Never log a secret**. Use `testenv.RedactSecret(s)` in any log line
   that might carry a token.
5. **One assertion per behavior**. Don't bundle five catalog rows in one
   test — they share state and a failure is opaque.
6. When done, tick the row in `docs/test/e2e-catalog.md`.

See `auth_login_test.go` for the minimal pattern.
