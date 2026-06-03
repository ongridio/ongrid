# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **Une IA d'ops qui comprend, trouve la cause et corrige.** *Monitoring, exécution distante, base de connaissances, agents spécialistes, Bash, fichiers et autres skills —— donnez les ordres directement depuis Slack, Telegram ou Lark.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | Français | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[Fonctionnalités](#fonctionnalités) • [Installation](#installation) • [Intégrations](#intégrations) • [Licence](#licence)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.168/Area2_hq.mp4">▶ Voir la démo complète en HD (MP4, 18 MB)</a></sub></p>

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

Téléchargez la dernière release, décompressez-la et exécutez le script d’installation (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9) :

```bash
# 1. Téléchargez la dernière release (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9)
wget https://github.com/ongridio/ongrid/releases/download/v0.7.168/ongrid-v0.7.168-linux-amd64.tar.xz

# 2. Décompresser
tar -xf ongrid-v0.7.168-linux-amd64.tar.xz && cd ongrid-v0.7.168-linux-amd64

# 3. Installer
sudo ./install.sh
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
