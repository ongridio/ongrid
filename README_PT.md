# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> ongrid

> **Coloque um agente leve em cada host e diagnostique em linguagem natural —— alertas, logs, métricas, traces, topologia e código-fonte, analisados em conjunto por um agente de AIOps na nuvem.**

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#stack-tecnológica)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | Português | [Русский](./README_RU.md)

[Visão geral](#visão-geral) • [Início rápido](#início-rápido) • [Arquitetura](#arquitetura) • [Stack tecnológica](#stack-tecnológica) • [Contribuir](#contribuir)

---

## Visão geral

ongrid é uma plataforma de AIOps open source e auto-hospedável. Um agente leve `ongrid-edge` em cada host envia métricas, logs e traces para a nuvem por um único túnel **de saída** multiplexado —— sem portas de entrada no host. A nuvem é um agente de operações orientado por LLM: você pergunta em linguagem natural e ele mesmo executa PromQL / LogQL / TraceQL, percorre a topologia de serviços, busca na base de conhecimento, lê o código-fonte e chama ferramentas de host somente leitura para dar uma resposta fundamentada.

O que resolve:

- **Barreira alta de diagnóstico** —— descreva o sintoma ("por que a carga está disparando?", "quem está perdendo pacotes?"); o agente decide qual métrica, quais logs e qual consulta olhar.
- **Alertas desconectados da causa raiz** —— em um alerta, ele percorre a topologia para o raio de impacto, correlaciona logs/traces e localiza a **posição no código-fonte** por trás do "porquê".
- **Sinais dispersos** —— métricas (Prometheus), logs (Loki), traces (Tempo), base de conhecimento (busca vetorial) e repositórios de código são unificados e analisados em uma única sessão.
- **Sem expor a intranet** —— o edge disca para fora; zero portas de entrada nos hosts; o plano de dados de telemetria é separado do plano de controle.
- **Auto-hospedável** —— um único `docker compose` sobe a stack completa; aponte o modelo para qualquer endpoint compatível com OpenAI.

## Início rápido

```bash
# 1. configurar: defina a conta de admin + uma API key de modelo
cp deploy/.env.example deploy/.env

# 2. subir a stack completa (mysql / ongrid / frontier / nginx / prometheus / grafana)
make compose-up      # make compose-down para parar
```

Abra `https://<host>` e entre com a conta de admin semeada a partir de `.env`. Para um pacote de release de produção (TLS, systemd, upgrade/desinstalação) veja [`deploy/install/`](deploy/install/README.md).

**Instalar o edge em um host** —— crie o edge no console, copie o comando de instalação de uma linha para a plataforma de destino e execute-o. O edge disca para fora e não escuta em nenhuma porta de entrada.

> Compilar do código: `make build` (a nuvem usa `CGO_ENABLED=1` por causa do embedder ONNX local) e `cd web && npm ci && npm run build`. Rode `make help` para todos os targets.

## Arquitetura

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

- **edge (`ongrid-edge`)** —— um por host, binário único em Go puro; coleta telemetria e expõe ferramentas de inspeção somente leitura pelo túnel. Apenas saída, zero portas de entrada.
- **cloud (`ongrid`)** —— manager + coordenador LLM que distribui para subagentes e ferramentas especializados (PromQL / LogQL / TraceQL / topologia / busca de conhecimento / leitura de código) e sintetiza a resposta.
- **web** —— SPA em React: diagnóstico conversacional + dashboards.

## Stack tecnológica

| Camada | Escolha |
|---|---|
| Nuvem | Go · framework de agentes [eino](https://github.com/cloudwego/eino) · GORM · túnel [geminio](https://github.com/singchia/geminio) · embedder ONNX local |
| Edge | Go —— binário único em Go puro, multiplataforma (Linux / macOS / Windows, x86_64 & ARM64) |
| Frontend | TypeScript · React (English / 简体中文) |
| Telemetria / armazenamento | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| Modelo | qualquer endpoint compatível com OpenAI —— OpenAI · Anthropic · Gemini · DeepSeek · Zhipu · Kimi · Ollama / vLLM / OpenRouter · … |

## Contribuir

Issues e PRs são bem-vindos. Antes de enviar, garanta que `make build`, `make test` e `make arch-lint` (que valida os limites de contexto delimitado) passem.

## Licença

[Apache-2.0](LICENSE).
