# 輪読運営エージェント（仮称）

担当者が準備できなくなっても、参加者の条件に合わせて次回の輪読を組み直し、合意と連絡まで進める運営エージェントを開発しています。

MVPは、既存の輪読会の「辞退→再計画→本人の引き受け・投票→計画保存→Discord通知」。固定の幹事がいない輪読で、メンバー同士がLINEで行っていた調整の負担を減らせるか検証します。日程の自動調整と当日の自動司会は今回の対象外です。

[AI HACK 2026](https://aihackathon.jp/)（テーマ：業務を自律化するAIエージェント）向けに開発しています。LLMの呼び出しには [OrcaRouter](https://www.orcarouter.ai/) を使います。

> 開発中です。仕様は [.agent/decisions/spec.md](.agent/decisions/spec.md) を参照してください。当面、設計・評価資料は `.agent/` 内で管理します。

## リポジトリ構成

| パス | 内容 |
| --- | --- |
| `src/backend/` | バックエンド（Go） |
| `src/frontend/` | フロントエンド（HTML / CSS / JavaScript） |
| `docs/` | 将来の公開ドキュメント用（当面は追加・更新しない） |
| `.agent/` | 検討メモ・調査・意思決定の記録 |

## ドキュメント

| ドキュメント | 内容 |
| --- | --- |
| overview | 前提・目的・利用の流れ（準備中） |
| rules | AIに任せること・委任ルール（準備中） |
| [architecture](.agent/decisions/architecture.md) | 共通処理と用途別Playbookの境界・ディレクトリ構成 |
| [api](.agent/decisions/api.md) | MVPのAPI契約・JSON型・認証・同意・エラー・画面別の対応（契約確定、実装はこれから） |
| data-model | テーブル定義（準備中） |
| agent | 状態遷移・ツール・モデル構成（準備中） |
| development | セットアップ・環境変数・開発ルール（準備中） |
| [evaluation](.agent/decisions/evaluation.md) | LINEとの比較方法・固定評価ケース・費用の計測（手順のみ、結果は未計測） |
| [backend implementation plan](.agent/decisions/backend-implementation-plan.md) | バックエンドの実装順と開発エージェントの分担案（未着手） |

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
