// 依頼票：開催回画面でいちばん大きく出す面。
//
// この製品は「参加者の入力が届くとAIが再開する」仕組みなので、入力が来ないと何も進まない。
// だから最大の面は、次にすることが決まる1文にする。
// 依頼が複数あっても最初に見えるのは1件だけ。3件並べるとどれも押されない。
//
// 面の作りで、文字を読む前に自分が動くかどうかが分かるようにする（契約 第4節 Task / permissions）。

import { el, formatDateTime, remaining } from "../dom.js";

const DECISION = {
  submit: { text: "回答する", main: true },
  accept: { text: "引き受ける", main: true },
  decline: { text: "引き受けない", main: false },
  approve: { text: "賛成する", main: true },
  reject: { text: "反対する", main: false },
};

const KIND_LABEL = {
  preparation: "あなたへの確認",
  assignment: "あなたへの依頼",
  approval: "あなたへの確認",
  owner_approval: "管理者のあなたへ",
};

/**
 * @param {object} detail SessionDetail
 * @param {object} handlers
 *   onDecision(task, decision) … assignment / approval / owner_approval
 *   onOpenPreparation()        … preparation タスクの入力を開く
 *   onOpenProposal()           … 管理者の代案入力を開く
 */
export function renderCall(detail, handlers) {
  const open = detail.my_tasks.filter((t) => t.status === "open");
  const task = open[0];

  if (task) return callForTask(task, open.length, detail, handlers);
  if (detail.permissions.can_submit_proposal) return callForOwnerDecision(detail, handlers);
  if (detail.session.status === "confirmed") return callConfirmed(detail);
  return callWaiting(detail);
}

function callForTask(task, openCount, detail, handlers) {
  const buttons = task.allowed_decisions.map((decision) => {
    const spec = DECISION[decision];
    if (!spec) return null;
    return el(
      "button",
      {
        type: "button",
        class: `btn${spec.main ? " btn--main" : ""}`,
        onClick: () => {
          if (decision === "submit") return handlers.onOpenPreparation();
          handlers.onDecision(task, decision);
        },
      },
      spec.text,
    );
  }).filter(Boolean);

  return el(
    "section",
    { class: "call call--act" },
    el("p", { class: "call__kind" }, KIND_LABEL[task.kind] ?? "あなたへの依頼"),
    el("h1", { class: "call__title" }, task.title),
    el(
      "p",
      { class: "call__due" },
      `期限 ${formatDateTime(task.due_at)}・残り ${remaining(task.due_at, detail.server_now)}`,
    ),
    el("div", { class: "btns" }, ...buttons),
    openCount > 1 && el("p", { class: "call__more" }, `このあと、ほかに ${openCount - 1} 件の確認があります。`),
  );
}

function callForOwnerDecision(detail, handlers) {
  const reason = {
    no_feasible_plan: "条件を満たす案が作れませんでした。",
    deadline_expired: "回答の期限までに必要な同意が揃いませんでした。",
    model_error: "AIの処理が失敗しました。",
    budget_exceeded: "AIの利用上限に達しました。",
  }[detail.active_case?.reason_code] ?? "AIでは決められませんでした。";

  // 見出しは「次にすること」。理由は本文に置く（両方に理由を書くと同じ文が2度出る）
  return el(
    "section",
    { class: "call call--act" },
    el("p", { class: "call__kind" }, "管理者のあなたへ"),
    el("h1", { class: "call__title" }, "代案を出してください。"),
    el("p", { class: "call__body" }, detail.active_case?.summary || reason),
    el(
      "div",
      { class: "btns" },
      el("button", { type: "button", class: "btn btn--main", onClick: () => handlers.onOpenProposal() }, "代案を入力する"),
    ),
  );
}

function callConfirmed(detail) {
  const s = detail.session;
  const n = detail.notification_summary;
  const notified = n.pending_count === 0 && n.failed_count === 0 && n.unknown_count === 0;

  return el(
    "section",
    { class: "call call--done on-kon" },
    el("p", { class: "call__kind" }, "確定しました"),
    el("h1", { class: "call__title" }, `${formatDateTime(s.starts_at)}・${detail.data.book_title}`),
    el(
      "p",
      { class: "call__due" },
      notified
        ? `全員に通知を送信しました（${n.sent_count}件）。送信済みは既読を意味しません。`
        : `通知：送信済み ${n.sent_count}・送信待ち ${n.pending_count}・失敗 ${n.failed_count}・成否未確認 ${n.unknown_count}`,
    ),
  );
}

function callWaiting(detail) {
  const c = detail.active_case;
  const s = detail.session;

  const line = {
    collecting: "参加条件の回答を集めています。",
    planning: "案を作っています。",
    awaiting_consent: "ほかの人の引き受けと同意を待っています。",
    confirmed: "計画が確定しました。",
    needs_owner: "管理者の判断を待っています。",
  }[c?.status] ?? "状況を確認しています。";

  const retry = c?.next_retry_at
    ? `AIの処理に失敗したため、${formatDateTime(c.next_retry_at)} に再試行します。`
    : null;

  return el(
    "section",
    { class: "call call--wait" },
    el("p", { class: "call__kind" }, "いまは待っていて大丈夫です"),
    el("h1", { class: "call__title" }, `${formatDateTime(s.starts_at)}・${detail.data.book_title}`),
    el("p", { class: "call__body" }, c?.summary || line),
    retry && el("p", { class: "call__due" }, retry),
  );
}
