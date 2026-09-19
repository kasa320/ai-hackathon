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
| [api](.agent/decisions/api.md) | MVPのAPI契約・JSON型・認証・同意・エラー・画面別の対応（`backend` ブランチで実装済み） |
| data-model | テーブル定義（準備中） |
| agent | 状態遷移・ツール・モデル構成（準備中） |
| development | セットアップ・環境変数・開発ルール（準備中） |
| [evaluation](.agent/decisions/evaluation.md) | LINEとの比較方法・固定評価ケース・費用の計測（手順のみ、結果は未計測） |
| [backend implementation plan](.agent/decisions/backend-implementation-plan.md) | バックエンドの実装順と開発エージェントの分担案 |

## 起動方法

必要なもの：Go 1.25 以上、make

```sh
cp .env.example .env   # 必要に応じて値を設定する
make dev               # http://localhost:8080 で起動
```

`AGENT_MODE=fake`（既定）では LLM を呼ばず、輪読 Playbook の規則だけで案を作ります（仮の判断処理）。OrcaRouter を使うときは `AGENT_MODE=llm` と `ORCAROUTER_API_KEY` を設定します。

### 主な環境変数

| 変数 | 既定値 | 内容 |
| --- | --- | --- |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | 通知に載せる画面URLと Origin 検証の基点。https なら Cookie に Secure を付ける |
| `DEV_MODE` | 空 | `1` で開発・デモ用 API（`/api/dev/*`）と障害注入を有効にする。本番では設定しない |
| `SESSION_SECRET` | 空 | セッショントークンのハッシュ用。未設定なら起動ごとに変わる（再起動でログアウト） |
| `DISCORD_CLIENT_ID` / `DISCORD_CLIENT_SECRET` / `DISCORD_REDIRECT_URL` | 空 | Discord ログイン。未設定なら `/api/auth/discord` は `auth_error=provider_unavailable` に戻る |
| `DISCORD_BOT_TOKEN` / `DISCORD_CHANNEL_ID` | 空 | 通知先チャンネル。未設定なら通知はサーバーログに出すだけ |
| `ORCAROUTER_SEARCH_MODEL` | 空 | 目次の Web 検索に使う検索付きモデル。空なら Web 検索をせず画像の提出を依頼する |
| `ORCAROUTER_VISION_MODEL` | 空 | 目次画像の書き写しに使うモデル（空なら `ORCAROUTER_MODEL`） |

### 開発モード（デモ）

`DEV_MODE=1` で起動すると、Discord の設定なしで人物を切り替えて操作できます。

```sh
curl -X POST localhost:8080/api/dev/seed  -H 'Content-Type: application/json' -d '{"scenario":"replan_demo"}'   # 初期データ（B担当で確定済み）
curl -X POST localhost:8080/api/dev/login -H 'Content-Type: application/json' -d '{"discord_user_id":"100000000000000002"}' -c b.jar  # B としてログイン
curl -X POST localhost:8080/api/dev/clock/advance -H 'Content-Type: application/json' -d '{"seconds":86400}'   # 時計を24時間進める
curl -X PUT  localhost:8080/api/dev/faults -H 'Content-Type: application/json' -d '{"llm":"error","notify":null}' # 障害注入
```

シナリオは `replan_demo`（辞退→AIの確認→再計画→同意→確定を実演する）と `initial_demo`（開催回登録の直後）。デモ用の利用者 A〜D の Discord ID は `100000000000000001`〜`…004`（架空）。

### テスト

- `make test`：全テスト（各パッケージの単体テストと結合テスト）
- `make test-e2e`：HTTP 経由の結合テストだけ（`src/backend/tests/e2e/`。評価ケース E01〜E12 と目次の取得を、外部サービスを偽物に差し替えて確認する）

その他のコマンド：`make fmt`（整形）、`make vet`（静的検査）、`make db-reset`（ローカル DB の削除）

依存ライブラリ：`modernc.org/sqlite`（SQLite）、`golang.org/x/text`（目次の照合での Unicode 正規化）。LLM・Discord の呼び出しは標準ライブラリの HTTP クライアントで行う。

## 提出物

- デモ動画：準備中
- 記事：準備中
