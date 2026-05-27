# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> ongrid

> **Un agente de IA para Operaciones.** Pon un agente ligero en cada host; ongrid analiza tus métricas, logs, trazas, topología y código fuente para identificar la causa raíz en lenguaje natural.
>
> *Hecho para equipos de SRE, plataforma y guardia (on-call).*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#stack-tecnológico)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | Español | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[Visión general](#visión-general) • [Inicio rápido](#inicio-rápido) • [Arquitectura](#arquitectura) • [Stack tecnológico](#stack-tecnológico) • [Contribuir](#contribuir)

---

## Visión general

ongrid es un agente de IA para operaciones, de código abierto y autoalojable. Un agente ligero `ongrid-edge` en cada host envía métricas, logs y trazas a la nube a través de un único túnel **saliente** multiplexado —— sin puertos entrantes en el host. La nube es un agente de operaciones impulsado por LLM: preguntas en lenguaje natural y él mismo ejecuta PromQL / LogQL / TraceQL, recorre la topología de servicios, busca en la base de conocimiento, lee el código fuente y llama a herramientas de host de solo lectura para devolver una respuesta fundamentada.

Qué resuelve:

- **Barrera de diagnóstico alta** —— describe el síntoma ("¿por qué se dispara la carga?", "¿quién pierde paquetes?"); el agente decide qué métrica, qué logs y qué consulta mirar.
- **Alertas desconectadas de la causa raíz** —— ante una alerta, recorre la topología para el radio de impacto, correlaciona logs/trazas y localiza la **ubicación en el código fuente** detrás del "por qué".
- **Señales dispersas** —— métricas (Prometheus), logs (Loki), trazas (Tempo), base de conocimiento (búsqueda vectorial) y repositorios de código se unifican y analizan en una sola sesión.
- **Sin exponer la intranet** —— el edge se conecta hacia afuera; cero puertos entrantes en los hosts; el plano de datos de telemetría está separado del plano de control.
- **Autoalojable** —— un solo `docker compose` levanta toda la pila; apunta el modelo a cualquier endpoint compatible con OpenAI.

## Inicio rápido

```bash
# 1. configurar: define la cuenta de admin + una API key de modelo
cp deploy/.env.example deploy/.env

# 2. levantar toda la pila (mysql / ongrid / frontier / nginx / prometheus / grafana)
make compose-up      # make compose-down para detener
```

Abre `https://<host>` e inicia sesión con la cuenta de admin sembrada desde `.env`. Para un paquete de release de producción (TLS, systemd, actualización/desinstalación) consulta [`deploy/install/`](deploy/install/README.md).

**Instalar edge en un host** —— crea el edge en la consola, copia el comando de instalación de una línea para la plataforma de destino y ejecútalo. El edge se conecta hacia afuera y no escucha en ningún puerto entrante.

> Compilar desde el código: `make build` (la nube usa `CGO_ENABLED=1` por el embedder ONNX local) y `cd web && npm ci && npm run build`. Ejecuta `make help` para ver todos los targets.

## Arquitectura

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

- **edge (`ongrid-edge`)** —— uno por host, binario único en Go puro; recopila telemetría y expone herramientas de inspección de solo lectura a través del túnel. Solo saliente, cero puertos entrantes.
- **cloud (`ongrid`)** —— manager + coordinador LLM que distribuye a subagentes y herramientas especializados (PromQL / LogQL / TraceQL / topología / búsqueda de conocimiento / lectura de código) y sintetiza la respuesta.
- **web** —— SPA en React: diagnóstico conversacional + dashboards.

## Stack tecnológico

| Capa | Elección |
|---|---|
| Nube | Go · framework de agentes [eino](https://github.com/cloudwego/eino) · GORM · túnel [geminio](https://github.com/singchia/geminio) · embedder ONNX local |
| Edge | Go —— binario único en Go puro, multiplataforma (Linux / macOS / Windows, x86_64 & ARM64) |
| Frontend | TypeScript · React (English / 简体中文) |
| Telemetría / almacenamiento | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| Modelo | cualquier endpoint compatible con OpenAI —— OpenAI · Anthropic · Gemini · DeepSeek · Zhipu · Kimi · Ollama / vLLM / OpenRouter · … |

## Contribuir

Issues y PRs son bienvenidos. Antes de enviar, asegúrate de que `make build`, `make test` y `make arch-lint` (que valida los límites de contexto acotado) pasen.

## Licencia

[Apache-2.0](LICENSE).
