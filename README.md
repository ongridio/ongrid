# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **An AI-native SRE workspace that investigates incidents, explains blast radius, and moves approved fixes through governed operations workflows.**

Ongrid connects alerts, metrics, logs, traces, topology, host evidence, runbooks, source code, workflows, and approval gates into one operations loop. It is built for teams that self-host critical infrastructure and want AI assistance without giving up control, auditability, or network boundaries.

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#what-ongrid-covers)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid product demo" width="920" />
</p>

<div align="center">

[Why Ongrid](#why-ongrid) - [Product Tour](#product-tour) - [In Action](#in-action) - [What Ongrid Covers](#what-ongrid-covers) - [Integrations](#integrations) - [Install](#install) - [Docs](#documentation)

</div>

## Why Ongrid

Ongrid is an open-source AIOps / SRE workspace for teams operating real infrastructure. It connects alerts, observability, topology, host evidence, runbooks, source code, workflows, and approval gates into one governed operations loop.

It is not a thin chat wrapper over shell commands. Ongrid is built around production boundaries: evidence before answers, read/write separation, explicit approval, outbound edge access, auditable tool calls, and workflows that can be reviewed before they change systems.

## Product Tour

Ongrid is organized around the real SRE operating loop: automate repeatable work, approve risky changes, keep durable outputs, and connect every answer back to the tools and knowledge that produced it.

### 1. Turn Good Incident Handling Into Workflows

<p align="center">
  <img src="docs/assets/readme/en-workflow-editor.png" alt="Ongrid workflow editor" width="920" />
</p>

Successful investigations can become repeatable workflows with triggers, agent nodes, tool nodes, HTTP calls, conditions, notifications, and generated artifacts.

- Alert, manual, and scheduled triggers.
- AI-generated workflows that remain editable.
- Tool, agent, condition, notification, and artifact nodes in one graph.

### 2. Approve Production Changes Before They Run

<p align="center">
  <img src="docs/assets/readme/en-agent-write-gate.png" alt="Ongrid approval gate" width="920" />
</p>

Ongrid separates reasoning from execution. Agents can propose a restart, config change, command, or remediation step, but humans decide what actually runs.

- Scoped proposals with affected resources.
- Dry-run context, rollback notes, and reviewer controls.
- Approve / reject history attached to the operational record.

### 3. Keep Durable Operational Artifacts

<p align="center">
  <img src="docs/assets/readme/en-artifacts.png" alt="Ongrid artifacts center" width="920" />
</p>

Generated pages and reports are private by default, shareable only when operators decide, and useful for handoff, retrospectives, customer updates, and daily briefs.

- RCA pages, operations reports, and investigation summaries.
- Explicit sharing and TTL controls.
- Artifacts tied back to tasks, workflows, and incidents.

### 4. Run One-off and Recurring Operations From One Place

<p align="center">
  <img src="docs/assets/readme/en-tasks.png" alt="Ongrid unified tasks" width="920" />
</p>

Scheduled reports, one-off investigations, and generated outputs share the same task surface. Operators can see what generated each report, when it runs next, and which artifacts are ready to review.

- One-off jobs and recurring schedules in the same view.
- Output history tied to reports and generated pages.
- Clear next-run and status information for handoff.

### More Product Surfaces

| Surface | What it does |
|---|---|
| **Investigation workspace** | Starts from an alert or operator question, gathers evidence across metrics, logs, traces, topology, host state, changes, runbooks, and code, then explains root cause and blast radius. |
| **Knowledge Vault** | Indexes runbooks, incident history, architecture notes, uploaded docs, and source repositories so agents can ground answers in internal context. |
| **Skills and MCP** | Shows every built-in skill and external MCP tool with runtime location, risk class, schema, and approval behavior. |
| **Fleet Control** | Uses outbound edge agents for device inventory, browser shell, process inspection, journal reads, filesystem checks, DNS, TCP, and HTTP diagnostics without inbound SSH. |

## In Action

### Incident response with an approval boundary

```text
Alert: checkout-api p99 latency is above SLO.
Ongrid: checks Prometheus, Loki, Tempo, topology, recent changes, runbooks, code, and host state.
Finding: db-read-1 has IO saturation after a backup job; checkout-api waits on read replicas.
Proposal: pause the backup job for 30 minutes and restart one unhealthy worker.
Gate: operator approves or rejects the proposal before anything mutating runs.
Output: RCA page, evidence links, approval history, rollback note, and customer update draft.
```

### Daily operations brief

```text
Trigger: every weekday at 09:00.
Flow: collect fleet health, top alerts, slow traces, noisy hosts, open approvals, and recent changes.
Output: a private report page for handoff, with share controls and TTL.
```

### Remote diagnostics without inbound SSH

```text
Ask: Inspect nginx memory, open files, and recent kernel messages on edge-03.
Ongrid: runs approved read-only host tools through the outbound edge tunnel.
Output: audited command results in the browser and attached to the incident timeline.
```

## What Ongrid Covers

| Layer | Capabilities |
|---|---|
| **Investigation** | Alerts, RCA, specialist agents, evidence collection, confidence, blast radius, and next actions. |
| **Knowledge** | Runbooks, incident history, architecture notes, repositories, semantic search, path filters, and tags. |
| **Tools** | Built-in skills, MCP servers, host diagnostics, observability queries, hosted pages, and IM delivery. |
| **Governance** | Approval gate, risk classes, dry-run context, rollback notes, reviewer controls, and audit trail. |
| **Automation** | Visual workflows, AI-generated flows, manual triggers, alert triggers, schedules, and unified tasks. |
| **Artifacts** | RCA pages, daily reports, investigation summaries, share links, TTL controls, and task output history. |
| **Platform** | Self-hosted manager, outbound edge agents, browser shell, built-in observability, and BYO model providers. |

## Install

Download the latest release for your server architecture (`linux-amd64` or `linux-arm64`), extract it, and run the installer (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

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

## Documentation

The full product documentation is available at [ongrid.cloud](https://ongrid.cloud/docs/get-started/introduction). This branch also includes website-ready product copy in [docs/product-showcase.md](docs/product-showcase.md) and workflow copy in [docs/workflow-catalog.md](docs/workflow-catalog.md).

| Area | Start here |
|---|---|
| **Get started** | [Introduction](https://ongrid.cloud/docs/get-started/introduction) - [Quickstart](https://ongrid.cloud/docs/get-started/quickstart) - [Architecture](https://ongrid.cloud/docs/get-started/architecture) - [Concepts](https://ongrid.cloud/docs/get-started/concepts) |
| **Install and operate** | [Server install](https://ongrid.cloud/docs/install/server) - [Edge install](https://ongrid.cloud/docs/install/edge) - [First boot](https://ongrid.cloud/docs/install/first-boot) - [Upgrade](https://ongrid.cloud/docs/install/upgrade) |
| **Capabilities** | [Alerts](https://ongrid.cloud/docs/capabilities/alerts) - [RCA](https://ongrid.cloud/docs/capabilities/rca) - [Monitoring](https://ongrid.cloud/docs/capabilities/monitoring) - [Logs](https://ongrid.cloud/docs/capabilities/logs) - [Traces](https://ongrid.cloud/docs/capabilities/traces) - [Knowledge](https://ongrid.cloud/docs/capabilities/knowledge) - [Skills](https://ongrid.cloud/docs/capabilities/skills) |
| **Agents** | [Overview](https://ongrid.cloud/docs/agents/overview) - [Coordinator](https://ongrid.cloud/docs/agents/coordinator) - [Incident investigator](https://ongrid.cloud/docs/agents/incident-investigator) - [Specialists](https://ongrid.cloud/docs/agents/specialists) - [Reviewer](https://ongrid.cloud/docs/agents/reviewer) |
| **Reference** | [API](https://ongrid.cloud/docs/reference/api) - [CLI](https://ongrid.cloud/docs/reference/cli) - [Alert rules](https://ongrid.cloud/docs/reference/alert-rules) - [Skill manifest](https://ongrid.cloud/docs/reference/skill-manifest) - [Data plane](https://ongrid.cloud/docs/reference/data-plane) |

## Project Map

| Area | Path |
|---|---|
| Manager and edge binaries | [`cmd/`](cmd/) |
| Go backend domains | [`internal/`](internal/) |
| React control plane | [`web/`](web/) |
| API contracts | [`api/`](api/) |
| Deployment assets | [`deploy/`](deploy/) |
| Built-in agent skills | [`skills/`](skills/) |
| Specialist agent prompts | [`agents/`](agents/) |

## Contributors

Thanks to everyone helping build Ongrid.

<a href="https://github.com/ongridio/ongrid/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=ongridio/ongrid" alt="Ongrid contributors" />
</a>

## License

Apache 2.0 - see [LICENSE](LICENSE).

## Star History

<a href="https://www.star-history.com/#ongridio/ongrid&amp;Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date&amp;theme=dark" />
    <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date" />
  </picture>
</a>
