# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **Um agente de IA de ops que entende sua infraestrutura, encontra a causa raiz e a corrige — direto do Slack ou Telegram.**

*Métricas · logs · traces · raio de impacto da topologia · correlação de causa raiz · execução remota · investigação automática acionada por alertas · busca RAG em conhecimento e código · agentes especialistas e skills.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | Português | [Русский](./README_RU.md)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>

<div align="center">

[Recursos](#recursos) • [Instalação](#instalação) • [Integrações](#integrações) • [Licença](#licença)

</div>

## Recursos

- 🤖 **Agentes Coordinator + Specialist** — o coordinator delega para sub-agentes SRE / rede / DB / ativos
- 🚨 **Auto-investigação no alerta** — o investigator lança um RCA worker e escreve a causa no chat
- 🔍 **RCA de causa raiz** — percorre a topologia, correlaciona métricas/logs/traces, identifica uma linha de código
- 🔒 **Zero portas de entrada** — o edge disca para fora; nenhuma porta 22 / 80 / 443 em hosts
- 💻 **SSH no navegador** — shell por túnel reverso, sem chaves, sem jumpbox, tudo auditado
- 🐳 **Self-host em um comando** — `install.sh` sobe toda a stack
- 📊 **Observabilidade integrada** — Prometheus + Loki + Tempo + Grafana prontos, o agente escreve as queries
- 🧠 **Traga seu próprio modelo** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, roteamento a quente
- 💬 **Canais IM bidirecionais** — Slack / Telegram / Larksuite / DingTalk / WeCom, idioma por canal
- 🛠️ **Ferramentas de host só-leitura** — sandbox bash + 26+ ferramentas, cada chamada auditada

## Instalação

Baixe a última release para a arquitetura do seu servidor (`linux-amd64` ou `linux-arm64`), descompacte e execute o instalador (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

Escolha o comando para a arquitetura do seu servidor:

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

**🇨🇳 China continental** — se o GitHub estiver lento, use a URL do mirror CDN correspondente à sua arquitetura:

```bash
# AMD64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-amd64.tar.xz

# ARM64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-arm64.tar.xz
```

## Tour do produto

### Análise de causa raiz

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-agent-write-gate.png" alt="Análise de causa raiz" width="100%" />
</p>

Comece por um alerta ou pergunta operacional e reúna topologia, dispositivos, métricas, logs e mudanças para uma análise com evidências.

### Construtor de workflows

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-workflow-editor.png" alt="Construtor de workflows" width="100%" />
</p>

Conecte gatilhos, agentes, ferramentas, condições e notificações em automações reutilizáveis.

### Catálogo de skills

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-skills-catalog.png" alt="Catálogo de skills" width="100%" />
</p>

Mostre as ferramentas que os agentes podem chamar, com descrições e limites claros.

### Servidores MCP

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-mcp-servers.png" alt="Servidores MCP" width="100%" />
</p>

Registre servidores MCP externos e exponha suas ferramentas no mesmo inventário governado.

### Base de conhecimento

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-knowledge-vault.png" alt="Base de conhecimento" width="100%" />
</p>

Indexe runbooks, histórico de incidentes, notas de arquitetura e repositórios como contexto pesquisável.

### Central de artefatos

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-artifacts-pages.png" alt="Central de artefatos" width="100%" />
</p>

Guarde páginas e relatórios gerados em um centro privado por padrão para revisão e handoff.

### Monitoramento

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-monitor.png" alt="Monitoramento" width="100%" />
</p>

Inspecione saúde da frota, logs, traces e alertas no mesmo workspace.

### Mapa de topologia

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-topology-map.png" alt="Mapa de topologia" width="100%" />
</p>

Visualize dependências e raio de impacto para rastrear incidentes.

### Aprovação e gate de escrita

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-rca-session.png" alt="Aprovação e gate de escrita" width="100%" />
</p>

Mantenha ações arriscadas atrás de aprovações e políticas visíveis.

## Documentação

A documentação completa está disponível em [ongrid.cloud](https://ongrid.cloud/docs/get-started/introduction).

| Área | Comece aqui |
|---|---|
| **Primeiros passos** | [Introduction](https://ongrid.cloud/docs/get-started/introduction) · [Quickstart](https://ongrid.cloud/docs/get-started/quickstart) · [Architecture](https://ongrid.cloud/docs/get-started/architecture) · [Concepts](https://ongrid.cloud/docs/get-started/concepts) |
| **Instalação e operação** | [Server install](https://ongrid.cloud/docs/install/server) · [Edge install](https://ongrid.cloud/docs/install/edge) · [First boot](https://ongrid.cloud/docs/install/first-boot) · [Upgrade](https://ongrid.cloud/docs/install/upgrade) |
| **Capacidades** | [Alerts](https://ongrid.cloud/docs/capabilities/alerts) · [RCA](https://ongrid.cloud/docs/capabilities/rca) · [Monitoring](https://ongrid.cloud/docs/capabilities/monitoring) · [Logs](https://ongrid.cloud/docs/capabilities/logs) · [Traces](https://ongrid.cloud/docs/capabilities/traces) · [Knowledge](https://ongrid.cloud/docs/capabilities/knowledge) · [Skills](https://ongrid.cloud/docs/capabilities/skills) |
| **Agentes** | [Overview](https://ongrid.cloud/docs/agents/overview) · [Coordinator](https://ongrid.cloud/docs/agents/coordinator) · [Incident investigator](https://ongrid.cloud/docs/agents/incident-investigator) · [Specialists](https://ongrid.cloud/docs/agents/specialists) · [Reviewer](https://ongrid.cloud/docs/agents/reviewer) |
| **Referência** | [API](https://ongrid.cloud/docs/reference/api) · [CLI](https://ongrid.cloud/docs/reference/cli) · [Alert rules](https://ongrid.cloud/docs/reference/alert-rules) · [Skill manifest](https://ongrid.cloud/docs/reference/skill-manifest) · [Data plane](https://ongrid.cloud/docs/reference/data-plane) |

## Integrações

Drop-in para os stacks de observabilidade, canais e modelos que sua equipe já usa.

| | |
|---|---|
| **Observabilidade** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Canais** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Modelos** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## Licença

Apache 2.0 — veja [LICENSE](LICENSE).

## Histórico de estrelas

<a href="https://www.star-history.com/#ongridio/ongrid&amp;Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date&amp;theme=dark" />
    <img alt="Gráfico do histórico de estrelas" src="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date" />
  </picture>
</a>
