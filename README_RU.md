# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **ИИ-агент для эксплуатации, который понимает вашу инфраструктуру, находит первопричину и устраняет её — прямо из Slack или Telegram.**

*Метрики · логи · трейсы · радиус влияния топологии · корреляция первопричин · удалённое выполнение · автоматическое расследование по алертам · RAG-поиск по знаниям и коду · специализированные агенты и навыки.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | Русский

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.169/Area2_hq.mp4">▶ Смотреть полное демо в HD (MP4, 18 MB)</a></sub></p>

<div align="center">

[Возможности](#возможности) • [Установка](#установка) • [Интеграции](#интеграции) • [Лицензия](#лицензия)

</div>

## Возможности

- 🤖 **Агенты Coordinator + Specialist** — coordinator делегирует суб-агентам SRE / сеть / БД / активы
- 🚨 **Авто-исследование по алерту** — investigator запускает RCA-worker и пишет причину в чат
- 🔍 **Корневая RCA** — обходит топологию, коррелирует метрики/логи/трейсы, до строки исходного кода
- 🔒 **Ноль входящих портов** — edge выходит наружу; нет порта 22 / 80 / 443 на хосте
- 💻 **SSH в браузере** — оболочка через обратный туннель, без ключей, без jumpbox, в аудите
- 🐳 **Self-host одной командой** — `docker compose up` поднимает весь стек
- 📊 **Встроенная observability** — Prometheus + Loki + Tempo + Grafana готовы, агент пишет запросы
- 🧠 **Принесите свою модель** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi, горячая маршрутизация
- 💬 **Двусторонние IM-каналы** — Slack / Telegram / Larksuite / DingTalk / WeCom, локаль на канал
- 🛠️ **Read-only host-инструменты** — sandbox bash + 26+ инструментов, каждый вызов в аудите

## Установка

Скачайте последний релиз, распакуйте и запустите скрипт установки (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9):

```bash
# 1. Скачайте последний релиз (Ubuntu 22.04+, Debian 12+, RHEL/Rocky 9)
wget https://github.com/ongridio/ongrid/releases/download/v0.8.3/ongrid-v0.8.3-linux-amd64.tar.xz

# 2. Распаковка
tar -xf ongrid-v0.8.3-linux-amd64.tar.xz && cd ongrid-v0.8.3-linux-amd64

# 3. Установка
sudo ./install.sh
```

**🇨🇳 Материковый Китай** — если GitHub медленный, скачайте шаг 1 с CDN-зеркала (остальное без изменений):

```bash
wget https://ongrid.cloud/dl/ongrid-v0.8.3-linux-amd64.tar.xz
```

### Или запустить из исходников

Локальная разработка: настройте админ-аккаунт и API-ключ модели, затем поднимите весь стек.

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## Интеграции

Подключается к стекам observability, каналов и моделей, которые ваша команда уже использует.

| | |
|---|---|
| **Observability** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **Каналы** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **Модели** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## Лицензия

Apache 2.0 — см. [LICENSE](LICENSE).
