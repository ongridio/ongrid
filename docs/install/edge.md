# Edge Agent — Installation Guide

Edge agents connect managed hosts to the ongrid control plane. The agent dials **out** — no inbound ports required on the host.

## Quick start (release tarball)

Generate an install command from the web UI under **Devices** (`/devices`), then run it on the target host:

```bash
curl -k -sSL https://<server>/install.sh | bash -s -- \
  --access-key=<access-key> \
  --secret-key=<secret-key> \
  --server-edge-addr=<server>:40012 \
  --server-http-addr=<server>
```

The install script detects the host architecture and downloads the matching binary from `https://<server>/edge/ongrid-edge-<os>-<arch>`.

## Diagnostics port conflicts

Ordinary host and Kubernetes Node agents prefer TCP port `9101` for their own
`/healthz` and `/metrics` endpoints. If that port is occupied, the OS allocates a
free port without interrupting startup. The `edge diagnostics listener bound`
log records `requested_addr`, `actual_addr`, `mode`, and `fallback_reason`.
The chosen port stays bound until exit and may change after a restart.
This does not change the cloud tunnel or host/process exporter ports.

If an external scraper, Docker port mapping, or firewall requires a fixed port,
set `ONGRID_EDGE_METRICS_ADDR=:19101` in `/etc/ongrid-edge/ongrid-edge.env` and
restart the service. You can also export this variable to the installer; both
Linux installers preserve it on reinstall unless an explicit new value is supplied.
An empty value restores automatic mode. Fixed addresses must contain a numeric
port from 1 to 65535; a bind failure is reported rather than silently changing it.
For loopback-only access, use `127.0.0.1:19101`.

The standalone Kubernetes telemetry Gateway and metrics Scraper retain their
fixed diagnostics listeners and HTTP probes. Logs, OBI, OTLP, pprof, and exporter
listeners are not changed by this fallback. If automatic allocation also fails,
startup fails with the bind errors; no existing process is stopped or reused.

## Kubernetes resource budgets

Each Edge container and its subprocesses share one CPU/memory budget. Auto APM,
logs, traces, and Gateway Collectors have no independent memory limiter;
legacy Collector memory settings are ignored. Batches, exporter queues, retry
policies, and log checkpoints remain bounded as before.

Chart defaults below are per container. They are a starting configuration for
dozens of nodes and around a thousand Pods, not a load-tested capacity guarantee.

| Component | CPU / memory request | CPU / memory limit | Replicas |
| --- | --- | --- | --- |
| Node Edge (including OBI and all node Collectors) | 50m / 128Mi | 4 / 8Gi | One per node |
| Telemetry Gateway | 200m / 256Mi | 4 / 8Gi | HPA 2–10 |
| Kubernetes metrics Scraper | 100m / 128Mi | 4 / 8Gi | 1 |
| Controller | 50m / 128Mi | 2 / 4Gi | 1 |
| kube-state-metrics | 20m / 64Mi | 2 / 4Gi | 1 |

Requests retain the lightweight defaults; raise them to measured sustained usage
on busy clusters. They affect scheduling and resource contention; limits are
ceilings, not reserved capacity. Existing explicit Helm resource overrides win;
when retaining smaller limits, also keep requests at or below those limits.
Ordinary host installs need systemd resource limits to enforce an overall cap.

Gateway HPA requires metrics-server. Its default memory target is half the
container limit (4Gi), and CPU utilization is 300% of the CPU **request**
(200m × 300% = 600m per Pod). Tune the percentage together with CPU requests;
explicit legacy percentages are preserved. Kubernetes permits CPU utilization
targets above 100% because requests are not CPU ceilings.
Explicit memory targets are preserved and must stay at or below 80% of the container
limit. Collectors can still be OOM-killed during bursts; removing their memory
limiter trades early backpressure for reliance on the container's OOM boundary.

Validate with actual log bytes/s, spans/s, active series, queue occupancy,
scrape duration, CPU throttling and memory usage. Gateway can scale horizontally;
the current Scraper is single-active and kube-state-metrics is unsharded. Do not
replicate scrapers without target partitioning. An overloaded Manager or storage
backend needs its own capacity work; raising Edge limits will not fix it. See
[Collector scaling](https://opentelemetry.io/docs/collector/scaling/) and
[kube-state-metrics scaling](https://github.com/kubernetes/kube-state-metrics#scaling-kube-state-metrics).

Auto APM has no per-scrape sample count limit and sends at most 1,000 samples
per tunnel request. A complete scrape is still held in Edge memory for resource
enrichment. HTTP scraping has a 10s timeout; resource enrichment and all batches
share one 60s push budget. Scrapes remain serial, and stopping the plugin cancels
an in-flight push. Other custom metrics targets retain the 15s default push
timeout, configurable per target with `push_timeout`. The container resource
budget still applies.
The separate cluster metrics scraper defaults to 250,000 samples per target.
Roll back both the Edge image and Chart to restore the previous Collector policy
and resource defaults.

## Batch install (non-Kubernetes fleets)

Open **Devices → Batch install** to create a bounded installation profile. A profile can either be:

- **Batch only**: devices share an installation batch for auditing, with no topology relationship.
- **Attach to cluster**: every enrolled device is automatically linked as `Device --member_of--> Cluster` when it first connects.

The generated command can be run on multiple hosts:

```bash
curl -k -sSL https://<server>/install.sh | bash -s -- \
  --enrollment-token=<token> \
  --server-edge-addr=<server>:40012 \
  --server-http-addr=<server> \
  --tls-insecure
```

The enrollment token is only a short-lived bootstrap capability. Each host exchanges it for a different AccessKey and SecretKey; devices never share a tunnel identity. Profiles expire, have a maximum device count, and can be revoked from the same dialog. Re-running the command on a host that has already completed enrollment is rejected instead of replacing its active credentials.

For a production deployment with a trusted HTTPS certificate, remove both `-k` and `--tls-insecure` from the command.

## Build and stage the edge binary (source installs only)

Release tarballs include pre-built binaries. If you are running from source, build and stage the binary before any host can install:

```bash
# 1. Cross-compile the edge agent for linux/amd64
make build-edge-linux-amd64
# Output: bin/linux-amd64/ongrid-edge

# 2. Fetch the exporter/collector bundles the edge plugins rely on.
#    The edge ships metrics/logs/traces via node_exporter, process_exporter,
#    otelcol-contrib — these are NOT produced by `go build`. Without
#    them the edge installs fine but has no data source: Monitor panels stay
#    empty, and there are no logs or traces.
make fetch-node-exporter fetch-process-exporter fetch-otelcol

# 3. Stage so nginx serves the edge binary at /edge/ongrid-edge-linux-amd64
cp bin/linux-amd64/ongrid-edge bin/ongrid-edge-linux-amd64
```

> **Tip:** the cleanest source path is `make package` — it cross-compiles the
> edge, fetches all four bundles, and stages everything into one tarball (the
> same artifact a release ships). The manual steps above only serve a bare
> edge binary for local development.

For all architectures (linux amd64/arm64, darwin amd64/arm64):

```bash
make build-edge-all
cp bin/linux-amd64/ongrid-edge  bin/ongrid-edge-linux-amd64
cp bin/linux-arm64/ongrid-edge  bin/ongrid-edge-linux-arm64
```

## Register a host

1. Open the web UI and go to **Devices** (`/devices`).
2. Create a new device — the UI generates a one-time `access-key` and `secret-key`.
3. Copy and run the generated install command on the target host.

> **Important:** The `access-key` is issued by the control plane. Arbitrary strings are rejected with `unauthorized`.

## Configuration notes

### Tunnel port

`--server-edge-addr` points to the geminio tunnel endpoint. The default port is **40012**, but `install.sh` increments automatically if the port is already in use. Always check the actual value in `.env` (`ONGRID_TUNNEL_PORT`):

```bash
grep ONGRID_TUNNEL_PORT /opt/ongrid/.env
```

### Same-host installs (hairpin NAT)

If the control plane and the edge agent run on the **same host**, use `127.0.0.1` instead of the public IP for `--server-edge-addr`. Most cloud and on-premises networks block hairpin NAT (a host reaching its own public IP):

```bash
--server-edge-addr=127.0.0.1:40012
```

### TLS

Port 443 (nginx) handles TLS termination. The tunnel port (default 40012) uses plain TCP — do not configure a TLS CA for the edge connection.

The self-signed certificate warning on `curl` is expected. For single-device installation, `-k` suppresses verification only while downloading the script and binaries. Batch enrollment also contacts the Manager API, so the default self-signed command includes `--tls-insecure`; remove both options after installing a trusted certificate.

## Verify the connection

```bash
journalctl -u ongrid-edge -f
# Look for: "tunnel: connected" with server_addr and edge_id
```

A successful connection looks like:

```
{"level":"INFO","msg":"tunnel: connected","server_addr":"127.0.0.1:40012"}
```

The device will appear in the web UI under **Devices** once connected.
