# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **Ein Ops-KI-Agent, der deine Infrastruktur versteht, die Ursache findet und sie behebt — direkt aus Slack oder Telegram.**

*Metriken · Logs · Traces · Topologie-Auswirkungsbereich · Ursachenkorrelation · Remote-Ausführung · alarmgesteuerte Auto-Untersuchung · RAG-Suche über Wissen und Code · Spezialisten-Agenten und Skills.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | Deutsch | [Português](./README_PT.md) | [Русский](./README_RU.md)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.169/Area2_hq.mp4">▶ Vollständige Demo in HD ansehen (MP4, 18 MB)</a></sub></p>

<div align="center">

[Funktionen](#funktionen) • [Installation](#installation) • [Integrationen](#integrationen) • [Lizenz](#lizenz)

</div>

## Funktionen

- 🤖 **Coordinator + Specialist Agenten** — der Coordinator delegiert an SRE / Netzwerk / DB / Asset Sub-Agenten
- 🚨 **Auto-Investigation bei Alarm** — der Investigator startet einen RCA-Worker, schreibt die Ursache in den Chat
- 🔍 **Grundursachen-RCA** — durchläuft die Topologie, korreliert Metriken/Logs/Traces, identifiziert eine Quellcode-Zeile
- 🔒 **Null eingehende Ports** — der Edge wählt nach außen; kein Port 22 / 80 / 443 auf Hosts
- 💻 **SSH im Browser** — Shell über Rückwärtstunnel, keine Schlüssel, kein Jumpbox, alles auditiert
- 🐳 **Selbst-Hosting in einem Befehl** — `docker compose up` startet die gesamte Stack
- 📊 **Eingebaute Observability** — Prometheus + Loki + Tempo + Grafana bereit, der Agent schreibt die Queries
- 🧠 **Eigenes Modell mitbringen** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, Hot-Routing
- 💬 **Zweiwege-IM-Kanäle** — Slack / Telegram / Larksuite / DingTalk / WeCom, Sprache pro Kanal
- 🛠️ **Schreibgeschützte Host-Tools** — bash Sandbox + 26+ Tools, jeder Aufruf auditiert

## Installation

Laden Sie das aktuelle Release herunter, entpacken Sie es und führen Sie das Installationsskript aus (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

```bash
# 1. Aktuelles Release herunterladen (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9)
wget https://github.com/ongridio/ongrid/releases/download/v0.8.3/ongrid-v0.8.3-linux-amd64.tar.xz

# 2. Entpacken
tar -xf ongrid-v0.8.3-linux-amd64.tar.xz && cd ongrid-v0.8.3-linux-amd64

# 3. Installieren
sudo ./install.sh
```

**🇨🇳 Festlandchina** — Wenn GitHub langsam ist, laden Sie Schritt 1 stattdessen vom CDN-Mirror (alles andere ist identisch):

```bash
wget https://ongrid.cloud/dl/ongrid-v0.8.3-linux-amd64.tar.xz
```

### Oder aus dem Quellcode ausführen

Lokale Entwicklung: Admin-Konto und einen Modell-API-Key konfigurieren, dann die gesamte Stack starten.

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## Integrationen

Drop-in für die Observability-, Channel- und Modell-Stacks, die Ihr Team bereits nutzt.

| | |
|---|---|
| **Observability** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Kanäle** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Modelle** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## Lizenz

Apache 2.0 — siehe [LICENSE](LICENSE).
