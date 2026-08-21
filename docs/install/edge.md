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
