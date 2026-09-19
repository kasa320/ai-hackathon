# 輪読運営エージェント（仮称）

「この本を、この人たちと輪読したい」と伝えるだけで、AIが参加者との連絡、日程・担当・準備内容の決定、欠席などが出たときの計画の組み直し、当日の進行までを担うエージェントです。

[AI HACK 2026](https://aihackathon.jp/)（テーマ：業務を自律化するAIエージェント）向けに開発しています。LLMの呼び出しには [OrcaRouter](https://www.orcarouter.ai/) を使います。

> 開発中です。仕様は [.agent/decisions/spec.md](.agent/decisions/spec.md) を参照してください（確定したものから `docs/` に移します）。

## リポジトリ構成

| パス | 内容 |
| --- | --- |
| `src/backend/` | バックエンド（Go） |
| `src/frontend/` | フロントエンド（HTML / CSS / JavaScript） |
| `docs/` | 仕様・設計ドキュメント |
| `.agent/` | 検討メモ・調査・意思決定の記録 |

## ドキュメント

| ドキュメント | 内容 |
| --- | --- |
| overview | 前提・目的・利用の流れ（準備中） |
| rules | AIに任せること・委任ルール（準備中） |
| architecture | 全体構成・ディレクトリ・分担（準備中） |
| api | フロントエンドとバックエンドの間の API（準備中） |
| data-model | テーブル定義（準備中） |
| agent | 状態遷移・ツール・モデル構成（準備中） |
| development | セットアップ・環境変数・開発ルール（準備中） |
| evaluation | テストケース・費用の計測（準備中） |

## 起動方法

必要なもの：Go 1.25 以上、make

```sh
cp .env.example .env   # 必要に応じて値を設定する
make dev               # http://localhost:8080 で起動
```

`AGENT_MODE=fake`（既定）では LLM を呼びません。OrcaRouter を使うときは `AGENT_MODE=llm` と `ORCAROUTER_API_KEY` を設定します。

その他のコマンド：`make test`（テスト）、`make fmt`（整形）、`make vet`（静的検査）、`make db-reset`（ローカル DB の削除）

## 提出物

- デモ動画：準備中
- 記事：準備中
