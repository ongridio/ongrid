# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="middle" style="vertical-align: middle;" /> ongrid

> **Установите лёгкий агент на каждый хост и устраняйте неполадки на естественном языке —— оповещения, логи, метрики, трейсы, топология и исходный код анализируются вместе облачным AIOps-агентом.**

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#технологии)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | Русский

[Обзор](#обзор) • [Быстрый старт](#быстрый-старт) • [Архитектура](#архитектура) • [Технологии](#технологии) • [Участие](#участие)

---

## Обзор

ongrid — это платформа AIOps с открытым исходным кодом, которую можно разворачивать у себя. Лёгкий агент `ongrid-edge` на каждом хосте отправляет метрики, логи и трейсы в облако через единый мультиплексированный **исходящий** туннель —— без входящих портов на хосте. Облако — это управляемый LLM операционный агент: вы спрашиваете на естественном языке, а он сам выполняет PromQL / LogQL / TraceQL, обходит топологию сервисов, ищет в базе знаний, читает исходный код и вызывает инструменты хоста только для чтения, чтобы дать обоснованный ответ.

Что решает:

- **Высокий порог входа в диагностику** —— опишите симптом («почему скачет нагрузка?», «кто теряет пакеты?»); агент сам решает, какую метрику, какие логи и какой запрос смотреть.
- **Разрыв между алертами и первопричиной** —— при алерте он обходит топологию для оценки радиуса воздействия, коррелирует логи/трейсы и определяет **место в исходном коде**, объясняющее «почему».
- **Разрозненные сигналы** —— метрики (Prometheus), логи (Loki), трейсы (Tempo), база знаний (векторный поиск) и репозитории кода объединяются и анализируются в одной сессии.
- **Без открытия внутренней сети** —— edge подключается наружу; ноль входящих портов на хостах; плоскость данных телеметрии отделена от плоскости управления.
- **Self-hosted** —— один `docker compose` поднимает весь стек; направьте модель на любой OpenAI-совместимый эндпоинт.

## Быстрый старт

```bash
# 1. настройка: задайте учётную запись администратора + API-ключ модели
cp deploy/.env.example deploy/.env

# 2. поднять весь стек (mysql / ongrid / frontier / nginx / prometheus / grafana)
make compose-up      # make compose-down для остановки
```

Откройте `https://<host>` и войдите под учётной записью администратора, заданной из `.env`. Готовый пакет для продакшена (TLS, systemd, обновление/удаление) — см. [`deploy/install/`](deploy/install/README.md).

**Установка edge на хост** —— создайте edge в консоли, скопируйте однострочную команду установки для нужной платформы и выполните её. edge подключается наружу и не слушает ни одного входящего порта.

> Сборка из исходников: `make build` (облако собирается с `CGO_ENABLED=1` из-за локального ONNX-эмбеддера) и `cd web && npm ci && npm run build`. `make help` покажет все цели.

## Архитектура

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

- **edge (`ongrid-edge`)** —— по одному на хост, один бинарник на чистом Go; собирает телеметрию и предоставляет инструменты проверки только для чтения через туннель. Только исходящие, ноль входящих портов.
- **cloud (`ongrid`)** —— manager + LLM-координатор, который распределяет задачи между специализированными суб-агентами и инструментами (PromQL / LogQL / TraceQL / топология / поиск по знаниям / чтение кода) и формирует итоговый ответ.
- **web** —— React SPA: диалоговая диагностика + дашборды.

## Технологии

| Слой | Выбор |
|---|---|
| Облако | Go · фреймворк агентов [eino](https://github.com/cloudwego/eino) · GORM · туннель [geminio](https://github.com/singchia/geminio) · локальный ONNX-эмбеддер |
| Edge | Go —— один бинарник на чистом Go, кроссплатформенный (Linux / macOS / Windows, x86_64 & ARM64) |
| Фронтенд | TypeScript · React (English / 简体中文) |
| Телеметрия / хранение | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| Модель | любой OpenAI-совместимый эндпоинт —— OpenAI · Anthropic · Gemini · DeepSeek · Zhipu · Kimi · Ollama / vLLM / OpenRouter · … |

## Участие

Issue и PR приветствуются. Перед отправкой убедитесь, что `make build`, `make test` и `make arch-lint` (проверяет границы ограниченных контекстов) проходят.

## Лицензия

[Apache-2.0](LICENSE).
