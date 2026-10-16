# Agent observability acceptance and local preview

The implementation is available at `/agent-observability`. It reads real Tempo
data. The local preview uses synthetic SDK payloads and separate loopback ports;
it does not change an installed Ongrid service.

## Verified SDK matrix

Tested on Python 3.14.3 with OpenTelemetry SDK/exporter 1.45.1. The complete
dependency set is pinned in `requirements.lock`.

| SDK | Version | Captured workload |
| --- | --- | --- |
| LangSmith | 0.14.8 | LangChain Core 1.6.10 / LangGraph 1.2.15: workflow, tool and fake chat model through native OTLP |
| OpenInference LangChain | 0.1.79 | Same graph through `LangChainInstrumentor` |
| OpenLLMetry / traceloop-sdk | 0.62.4 | Same graph through LangChain instrumentation |
| Langfuse | 4.17.0 | `@observe` agent, tool and generation with a custom OTLP exporter |

Fixtures in `internal/manager/biz/agenttrace/testdata/sdk` are actual SDK exports,
not handwritten approximations. The fake model reports 42 input and 16 output
tokens; tests expect 58 total, a conversation ID and preserved tool/model steps.
No provider key or billable model call is needed.

LlamaIndex, OpenAI Agents, CrewAI, other SDK versions, JS/TS exporters and real
provider streaming/cancellation have not been certified by this acceptance.
Their raw spans remain readable; semantic support must be verified before a
compatibility claim. The fixed-version setup snippets in the UI were separately
executed through OTLP HTTP → Tempo → the actual Agent API; all four returned runs.

## Reproduce the preview

Run from the repository root. Requirements: Docker, Go, Node/npm and Python 3.14.
Use a fresh virtual environment; do not install these packages into an application.

```sh
python3.14 -m venv /tmp/ongrid-agent-sdks
/tmp/ongrid-agent-sdks/bin/pip install -r scripts/agent-observability/requirements.lock
docker run --rm -d --name ongrid-agent-observability-preview-tempo \
  -p 127.0.0.1:13200:3200 -p 127.0.0.1:14318:4318 \
  -v "$PWD/scripts/agent-observability/tempo.yaml:/etc/tempo.yaml:ro" \
  grafana/tempo:2.10.0 -config.file=/etc/tempo.yaml
```

Wait until `http://127.0.0.1:13200/ready` returns ready, then seed the isolated store:

```sh
/tmp/ongrid-agent-sdks/bin/python scripts/agent-observability/seed_preview.py
go run scripts/agent-observability/preview.go
```

Keep that server running. In a second terminal:

```sh
cd web
npm ci
npm run dev -- --config ../scripts/agent-observability/vite.config.mts
```

Open <http://127.0.0.1:15173/agent-preview>. This entry creates a synthetic viewer
identity only in the preview origin. The API binds `127.0.0.1:18590` and queries
Tempo on `13200`. Never use the preview shell as a production authentication server.
The regular production route still uses Ongrid authentication.

The seed copies real SDK fixtures into 24 synthetic runs across three applications
and six conversations, with successful/recovered/failed runs, long names and Span
Links. Labels and environment identify synthetic data. Preview retention is one
hour. The short block polling and query settings support backdated fixtures;
they are not production recommendations. Seeding again adds new traces.

Stop the two foreground processes and run
`docker stop ongrid-agent-observability-preview-tempo` to remove the isolated store.
The Python environment can be kept for later fixture verification.

## Regenerate fixture captures

Each SDK runs in a separate process. The script clears inherited telemetry/proxy
settings in that child process and exports only the built-in synthetic workload.
This command replaces the corresponding checked-in fixture.

```sh
for sdk in langsmith openinference openllmetry langfuse; do
  /tmp/ongrid-agent-sdks/bin/python scripts/agent-observability/capture_sdks.py "$sdk"
done
go test -race ./internal/manager/biz/agenttrace ./internal/manager/server/agenttrace ./internal/pkg/tracequery ./cmd/ongrid
```

## Acceptance record: 2026-10-14

- Affected Go tests with the race detector: passed.
- Actual SDK exports: all four adapters parsed without fixture diagnostics.
- Four generated setup snippets: OTLP HTTP export, Tempo persistence, Agent API
  query and scoped identity verified. The preview returned all 28 traces
  (24 seeded scenarios plus four setup runs).
- Frontend build, 13 focused tests and lint for new files: passed.
- Full frontend suite: 407/416 passed initially; nine tests in six existing files
  timed out under parallel load. Those six files passed all 92 tests when rerun
  with one worker. New Agent tests were added and checked separately.
- Browser: run list and detail each checked in Chinese/English, light/dark and
  widths 390, 768, 1280 and 1920. Tables scroll internally; pages do not overflow.
  Run selection, model messages, session grouping, cross-Trace links, empty state,
  diagnostics and setup configuration checked.
- Proto generation and lint, including the new path in `make lint-proto`: passed.
- Full `go test ./...` on macOS: blocked by existing Linux-only packages
  (`aiops/tools`, `cmd/ongrid-edge`) and the existing upgrade destination case test.
- Full frontend lint: existing errors in `chat.ts`, `packetCaptures.ts`,
  `MessageBubble.tsx`, `SkillRun.tsx`. These files are unchanged.
- `make arch-lint` with go-arch-lint 1.12.0: existing configuration fails validation
  (missing `internal/edgeagent/data/**`, empty dependency entries). It is not a pass.

Production ingest through a live Edge/Kubernetes gateway, CI, production deployment, large
traffic, real model streaming and every SDK/framework combination remain unverified.
The list reads at most 50 candidate traces; sessions and statistics describe that
result set, not all application traffic. See
[RFC-005](../../docs/rfc/RFC-005-agent-observability.md) for semantics and limits.

### Trace inspector UI refinement

The same-day UI refinement passed 10 related component/page tests, affected-file
lint and a production build. Real-browser assertions cover 48 list/tree/waterfall
combinations across Chinese/English, light/dark and 390/768/1280/1920px widths,
including long names, internally scrolling tables and aligned waterfall origins.
Collapse/expand, step navigation, Reader/JSON, metadata and empty search were
checked interactively. Full lint remains at the baseline 9 errors / 43 warnings.

Only the local test environment's Web container was updated for this pass. Its
HTML/assets match the build, authenticated APIs and health checks passed, and
other container IDs did not change. This does not verify production ingestion.
