# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **一个看得懂系统、查得出根因、还能动手解决的运维 AI。** *监控、远程执行、知识库、智能体、技能 —— 一句话就够了。装进 Slack、Telegram、飞书，团队在哪儿它就在哪儿。*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#)

[English](./README.md) | 简体中文 | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[特性](#特性) • [安装](#安装) • [集成](#集成) • [许可证](#许可证)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.168/Area2_hq.mp4">▶ 观看高清完整视频 (MP4, 18 MB)</a></sub></p>

## 特性

- 🤖 **Coordinator + Specialist 双层 Agent** — coordinator 派活给 SRE / 网络 / DB 子 agent
- 🚨 **告警触发自动调查** — investigator 派 RCA worker, 根因 + 证据回填到聊天
- 🔍 **根因 RCA** — 沿拓扑做爆炸半径分析, 跨指标/日志/链路相关到源码行
- 🔒 **零入站端口** — edge 主动外联, host 不开 22 / 80 / 443
- 💻 **浏览器 SSH** — 反向隧道开交互式 shell, 不用 key / 跳板机, 全程审计
- 🐳 **一行命令自托管** — `docker compose up` 起整套栈
- 📊 **可观测全栈内置** — Prometheus + Loki + Tempo + Grafana 自动起, Agent 自动写 query
- 🧠 **自带任意模型** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, 热路由
- 💬 **双向 IM 通道** — Slack / Telegram / Larksuite / DingTalk / WeCom, 按通道语言
- 🛠️ **只读主机巡检工具** — bash 沙箱 + 26+ 工具, 每次调用全审计

## 安装

下载最新 release，解压后运行安装脚本（Ubuntu 22.04+、Debian 12+、RHEL/Rocky 9）：

```bash
# 1. 下载最新 release（Ubuntu 22.04+、Debian 12+、RHEL/Rocky 9）
wget https://github.com/ongridio/ongrid/releases/download/v0.7.168/ongrid-v0.7.168-linux-amd64.tar.xz

# 2. 解压
tar -xf ongrid-v0.7.168-linux-amd64.tar.xz && cd ongrid-v0.7.168-linux-amd64

# 3. 安装
sudo ./install.sh
```

### 或从源码运行

本地开发：先配好管理员账号和一个模型 API key，再起整套栈。

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## 集成

即插即用，对接团队现有的可观测、IM 通道与模型栈。

| | |
|---|---|
| **可观测** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **通道** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **模型** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## 许可证

Apache 2.0 — 见 [LICENSE](LICENSE)。
