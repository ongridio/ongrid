# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **Un agente de IA de ops que entiende tu infraestructura, encuentra la causa raíz y la soluciona — directamente desde Slack o Telegram.**

*Métricas · registros · trazas · radio de impacto de topología · correlación de causa raíz · ejecución remota · investigación automática por alertas · búsqueda RAG en conocimiento y código · agentes especialistas y skills.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | Español | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.169/Area2_hq.mp4">▶ Ver demo completa en HD (MP4, 18 MB)</a></sub></p>

<div align="center">

[Características](#características) • [Instalación](#instalación) • [Integraciones](#integraciones) • [Licencia](#licencia)

</div>

## Características

- 🤖 **Agentes Coordinator + Specialist** — el coordinator delega a sub-agentes SRE / red / DB / activos
- 🚨 **Auto-investigación en alerta** — el investigator lanza un RCA worker y escribe la causa al chat
- 🔍 **RCA de causa raíz** — recorre la topología, correlaciona métricas/logs/trazas, llega a una línea de código
- 🔒 **Cero puertos entrantes** — el edge sale al exterior; ningún puerto 22 / 80 / 443 en hosts
- 💻 **SSH en el navegador** — shell por túnel inverso, sin claves, sin jumpbox, todo auditado
- 🐳 **Self-host en un comando** — `docker compose up` levanta toda la stack
- 📊 **Observabilidad integrada** — Prometheus + Loki + Tempo + Grafana listos, el agente escribe las queries
- 🧠 **Trae tu propio modelo** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, enrutamiento en caliente
- 💬 **Canales IM bidireccionales** — Slack / Telegram / Larksuite / DingTalk / WeCom, idioma por canal
- 🛠️ **Herramientas de host solo-lectura** — sandbox bash + 26+ herramientas, cada llamada auditada

## Instalación

Descarga la última release para la arquitectura de tu servidor (`linux-amd64` o `linux-arm64`), descomprímela y ejecuta el instalador (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

Elige el comando para la arquitectura de tu servidor:

**AMD64**
```bash
wget https://github.com/ongridio/ongrid/releases/download/v0.8.4/ongrid-v0.8.4-linux-amd64.tar.xz
tar -xf ongrid-v0.8.4-linux-amd64.tar.xz && cd ongrid-v0.8.4-linux-amd64
sudo ./install.sh
```

**ARM64**
```bash
wget https://github.com/ongridio/ongrid/releases/download/v0.8.4/ongrid-v0.8.4-linux-arm64.tar.xz
tar -xf ongrid-v0.8.4-linux-arm64.tar.xz && cd ongrid-v0.8.4-linux-arm64
sudo ./install.sh
```

**🇨🇳 China continental** — si GitHub va lento, usa la URL del mirror CDN que coincida con tu arquitectura:

```bash
# AMD64
wget https://ongrid.cloud/dl/ongrid-v0.8.4-linux-amd64.tar.xz

# ARM64
wget https://ongrid.cloud/dl/ongrid-v0.8.4-linux-arm64.tar.xz
```

### O ejecutar desde el código fuente

Desarrollo local: configura la cuenta de admin y una API key de modelo, y levanta todo el stack.

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## Integraciones

Se integra con los stacks de observabilidad, canales y modelos que tu equipo ya usa.

| | |
|---|---|
| **Observabilidad** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Canales** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Modelos** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## Licencia

Apache 2.0 — ver [LICENSE](LICENSE).
