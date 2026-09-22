-- テーブル定義。起動時に毎回適用するため、CREATE TABLE IF NOT EXISTS で書く。
-- 日時は UTC の RFC 3339（ナノ秒付き）文字列で保存する。用途固有のデータは JSON 文字列で保存し、共通側は解釈しない。

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- 利用者（Discord でログインした人）。
CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    discord_user_id TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

-- ログインセッション。Cookie の値は保存せず、ハッシュだけを持つ。
CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

-- OAuth の state と戻り先。
CREATE TABLE IF NOT EXISTS oauth_states (
    state      TEXT PRIMARY KEY,
    return_to  TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS groups (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    created_at    TEXT NOT NULL,
    deleted_at    TEXT,
    -- グループの種別。作成後は変更しない。既存のグループは輪読として扱う。
    playbook_id   TEXT NOT NULL DEFAULT 'reading'
);

-- グループの固定メンバー。user_id が NULL の間は招待中（本人未ログイン）。
CREATE TABLE IF NOT EXISTS members (
    id              TEXT PRIMARY KEY,
    group_id        TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    discord_user_id TEXT NOT NULL,
    user_id         TEXT REFERENCES users(id),
    display_name    TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    seq             INTEGER NOT NULL,
    left_at         TEXT,
    UNIQUE (group_id, discord_user_id)
);
CREATE INDEX IF NOT EXISTS members_user ON members(user_id);
CREATE INDEX IF NOT EXISTS members_discord ON members(discord_user_id);

-- starts_at は常に値を持つ。期間だけで登録した回では、確定するまで仮の候補が入る
-- （schedule_status='proposed'）。日時が確定すると 'confirmed' になる。
CREATE TABLE IF NOT EXISTS sessions (
    id                    TEXT PRIMARY KEY,
    group_id              TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    playbook_id           TEXT NOT NULL,
    title                 TEXT NOT NULL,
    starts_at             TEXT NOT NULL,
    period_start          TEXT NOT NULL DEFAULT '',
    period_end            TEXT NOT NULL DEFAULT '',
    schedule_status       TEXT NOT NULL DEFAULT 'confirmed' CHECK (schedule_status IN ('proposed', 'confirmed')),
    duration_minutes      INTEGER NOT NULL,
    revision              INTEGER NOT NULL,
    status                TEXT NOT NULL CHECK (status IN ('draft', 'confirmed', 'needs_attention')),
    data                  TEXT NOT NULL,
    confirmed_proposal_id TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_group ON sessions(group_id, starts_at);

-- 開催回に固定したメンバー。
CREATE TABLE IF NOT EXISTS session_members (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    member_id  TEXT NOT NULL REFERENCES members(id),
    PRIMARY KEY (session_id, member_id)
);

-- 参加条件。行がなければ未回答。
CREATE TABLE IF NOT EXISTS preparations (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    member_id  TEXT NOT NULL REFERENCES members(id),
    attendance TEXT NOT NULL CHECK (attendance IN ('attending', 'absent')),
    data       TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (session_id, member_id)
);

-- 調整案件。1開催回に未完了（confirmed 以外）の案件は最大1つ。
CREATE TABLE IF NOT EXISTS cases (
    id                   TEXT PRIMARY KEY,
    session_id           TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    status               TEXT NOT NULL CHECK (status IN ('collecting', 'planning', 'awaiting_consent', 'confirmed', 'needs_owner')),
    reason_code          TEXT,
    summary              TEXT NOT NULL,
    next_retry_at        TEXT,
    retry_count          INTEGER NOT NULL DEFAULT 0,
    llm_call_count       INTEGER NOT NULL DEFAULT 0,
    tool_call_count      INTEGER NOT NULL DEFAULT 0,
    withdrawn_member_ids TEXT NOT NULL DEFAULT '[]',
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    seq                  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS cases_session ON cases(session_id, seq);

-- 版付きの案。requirements は作成時に固定した引き受け・承認条件。
CREATE TABLE IF NOT EXISTS proposals (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    case_id           TEXT NOT NULL REFERENCES cases(id),
    version           INTEGER NOT NULL,
    status            TEXT NOT NULL CHECK (status IN ('pending', 'confirmed', 'superseded', 'rejected')),
    change_kind       TEXT NOT NULL CHECK (change_kind IN ('initial', 'replan')),
    author            TEXT NOT NULL CHECK (author IN ('agent', 'owner')),
    summary           TEXT NOT NULL,
    data              TEXT NOT NULL,
    requirements      TEXT NOT NULL,
    revision          INTEGER NOT NULL,
    created_at        TEXT NOT NULL,
    confirmed_at      TEXT,
    UNIQUE (session_id, version)
);

-- 本人宛ての確認・引き受け・投票・承認。decision が回答内容。
CREATE TABLE IF NOT EXISTS tasks (
    id               TEXT PRIMARY KEY,
    session_id       TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    case_id          TEXT NOT NULL REFERENCES cases(id),
    member_id        TEXT NOT NULL REFERENCES members(id),
    kind             TEXT NOT NULL CHECK (kind IN ('preparation', 'assignment', 'approval', 'owner_approval')),
    status           TEXT NOT NULL CHECK (status IN ('open', 'answered', 'expired', 'obsolete')),
    title            TEXT NOT NULL,
    due_at           TEXT NOT NULL,
    proposal_id      TEXT REFERENCES proposals(id),
    proposal_version INTEGER,
    decision         TEXT,
    requested_by     TEXT NOT NULL DEFAULT 'system',
    answered_at      TEXT,
    reminded_at      TEXT,
    created_at       TEXT NOT NULL,
    seq              INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_case ON tasks(case_id, seq);
CREATE INDEX IF NOT EXISTS tasks_proposal ON tasks(proposal_id);
CREATE INDEX IF NOT EXISTS tasks_kind_status ON tasks(kind, status);

-- 処理イベント（AIの計画、期限、催促）。run_at 以降に処理し、再起動後も残る。
CREATE TABLE IF NOT EXISTS events (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    case_id    TEXT NOT NULL,
    kind       TEXT NOT NULL,
    ref_id     TEXT,
    run_at     TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('pending', 'done', 'cancelled')),
    created_at TEXT NOT NULL,
    seq        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_due ON events(status, run_at);

-- 通知待ち。sending のまま再起動した通知は成否不明（unknown）にする。
-- components は通知に添えるボタン（label・custom_id・primary の配列、JSON）。対象を束縛した
-- notify_actions の参照だけを custom_id に持ち、宛先が1人の本人宛て通知にだけ付ける。
CREATE TABLE IF NOT EXISTS notifications (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    case_id    TEXT NOT NULL,
    kind       TEXT NOT NULL,
    dedupe_key TEXT NOT NULL UNIQUE,
    content    TEXT NOT NULL,
    mentions   TEXT NOT NULL DEFAULT '[]',
    components TEXT NOT NULL DEFAULT '[]',
    status     TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'unknown', 'cancelled')),
    error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    seq        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS notifications_status ON notifications(status, seq);

-- 開催回に属さないグループ操作の個人DM。受信者ごとに1行を作り、個別に成否を残す。
CREATE TABLE IF NOT EXISTS group_notifications (
    id                        TEXT PRIMARY KEY,
    group_id                  TEXT NOT NULL REFERENCES groups(id),
    kind                      TEXT NOT NULL,
    recipient_discord_user_id TEXT NOT NULL,
    dedupe_key                TEXT NOT NULL UNIQUE,
    content                   TEXT NOT NULL,
    components                TEXT NOT NULL DEFAULT '[]',
    status                    TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'unknown')),
    error_code                TEXT,
    created_at                TEXT NOT NULL,
    updated_at                TEXT NOT NULL,
    seq                       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS group_notifications_status ON group_notifications(status, seq);

-- 実行履歴。私的な理由・プロンプト全文・モデルの生出力は保存しない。
CREATE TABLE IF NOT EXISTS activity (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    id          TEXT NOT NULL UNIQUE,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    case_id     TEXT NOT NULL,
    kind        TEXT NOT NULL,
    summary     TEXT NOT NULL,
    proposal_id TEXT,
    occurred_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS activity_session ON activity(session_id, occurred_at);

-- LLM 呼び出しの記録。金額が分からなければ NULL（0 として扱わない）。
CREATE TABLE IF NOT EXISTS llm_calls (
    id               TEXT PRIMARY KEY,
    case_id          TEXT,
    lookup_id        TEXT,
    model            TEXT NOT NULL,
    input_tokens     INTEGER,
    output_tokens    INTEGER,
    currency         TEXT NOT NULL,
    estimated_amount TEXT,
    billed_amount    TEXT,
    succeeded        INTEGER NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS llm_calls_case ON llm_calls(case_id);

-- 業務更新の再送判定。利用者・キーごとに最初の成功応答を保存する。
CREATE TABLE IF NOT EXISTS idempotency (
    user_id     TEXT NOT NULL,
    key         TEXT NOT NULL,
    method      TEXT NOT NULL,
    path        TEXT NOT NULL,
    body_hash   TEXT NOT NULL,
    status      INTEGER NOT NULL,
    body        BLOB NOT NULL,
    location    TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    PRIMARY KEY (user_id, key)
);

-- 輪読：目次の取得。画像は保存しない。
CREATE TABLE IF NOT EXISTS reading_toc_lookups (
    id               TEXT PRIMARY KEY,
    group_id         TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    isbn             TEXT NOT NULL,
    status           TEXT NOT NULL,
    book             TEXT,
    source           TEXT,
    source_urls      TEXT NOT NULL DEFAULT '[]',
    entries          TEXT NOT NULL DEFAULT '[]',
    unreadable_count INTEGER NOT NULL DEFAULT 0,
    reason_code      TEXT,
    retry_count      INTEGER NOT NULL DEFAULT 0,
    next_run_at      TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    expires_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS reading_toc_lookups_group ON reading_toc_lookups(group_id, created_at);

-- 輪読ブックと、その回ごとの予定枠。未開始枠は session_id を持たないため、共通の
-- 案件・タスク・通知を作らない。session_creation_mode は旧契約の名残で、新規のブックは常に 'all'。
-- plan_status: legacy（旧ブック・自動進行なし）/ planning（AIの計画待ち）/ awaiting_approval（担当者の承認待ち）/
--              approved（自動進行中）/ needs_attention（管理者の判断待ち）。
CREATE TABLE IF NOT EXISTS reading_books (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    isbn TEXT,
    toc_source TEXT NOT NULL,
    sections TEXT NOT NULL,
    planned_session_count INTEGER NOT NULL CHECK (planned_session_count BETWEEN 1 AND 52),
    session_creation_mode TEXT NOT NULL CHECK (session_creation_mode IN ('sequential', 'all')),
    status TEXT NOT NULL CHECK (status IN ('in_progress', 'completed')),
    completed_section_ids TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    period_start TEXT NOT NULL DEFAULT '',
    period_end TEXT NOT NULL DEFAULT '',
    duration_minutes INTEGER NOT NULL DEFAULT 0,
    adjustment_lead_days INTEGER NOT NULL DEFAULT 7,
    plan_status TEXT NOT NULL DEFAULT 'legacy',
    plan_version INTEGER NOT NULL DEFAULT 0,
    plan_summary TEXT NOT NULL DEFAULT '',
    plan_attempts INTEGER NOT NULL DEFAULT 0,
    plan_next_at TEXT,
    plan_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS reading_books_group ON reading_books(group_id, created_at);

-- assignment_status: unassigned / pending（仮担当・未回答）/ accepted / change_requested（候補待ち）/
--                    change_proposed（候補本人の承認待ち）/ needs_attention（候補を作れない）。
CREATE TABLE IF NOT EXISTS reading_book_slots (
    id TEXT PRIMARY KEY,
    book_id TEXT NOT NULL REFERENCES reading_books(id) ON DELETE CASCADE,
    sequence_number INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('planned', 'active', 'completed')),
    session_id TEXT UNIQUE REFERENCES sessions(id) ON DELETE SET NULL,
    covered_section_ids TEXT NOT NULL DEFAULT '[]',
    period_start TEXT NOT NULL DEFAULT '',
    period_end TEXT NOT NULL DEFAULT '',
    target_section_ids TEXT NOT NULL DEFAULT '[]',
    assignee_member_id TEXT,
    assignment_status TEXT NOT NULL DEFAULT 'unassigned',
    proposed_assignee_member_id TEXT,
    excluded_member_ids TEXT NOT NULL DEFAULT '[]',
    change_attempts INTEGER NOT NULL DEFAULT 0,
    change_next_at TEXT,
    attention_reason TEXT NOT NULL DEFAULT '',
    UNIQUE (book_id, sequence_number)
);
CREATE INDEX IF NOT EXISTS reading_book_slots_book ON reading_book_slots(book_id, sequence_number);

-- ユーザー共通の週間空き時間。複数のグループ・ブックから参照する。windows は JSON。
-- 各セッションの「今回だけ参加不可」は参加条件（preparations）で別に集める。
CREATE TABLE IF NOT EXISTS user_weekly_availability (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    timezone   TEXT NOT NULL,
    windows    TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- 開催3日前の担当者の最終確認。(枠, 担当者) ごとに1行だけ作る。
-- status: open / confirmed / change_requested / needs_owner（1日前でも未回答）/ superseded（担当交代）。
CREATE TABLE IF NOT EXISTS reading_slot_confirmations (
    id           TEXT PRIMARY KEY,
    slot_id      TEXT NOT NULL REFERENCES reading_book_slots(id) ON DELETE CASCADE,
    member_id    TEXT NOT NULL REFERENCES members(id),
    status       TEXT NOT NULL,
    requested_at TEXT NOT NULL,
    reminded_at  TEXT,
    escalated_at TEXT,
    answered_at  TEXT,
    UNIQUE (slot_id, member_id)
);

-- 通知のボタンから直接始める操作の参照（.agent/kasa/decisions/discord-availability-home-refresh.md）。
-- 表示した通知に束縛し、押下時に本人・期限・許可された decision をサーバー側で再検証する。
-- ref は種別ごとの対象（task_id・group_id/book_id/slot_id 等）を持つ JSON で、共通側は中身を解釈しない。
CREATE TABLE IF NOT EXISTS notify_actions (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    ref         TEXT NOT NULL,
    decisions   TEXT NOT NULL DEFAULT '[]',
    consumed_at TEXT,
    expires_at  TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS notify_actions_user ON notify_actions(user_id, expires_at);

-- ブックの計画・担当承認・最終確認の履歴（追跡用）。私的な理由は記録しない。
CREATE TABLE IF NOT EXISTS reading_book_log (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    id          TEXT NOT NULL UNIQUE,
    book_id     TEXT NOT NULL REFERENCES reading_books(id) ON DELETE CASCADE,
    slot_id     TEXT,
    member_id   TEXT,
    kind        TEXT NOT NULL,
    summary     TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS reading_book_log_book ON reading_book_log(book_id, seq);
