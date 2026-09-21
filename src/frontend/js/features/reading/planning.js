// 輪読：ブック全体のセッション計画（担当の仮割当・日程調整・直前の担当確認）の表示語彙。
//
// バックエンドの状態値はまだ確定していないため、ここに対応表を一か所だけ置く。
// 表にない値は「確認中」として生の値を添えて出す。確定・引き受け済みのように見せない。

/** 担当の状態を画面で区別する4+1種類。 */
export const ASSIGNMENT_KIND = Object.freeze({
  unassigned: "unassigned",
  unanswered: "unanswered",
  changeRequested: "change_requested",
  replacementPending: "replacement_pending",
  confirmationPending: "confirmation_pending",
  confirmed: "confirmed",
  unknown: "unknown",
});

const ASSIGNMENT_ALIASES = {
  [ASSIGNMENT_KIND.unassigned]: ["unassigned", "none", "not_assigned"],
  [ASSIGNMENT_KIND.unanswered]: ["proposed", "pending", "tentative", "awaiting_response", "awaiting_answer", "unanswered", "requested"],
  [ASSIGNMENT_KIND.changeRequested]: ["change_requested", "change_request", "requested_change"],
  [ASSIGNMENT_KIND.replacementPending]: ["replacement_pending", "replacement_proposed", "awaiting_replacement", "awaiting_replacement_approval", "replacement_candidate"],
  [ASSIGNMENT_KIND.confirmationPending]: ["confirmation_pending", "reconfirmation_pending", "final_confirmation_pending", "awaiting_confirmation", "awaiting_final_confirmation", "reconfirmation_requested"],
  [ASSIGNMENT_KIND.confirmed]: ["confirmed", "accepted", "accept"],
};

const ASSIGNMENT_LABEL = {
  [ASSIGNMENT_KIND.unassigned]: "担当者未定",
  [ASSIGNMENT_KIND.unanswered]: "未回答",
  [ASSIGNMENT_KIND.changeRequested]: "変更希望",
  [ASSIGNMENT_KIND.replacementPending]: "交代候補の承認待ち",
  [ASSIGNMENT_KIND.confirmationPending]: "直前の確認待ち",
  [ASSIGNMENT_KIND.confirmed]: "確定",
};

const ASSIGNMENT_HINT = {
  [ASSIGNMENT_KIND.unassigned]: "この回の担当者はまだ決まっていません。",
  [ASSIGNMENT_KIND.unanswered]: "仮の割当です。担当者本人の回答を待っています。",
  [ASSIGNMENT_KIND.changeRequested]: "担当者から変更の希望が出ています。交代は本人と関係者の承認で決まります。",
  [ASSIGNMENT_KIND.replacementPending]: "交代候補が挙がっています。候補の本人が承認するまで交代は確定しません。",
  [ASSIGNMENT_KIND.confirmationPending]: "開催が近いため、担当者に最終確認を求めています。",
  [ASSIGNMENT_KIND.confirmed]: "担当者が引き受けています。",
};

const SCHEDULING_KIND = Object.freeze({
  notStarted: "not_started",
  collecting: "collecting",
  proposing: "proposing",
  confirmed: "confirmed",
  needsAttention: "needs_attention",
  unknown: "unknown",
});

const SCHEDULING_ALIASES = {
  [SCHEDULING_KIND.notStarted]: ["not_started", "waiting", "planned", "scheduled", "pending"],
  [SCHEDULING_KIND.collecting]: ["collecting", "collecting_availability", "in_progress", "active", "started"],
  [SCHEDULING_KIND.proposing]: ["proposed", "proposing", "awaiting_consent", "awaiting_approval"],
  [SCHEDULING_KIND.confirmed]: ["confirmed", "fixed"],
  [SCHEDULING_KIND.needsAttention]: ["needs_attention", "needs_owner", "blocked", "failed"],
};

const SCHEDULING_LABEL = {
  [SCHEDULING_KIND.notStarted]: "調整はまだ始まっていません",
  [SCHEDULING_KIND.collecting]: "参加できる日を集めています",
  [SCHEDULING_KIND.proposing]: "日程の案を確認中",
  [SCHEDULING_KIND.confirmed]: "日程確定",
  [SCHEDULING_KIND.needsAttention]: "調整に確認が必要です",
};

const SLOT_STATUS_LABEL = { planned: "開始前", active: "進行中", completed: "完了" };

function classify(table, value, fallback) {
  for (const [kind, aliases] of Object.entries(table)) {
    if (aliases.includes(value)) return kind;
  }
  return fallback;
}

/** 担当状態を { kind, label, hint, raw } にする。未知の値は unknown として生の値を残す。 */
export function assignmentState(slot) {
  const raw = slot.assignee_member_id ? slot.assignment_status ?? "" : "";
  const kind = slot.assignee_member_id
    ? classify(ASSIGNMENT_ALIASES, raw, ASSIGNMENT_KIND.unknown)
    : ASSIGNMENT_KIND.unassigned;
  if (kind === ASSIGNMENT_KIND.unknown) {
    return { kind, label: "担当状態を確認中", hint: "画面が対応していない状態です。確定とは限りません。", raw };
  }
  return { kind, label: ASSIGNMENT_LABEL[kind], hint: ASSIGNMENT_HINT[kind], raw };
}

export function schedulingState(slot) {
  const raw = slot.scheduling_status ?? "";
  const kind = classify(SCHEDULING_ALIASES, raw, SCHEDULING_KIND.unknown);
  if (kind === SCHEDULING_KIND.unknown) return { kind, label: "日程調整の状態を確認中", raw };
  return { kind, label: SCHEDULING_LABEL[kind], raw };
}

export function slotStatusLabel(status) {
  return SLOT_STATUS_LABEL[status] ?? status;
}

/** 自分の担当のうち、初期割当への回答がまだの枠。 */
export function slotsAwaitingMyAnswer(slots, memberId) {
  return slots.filter((slot) => slot.assignee_member_id === memberId && assignmentState(slot).kind === ASSIGNMENT_KIND.unanswered);
}

export function slotAwaitsMyConfirmation(slot, memberId) {
  return slot.assignee_member_id === memberId && assignmentState(slot).kind === ASSIGNMENT_KIND.confirmationPending;
}

const PLAN_LABEL = {
  draft: "計画を作成中",
  awaiting_assignments: "担当の回答待ち",
  awaiting_assignment_responses: "担当の回答待ち",
  active: "進行中",
  scheduling: "日程調整中",
  completed: "完了",
};

/** ブック全体の計画状態。値が無い・未知のときは null（見せない）か生の値。 */
export function planLabel(book) {
  const raw = book.planning_status;
  if (!raw) return null;
  return PLAN_LABEL[raw] ?? `計画状態：${raw}`;
}

/** 最初の実セッションが作られたあとは編集できない。理由を返す（編集できるなら null）。 */
export function editLockReason(slots) {
  const started = slots.find((slot) => slot.session || slot.status !== "planned");
  return started ? `第${started.sequence_number}回の実セッションが作られたため、ブックの内容は変更できません。` : null;
}

export function formatPeriod(start, end) {
  if (!start && !end) return "未設定";
  const f = (d) => (d ? d.replaceAll("-", "/") : "—");
  return `${f(start)} 〜 ${f(end)}`;
}
