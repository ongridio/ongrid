# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **Uma IA de ops que entende, encontra a causa e corrige.** *Monitoramento, exec remoto, conhecimento, agentes, skills —— a um comando. Vive no Slack, Telegram ou Lark.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | Português | [Русский](./README_RU.md)

[Recursos](#recursos) • [Instalação](#instalação) • [Integrações](#integrações) • [Licença](#licença)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.168/Area2_hq.mp4">▶ Ver demo completa em HD (MP4, 18 MB)</a></sub></p>

## Recursos

- 🤖 **Agentes Coordinator + Specialist** — o coordinator delega para sub-agentes SRE / rede / DB / ativos
- 🚨 **Auto-investigação no alerta** — o investigator lança um RCA worker e escreve a causa no chat
- 🔍 **RCA de causa raiz** — percorre a topologia, correlaciona métricas/logs/traces, identifica uma linha de código
- 🔒 **Zero portas de entrada** — o edge disca para fora; nenhuma porta 22 / 80 / 443 em hosts
- 💻 **SSH no navegador** — shell por túnel reverso, sem chaves, sem jumpbox, tudo auditado
- 🐳 **Self-host em um comando** — `docker compose up` sobe toda a stack
- 📊 **Observabilidade integrada** — Prometheus + Loki + Tempo + Grafana prontos, o agente escreve as queries
- 🧠 **Traga seu próprio modelo** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, roteamento a quente
- 💬 **Canais IM bidirecionais** — Slack / Telegram / Larksuite / DingTalk / WeCom, idioma por canal
- 🛠️ **Ferramentas de host só-leitura** — sandbox bash + 26+ ferramentas, cada chamada auditada

## Instalação

Baixe a última release, descompacte e execute o instalador (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

```bash
# 1. Baixe a última release (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9)
wget https://github.com/ongridio/ongrid/releases/download/v0.7.168/ongrid-v0.7.168-linux-amd64.tar.xz

# 2. Descompactar
tar -xf ongrid-v0.7.168-linux-amd64.tar.xz && cd ongrid-v0.7.168-linux-amd64

# 3. Instalar
sudo ./install.sh
```

### Ou executar a partir do código

Desenvolvimento local: configure a conta admin e uma API key de modelo, depois suba todo o stack.

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## Integrações

Drop-in para os stacks de observabilidade, canais e modelos que sua equipe já usa.

| | |
|---|---|
| **Observabilidade** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Canais** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Modelos** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## Licença

Apache 2.0 — veja [LICENSE](LICENSE).
