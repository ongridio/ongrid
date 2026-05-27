# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> ongrid

> **给每台主机装上一个轻量 agent，然后用自然语言排障 —— 告警、日志、指标、链路、拓扑、源码，交给云端的 AIOps Agent 一起分析。**

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#技术栈)

[English](./README.md) | 简体中文 | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[项目简介](#项目简介) • [快速开始](#快速开始) • [架构](#架构) • [技术栈](#技术栈) • [贡献](#贡献)

---

## 项目简介

ongrid 是一个开源、可私有化的 AIOps 平台。在每台主机上装一个轻量的 `ongrid-edge` agent，它通过一条多路复用的**出站**隧道把指标、日志、链路上报到云端 —— 主机侧不开任何入站端口。云端是一个 LLM 驱动的运维 Agent：你用自然语言提问，它自己去查 PromQL / LogQL / TraceQL、走业务拓扑、检索知识库、读源码、调用主机只读巡检工具，给出带证据链的回答。

它主要解决：

- **排障门槛高** —— 用自然语言描述现象（"这台机器为什么 load 飙了？"、"谁在丢包？"），Agent 自己决定看哪个指标、查哪段日志、跑哪条查询。
- **告警与根因脱节** —— 告警触发后，Agent 顺着拓扑做爆炸半径分析、关联日志/链路，并定位到**源码位置**，把"告了什么"接到"为什么"。
- **数据散落** —— 指标（Prometheus）、日志（Loki）、链路（Tempo）、知识库（向量检索）、源码仓库统一接入，一个会话里联合分析。
- **不暴露内网** —— edge 主动外拨，主机侧零入站端口；遥测数据面与控制面分离。
- **可私有化** —— 一键 `docker compose` 起一整套；模型可对接任意 OpenAI 兼容 endpoint。

## 快速开始

```bash
# 1. 配置：设置管理员账号 + 一个模型 API key
cp deploy/.env.example deploy/.env

# 2. 起一整套（mysql / ongrid / frontier / nginx / prometheus / grafana）
make compose-up      # make compose-down 停止
```

打开 `https://<主机>`，用 `.env` 里种子化的管理员账号登录。生产发布包（TLS、systemd、升级/卸载）见 [`deploy/install/`](deploy/install/README.md)。

**在主机上装 edge** —— 在控制台创建 edge，复制对应平台的一行安装命令在目标机上执行即可。edge 主动外拨、不监听任何入站端口。

> 从源码构建：`make build`（云端以 `CGO_ENABLED=1` 构建，内嵌本地 ONNX embedder）、`cd web && npm ci && npm run build`。离线 RAG 需先 `make fetch-embedding-model` 拉 BGE 模型。`make help` 列出全部 target。

## 架构

```
  主机 ─┐
        │  ongrid-edge（每台一个）
        │  · 采集 metrics / logs / traces
        │  · 暴露只读主机巡检工具
        ▼
   ┌───────── 出站多路复用隧道 ─────────┐
   ▼                                    ▼
ongrid（云端）
  ├─ manager     边端管理 + 遥测接入 + AIOps Agent
  │   └─ 协调者 Agent ─分派─► 专家子 Agent + 工具
  │        PromQL · LogQL · TraceQL · 拓扑 · RAG · 源码阅读 · 主机只读工具
  ├─ 遥测栈      Prometheus · Loki · Tempo · Grafana
  ├─ 知识库      向量检索（内置 playbook + 组织文档）· 离线 ONNX embedder
  └─ web UI      对话 + 仪表盘
```

- **edge（`ongrid-edge`）** —— 每台主机一个，纯 Go 单二进制；采集遥测并通过隧道暴露只读巡检工具。主动外拨，零入站端口。
- **cloud（`ongrid`）** —— manager + LLM 协调者，把问题分派给专家子 Agent 和工具（PromQL / LogQL / TraceQL / 拓扑 / 知识库检索 / 源码阅读）并联合给出结论。
- **web** —— React SPA：对话式排障 + 仪表盘。

## 技术栈

| 层 | 选型 |
|---|---|
| 云端 | Go · [eino](https://github.com/cloudwego/eino) Agent 框架 · GORM · [geminio](https://github.com/singchia/geminio) 隧道 · 本地 ONNX embedder |
| 边端 | Go —— 纯 Go 单二进制，跨平台（Linux / macOS / Windows，x86_64 & ARM64） |
| 前端 | TypeScript · React（English / 简体中文） |
| 遥测 / 存储 | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| 模型 | 任意 OpenAI 兼容 endpoint —— OpenAI · Anthropic · Gemini · DeepSeek · 智谱 · Kimi · Ollama / vLLM / OpenRouter · … |

## 贡献

欢迎 issue 和 PR。提交前请确保 `make build`、`make test`、`make arch-lint`（校验限界上下文边界）都通过。

## 许可证

[Apache-2.0](LICENSE)。
