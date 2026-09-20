# データ構造

APIがやりとりする型。エンドポイントは [api-endpoint.md](api-endpoint.md)、保存側のテーブルは [database.md](database.md) を参照。

## 表記

- `ID` と `Timestamp` はstring、`Revision` はinteger。`?` のないキーは必須、`null` は値がないこと、空の配列は `[]`。
- IDは1〜128文字の不透明な文字列。フロントは形式を解釈しない。DiscordユーザーIDも文字列として扱う。
- 日時はRFC 3339。返却はUTCの `Z` 表記で、表示時に利用者のタイムゾーンへ変換する。`starts_at` の入力はオフセット必須。
- `revision` と `version` は1から始まる整数。金額は小数文字列。
- 返却データに私的な理由、プロンプト全文、モデルの生出力、秘密情報は含まれない。
- `SessionData`・`PreparationData`・`PlanData` は**用途ごとに中身が違う**。どの型かは開催回の `playbook_id` で決まる（輪読の定義は後半）。

## 共通の型

```ts
type Member = {
  id: ID;                    // グループ内の member_id
  display_name: string;
  role: "owner" | "member";
  joined: boolean;           // 招待対象のDiscord IDでログイン済みか
  left: boolean;             // 脱退済みか。Group.members には出ないが、
                             // SessionDetail.members には固定時の顔ぶれとして残る
};

type Group = { id: ID; name: string; current_member_id: ID; members: Member[] };

type Preparation = {
  attendance: "attending" | "absent";  // 共通：参加するかどうか
  data: PreparationData;               // 用途固有：準備状況・担当できる範囲
};

type SessionSummary = {
  id: ID;
  group_id: ID;
  playbook_id: string;
  title: string;
  starts_at: Timestamp;
  duration_minutes: number;
  revision: Revision;
  status: "draft" | "confirmed" | "needs_attention";
  updated_at: Timestamp;
};

type Proposal = {
  id: ID;
  version: number;
  status: "pending" | "confirmed" | "superseded" | "rejected";
  change_kind: "initial" | "replan";
  author: "agent" | "owner";
  summary: string;             // 共有してよい変更点の説明
  data: PlanData;              // 用途固有の計画
  approvals: ApprovalProgress[];
  assignments: { member_id: ID; status: "pending" | "accepted" | "declined" }[];
};

type ApprovalProgress = {
  kind: "majority" | "owner";
  eligible_member_ids: ID[];
  required_count: number;       // majority は floor(対象人数 / 2) + 1
  approved_member_ids: ID[];
  rejected_member_ids: ID[];
};

type CaseSummary = {
  id: ID;
  status: "collecting" | "planning" | "awaiting_consent" | "confirmed" | "needs_owner";
  reason_code: null | "no_feasible_plan" | "deadline_expired" | "model_error" | "budget_exceeded";
  summary: string;
  next_retry_at: Timestamp | null;
};

type Task = {
  id: ID;
  session_id: ID;
  kind: "preparation" | "assignment" | "approval" | "owner_approval";
  status: "open" | "answered" | "expired" | "obsolete";
  title: string;
  due_at: Timestamp;                 // 作成から24時間と開催1時間前の早い方
  proposal_id: ID | null;
  proposal_version: number | null;
  allowed_decisions: ("submit" | "accept" | "decline" | "approve" | "reject")[];
};

type MutationAccepted = {            // 更新系の202応答
  session_id: ID;
  revision: Revision;
  case_id: ID;
  processing_status: "queued";
};
```

- `member_id` はグループ内のID、`/api/me` の `user.id` はアプリのユーザーID。互換に扱わない。
- 参加条件の未回答は `Preparation = null`。欠席（`attendance: "absent"`）とは区別する。
- `assignments` は案に担当がある人ごとに1件。引き受けは本人しかできず、集団の投票や管理者の承認では代行されない。

## 画面の主データ

`GET /api/sessions/{session_id}` の応答。

```ts
type SessionDetail = {
  session: SessionSummary;
  data: SessionData;                                        // 用途固有
  members: Member[];
  preparations: { member_id: ID; value: Preparation | null }[];
  confirmed_plan: Proposal | null;                          // 直近の確定案
  current_proposal: Proposal | null;                        // 最新の案
  active_case: CaseSummary | null;                          // 未完了の案件は最大1つ
  my_tasks: Task[];                                         // 自分宛てだけ、作成順
  permissions: {
    can_update_preparation: boolean;
    can_withdraw_assignment: boolean;
    can_withdraw_attendance: boolean;
    can_submit_proposal: boolean;                           // 管理者かつ needs_owner のときだけ
    can_view_activity: boolean;                             // 管理者だけ
  };
  current_member_id: ID;
  notification_summary: { pending_count; sent_count; failed_count; unknown_count: number };
  server_now: Timestamp;
};
```

- `confirmed_plan` は担当辞退で実行不能になっても履歴として残る。その場合 `session.status` が `needs_attention` になるので、画面は「現在も実行可能」と見せない。
- `permissions` は表示用。サーバーは実際の更新時に権限と条件を再検証する。
- `notification_summary` の「送信済み」はDiscord APIの成功応答であって、既読ではない。
- GETではLLMを起動しない。revisionが同じでも後続の変化はあり得るので、ポーリング結果はrevisionだけで比較しない。

## 更新系の入力

```ts
// PUT /api/sessions/{id}/preparations/me
type PutPreparation = { expected_revision: Revision; preparation: Preparation };

// POST /api/sessions/{id}/preparations/me/interpretations（保存しない）
type InterpretInput = { text: string };               // 1〜2000文字
type Interpretation = {
  preparation: Preparation;                           // 検証済みで、そのまま保存できる値
  unclear: string[];                                  // 読み取れなかった項目名
  needs_followup: boolean;
  saved: false;                                       // 常に false
};

// POST /api/sessions/{id}/withdrawals
type WithdrawalInput = { expected_revision: Revision; scope: "assignment" | "attendance" };

// POST /api/groups/{group_id}/leave（本人のみ。管理者は 409 invalid_state）
type LeaveGroupInput  = {};                           // 項目なし。空オブジェクトを送る
type LeaveGroupResult = {
  group_id: ID;
  affected_session_ids: ID[];                         // 本人が外れて再調整が始まった開催回
};

// POST /api/tasks/{task_id}/responses（タスクの kind により形が決まる）
type PreparationResponse = { decision: "submit"; expected_revision: Revision; preparation: Preparation };
type AssignmentResponse  = { decision: "accept" | "decline"; proposal_id: ID; proposal_version: number };
type ApprovalResponse    = { decision: "approve" | "reject"; proposal_id: ID; proposal_version: number };

// POST /api/sessions/{id}/proposals（管理者、needs_owner のときだけ）
type SubmitProposal = { expected_revision: Revision; data: PlanData };
```

## 実行履歴

`GET /api/sessions/{session_id}/activity` の応答。履歴は新しい順に最大200件。

```ts
type ActivityResponse = {
  items: {
    id: ID;
    occurred_at: Timestamp;
    case_id: ID;
    kind: "input_received" | "proposal_created" | "response_recorded" |
          "plan_confirmed" | "agent_retry_scheduled" | "agent_stopped" | "notification_updated";
    summary: string;
    proposal_id: ID | null;
  }[];
  summary: {
    case_id: ID | null;
    llm_call_count: number;
    tool_call_count: number;
    costs: { currency: string; estimated_amount: string | null; billed_amount: string | null }[];
    notifications: {
      id: ID;
      status: "pending" | "sent" | "failed" | "unknown";
      updated_at: Timestamp;
      error_code: null | "delivery_failed" | "delivery_unknown";
    }[];
  };
};
```

- `costs` は通貨ごとに分け、異なる通貨を合算しない。費用が不明なら `amount: null`（0円として扱わない）、通貨自体が不明なら `currency: "unknown"`。
- 通知は `failed`（送信されなかったことが確実）と `unknown`（成否不明）を区別する。`unknown` を未送信と決めつけない。
- 通知の本文、チャンネルの秘密情報、モデルの生出力は返さない。

## 輪読の用途固有データ（`playbook_id = "reading"`）

```ts
// SessionData：開催回の教材と範囲
type ReadingSessionData = {
  book_title: string;                      // 1〜100文字
  isbn: string | null;
  toc_source: { kind: "web" | "image" | "manual"; urls: string[] };  // 節一覧の出どころ
  sections: { id: ID; title: string }[];   // 1〜100件、タイトルは1〜200文字
  completed_section_ids: ID[];             // 前回までに読んだ範囲
  target_section_ids: ID[];                // 今回扱う予定の範囲（1件以上）
};

// PreparationData：各メンバーの準備状況
type ReadingPreparationData = {
  willing_to_present: boolean;
  prepared_section_ids: ID[];              // 読んできた節
  explainable_section_ids: ID[];           // 説明できる節（読んできた節の範囲内）
  max_presentation_minutes: number;        // 説明に使える時間。担当しないなら0
};

// PlanData：今回の計画
type ReadingPlanData = {
  covered_section_ids: ID[];               // 今回扱う範囲
  deferred_section_ids: ID[];              // 次回へ持ち越す範囲
  agenda: {                                // 1〜20件
    id: ID;
    activity: "presentation" | "review" | "discussion" | "joint_reading";
    section_ids: ID[];
    presenter_member_id: ID | null;        // presentation には必須
    minutes: number;                       // 1以上、合計は duration_minutes 以下
  }[];
};
```

主な検証（サーバー側で必ず行う）:

- 節IDは開催回に登録済みのものだけ。`completed` と `target` は重ならない。
- `covered` と `deferred` は重ならず、合わせて `target_section_ids` と一致する。
- 担当者は参加予定かつ `willing_to_present: true` の人だけ。担当する節は本人の `explainable_section_ids` の範囲内、担当時間の合計は本人の `max_presentation_minutes` 以下。
- 進行表の節は `covered` か `completed` の範囲内。各 `covered` の節は少なくとも1つの進行項目に含まれる。
- 同じ人を3回連続で代役にしない。

## 目次の取得（輪読）

```ts
type TocLookup = {
  id: ID;
  status: "resolving_book" | "searching" | "verifying" | "needs_image" |
          "reading_image" | "succeeded" | "failed";
  book: { isbn: string; title: string; authors: string[];
          publisher: string | null; pages: number | null } | null;
  source: "web" | "image" | null;                  // 候補の出どころ
  source_urls: string[];                           // source="web" のとき1件以上
  entries: { title: string; level: 1 | 2 | 3 }[];  // 候補（章=1、節=2、項=3）
  unreadable_count: number;                        // 画像から読めなかった箇所
  reason_code: null | "book_not_found" | "toc_not_found" | "source_mismatch" |
               "image_unreadable" | "budget_exceeded" | "model_error";
  expires_at: Timestamp;                           // 作成から24時間
};
```

`entries` はフロントで選択・編集し、`sections` に変換してから開催回登録に使う（IDはフロントが付ける）。`failed` になったら手入力へ案内し、`toc_source.kind` を `manual` にする。
