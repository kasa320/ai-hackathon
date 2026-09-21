# Codex並列開発runbook

複数のCodexセッションへ実装を分担し、Git worktreeで安全に統合するための手順。今回のブック計画機能の並列開発で実施した流れを一般化した。

## 使う場面

- 2つ以上の作業を、ファイル所有範囲を分けて同時に進められる。
- 先に公開APIや共有型を固定できる。
- 各作業を独立してテスト・コミットできる。

共有データモデルが未確定、または同じファイルを複数担当が大きく変更する場合は、先に仕様・API契約を確定する。機能名だけで分けず、`目次`、`バックエンド`、`フロントエンド`のように所有ファイルの境界で分ける。

## 1. 開始前の確認

1. ルートの `AGENTS.md` と対象仕様を読む。
2. 基点ブランチの未コミット変更を確認する。
3. 基点コミットを全担当で揃える。
4. 対象ユーザー、デモフロー、外部連携、承認地点、成功指標を明文化する。
5. 共有APIについて、URL、入力、出力、状態値、権限、エラーを先に固定する。
6. 各担当の変更可能・変更禁止ファイルを決める。

確認例:

```bash
git status --short --branch
git worktree list
git log -1 --oneline
```

## 2. worktreeとブランチ

作業ごとに専用ブランチと `/tmp` 配下のworktreeを作る。

```bash
git worktree add -b feat/<task-a> /tmp/<repo>-<task-a> HEAD
git worktree add -b feat/<task-b> /tmp/<repo>-<task-b> HEAD
git worktree add -b feat/<task-c> /tmp/<repo>-<task-c> HEAD
```

既存パス・既存ブランチと衝突しないことを先に確認する。別作業のworktreeは削除しない。

## 3. 各Codexセッションへ渡すプロンプト

各プロンプトに必ず含める。

- 絶対パスのworktreeとブランチ名
- 最初に読む `AGENTS.md` と仕様
- 担当する目的と利用者フロー
- 変更可能・変更禁止の領域
- 固定済みAPI契約と状態値
- 認証、承認、冪等性、失敗時の要件
- 必須テスト
- 小さなConventional Commitに分けること
- 最終報告にコミットSHA、変更ファイル、テスト、未完了事項、契約差分を含めること

フロントとバックエンドを並行実装する場合、同じAPI契約を双方のプロンプトへコピーする。バックエンド未完成を理由に、フロントが固定データへ黙ってフォールバックしないよう明記する。

## 4. 完了後のレビュー

各worktreeを変更せずにレビューする。可能なら担当ごとにレビュー用エージェントを分ける。

```bash
git -C /tmp/<worktree> status --short --branch
git -C /tmp/<worktree> log --oneline <base>..HEAD
git -C /tmp/<worktree> diff --check <base>...HEAD
git -C /tmp/<worktree> diff --stat <base>...HEAD
```

確認順:

1. 未コミット変更がないか。
2. 要件を満たすか。
3. APIのURL、フィールド名、状態値が一致するか。
4. 権限・本人同意・未回答の扱いが安全か。
5. DBマイグレーションが既存DBで動くか。
6. 再試行・再起動で重複しないか。
7. 単体テスト、E2E、静的検査、ビルドが通るか。

## 5. 統合

`master`へ直接統合せず、専用の統合ブランチを作る。

```bash
git switch -c integrate/<topic>
git merge --no-ff feat/<task-a> -m "merge: integrate <task-a>"
git merge --no-ff feat/<task-b> -m "merge: integrate <task-b>"
git merge --no-ff feat/<task-c> -m "merge: integrate <task-c>"
```

依存の小さいもの、バックエンド契約、フロントエンドの順を基本とする。マージできたことを完成条件にせず、統合後に契約差分を再確認する。レビュー修正は、元の作業ブランチを書き換えず統合ブランチへ目的別にコミットする。

このリポジトリの標準検証:

```bash
make ORCAROUTER_PLANNER_MODEL= ORCAROUTER_INTERPRETER_MODEL= ORCAROUTER_SEARCH_MODEL= ORCAROUTER_VISION_MODEL= test
make ORCAROUTER_PLANNER_MODEL= ORCAROUTER_INTERPRETER_MODEL= ORCAROUTER_SEARCH_MODEL= ORCAROUTER_VISION_MODEL= vet
make ORCAROUTER_PLANNER_MODEL= ORCAROUTER_INTERPRETER_MODEL= ORCAROUTER_SEARCH_MODEL= ORCAROUTER_VISION_MODEL= build
```

高リスクなDB・自動進行変更では、対象パッケージとE2Eへ `go test -race` も実行する。linked worktreeでGoのVCS情報取得だけが失敗する場合は、原因を区別するため `go build -buildvcs=false` でも確認する。

## 6. cleanup

統合ブランチがクリーンで、各作業コミットが統合済みであることを確認してから削除する。

```bash
git worktree remove /tmp/<repo>-<task-a>
git worktree remove /tmp/<repo>-<task-b>
git worktree remove /tmp/<repo>-<task-c>
git branch -d feat/<task-a> feat/<task-b> feat/<task-c>
git worktree list
git status --short --branch
```

削除対象は今回作成したものを絶対パスとブランチ名で列挙する。無関係なworktree、統合ブランチ、`master`は削除しない。

## Skillへ昇格する基準

このrunbookはリポジトリ固有の正本とする。別リポジトリでも繰り返す場合は、次の処理だけを行う薄いSkillを作る。

- リポジトリの `AGENTS.md` とrunbookを読む。
- 基点、分割案、共有契約、競合リスクを提示する。
- 承認された名前でworktreeを作る。
- セッション別プロンプトを生成する。
- 完了後にレビュー、統合、検証、cleanupを行う。

Skillへプロジェクト固有のAPIやテスト値を直接埋め込まず、各リポジトリのrunbookから読む。これにより手順の再利用性と、リポジトリ固有ルールの追跡性を分ける。
