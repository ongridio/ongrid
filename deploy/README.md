# deploy/

Deployment artifacts for ongrid. `deploy/docker-compose.yml` is for local
development; production-style install assets live under `deploy/install/`.

## Contents

- `Dockerfile.ongrid` — multi-stage build for the cloud service (`cmd/ongrid`). Final image is `debian:bookworm-slim`, nonroot — **not** distroless/static: the cloud binary is cgo-linked because the local ONNX embedder (fastembed-go → libonnxruntime) needs glibc + libstdc++ + libgomp, and the image bundles ONNX Runtime under `ONNX_PATH`.
- `Dockerfile.ongrid-edge` — the edge agent (`cmd/ongrid-edge`) is pure-Go (`CGO_ENABLED=0`), so *its* image is distroless + static (`gcr.io/distroless/static-debian12:nonroot`) — the opposite of the cloud image, which carries the embedder. Exposes `9101` only for local `/metrics` debug; the agent dials out.
- `docker-compose.yml` — spins up `mysql`, `ongrid`, `frontier`, `nginx`, `prometheus`, and `grafana` on the `ongrid_net` bridge. MySQL is the default backend; its data lives in the named volume `mysql_data`. `ongrid-edge` is intentionally excluded (edge runs on user hosts) — a commented stanza at the bottom shows how to run one for demos.
- `install/` — production-style release package assets, with an install guide (`install/README.md`) covering package upload, install, upgrade, and verification on a target host.
- `prometheus/prometheus.yml` — minimal scrape config: jobs `prometheus` and `ongrid-manager` -> `ongrid:9100/metrics`.

## Quickstart

```
cp .env.example .env     # edit secrets as needed (see "First login" below)
make compose-up          # docker compose -f deploy/docker-compose.yml up -d
make compose-down        # stop
```

MySQL auto-starts as part of compose. The `ongrid` service is wired with `depends_on: mysql: { condition: service_healthy }`, so the app waits until `mysqladmin ping` succeeds before it tries to connect. On a clean volume, the MySQL container takes ~10–20s to pass its healthcheck; `make compose-up` blocks until the cloud service is up.

Schema is managed via GORM `AutoMigrate` at startup — every `ongrid` boot reconciles the DB schema against the code. There is no separate `migrate` step.

Images are built with `make docker` (wraps `docker build` with the correct context = repo root).

## Prometheus

Prometheus is part of the local dev stack by default. It:

- scrapes the ongrid manager on `ongrid:9100`;
- receives manager `remote_write` traffic for open-set exporter samples;
- serves the AIOps `query_promql` capability through the manager.

Local dev publishes the UI at `http://localhost:9090`. The install compose
does **not** publish Prometheus to the host; on server installs, verify it
from inside the box:

```bash
ssh root@<host> 'docker exec ongrid-prometheus wget -qO- http://localhost:9090/-/ready'
ssh root@<host> 'docker exec ongrid-prometheus wget -qO- "http://localhost:9090/api/v1/query?query=up"'
```

## Grafana

Grafana is now part of both the local dev stack and the install stack:

- local dev publishes Grafana at `http://localhost:3000`;
- the install stack keeps Grafana private on the Docker network and exposes it through nginx at `https://<host>/grafana/`;
- nginx applies the same ongrid session auth gate used for `/prometheus/`.

Grafana is provisioned with one default datasource:

- name: `ongrid-prometheus`
- uid: `ongrid-prometheus`
- url: `http://prometheus:9090/prometheus`

The install/local packages also provision one default dashboard template:

- title: `服务器详情`
- uid: `ongrid-server-detail`
- variable: `edge_id`

## First login

Ongrid is self-managed: there is no public signup and no central auth. The
cloud binary seeds the first admin user from env on startup:

- `ONGRID_ADMIN_EMAIL` — email the admin logs in with.
- `ONGRID_ADMIN_PASSWORD` — initial password (change it at first login).

Behaviour:

- If **both** are set and no user with that email exists yet, ongrid creates
  a user with `role=admin`, `status=active`.
- If a user with that email already exists, the env is ignored (we do not
  silently overwrite a password — rotate via the admin UI / API instead).
- If **either** is empty, no admin is seeded. The server still starts and
  serves HTTP, but the `users` table is empty and nobody can log in. Set
  the envs in `.env` and restart to recover.

In production, set both to strong values in `.env` **before** the first
`compose up`. Do not commit `.env`.

## Endpoints (once up)

| URL                             | What                            |
|---------------------------------|---------------------------------|
| `http://localhost:8080`         | ongrid HTTP API                 |
| `http://localhost:9100/metrics` | Prometheus `/metrics` on ongrid |
| `http://localhost:40012`         | edge tunnel listener (geminio)  |
| `http://localhost:9090`         | Prometheus UI (targets, graph)  |
| `localhost:3306`                | MySQL (user `ongrid`, pw `ongrid`, db `ongrid`) |

Data lives in the `mysql_data` Docker volume. Back that volume up with
`sudo tar czf mysql.tgz -C "$(docker volume inspect -f '{{.Mountpoint}}' mysql_data)" .`
or the equivalent in your ops tooling.

## Switching to SQLite (local tinkering)

For fast single-user local dev without a MySQL container, the cloud service
can be pointed at a SQLite file. In `deploy/docker-compose.yml`, uncomment
the `volumes` stanza on the `ongrid` service (there is a marker comment in
the file), then set in `.env`:

```
ONGRID_DB_DIALECT=sqlite
ONGRID_DB_PATH=/data/ongrid.db
```

You can then drop the `mysql` service from the compose stack (`docker
compose up ongrid prometheus`). SQLite is fine for playing with the API;
don't use it for anything you care about losing.

## Warning — not production

This compose stack is a **local dev convenience only**. In particular:

- No TLS anywhere (neither HTTP API nor the edge tunnel).
- MySQL runs with a trivial root password baked into the compose file; the
  `ongrid` user has full privileges on the `ongrid` database. Rotate both
  before exposing the stack to anything but `localhost`.
- No rate limiting, no auth proxy, no log shipping.
- Prometheus is tuned for local durability (`90d` / `20GB`) and remote-write ingest, but still has no production alerting rules in this dev compose.

For a production-style deployment (TLS termination via nginx, managed MySQL /
backups, single-host layout), use the release package and follow
`install/README.md`.
