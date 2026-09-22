# Agent 2：普段の空き時間・AI下書き・開催回例外を統一する

## 作業場所

- worktree：`/tmp/ai-hackathon-availability-drafts`
- branch：`feat/availability-drafts`
- 共通基点：`f53c740` 以降のプロンプト同期コミット
- このworktree以外を編集しない。他ブランチのマージ、worktreeの削除、`master`への切替は行わない。

## 最初に読むもの

1. ルートの `AGENTS.md`
2. `.agent/decisions/spec.md`
3. `.agent/decisions/plan.md`
4. `.agent/kasa/decisions/discord-availability-home-refresh.md`（今回の確定仕様）
5. `.agent/kasa/decisions/group-kind-and-book-planning.md`
6. `.agent/kasa/decisions/scheduling-input-boundary.md`
7. `.agent/kasa/workflows/parallel-worktree-development.md`

## 目的とデモフロー

普段の空き時間をWebで文章から下書きにでき、本人が確認して保存できるようにする。開催回の参加条件では登録済みの普段の空き時間を表示し、編集画面へのリンクを置き、今回だけ参加できない日・今回だけ参加できる日時を追加できるようにする。開催回で週間予定を明示的に入力・変更した場合は、その内容で普段の空き時間を自動的に全置換する。

## 固定契約

- 週間解釈APIは `POST /api/me/weekly-availability/interpretations`。入力・出力は `.agent/kasa/decisions/discord-availability-home-refresh.md` の形から変更しない。このエンドポイントのバックエンドはDiscord担当が実装するため、このブランチではフロントから利用する。競合回避のため同じバックエンド解釈器を別実装しない。
- 解釈結果はフォームへ反映するだけで保存しない。本人が既存PUTの保存ボタンを押して初めて全置換する。原文をDB・ログへ保存しない。
- 開催回の保存入力に省略可能な `update_weekly_availability`（既定false）を追加する。直接更新とtaskの `decision=submit` の両方で同じ意味にする。
- `true` は本人が週間曜日・時間帯を明示的に入力または変更し、確認画面にも更新を表示した場合だけ送る。有効な週間枠がない `true` は422。保存時は参加条件と同じトランザクションで、`Asia/Tokyo` の普段の空き時間を全置換する。失敗時に片方だけ保存しない。
- 既存の普段の空き時間を表示しただけ、今回だけの例外を変えただけ、`unknown` を選んだだけでは普段の空き時間を更新しない。
- `schedule` が省略または `status=unknown` でも、本人が参加すると明示していれば登録済みの普段の空き時間を候補計算に使う。未回答を参加可能扱いにはしない。
- 今回だけ参加できない日は既存 `unavailable_dates`（終日）、今回だけ参加できる日時は既存 `date_windows` を利用する。特定日の時間帯はその日の週間設定を置換する。曜日番号の違い（sessionは0=日、weeklyは1=月〜7=日）を境界で明示変換する。
- 参加条件の自由文placeholderを現仕様に合わせる。例：「参加します。普段は水曜と金曜の19時〜22時が空いています。10月7日は参加できません。」準備状況や説明時間は含めない。

## 主な変更可能領域

- `src/frontend/js/weekly.js`、`weeklyAvailability.js`、`session.js`
- `src/frontend/js/features/reading/preparation.js`、`availability.js`
- `src/frontend/js/api.js` の確定済みAPI呼出と保存フラグ
- `src/frontend/css/styles.css` の必要最小限
- `src/backend/internal/apitypes/types.go` の `update_weekly_availability`
- `src/backend/internal/coord/mutations.go`、`weekly_availability.go`、空き時間合成処理
- 用途別の抽出interface実装が必要なら新規ファイル（ただしDiscord担当の週間解釈APIと重複しない）
- 関連するcoord/playbook/store/E2Eテスト

## 変更禁止・競合回避

- `src/backend/internal/discord/**`、`internal/notify/**`、通知actionスキーマは変更しない。
- `src/frontend/js/index.js`、`book.js` は変更しない。
- `internal/playbook/reading/rules.go` の担当承認要件は変更しない。空き時間の規則変更は可能なら `availability.go` や新規ファイルに隔離する。
- 週間解釈APIのURL・JSON形を独自に変更しない。不足があれば最終報告に記載する。
- `docs/` は更新しない。必要な判断記録は `.agent/kasa/decisions/` に置く。

## UI要件

- `/weekly.html` に「文章から下書きを作る」を追加し、処理中・抽出結果・未確定項目・エラーを表示する。下書き後も曜日時間フォームで修正できる。
- セッション参加条件には普段の空き時間を読みやすく表示し、「普段の空き時間を編集」リンクを置く。
- 今回だけの例外を、普段の空き時間と混同しない見出し・説明にする。
- 週間予定を変更する操作では「普段の空き時間も全置換されます」と保存前に明示する。
- 取得失敗を「未登録」として表示しない。入力中の値を通信失敗で失わない。

## 必須テスト

- `unknown` / schedule省略でも登録済み普段枠を使用し、今回だけ不可日と特別可日時を正しく合成する。
- 明示的な週間変更だけがglobalを全置換する。フラグfalse、欠席、無効な週間枠、競合、トランザクション失敗で誤更新しない。
- task submitと直接更新が同じ結果になる。曜日変換、timezone、日付置換、複数ブック/セッションでもユーザー単位の値を使う。
- 週間下書きは保存前にPUTされず、保存後だけ反映されること（バックエンドAPI自体のテストはDiscord担当。ここではUI契約と統合後のE2Eを想定）。
- フロントJS構文確認、全Goテスト。高リスクなCoordinator/E2Eはraceも実施。

## 完了条件

- 変更を目的別の小さなConventional Commitへ分ける。
- `make ORCAROUTER_PLANNER_MODEL= ORCAROUTER_INTERPRETER_MODEL= ORCAROUTER_SEARCH_MODEL= ORCAROUTER_VISION_MODEL= test vet build` 相当を通す。
- worktreeをcleanにする。
- 最終報告に、コミットSHA、変更ファイル、テスト結果、未完了、Discord担当との統合点・契約差分を記載する。

