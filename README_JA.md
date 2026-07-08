# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **インフラを理解し、原因を突き止め、自ら直す運用 AI エージェント — Slack や Telegram から直接。**

*メトリクス · ログ · トレース · トポロジ影響範囲 · 根本原因の相関 · リモート実行 · アラート起点の自動調査 · ナレッジ＆コードの RAG 検索 · 専門エージェントとスキル。*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![Release](https://img.shields.io/github/v/release/ongridio/ongrid?logo=github&label=release&color=2563eb)](https://github.com/ongridio/ongrid/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/ongridio/ongrid?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?logo=apache)](https://opensource.org/licenses/Apache-2.0)
[![Stack](https://img.shields.io/badge/stack-Go%20%7C%20TypeScript%20%7C%20React-1e40af?logo=react&logoColor=white)](#features)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-22c55e.svg?logo=git&logoColor=white)](CONTRIBUTING.md)
[![Telegram](https://img.shields.io/badge/Telegram-Join-26A5E4?logo=telegram&logoColor=white)](https://t.me/ongridai)
[![Slack](https://img.shields.io/badge/Slack-Join-4A154B?logo=slack&logoColor=white)](https://join.slack.com/t/ongrid-co/shared_invite/zt-400skx7hz-WU1nmF1XVYH4S3Q1NfWrbw)

[English](./README.md) | [简体中文](./README_ZH.md) | 日本語 | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>

<div align="center">

[機能](#機能) • [インストール](#インストール) • [インテグレーション](#インテグレーション) • [ライセンス](#ライセンス)

</div>

## 機能

- 🤖 **Coordinator + Specialist エージェント** — coordinator が SRE / ネットワーク / DB のサブエージェントへ派遣
- 🚨 **アラート発火で自動調査** — investigator が RCA worker を派遣、原因をチャットに記録
- 🔍 **根本原因 RCA** — トポロジーで影響範囲を分析、メトリクス/ログ/トレースを相関、ソースコード行まで
- 🔒 **インバウンドポートゼロ** — edge はアウトバウンドのみ、ホストは 22 / 80 / 443 を開かない
- 💻 **ブラウザ SSH** — 逆トンネルで対話シェル、鍵不要、踏み台不要、全コマンド監査
- 🐳 **1 コマンドでセルフホスト** — `install.sh` でフルスタック起動
- 📊 **可観測性スタック組み込み** — Prometheus + Loki + Tempo + Grafana 自動配備、エージェントがクエリを書く
- 🧠 **任意モデル持ち込み** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi、ホット切り替え
- 💬 **双方向 IM チャネル** — Slack / Telegram / Larksuite / DingTalk / WeCom、チャネル別ロケール
- 🛠️ **読み取り専用ホストツール** — bash サンドボックス + 26+ ツール、全コール監査

## インストール

サーバーのアーキテクチャ（`linux-amd64` または `linux-arm64`）に合う最新リリースをダウンロードし、展開してインストーラーを実行します（Ubuntu 22.04+、Debian 12+、RHEL/Rocky 9）：

サーバーのアーキテクチャに合うコマンドを選択してください：

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

**🇨🇳 中国本土ユーザー** — GitHub が遅い場合は、アーキテクチャに合う CDN ミラー URL を使用してください：

```bash
# AMD64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-amd64.tar.xz

# ARM64
wget https://ongrid.cloud/dl/ongrid-v0.9.0-linux-arm64.tar.xz
```

## 製品ツアー

### 根本原因分析

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-agent-write-gate.png" alt="根本原因分析" width="100%" />
</p>

アラートや運用上の質問から開始し、トポロジー、デバイス、メトリクス、ログ、変更履歴を集めて、証拠に基づく分析を作成します。

### ワークフロービルダー

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-workflow-editor.png" alt="ワークフロービルダー" width="100%" />
</p>

トリガー、Agent、ツール、条件、通知を再利用可能な自動化として構成します。

### スキルカタログ

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-skills-catalog.png" alt="スキルカタログ" width="100%" />
</p>

Agent が呼び出せるツールと説明を一覧化し、運用者が能力範囲を把握できます。

### MCP サーバー

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-mcp-servers.png" alt="MCP サーバー" width="100%" />
</p>

外部 MCP サーバーを登録し、ツールを同じ管理されたインベントリに接続します。

### ナレッジベース

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-knowledge-vault.png" alt="ナレッジベース" width="100%" />
</p>

Runbook、インシデント履歴、設計メモ、リポジトリを検索可能な文脈にします。

### 成果物センター

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-artifacts-pages.png" alt="成果物センター" width="100%" />
</p>

生成されたページやレポートを一か所に保存し、レビューと引き継ぎに使えます。

### モニタリング

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-monitor.png" alt="モニタリング" width="100%" />
</p>

ホスト状態、ログ、トレース、アラートを同じ作業空間で確認できます。

### トポロジーマップ

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-topology-map.png" alt="トポロジーマップ" width="100%" />
</p>

依存関係と影響範囲を可視化し、インシデントの波及を追跡します。

### 承認と書き込みゲート

<p align="center">
  <img src="docs/assets/readme-tour/user-20260707-rca-session.png" alt="承認と書き込みゲート" width="100%" />
</p>

リスクの高い操作は承認とポリシー境界の内側で扱います。

## ドキュメント

製品ドキュメントは [ongrid.cloud](https://ongrid.cloud/docs/get-started/introduction) で公開されています。

| 領域 | ここから開始 |
|---|---|
| **はじめに** | [Introduction](https://ongrid.cloud/docs/get-started/introduction) · [Quickstart](https://ongrid.cloud/docs/get-started/quickstart) · [Architecture](https://ongrid.cloud/docs/get-started/architecture) · [Concepts](https://ongrid.cloud/docs/get-started/concepts) |
| **インストールと運用** | [Server install](https://ongrid.cloud/docs/install/server) · [Edge install](https://ongrid.cloud/docs/install/edge) · [First boot](https://ongrid.cloud/docs/install/first-boot) · [Upgrade](https://ongrid.cloud/docs/install/upgrade) |
| **機能** | [Alerts](https://ongrid.cloud/docs/capabilities/alerts) · [RCA](https://ongrid.cloud/docs/capabilities/rca) · [Monitoring](https://ongrid.cloud/docs/capabilities/monitoring) · [Logs](https://ongrid.cloud/docs/capabilities/logs) · [Traces](https://ongrid.cloud/docs/capabilities/traces) · [Knowledge](https://ongrid.cloud/docs/capabilities/knowledge) · [Skills](https://ongrid.cloud/docs/capabilities/skills) |
| **Agent** | [Overview](https://ongrid.cloud/docs/agents/overview) · [Coordinator](https://ongrid.cloud/docs/agents/coordinator) · [Incident investigator](https://ongrid.cloud/docs/agents/incident-investigator) · [Specialists](https://ongrid.cloud/docs/agents/specialists) · [Reviewer](https://ongrid.cloud/docs/agents/reviewer) |
| **リファレンス** | [API](https://ongrid.cloud/docs/reference/api) · [CLI](https://ongrid.cloud/docs/reference/cli) · [Alert rules](https://ongrid.cloud/docs/reference/alert-rules) · [Skill manifest](https://ongrid.cloud/docs/reference/skill-manifest) · [Data plane](https://ongrid.cloud/docs/reference/data-plane) |

## インテグレーション

チームの可観測性・チャネル・モデルスタックにそのまま組み込めます。

| | |
|---|---|
| **可観測性** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **チャネル** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **モデル** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## ライセンス

Apache 2.0 — [LICENSE](LICENSE) を参照。

## Star 履歴

<a href="https://www.star-history.com/#ongridio/ongrid&amp;Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date&amp;theme=dark" />
    <img alt="Star 履歴チャート" src="https://api.star-history.com/svg?repos=ongridio/ongrid&amp;type=Date" />
  </picture>
</a>
