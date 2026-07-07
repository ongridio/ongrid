# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **An ops AI that understands, finds the root cause, and fixes things.** *Monitoring, remote execution, knowledge base, specialist agents, Bash, files, and more skills — issue commands directly from Slack, Telegram, or Lark.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#)

English | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[Features](#features) • [Install](#install) • [Integrations](#integrations) • [License](#license)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.169/Area2_hq.mp4">▶ Watch full demo in HD (MP4, 18 MB)</a></sub></p>

## Features

- 🤖 **Coordinator + Specialist agents** — coordinator dispatches to SRE / network / DB sub-agents
- 🚨 **Auto-investigate on alert** — investigator spawns an RCA worker, writes the cause back to chat
- 🔍 **Root-cause RCA** — walks topology, correlates m/l/t, pins the "why" to a source-code line
- 🔒 **Zero inbound ports** — edge dials out; no port 22 / 80 / 443 on hosts
- 💻 **Browser SSH** — reverse-tunnel shell into any host; no keys, no jumpbox, all audited
- 🐳 **Self-host in one command** — `docker compose up` brings up the full stack
- 📊 **Built-in observability** — Prometheus + Loki + Tempo + Grafana wired; the agent writes the queries
- 🧠 **Bring your own model** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, hot routing
- 💬 **Two-way IM channels** — Slack / Telegram / Larksuite / DingTalk / WeCom, per-channel locale
- 🛠️ **Read-only host tools** — bash sandbox + 26+ inspection tools; every call audited

## Install

Download the latest release, extract it, and run the installer (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

```bash
# 1. Download latest release (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9)
wget https://github.com/ongridio/ongrid/releases/download/v0.7.169/ongrid-v0.7.169-linux-amd64.tar.xz

# 2. Extract
tar -xf ongrid-v0.7.169-linux-amd64.tar.xz && cd ongrid-v0.7.169-linux-amd64

# 3. Install
sudo ./install.sh
```

### Or run from source

Local dev: set the admin account + one model API key, then bring up the full stack.

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## Product Tour

Ongrid is organized around the real SRE operating loop: detect, investigate, use governed tools, automate repeatable work, preserve outputs, and keep the surrounding knowledge and topology visible to humans.

### 1. Evidence-backed RCA artifacts

<p align="center">
  <img src="docs/assets/readme/live/en-rca-artifact-view.png" alt="Ongrid RCA artifact" width="920" />
</p>

Agents turn an incident or operator question into a reviewable artifact with signal summary, blast radius, evidence, confidence, and the next approved step. The output is dark-theme, shareable, and useful for handoff rather than just another chat answer.

### 2. Workflow studio for repeatable operations

<p align="center">
  <img src="docs/assets/readme/live/en-workflow-editor.png" alt="Ongrid workflow editor" width="920" />
</p>

Successful investigations can become editable workflows with alert, manual, or scheduled triggers; agent and tool nodes; conditions; notifications; and generated pages or reports.

### 3. Approval boundary for write actions

<p align="center">
  <img src="docs/assets/readme/en-agent-write-gate.png" alt="Ongrid approval gate" width="920" />
</p>

Ongrid keeps reasoning separate from execution. Agents can propose a restart, config change, command, or remediation step, but humans decide what actually runs.

### 4. Tasks for one-off and recurring operations

<p align="center">
  <img src="docs/assets/readme/en-tasks.png" alt="Ongrid unified tasks" width="920" />
</p>

Scheduled reports, one-off investigations, and generated outputs share the same task surface. Operators can see what generated each report, when it runs next, and which artifacts are ready to review.

### 5. Artifacts center for durable outputs

<p align="center">
  <img src="docs/assets/readme/live/en-artifacts-pages.png" alt="Ongrid artifacts center" width="920" />
</p>

Generated RCA pages, operational reports, daily briefs, and customer-ready summaries stay private by default. Operators can inspect, share, and reuse outputs without hunting through chat history.

### 6. Governed skills and external MCP tools

<p align="center">
  <img src="docs/assets/readme/live/en-skills-catalog.png" alt="Ongrid skills catalog" width="920" />
</p>

The Skills catalog shows what the agent can call, where it runs, and its risk class. Built-in SRE tools cover observability, devices, incidents, knowledge, cloud actions, artifacts, and messaging.

<p align="center">
  <img src="docs/assets/readme/live/en-mcp-inventory.png" alt="Ongrid MCP server inventory" width="920" />
</p>

External MCP servers bring Grafana, Prometheus, Loki, Tempo, Kubernetes, PagerDuty, GitHub, databases, Terraform, Slack, and internal platforms into the same governed tool inventory.

### 7. Observability and fleet context

<p align="center">
  <img src="docs/assets/readme/live/en-monitor.png" alt="Ongrid monitoring dashboard" width="920" />
</p>

Operators can inspect fleet CPU, memory, disk, network, logs, traces, and alert state in the same workspace where the agent performs evidence collection.

### 8. Topology and blast-radius mapping

<p align="center">
  <img src="docs/assets/readme/live/en-topology-map.png" alt="Ongrid topology graph" width="920" />
</p>

Topology connects apps, services, clusters, devices, and failure domains. RCA workflows can explain affected systems and dependency paths instead of treating alerts as isolated metrics.

### 9. Knowledge base and source context

<p align="center">
  <img src="docs/assets/readme/live/en-knowledge-vault.png" alt="Ongrid knowledge base" width="920" />
</p>

Runbooks, architecture notes, built-in diagnostics, incident templates, uploaded docs, and repositories become searchable context that both operators and agents can inspect.

## Integrations

Drop-in for the observability, channel, and model stacks your team already uses.

| | |
|---|---|
| **Observability** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Channels** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Models** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## License

Apache 2.0 — see [LICENSE](LICENSE).
