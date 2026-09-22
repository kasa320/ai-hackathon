# Agent 1：DiscordをWeb UIと同じ回答入口にする

## 作業場所

- worktree：`/tmp/ai-hackathon-discord-ui-parity`
- branch：`feat/discord-ui-parity`
- 共通基点：`f53c740` 以降のプロンプト同期コミット
- このworktree以外を編集しない。他ブランチのマージ、worktreeの削除、`master`への切替は行わない。

## 最初に読むもの

1. ルートの `AGENTS.md`
2. `.agent/decisions/spec.md`
3. `.agent/decisions/plan.md`
4. `.agent/kasa/decisions/discord-availability-home-refresh.md`（今回の確定仕様）
5. `.agent/kasa/decisions/discord-interactive-agent.md`
6. `.agent/kasa/workflows/parallel-worktree-development.md`

## 目的とデモフロー

Discordの個人通知をWeb UIの代替入口にする。参加者は通知後に「回答」「参加条件」と送り直さず、通知のボタンから対象に束縛された回答を開始できる。参加条件は「対象 → 参加可否（ボタン）→ 日程条件と今回だけの例外（ここだけ自然文をLLM抽出）→ 構造化確認 → 本人が保存」の順にする。案への同意、旧方式セッションの担当引受、ブック計画の担当承認・交代候補、開催3日前の担当確認、普段の空き時間の登録・再確認もDiscordから本人が回答できる。管理者の作成・編集・削除はWebに残す。

## 固定契約

- 選択式回答、同意、担当承認、保存にはLLMを使わない。自由文をLLMへ渡すのは日時条件の抽出だけ。
- 「分からない」「最初から」「やり直し」は未保存値を破棄して同じ対象の最初から即再開。「中断」「あとで」「キャンセル」は未保存値を破棄して終了する。
- 通知からの操作は、通知作成時に対象を明示的に束縛する。ボタンはopaqueな参照だけを持ち、押下時にDiscord操作者→user、所属、対象、期限、状態、許可されたdecisionをCoordinatorで再検証する。本文・生のmember ID・custom IDを認可根拠にしない。
- action参照は永続化し、Gateway再送・二重押下・再起動で二重回答しない。期限切れ・状態変更済みのボタンは安全に拒否し、最新状態またはWebリンクを案内する。
- `POST /api/me/weekly-availability/interpretations` の入出力は確定仕様どおり。この担当でバックエンドの型・Coordinator解釈・API・厳格な検証・LLM費用記録を実装する。解釈だけでは保存しない。
- 開催回回答の `update_weekly_availability` は別担当が保存処理を実装する。このブランチではDiscordの確認状態に値を保持し、週間予定を本人が明示したとき `true` を通常の `PutPreparation` / task submitへ渡せる契約にする。基点にフィールドがなければ最小限の型追加は可。ただし保存規則そのものは変更しない。
- 週間空き時間のDiscord保存は、解釈結果を表示して本人の保存ボタンを経由し、既存 `PutWeeklyAvailability` を呼ぶ。
- Webの公開JSON契約や既存のWeb操作を壊さない。Discordの発話原文・未保存下書きをDBやアプリログへ保存しない。

## 主な変更可能領域

- `src/backend/internal/discord/**`
- `src/backend/internal/notify/**`
- `src/backend/internal/coord/dialog.go`、`dialog_test.go`、通知とDiscord actionのための新規ファイル
- `src/backend/internal/coord/flow.go` の通知文・action接続
- `src/backend/internal/coord/book_plan.go`、`book_auto.go` の個人通知action接続（業務状態遷移そのものは変えない）
- `src/backend/internal/store` の通知action永続化に必要な追加スキーマ・専用ファイル
- 週間空き時間解釈専用の `agent` / `coord` / `apitypes` / `api` 新規実装
- 関連する単体・E2Eテスト

## 変更禁止・競合回避

- `src/frontend/**` は変更しない。
- `src/frontend/js/index.js`、`book.js`、`session.js`、`weekly.js` は変更しない。
- 普段の空き時間と開催回例外の合成、参加条件保存時の自動全置換は実装しない。
- `internal/playbook/reading/rules.go` の担当承認規則は変更しない。
- `docs/` は更新しない。仕様追加の理由は必要なら `.agent/kasa/decisions/` に残す。
- 境界外の変更が必要なら独自の代替契約を作らず、最終報告の「統合時に必要なこと」へ明記する。

## 必須テスト

- Discord通知からコマンドなしで各回答を開始できる。
- UI順の参加条件質問、選択回答でLLM未呼出、日時だけLLM呼出、確認前に未保存。
- やり直しと中断の差、複数対象、古いボタン、他人のボタン、期限切れ、二重押下、Gateway再送、再起動後の挙動。
- session task、book assignment、交代候補、直前確認、週間空き時間の各actionが通常Coordinator操作と同じ結果になる。
- 週間解釈APIの認証、未知フィールド、不正・重複時間、プロンプトインジェクション、費用上限、下書き非保存。
- 通知送信のDM/fallback/unknown/failedと、componentsを含む送信。
- `go test -race` を少なくとも `internal/discord`、`internal/notify`、変更したCoordinator、関連E2Eへ実施。

## 完了条件

- 変更を目的別の小さなConventional Commitへ分ける。
- `make ORCAROUTER_PLANNER_MODEL= ORCAROUTER_INTERPRETER_MODEL= ORCAROUTER_SEARCH_MODEL= ORCAROUTER_VISION_MODEL= test vet build` 相当を通す（必要なら個別実行）。
- worktreeをcleanにする。
- 最終報告に、コミットSHA、変更ファイル、テスト結果、未完了、統合時の契約差分・競合候補を記載する。

