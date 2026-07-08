# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **인프라를 이해하고, 근본 원인을 찾아내고, 직접 고치는 운영 AI 에이전트 — Slack과 Telegram에서 바로.**

*메트릭 · 로그 · 트레이스 · 토폴로지 영향 범위 · 근본 원인 상관 분석 · 원격 실행 · 알림 기반 자동 조사 · 지식·코드 RAG 검색 · 전문 에이전트와 스킬.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | 한국어 | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>

<div align="center">

[기능](#기능) • [설치](#설치) • [연동](#연동) • [라이선스](#라이선스)

</div>

## 기능

- 🤖 **Coordinator + Specialist 에이전트** — coordinator가 SRE / 네트워크 / DB 서브 에이전트로 라우팅
- 🚨 **알림 발생 시 자동 조사** — investigator가 RCA worker 파견, 근본 원인을 채팅에 기록
- 🔍 **근본 원인 RCA** — 토폴로지로 영향 범위 분석, 메트릭/로그/트레이스 상관, 소스 코드 라인까지
- 🔒 **인바운드 포트 0** — edge가 외부로 발신, 호스트는 22 / 80 / 443 미오픈
- 💻 **브라우저 SSH** — 역방향 터널로 대화형 셸, 키 / 점프 호스트 불필요, 모든 명령 감사
- 🐳 **한 줄로 셀프 호스팅** — `install.sh`으로 전체 스택 기동
- 📊 **가관측성 전체 스택 내장** — Prometheus + Loki + Tempo + Grafana 자동 배포, Agent가 쿼리 작성
- 🧠 **원하는 모델 사용** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, 핫 라우팅
- 💬 **양방향 IM 채널** — Slack / Telegram / Larksuite / DingTalk / WeCom, 채널별 로케일
- 🛠️ **읽기 전용 호스트 도구** — bash 샌드박스 + 26+ 도구, 모든 호출 감사

## 설치

서버 아키텍처(`linux-amd64` 또는 `linux-arm64`)에 맞는 최신 릴리스를 다운로드하고 압축을 푼 다음 설치 스크립트를 실행하세요 (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

서버 아키텍처에 맞는 명령을 선택하세요:

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

**🇨🇳 중국 본토 사용자** — GitHub이 느리면 아키텍처에 맞는 CDN 미러 URL을 사용하세요:

```bash
# AMD64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-amd64.tar.xz

# ARM64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-arm64.tar.xz
```

## 제품 둘러보기

### 근본 원인 분석

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-agent-write-gate.png" alt="근본 원인 분석" width="100%" />
</p>

알림이나 운영 질문에서 시작해 토폴로지, 장비, 메트릭, 로그, 변경 이력을 모아 근거 기반 분석을 만듭니다.

### 워크플로 빌더

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-workflow-editor.png" alt="워크플로 빌더" width="100%" />
</p>

트리거, Agent, 도구, 조건, 알림을 재사용 가능한 자동화로 구성합니다.

### 스킬 카탈로그

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-skills-catalog.png" alt="스킬 카탈로그" width="100%" />
</p>

Agent가 호출할 수 있는 도구와 설명을 운영자가 볼 수 있게 정리합니다.

### MCP 서버

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-mcp-servers.png" alt="MCP 서버" width="100%" />
</p>

외부 MCP 서버를 등록해 도구를 동일한 관리 인벤토리에 연결합니다.

### 지식 저장소

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-knowledge-vault.png" alt="지식 저장소" width="100%" />
</p>

런북, 사고 이력, 아키텍처 노트, 저장소를 검색 가능한 운영 컨텍스트로 만듭니다.

### 아티팩트 센터

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-artifacts-pages.png" alt="아티팩트 센터" width="100%" />
</p>

생성된 페이지와 보고서를 한곳에 보관해 검토와 인계에 사용합니다.

### 모니터링

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-monitor.png" alt="모니터링" width="100%" />
</p>

호스트 상태, 로그, 트레이스, 알림 상태를 같은 작업 공간에서 확인합니다.

### 토폴로지 맵

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-topology-map.png" alt="토폴로지 맵" width="100%" />
</p>

의존성과 영향 범위를 시각화해 장애 전파 경로를 추적합니다.

### 승인 및 쓰기 게이트

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-rca-session.png" alt="승인 및 쓰기 게이트" width="100%" />
</p>

위험한 작업은 승인과 정책 경계 안에서만 진행합니다.

## 문서

전체 제품 문서는 [ongrid.cloud](https://ongrid.cloud/docs/get-started/introduction) 에 있습니다.

| 영역 | 시작하기 |
|---|---|
| **시작하기** | [Introduction](https://ongrid.cloud/docs/get-started/introduction) · [Quickstart](https://ongrid.cloud/docs/get-started/quickstart) · [Architecture](https://ongrid.cloud/docs/get-started/architecture) · [Concepts](https://ongrid.cloud/docs/get-started/concepts) |
| **설치 및 운영** | [Server install](https://ongrid.cloud/docs/install/server) · [Edge install](https://ongrid.cloud/docs/install/edge) · [First boot](https://ongrid.cloud/docs/install/first-boot) · [Upgrade](https://ongrid.cloud/docs/install/upgrade) |
| **기능** | [Alerts](https://ongrid.cloud/docs/capabilities/alerts) · [RCA](https://ongrid.cloud/docs/capabilities/rca) · [Monitoring](https://ongrid.cloud/docs/capabilities/monitoring) · [Logs](https://ongrid.cloud/docs/capabilities/logs) · [Traces](https://ongrid.cloud/docs/capabilities/traces) · [Knowledge](https://ongrid.cloud/docs/capabilities/knowledge) · [Skills](https://ongrid.cloud/docs/capabilities/skills) |
| **Agent** | [Overview](https://ongrid.cloud/docs/agents/overview) · [Coordinator](https://ongrid.cloud/docs/agents/coordinator) · [Incident investigator](https://ongrid.cloud/docs/agents/incident-investigator) · [Specialists](https://ongrid.cloud/docs/agents/specialists) · [Reviewer](https://ongrid.cloud/docs/agents/reviewer) |
| **참조** | [API](https://ongrid.cloud/docs/reference/api) · [CLI](https://ongrid.cloud/docs/reference/cli) · [Alert rules](https://ongrid.cloud/docs/reference/alert-rules) · [Skill manifest](https://ongrid.cloud/docs/reference/skill-manifest) · [Data plane](https://ongrid.cloud/docs/reference/data-plane) |

## 연동

팀의 가관측성, 채널, 모델 스택에 그대로 연동됩니다.

| | |
|---|---|
| **가관측성** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **채널** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **모델** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## 라이선스

Apache 2.0 — [LICENSE](LICENSE) 참조.

## Star 기록

<a href="https://www.star-history.com/#ongridio/ongrid&amp;Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date&amp;theme=dark" />
    <img alt="Star 기록 차트" src="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date" />
  </picture>
</a>
