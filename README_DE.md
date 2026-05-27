# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> ongrid

> **Ein KI-Agent für den Betrieb.** Installieren Sie auf jedem Host einen leichtgewichtigen Agenten; Ongrid analysiert Ihre Metriken, Logs, Traces, Topologie und Ihren Quellcode und ermittelt die Grundursache in natürlicher Sprache.
>
> *Entwickelt für SRE-, DevOps- und Plattformteams.*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#tech-stack)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | Deutsch | [Português](./README_PT.md) | [Русский](./README_RU.md)

[Überblick](#überblick) • [Schnellstart](#schnellstart) • [Architektur](#architektur) • [Tech-Stack](#tech-stack) • [Mitwirken](#mitwirken)

---

## Überblick

Ongrid ist ein quelloffener, selbst-hostbarer KI-Agent für den Betrieb. Ein leichtgewichtiger `ongrid-edge`-Agent auf jedem Host sendet Metriken, Logs und Traces über einen einzigen gemultiplexten **ausgehenden** Tunnel in die Cloud —— keine eingehenden Ports auf dem Host. Die Cloud ist ein LLM-gesteuerter Ops-Agent: Sie fragen in natürlicher Sprache, und er führt selbst PromQL / LogQL / TraceQL aus, durchläuft die Service-Topologie, durchsucht die Wissensdatenbank, liest Quellcode und ruft schreibgeschützte Host-Tools auf, um eine fundierte Antwort zu liefern.

Was es löst:

- **Hohe Einstiegshürde beim Troubleshooting** —— beschreiben Sie das Symptom („Warum steigt die Last?", „Wer verliert Pakete?"); der Agent entscheidet, welche Metrik, welche Logs und welche Query er ansieht.
- **Alarme ohne Bezug zur Grundursache** —— bei einem Alarm durchläuft er die Topologie für den Blast Radius, korreliert Logs/Traces und lokalisiert die **Quellcode-Stelle** hinter dem „Warum".
- **Verstreute Signale** —— Metriken (Prometheus), Logs (Loki), Traces (Tempo), Wissensdatenbank (Vektorsuche) und Quell-Repositories werden vereint und in einer Sitzung gemeinsam analysiert.
- **Kein offenes Intranet** —— der Edge wählt sich nach außen; null eingehende Ports auf den Hosts; die Telemetrie-Datenebene ist von der Steuerungsebene getrennt.
- **Selbst-hostbar** —— ein einziges `docker compose` startet den gesamten Stack; richten Sie das Modell auf einen beliebigen OpenAI-kompatiblen Endpunkt.

## Schnellstart

```bash
# 1. konfigurieren: Admin-Konto + einen Modell-API-Key festlegen
cp deploy/.env.example deploy/.env

# 2. gesamten Stack starten (mysql / ongrid / frontier / nginx / prometheus / grafana)
make compose-up      # make compose-down zum Stoppen
```

Öffnen Sie `https://<host>` und melden Sie sich mit dem aus `.env` initialisierten Admin-Konto an. Für ein Produktions-Release-Paket (TLS, systemd, Upgrade/Deinstallation) siehe [`deploy/install/`](deploy/install/README.md).

**Edge auf einem Host installieren** —— erstellen Sie den Edge in der Konsole, kopieren Sie den Einzeiler-Installationsbefehl für die Zielplattform und führen Sie ihn aus. Der Edge wählt sich nach außen und lauscht auf keinem eingehenden Port.

> Aus dem Quellcode bauen: `make build` (die Cloud nutzt `CGO_ENABLED=1` für den lokalen ONNX-Embedder) und `cd web && npm ci && npm run build`. `make help` listet alle Targets.

## Architektur

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

- **edge (`ongrid-edge`)** —— einer pro Host, reines Go als Single-Binary; sammelt Telemetrie und stellt schreibgeschützte Inspektions-Tools über den Tunnel bereit. Nur ausgehend, null eingehende Ports.
- **cloud (`ongrid`)** —— Manager + LLM-Koordinator, der an spezialisierte Sub-Agenten und Tools (PromQL / LogQL / TraceQL / Topologie / Wissenssuche / Quellcode-Lesen) verteilt und die Antwort zusammenführt.
- **web** —— React-SPA: dialogbasiertes Troubleshooting + Dashboards.

## Tech-Stack

| Schicht | Wahl |
|---|---|
| Cloud | Go · [eino](https://github.com/cloudwego/eino) Agenten-Framework · GORM · [geminio](https://github.com/singchia/geminio) Tunnel · lokaler ONNX-Embedder |
| Edge | Go —— reines Go als Single-Binary, plattformübergreifend (Linux / macOS / Windows, x86_64 & ARM64) |
| Frontend | TypeScript · React (English / 简体中文) |
| Telemetrie / Speicher | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| Modell | beliebiger OpenAI-kompatibler Endpunkt —— OpenAI · Anthropic · Gemini · DeepSeek · Zhipu · Kimi · Ollama / vLLM / OpenRouter · … |

## Mitwirken

Issues und PRs sind willkommen. Stellen Sie vor dem Einreichen sicher, dass `make build`, `make test` und `make arch-lint` (prüft die Grenzen der Bounded Contexts) alle durchlaufen.

## Lizenz

[Apache-2.0](LICENSE).
