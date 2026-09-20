// フロント用モック。api.js と同じ呼び出し方で、契約どおりの形を返す。
// 契約：.agent/decisions/api.md（mvp-2）第3部「モックの状態」。
//
// API の実装待ちで画面開発を止めないためのフィクスチャ。
// 本番 API に人物切り替えや架空の同意を登録する機能は足さない。

import { ApiError } from "./api.js";

export const MOCK_STATES = {
  unauthenticated: "1. 未ログイン",
  registering: "2. 初期登録（参加条件の回答待ち）",
  initial_approval: "3. 初回案の承認待ち",
  confirmed: "4. 通常時（確定済み）",
  withdrawn: "5. 辞退直後",
  replan: "6. 再提案（同意集め）",
  after_confirm: "7. 確定後（通知の送信中）",
  retrying: "8. 再試行待ち",
  needs_owner: "9. 停止・障害",
  conflict: "10. 競合（古い版への回答）",
  toc_lookup: "11. 目次の取得",
};

const SESSION_ID = "ses_demo";
const GROUP_ID = "grp_demo";

const MEMBERS = [
  { id: "mem_a", display_name: "A", role: "owner", joined: true, left: false },
  { id: "mem_b", display_name: "B", role: "member", joined: true, left: false },
  { id: "mem_c", display_name: "C", role: "member", joined: true, left: false },
  { id: "mem_d", display_name: "D", role: "member", joined: true, left: false },
];

const READING_DATA = {
  book_title: "サンプル技術書",
  isbn: null,
  toc_source: { kind: "manual", urls: [] },
  sections: [
    { id: "sec_1", title: "前回の範囲" },
    { id: "sec_2", title: "今回の前半" },
    { id: "sec_3", title: "今回の後半" },
  ],
  completed_section_ids: ["sec_1"],
  target_section_ids: ["sec_2", "sec_3"],
};

const NOW = "2026-09-19T09:00:00Z";
const DUE = "2026-09-20T09:00:00Z";

const prep = (present, prepared, explainable, minutes, attendance = "attending") => ({
  attendance,
  data: {
    willing_to_present: present,
    prepared_section_ids: prepared,
    explainable_section_ids: explainable,
    max_presentation_minutes: minutes,
  },
});

const ALL_PREPARED = [
  { member_id: "mem_a", value: prep(false, ["sec_1", "sec_2"], [], 0) },
  { member_id: "mem_b", value: prep(true, ["sec_1", "sec_2", "sec_3"], ["sec_2", "sec_3"], 40) },
  { member_id: "mem_c", value: prep(true, ["sec_1", "sec_2"], ["sec_2"], 15) },
  { member_id: "mem_d", value: prep(false, ["sec_1"], [], 0) },
];

const NONE_PREPARED = MEMBERS.map((m) => ({ member_id: m.id, value: null }));

/** B が全範囲を発表する初回案 */
const PLAN_INITIAL = {
  covered_section_ids: ["sec_2", "sec_3"],
  deferred_section_ids: [],
  agenda: [
    { id: "item_1", activity: "presentation", section_ids: ["sec_2", "sec_3"], presenter_member_id: "mem_b", minutes: 40 },
    { id: "item_2", activity: "discussion", section_ids: ["sec_2", "sec_3"], presenter_member_id: null, minutes: 20 },
  ],
};

/** B の辞退を受けて、C が前半だけを担当する再計画案 */
const PLAN_REPLAN = {
  covered_section_ids: ["sec_2"],
  deferred_section_ids: ["sec_3"],
  agenda: [
    { id: "item_1", activity: "presentation", section_ids: ["sec_2"], presenter_member_id: "mem_c", minutes: 15 },
    { id: "item_2", activity: "review", section_ids: ["sec_1"], presenter_member_id: null, minutes: 20 },
    { id: "item_3", activity: "discussion", section_ids: ["sec_2"], presenter_member_id: null, minutes: 25 },
  ],
};

const proposal = (over) => ({
  id: "prop_1",
  version: 1,
  status: "pending",
  change_kind: "initial",
  author: "agent",
  summary: "",
  data: PLAN_INITIAL,
  approvals: [],
  assignments: [],
  ...over,
});

const task = (over) => ({
  id: "task_1",
  session_id: SESSION_ID,
  kind: "preparation",
  status: "open",
  title: "",
  due_at: DUE,
  proposal_id: null,
  proposal_version: null,
  allowed_decisions: [],
  ...over,
});

const summary = (status, over = {}) => ({
  id: "case_demo",
  status,
  reason_code: null,
  summary: "",
  next_retry_at: null,
  ...over,
});

const NOTIFY_ZERO = { pending_count: 0, sent_count: 4, failed_count: 0, unknown_count: 0 };

/** 版が1つ上がった再計画案。担当は C、範囲を縮めたので過半数の承認も要る。 */
const REPLAN_PROPOSAL = proposal({
  id: "prop_2",
  version: 2,
  change_kind: "replan",
  summary: "Bさんの辞退により、今回の範囲を「今回の前半」だけに縮め、Cさんが15分で説明します。残りは次回へ持ち越します。",
  data: PLAN_REPLAN,
  approvals: [{
    kind: "majority",
    eligible_member_ids: ["mem_a", "mem_b", "mem_c"],
    required_count: 2,
    approved_member_ids: ["mem_a"],
    rejected_member_ids: [],
  }],
  assignments: [{ member_id: "mem_c", status: "pending" }],
});

function build(state, viewer) {
  const isOwner = viewer === "mem_a";

  const base = {
    session: {
      id: SESSION_ID,
      group_id: GROUP_ID,
      playbook_id: "reading",
      title: "第2回",
      starts_at: "2026-09-26T11:00:00Z",
      duration_minutes: 60,
      revision: 1,
      status: "draft",
      updated_at: NOW,
    },
    data: READING_DATA,
    members: MEMBERS,
    preparations: NONE_PREPARED,
    confirmed_plan: null,
    current_proposal: null,
    active_case: summary("collecting", { summary: "参加条件を確認しています。" }),
    my_tasks: [],
    permissions: {
      can_update_preparation: true,
      can_withdraw_assignment: false,
      can_withdraw_attendance: true,
      can_submit_proposal: false,
      can_view_activity: isOwner,
    },
    current_member_id: viewer,
    notification_summary: { pending_count: 4, sent_count: 0, failed_count: 0, unknown_count: 0 },
    server_now: NOW,
  };

  switch (state) {
    case "registering":
      base.my_tasks = [task({
        id: `task_prep_${viewer}`,
        title: "今回の準備状況を教えてください。",
        allowed_decisions: ["submit"],
      })];
      return base;

    case "initial_approval": {
      const p = proposal({
        summary: "Bさんが「今回の前半」「今回の後半」を40分で説明し、残り20分を議論にあてます。",
        approvals: [{
          kind: "owner",
          eligible_member_ids: ["mem_a"],
          required_count: 1,
          approved_member_ids: [],
          rejected_member_ids: [],
        }],
        assignments: [{ member_id: "mem_b", status: "pending" }],
      });
      base.session = { ...base.session, revision: 5 };
      base.preparations = ALL_PREPARED;
      base.current_proposal = p;
      base.active_case = summary("awaiting_consent", { summary: "担当の引き受けと管理者の承認を待っています。" });
      if (viewer === "mem_b") {
        base.my_tasks = [task({
          id: "task_assign_b", kind: "assignment", title: "この案の発表を引き受けますか。",
          proposal_id: p.id, proposal_version: p.version, allowed_decisions: ["accept", "decline"],
        })];
      }
      if (isOwner) {
        base.my_tasks = [task({
          id: "task_owner_a", kind: "owner_approval", title: "初回の計画を承認しますか。",
          proposal_id: p.id, proposal_version: p.version, allowed_decisions: ["approve", "reject"],
        })];
      }
      return base;
    }

    case "confirmed":
    case "after_confirm": {
      const p = proposal({
        status: "confirmed",
        summary: "Bさんが40分で説明し、残り20分を議論にあてます。",
        assignments: [{ member_id: "mem_b", status: "accepted" }],
        approvals: [{
          kind: "owner", eligible_member_ids: ["mem_a"], required_count: 1,
          approved_member_ids: ["mem_a"], rejected_member_ids: [],
        }],
      });
      base.session = { ...base.session, revision: 6, status: "confirmed" };
      base.preparations = ALL_PREPARED;
      base.confirmed_plan = p;
      base.current_proposal = p;
      base.active_case = summary("confirmed", { summary: "計画が確定しました。" });
      base.permissions.can_withdraw_assignment = viewer === "mem_b";
      base.notification_summary = state === "after_confirm"
        ? { pending_count: 2, sent_count: 2, failed_count: 0, unknown_count: 0 }
        : NOTIFY_ZERO;
      return base;
    }

    case "withdrawn": {
      const p = proposal({
        status: "superseded",
        summary: "Bさんが40分で説明し、残り20分を議論にあてます。",
        assignments: [{ member_id: "mem_b", status: "accepted" }],
      });
      base.session = { ...base.session, revision: 7, status: "needs_attention" };
      base.preparations = ALL_PREPARED.map((x) =>
        x.member_id === "mem_b" ? { member_id: "mem_b", value: prep(false, ["sec_1", "sec_2", "sec_3"], [], 0) } : x);
      base.confirmed_plan = p;
      base.current_proposal = null;
      base.active_case = summary("planning", { summary: "Bさんの辞退を受けて、代わりの案を作っています。" });
      base.notification_summary = NOTIFY_ZERO;
      return base;
    }

    case "replan": {
      base.session = { ...base.session, revision: 8, status: "needs_attention" };
      base.preparations = ALL_PREPARED.map((x) =>
        x.member_id === "mem_b" ? { member_id: "mem_b", value: prep(false, ["sec_1", "sec_2", "sec_3"], [], 0) } : x);
      base.confirmed_plan = proposal({
        status: "superseded",
        summary: "Bさんが40分で説明し、残り20分を議論にあてます。",
        assignments: [{ member_id: "mem_b", status: "accepted" }],
      });
      base.current_proposal = REPLAN_PROPOSAL;
      base.active_case = summary("awaiting_consent", { summary: "変更案への同意を集めています。" });
      base.notification_summary = NOTIFY_ZERO;

      const tasks = [];
      if (viewer === "mem_c") {
        tasks.push(task({
          id: "task_assign_c", kind: "assignment", title: "「今回の前半」を15分で説明する担当を引き受けますか。",
          proposal_id: "prop_2", proposal_version: 2, allowed_decisions: ["accept", "decline"],
        }));
      }
      // 1人に複数タスクが出る場合を再現する（Cは引き受けと投票の両方）
      if (["mem_a", "mem_b", "mem_c"].includes(viewer)) {
        tasks.push(task({
          id: `task_approve_${viewer}`, kind: "approval", title: "読む範囲を「今回の前半」に縮める案に賛成しますか。",
          proposal_id: "prop_2", proposal_version: 2, allowed_decisions: ["approve", "reject"],
        }));
      }
      base.my_tasks = tasks;
      return base;
    }

    case "retrying":
      base.session = { ...base.session, revision: 8, status: "needs_attention" };
      base.preparations = ALL_PREPARED;
      base.active_case = summary("planning", {
        summary: "AIの応答に失敗したため、18:42 に再試行します。",
        next_retry_at: "2026-09-19T09:42:00Z",
      });
      base.notification_summary = NOTIFY_ZERO;
      return base;

    case "needs_owner":
      base.session = { ...base.session, revision: 9, status: "needs_attention" };
      base.preparations = ALL_PREPARED;
      base.active_case = summary("needs_owner", {
        reason_code: "no_feasible_plan",
        summary: "条件を満たす案が作れませんでした。管理者の判断をお願いします。",
      });
      base.permissions.can_submit_proposal = isOwner;
      base.notification_summary = { pending_count: 0, sent_count: 1, failed_count: 1, unknown_count: 2 };
      return base;

    case "conflict":
      // 表示自体は再提案と同じ。回答を送ると 409 を返す（respondToTask を参照）
      return build("replan", viewer);

    default:
      return base;
  }
}

const ACTIVITY = {
  items: [
    { id: "ev_8", occurred_at: "2026-09-19T09:13:00Z", case_id: "case_demo", kind: "agent_stopped", summary: "Dさんの返事を待っています。待っている間、LLMは呼び出しません", proposal_id: null },
    { id: "ev_7", occurred_at: "2026-09-19T09:12:30Z", case_id: "case_demo", kind: "notification_updated", summary: "A・C・D に同意の依頼を送信しました", proposal_id: "prop_2" },
    { id: "ev_6", occurred_at: "2026-09-19T09:12:00Z", case_id: "case_demo", kind: "proposal_created", summary: "変更案 v2 を作成しました（範囲を「今回の前半」に縮小、「今回の後半」は次回へ）", proposal_id: "prop_2" },
    { id: "ev_5", occurred_at: "2026-09-19T09:11:00Z", case_id: "case_demo", kind: "response_recorded", summary: "Cさんの参加条件（「今回の前半」・15分）を記録しました", proposal_id: null },
    { id: "ev_4", occurred_at: "2026-09-19T09:05:00Z", case_id: "case_demo", kind: "notification_updated", summary: "Cさんに参加条件の確認を送信しました", proposal_id: null },
    { id: "ev_3", occurred_at: "2026-09-19T09:04:00Z", case_id: "case_demo", kind: "input_received", summary: "Bさんの担当辞退を受け取りました", proposal_id: null },
    { id: "ev_2", occurred_at: "2026-09-19T09:00:30Z", case_id: "case_demo", kind: "plan_confirmed", summary: "初回の計画を確定しました", proposal_id: "prop_1" },
    { id: "ev_1", occurred_at: "2026-09-19T09:00:00Z", case_id: "case_demo", kind: "proposal_created", summary: "初回案 v1 を作成しました", proposal_id: "prop_1" },
  ],
  summary: {
    case_id: "case_demo",
    llm_call_count: 4,
    tool_call_count: 6,
    costs: [{ currency: "JPY", estimated_amount: "12.40", billed_amount: null }],
    notifications: [
      { id: "ntf_1", status: "sent", updated_at: "2026-09-19T09:12:30Z", error_code: null },
      { id: "ntf_2", status: "sent", updated_at: "2026-09-19T09:12:31Z", error_code: null },
      { id: "ntf_3", status: "pending", updated_at: "2026-09-19T09:12:31Z", error_code: null },
    ],
  },
};

const ACTIVITY_FAILED = {
  items: ACTIVITY.items,
  summary: {
    ...ACTIVITY.summary,
    costs: [{ currency: "unknown", estimated_amount: null, billed_amount: null }],
    notifications: [
      { id: "ntf_1", status: "sent", updated_at: "2026-09-19T09:12:30Z", error_code: null },
      { id: "ntf_2", status: "failed", updated_at: "2026-09-19T09:12:31Z", error_code: "delivery_failed" },
      { id: "ntf_3", status: "unknown", updated_at: "2026-09-19T09:12:32Z", error_code: "delivery_unknown" },
      { id: "ntf_4", status: "unknown", updated_at: "2026-09-19T09:12:33Z", error_code: "delivery_unknown" },
    ],
  },
};

const TOC_LOOKUPS = {
  succeeded_web: {
    id: "toc_1", status: "succeeded",
    book: { isbn: "9784297127831", title: "サンプル技術書", authors: ["著者 太郎"], publisher: "サンプル社", pages: 320 },
    source: "web", source_urls: ["https://example.com/books/sample"],
    entries: [
      { title: "第1章 はじめに", level: 1 },
      { title: "1-1 この本の目的", level: 2 },
      { title: "1-2 読み進め方", level: 2 },
      { title: "第2章 基本の仕組み", level: 1 },
      { title: "2-1 全体像", level: 2 },
      { title: "2-2 内部の動き", level: 2 },
    ],
    unreadable_count: 0, reason_code: null, expires_at: "2026-09-20T09:00:00Z",
  },
  needs_image: {
    id: "toc_2", status: "needs_image",
    book: { isbn: "9784297127831", title: "サンプル技術書", authors: ["著者 太郎"], publisher: "サンプル社", pages: 320 },
    source: null, source_urls: [], entries: [], unreadable_count: 0,
    reason_code: "source_mismatch", expires_at: "2026-09-20T09:00:00Z",
  },
  succeeded_image: {
    id: "toc_3", status: "succeeded",
    book: { isbn: "9784297127831", title: "サンプル技術書", authors: ["著者 太郎"], publisher: "サンプル社", pages: 320 },
    source: "image", source_urls: [],
    entries: [
      { title: "第1章 はじめに", level: 1 },
      { title: "1-1 この本の目的", level: 2 },
      { title: "第2章 基本の仕組み", level: 1 },
    ],
    unreadable_count: 2, reason_code: null, expires_at: "2026-09-20T09:00:00Z",
  },
  failed: {
    id: "toc_4", status: "failed", book: null, source: null, source_urls: [], entries: [],
    unreadable_count: 0, reason_code: "image_unreadable", expires_at: "2026-09-20T09:00:00Z",
  },
};

const fail = (status, code, details = {}) =>
  Promise.reject(new ApiError(status, { error: { code, message: mockMessage(code), details, request_id: "req_mock" } }));

function mockMessage(code) {
  return {
    unauthenticated: "ログインしてください。",
    proposal_superseded: "新しい案が出ています。最新の内容を確認してください。",
    revision_conflict: "状態が更新されています。最新の内容を確認してください。",
    invalid_state: "管理者は脱退できません。先に管理者を交代してください。",
  }[code] || "モックのエラーです。";
}

const delay = (ms = 220) => new Promise((r) => setTimeout(r, ms));

/**
 * api.js と同じ呼び出し方のモック。
 * @param {{state: string, viewer: string}} opts
 */
export function createMockApi(opts) {
  const get = () => build(opts.state, opts.viewer);

  return {
    isMock: true,

    health: async () => ({ status: "ok", now: NOW, dev_mode: true }),
    playbooks: async () => ({ playbooks: [{ id: "reading", name: "輪読" }] }),
    loginUrl: (returnTo) => `#mock-login${returnTo ? `?return_to=${returnTo}` : ""}`,

    async me() {
      await delay(120);
      if (opts.state === "unauthenticated") return fail(401, "unauthenticated");
      const m = MEMBERS.find((x) => x.id === opts.viewer) || MEMBERS[0];
      return {
        user: { id: `usr_${m.id.slice(4)}`, display_name: m.display_name, discord_user_id: "111111111111111111" },
        csrf_token: "mock-csrf-token",
      };
    },

    async logout() { opts.state = "unauthenticated"; },

    async groups() {
      await delay();
      return { items: [{ id: GROUP_ID, name: "技術書輪読", current_member_id: opts.viewer, role: opts.viewer === "mem_a" ? "owner" : "member", member_count: 4 }] };
    },

    async group() { await delay(); return { id: GROUP_ID, name: "技術書輪読", current_member_id: opts.viewer, members: MEMBERS }; },

    async sessions() {
      await delay();
      const d = get();
      return { items: [d.session] };
    },

    async createSession() { await delay(400); return { session: get().session, case_id: "case_demo" }; },
    async createGroup() { await delay(400); return { id: GROUP_ID, name: "技術書輪読", current_member_id: "mem_a", members: MEMBERS }; },

    /** 管理者は脱退できない。それ以外は開始前の開催回から外れる。 */
    async leaveGroup() {
      await delay(400);
      if (opts.viewer === "mem_a") return fail(409, "invalid_state");
      return { group_id: GROUP_ID, affected_session_ids: [get().session.id] };
    },

    async session() { await delay(); return get(); },

    async activity() {
      await delay();
      if (!get().permissions.can_view_activity) return fail(403, "forbidden");
      return opts.state === "needs_owner" ? ACTIVITY_FAILED : ACTIVITY;
    },

    async updatePreparation(_id, expectedRevision) {
      await delay(400);
      const d = get();
      if (expectedRevision !== d.session.revision) {
        return fail(409, "revision_conflict", { current_revision: d.session.revision });
      }
      return { session_id: SESSION_ID, revision: d.session.revision + 1, case_id: "case_demo", processing_status: "queued" };
    },

    async withdraw(_id, expectedRevision) {
      await delay(400);
      const d = get();
      if (expectedRevision !== d.session.revision) {
        return fail(409, "revision_conflict", { current_revision: d.session.revision });
      }
      opts.state = "withdrawn";
      return { session_id: SESSION_ID, revision: d.session.revision + 1, case_id: "case_demo", processing_status: "queued" };
    },

    async respondToTask(_taskId, payload) {
      await delay(400);
      // 状態10：古い版への回答は 409。入力を保ったまま最新を確認させる
      if (opts.state === "conflict") return fail(409, "proposal_superseded", {});
      const d = get();
      if (payload.proposal_version && d.current_proposal && payload.proposal_version !== d.current_proposal.version) {
        return fail(409, "proposal_superseded", {});
      }
      return { session_id: SESSION_ID, revision: d.session.revision, case_id: "case_demo", processing_status: "queued" };
    },

    async submitProposal() {
      await delay(500);
      const d = get();
      return { session_id: SESSION_ID, revision: d.session.revision + 1, case_id: "case_demo", processing_status: "queued" };
    },

    async startTocLookup() { await delay(300); return TOC_LOOKUPS[opts.tocVariant || "succeeded_web"]; },
    async tocLookup() { await delay(300); return TOC_LOOKUPS[opts.tocVariant || "succeeded_web"]; },
    async submitTocImages() { await delay(600); opts.tocVariant = "succeeded_image"; return TOC_LOOKUPS.succeeded_image; },

    dev: {
      status: async () => ({ now: NOW, offset_seconds: 0, faults: {} }),
      advanceClock: async () => ({ now: NOW }),
      login: async () => ({}),
      faults: async () => ({}),
      seed: async () => ({}),
    },
  };
}
