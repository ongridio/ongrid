# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> ongrid

> **運用のための AI エージェント。** 各ホストに軽量エージェントを配置すると、ongrid がメトリクス・ログ・トレース・トポロジー・ソースコードを横断的に分析し、自然言語で根本原因を特定します。
>
> *SRE・プラットフォーム・オンコールチームのために。*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#技術スタック)

[English](./README.md) | [简体中文](./README_ZH.md) | 日本語 | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[概要](#概要) • [クイックスタート](#クイックスタート) • [アーキテクチャ](#アーキテクチャ) • [技術スタック](#技術スタック) • [コントリビュート](#コントリビュート)

---

## 概要

ongrid はオープンソースでセルフホスト可能な、運用のための AI エージェントです。各ホストに軽量な `ongrid-edge` エージェントを配置すると、単一の多重化された**アウトバウンド**トンネル経由でメトリクス・ログ・トレースをクラウドへ送信します —— ホスト側にインバウンドポートは一切不要です。クラウドは LLM 駆動の運用エージェントで、自然言語で質問すると、自ら PromQL / LogQL / TraceQL を実行し、サービストポロジーをたどり、ナレッジベースを検索し、ソースコードを読み、読み取り専用のホストツールを呼び出して、根拠のある回答を返します。

解決する課題:

- **トラブルシューティングの敷居が高い** —— 症状を自然言語で伝えるだけ（「なぜ load が急騰したのか」「誰がパケットを落としているのか」）。どのメトリクス・ログ・クエリを見るかはエージェントが判断します。
- **アラートと根本原因の断絶** —— アラート発生時、トポロジーをたどって影響範囲を分析し、ログ/トレースを相関させ、「なぜ」を説明する**ソースコードの位置**まで特定します。
- **シグナルの分散** —— メトリクス（Prometheus）・ログ（Loki）・トレース（Tempo）・ナレッジベース（ベクトル検索）・ソースリポジトリを統合し、1 つのセッションでまとめて分析します。
- **イントラネットを公開しない** —— edge はアウトバウンド接続のみ。ホストのインバウンドポートはゼロ。テレメトリのデータプレーンはコントロールプレーンと分離されています。
- **セルフホスト可能** —— `docker compose` 一発でフルスタックが起動。モデルは任意の OpenAI 互換エンドポイントに接続できます。

## クイックスタート

```bash
# 1. 設定: 管理者アカウント + モデルの API キーを設定
cp deploy/.env.example deploy/.env

# 2. フルスタックを起動 (mysql / ongrid / frontier / nginx / prometheus / grafana)
make compose-up      # 停止は make compose-down
```

`https://<host>` を開き、`.env` でシードした管理者アカウントでログインします。本番リリースパッケージ（TLS・systemd・アップグレード/アンインストール）は [`deploy/install/`](deploy/install/README.md) を参照してください。

**ホストに edge をインストール** —— コンソールで edge を作成し、対象プラットフォーム用のワンライナーのインストールコマンドをコピーして実行します。edge はアウトバウンド接続のみで、インバウンドポートを待ち受けません。

> ソースからビルド: `make build`（クラウドはローカル ONNX embedder のため `CGO_ENABLED=1`）と `cd web && npm ci && npm run build`。全ターゲットは `make help` を参照。

## アーキテクチャ

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

- **edge (`ongrid-edge`)** —— ホストごとに 1 つ、ピュア Go の単一バイナリ。テレメトリを収集し、トンネル経由で読み取り専用の点検ツールを公開します。アウトバウンドのみ、インバウンドポートはゼロ。
- **cloud (`ongrid`)** —— manager + LLM コーディネーター。専門のサブエージェントとツール（PromQL / LogQL / TraceQL / トポロジー / ナレッジ検索 / ソース読み取り）に振り分け、回答を統合します。
- **web** —— React SPA: 対話型トラブルシューティング + ダッシュボード。

## 技術スタック

| レイヤー | 選定 |
|---|---|
| クラウド | Go · [eino](https://github.com/cloudwego/eino) エージェントフレームワーク · GORM · [geminio](https://github.com/singchia/geminio) トンネル · ローカル ONNX embedder |
| エッジ | Go —— ピュア Go 単一バイナリ、クロスプラットフォーム（Linux / macOS / Windows、x86_64 & ARM64） |
| フロントエンド | TypeScript · React（English / 简体中文） |
| テレメトリ / ストレージ | Prometheus · Loki · Tempo · Grafana · qdrant · MySQL / SQLite |
| モデル | 任意の OpenAI 互換エンドポイント —— OpenAI · Anthropic · Gemini · DeepSeek · Zhipu · Kimi · Ollama / vLLM / OpenRouter · … |

## コントリビュート

Issue と PR を歓迎します。提出前に `make build`、`make test`、`make arch-lint`（境界づけられたコンテキストの境界を検証）がすべて通ることを確認してください。

## ライセンス

[Apache-2.0](LICENSE)。
