# フロント・バックエンド間のAPI契約

決定日：2026-09-19。契約バージョン：`mvp-2`（`mvp-1` から汎用部分と輪読部分を分離し、開発用APIと画面URLを追加、ページングを削除）。

この文書をMVPの実装・フロント用モックの唯一の共通仕様とする。APIの実装を完了したという意味ではない。現在の `master` はヘルスチェックのみ、バックエンド雛形ブランチはPlaybook一覧まで実装している。

業務要件は [spec.md](spec.md)、用途分離は [architecture.md](architecture.md)、評価は [evaluation.md](evaluation.md) を参照。内部のGo型やDBテーブルをそのまま公開せず、本書の入出力へ変換する。

## 文書の構成

| 部 | 内容 | 対応するコード |
| --- | --- | --- |
| 第1部 汎用API | 認証、グループ、開催回、参加条件、タスク、案、同意、履歴、エラー、再送、AI処理の再試行、画面URL、開発用API。用途によらず同じ | `api/`、`auth/`、`coord/`、`notify/` |
| 第2部 輪読Playbook | `playbook_id="reading"` のときに `data` へ入る型、検証ルール、承認条件、目次の取得API | `playbook/reading/` |
| 第3部 画面対応とモック | 画面ごとの利用API、ポーリング、モックの状態、変更ルール | フロント |

**汎用と用途別の境界**：第1部の型は、用途ごとの中身を必ず `data` フィールドに包んで受け渡す。第1部には節・発表・進行表などの輪読用語を出さない。`data` の型・検証・承認条件は第2部で定義する。新しい用途を追加するときは、第2部に相当する部を追加し、第1部は変更しないことを目標とする。

---

# 第1部 汎用API

## 1. 今回の決定

- 同一オリジンのHTTP JSON API。ベースパスは `/api`。日時・担当変更などの業務処理はバックエンドが担当する。
- 認証はDiscord OAuth＋サーバーセッションCookie。本人IDや権限をリクエスト本文で指定しない。
- 次回日程が決まっている会を対象とする。グループ作成、固定メンバー登録、開催回登録、参加条件の回答、辞退、引き受け、投票、管理者の判断待ち時の代案入力までを契約に含める。
- **案（初回案・再計画案）はAIが作成する。** 管理者は初回案を承認する。管理者が案を入力できるのは、AIが解決できず「管理者の判断待ち」になったときだけ。
- 通知先はサーバー設定済みのDiscordチャンネル。チャンネル選択・DM・日程投票・メンバー入れ替え・当日進行は対象外。
- 入力は構造化フォームに固定する。自由文の解釈APIはMVPに含めない。
- 変更受付とAI処理を分ける。業務の更新は保存後に `202 Accepted` を返し、フロントは開催回詳細を3秒間隔で取得する。WebSocket・SSEは使わない。
- 「登録一覧にある」「202が返った」「計画が確定した」「通知が送信された」を区別する。フロントで確定条件を独自に計算しない。
- デモ・評価用の操作（時計・人物切り替え・障害注入・初期データ）は、開発モードでのみ有効な `/api/dev/*` に限定する（第14節）。

## 2. 共通規約

### JSONと識別子

- JSONフィールドは `snake_case`。通常レスポンスは `Content-Type: application/json`。成功データを共通の `data` キーで包まず、以下の各レスポンスをそのまま返す（用途固有データの `data` フィールドとは別の話）。
- 本書の型定義は契約の表記であり、TypeScriptの導入を要求しない。`?` のないキーは必須、`null` は値がないことを意味する。配列が空なら `[]`。
- アプリのIDは長さ1〜128文字の不透明な文字列。フロントは形式を解釈しない。DiscordユーザーIDも文字列で扱い、数値に変換しない。
- 日時はタイムゾーン付きRFC 3339、返却はUTCの `Z` 表記。表示時に利用者のタイムゾーンへ変換する。`starts_at` の入力は明示的なオフセット必須。
- revision・version・分数・件数はJavaScriptの安全な整数範囲内の整数。revisionとversionは1から始まる。金額は小数文字列で返す。
- JSON本文は最大64 KiB。未知の入力フィールドは拒否する。未指定項目を暗黙に上書きするPATCHは用意しない。
- 画像などのファイルを受け取るAPIだけは `multipart/form-data` とし、形式・サイズの上限は各Playbookの部で定める。
- 名前・タイトルは前後の空白を除いた1〜100文字。グループの人数は管理者込み2〜10人、開催回の持ち時間は15〜180分とする。用途固有データの制限は各Playbookの部で定める。フォームも同じ制限を使う。
- 認証済みレスポンスは `Cache-Control: no-store`。APIのエラーをHTMLやログインページとして返さない。
- 一覧APIはページングせず全件を返す（MVPの件数は多くて数十件）。実行履歴だけは新しい順に最大200件とする。

### セッションとCSRF

`GET /api/me` はログイン状態とセッションに結び付いたCSRFトークンを返す。Cookie名は `session`、`HttpOnly; SameSite=Lax; Path=/`、本番HTTPSでは `Secure` を付ける。ローカルHTTP開発のみSecureを外す。固定の有効期限はログインから7日、ログアウトでサーバー側も無効化する。

すべての認証済みPOST・PUTに `X-CSRF-Token` を必須とし、サーバーでもOriginを検証する。同一オリジンの `fetch` で `credentials: "same-origin"` を使う。開発時にフロントを別ポートで動かすなら `/api` をプロキシし、任意オリジンを許可するCORS設定は追加しない。

OAuth開始はブラウザー遷移、callbackはバックエンド処理。stateの検証・トークン交換・セッション発行はバックエンド側で行う。DiscordのアクセストークンやBotトークンをフロントに返さない。

### 再送と競合

業務更新のPOST・PUTには `Idempotency-Key` を必須とする。フロントは1回の送信操作にUUIDを生成し、通信失敗時の再送では同じキーと同じ本文を使う。ログアウトと `/api/dev/*` はこのキーを不要とする。

バックエンドは利用者・キーの組を記録し、メソッド・パス・JSON内容が同じなら最初の成功ステータスと本文を返す。違う操作へのキー再利用は `409 idempotency_key_reused`。業務更新と成功応答の再送情報を同じトランザクションで保存し、保存期間は最低24時間とする。検証で拒否した更新は成功記録を作らない。SQLiteへの書き込みは1接続に直列化しているため、同じキーが同時に届いても1回分として処理される。

認証・対象への現在のアクセス権を確認した後、既に成功したキーの再送判定を行い、その後に新規操作の版と入力を検証する。これにより、成功後の再送が「既に回答済み」で失敗することを防ぐ。

`session.revision` は参加条件・有効な案・確定計画が変わると増える。投票・引き受けの記録だけ、実行ログ、通知状況の更新では増やさない。各案には別に開催回内で単調増加する `version` がある。

- 参加条件の更新・辞退・管理者による代案は `expected_revision` を送る。古ければ `409 revision_conflict`。
- 版付き案への回答は `proposal_id` と `proposal_version` を送る。投票同士はsessionのrevisionで競合させない。
- 更新により現在の案が不成立になったら、入力保存と同時に案を `superseded`、未回答タスクを `obsolete` にする。AIの再計画完了を待って古い票を受け付け続けない。
- 409を受けたフロントは最新状態を取得し、入力下書きを保持して再確認を求める。古い同意を新版へ自動再送しない。

## 3. エンドポイント一覧

権限の「メンバー」は対象グループに所属する本人。「管理者」はグループ作成者。グループに属さない利用者には対象の存在も含め `404` を返す。

| メソッド・パス | 権限 | 成功 | 用途 |
| --- | --- | --- | --- |
| `GET /api/health` | 不要 | 200 | 接続確認 |
| `GET /api/playbooks` | 不要 | 200 | 登録された用途の一覧 |
| `GET /api/auth/discord` | 不要 | 302 | Discordログイン開始 |
| `GET /api/auth/callback` | OAuth state | 303 | ログイン完了後、指定の画面へ戻す |
| `GET /api/me` | ログイン済み | 200 | 自分とCSRFトークン |
| `POST /api/auth/logout` | ログイン済み | 204 | ログアウト |
| `GET /api/groups` | ログイン済み | 200 | 自分が参加するグループ一覧 |
| `POST /api/groups` | ログイン済み | 201 | 固定メンバーでグループ作成 |
| `GET /api/groups/{group_id}` | メンバー | 200 | メンバー一覧と自分の役割 |
| `GET /api/groups/{group_id}/sessions` | メンバー | 200 | 開催回一覧 |
| `POST /api/groups/{group_id}/sessions` | 管理者 | 201 | 開催回を登録し、参加条件の確認を開始 |
| `GET /api/sessions/{session_id}` | メンバー | 200 | 共有計画・進行状況・自分の操作をまとめて取得 |
| `PUT /api/sessions/{session_id}/preparations/me` | メンバー本人 | 202 | 自分の参加条件を送信・更新 |
| `POST /api/sessions/{session_id}/preparations/me/interpretations` | メンバー本人 | 200 | 自分の自由文から参加条件の下書きを作る（保存しない） |
| `POST /api/sessions/{session_id}/withdrawals` | メンバー本人 | 202 | 自分の担当辞退・欠席を登録 |
| `POST /api/tasks/{task_id}/responses` | タスクの本人 | 202 | 確認への回答・担当引き受け・投票・承認 |
| `POST /api/sessions/{session_id}/proposals` | 管理者 | 202 | 管理者の判断待ちのときに代案を提出 |
| `GET /api/sessions/{session_id}/activity` | 管理者 | 200 | 実行履歴・費用・通知状況を取得 |
| `/api/dev/*` | 開発モードのみ | — | 第14節 |

用途固有のAPIは `/api/groups/{group_id}/{playbook_id}/...` に置き、各Playbookの部で定める（輪読は第2部 R7）。認証・CSRF・Idempotency-Key・エラー形式は第1部の規約に従う。

ブラウザー向けの「AI実行」「強制確定」「他人として同意」「通知送信」APIは設けない。案の自動作成・条件成立時の確定・通知・再試行はバックエンド内のイベント処理で行う。

### 公開APIのレスポンス

`GET /api/health` の200：

```json
{ "status": "ok", "now": "2026-09-19T09:00:00Z", "dev_mode": false }
```

`dev_mode` は開発用APIが有効かどうか。フロントはtrueのとき「デモモード」の表示を出す。

`GET /api/playbooks` の200：

```json
{ "playbooks": [{ "id": "reading", "name": "輪読" }] }
```

一覧は登録された用途のメタデータ。雛形段階ではここに含まれていても、業務用エンドポイントの実装済みを意味しない。

## 4. 共通データ型

以下の `ID` と `Timestamp` はstring、`Revision` はinteger。`PlaybookID` は `GET /api/playbooks` の `id`。返却データには私的理由、プロンプト全文、モデルの生出力、秘密情報を含めない。

**用途固有の型**：`SessionData`・`PreparationData`・`PlanData` は用途ごとに異なる。どの型かは開催回の `playbook_id` で決まる。輪読の定義は第2部。

```ts
type Member = {
  id: ID;                    // グループ内のmember_id
  display_name: string;
  role: "owner" | "member";
  joined: boolean;           // 招待対象のDiscord IDでログイン済みか
};

type Group = {
  id: ID;
  name: string;
  current_member_id: ID;
  members: Member[];
};

type Preparation = {
  attendance: "attending" | "absent";  // 共通：参加するかどうか
  data: PreparationData;               // 用途固有：準備状況・担当できる範囲など
};

type SessionSummary = {
  id: ID;
  group_id: ID;
  playbook_id: PlaybookID;
  title: string;
  starts_at: Timestamp;
  duration_minutes: number;
  revision: Revision;
  status: "draft" | "confirmed" | "needs_attention";
  updated_at: Timestamp;
};

type ApprovalProgress = {
  kind: "majority" | "owner";
  eligible_member_ids: ID[];
  required_count: number;
  approved_member_ids: ID[];
  rejected_member_ids: ID[];
};

type Proposal = {
  id: ID;
  version: number;
  status: "pending" | "confirmed" | "superseded" | "rejected";
  change_kind: "initial" | "replan";
  author: "agent" | "owner";   // AIが作った案か、管理者の代案か
  summary: string;             // 共有可能な変更点の説明
  data: PlanData;              // 用途固有の計画
  approvals: ApprovalProgress[];
  assignments: {               // 本人の引き受けが必要な人
    member_id: ID;
    status: "pending" | "accepted" | "declined";
  }[];
};

type CaseSummary = {
  id: ID;
  status: "collecting" | "planning" | "awaiting_consent" | "confirmed" | "needs_owner";
  reason_code: null | "no_feasible_plan" | "deadline_expired" | "model_error" | "budget_exceeded";
  summary: string;
  next_retry_at: Timestamp | null;  // AI処理の再試行待ちのときの次回予定（第10節）
};

type Task = {
  id: ID;
  session_id: ID;
  kind: "preparation" | "assignment" | "approval" | "owner_approval";
  status: "open" | "answered" | "expired" | "obsolete";
  title: string;              // 共有可能な情報だけを使う
  due_at: Timestamp;
  proposal_id: ID | null;
  proposal_version: number | null;
  allowed_decisions: ("submit" | "accept" | "decline" | "approve" | "reject")[];
};

type MutationAccepted = {
  session_id: ID;
  revision: Revision;        // この入力保存直後のrevision
  case_id: ID;
  processing_status: "queued";
};
```

- member_idはグループ内ID、`/me.user.id` はアプリのユーザーID。互換として扱わない。サーバーはセッションから現在のmember_idを解決する。
- 参加条件の未回答は `Preparation=null` で表現し、不参加（`attendance="absent"`）とは区別する。
- `assignments` は案に含まれる担当者ごとに1件。その人が引き受けるのは当該版の自分の担当すべてであり、集団の投票とは別に記録する。どの人に担当があるかは用途固有の `PlanData` からPlaybookが求める。

## 5. 認証と初期メンバー登録

### ログイン

ログインボタンで `GET /api/auth/discord` に遷移する。`?return_to=` に戻り先を指定できる。受け付けるのは `/` で始まり `//` で始まらない同一サイト内の相対パスだけで、それ以外は無視して `/` に戻す。戻り先はサーバー側でOAuth stateに結び付けて保持する。

callback成功時はCookieを発行して `303 Location: <return_to または />`、失敗時は `303 Location: /?auth_error=access_denied` または `invalid_state` または `provider_unavailable`。エラーの詳細や外部トークンはURLに含めない。

`GET /api/me` の200レスポンス例：

```json
{
  "user": { "id": "usr_a", "display_name": "A", "discord_user_id": "111111111111111111" },
  "csrf_token": "session-bound-token"
}
```

未ログインは `401 unauthenticated`。POST logoutの本文はなし、CSRFヘッダーは必須。成功は204、Cookieを削除する。フロントは取得済みの個人データとCSRFトークンを破棄する。

### グループ作成と参加

MVPでは管理者が事前に共有してもらったDiscordユーザーIDを登録する。入力されたIDは「招待先」であり、その人として操作する権限は得られない。招待された本人がDiscord OAuthでログインした時点で所属を有効にする。未参加者へアプリが自動でDMを送る機能はない。

`POST /api/groups`：

```json
{
  "name": "技術書輪読",
  "invitees": [
    { "discord_user_id": "222222222222222222", "display_name": "B" },
    { "discord_user_id": "333333333333333333", "display_name": "C" },
    { "discord_user_id": "444444444444444444", "display_name": "D" }
  ]
}
```

作成者は自動でownerになる。inviteesは1〜9人、IDの重複と自分のIDは禁止。Discord IDは17〜20桁の数字文字列として検証し、存在確認は本人のログイン時まで保証しない。作成時のdisplay_nameは仮の表示名で、本人ログイン後は本人の表示名を使う。既にこのアプリでDiscord認証済みのユーザーが一致する場合は、その所属を作成時点で有効にする。

成功は `201`、本文は `Group`、`Location: /api/groups/{group_id}`。本人がログインしていないメンバーは `joined=false`。開催回を作る前に全員のjoinedを確認し、未参加者がいれば `409 members_not_joined`。所属の追加・削除・管理者変更はMVP外。

`GET /api/groups/{group_id}` はGroupを返す。招待先のDiscord ID一覧は返さない。

`GET /api/groups` は自分の所属だけを作成日時の降順で返す：

```ts
type GroupList = { items: { id: ID; name: string; current_member_id: ID; role: "owner" | "member"; member_count: number }[] };
```

## 6. 開催回の登録・取得

### 登録

`POST /api/groups/{group_id}/sessions`：

```json
{
  "playbook_id": "reading",
  "title": "第2回",
  "starts_at": "2026-09-21T11:00:00Z",
  "duration_minutes": 60,
  "data": { }
}
```

`data` は `playbook_id` に対応する `SessionData`（輪読は第2部 R1、例は第2部 R6）。存在しない用途は `422 unsupported_playbook`、`data` の検証はPlaybookが行い、違反は `422 validation_failed`。starts_atは現在時刻から1時間より先で、回答期限を確保できる日時とする。

成功は `201`、本文 `{ "session": SessionSummary, "case_id": ID }`、Locationは `/api/sessions/{session_id}`。状態はdraft、revision=1。グループ全員をこの開催回のメンバーとして固定し、初期のPreparationはnullとする。全員へのpreparationタスクと処理イベントを同じトランザクションで登録する。LLM完了や通知完了を待たない。作成後の `data`・日時は MVP では編集しない。

`GET /api/groups/{group_id}/sessions` は `{ "items": SessionSummary[] }` を starts_at の降順で返す。

### 詳細：共有画面と参加者画面の共通データ

`GET /api/sessions/{session_id}` の200レスポンス：

```ts
type SessionDetail = {
  session: SessionSummary;
  data: SessionData;                 // 用途固有の開催回データ
  members: Member[];
  preparations: { member_id: ID; value: Preparation | null }[];
  confirmed_plan: Proposal | null;
  current_proposal: Proposal | null;
  active_case: CaseSummary | null;
  my_tasks: Task[];
  permissions: {
    can_update_preparation: boolean;
    can_withdraw_assignment: boolean;
    can_withdraw_attendance: boolean;
    can_submit_proposal: boolean;
    can_view_activity: boolean;
  };
  current_member_id: ID;
  notification_summary: {
    pending_count: number;
    sent_count: number;
    failed_count: number;
    unknown_count: number;
  };
  server_now: Timestamp;
};
```

- `confirmed_plan` は直近の確定案。担当辞退で実行不能になっても履歴として残し、session.statusをneeds_attentionにする。画面は履歴を「現在も実行可能」と表示しない。
- `current_proposal` は最新の案。棄却・旧版になった場合も次の案が出るまでそのstatus付きで返せる。同じ案が確定した場合はconfirmed_planと同じIDになる。
- 1開催回に未完了の調整案件は最大1つ。追加の参加条件の変更・辞退は同じ案件に取り込む。完了後の新しい問題は新しい案件になる。active_caseは直近の案件を返し、確定後はstatus=confirmedのまま返す。
- preparationsは本人が共有を確認して送信した構造化情報のみ。フォームには「参加条件はこのグループのメンバーに共有されます」と表示する。
- my_tasksは現在の案件で自分宛てに作られたタスクのみを、作成順に返す。他人の回答を本人が行えるような入力先を返さない。
- permissionsはその時点の画面表示用。サーバーは実際の更新時に権限と条件を再検証する。管理者以外のcan_submit_proposal/can_view_activityはfalse。
- can_submit_proposalは、管理者で、active_caseがneeds_ownerで、新たな回答期限を確保できるときのみtrue。開催時刻以降の更新はMVP外で、参加条件・辞退・案提出のpermissionもfalseにする。
- notification_summaryは現在の案件に関連する全通知の状態別件数。送信済みはDiscord APIの成功応答であり、既読ではない。
- GETではLLMを起動せず、保存済み状態を返す。後続の状態変化でrevisionが同じ場合もあるため、ポーリング結果はrevisionだけで比較せず表示へ反映する。

レスポンス例は第2部 R6。

## 7. 参加条件の回答と辞退

### 参加条件の回答

`PUT /api/sessions/{session_id}/preparations/me`：

```json
{
  "expected_revision": 3,
  "preparation": {
    "attendance": "attending",
    "data": { }
  }
}
```

`preparation.data` は用途固有の `PreparationData`（輪読は第2部 R1・R3）。Playbookが検証し、違反は `422 validation_failed`。

本人のPreparationを全置換する。自分宛てのopenなpreparationタスクがあれば回答済みにする。入力が変わった場合はrevisionを増やし、現在のpending案を旧版にして案件をcollectingへ戻す。値が同じ場合は版を変えず、未回答タスクの回答処理のみ行う。確定計画への影響をPlaybookで検証し、実行不能ならneeds_attentionにする。

成功は202とMutationAccepted。本人以外のmember_idを含めた本文は拒否する。参加条件の回答で担当の引き受けや投票を自動作成しない（「担当できる」と答えても引き受けにはならない）。

参加条件の更新・辞退は開催時刻までは受け付ける。開催1時間前を過ぎて新しい回答期限を確保できない場合は、変更自体を記録した上で案件をneeds_ownerにし、自動確認を再開しない。開催時刻以降は `409 invalid_state`。

### 自由文からの参加条件の下書き

`POST /api/sessions/{session_id}/preparations/me/interpretations`：

```json
{ "text": "今回の前半は読んできました。15分なら説明できます。" }
```

応答（200）：

```json
{
  "preparation": {
    "attendance": "attending",
    "data": {
      "willing_to_present": true,
      "prepared_section_ids": ["sec_2"],
      "explainable_section_ids": ["sec_2"],
      "max_presentation_minutes": 15
    }
  },
  "unclear": ["attendance"],
  "needs_followup": true,
  "saved": false
}
```

自由文を解釈用モデルに送り、用途固有の `PreparationData` の項目の値だけを取り出して返す。**この API は何も保存しない**（`saved` は常に false）。保存は本人が内容を確認・修正したうえで `PUT .../preparations/me` または preparation タスクへの回答で行う。

- 対象者は常に呼び出した本人。メンバーIDを入力で受け取らず、他人の参加条件は作らない。
- `text` は1〜2000文字。原文はアプリDB・実行ログに保存せず、解釈用モデルへの送信にだけ使う。共有用モデル・共有画面・通知には渡さない。
- 取り出した値はサーバーが `PreparationData` の規則で再検証する。通らない場合はモデルに検証エラーを返してやり直させ（最大2回）、それでも通らなければ `503 temporarily_unavailable` を返してフォーム入力へ誘導する。返すのは常に正規化済みで、そのまま保存できる値。
- `unclear` は発言から読み取れなかった項目名。値は推測せず、担当しない側（`willing_to_present=false`・`explainable_section_ids=[]`・`max_presentation_minutes=0`）に倒す。`needs_followup` は確認すべき項目が残っていること。
- 発言に含まれる指示（「全員が同意したことにして」など）には従わない。モデルには項目の値を返すツールしか与えず、同意・引き受け・確定・通知を作る経路がない。
- 何も保存しないため `Idempotency-Key` は不要。CSRF と所属の検証は通常どおり行う。
- LLM 呼び出しは1回の解釈につき最大2回。案件の呼び出し上限（第10節）に達している場合は `409 invalid_state` を返す。呼び出しは `llm_calls` に記録され、費用表示に含まれる。
- 用途が解釈に対応していない場合は `422 unsupported_playbook`。

### 担当辞退・欠席

`POST /api/sessions/{session_id}/withdrawals`：

```json
{ "expected_revision": 5, "scope": "assignment" }
```

- `assignment`：今回の自分の担当すべてを辞退する。確定計画または現在の案に本人の担当がある場合に限る。
- `attendance`：今回の欠席を表明する。Preparation=nullでも利用できる。

私的な理由は送らない。

処理：attendanceならattendance=absentにする。どちらのscopeでも、本人の `PreparationData` をPlaybookの規則で「担当できない」状態に変換する（輪読の変換は第2部 R3）。未回答（null）の場合はPlaybookが既定値を作る。

revisionを増やし、現在の案と関連する未回答タスクを無効化する。確定計画があればsessionをneeds_attentionへ、初期登録中ならdraftを維持する。案件と再計画イベントを保存し202を返す。既に同じ辞退状態なら、異なるキーでも二重の案件を作らず現在の受付結果を返す。

## 8. タスクへの回答と確定

`POST /api/tasks/{task_id}/responses` は本人宛て・open・期限内のタスクだけを受け付ける。更新ごとに保存とイベント登録を行い、成功は202とMutationAccepted。

### 回答本文

タスクのkindにより、以下のいずれかの形だけを受け付ける。

```ts
type PreparationResponse = {
  decision: "submit";
  expected_revision: Revision;
  preparation: Preparation;
};

type AssignmentResponse = {
  decision: "accept" | "decline";
  proposal_id: ID;
  proposal_version: number;
};

type ApprovalResponse = {
  decision: "approve" | "reject";
  proposal_id: ID;
  proposal_version: number;
};
```

preparationはPreparationResponse、assignmentはAssignmentResponse、approval/owner_approvalはApprovalResponseに対応する。openなtaskのallowed_decisionsはこの対応に従い、open以外では空配列とする。

preparationタスクへのsubmitは第7節の参加条件の回答と同じ処理を使う。assignmentのacceptは当該版の本人担当すべてへの引き受けであり、approvalのapproveとは独立に扱う。

回答済み・期限切れ・旧版のタスクは409で拒否する。同じIdempotency-Keyでの成功済み再送だけは元の成功を返す。同意の直接編集・取消APIはMVP外とし、担当を引き受けられなくなった場合は参加条件の更新または辞退で計画を無効化する。

### 確定条件（汎用の規則）

各案に必要な条件は、Playbookが `PlanData` から算出する（輪読の算出規則は第2部 R4）。条件は次の2種類で、すべてを満たすと確定する。

- **本人の引き受け**（`assignments`）：案に担当がある人全員のaccept。
- **承認**（`approvals`）：`majority`（対象者の過半数）または `owner`（管理者）。

どの用途でも、共通側で次を保証する。

- 引き受けは本人しかできない。集団の賛成票や管理者の承認で本人の引き受けを代行しない。管理者が案を提出したことを承認として数えない。
- 過半数の必要数は `floor(対象人数 / 2) + 1`。対象者はPlaybookが指定し、共通側で所属と参加予定（attendance=attending）を確認する。対象者と必要数は版ごとに固定し、反対・未回答で分母を縮めない。参加条件や参加予定者が変わったら新しい版で取り直す。参加予定者が0人なら案を作らず管理者の判断待ち。
- 初回の参加条件の未回答者を勝手に欠席扱いせず、全員の回答が揃うまで案を作らない。
- assignmentのdeclineまたはowner_approvalのrejectで案をrejectedにし、未回答タスクをobsoleteにして再計画へ戻す。majorityで一票rejectが出ても即棄却しない。残り全員が賛成しても成立しない場合は棄却・再計画する。
- 最後の必要な回答が揃うとバックエンドが自動で検証・確定する。再度最新版と参加条件を確認し、計画保存と通知待ち登録を同一トランザクションで行う。未回答の残りタスクはobsoleteにし、確定後に追加の投票で結果を変更しない。最後のPOSTの202時点で確定済みとは限らない。
- 各タスクの期限は作成から24時間と開催1時間前の早い方。サーバー時刻がdue_at以上なら拒否する。未回答者には期限の中間時点で1回だけDiscordで催促する。必要条件が期限までに揃わなければneeds_ownerへ戻す。

## 9. 管理者による代案

`POST /api/sessions/{session_id}/proposals` は、案件がneeds_ownerの場合だけ利用可能。AIが調整中の案を管理者が上書きする用途には使わない。

```json
{ "expected_revision": 8, "data": { } }
```

`data` は用途固有の `PlanData`（輪読の例は第2部 R6）。全員の参加条件の回答が揃い、Playbookの検証を満たすことが必要。初回案か変更案か、誰の承認が必要かはサーバーが決定し、本文で指定しない。案に含める担当者は指定できるが、本人の引き受けを代行できない。

成功時に新しい版（`author="owner"`）を発行してrevisionを増やし、以前の案とタスクを無効化する。必要なタスクとイベントを作成し202を返す。管理者の案にもAIと同じ検証・同意条件を適用する。実現不能な場合は422で保存せず、日時変更・中止の自動実行は行わない。

新しい回答期限を確保できない場合は `409 invalid_state`。AI作成・管理者提出のどちらでも、案の保存時に生成元のrevisionを確認する。生成中に参加条件の更新や別案の提出があった場合、古い状態に基づく案で上書きしない。

## 10. AI処理の失敗と再試行

AI処理（LLM呼び出しとツール実行）の失敗は、受理済みの202を取り消さず、案件の状態で伝える。失敗の種類ごとに扱いを分ける。

| 種類 | 例 | 扱い |
| --- | --- | --- |
| 一時的な障害 | 通信エラー、タイムアウト、429、5xx | 指数バックオフで再試行し続ける（下記）。案件はplanningのまま、`next_retry_at` を設定 |
| 不正な出力 | ツール引数のJSON不正、Playbookの検証違反 | 検証エラーをモデルに返して同じ起動内で最大2回やり直す。直らなければneeds_owner（`model_error`） |
| 予算超過 | 案件の費用・呼び出し回数の上限に到達 | 再試行しない。needs_owner（`budget_exceeded`） |
| 案が作れない | 制約を満たす案がない | 再試行しない。needs_owner（`no_feasible_plan`） |

一時的な障害の再試行：

- 間隔は30秒から倍々に増やし、最大10分。±20%のゆらぎを加える。
- 再試行の打ち切りは「次に必要な回答期限を確保できなくなる時刻」（開催1時間前から逆算）。そこまでに成功しなければneeds_owner（`deadline_expired`）。
- 再試行の予定はイベントとしてDBに保存し、サーバー再起動後も継続する。待機中はLLMを呼ばない。
- LLM呼び出し回数の上限は再試行を含めて数える。上限に達したら `budget_exceeded`。SDKの自動再試行は無効にし、アプリ側で回数と費用を数える。
- 待機中、`CaseSummary.summary` に「AIの応答に失敗したため、hh:mm に再試行します」のような説明を入れ、`next_retry_at` を返す。

管理者がAIに再開を指示するAPIは設けない。needs_ownerからの復帰は、管理者の代案（第9節）または参加条件の更新・辞退による新しい入力で行う。

## 11. 実行履歴・費用・通知

`GET /api/sessions/{session_id}/activity` は管理者専用。履歴は開催回全体を新しい順に最大200件、summaryは現在の案件全体の集計。

```ts
type ActivityItem = {
  id: ID;
  occurred_at: Timestamp;
  case_id: ID;
  kind: "input_received" | "proposal_created" | "response_recorded" |
        "plan_confirmed" | "agent_retry_scheduled" | "agent_stopped" | "notification_updated";
  summary: string;
  proposal_id: ID | null;
};

type ActivityResponse = {
  items: ActivityItem[];
  summary: {
    case_id: ID | null;
    llm_call_count: number;
    tool_call_count: number;
    costs: {
      currency: string;
      estimated_amount: string | null;
      billed_amount: string | null;
    }[];
    notifications: {
      id: ID;
      status: "pending" | "sent" | "failed" | "unknown";
      updated_at: Timestamp;
      error_code: null | "delivery_failed" | "delivery_unknown";
    }[];
  };
};
```

同時刻の履歴はIDで順序を固定する。現在案件がなければcase_id=null、件数0、配列=[]。

costsは通貨ごとに分け、異なる通貨を合算しない。LLM呼び出し済みで費用が不明ならamount=null。請求通貨自体も不明ならcurrency=`unknown`の項目を返す。呼び出しが0回ならcosts=[]。推定額と確定請求額を区別し、片方でも明細の欠落がある集計値はそのamountをnullにする。

通知本文、チャンネルの秘密情報、モデルの生出力は返さない。failedとunknownを区別し、unknownを「未送信」と決めつけない。通知の手動再送APIは今回設けず、管理者がDiscord側で状況を確認できる表示にする。

## 12. エラー契約

healthの503レスポンス `{ "status": "db_unavailable" }` とOAuthリダイレクト以外は、失敗を次のJSONに統一する。フロントはmessage文字列を条件分岐に使わずcodeで処理する。

```json
{
  "error": {
    "code": "revision_conflict",
    "message": "状態が更新されています。最新の内容を確認してください。",
    "details": { "current_revision": 9 },
    "request_id": "req_example"
  }
}
```

| HTTP | code | フロントの処理 |
| --- | --- | --- |
| 400 | `invalid_json`, `idempotency_key_required` | 入力・送信処理を修正 |
| 401 | `unauthenticated` | 個人データを破棄してログインを案内 |
| 403 | `forbidden`, `csrf_invalid` | 操作不可を表示。CSRFの場合はmeを再取得し、勝手に更新を再送しない |
| 404 | `not_found` | 対象がない／アクセスできないと表示 |
| 405 | `method_not_allowed` | 呼び出すHTTPメソッドを修正 |
| 409 | `revision_conflict`, `proposal_superseded`, `task_closed`, `task_expired`, `invalid_state`, `members_not_joined`, `idempotency_key_reused` | 最新状態を取得し、利用者に再確認を求める |
| 413 | `payload_too_large` | 入力サイズを減らす |
| 415 | `unsupported_media_type` | JSON本文のContent-Typeを修正 |
| 422 | `validation_failed`, `unsupported_playbook` | 対応する入力欄に表示 |
| 500 | `internal_error` | 失敗を表示しrequest_idを記録 |
| 503 | `temporarily_unavailable` | 通信障害として扱う。更新を再送するなら同じキーを使う |

detailsは常にobject。revision_conflictではcurrent_revision必須。validation_failedでは `fields: [{ "path": "preparation.data.max_presentation_minutes", "message": "持ち時間以内で指定してください" }]` （輪読の例）のように、本文内のパスで返す。その他は空objectでもよい。認証失敗時や非所属者への404にはリソース情報を含めない。

自分以外のタスクIDは同じグループ内でも404。メンバーであっても管理者専用APIを呼んだ場合は403。

## 13. 画面URLとDiscord通知のリンク

フロントは静的ファイルとして配信し、画面の切り替えはファイル＋クエリ文字列で行う（存在しないパスを `index.html` に振り替える処理は入れない）。

バックエンドがURLを生成するのは次の2つだけで、この2つのファイル名は固定する。その他の画面構成はフロント担当が決める。

| URL | 用途 | 生成する場所 |
| --- | --- | --- |
| `/` | トップ（ログイン・グループ選択） | OAuth callbackの既定の戻り先 |
| `/session.html?id={session_id}` | 開催回の画面 | Discord通知のリンク、ログイン後の戻り先 |

通知のリンクは環境変数 `PUBLIC_BASE_URL` とこのパスを組み合わせて作る。未ログインで開いた場合、フロントは `GET /api/auth/discord?return_to=/session.html?id=...` へ遷移させる。

## 14. 開発・デモ用API

時計の早送り、人物切り替え、障害注入、初期データ投入は、デモと評価に必要だが本番の安全性を損なう。そこで環境変数 `DEV_MODE=1` のときだけルーティングに登録する。無効時は存在しない扱い（404）とし、本番用のハンドラーに分岐を混ぜない。

| メソッド・パス | 本文 | 用途 |
| --- | --- | --- |
| `GET /api/dev/status` | — | 現在時刻、時計のずれ、有効な障害注入を返す |
| `POST /api/dev/clock/advance` | `{ "seconds": 86400 }` | 時計を進める（戻せない）。期限・催促・再試行の判定が進んだ時刻で動く |
| `POST /api/dev/login` | `{ "discord_user_id": "..." }` | 登録済みユーザーとして通常と同じセッションを発行する（人物切り替え） |
| `PUT /api/dev/faults` | `{ "llm": null \| "error" \| "invalid_output", "notify": null \| "fail" \| "unknown" }`（用途固有のキーは各部で追加） | LLM・通知の障害を注入する |
| `POST /api/dev/seed` | `{ "scenario": "replan_demo" }` | DBを初期化し、評価・デモ用の初期状態を投入する |

- Idempotency-Keyは不要。Originの検証は行う。
- 開発モード中は `GET /api/health` が `dev_mode: true` を返し、フロントは全画面に「デモモード」を表示する。デモで使った場合は発表でも明示する。
- シナリオの中身（誰がどの状態か）は評価ケースと合わせて [evaluation.md](evaluation.md) で定める。

---

# 第2部 輪読Playbook（`playbook_id = "reading"`）

第1部の `SessionData`・`PreparationData`・`PlanData` の輪読版と、輪読固有の検証・承認条件を定める。ここで定める規則は `playbook/reading` が実装し、第1部の共通処理は輪読固有の項目を解釈しない。

## R1. データ型

```ts
// SessionData：開催回の登録時に入力する教材と範囲
type ReadingSessionData = {
  book_title: string;
  isbn: string | null;                     // 目次の取得に使ったISBN
  toc_source: {                            // 節一覧の出どころ（R7）
    kind: "web" | "image" | "manual";
    urls: string[];                        // web のときの取得元。それ以外は []
  };
  sections: { id: ID; title: string }[];   // 節・テーマ
  completed_section_ids: ID[];             // 前回までに読んだ範囲
  target_section_ids: ID[];                // 今回扱う予定の範囲
};

// PreparationData：各メンバーの準備状況と担当できる範囲
type ReadingPreparationData = {
  willing_to_present: boolean;
  prepared_section_ids: ID[];              // 読んできた節
  explainable_section_ids: ID[];           // 説明できる節
  max_presentation_minutes: number;        // 説明できる時間
};

// PlanData：今回の計画（範囲と進行表）
type ReadingPlanData = {
  covered_section_ids: ID[];               // 今回扱う範囲
  deferred_section_ids: ID[];              // 次回へ持ち越す範囲
  agenda: {
    id: ID;
    activity: "presentation" | "review" | "discussion" | "joint_reading";
    section_ids: ID[];
    presenter_member_id: ID | null;
    minutes: number;
  }[];
};
```

## R2. 入力の制限

- 節タイトルは前後の空白を除いた1〜200文字、書名は1〜100文字。
- 節は1〜100件、進行項目は1〜20件。
- `sections[].id` はグループ内でなく開催回内の識別子。作成者が一意な文字列を付け、フロントはセレクトボックスの値として保持する。

## R3. 検証ルール

**開催回データ（ReadingSessionData）**

- 節IDの重複は禁止。`completed_section_ids` と `target_section_ids` はすべて登録済みの節で、互いに重ならず、対象範囲は1件以上。
- `isbn` は指定する場合、ハイフンなし13桁でチェックディジットが正しいこと。`toc_source.kind=web` なら `urls` は1件以上の https URL、それ以外は空配列。

**参加条件（ReadingPreparationData）**

- 節IDはすべて開催回に登録済みで、配列内の重複は禁止。
- `explainable_section_ids` は `prepared_section_ids` の部分集合。
- 担当できる場合（attendance=attending かつ willing_to_present=true）は、説明できる節が1件以上、説明できる時間が1〜持ち時間の整数。
- それ以外（欠席または担当しない）は willing_to_present=false、説明できる節は空、時間は0。

**辞退時の変換（第1部 第7節から呼ばれる）**

- どちらのscopeでも willing_to_present=false、explainable_section_ids=[]、max_presentation_minutes=0 にする。prepared_section_ids は維持する。
- 未回答（null）の場合は prepared_section_ids=[] として同じ値を作る。

**計画（ReadingPlanData）**

- `covered_section_ids` と `deferred_section_ids` は重ならず、和集合が開催回の `target_section_ids` と一致する。復習回ではcoveredが空でもよい。
- 進行表の節は今回扱うcoveredまたはcompletedの範囲内。各covered節は少なくとも1つの進行項目に含まれる。
- 進行項目の節は1件以上、分数は1以上、合計は開催回の持ち時間以下。
- `presentation` には担当者が必要。ほかのactivityの担当者は任意だが、指定した場合は本人の引き受けが必要。
- 担当者は参加予定者で、担当する節は本人の explainable_section_ids の範囲内、担当時間の合計は本人の max_presentation_minutes 以下。
- 同じ人を3回連続で代役にしない。履歴は確定済みの開催回ごとに数え、再試行や案の作り直しは回数に含めない。

## R4. 承認条件の算出

| 案の種類 | 本人の引き受け | 承認 |
| --- | --- | --- |
| 初回案（change_kind=initial） | 担当者全員 | 管理者（owner） |
| 担当だけの変更（範囲・進行表の内容と時間配分が同じ） | 新しい案の担当者全員 | 不要 |
| 範囲・形式・時間配分の変更 | 新しい案の担当者全員 | 案の作成時点の参加予定者の過半数（majority） |

担当者は `agenda[].presenter_member_id` が指定されている人。

## R5. AIの計画方針

本人の引き受け、承認条件、時間制限を必須条件とする。その範囲で「開催日時の維持 → 準備済み範囲の活用 → 代役の偏りの抑制」を優先する。準備状況が不明なら確認し、書名から章の内容や本人の理解度を推測して確定しない。

## R6. 例

初期登録（`POST /api/groups/{group_id}/sessions` の `data`）：

```json
{
  "book_title": "サンプル技術書",
  "isbn": null,
  "toc_source": { "kind": "manual", "urls": [] },
  "sections": [
    { "id": "sec_1", "title": "前回の範囲" },
    { "id": "sec_2", "title": "今回の前半" },
    { "id": "sec_3", "title": "今回の後半" }
  ],
  "completed_section_ids": ["sec_1"],
  "target_section_ids": ["sec_2", "sec_3"]
}
```

参加条件の回答（`PUT .../preparations/me` の `preparation`）：

```json
{
  "attendance": "attending",
  "data": {
    "willing_to_present": true,
    "prepared_section_ids": ["sec_1", "sec_2"],
    "explainable_section_ids": ["sec_2"],
    "max_presentation_minutes": 15
  }
}
```

管理者の代案・AIの案（`PlanData`）：

```json
{
  "covered_section_ids": ["sec_2"],
  "deferred_section_ids": ["sec_3"],
  "agenda": [
    { "id": "item_1", "activity": "presentation", "section_ids": ["sec_2"], "presenter_member_id": "mem_c", "minutes": 15 },
    { "id": "item_2", "activity": "review", "section_ids": ["sec_1"], "presenter_member_id": null, "minutes": 20 },
    { "id": "item_3", "activity": "discussion", "section_ids": ["sec_2"], "presenter_member_id": null, "minutes": 25 }
  ]
}
```

初期登録直後、Bが取得した `GET /api/sessions/{session_id}` の例（名前・ID・時刻は架空のモック用）：

```json
{
  "session": {
    "id": "ses_demo", "group_id": "grp_demo", "playbook_id": "reading",
    "title": "第2回", "starts_at": "2026-09-21T11:00:00Z", "duration_minutes": 60,
    "revision": 1, "status": "draft", "updated_at": "2026-09-19T09:00:00Z"
  },
  "data": {
    "book_title": "サンプル技術書",
    "isbn": null,
    "toc_source": { "kind": "manual", "urls": [] },
    "sections": [
      { "id": "sec_1", "title": "前回の範囲" },
      { "id": "sec_2", "title": "今回の前半" },
      { "id": "sec_3", "title": "今回の後半" }
    ],
    "completed_section_ids": ["sec_1"], "target_section_ids": ["sec_2", "sec_3"]
  },
  "members": [
    { "id": "mem_a", "display_name": "A", "role": "owner", "joined": true },
    { "id": "mem_b", "display_name": "B", "role": "member", "joined": true },
    { "id": "mem_c", "display_name": "C", "role": "member", "joined": true },
    { "id": "mem_d", "display_name": "D", "role": "member", "joined": true }
  ],
  "preparations": [
    { "member_id": "mem_a", "value": null },
    { "member_id": "mem_b", "value": null },
    { "member_id": "mem_c", "value": null },
    { "member_id": "mem_d", "value": null }
  ],
  "confirmed_plan": null, "current_proposal": null,
  "active_case": { "id": "case_demo", "status": "collecting", "reason_code": null, "summary": "参加条件を確認しています。", "next_retry_at": null },
  "my_tasks": [{
    "id": "task_b", "session_id": "ses_demo", "kind": "preparation", "status": "open",
    "title": "今回の準備状況を教えてください。", "due_at": "2026-09-20T09:00:00Z",
    "proposal_id": null, "proposal_version": null, "allowed_decisions": ["submit"]
  }],
  "permissions": {
    "can_update_preparation": true, "can_withdraw_assignment": false,
    "can_withdraw_attendance": true, "can_submit_proposal": false, "can_view_activity": false
  },
  "current_member_id": "mem_b",
  "notification_summary": { "pending_count": 4, "sent_count": 0, "failed_count": 0, "unknown_count": 0 },
  "server_now": "2026-09-19T09:00:00Z"
}
```

## R7. 目次の取得

開催回の登録時に節一覧を手で入力する負担を減らすため、ISBNから目次を取得する。**取得結果は候補にすぎず、管理者が確認・修正して開催回登録（`POST /api/groups/{group_id}/sessions` の `data.sections`）に使うまで保存されない。** 2回目以降の開催回は、フロントが前回の開催回の `data` から教材を引き継ぐ。

### 取得の流れ

```text
ISBN入力 ─▶ 書誌の確定（openBD → 国立国会図書館サーチ）
              │ 見つからない ──────────────────────────┐
              ▼                                        │
          Web検索で目次を取得（取得元URL付き）          │
              │                                        │
              ▼                                        ▼
          取得元ページとの照合 ── 不一致・未取得 ──▶ 画像の提出を依頼（needs_image）
              │ 一致                                   │ 目次ページの写真
              ▼                                        ▼
          候補（source=web）                  画像から文字起こし（source=image）
              │                                        │ 読み取れない ──▶ 手入力
              └──────────────▶ 管理者が確認・修正 ◀─────┘
```

### 推測で目次を作らないための規則

- LLMに書名や記憶から目次を作らせる経路は設けない。Web検索の指示は「取得元のページに書かれた目次だけを返し、見つからなければ not_found を返す」とする。
- **Web検索の結果はコードで照合する。** バックエンドが取得元URLのページを取得し、候補の各項目名が本文に含まれるかを確かめる（Unicode正規化・空白除去後の部分一致）。9割未満なら `source_mismatch` として採用せず、画像の提出を求める。
- 取得元ページの取得は、https・公開IPアドレスのみ（名前解決後に私設・ループバック・リンクローカルを拒否）、リダイレクト3回まで、5秒・2 MBまでに制限する。
- 画像からの文字起こしは「写っている文字だけを書き写し、読めない箇所は推測せず読めないと記録する」とする。読めない箇所の数を返し、フロントは管理者の手元の画像と並べて確認させる。
- 画像はLLMへの送信にだけ使い、DB・ログ・ファイルに保存しない。処理が終われば破棄する。
- どの経路でも、節一覧の出どころを `toc_source` として開催回に記録する。

### エンドポイント

すべて対象グループの管理者専用。POSTには第1部のCSRF・Idempotency-Keyを適用する。

| メソッド・パス | 成功 | 用途 |
| --- | --- | --- |
| `POST /api/groups/{group_id}/reading/toc-lookups` | 202 | ISBNから目次の取得を開始 |
| `GET /api/groups/{group_id}/reading/toc-lookups/{lookup_id}` | 200 | 取得状況と候補を取得（3秒間隔のポーリング） |
| `POST /api/groups/{group_id}/reading/toc-lookups/{lookup_id}/images` | 202 | 目次ページの画像を提出（`needs_image` のときだけ） |

開始の本文：

```json
{ "isbn": "9784297127831" }
```

画像の提出は `multipart/form-data` で、フィールド名 `images` に1〜5枚。JPEG・PNG・WebP、1枚4 MB・合計10 MBまで。上限超過は `413 payload_too_large`、形式違いは `415 unsupported_media_type`、`needs_image` 以外の状態では `409 invalid_state`。

```ts
type TocLookup = {
  id: ID;
  status: "resolving_book" | "searching" | "verifying" | "needs_image" | "reading_image" | "succeeded" | "failed";
  book: {
    isbn: string;
    title: string;
    authors: string[];
    publisher: string | null;
    pages: number | null;
  } | null;                                  // 書誌DBで確定できた場合
  source: "web" | "image" | null;            // 候補の出どころ
  source_urls: string[];                     // source=web のとき1件以上
  entries: { title: string; level: 1 | 2 | 3 }[];  // 候補（章=1、節=2、項=3）
  unreadable_count: number;                  // source=image で読めなかった箇所の数
  reason_code: null | "book_not_found" | "toc_not_found" | "source_mismatch" |
               "image_unreadable" | "budget_exceeded" | "model_error";
  expires_at: Timestamp;                     // 作成から24時間
};
```

- `needs_image` になった理由は `reason_code`（`book_not_found`・`toc_not_found`・`source_mismatch`）で示す。書誌が見つからない場合はWeb検索をせず、画像の提出を求める。
- `failed` は画像からも読み取れない（`image_unreadable`）か、上限・障害で止まった場合。フロントは手入力へ案内する（`toc_source.kind=manual`）。
- LLMの一時的な障害は第1部 第10節と同じく指数バックオフで再試行する。ただし打ち切りは作成から10分とする。
- 費用の上限として、グループごとに開始は1日10回まで。超過は `failed`（`budget_exceeded`）。LLM呼び出しと費用は通常の実行記録に残す。
- `entries` の項目はフロントで選択・編集し、`sections` に変換してから開催回登録に使う。IDはフロントが付ける。

### 障害注入（開発モード）

`PUT /api/dev/faults` に `"reading.toc_search": null | "not_found" | "source_mismatch"` を追加する。デモで画像提出への切り替えを見せるときに使う。

### 未検証

Web検索で日本語技術書の目次を取得できる割合、1回あたりの費用・所要時間は未計測。APIキー取得後に数冊で確認し、成功率が低ければWeb検索を省いて画像提出を基本にする（APIの形は変えない）。調査記録は [book-toc-sources.md](../kasa/research/book-toc-sources.md)。

---

# 第3部 画面対応とモック

## 画面ごとの利用API

画面は汎用部分（ログイン・グループ・状態表示・タスク）と、用途固有の部分（`data` の表示・入力）に分ける。用途固有の部分は `features/<playbook_id>/` に置く。

| 画面・操作 | 使用するAPI | 用途固有の部分 |
| --- | --- | --- |
| 初期表示・ログイン | me、playbooks、auth/discord | — |
| グループ選択・初期登録 | groupsの一覧・作成・詳細 | — |
| 開催回選択・登録 | groups/{id}/sessionsの一覧・作成 | 登録フォームの `data`（輪読：書名・節・範囲） |
| 教材の登録（目次の取得） | — | 輪読：reading/toc-lookups（ISBN入力 → 候補の確認・修正、取得できなければ画像提出 → 手入力） |
| 今回の計画・状態表示 | sessions/{id} | `data` と `PlanData` の表示（輪読：範囲・進行表） |
| 自分の参加条件フォーム | preparations/me、またはpreparationタスクへのresponses | `PreparationData` の入力（輪読：準備した節・説明できる節と時間） |
| 自由文で書いて確認する | preparations/me/interpretations → 確認・修正 → preparations/me | 下書きを表示し、本人が確定してから保存する。解釈だけでは保存されない |
| 担当辞退・欠席 | withdrawals | — |
| 引き受け・投票・管理者承認 | tasks/{id}/responses | — |
| 管理者の代案入力 | sessions/{id}/proposals | `PlanData` の入力（輪読：進行表） |
| 管理者の実行状況 | sessions/{id}/activity | — |
| デモモード表示・操作 | health、dev/* | — |

## ポーリング

開催回画面は表示中だけ3秒間隔で詳細を取得し、前回リクエストが終了するまで次を送らない。画面を離れたら停止、タブが非表示なら一時停止する。更新の成功直後は一度再取得する。401/403/404では停止し、通信エラー時は3→6→12→最大30秒に間隔を広げ、成功したら3秒に戻す。

## モックの状態

フロント用モックは以下の状態を同じ契約で用意する。APIの実装待ちで画面開発を止めないためのフィクスチャ。

1. 未ログイン：meが401。
2. 初期登録：draft、Preparation=null、本人のpreparationタスクがopen。
3. 初回案の承認待ち：current_proposal=pending（author=agent, change_kind=initial）。担当者にはassignment、管理者にはowner_approval。
4. 通常時：confirmed、confirmed_planあり、openなmy_tasksなし。
5. 辞退直後：needs_attention、active_case=collecting/planning、以前の確定計画は履歴表示。
6. 再提案：current_proposal=pending（change_kind=replan）。Cにはassignment、参加者にはapproval。1人に複数タスクがある場合も表示する。
7. 確定後：session/active_case=confirmed。通知がpendingからsentへ変化してもrevisionは同じ場合がある。
8. 再試行待ち：active_case=planning、next_retry_atあり。
9. 停止・障害：needs_owner、通知failed/unknown、費用不明。
10. 競合：旧版への回答が409となり、入力を保ったまま最新案の再確認を促す。
11. 目次の取得（輪読）：`succeeded`（source=web、取得元URLあり）、`needs_image`（`toc_not_found`・`source_mismatch`）、画像からの `succeeded`（unreadable_countあり）、`failed`（手入力へ）。

## 一連の通信例

1. 全員がログインし、管理者がグループと開催回を作成する。
2. 全員が参加条件を回答し、AIが初回案を作って担当者のassignmentと管理者のowner_approvalタスクを作る。
3. 担当者の引き受けと管理者承認が揃うと、バックエンドが確定・通知する。
4. Bがwithdrawalsへ `scope=assignment` を送る。202を受け、画面は詳細を再取得する。
5. AIがCにpreparationタスクを出す。Cは第2節・15分という条件を回答する。
6. 新しい案に対してCがassignmentをaccept、参加予定者がapprovalに回答する。条件付きの回答と正式な引き受けを区別する。
7. 必要条件が揃ったらバックエンドが確定し、詳細GETに確定計画と通知状態が現れる。

## 実装分担と変更ルール

- フロント担当は本書の型・成功応答・エラー・画面対応に基づいてモックと画面を作る。画面で多数決や確定可否を独自判定せず、permissions・task・proposalの状態を使う。
- バックエンド担当はAPI DTOを本書に合わせ、Playbook内の型と変換する。既存雛形の `presenter_id` など内部のフィールド名で公開契約を変更しない。
- 第1部の処理に `playbook_id` による分岐や輪読固有の項目を入れない。用途固有の処理は `coord.Playbook` を通して呼ぶ。
- フィールド・enum・必須条件の変更は本書とモックを同時に更新し、フロント・バック双方へ共有する。第1部の変更と第2部の変更は分けて記録する。各担当が独自にURLやレスポンスを変えない。
- 今回は本書の契約確定のみ。DB設計、API実装、モック実装は別作業とする。金額上限とモデル選定は未確定でも、この公開APIの形には影響しない。
