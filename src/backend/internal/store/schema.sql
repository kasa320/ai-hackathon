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
    deleted_at    TEXT
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

CREATE TABLE IF NOT EXISTS sessions (
    id                    TEXT PRIMARY KEY,
    group_id              TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    playbook_id           TEXT NOT NULL,
    title                 TEXT NOT NULL,
    starts_at             TEXT NOT NULL,
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
CREATE TABLE IF NOT EXISTS notifications (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    case_id    TEXT NOT NULL,
    kind       TEXT NOT NULL,
    dedupe_key TEXT NOT NULL UNIQUE,
    content    TEXT NOT NULL,
    mentions   TEXT NOT NULL DEFAULT '[]',
    status     TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'unknown', 'cancelled')),
    error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    seq        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS notifications_status ON notifications(status, seq);

-- グループ単位のお知らせ。開催回がなくても、論理削除後でも送信できる。
CREATE TABLE IF NOT EXISTS group_notifications (
    id         TEXT PRIMARY KEY,
    group_id   TEXT NOT NULL REFERENCES groups(id),
    kind       TEXT NOT NULL CHECK (kind IN ('member_left', 'group_deleted')),
    dedupe_key TEXT NOT NULL UNIQUE,
    content    TEXT NOT NULL,
    mentions   TEXT NOT NULL DEFAULT '[]',
    status     TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'unknown', 'cancelled')),
    error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    seq        INTEGER NOT NULL
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
