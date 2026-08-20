# ongrid pure-systemd mode

Alternative to docker-compose for operators who can't (or won't) run
docker. Manager + frontier + dep stack all run as native systemd units.
Suite files live under `deploy/install/systemd/`.

## When to pick this mode

- Air-gapped / regulated env that disallows the docker daemon.
- Existing host already runs MariaDB / Prometheus / Grafana under
  systemd; we don't want to dual-stack.
- Want tighter resource/security control via systemd's primitives
  (cgroups, capabilities, namespaces) directly.

For everything else, prefer the default docker-compose mode
(`deploy/install/docker-compose.yml` + `deploy/install/install.sh`).

## Security note

The nginx reverse proxy shipped here (`deploy/install/systemd/nginx/snippets/`) targets a
**trusted internal network** management entry. It has no auth gate and
listens plain HTTP by default. Before exposing it beyond the loopback /
internal network you MUST add TLS + access control yourself
(`auth_basic` / `auth_request` / `allow-deny`). Write-path endpoints
(`/-/` lifecycle, remote-write, loki push) are blocked at the nginx
layer as a defense-in-depth measure.

## Layout

```
/usr/local/bin/ongrid             # manager binary
/usr/local/bin/ongrid-frontier    # tunnel multiplexer binary
/etc/systemd/system/
  ongrid.service                  # manager unit (suite dir → installed here)
  ongrid.service.d/wait-for-deps.conf
  ongrid-frontier.service
  prometheus.service              # local Prom (we own the config)
  loki.service                    # local Loki
  tempo.service                   # local Tempo
  qdrant.service                  # local qdrant
  node_exporter.service
  process_exporter.service
/etc/ongrid/
  ongrid.env                      # manager env (DSNs, LLM key, listen addrs)
  frontier.yaml
  prometheus/prometheus.yml       # from prometheus-scrape.yml (bare-metal jobs)
  prometheus/rules.yml            # self-obs alert rules
  loki-config.yaml
  tempo-config.yaml
/var/lib/ongrid/                  # manager state
/var/lib/ongrid-prometheus/       # TSDB
/loki/                            # log store
/var/tempo/                       # trace store
/var/lib/ongrid-qdrant/           # vector store
/var/log/ongrid/                  # journald is primary; this is for app logs
```

## Install

Prerequisite: stage the manager binaries from a source checkout:

```bash
# at the repo root
make build-ongrid                        # produces bin/ongrid
mkdir -p deploy/install/bin
cp bin/ongrid deploy/install/bin/ongrid
# ongrid-frontier: build from singchia/frontier or fetch its release binary
cp <frontier-binary> deploy/install/bin/ongrid-frontier
```

Then, from the suite directory (`deploy/install/systemd/`):

```bash
sudo bash install-deps.sh                # OS packages + Prom/Loki/Tempo/qdrant binaries
sudo bash install-systemd.sh             # users, units, configs
# render /etc/ongrid/ongrid.env (LLM key via env var to keep it out of shell history)
sudo ONGRID_OPENAI_API_KEY='...' ONGRID_DB_PASSWORD='...' bash render-ongrid-env.sh
```

install-deps.sh:

1. apt/dnf-installs `mariadb-server`, `nginx`, `grafana` (+ apt repo
   fallback for grafana-oss).
2. Downloads Prom / Loki / Tempo / qdrant + node_exporter /
   process_exporter from upstream releases, **verifies sha256**, installs
   to `/usr/local/bin/`.
3. Bootstraps the MariaDB schema + generates DB / grafana passwords.

install-systemd.sh:

1. Creates the `ongrid` system user + per-dep users
   (`ongrid-prometheus`, `ongrid-loki`, `ongrid-tempo`, `ongrid-qdrant`,
   each with its own group).
2. Lays down `/etc/ongrid/` configs (preserves existing on re-run).
3. Installs the manager + frontier binaries to `/usr/local/bin/`.
4. Writes the systemd unit files + nginx reverse-proxy snippets.
5. Runs `systemctl daemon-reload` + `enable` (not `start` — operator
   reviews `/etc/ongrid/ongrid.env` first).
6. Prints the bring-up sequence.

## Uninstall

```bash
sudo bash uninstall-systemd.sh                  # stop + remove units; preserve data
sudo bash uninstall-systemd.sh --purge          # also nuke data dirs + service users
sudo bash uninstall-systemd.sh --purge --yes    # skip the confirmation
```
