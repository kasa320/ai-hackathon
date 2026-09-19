# フロント・バックエンド間のAPI契約

決定日：2026-09-19。契約バージョン：`mvp-1`。

この文書をMVPの実装・フロント用モックの共通仕様とする。APIの実装を完了したという意味ではない。現在の `master` はヘルスチェックのみ、バックエンド雛形ブランチはPlaybook一覧まで実装している。

業務要件は [spec.md](spec.md)、用途分離は [architecture.md](architecture.md)、評価は [evaluation.md](evaluation.md) を参照。内部のGo型やDBテーブルをそのまま公開せず、本書の入出力へ変換する。実装時に生成するOpenAPIも本書と一致させる。

## 1. 今回の決定

- 同一オリジンのHTTP JSON API。ベースパスは `/api`。日時・担当変更などの業務処理はバックエンドが担当する。
- 認証はDiscord OAuth＋サーバーセッションCookie。本人IDや権限をリクエスト本文で指定しない。
- 次回日程が決まっている輪読会を対象とする。グループ作成、固定メンバー登録、開催回登録、準備回答、担当辞退、引き受け、投票、管理者による代案入力までを契約に含める。
- 通知先はサーバー設定済みのDiscordチャンネル。チャンネル選択・DM・日程投票・メンバー入れ替え・当日進行は対象外。
- 入力は構造化フォームに固定する。自由文の解釈APIはMVPに含めない。「第2節なら15分説明できる」は範囲選択と時間で表現する。
- 変更受付とAI処理を分ける。業務の更新は保存後に `202 Accepted` を返し、フロントは開催回詳細を3秒間隔で取得する。WebSocket・SSEは使わない。
- 「登録一覧にある」「202が返った」「計画が確定した」「通知が送信された」を区別する。フロントで確定条件を独自に計算しない。

## 2. 共通規約

### JSONと識別子

- JSONフィールドは `snake_case`。通常レスポンスは `Content-Type: application/json`。成功データを共通の `data` キーで包まず、以下の各レスポンスをそのまま返す。
- 本書の型定義は契約の表記であり、TypeScriptの導入を要求しない。`?` のないキーは必須、`null` は値がないことを意味する。配列が空なら `[]`。
- アプリのIDは長さ1〜128文字の不透明な文字列。フロントは形式を解釈しない。DiscordユーザーIDも文字列で扱い、数値に変換しない。
- 日時はタイムゾーン付きRFC 3339、返却はUTCの `Z` 表記。表示時に利用者のタイムゾーンへ変換する。`starts_at` の入力は明示的なオフセット必須。
- revision・version・分数・件数はJavaScriptの安全な整数範囲内の整数。revisionとversionは1から始まる。金額は小数文字列で返す。
- JSON本文は最大64 KiB。未知の入力フィールドは拒否する。未指定項目を暗黙に上書きするPATCHは用意しない。
- 名前・タイトルは前後の空白を除いた1〜100文字、節タイトルは1〜200文字。グループの人数は管理者込み2〜10人、教材の節は1〜100件、進行項目は1〜20件、持ち時間は15〜180分とする。フォームも同じ制限を使う。
- 認証済みレスポンスは `Cache-Control: no-store`。APIのエラーをHTMLやログインページとして返さない。

### セッションとCSRF

`GET /api/me` はログイン状態とセッションに結び付いたCSRFトークンを返す。Cookie名は `session`、`HttpOnly; SameSite=Lax; Path=/`、本番HTTPSでは `Secure` を付ける。ローカルHTTP開発のみSecureを外す。固定の有効期限はログインから7日、ログアウトでサーバー側も無効化する。

すべての認証済みPOST・PUTに `X-CSRF-Token` を必須とし、サーバーでもOriginを検証する。同一オリジンの `fetch` で `credentials: "same-origin"` を使う。開発時にフロントを別ポートで動かすなら `/api` をプロキシし、任意オリジンを許可するCORS設定は追加しない。

OAuth開始はブラウザー遷移、callbackはバックエンド処理。stateの検証・トークン交換・セッション発行はバックエンド側で行う。DiscordのアクセストークンやBotトークンをフロントに返さない。

### 再送と競合

業務更新のPOST・PUTには `Idempotency-Key` を必須とする。フロントは1回の送信操作にUUIDを生成し、通信失敗時の再送では同じキーと同じ本文を使う。ログアウトはこのキーを不要とする。

バックエンドは利用者・キーの組を記録し、メソッド・パス・JSON内容が同じなら最初の成功ステータスと本文を返す。違う操作へのキー再利用は `409 idempotency_key_reused`。同時に届いた同一キーも1回分として直列化する。業務更新と成功応答の再送情報を同じトランザクションで保存し、保存期間は最低24時間とする。検証で拒否した更新は成功記録を作らない。

認証・対象への現在のアクセス権を確認した後、既に成功したキーの再送判定を行い、その後に新規操作の版と入力を検証する。これにより、成功後の再送が「既に回答済み」で失敗することを防ぐ。

`session.revision` は準備状況・参加条件・有効な提案・確定計画が変わると増える。投票・引き受けの記録だけ、実行ログ、通知状況の更新では増やさない。各提案には別に開催回内で単調増加する `version` がある。

- 準備更新・辞退・管理者による案作成は `expected_revision` を送る。古ければ `409 revision_conflict`。
- 版付き案への回答は `proposal_id` と `proposal_version` を送る。投票同士はsessionのrevisionで競合させない。
- 更新により現在の提案が不成立になったら、入力保存と同時に提案を `superseded`、未回答タスクを `obsolete` にする。AIの再計画完了を待って古い票を受け付け続けない。
- 409を受けたフロントは最新状態を取得し、入力下書きを保持して再確認を求める。古い同意を新版へ自動再送しない。

## 3. エンドポイント一覧

権限の「メンバー」は対象グループに所属する本人。「管理者」はグループ作成者。グループに属さない利用者には対象の存在も含め `404` を返す。

| メソッド・パス | 権限 | 成功 | 用途 |
| --- | --- | --- | --- |
| `GET /api/health` | 不要 | 200 | 接続確認 |
| `GET /api/playbooks` | 不要 | 200 | 登録された用途の一覧 |
| `GET /api/auth/discord` | 不要 | 302 | Discordログイン開始 |
| `GET /api/auth/callback` | OAuth state | 303 | ログイン完了後 `/` へ戻す |
| `GET /api/me` | ログイン済み | 200 | 自分とCSRFトークン |
| `POST /api/auth/logout` | ログイン済み | 204 | ログアウト |
| `GET /api/groups` | ログイン済み | 200 | 自分が参加するグループ一覧 |
| `POST /api/groups` | ログイン済み | 201 | 固定メンバーでグループ作成 |
| `GET /api/groups/{group_id}` | メンバー | 200 | メンバー一覧と自分の役割 |
| `GET /api/groups/{group_id}/sessions` | メンバー | 200 | 開催回一覧 |
| `POST /api/groups/{group_id}/sessions` | 管理者 | 201 | 開催回を登録し、準備確認を開始 |
| `GET /api/sessions/{session_id}` | メンバー | 200 | 共有計画・進行状況・自分の操作をまとめて取得 |
| `PUT /api/sessions/{session_id}/preparations/me` | メンバー本人 | 202 | 自分の準備状況を送信・更新 |
| `POST /api/sessions/{session_id}/withdrawals` | メンバー本人 | 202 | 自分の担当辞退・欠席を登録 |
| `POST /api/tasks/{task_id}/responses` | タスクの本人 | 202 | 確認への回答・担当引き受け・投票 |
| `POST /api/sessions/{session_id}/proposals` | 管理者 | 202 | 初回案／行き詰まった案件の代案を提出 |
| `GET /api/sessions/{session_id}/activity` | 管理者 | 200 | 実行履歴・費用・通知状況を取得 |

ブラウザー向けの「AI実行」「強制確定」「他人として同意」「通知送信」APIは設けない。変更案の自動生成・条件成立時の確定・通知はバックエンド内のイベント処理で行う。

## 4. 共通データ型

以下の `ID` と `Timestamp` はstring、`Revision` はinteger。返却データには私的理由、プロンプト全文、モデルの生出力、秘密情報を含めない。

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
  attendance: "attending" | "absent";
  willing_to_present: boolean;
  prepared_section_ids: ID[];
  explainable_section_ids: ID[];
  max_presentation_minutes: number;
};

type SessionSummary = {
  id: ID;
  group_id: ID;
  playbook_id: "reading";
  title: string;
  starts_at: Timestamp;
  duration_minutes: number;
  revision: Revision;
  status: "draft" | "confirmed" | "needs_attention";
  updated_at: Timestamp;
};

type ReadingData = {
  book_title: string;
  sections: { id: ID; title: string }[];
  completed_section_ids: ID[];
  target_section_ids: ID[];
};

type ReadingPlan = {
  covered_section_ids: ID[];
  deferred_section_ids: ID[];
  agenda: {
    id: ID;
    activity: "presentation" | "review" | "discussion" | "joint_reading";
    section_ids: ID[];
    presenter_member_id: ID | null;
    minutes: number;
  }[];
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
  summary: string;            // 共有可能な変更点の説明
  data: ReadingPlan;         // playbook_id=readingの用途固有データ
  approvals: ApprovalProgress[];
  assignments: {
    member_id: ID;
    status: "pending" | "accepted" | "declined";
  }[];
};

type CaseSummary = {
  id: ID;
  status: "collecting" | "planning" | "awaiting_consent" | "confirmed" | "needs_owner";
  reason_code: null | "no_feasible_plan" | "deadline_expired" | "model_error" | "budget_exceeded";
  summary: string;
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

member_idはグループ内ID、`/me.user.id` はアプリのユーザーID。互換として扱わない。サーバーはセッションから現在のmember_idを解決する。

`explainable_section_ids` は `prepared_section_ids` の部分集合。出席しない／担当しない場合は `willing_to_present=false`、説明可能範囲は空、説明可能時間は0とする。担当可能なら1〜持ち時間の整数と、1件以上の説明可能範囲が必要。未回答は `Preparation=null` で表現し、不参加とは区別する。

### 読む範囲と進行表の検証

- `sections[].id` はグループ内でなく開催回内の識別子。作成者が一意な文字列を付け、フロントはセレクトボックスの値として保持する。作成後の節一覧・対象範囲・日時はMVPでは編集しない。
- すべての節IDはその開催回に登録されていること。配列内の重複は禁止。`completed_section_ids` と `target_section_ids` は互いに重ならず、対象範囲は1件以上。
- `covered_section_ids` と `deferred_section_ids` は重ならず、和集合が開催回の `target_section_ids` と一致する。復習回ではcoveredが空でもよい。
- 進行表の節は今回扱うcoveredまたはcompletedの範囲内。各covered節は少なくとも1つの進行項目に含まれる。進行項目の節は1件以上、分数は1以上、合計は持ち時間以下。
- `presentation` には担当者が必要。ほかのactivityの担当者は任意だが、指定した場合は本人の引き受けが必要。担当者は参加予定者で、担当内容・時間は本人の回答の範囲内であること。複数項目を担当する場合は合計時間で検証する。
- `assignments` はその案の担当者ごとに1件。その人が引き受けるのは当該版の自分の担当項目すべてであり、集団投票とは別に記録する。

## 5. 認証と初期メンバー登録

### ログイン

ログインボタンで `GET /api/auth/discord` に遷移する。任意のリダイレクト先は受け付けない。callback成功時はCookieを発行して `303 Location: /`、失敗時は `303 Location: /?auth_error=access_denied` または `invalid_state` または `provider_unavailable`。エラーの詳細や外部トークンはURLに含めない。

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

作成者は自動でownerになる。inviteesは1〜9人、IDの重複と自分のIDは禁止。Discord IDは17〜20桁の数字文字列として検証し、存在確認は本人のログイン時まで保証しない。作成時のdisplay_nameは仮の表示名で、本人ログイン後は本人の表示名を使う。

成功は `201`、本文は `Group`、`Location: /api/groups/{group_id}`。本人がログインしていないメンバーは `joined=false`。開催回を作る前に全員のjoinedを確認し、未参加者がいれば `409 members_not_joined`。所属の追加・削除・管理者変更はMVP外。

`GET /api/groups/{group_id}` はGroupを返す。招待先のDiscord ID一覧は返さない。`GET /api/groups` は次の一覧形式を使い、自分の所属だけを返す。

### 一覧の共通ページング

groups・sessions・activityのGETは `?limit=20&cursor=...` を受け付ける。limitは1〜100、既定20。cursorはサーバー発行の不透明文字列、未指定は先頭。無効なcursorは `400 invalid_cursor`。

```ts
type Page<T> = { items: T[]; next_cursor: string | null };
```

groupsのitemsは `{ id, name, current_member_id, role, member_count }`（roleはowner/member）、作成日時の降順。sessionsは `SessionSummary`、starts_at降順。activityは第10節の `ActivityItem`、発生日時の降順。同時刻はIDで順序を固定する。カーソルは同じ利用者・対象・並び順の一覧内でのみ有効。

## 6. 開催回の登録・取得

### 登録

`POST /api/groups/{group_id}/sessions`：

```json
{
  "playbook_id": "reading",
  "title": "第2回",
  "starts_at": "2026-09-21T11:00:00Z",
  "duration_minutes": 60,
  "data": {
    "book_title": "サンプル技術書",
    "sections": [
      { "id": "sec_1", "title": "前回の範囲" },
      { "id": "sec_2", "title": "今回の前半" },
      { "id": "sec_3", "title": "今回の後半" }
    ],
    "completed_section_ids": ["sec_1"],
    "target_section_ids": ["sec_2", "sec_3"]
  }
}
```

`data` は `ReadingData`。MVPで受け付けるplaybook_idはreadingのみ。存在しない用途は `422 unsupported_playbook`。starts_atは現在時刻より1時間以上先で、回答期限を確保できる日時とする。

成功は `201`、本文 `{ "session": SessionSummary, "case_id": ID }`、Locationは `/api/sessions/{session_id}`。状態はdraft、revision=1。グループ全員をこの開催回のメンバーとして固定し、初期のPreparationはnullとする。全員へのpreparationタスクと処理イベントを同じトランザクションで登録する。LLM完了や通知完了を待たない。

### 詳細：共有画面と参加者画面の共通データ

`GET /api/sessions/{session_id}` の200レスポンス：

```ts
type SessionDetail = {
  session: SessionSummary;
  reading: ReadingData;
  members: Member[];
  preparations: { member_id: ID; value: Preparation | null }[];
  confirmed_plan: Proposal | null;
  current_proposal: Proposal | null;
  active_case: CaseSummary | null;
  my_tasks: Task[];
  permissions: {
    can_update_preparation: boolean;
    can_withdraw_presentation: boolean;
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
- 1開催回に未完了の調整案件は最大1つ。追加の準備変更・辞退は同じ案件に取り込む。完了後の新しい問題は新しい案件になる。active_caseは直近の案件を返し、確定後はstatus=confirmedのまま返す。
- preparationsは本人が共有を確認して送信した構造化情報のみ。フォームには「準備状況はこの輪読会のメンバーに共有されます」と表示する。
- my_tasksは現在の案件で自分宛てに作られたタスクのみを、作成順に返す。他人の回答を本人が行えるような入力先を返さない。
- permissionsはその時点の画面表示用。サーバーは実際の更新時に権限と条件を再検証する。管理者以外のcan_submit_proposal/can_view_activityはfalse。
- notification_summaryは現在の案件に関連する全通知の状態別件数。送信済みはDiscord APIの成功応答であり、既読ではない。
- GETではLLMを起動せず、保存済み状態を返す。後続の状態変化でrevisionが同じ場合もあるため、ポーリング結果はrevisionだけで比較せず表示へ反映する。

## 7. 準備回答と辞退

### 準備回答

`PUT /api/sessions/{session_id}/preparations/me`：

```json
{
  "expected_revision": 3,
  "preparation": {
    "attendance": "attending",
    "willing_to_present": true,
    "prepared_section_ids": ["sec_1", "sec_2"],
    "explainable_section_ids": ["sec_2"],
    "max_presentation_minutes": 15
  }
}
```

本人のPreparationを全置換する。自分宛てのopenなpreparationタスクがあれば回答済みにする。入力が変わった場合はrevisionを増やし、現在のpending案を旧版にして案件をcollectingへ戻す。値が同じ場合は版を変えず、未回答タスクの回答処理のみ行う。確定計画への影響を検証し、実行不能ならneeds_attentionにする。

成功は202とMutationAccepted。本人以外のmember_idを含めた本文は拒否する。準備回答で担当の引き受けや投票を自動作成しない。

### 担当辞退・欠席

`POST /api/sessions/{session_id}/withdrawals`：

```json
{ "expected_revision": 5, "scope": "presentation" }
```

scopeはpresentationまたはattendance。presentationは今回の自分の担当全体を辞退する操作で、確定計画に本人の担当がある場合に限る。attendanceは今回の欠席表明で、Preparation=nullでも利用できる。私的な理由は送らない。

presentationなら本人のwilling_to_present=false、説明可能範囲=[]、説明可能時間=0にする。attendanceなら加えてattendance=absentにする。既存のprepared_section_idsは維持する。

revisionを増やし、現在の提案と関連する未回答タスクを無効化する。確定計画があればsessionをneeds_attentionへ、初期登録中ならdraftを維持する。案件と再計画イベントを保存し202を返す。既に同じ辞退状態なら、異なるキーでも二重の案件を作らず現在の受付結果を返す。

## 8. タスクへの回答と同意

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

preparationはPreparationResponse、assignmentはAssignmentResponse、approval/owner_approvalはApprovalResponseに対応する。task内のallowed_decisionsもこの対応に従う。

preparationタスクへのsubmitは第7節の準備更新と同じ処理を使う。担当可能と答えても担当承諾にはならない。assignmentのacceptは当該版の本人担当すべてへの引き受けであり、approvalのapproveとは独立に扱う。

回答済み・期限切れ・旧版のタスクは409で拒否する。同じIdempotency-Keyでの成功済み再送だけは元の成功を返す。同意の直接編集・取消APIはMVP外とし、担当を引き受けられなくなった場合は準備更新または辞退で計画を無効化する。

### 初回案と変更案の確定条件

- 初回案：担当者全員のassignment=acceptedと管理者のowner_approval=approveが必要。管理者が案を入力したことを承認として数えない。
- 担当だけの変更：新しい案の全担当者の引き受けが必要。読む範囲と時間配分等に変更がなければ、範囲変更の多数決は不要。
- 範囲・形式・時間配分の変更：上記に加え、提案時点の参加予定者の過半数のapprovalが必要。参加予定者はPreparation.attendance=attendingの人。初回の準備未回答者を勝手に欠席扱いせず、全員の回答が揃うまで案を確定可能な状態にしない。
- 分母と対象者は版ごとに固定し、反対・未回答で縮めない。準備条件や参加予定者が変わったら新しい版で取り直す。参加予定者が0人なら案を作らず管理者判断待ち。
- assignmentのdeclineまたはowner_approvalのrejectで案をrejectedにし、未回答タスクをobsoleteにして再計画へ戻す。多数決で一票rejectが出ても即棄却しない。残り全員が賛成しても成立しない場合は棄却・再計画する。
- 最後の必要な回答が揃うとバックエンドが自動で検証・確定する。再度最新版と参加条件を確認し、計画保存と通知待ち登録を同一トランザクションで行う。最後のPOSTの202時点で確定済みとは限らない。
- 各タスクの期限は作成から24時間と開催1時間前の早い方。サーバー時刻がdue_at以上なら拒否する。必要条件が期限までに揃わなければneeds_ownerへ戻す。

## 9. 管理者による代案

`POST /api/sessions/{session_id}/proposals` はdraft、または案件がneeds_ownerの場合だけ利用可能。管理者が通常のAI調整中の案を同時に上書きする用途には使わない。

```json
{
  "expected_revision": 8,
  "data": {
    "covered_section_ids": ["sec_2"],
    "deferred_section_ids": ["sec_3"],
    "agenda": [
      { "id": "item_1", "activity": "presentation", "section_ids": ["sec_2"], "presenter_member_id": "mem_c", "minutes": 15 },
      { "id": "item_2", "activity": "review", "section_ids": ["sec_1"], "presenter_member_id": null, "minutes": 20 },
      { "id": "item_3", "activity": "discussion", "section_ids": ["sec_2"], "presenter_member_id": null, "minutes": 25 }
    ]
  }
}
```

dataはReadingPlan。全員の準備回答が揃い、輪読の制約を満たすことが必要。初期案か変更案か、誰の承認が必要かはサーバーが決定し、本文で指定しない。案に含める担当者のmember_idは指定できるが、本人の引き受けを代行できない。

成功時に新しい版を発行してrevisionを増やし、以前の案とタスクを無効化する。必要なタスクとイベントを作成し202を返す。管理者の案にもAIと同じ検証・同意条件を適用する。実現不能な場合は422で保存せず、日時変更・中止の自動実行は行わない。

## 10. 実行履歴・費用・通知

`GET /api/sessions/{session_id}/activity` は管理者専用。本文はPageと以下のsummaryを同時に返す。

```ts
type ActivityItem = {
  id: ID;
  occurred_at: Timestamp;
  case_id: ID;
  kind: "input_received" | "proposal_created" | "response_recorded" |
        "plan_confirmed" | "agent_stopped" | "notification_updated";
  summary: string;
  proposal_id: ID | null;
};

type ActivityResponse = {
  items: ActivityItem[];
  next_cursor: string | null;
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

itemsは開催回全体の履歴、summaryは現在の案件全体の集計で、ページに含まれるitemsだけの集計ではない。現在案件がなければcase_id=null、件数0、配列=[]。

costsは通貨ごとに分け、異なる通貨を合算しない。LLM呼び出し済みで費用が不明ならamount=null。請求通貨自体も不明ならcurrency=`unknown`の項目を返す。呼び出しが0回ならcosts=[]。推定額と確定請求額を区別し、片方でも明細の欠落がある集計値はそのamountをnullにする。

通知本文、チャンネルの秘密情報、モデルの生出力は返さない。failedとunknownを区別し、unknownを「未送信」と決めつけない。通知の手動再送APIは今回設けず、管理者がDiscord側で状況を確認できる表示にする。

## 11. エラー契約

healthの既存503レスポンス `{ "status": "db_unavailable" }` とOAuthリダイレクト以外は、失敗を次のJSONに統一する。フロントはmessage文字列を条件分岐に使わずcodeで処理する。

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
| 400 | `invalid_json`, `invalid_cursor`, `idempotency_key_required` | 入力・送信処理を修正 |
| 401 | `unauthenticated` | 個人データを破棄してログインを案内 |
| 403 | `forbidden`, `csrf_invalid` | 操作不可を表示。CSRFの場合はmeを再取得し、勝手に更新を再送しない |
| 404 | `not_found` | 対象がない／アクセスできないと表示 |
| 409 | `revision_conflict`, `proposal_superseded`, `task_closed`, `task_expired`, `invalid_state`, `members_not_joined`, `idempotency_key_reused` | 最新状態を取得し、利用者に再確認を求める |
| 413 | `payload_too_large` | 入力サイズを減らす |
| 422 | `validation_failed`, `unsupported_playbook` | 対応する入力欄に表示 |
| 429 | `rate_limited` | Retry-Afterの秒数まで待つ。入力は保持 |
| 500 | `internal_error` | 失敗を表示しrequest_idを記録 |
| 503 | `temporarily_unavailable` | 通信障害として扱う。更新を再送するなら同じキーを使う |

detailsは常にobject。revision_conflictではcurrent_revision必須。validation_failedでは `fields: [{ "path": "preparation.max_presentation_minutes", "message": "持ち時間以内で指定してください" }]` を返す。その他は空objectでもよい。認証失敗時や非所属者への404にはリソース情報を含めない。

自分以外のタスクIDは同じグループ内でも404。メンバーであっても管理者専用APIを呼んだ場合は403。受理済み処理のLLM失敗は、元の202を取り消さず、案件のneeds_ownerとreason_codeで伝える。

## 12. 画面との対応とモック作成

| 画面・操作 | 使用するAPI |
| --- | --- |
| 初期表示・ログイン | me、playbooks、auth/discord |
| グループ選択・初期登録 | groupsの一覧・作成・詳細 |
| 開催回選択・登録 | groups/{id}/sessionsの一覧・作成 |
| 今回の計画・状態表示 | sessions/{id} |
| 自分の準備フォーム | preparations/me、またはpreparationタスクへのresponses |
| 担当辞退・欠席 | withdrawals |
| 引き受け・投票・管理者承認 | tasks/{id}/responses |
| 管理者の代案入力 | sessions/{id}/proposals |
| 管理者の実行状況 | sessions/{id}/activity |

開催回画面は表示中だけ3秒間隔で詳細を取得し、前回リクエストが終了するまで次を送らない。画面を離れたら停止、タブが非表示なら一時停止する。更新の成功直後は一度再取得する。401/403/404では停止し、通信エラー時は3→6→12→最大30秒に間隔を広げ、成功したら3秒に戻す。429はRetry-Afterを優先する。

フロント用モックは以下の状態を同じ契約で用意する。APIの実装待ちで画面開発を止めないためのフィクスチャであり、本番APIに人物切り替えや架空の同意を登録する機能を加えない。

1. 未ログイン：meが401。
2. 初期登録：draft、Preparation=null、本人のpreparationタスクがopen。
3. 通常時：confirmed、confirmed_planあり、openなmy_tasksなし。
4. 辞退直後：needs_attention、active_case=collecting/planning、以前の確定計画は履歴表示。
5. 再提案：current_proposal=pending。Cにはassignment、参加者にはapproval。1人に複数タスクがある場合も表示する。
6. 確定後：session/active_case=confirmed。通知がpendingからsentへ変化してもrevisionは同じ場合がある。
7. 停止・障害：needs_owner、通知failed/unknown、費用不明。
8. 競合：旧版への回答が409となり、入力を保ったまま最新案の再確認を促す。

### 一連の通信例

1. 全員がログインし、管理者がグループと開催回を作成する。
2. 全員の準備回答を保存し、バックエンドが初回案と担当・管理者承認タスクを作る。
3. 本人の引き受けと管理者承認後、バックエンドが確定・通知する。
4. Bがwithdrawalsへpresentationを送る。202を受け、画面は詳細を再取得する。
5. バックエンドがCにpreparationタスクを出す。Cは第2節・15分という条件を回答する。
6. 新しい提案に対してCがassignmentをaccept、参加予定者がapprovalに回答する。条件付き回答と正式な引き受けを区別する。
7. 必要条件が揃ったらバックエンドが確定し、詳細GETに確定計画と通知状態が現れる。

## 13. 実装分担と変更ルール

- フロント担当は本書の型・成功応答・エラー・画面対応に基づいてモックと画面を作る。画面で多数決や確定可否を独自判定せず、permissions・task・proposalの状態を使う。
- バックエンド担当はAPI DTOを本書に合わせ、Playbook内の型と変換する。既存雛形の `presenter_id` など内部のフィールド名で公開契約を変更しない。
- 共通の登録・同意・通知を扱うルートと、ReadingData/ReadingPlanの検証を分離する。新用途を追加する際はplaybook_idに対応するdataの型を増やす。
- フィールド・enum・必須条件の変更は本書とモック、生成OpenAPIを同時に更新し、フロント・バック双方へ共有する。各担当が独自にURLやレスポンスを変えない。
- 今回は本書の契約確定のみ。DB設計、API実装、モック実装、コミット・pushは別作業とする。金額上限とモデル選定は未確定でも、この公開APIの形には影響しない。
