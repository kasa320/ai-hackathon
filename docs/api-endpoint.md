# APIエンドポイント

フロントとバックエンドの境界。データ型は [data-structure.md](data-structure.md) を参照。

## 共通の約束

- 同一オリジンのJSON API。`Content-Type: application/json`、JSONは `snake_case`、本文は最大64 KiB、未知のフィールドは拒否する。
- 認証はセッションCookie（`session`、HttpOnly、ログインから7日）。本人IDや権限をリクエスト本文で指定しない。
- 認証済みのPOST・PUTには `X-CSRF-Token`（`GET /api/me` で取得）が必須。サーバー側でOriginも検証する。
- 業務更新のPOST・PUTには `Idempotency-Key` が必須。同じキー・同じ本文の再送には最初の成功応答を返す。違う操作での再利用は `409 idempotency_key_reused`。
- 更新系は「受け付けた」で202を返し、AIの処理・確定・通知はバックエンドのイベント処理が進める。結果は `GET /api/sessions/{id}` のポーリングで確認する。
- 競合は版番号で防ぐ。参加条件の更新・辞退・代案は `expected_revision`、案への回答は `proposal_id` と `proposal_version` を送る。古ければ `409`。
- グループに属さない利用者には、対象の存在も含めて `404` を返す。
- 開催回は「日時を人が決める」「期間だけ渡してエージェントに決めさせる」の2通りで登録できる。後者は `schedule_status="proposed"` で始まり、`starts_at` は**仮の候補**（確定した日時ではない）。案が同意を集めて確定した時点で `starts_at` が決まり、`schedule_status="confirmed"` になる。画面はこの間、日時を決まったものとして表示しない。

## 汎用API

| メソッド・パス | 権限 | 成功 | できること |
| --- | --- | --- | --- |
| `GET /api/health` | 不要 | 200 | 接続確認。`dev_mode` で開発モードか分かる |
| `GET /api/playbooks` | 不要 | 200 | 登録された用途（playbook）の一覧 |
| `GET /api/auth/discord` | 不要 | 302 | Discordログイン開始。`?return_to=` に同一サイトの相対パスを指定できる |
| `GET /api/auth/callback` | OAuth state | 303 | ログイン完了。指定の画面へ戻す |
| `GET /api/me` | ログイン済み | 200 | 自分の情報とCSRFトークン |
| `POST /api/auth/logout` | ログイン済み | 204 | ログアウト（サーバー側のセッションも無効化） |
| `GET /api/groups` | ログイン済み | 200 | 自分が参加するグループ一覧 |
| `POST /api/groups` | ログイン済み | 201 | 固定メンバーでグループ作成。招待はDiscord IDで登録する |
| `GET /api/groups/{group_id}` | メンバー | 200 | メンバー一覧と自分の役割 |
| `GET /api/groups/{group_id}/sessions` | メンバー | 200 | 開催回の一覧 |
| `POST /api/groups/{group_id}/sessions` | 管理者 | 201 | 開催回を登録し、全員への参加条件の確認を開始する。日時（`starts_at`）か期間（`period_start`・`period_end`）のどちらかを渡す |
| `GET /api/sessions/{session_id}` | メンバー | 200 | **画面の主データ。** 計画・参加条件・案件の状況・自分宛てタスク・権限をまとめて返す |
| `PUT /api/sessions/{session_id}/preparations/me` | メンバー本人 | 202 | 自分の参加条件を送信・更新する |
| `POST /api/sessions/{session_id}/preparations/me/interpretations` | メンバー本人 | 200 | 自分の自由文から参加条件の下書きを作る。**保存はしない** |
| `POST /api/sessions/{session_id}/withdrawals` | メンバー本人 | 202 | 自分の担当辞退（`assignment`）・欠席（`attendance`）を登録する |
| `POST /api/tasks/{task_id}/responses` | タスクの本人 | 202 | 確認への回答・担当の引き受け・投票・管理者の承認 |
| `POST /api/sessions/{session_id}/proposals` | 管理者 | 202 | 管理者判断待ちのときに代案を提出する |
| `GET /api/sessions/{session_id}/activity` | 管理者 | 200 | 実行履歴・LLM費用・通知の状況 |

「AIを実行する」「強制的に確定する」「他人として同意する」「通知を送る」APIは設けていない。案の作成・確定・通知・再試行はすべてバックエンド内のイベント処理が行う。

### 自由文の解釈について

`POST .../preparations/me/interpretations` は `{"text": "..."}` を受け取り、参加条件の下書き（`saved: false`）を返すだけで**何も保存しない**。本人が確認・修正して `PUT .../preparations/me` を送ると保存される。

- 対象者は常に呼び出した本人。メンバーIDを入力で受け取らない。
- LLMが決めるのは定義済み項目の値だけ。読み取れない項目は推測せず `unclear` に入れる。
- 原文は解釈用モデルへの送信にだけ使い、DB・ログに保存しない。
- 保存しないため `Idempotency-Key` は不要。CSRFと所属の検証は通常どおり。

## 輪読固有API（`playbook_id = "reading"`）

用途固有のAPIは `/api/groups/{group_id}/{playbook_id}/...` に置く。すべて管理者専用。

| メソッド・パス | 成功 | できること |
| --- | --- | --- |
| `POST /api/groups/{group_id}/reading/toc-lookups` | 202 | ISBNから目次の取得を開始する |
| `GET /api/groups/{group_id}/reading/toc-lookups/{lookup_id}` | 200 | 取得状況と候補を取得する（3秒間隔のポーリング） |
| `POST /api/groups/{group_id}/reading/toc-lookups/{lookup_id}/images` | 202 | 目次ページの画像を提出する（`needs_image` のときだけ） |

取得結果は**候補にすぎず**、管理者が確認・修正して開催回登録の `data.sections` に使うまで保存されない。書名から目次を推測する経路はなく、取得元ページと照合できなければ画像の提出を求める。画像は LLM への送信にだけ使い、保存しない。

画像は `multipart/form-data` のフィールド名 `images` に1〜5枚（JPEG・PNG・WebP、1枚4 MB・合計10 MBまで）。

## 開発・デモ用API

`DEV_MODE=1` のときだけ登録される。無効時は存在しない扱い（404）。`Idempotency-Key` は不要、Originの検証は行う。

| メソッド・パス | できること |
| --- | --- |
| `GET /api/dev/status` | 現在時刻、時計のずれ、有効な障害注入を返す |
| `POST /api/dev/clock/advance` | 時計を進める（戻せない）。期限・催促・再試行の判定が進んだ時刻で動く |
| `POST /api/dev/login` | 登録済みユーザーとして通常と同じセッションを発行する（人物切り替え） |
| `PUT /api/dev/faults` | LLM・通知・目次検索の障害を注入する |
| `POST /api/dev/seed` | DBを初期化し、デモ用の初期状態（`initial_demo` / `replan_demo`）を投入する |

## エラー

`GET /api/health` の503とOAuthのリダイレクトを除き、失敗は次の形で返る。フロントは `message` ではなく `code` で分岐する。

```json
{ "error": { "code": "revision_conflict", "message": "...", "details": {}, "request_id": "req_..." } }
```

| HTTP | code | フロントの処理 |
| --- | --- | --- |
| 400 | `invalid_json`, `idempotency_key_required` | 送信内容を直す |
| 401 | `unauthenticated` | 個人データを破棄してログインへ案内 |
| 403 | `forbidden`, `csrf_invalid` | 操作不可を表示。CSRFなら `/api/me` を取り直し、勝手に再送しない |
| 404 | `not_found` | 対象がない／アクセスできない |
| 405 | `method_not_allowed` | メソッドを直す |
| 409 | `revision_conflict`, `proposal_superseded`, `task_closed`, `task_expired`, `invalid_state`, `members_not_joined`, `idempotency_key_reused` | 最新状態を取得して再確認を求める |
| 413 | `payload_too_large` | 入力サイズを減らす |
| 415 | `unsupported_media_type` | Content-Typeを直す |
| 422 | `validation_failed`, `unsupported_playbook` | `details.fields[].path` を対応する入力欄に表示 |
| 500 | `internal_error` | 失敗を表示して `request_id` を記録 |
| 503 | `temporarily_unavailable` | 通信障害として扱う。再送するなら同じ `Idempotency-Key` を使う |

`details` は常にobject。`revision_conflict` には `current_revision` が入る。

## 画面URL

バックエンドがURLを作るのはこの2つだけで、ファイル名は固定。他の画面構成はフロント側で決める。

| URL | 用途 |
| --- | --- |
| `/` | トップ（ログイン・グループ選択）。OAuth callback の既定の戻り先 |
| `/session.html?id={session_id}` | 開催回の画面。Discord通知のリンク先、ログイン後の戻り先 |

通知のリンクは `PUBLIC_BASE_URL` とこのパスを組み合わせて作る。未ログインで開いた場合は `GET /api/auth/discord?return_to=/session.html?id=...` へ遷移させる。
