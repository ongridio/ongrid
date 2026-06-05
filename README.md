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

## Integrations

Drop-in for the observability, channel, and model stacks your team already uses.

| | |
|---|---|
| **Observability** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Channels** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Models** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## License

Apache 2.0 — see [LICENSE](LICENSE).
