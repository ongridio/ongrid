# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> ongrid

> **운영을 위한 AI 에이전트.** 모든 호스트에 경량 에이전트를 설치하면 ongrid가 메트릭·로그·트레이스·토폴로지·소스 코드를 종합 분석해 자연어로 근본 원인을 짚어냅니다.
>
> *SRE, 플랫폼, 온콜 팀을 위해 만들었습니다.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#기술-스택)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | 한국어 | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[개요](#개요) • [빠른 시작](#빠른-시작) • [아키텍처](#아키텍처) • [기술 스택](#기술-스택) • [기여](#기여)

---

## 개요

ongrid 은 오픈소스이며 셀프 호스팅 가능한, 운영을 위한 AI 에이전트입니다. 각 호스트에 경량 `ongrid-edge` 에이전트를 설치하면, 단일 다중화 **아웃바운드** 터널을 통해 메트릭·로그·트레이스를 클라우드로 전송합니다 —— 호스트에 인바운드 포트가 전혀 필요 없습니다. 클라우드는 LLM 기반 운영 에이전트로, 자연어로 질문하면 스스로 PromQL / LogQL / TraceQL 을 실행하고, 서비스 토폴로지를 탐색하고, 지식 베이스를 검색하고, 소스 코드를 읽고, 읽기 전용 호스트 도구를 호출하여 근거 있는 답변을 제공합니다.

해결하는 문제:

- **높은 트러블슈팅 진입 장벽** —— 증상만 자연어로 설명하면 됩니다("왜 load 가 치솟나요?", "누가 패킷을 떨어뜨리나요?"). 어떤 메트릭·로그·쿼리를 볼지는 에이전트가 판단합니다.
- **알림과 근본 원인의 단절** —— 알림 발생 시 토폴로지를 따라 영향 범위(blast radius)를 분석하고, 로그/트레이스를 상관시키며, "왜"를 설명하는 **소스 코드 위치**까지 짚어냅니다.
- **분산된 신호** —— 메트릭(Prometheus)·로그(Loki)·트레이스(Tempo)·지식 베이스(벡터 검색)·소스 저장소를 통합하여 하나의 세션에서 함께 분석합니다.
- **내부망을 노출하지 않음** —— edge 는 아웃바운드로만 연결하며 호스트의 인바운드 포트는 0 입니다. 텔레메트리 데이터 플레인은 컨트롤 플레인과 분리됩니다.
- **셀프 호스팅 가능** —— `docker compose` 한 번으로 전체 스택이 기동됩니다. 모델은 OpenAI 호환 엔드포인트라면 무엇이든 연결할 수 있습니다.

## 빠른 시작

```bash
# 1. 설정: 관리자 계정 + 모델 API 키 설정
cp deploy/.env.example deploy/.env

# 2. 전체 스택 기동 (mysql / ongrid / frontier / nginx / prometheus / grafana)
make compose-up      # 중지는 make compose-down
```

`https://<host>` 를 열고 `.env` 에서 시드된 관리자 계정으로 로그인합니다. 프로덕션 릴리스 패키지(TLS·systemd·업그레이드/제거)는 [`deploy/install/`](deploy/install/README.md) 를 참고하세요.

**호스트에 edge 설치** —— 콘솔에서 edge 를 생성하고, 대상 플랫폼용 한 줄 설치 명령을 복사해 실행합니다. edge 는 아웃바운드로만 연결하며 인바운드 포트를 수신 대기하지 않습니다.

> 소스에서 빌드: `make build`(클라우드는 로컬 ONNX embedder 때문에 `CGO_ENABLED=1`)와 `cd web && npm ci && npm run build`. 전체 타깃은 `make help` 참고.

## 아키텍처

```
  hosts ─┐
         │  ongrid-edge (one per host)
         │  · collects metrics / logs / traces
         │  · exposes read-only host inspection tools
         ▼
   ┌──────── outbound multiplexed tunnel ────────┐
   ▼                                              ▼
ongrid (cloud)
  ├─ manager     edge mgmt + telemetry ingest + AIOps agent
  │   └─ coordinator agent ─dispatch─► specialist sub-agents + tools
  │        PromQL · LogQL · TraceQL · topology · RAG · source reading · host tools
  ├─ telemetry   Prometheus · Loki · Tempo · Grafana
  ├─ knowledge   vector search (built-in playbooks + org docs) · offline ONNX embedder
  └─ web UI      chat + dashboards
```

- **edge (`ongrid-edge`)** —— 호스트당 하나, 순수 Go 단일 바이너리. 텔레메트리를 수집하고 터널을 통해 읽기 전용 점검 도구를 노출합니다. 아웃바운드 전용, 인바운드 포트 0.
- **cloud (`ongrid`)** —— manager + LLM 코디네이터. 전문 서브 에이전트와 도구(PromQL / LogQL / TraceQL / 토폴로지 / 지식 검색 / 소스 읽기)에 분배하고 답변을 종합합니다.
- **web** —— React SPA: 대화형 트러블슈팅 + 대시보드.

## 기술 스택

| 레이어 | 선택 |
|---|---|
| 클라우드 | Go · [eino](https://github.com/cloudwego/eino) 에이전트 프레임워크 · GORM · [geminio](https://github.com/singchia/geminio) 터널 · 로컬 ONNX embedder |
| 엣지 | Go —— 순수 Go 단일 바이너리, 크로스 플랫폼(Linux / macOS / Windows, x86_64 & ARM64) |
| 프런트엔드 | TypeScript · React(English / 简体中文) |
| 텔레메트리 / 스토리지 | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| 모델 | OpenAI 호환 엔드포인트라면 무엇이든 —— OpenAI · Anthropic · Gemini · DeepSeek · Zhipu · Kimi · Ollama / vLLM / OpenRouter · … |

## 기여

Issue 와 PR 을 환영합니다. 제출 전에 `make build`, `make test`, `make arch-lint`(바운디드 컨텍스트 경계 검증)가 모두 통과하는지 확인하세요.

## 라이선스

[Apache-2.0](LICENSE).
