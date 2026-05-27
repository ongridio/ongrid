# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> ongrid

> **Un agent IA pour les opérations.** Installez un agent léger sur chaque hôte ; ongrid analyse vos métriques, logs, traces, topologie et code source pour identifier la cause racine en langage naturel.
>
> *Conçu pour les équipes SRE, plateforme et d'astreinte (on-call).*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#stack-technique)

[English](./README.md) | [简体中文](./README_ZH.md) | [日本語](./README_JA.md) | [한국어](./README_KO.md) | [Español](./README_ES.md) | Français | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[Aperçu](#aperçu) • [Démarrage rapide](#démarrage-rapide) • [Architecture](#architecture) • [Stack technique](#stack-technique) • [Contribuer](#contribuer)

---

## Aperçu

ongrid est un agent IA pour les opérations, open source et auto-hébergeable. Un agent léger `ongrid-edge` sur chaque hôte envoie métriques, logs et traces vers le cloud via un unique tunnel **sortant** multiplexé —— aucun port entrant sur l'hôte. Le cloud est un agent d'exploitation piloté par LLM : posez une question en langage naturel et il exécute lui-même les PromQL / LogQL / TraceQL, parcourt la topologie de services, interroge la base de connaissances, lit le code source et appelle des outils hôte en lecture seule pour fournir une réponse étayée.

Ce qu'il résout :

- **Barrière de dépannage élevée** —— décrivez le symptôme (« pourquoi la charge grimpe-t-elle ? », « qui perd des paquets ? ») ; l'agent décide quelle métrique, quels logs et quelle requête examiner.
- **Alertes déconnectées de la cause racine** —— à la réception d'une alerte, il parcourt la topologie pour le rayon d'impact, corrèle logs/traces et localise l'**emplacement dans le code source** derrière le « pourquoi ».
- **Signaux dispersés** —— métriques (Prometheus), logs (Loki), traces (Tempo), base de connaissances (recherche vectorielle) et dépôts de code sont unifiés et analysés en une seule session.
- **Pas d'intranet exposé** —— l'edge se connecte vers l'extérieur ; zéro port entrant sur les hôtes ; le plan de données de télémétrie est séparé du plan de contrôle.
- **Auto-hébergeable** —— un seul `docker compose` démarre toute la pile ; pointez le modèle vers n'importe quel endpoint compatible OpenAI.

## Démarrage rapide

```bash
# 1. configurer : définissez le compte admin + une clé API de modèle
cp deploy/.env.example deploy/.env

# 2. démarrer toute la pile (mysql / ongrid / frontier / nginx / prometheus / grafana)
make compose-up      # make compose-down pour arrêter
```

Ouvrez `https://<host>` et connectez-vous avec le compte admin initialisé depuis `.env`. Pour un paquet de release de production (TLS, systemd, mise à niveau/désinstallation), voir [`deploy/install/`](deploy/install/README.md).

**Installer l'edge sur un hôte** —— créez l'edge dans la console, copiez la commande d'installation en une ligne pour la plateforme cible et exécutez-la. L'edge se connecte vers l'extérieur et n'écoute sur aucun port entrant.

> Compiler depuis les sources : `make build` (le cloud utilise `CGO_ENABLED=1` pour l'embedder ONNX local) et `cd web && npm ci && npm run build`. Lancez `make help` pour tous les targets.

## Architecture

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

- **edge (`ongrid-edge`)** —— un par hôte, binaire unique en Go pur ; collecte la télémétrie et expose des outils d'inspection en lecture seule via le tunnel. Sortant uniquement, zéro port entrant.
- **cloud (`ongrid`)** —— manager + coordinateur LLM qui répartit vers des sous-agents et outils spécialisés (PromQL / LogQL / TraceQL / topologie / recherche de connaissances / lecture de code) et synthétise la réponse.
- **web** —— SPA React : dépannage conversationnel + tableaux de bord.

## Stack technique

| Couche | Choix |
|---|---|
| Cloud | Go · framework d'agents [eino](https://github.com/cloudwego/eino) · GORM · tunnel [geminio](https://github.com/singchia/geminio) · embedder ONNX local |
| Edge | Go —— binaire unique en Go pur, multiplateforme (Linux / macOS / Windows, x86_64 & ARM64) |
| Frontend | TypeScript · React (English / 简体中文) |
| Télémétrie / stockage | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| Modèle | n'importe quel endpoint compatible OpenAI —— OpenAI · Anthropic · Gemini · DeepSeek · Zhipu · Kimi · Ollama / vLLM / OpenRouter · … |

## Contribuer

Les issues et PR sont les bienvenues. Avant de soumettre, assurez-vous que `make build`, `make test` et `make arch-lint` (qui vérifie les limites de contexte délimité) passent tous.

## Licence

[Apache-2.0](LICENSE).
