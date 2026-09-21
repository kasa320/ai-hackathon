# マルナゲ（Marunage）— 幹事エージェント

担当者が準備できなくなっても、参加者の条件に合わせて次回の輪読を組み直し、合意と連絡まで進める運営エージェントを開発しています。

MVPは、既存の輪読会の「期間だけ登録→日時と進行の提案→本人の引き受け・投票→計画保存→Discord通知」と、そのあとの「辞退→再計画」。固定の幹事がいない輪読で、メンバー同士がLINEで行っていた調整の負担を減らせるか検証します。当日の自動司会と、期間全体の複数回をまとめて決めることは今回の対象外です。

[AI HACK 2026](https://aihackathon.jp/)（テーマ：業務を自律化するAIエージェント）向けに開発しています。LLMの呼び出しには [OrcaRouter](https://www.orcarouter.ai/) を使います。

> 開発中です。仕様は [.agent/decisions/spec.md](.agent/decisions/spec.md) を参照してください。当面、設計・評価資料は `.agent/` 内で管理します。

## リポジトリ構成

| パス | 内容 |
| --- | --- |
| `src/backend/` | バックエンド（Go） |
| `src/frontend/` | フロントエンド（HTML / CSS / JavaScript） |
| `docs/` | API・データ構造・DBのドキュメント |
| `.agent/` | 検討メモ・調査・意思決定の記録 |

## ドキュメント

| ドキュメント | 内容 |
| --- | --- |
| overview | 前提・目的・利用の流れ（準備中） |
| rules | AIに任せること・委任ルール（準備中） |
| [run](docs/run.md) | 起動方法・デモモード・よく使うコマンド |
| [architecture](.agent/decisions/architecture.md) | 共通処理と用途別Playbookの境界・ディレクトリ構成 |
| [api-endpoint](docs/api-endpoint.md) | エンドポイント一覧・共通の呼び出し規約・エラー・画面URL |
| [data-structure](docs/data-structure.md) | APIがやりとりする型と状態値 |
| [database](docs/database.md) | テーブル定義と1レコードのサンプル |
| [scheduling-rule](docs/scheduling-rule.md) | 輪読の日程調整の流れ・候補日時の決め方・確定条件・期限 |
| agent | 状態遷移・ツール・モデル構成（準備中） |
| development | セットアップ・環境変数・開発ルール（準備中） |
| [evaluation](.agent/decisions/evaluation.md) | LINEとの比較方法・固定評価ケース・費用の計測（手順のみ、結果は未計測） |
| [backend implementation plan](.agent/decisions/backend-implementation-plan.md) | バックエンドの実装順と開発エージェントの分担案 |

## 起動方法

必要なもの：Go 1.25 以上、make

```sh
cp .env.example .env   # 必要に応じて値を設定する
make dev               # http://localhost:24680 で起動
```

`AGENT_MODE=fake`（既定）では LLM を呼ばず、輪読 Playbook の規則だけで案を作り、自由文も規則だけで解釈します（仮の判断処理）。OrcaRouter を使うときは `AGENT_MODE=llm` と `ORCAROUTER_API_KEY` を設定します。

### 画面

Go サーバーが `src/frontend/` をそのまま配信します。ビルドも依存パッケージもありません（素の ES モジュール）。

| パス | 画面 |
| --- | --- |
| `/` | 未ログインは仕組みの紹介、ログイン後は「あなたが返事をする1件」と会の一覧 |
| `/session.html?id={session_id}` | 会の詳細。次にすること・案・参加条件・エージェントの状況・通知・実行記録 |
| `/setup.html` | 会の登録（形式 → 参加者 → 教材と範囲 → いつまでに開くか） |

| ファイル | 役割 |
| --- | --- |
| `js/api.js` | API 呼び出し。CSRF と Idempotency-Key の付与、エラーの型 |
| `js/poll.js` | 会の詳細の取り直し（3秒間隔、非表示中は止める） |
| `js/ui.js` | 画面をまたぐ部品（上部の帯・進行表・知らせ・ダイアログ・デモ用の帯） |
| `js/features/reading/` | 輪読固有の画面（範囲・進行表・参加条件・代案・目次の取得） |
| `css/styles.css` | デザイン。守っている決めごとは先頭のコメントに書いてあります |

`DEV_MODE=1` のときだけ、画面上部にデモ用の帯（人物の切り替え・時計を進める・初期データの投入）が出ます。

開催日時は登録時に入れません。開始日・終了目安日・1回の長さを渡すと、参加者から参加できる曜日や時間帯を集め、全員の共通時間から日時と進行を提案します。輪読は日程を決めてから範囲を読んでくるので、準備状況は聞きません。説明の担当はエージェントが担当の少ない人から割り振り、割り振られた本人が引き受けるかを答えます。最大参加時間や出られない日はこちらから聞かず、本人が言ったときだけ反映します。時刻は日本時間（JST）。管理者の承認・全員の明示的な同意・担当者本人の引き受けがそろった時点で確定します。未回答・未定・欠席の人を除いて確定することはありません。日時を人が決めたいときは、登録時に `starts_at` を渡せば今までどおりです。

予定が不明なら対象者に追加確認し、共通時間がなければ期間・所要時間・参加条件の見直しを案内して管理者判断待ちにします。否決された日時は同じ案件で再提案しません。会の期間や所要時間をAIが勝手に変更することはありません。公開型の追加契約は [.agent/decisions/structured-scheduling-implementation.md](.agent/decisions/structured-scheduling-implementation.md) を参照してください（`docs/` は更新停止中）。

期間から調整した回では、確定後に欠席や時間条件の変更があった場合も全員参加を維持し、不成立なら再調整を案内します。確定日時を変更するAPIは未対応のため、日時を変える場合は新しい開催回を登録します。日時を直接指定した既存会の再計画は従来のルールを維持します。

参加条件は自由文でも入力できます（`POST /api/sessions/{id}/preparations/me/interpretations`）。LLM が決めるのは定義済み項目の値だけで、結果は保存されません。本人が確認・修正して通常の送信をしたときに初めて保存されます。原文は解釈用モデルへの送信にだけ使い、DB とログには残しません。

`DISCORD_BOT_TOKEN` を設定すると、同じ項目を Discord の DM からも更新できます。Bot は「いつ頃なら参加できそうですか？」のように聞き、ふだんの言葉での返信から AI が値を取り出します。足りない項目は AI が会話調の質問文を考えて聞き直し、全部そろったら確認ボタンを出します（AI の質問文は長さを制限し、改行・メンション・URL を取り除いてから送る。使えなければ既定の文に戻す）。保存されるのは本人がボタンを押した内容だけで、会話の途中経過はメモリにしか置きません。DM を使わない人は Web だけで完結できます。

Discordに「参加条件」と送ると、参加できる日時を会話で入力できます。「回答」と送ると、現在の案の日時・長さ・進行・担当を確認し、同意や担当の引き受けをボタンで回答できます。それぞれ何を判断する回答なのかを明示し、参加条件の保存と案への同意を区別します。ボタンは本人・メッセージ・案の版に紐づき、旧版・期限切れの回答は拒否します。確認内容がDiscordの長さ制限を超える場合はWebへ案内します。

日程の自由文も決められた型への抽出に限定します。抽出用AIには承認・保存の権限を与えず、未知の項目・重複キー・不正な型は拒否し、原文や自由記述の理由を計画用AIへ渡しません。抽出誤りは残るため、保存前に本人が確認します。`AGENT_MODE=fake` のDM日程入力は `水 20:00-22:00` または `2026-10-07 21:00-22:00` の形式（1時間帯）、`未定`、`期間内は参加不可` に対応します。複数時間帯はWebのフォーム、自然文の抽出は `AGENT_MODE=llm` を使います。

### 主な環境変数

| 変数 | 既定値 | 内容 |
| --- | --- | --- |
| `PUBLIC_BASE_URL` | `http://localhost:24680` | 通知に載せる画面URLと Origin 検証の基点。https なら Cookie に Secure を付ける |
| `DEV_MODE` | 空 | `1` で開発・デモ用 API（`/api/dev/*`）と障害注入を有効にする。画面のファイルは `Clear-Site-Data` で毎回読み直させる（古い JS・CSS を掴んだブラウザ対策）。本番では設定しない |
| `SESSION_SECRET` | 空 | セッショントークンのハッシュ用。未設定なら起動ごとに変わる（再起動でログアウト） |
| `DISCORD_CLIENT_ID` / `DISCORD_CLIENT_SECRET` / `DISCORD_REDIRECT_URL` | 空 | Discord ログイン。未設定なら `/api/auth/discord` は `auth_error=provider_unavailable` に戻る |
| `DISCORD_BOT_TOKEN` | 空 | Bot が常駐し、DM での対話と本人宛て通知の DM 送信を行う（`DIRECT_MESSAGES` インテントが必要）。未設定なら通知はサーバーログに出すだけ |
| `DISCORD_CHANNEL_ID` | 空 | DM が使えないときの退避先と、全員宛ての連絡（計画の確定）の送り先。未設定なら本人宛ての DM だけを送り、宛先のない通知は失敗として記録する |
| `ORCAROUTER_PLANNER_MODEL` | 空 | 案（日時・進行・担当）を考えるモデル。空なら `ORCAROUTER_MODEL` |
| `ORCAROUTER_INTERPRETER_MODEL` | 空 | Web・Discordの自由文から参加条件を取り出すモデル。空なら `ORCAROUTER_MODEL`。Discordの1往復は60秒で打ち切るので、応答の速いモデルを選ぶ |
| `ORCAROUTER_TIMEOUT_SECONDS` | `180` | LLM 呼び出し1回の待ち時間の上限（1〜600秒）。超えると一時的な失敗として再試行する |
| `ORCAROUTER_SEARCH_MODEL` | 空 | 国立国会図書館サーチに目次が登録されていない本を Web 検索で探す検索付きモデル。空なら Web 検索をせず画像の提出を依頼する |
| `ORCAROUTER_VISION_MODEL` | 空 | 目次画像の書き写しに使うモデル（空なら `ORCAROUTER_MODEL`） |

### 開発モード（デモ）

`DEV_MODE=1` で起動すると、Discord の設定なしで人物を切り替えて操作できます。

```sh
curl -X POST localhost:24680/api/dev/seed  -H 'Content-Type: application/json' -d '{"scenario":"replan_demo"}'   # 初期データ（B担当で確定済み）
curl -X POST localhost:24680/api/dev/login -H 'Content-Type: application/json' -d '{"discord_user_id":"100000000000000002"}' -c b.jar  # B としてログイン
curl -X POST localhost:24680/api/dev/clock/advance -H 'Content-Type: application/json' -d '{"seconds":86400}'   # 時計を24時間進める
curl -X PUT  localhost:24680/api/dev/faults -H 'Content-Type: application/json' -d '{"llm":"error","notify":null}' # 障害注入
```

シナリオは `replan_demo`（辞退→AIの確認→再計画→同意→確定を実演する）と `initial_demo`（開催回登録の直後）。デモ用の利用者 A〜D の Discord ID は `100000000000000001`〜`…004`（架空）。

### テスト

- `make test`：全テスト（各パッケージの単体テストと結合テスト）
- `make test-e2e`：HTTP 経由の結合テストだけ（`src/backend/tests/e2e/`。評価ケース E01〜E12 と目次の取得を、外部サービスを偽物に差し替えて確認する）

その他のコマンド：`make fmt`（整形）、`make vet`（静的検査）、`make db-reset`（ローカル DB の削除）

依存ライブラリ：`modernc.org/sqlite`（SQLite）、`golang.org/x/text`（目次の照合での Unicode 正規化）、`github.com/bwmarrin/discordgo`（DM の受信とボタン）。LLM の呼び出しと通知の送信は標準ライブラリの HTTP クライアントで行う。

## 提出物

- デモ動画：準備中
- 記事：準備中
