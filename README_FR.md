# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **Un agent IA d'ops qui comprend votre infrastructure, trouve la cause racine et la corrige — directement depuis Slack ou Telegram.**

*Métriques · logs · traces · rayon d'impact de topologie · corrélation de cause racine · exécution à distance · investigation automatique déclenchée par alerte · recherche RAG dans les connaissances et le code · agents spécialisés et compétences.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | Français | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.169/Area2_hq.mp4">▶ Voir la démo complète en HD (MP4, 18 MB)</a></sub></p>

<div align="center">

[Fonctionnalités](#fonctionnalités) • [Installation](#installation) • [Intégrations](#intégrations) • [Licence](#licence)

</div>

## Fonctionnalités

- 🤖 **Agents Coordinator + Specialist** — le coordinator délègue aux sous-agents SRE / réseau / DB / actifs
- 🚨 **Auto-investigation sur alerte** — l’investigator lance un RCA worker et écrit la cause au chat
- 🔍 **RCA cause racine** — parcourt la topologie, corrèle métriques/logs/traces, identifie une ligne de code
- 🔒 **Zéro port entrant** — l’edge sort vers l’extérieur ; aucun port 22 / 80 / 443 sur l’hôte
- 💻 **SSH dans le navigateur** — shell par tunnel inverse, pas de clé, pas de jumpbox, tout audité
- 🐳 **Auto-hébergeable en une commande** — `docker compose up` lance toute la stack
- 📊 **Observabilité intégrée** — Prometheus + Loki + Tempo + Grafana prêts, l’agent écrit les requêtes
- 🧠 **Apportez votre modèle** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, routage à chaud
- 💬 **Canaux IM bidirectionnels** — Slack / Telegram / Larksuite / DingTalk / WeCom, langue par canal
- 🛠️ **Outils host en lecture seule** — sandbox bash + 26+ outils, chaque appel audité

## Installation

Téléchargez la dernière release adaptée à l’architecture de votre serveur (`linux-amd64` ou `linux-arm64`), décompressez-la et exécutez le script d’installation (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9) :

Choisissez la commande adaptée à l’architecture de votre serveur :

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

**🇨🇳 Chine continentale** — si GitHub est lent, utilisez l’URL du miroir CDN correspondant à votre architecture :

```bash
# AMD64
wget https://ongrid.cloud/dl/ongrid-v0.8.4-linux-amd64.tar.xz

# ARM64
wget https://ongrid.cloud/dl/ongrid-v0.8.4-linux-arm64.tar.xz
```

### Ou exécuter depuis les sources

Dev local : configurez le compte admin et une clé API de modèle, puis lancez la stack complète.

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## Intégrations

S’intègre aux stacks d’observabilité, de canaux et de modèles déjà en place.

| | |
|---|---|
| **Observabilité** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Canaux** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Modèles** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## Licence

Apache 2.0 — voir [LICENSE](LICENSE).
