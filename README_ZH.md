# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **懂你的系统和基础设施、查得出根因、还能动手修复的 AI Agent，直接在飞书和钉钉里指挥。**

*指标 · 日志 · 链路 · 拓扑影响面 · 根因关联 · 远程执行 · 告警自动排查 · 知识库与代码 RAG 检索 · 专家 Agent 与技能。*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | 简体中文 | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>

<div align="center">

[特性](#特性) • [安装](#安装) • [集成](#集成) • [许可证](#许可证)

</div>

## 特性

- 🤖 **Coordinator + Specialist 双层 Agent** — coordinator 派活给 SRE / 网络 / DB 子 agent
- 🚨 **告警触发自动调查** — investigator 派 RCA worker, 根因 + 证据回填到聊天
- 🔍 **根因 RCA** — 沿拓扑做爆炸半径分析, 跨指标/日志/链路相关到源码行
- 🔒 **零入站端口** — edge 主动外联, host 不开 22 / 80 / 443
- 💻 **浏览器 SSH** — 反向隧道开交互式 shell, 不用 key / 跳板机, 全程审计
- 🐳 **一行命令自托管** — `install.sh` 起整套栈
- 📊 **可观测全栈内置** — Prometheus + Loki + Tempo + Grafana 自动起, Agent 自动写 query
- 🧠 **自带任意模型** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, 热路由
- 💬 **双向 IM 通道** — Slack / Telegram / Larksuite / DingTalk / WeCom, 按通道语言
- 🛠️ **只读主机巡检工具** — bash 沙箱 + 26+ 工具, 每次调用全审计

## 安装

按服务器架构下载最新 release（`linux-amd64` 或 `linux-arm64`），解压后运行安装脚本（Ubuntu 22.04+、Debian 12+、RHEL/Rocky 9）：

按服务器架构选择对应命令：

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

**🇨🇳 中国大陆用户** — GitHub 较慢时，选择对应架构的 CDN 镜像地址下载：

```bash
# AMD64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-amd64.tar.xz

# ARM64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-arm64.tar.xz
```

## 产品导览

### 根因分析

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-agent-write-gate.png" alt="根因分析" width="100%" />
</p>

从告警或运维问题出发，汇总拓扑、设备、指标、日志和变更上下文，形成带证据的根因分析与下一步建议。

### 工作流编排

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-workflow-editor.png" alt="工作流编排" width="100%" />
</p>

把触发器、Agent、工具、条件和通知节点串成可复用的自动化流程，并保持可编辑、可审查。

### 技能目录

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-skills-catalog.png" alt="技能目录" width="100%" />
</p>

展示 Agent 可调用的工具、描述和边界，让运维人员能清楚看到自动化能力面。

### MCP 服务器

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-mcp-servers.png" alt="MCP 服务器" width="100%" />
</p>

注册外部 MCP 服务器，把 Grafana、Kubernetes、PagerDuty、GitHub 或内部平台工具接入同一套治理清单。

### 知识库

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-knowledge-vault.png" alt="知识库" width="100%" />
</p>

索引 Runbook、事故记录、架构笔记和代码仓库，让人和 Agent 使用同一份上下文。

### 产物中心

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-artifacts-pages.png" alt="产物中心" width="100%" />
</p>

集中保存 Agent 和工作流生成的页面与报告，默认私有，便于审阅和交接。

### 监控

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-monitor.png" alt="监控" width="100%" />
</p>

在同一个工作台里查看主机健康、日志、链路和告警状态，为 RCA 收集证据。

### 拓扑图

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-topology-map.png" alt="拓扑图" width="100%" />
</p>

可视化服务、集群、设备与故障域之间的依赖关系，用于判断影响面。

### 审批与写入闸门

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-rca-session.png" alt="审批与写入闸门" width="100%" />
</p>

高风险动作先进入审批和策略边界，避免 Agent 直接修改生产系统。

## 文档

完整产品文档见 [ongrid.cloud](https://ongrid.cloud/docs/get-started/introduction)。

| 领域 | 从这里开始 |
|---|---|
| **快速开始** | [介绍](https://ongrid.cloud/docs/get-started/introduction) · [Quickstart](https://ongrid.cloud/docs/get-started/quickstart) · [架构](https://ongrid.cloud/docs/get-started/architecture) · [概念](https://ongrid.cloud/docs/get-started/concepts) |
| **安装与运维** | [服务端安装](https://ongrid.cloud/docs/install/server) · [边缘安装](https://ongrid.cloud/docs/install/edge) · [首次启动](https://ongrid.cloud/docs/install/first-boot) · [升级](https://ongrid.cloud/docs/install/upgrade) |
| **能力** | [告警](https://ongrid.cloud/docs/capabilities/alerts) · [RCA](https://ongrid.cloud/docs/capabilities/rca) · [监控](https://ongrid.cloud/docs/capabilities/monitoring) · [日志](https://ongrid.cloud/docs/capabilities/logs) · [链路](https://ongrid.cloud/docs/capabilities/traces) · [知识库](https://ongrid.cloud/docs/capabilities/knowledge) · [技能](https://ongrid.cloud/docs/capabilities/skills) |
| **Agent** | [概览](https://ongrid.cloud/docs/agents/overview) · [Coordinator](https://ongrid.cloud/docs/agents/coordinator) · [Incident investigator](https://ongrid.cloud/docs/agents/incident-investigator) · [Specialists](https://ongrid.cloud/docs/agents/specialists) · [Reviewer](https://ongrid.cloud/docs/agents/reviewer) |
| **参考** | [API](https://ongrid.cloud/docs/reference/api) · [CLI](https://ongrid.cloud/docs/reference/cli) · [告警规则](https://ongrid.cloud/docs/reference/alert-rules) · [Skill manifest](https://ongrid.cloud/docs/reference/skill-manifest) · [Data plane](https://ongrid.cloud/docs/reference/data-plane) |

## 集成

即插即用，对接团队现有的可观测、IM 通道与模型栈。

| | |
|---|---|
| **可观测** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **通道** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **模型** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## 许可证

Apache 2.0 — 见 [LICENSE](LICENSE)。

## Star 趋势

<a href="https://www.star-history.com/#ongridio/ongrid&amp;Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date&amp;theme=dark" />
    <img alt="Star 趋势图" src="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date" />
  </picture>
</a>

## 加入群组

<p align="center">
  <img src="docs/assets/community/wechat-dev-community.jpg" alt="Ongrid 微信群二维码" width="200" />
</p>

扫码加入 Ongrid 开发者社区，交流部署、AIOps 场景、工作流和插件扩展。
