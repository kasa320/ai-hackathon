# 担当承認の二重化解消とホームの状態別一覧

決定日：2026-09-22。ブランチ：`feat/session-overview-consistency`（並列開発の3本目、`.agent/kasa/decisions/discord-availability-home-refresh.md` の担当。worktree：`/tmp/ai-hackathon-session-overview`）。

## 対象・単一デモ・承認地点

- 対象：ブック由来の実セッションで担当を確認する参加者、複数ブックの進行状況をホームで確認するグループ参加者。
- 単一デモ：ブックの担当承認（`reading_book_slots.assignment_status`）が済んだ回で、実セッションの案に対して同じ担当へ重複した「引き受ける」を求めない。ホームでは最上部の1件はそのままに、その下に全所属グループの未完了book slotを状態別に一覧する。
- 承認地点：担当の引き受けは従来どおり本人の明示操作のみ。無効化した重複タスクへの回答は拒否し、無効化そのものを本人の承認として記録しない。

## 決定

### 担当承認の唯一のsource of truth

- `SessionData.AssigneeMemberID` が設定されているセッションでは、`ApprovalRequirements` が `RequiredAcceptorIDs` を空にする（`src/backend/internal/playbook/reading/rules.go`）。`ValidatePlan` は従来どおり発表者をこの担当に固定する検証を維持する。
- `AssigneeMemberID` がない旧方式・手動セッションは、従来どおり本人の `assignment` タスクを作る（`rules_test.go` に両ケースを追加）。

### 既存の重複タスクの解消

- 修正前に作られた open な `assignment` タスクは、`ProcessDue` の中で毎回状態から解消する（`src/backend/internal/coord/book_reconcile.go` の `reconcileBookAssignmentTasks`）。対象は「セッションが reading で `AssigneeMemberID` が設定されており、その値がタスクの `member_id` と一致する open な `assignment` タスク」。
  - 案（`Proposal.Requirements.acceptors`）からそのメンバーを取り除き、タスクを `obsolete` にし、参照するイベントを取り消し、活動記録を残してから `evaluate` を呼び直す。これにより、重複タスクが原因で確定できずにいた案が、残りの条件（全員同意など）だけで確定できる。
  - 担当を勝手に承認した扱いにはしない：`reading_book_slots.assignment_status` は変更しない。無効化済みタスクへの回答は、案が既に確定していれば `proposal_superseded`、確定前でも `task_closed` 相当で拒否される（`RespondTask` の既存の分岐をそのまま使う）。
- 一括の一回限りマイグレーションではなく `ProcessDue` の毎回実行にした。状態から導くため再実行しても安全（冪等）で、起動時の別ステップを増やさない。既存の自動進行（`processBookWork`）と同じ設計方針。

### ホームの状態別一覧

- `src/frontend/js/index.js` に、全所属グループの book slot（実セッション作成前を含む）を集めて次の優先順で1つのバケツに分類する処理を追加（重複なし）：自分の回答待り → 要確認・管理者判断待ち → 日程調整中 → 日時確定・実施待ち → 調整開始待ち。完了済み枠は除外。
- 分類は `src/frontend/js/features/reading/planning.js` の `assignmentState`/`schedulingState`/`slotAwaitsMyConfirmation` をそのまま再利用し、フロントの状態値マッピングを一箇所に保つ既存方針を維持した。`SCHEDULING_KIND` をエクスポートに追加。
- 専用dashboard APIは追加せず、`api.books(groupId)` → `api.book(groupId, bookId)` を集約する。取得は `mapLimited`（同時実行数4）で行い、黙って件数を切り詰めない。既存の「セッション詳細は最大8件」という制限（`index.js` の旧コード）も同じ理由で撤廃した。
- 実セッションが既にある枠は、下の「ほかのセッション」一覧から除外する（`renderList` に `coveredSessionIds` を渡す）ことで、実セッションとbook slotの二重表示を避けた。
- 取得の部分失敗（グループのブック一覧・ブック詳細）は空一覧に見せず、「一部を取得できませんでした」の通知で対象を明示する。

## 却下した選択肢

- **専用の「ホームダッシュボード」APIを新設する**：仕様の「専用dashboard APIは追加しない」に反する。既存3種のAPI（groups/books/book detail）で必要なフィールド（`assignment_status`・`scheduling_status`・`assignee_confirmation_status`・`session`）が揃っており、集約で足りると判断した。
- **重複タスクの解消を一回限りのDBマイグレーションスクリプトにする**：デモ環境の再起動・時計操作や再処理で新たに古い状態が再発しても追従できない。状態から導く冪等な処理のほうが、このリポジトリの他の自動進行（ブックの計画・調整開始）と設計が揃う。
- **無効化した重複タスクを本人の承認として記録する（`accept` 相当にする）**：担当を勝手に承認した扱いになり、「本人の明示操作でのみ同意・引き受けを記録する」という固定方針（仕様書第4節）に反する。単に不要（obsolete）として片付け、担当承認自体は `reading_book_slots.assignment_status` に一本化した。
- **ホーム最上部の1件と下の状態別一覧を完全に排他にする（同じ枠を二重に見せない）**：仕様は「上部の1件は維持」「その下に全未完了枠」としており、上下の役割が異なる（強調 vs 一覧）ため、同じ枠が両方に出ることは二重表示とは扱わない。二重表示として明示的に禁止されているのは「実セッションとbook slot」の重複だけ。

## 検証と限界

- 実施：`go build -buildvcs=false ./...`、`go vet ./...`、`go test ./...`、`go test -race ./internal/coord/... ./internal/store/... ./tests/e2e/...`（すべて成功）。フロントは `node --check` によるJS構文確認のみ（本リポジトリにJSテスト基盤が無いため、`docs.md` に記載のとおり手動確認が必要）。
- 未実施：実ブラウザでのホーム画面の目視確認、実データでのレスポンシブ・キーボード操作の確認。
- 限界：`go build`（`-buildvcs=false` なし）はこのリンクされたworktree環境ではVCS情報取得に失敗する（`error obtaining VCS status: exit status 128`）。これは本worktree構成に起因する既知の制約（`.agent/kasa/workflows/parallel-worktree-development.md` に記載済み）で、今回の変更が原因ではない。通常のクローンでは発生しない。
