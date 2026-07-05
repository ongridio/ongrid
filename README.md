# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **An infrastructure AI agent that investigates incidents, explains the blast radius, and executes approved fixes from chat.**

Ongrid brings metrics, logs, traces, topology, host inspection, workflows, knowledge retrieval, and approval gates into one operator-facing control plane. It is built for self-hosted teams that want the agent close to their infrastructure, with every action audited.

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.169/Area2_hq.mp4">▶ Watch full demo in HD (MP4, 18 MB)</a></sub></p>

<div align="center">

[What it does](#what-it-does) • [Operating themes](#operating-themes) • [Examples](#examples) • [Install](#install) • [Integrations](#integrations)

</div>

## What it does

| Capability | What operators get |
|---|---|
| **Incident investigation** | Alert-triggered RCA workers correlate PromQL, LogQL, TraceQL, topology, host facts, and recent changes. |
| **Chat operations** | Slack, Telegram, Larksuite, DingTalk, WeCom, and generic webhooks become two-way operations channels. |
| **Secure host access** | Edge agents dial out through a reverse tunnel, so hosts need no inbound SSH or public ports. |
| **Action approval** | Mutating actions can be proposed, previewed, approved, executed, and audited instead of silently run. |
| **Workflow studio** | Build and run repeatable incident, inspection, and remediation flows from the UI. |
| **Knowledge retrieval** | Search internal docs, skills, code, runbooks, and incident history during the investigation loop. |

## Operating themes

| Theme | Screenshot idea | Why it is different |
|---|---|---|
| **Incident Room** | RCA timeline with cause, evidence, related logs, trace spans, and approval cards. | Turns an alert into a structured investigation record instead of another chat thread. |
| **Fleet Control** | Edge inventory, device shell, topology neighbors, and health state in one view. | Operators can inspect hosts without opening inbound ports or distributing SSH keys. |
| **Workflow Studio** | Visual flow editor with AI-generated nodes, approvals, retries, and outputs. | Reusable operations workflows sit next to the agent instead of living in scattered scripts. |
| **Knowledge Vault** | Search results from docs, skills, source code, and past incident reports. | The agent can cite internal context while humans keep the source of truth visible. |
| **Approval Gate** | Proposed command, dry-run output, blast radius, approver, and audit trail. | Keeps automation useful without making production changes invisible. |

## Examples

### 1. Alert-to-root-cause

```text
Alert: payment-api p99 latency is above SLO
Ongrid: checks Prometheus, Loki, Tempo, topology, recent deploys, and host pressure
Result: "db-read-1 has IO saturation after a backup job; payment-api waits on read replicas"
Next: propose pausing the backup job and attach the exact command for approval
```

### 2. Natural-language observability

```text
Ask: "Show whether the checkout errors started before or after the deploy"
Ongrid: generates PromQL + LogQL, runs them, and links the rendered Grafana panel
Result: one answer with query evidence instead of a hand-written dashboard hunt
```

### 3. Remote diagnostics without inbound SSH

```text
Ask: "Inspect nginx memory, open files, and recent kernel messages on edge-03"
Ongrid: runs approved read-only host tools through the outbound edge tunnel
Result: audited command output in the browser and in the incident timeline
```

### 4. Controlled remediation

```text
Ask: "Restart only the unhealthy workers in us-east"
Ongrid: finds candidates, builds a proposal, estimates blast radius, and waits for approval
Result: execution output, rollback notes, and audit records are attached to the same incident
```

## Install

Download the latest release for your server architecture (`linux-amd64` or `linux-arm64`), extract it, and run the installer (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

Choose the command for your server architecture:

**AMD64**
```bash
wget https://github.com/ongridio/ongrid/releases/download/v0.9.0/ongrid-v0.9.0-linux-amd64.tar.xz
tar -xf ongrid-v0.9.0-linux-amd64.tar.xz && cd ongrid-v0.9.0-linux-amd64
sudo ./install.sh
```

**ARM64**
```bash
wget https://github.com/ongridio/ongrid/releases/download/v0.9.0/ongrid-v0.9.0-linux-arm64.tar.xz
tar -xf ongrid-v0.9.0-linux-arm64.tar.xz && cd ongrid-v0.9.0-linux-arm64
sudo ./install.sh
```

If GitHub downloads are slow, use the matching CDN mirror URL instead:

```bash
# AMD64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-amd64.tar.xz

# ARM64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-arm64.tar.xz
```

## Integrations

Ongrid connects to the observability, chat, and model stacks your team already uses.

| | |
|---|---|
| **Observability** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Channels** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Models** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## Architecture

```text
Chat / Web UI
    |
    v
Ongrid Manager
    |-- agent runtime
    |-- workflow engine
    |-- approval gate
    |-- knowledge retrieval
    |-- observability query layer
    |
    v
Outbound Edge Tunnel
    |
    v
Customer hosts, services, logs, metrics, traces, and topology
```

## Project map

| Area | Path |
|---|---|
| Manager and edge binaries | [`cmd/`](cmd/) |
| Go backend domains | [`internal/`](internal/) |
| React control plane | [`web/`](web/) |
| API contracts | [`api/`](api/) |
| Deployment assets | [`deploy/`](deploy/) |
| Built-in agent skills | [`skills/`](skills/) |
| Specialist agent prompts | [`agents/`](agents/) |

## License

Apache 2.0 — see [LICENSE](LICENSE).

## Star History

<a href="https://www.star-history.com/#ongridio/ongrid&amp;Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date&amp;theme=dark" />
    <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date" />
  </picture>
</a>
