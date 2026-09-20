# データベース

SQLite 1ファイル（既定 `data/app.db`、`DB_PATH` で変更可）。定義は `src/backend/internal/store/migrations/` に版ごとに置き、起動時に未適用の版だけが順に適用される（下記「スキーマの変更」）。

- 日時はすべて UTC の RFC 3339（ナノ秒付き）文字列。
- 用途固有のデータは JSON 文字列のカラムに入れ、共通側は中身を解釈しない。輪読用の型は `src/backend/internal/playbook/reading/model.go`。
- ID は `<接頭辞>_<24桁の16進>`。

サンプルは `POST /api/dev/seed`（`replan_demo`）で投入した実際のレコード。

---

## スキーマの変更

テーブル定義は `src/backend/internal/store/migrations/<4桁の版番号>_<説明>.sql` に置く。実行ファイルに同梱され、`store.Open` が**適用済みの版より新しいものだけ**を昇順で適用する。適用済みの版番号は `meta` テーブルの `schema_version` に入る。

**既存のファイルは書き換えない。** 変更は新しい版を足して表す。

```sh
# 例：members に退会日時の列を足す
cat > src/backend/internal/store/migrations/0002_add_member_left_at.sql <<'SQL'
ALTER TABLE members ADD COLUMN left_at TEXT;
SQL
make dev   # 次の起動で自動的に適用される
```

### 決まりごと

| 項目 | 内容 |
| --- | --- |
| ファイル名 | `<4桁の版番号>_<説明>.sql`。番号は 1 から連番（抜けがあるとテストが落ちる） |
| 適用の単位 | 1つの版＝1トランザクション。版番号の記録も同じトランザクション内 |
| 失敗したとき | その版の変更だけ巻き戻り、版番号も上がらない。直して起動し直せば同じ版からやり直せる |
| 巻き戻し用のSQL | 持たない。失敗した版は直して入れ直す。手元をやり直すなら `make db-reset` |
| 版が新しすぎるDB | プログラムが知らない版まで進んだDBは、開かずにエラーにする |

### 列の削除・型変更・制約変更

SQLite には該当する `ALTER TABLE` がなく、新しい表へ移し替えて改名する手順になる。この場合は**ファイルの先頭行**に `-- foreign_keys: off` と書く。適用中だけ外部キー検査を止め、コミット直前に `PRAGMA foreign_key_check` で参照を失った行がないかを確かめ、あれば巻き戻す。

```sql
-- foreign_keys: off
CREATE TABLE members_new (...);
INSERT INTO members_new SELECT ... FROM members;
DROP TABLE members;
ALTER TABLE members_new RENAME TO members;
CREATE INDEX IF NOT EXISTS members_user ON members(user_id);  -- 索引は作り直す
```

### 0001 だけの前提

`0001_init.sql` はマイグレーション導入前の `schema.sql` をそのまま移したもので、中身が全部 `CREATE ... IF NOT EXISTS` になっている。導入前に作った `data/app.db` に対しては**何も起こさずに版番号だけが 1 になる**ため、既存のDBを作り直さずに引き継げる。この冪等性は 0001 に限った前提で、0002 以降は満たさなくてよい。

---

## users

Discord でログインした利用者。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | アプリ内のユーザーID | `usr_6cab143281b58c82e75b4ccd` |
| `discord_user_id` | Discord のユーザーID（UNIQUE） | `100000000000000001` |
| `display_name` | Discord の表示名 | `A` |
| `created_at` | 初回ログイン日時 | `2026-09-20T06:28:09.264668603Z` |

## auth_sessions

ログインセッション。Cookie の値そのものは保存しない。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `token_hash` | Cookie のトークンのハッシュ（主キー） | `05115dae997fc784a50983196cb7f4df...` |
| `user_id` | 本人 | `usr_e3ee07baf9fcb7b54c3e4e7f` |
| `csrf_token` | `X-CSRF-Token` と照合する値 | `9c8281f112d6f092dd3d5e6d91bbb301...` |
| `created_at` | 発行日時 | `2026-09-20T06:28:15.578685980Z` |
| `expires_at` | 期限（発行から7日） | `2026-09-27T06:28:15.578685980Z` |

## oauth_states

OAuth の state。1回使うと削除する。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `state` | 認可リクエストに付けた乱数 | `68d7426cad809edf191b0889aadeda3a...` |
| `return_to` | ログイン後の戻り先（同一サイトの相対パスのみ） | `/session.html?id=ses_eaba7ee95b34d9cb3fbffcb5` |
| `expires_at` | 期限（10分） | `2026-09-20T06:39:08.493752191Z` |

## groups

輪読グループ。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | グループID | `grp_c87e49a07149914a2946e5bd` |
| `name` | グループ名 | `技術書輪読（デモ）` |
| `owner_user_id` | 作成者（管理者） | `usr_6cab143281b58c82e75b4ccd` |
| `created_at` | 作成日時 | `2026-09-20T06:28:09.265000000Z` |

## members

グループの固定メンバー。`UNIQUE(group_id, discord_user_id)`。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | メンバーID（計画・タスクはこのIDで人を指す） | `mem_1a9ffe26d9a1f00b1e8c6c98` |
| `group_id` | 所属グループ | `grp_c87e49a07149914a2946e5bd` |
| `discord_user_id` | 招待時に登録した Discord ID | `100000000000000001` |
| `user_id` | 本人のユーザーID。**NULL の間は招待中（本人未ログイン）** | `usr_6cab143281b58c82e75b4ccd` |
| `display_name` | 表示名（本人ログイン後は本人の名前に置き換わる） | `A` |
| `role` | `owner` / `member` | `owner` |
| `seq` | グループ内の並び順 | `0` |

## sessions

開催回。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | 開催回ID | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `group_id` | 所属グループ | `grp_c87e49a07149914a2946e5bd` |
| `playbook_id` | 用途 | `reading` |
| `title` | 回の名前 | `第2回` |
| `starts_at` | 開始日時 | `2026-09-23T06:00:00.000000000Z` |
| `duration_minutes` | 持ち時間 | `60` |
| `revision` | 更新のたびに増える版番号（楽観ロックに使う） | `8` |
| `status` | `draft` / `confirmed` / `needs_attention` | `needs_attention` |
| `data` | 用途固有データ（下記 JSON） | — |
| `confirmed_proposal_id` | 確定済みの案 | `prop_48f618bac6748e0642a49b49` |
| `created_at` / `updated_at` | 作成・更新日時 | `2026-09-20T06:28:21.899000000Z` |

`data`（輪読）：

```json
{
  "book_title": "サンプル技術書",
  "isbn": null,
  "toc_source": {"kind": "manual", "urls": []},
  "sections": [
    {"id": "sec_1", "title": "前回の範囲"},
    {"id": "sec_2", "title": "今回の前半"},
    {"id": "sec_3", "title": "今回の後半"}
  ],
  "completed_section_ids": ["sec_1"],
  "target_section_ids": ["sec_2", "sec_3"]
}
```

## session_members

開催回の時点で固定した参加対象者。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `session_id` | 開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `member_id` | メンバー | `mem_1a9ffe26d9a1f00b1e8c6c98` |

## preparations

参加条件。**行が無ければ未回答**（未回答を賛成として扱わない）。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `session_id` | 開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `member_id` | 回答者 | `mem_1a9ffe26d9a1f00b1e8c6c98` |
| `attendance` | `attending` / `absent` | `attending` |
| `data` | 用途固有の準備状況（下記 JSON） | — |
| `updated_at` | 更新日時 | `2026-09-20T06:28:09.268000000Z` |

`data`（輪読）：

```json
{
  "willing_to_present": false,
  "prepared_section_ids": ["sec_1"],
  "explainable_section_ids": [],
  "max_presentation_minutes": 0
}
```

## cases

調整案件。1つの開催回に未完了の案件は最大1つ。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | 案件ID | `case_52e00dd5202cf4c3cd0f1e73` |
| `session_id` | 対象の開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `status` | `collecting` / `planning` / `awaiting_consent` / `confirmed` / `needs_owner` | `confirmed` |
| `reason_code` | 管理者へ戻した理由（`no_feasible_plan` / `deadline_expired` / `model_error` / `budget_exceeded`） | `null` |
| `summary` | 現状の説明（画面に出す文） | `計画が確定しました（版1）。` |
| `next_retry_at` | 次の再試行時刻 | `null` |
| `retry_count` | 再試行回数 | `0` |
| `llm_call_count` | 案件累計の LLM 呼び出し回数（上限判定に使う） | `0` |
| `tool_call_count` | 案件累計のツール実行回数 | `1` |
| `withdrawn_member_ids` | 辞退した人のメンバーID配列（JSON） | `[]` |
| `created_at` / `updated_at` | 作成・更新日時 | `2026-09-20T06:28:09.275000000Z` |
| `seq` | 開催回内の連番 | `1` |

## proposals

版付きの案。`UNIQUE(session_id, version)`。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | 案ID | `prop_48f618bac6748e0642a49b49` |
| `session_id` | 開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `case_id` | 案件 | `case_52e00dd5202cf4c3cd0f1e73` |
| `version` | 版番号。内容が変わると新しい版になる | `1` |
| `status` | `pending` / `confirmed` / `superseded` / `rejected` | `confirmed` |
| `change_kind` | `initial`（初回計画） / `replan`（再計画） | `initial` |
| `author` | `agent` / `owner` | `agent` |
| `summary` | 案の説明文 | `Bさんが「今回の前半」を13分で説明、…` |
| `data` | 用途固有の計画（下記 JSON） | — |
| `requirements` | **作成時に固定した**引き受け・承認条件（下記 JSON） | — |
| `revision` | 作成時点の開催回の版 | `6` |
| `created_at` | 作成日時 | `2026-09-20T06:28:09.273000000Z` |
| `confirmed_at` | 確定日時 | `2026-09-20T06:28:09.275000000Z` |

`data`（輪読）：

```json
{
  "covered_section_ids": ["sec_2", "sec_3"],
  "deferred_section_ids": [],
  "agenda": [
    {"id": "item_1", "activity": "presentation", "section_ids": ["sec_2"], "presenter_member_id": "mem_55fbbd7eb48f48dcc78f3c85", "minutes": 13},
    {"id": "item_3", "activity": "review", "section_ids": ["sec_1"], "presenter_member_id": null, "minutes": 20},
    {"id": "item_4", "activity": "discussion", "section_ids": ["sec_2", "sec_3"], "presenter_member_id": null, "minutes": 14}
  ]
}
```

`requirements`：

```json
{
  "acceptors": ["mem_55fbbd7eb48f48dcc78f3c85"],
  "approvals": [{"kind": "owner", "eligible": ["mem_1a9ffe26d9a1f00b1e8c6c98"], "required": 1}]
}
```

## tasks

本人宛の確認・引き受け・投票・承認。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | タスクID | `task_1a4ddabc32b7e13e3ba3d005` |
| `session_id` | 開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `case_id` | 案件 | `case_52e00dd5202cf4c3cd0f1e73` |
| `member_id` | 宛先。**この本人しか回答できない** | `mem_1a9ffe26d9a1f00b1e8c6c98` |
| `kind` | `preparation` / `assignment`（担当引き受け） / `approval`（投票） / `owner_approval` | `preparation` |
| `status` | `open` / `answered` / `expired` / `obsolete`（版が変わって無効） | `answered` |
| `title` | 画面に出す依頼文 | `今回の準備状況を教えてください。` |
| `due_at` | 回答期限 | `2026-09-21T06:28:09.266000000Z` |
| `proposal_id` | 対象の案 | `null` |
| `proposal_version` | 対象の版。古い版への回答はここで弾く | `null` |
| `decision` | 回答。`submit` / `accept` / `decline` / `approve` / `reject` | `submit` |
| `requested_by` | 依頼元 | `system` |
| `answered_at` | 回答日時 | `2026-09-20T06:28:09.268000000Z` |
| `reminded_at` | 催促した日時（1回だけ） | `null` |
| `created_at` | 作成日時 | `2026-09-20T06:28:09.266000000Z` |
| `seq` | 案件内の連番 | `1` |

## events

期限・催促・AI起動の予約。再起動後も残り、`run_at` を過ぎたものから処理する。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | イベントID | `evt_d7cc36c7ef2824fba97cdda7` |
| `session_id` | 開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `case_id` | 案件 | `case_52e00dd5202cf4c3cd0f1e73` |
| `kind` | `plan`（AIの計画・再試行） / `deadline` / `reminder` | `reminder` |
| `ref_id` | 対象（タスクIDなど） | `task_1a4ddabc32b7e13e3ba3d005` |
| `run_at` | 実行予定時刻 | `2026-09-20T18:28:09.266000000Z` |
| `status` | `pending` / `done` / `cancelled` | `pending` |
| `created_at` | 作成日時 | `2026-09-20T06:28:09.266000000Z` |
| `seq` | 連番（処理順） | `1` |

## notifications

Discord への送信待ち行列。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | 通知ID | `ntf_5b60b3e2e3466efddcf1eba4` |
| `session_id` | 開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `case_id` | 案件 | `case_2bf9863fae5bf6b06baea10f` |
| `kind` | `task_requested` / `reminder` / `plan_confirmed` / `needs_owner` | `task_requested` |
| `dedupe_key` | UNIQUE。**同じ通知を二重に積まないための鍵** | `task:task_100957da2b22d23ce13bf1c5` |
| `content` | 送信本文 | `<@100000000000000003> 【第2回】今回担当できる範囲と時間を確認させてください。（回答期限：9/21 15:28）\nhttp://localhost:8099/session.html?id=ses_…` |
| `mentions` | メンションを許可する Discord ID の配列（JSON） | `["100000000000000003"]` |
| `status` | `pending` / `sending` / `sent` / `failed` / `unknown`（成否不明） | `sent` |
| `error_code` | 失敗の理由 | `null` |
| `created_at` / `updated_at` | 作成・更新日時 | `2026-09-20T06:28:23.128000000Z` |
| `seq` | 送信順 | `1` |

## activity

実行履歴。私的な理由・プロンプト全文・モデルの生出力は保存しない。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `seq` | 連番（主キー、AUTOINCREMENT） | `1` |
| `id` | 履歴ID | `act_cdbfbdc2e08c09c374f26e5e` |
| `session_id` | 開催回 | `ses_eaba7ee95b34d9cb3fbffcb5` |
| `case_id` | 案件 | `case_52e00dd5202cf4c3cd0f1e73` |
| `kind` | `input_received` / `proposal_created` / `response_recorded` / `plan_confirmed` / `agent_retry_scheduled` / `agent_stopped` | `input_received` |
| `summary` | 内容の説明文 | `開催回が登録され、参加条件の確認を始めました。` |
| `proposal_id` | 関連する案 | `null` |
| `occurred_at` | 発生日時 | `2026-09-20T06:28:09.266000000Z` |

## llm_calls

LLM 呼び出し1回の記録。金額が分からなければ NULL のままにし、0円として扱わない。以下のサンプルだけは `AGENT_MODE=llm` のときの想定値（デモ投入は `fake` のため記録されない）。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | 記録ID | `llm_1f0c3a7d94e2b58c6d0a1e44` |
| `case_id` | 調整案件（目次取得なら NULL） | `case_52e00dd5202cf4c3cd0f1e73` |
| `lookup_id` | 目次取得（調整案件なら NULL） | `null` |
| `model` | 使ったモデル | `orcarouter/auto` |
| `input_tokens` / `output_tokens` | トークン数（不明なら NULL） | `1820` / `260` |
| `currency` | 通貨。不明なら `unknown` | `unknown` |
| `estimated_amount` | 呼び出し前の見積額（文字列） | `null` |
| `billed_amount` | 実際の請求額（文字列） | `null` |
| `succeeded` | 成功したか（0/1）。失敗も回数に数える | `1` |
| `created_at` | 呼び出し日時 | `2026-09-20T06:28:09.270000000Z` |

## idempotency

同じリクエストの再送に同じ応答を返すための記録。`(user_id, key)` が主キー。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `user_id` | 送信者 | `usr_e3ee07baf9fcb7b54c3e4e7f` |
| `key` | `Idempotency-Key` ヘッダーの値 | `wd-2` |
| `method` / `path` | 対象のリクエスト | `POST` / `/api/sessions/ses_eaba…/withdrawals` |
| `body_hash` | 本文のハッシュ。同じ鍵で違う本文なら拒否する | `6f727e36855ff4cc1cf80ff3c153b5be...` |
| `status` | 最初の応答のステータス | `202` |
| `body` | 最初の応答本文 | `{"session_id":"ses_eaba…","revision":8,"case_id":"case_2bf9…","processing_status":"queued"}` |
| `location` | `Location` ヘッダー（あれば） | `` |
| `created_at` | 記録日時 | `2026-09-20T06:28:21.899000000Z` |

## reading_toc_lookups

輪読固有。ISBN からの目次取得。画像は保存しない。

| カラム | 意味 | サンプル |
| --- | --- | --- |
| `id` | 取得ID | `toc_d9664bd649ee3dd0b144d75a` |
| `group_id` | 依頼元グループ | `grp_c87e49a07149914a2946e5bd` |
| `isbn` | 対象のISBN | `9784873119045` |
| `status` | `resolving_book` / `searching` / `verifying` / `needs_image` / `reading_image` / `succeeded` / `failed` | `needs_image` |
| `book` | 書誌情報（JSON） | `{"isbn":"9784873119045","title":"プログラミングTypeScript…","authors":[…],"publisher":"オーム社","pages":null}` |
| `source` | 取得元の種類 | `null` |
| `source_urls` | 照合できた取得元URL（JSON配列） | `[]` |
| `entries` | 目次候補（JSON配列） | `[]` |
| `unreadable_count` | 画像から読めなかった項目数 | `0` |
| `reason_code` | 失敗・要画像の理由 | `toc_not_found` |
| `retry_count` | 再試行回数 | `0` |
| `next_run_at` | 次の処理時刻 | `2026-09-20T06:29:08.551000000Z` |
| `created_at` / `expires_at` | 作成日時・保持期限（24時間） | `2026-09-21T06:29:08.513000000Z` |

## meta

`key` / `value` の2カラム。

| キー | 意味 | サンプル |
| --- | --- | --- |
| `schema_version` | 適用済みのマイグレーションの版番号（10進の文字列） | `1` |
