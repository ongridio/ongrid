# <img src="web/public/ongrid-logo.svg" alt="" width="40" align="absmiddle" style="vertical-align: middle;" /> Ongrid

> **システムを理解し、原因を突き止め、自ら直す運用 AI。** *監視、リモート実行、ナレッジベース、スペシャリストエージェント、Bash、ファイルなど各種スキル —— Slack、Telegram、Lark から直接指示を送る。*

[![Go Report Card](https://goreportcard.com/badge/github.com/ongridio/ongrid)](https://goreportcard.com/report/github.com/ongridio/ongrid)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Tech](https://img.shields.io/badge/Tech-Go%20%7C%20TypeScript%20%7C%20React-blue)](#)

[English](./README.md) | [简体中文](./README_ZH.md) | 日本語 | [한국어](./README_KO.md) | [Español](./README_ES.md) | [Français](./README_FR.md) | [Deutsch](./README_DE.md) | [Português](./README_PT.md) | [Русский](./README_RU.md)

[機能](#機能) • [インストール](#インストール) • [インテグレーション](#インテグレーション) • [ライセンス](#ライセンス)

---

<p align="center">
  <img src="docs/assets/demo.gif" alt="Ongrid demo" width="100%" />
</p>
<p align="center"><sub><a href="https://github.com/ongridio/ongrid/releases/download/v0.7.168/Area2_hq.mp4">▶ フル動画 (HD) を見る (MP4, 18 MB)</a></sub></p>

## 機能

- 🤖 **Coordinator + Specialist エージェント** — coordinator が SRE / ネットワーク / DB のサブエージェントへ派遣
- 🚨 **アラート発火で自動調査** — investigator が RCA worker を派遣、原因をチャットに記録
- 🔍 **根本原因 RCA** — トポロジーで影響範囲を分析、メトリクス/ログ/トレースを相関、ソースコード行まで
- 🔒 **インバウンドポートゼロ** — edge はアウトバウンドのみ、ホストは 22 / 80 / 443 を開かない
- 💻 **ブラウザ SSH** — 逆トンネルで対話シェル、鍵不要、踏み台不要、全コマンド監査
- 🐳 **1 コマンドでセルフホスト** — `docker compose up` でフルスタック起動
- 📊 **可観測性スタック組み込み** — Prometheus + Loki + Tempo + Grafana 自動配備、エージェントがクエリを書く
- 🧠 **任意モデル持ち込み** — Anthropic / OpenAI / GLM / DeepSeek / Gemini / Kimi、ホット切り替え
- 💬 **双方向 IM チャネル** — Slack / Telegram / Larksuite / DingTalk / WeCom、チャネル別ロケール
- 🛠️ **読み取り専用ホストツール** — bash サンドボックス + 26+ ツール、全コール監査

## インストール

最新リリースをダウンロードし、展開してインストーラーを実行します（Ubuntu 22.04+、Debian 12+、RHEL/Rocky 9）：

```bash
# 1. 最新リリースをダウンロード（Ubuntu 22.04+、Debian 12+、RHEL/Rocky 9）
wget https://github.com/ongridio/ongrid/releases/download/v0.7.168/ongrid-v0.7.168-linux-amd64.tar.xz

# 2. 展開
tar -xf ongrid-v0.7.168-linux-amd64.tar.xz && cd ongrid-v0.7.168-linux-amd64

# 3. インストール
sudo ./install.sh
```

### またはソースから実行

ローカル開発: 管理者アカウントとモデル API キーを設定し、フルスタックを起動します。

```bash
cp deploy/.env.example deploy/.env
make compose-up    # make compose-down to stop
```

## インテグレーション

チームの可観測性・チャネル・モデルスタックにそのまま組み込めます。

| | |
|---|---|
| **可観測性** | <img src="https://api.iconify.design/logos:prometheus.svg" alt="Prometheus" title="Prometheus" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:grafana.svg" alt="Grafana" title="Grafana" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/loki.svg" alt="Loki" title="Loki" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/tempo.svg" alt="Tempo" title="Tempo" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/opentelemetry.svg" alt="OpenTelemetry" title="OpenTelemetry" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:qdrant-icon.svg" alt="Qdrant" title="Qdrant" width="28" height="28" /> |
| **チャネル** | <img src="https://api.iconify.design/logos:slack-icon.svg" alt="Slack" title="Slack" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:telegram.svg" alt="Telegram" title="Telegram" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/larksuite.svg" alt="Larksuite" title="Larksuite" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/dingtalk.svg" alt="DingTalk" title="DingTalk" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.simpleicons.org/wechat" alt="WeCom" title="WeCom" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://api.iconify.design/logos:webhooks.svg" alt="Webhook" title="Webhook" width="28" height="28" /> |
| **モデル** | <img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/claude-color.svg" alt="Anthropic" title="Anthropic" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/openai.svg" alt="OpenAI" title="OpenAI" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/gemini-color.svg" alt="Gemini" title="Gemini" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/deepseek-color.svg" alt="DeepSeek" title="DeepSeek" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="docs/assets/integrations/zhipu.svg" alt="Zhipu" title="Zhipu" width="28" height="28" />&nbsp;&nbsp;&nbsp;<img src="https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/kimi-color.svg" alt="Kimi" title="Kimi" width="28" height="28" /> |

## ライセンス

Apache 2.0 — [LICENSE](LICENSE) を参照。
