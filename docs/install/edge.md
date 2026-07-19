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

## Build and stage the edge binary (source installs only)

Release tarballs include pre-built binaries. If you are running from source, build and stage the binary before any host can install:

```bash
# 1. Cross-compile the edge agent for linux/amd64
make build-edge-linux-amd64
# Output: bin/linux-amd64/ongrid-edge

# 2. Fetch the exporter/collector bundles the edge plugins rely on.
#    The edge ships metrics/logs/traces via node_exporter, process_exporter,
#    promtail and otelcol — these are NOT produced by `go build`. Without
#    them the edge installs fine but has no data source: Monitor panels stay
#    empty, and there are no logs or traces.
make fetch-node-exporter fetch-process-exporter fetch-promtail fetch-otelcol

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

The self-signed certificate warning on `curl` is expected — the `-k` flag suppresses it for the install script download only.

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

## Windows hosts

Windows agents run a supervisor + worker pair installed as a Windows Service. The supervisor owns the worker lifecycle (start/stop/restart) and self-updates via a rename-aside swap. Like the Linux agent, the worker dials **out** — no inbound ports required on the host.

### Quick start (release zip)

Download the Windows bundle from the release artifacts (or stage it from source — see below) and unpack it to `C:\Program Files\ongrid-edge\bin`:

```powershell
$installDir = "C:\Program Files\ongrid-edge\bin"
$dataDir    = "C:\ProgramData\ongrid-edge"
New-Item -ItemType Directory -Force -Path $installDir, $dataDir, "$dataDir\plugins" | Out-Null
Expand-Archive .\ongrid-edge-windows-amd64.zip -DestinationPath $installDir
```

The bundle contains `ongrid-edge-supervisor.exe`, `ongrid-edge-worker.exe`, and the exporter/collector plugins (node_exporter-equivalent metrics, promtail logs, otelcol traces).

### Build and stage the Windows binaries (source installs only)

Release bundles ship pre-built Windows binaries. If you are building from source:

```powershell
make build-edge-windows-amd64
# Output: bin/windows-amd64/ongrid-edge-supervisor.exe + ongrid-edge-worker.exe

# Fetch the exporter/collector bundles the worker plugins rely on
make fetch-node-exporter fetch-process-exporter fetch-promtail fetch-otelcol

# Stage so nginx serves the bundle at /edge/ongrid-edge-windows-amd64.zip
Copy-Item bin\windows-amd64\ongrid-edge-*.exe bin\
```

### Install the service

The install is performed by `ongrid-edge-supervisor.exe --install`, which:

1. Encrypts the broker token with DPAPI (`CRYPTPROTECT_LOCAL_MACHINE`) into `C:\ProgramData\ongrid-edge\secrets.enc`.
2. Creates the `ongrid-edge` Windows Service and writes its environment (cloud addr, access key, secrets path) to the service registry.
3. Starts the service.

The DPAPI encryption must run as `NT AUTHORITY\SYSTEM`. The simplest portable way is a one-shot scheduled task:

```powershell
$bin = "C:\Program Files\ongrid-edge\bin"
$cmd = "`"$bin\ongrid-edge-supervisor.exe`" --install " +
       "--token <broker_token> " +
       "--cloud-addr <server>:40012 " +
       "--access-key <access_key> " +
       "--collector-mode off " +
       "--plugin-bin-dir `"$bin`" " +
       "--plugin-work-dir `"C:\ProgramData\ongrid-edge\plugins`""
$cmd | Set-Content "$env:TEMP\install_ongrid.bat" -Encoding ASCII

schtasks /create /tn "ongrid_install" /tr "$env:TEMP\install_ongrid.bat" `
         /sc once /st 23:59 /ru SYSTEM /rl HIGHEST /f
schtasks /run /tn "ongrid_install"
Start-Sleep -Seconds 8
schtasks /delete /tn "ongrid_install" /f
```

For fleet deployments, any tool that runs a command as `SYSTEM` (GPO scheduled task, SCCM, DSC, `psexec -s -i`) works equally well.

> **Important:** The `access_key` is issued by the control plane via the web UI under **Devices** (`/devices`), same as on Linux. Arbitrary strings are rejected with `unauthorized`.

### Configuration notes

#### Service account (SYSTEM required)

`--install` calls DPAPI `CryptProtectData` with the `CRYPTPROTECT_LOCAL_MACHINE` flag, which needs access to the machine's `SystemCredential` DPAPI profile. This profile is only available to `SYSTEM` (and `NetworkService`, which is also a System identity). An interactive Administrator session may succeed on some Windows configurations and fail with `Access is denied` on others, depending on local policy. Running via a `SYSTEM` scheduled task (shown above) is the only universally reliable path.

Outbound network requirements: the worker connects to `<server>:40012` (TCP). Windows Firewall allows outbound by default, but if your policy restricts outbound traffic, add a rule:

```powershell
New-NetFirewallRule -DisplayName "ongrid-edge-outbound" `
    -Direction Outbound -Protocol TCP -RemotePort 40012 -Action Allow -Profile Any
```

No inbound ports are required on the host.

#### Microsoft Defender exclusions

Defender real-time protection scans `ongrid-edge-worker.exe` during upgrades and can hold an exclusive lock on the binary, breaking the rename-aside self-update. Add exclusions before the first upgrade:

```powershell
Add-MpPreference -ExclusionPath "C:\Program Files\ongrid-edge"
Add-MpPreference -ExclusionPath "C:\ProgramData\ongrid-edge"
Add-MpPreference -ExclusionProcess "ongrid-edge-worker.exe"
Add-MpPreference -ExclusionProcess "ongrid-edge-supervisor.exe"

# Verify
Get-MpPreference | Select-Object -ExpandProperty ExclusionPath
Get-MpPreference | Select-Object -ExpandProperty ExclusionProcess
```

#### Time synchronization (NTP)

The control plane rotates broker tokens on a fixed cadence with a short grace window. If the host clock drifts too far from the control plane, the worker's token is rejected as expired and the agent drops offline with `unauthorized: token expired` in `C:\ProgramData\ongrid-edge\worker-stderr.log`. Keep drift under 5 minutes against the same time source the control plane uses.

Check the current source and sync state:

```powershell
w32tm /query /status
# Expect: Source = domain controller or external NTP; Last Successful Sync < 24h
```

For hosts in an Active Directory domain, `W32time` synchronizes to the domain controller by default. For standalone hosts, point `W32time` at a public NTP source (e.g. `time.windows.com` or your upstream NTP) that shares a common ancestry with the control plane.

### Verify the connection

```powershell
Get-Service ongrid-edge
# Expect: Status=Running, StartType=Automatic

Get-Content "C:\ProgramData\ongrid-edge\worker-stderr.log" -Tail 30
# Expect: "tunnel: connected" with server_addr and edge_id
```

A successful connection looks like:

```
{"level":"INFO","msg":"tunnel: connected","server_addr":"<server>:40012"}
```

The device will appear in the web UI under **Devices** once connected.

To uninstall:

```powershell
.\ongrid-edge-supervisor.exe --uninstall
```

This stops and deletes the service and removes `secrets.enc`. Unlike `--install`, `--uninstall` can be run by an Administrator (it does not touch DPAPI).
