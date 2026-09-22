# Agent 3：担当承認の二重化を解消し、ホームを状態別一覧にする

## 作業場所

- worktree：`/tmp/ai-hackathon-session-overview`
- branch：`feat/session-overview-consistency`
- 共通基点：`f53c740` 以降のプロンプト同期コミット
- このworktree以外を編集しない。他ブランチのマージ、worktreeの削除、`master`への切替は行わない。

## 最初に読むもの

1. ルートの `AGENTS.md`
2. `.agent/decisions/spec.md`
3. `.agent/decisions/plan.md`
4. `.agent/kasa/decisions/discord-availability-home-refresh.md`（今回の確定仕様）
5. `.agent/kasa/decisions/group-kind-and-book-planning.md`
6. `.agent/kasa/workflows/parallel-worktree-development.md`

## 目的とデモフロー

ブック計画で担当を本人承認済みなのに、実セッション詳細でも同じ担当の「引き受ける」を要求し、ブック画面と状態が食い違う問題を解消する。またホームの強調表示1件は維持しつつ、同じページで未完了の全ブック枠を状態別に確認できるようにする。

## 担当承認の固定仕様

- `reading_book_slots.assignment_status` をブック由来セッションの担当承認の唯一のsource of truthにする。
- `SessionData.AssigneeMemberID` が設定されたブック由来セッションでは、proposalの担当者がその固定担当と一致することを従来どおり検証する一方、session proposal用の `RequiredAcceptorIDs` は空にし、重複した `assignment` taskを新規作成しない。
- `AssigneeMemberID` がない旧方式・手動セッションでは従来どおり担当本人のassignment taskを作る。
- 既存DBですでに発行済みの重複assignment taskも安全に解消する。担当を勝手に承認した扱いにはせず、「ブック側ですでに本人承認済みのため不要になったタスク」として無効化し、proposalの残りの同意要件を再評価する。古いボタンへの回答は拒否する。
- ブック側の「引き受ける」は初期担当がpending、または本人が交代候補のchange_proposedの場合だけ表示する。開催3日前の「このまま担当する」は別概念として維持する。

## ホーム一覧の固定仕様

- 一番上の「いま自分が回答する1件」は維持する。
- その下に、全所属グループの未完了book slotを重複なく次の優先順で分類する：
  1. 自分の回答待ち
  2. 要確認・管理者判断待ち
  3. 日程調整中
  4. 日時確定・実施待ち
  5. 調整開始待ち
- 実セッション作成前のslotも「調整開始待ち」に含める。完了済みは除外する。実セッションとbook slotを二重表示しない。
- 各行にグループ、ブック、第N回、対象章、担当、開催目安または確定日時、状態、遷移先を可能な範囲で表示する。
- 専用dashboard APIは追加せず、既存groups/books/book detail/sessions APIを集約する。取得数を8件に黙って制限しない。無制限な同時fetchを避け、部分失敗は空一覧に見せず該当グループ/ブックを取得できなかった旨を表示する。
- レスポンシブ表示、キーボード操作、状態を色だけで表さない点を既存UIに合わせる。

## 主な変更可能領域

- `src/backend/internal/playbook/reading/rules.go`、`rules_test.go`
- `src/backend/internal/coord/book_auto_test.go`、必要な重複task reconciliationのCoordinator新規/既存ファイル
- `src/frontend/js/index.js`
- `src/frontend/js/book.js` は表示条件の回帰修正が必要な場合のみ
- `src/frontend/js/features/reading/planning.js`
- `src/frontend/css/styles.css` の必要最小限
- 関連する単体・E2Eテスト

## 変更禁止・競合回避

- `src/backend/internal/discord/**`、`internal/notify/**` は変更しない。
- 週間空き時間、参加条件フォーム、`session.js`、`weekly.js`、週間解釈APIは変更しない。
- 公開APIを増やさない。既存レスポンスで不足する場合は、まず既存book/session viewへの後方互換な最小追加を検討し、独自dashboard APIは作らない。
- `docs/` は更新しない。必要な判断記録は `.agent/kasa/decisions/` に置く。

## 必須テスト

- 固定担当ありの初回案・再計画でsession assignment taskがなく、全員同意など残りの要件だけで確定する。
- 固定担当なしのlegacy sessionではassignment taskを維持する。
- 既存の重複open taskが無効化され、ブック担当状態はacceptedのまま、古い回答は拒否される。
- 新規ブック→担当承認→時計進行→実セッション→参加条件→案作成の一連で、担当承認を一度しか求めない。
- ホーム分類に各状態が入り、完了除外、重複排除、実セッション前slot、部分失敗、0件を確認する。
- フロントJS構文、全Goテスト、担当タスクのCoordinator/E2Eはraceも実施。

## 完了条件

- 変更を目的別の小さなConventional Commitへ分ける。
- `make ORCAROUTER_PLANNER_MODEL= ORCAROUTER_INTERPRETER_MODEL= ORCAROUTER_SEARCH_MODEL= ORCAROUTER_VISION_MODEL= test vet build` 相当を通す。
- worktreeをcleanにする。
- 最終報告に、コミットSHA、変更ファイル、テスト結果、未完了、統合時の契約差分・競合候補を記載する。
