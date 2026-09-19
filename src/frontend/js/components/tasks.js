// 自分宛てのタスク。契約 第1部 第8節。
//
// 画面では確定可否を計算しない。task.allowed_decisions に載っている操作だけを出す。
// 期限は server_now を基準に出す（デモで時計を進めるため）。

import { el, remaining } from "../dom.js";

const KIND_LABEL = {
  preparation: "参加条件の回答",
  assignment: "担当の引き受け",
  approval: "変更への賛否",
  owner_approval: "管理者の承認",
};

const DECISION_LABEL = {
  submit: "回答する",
  accept: "引き受ける",
  decline: "引き受けない",
  approve: "賛成する",
  reject: "反対する",
};

/** 押されたときに呼ぶ処理を渡す。preparation は入力が要るのでフォームを開く。 */
export function renderTaskCard(taskItem, { serverNow, onDecide, onOpenPreparation, busy }) {
  const expired = taskItem.status === "expired";
  const obsolete = taskItem.status === "obsolete";

  const buttons = taskItem.allowed_decisions.map((decision, i) => {
    if (taskItem.kind === "preparation" && decision === "submit") {
      return el("button", {
        type: "button",
        class: "btn btn--primary",
        disabled: busy,
        onclick: () => onOpenPreparation(taskItem),
      }, DECISION_LABEL[decision]);
    }
    const primary = i === 0;
    return el("button", {
      type: "button",
      class: primary ? "btn btn--primary" : "btn",
      disabled: busy,
      onclick: () => onDecide(taskItem, decision),
    }, DECISION_LABEL[decision]);
  });

  return el("article", { class: "card" },
    el("div", { class: "card__head" },
      el("span", { class: "badge badge--danger" }, KIND_LABEL[taskItem.kind] ?? taskItem.kind),
      el("span", { class: "card__title" }, taskItem.title),
      expired && el("span", { class: "badge badge--muted" }, "期限切れ"),
      obsolete && el("span", { class: "badge badge--muted" }, "無効"),
    ),
    el("p", { class: "card__note" },
      "回答期限 ",
      el("span", { class: "num" }, remaining(taskItem.due_at, serverNow)),
      taskItem.proposal_version ? ` ／ 案 v${taskItem.proposal_version} について` : "",
    ),
    buttons.length ? el("div", { class: "hero__actions", style: "margin-top:16px" }, buttons) : null,
  );
}

/** ヒーローに出す1件。「自分がいまやること」は常に期限の近い順で1件だけ。 */
export function primaryTask(tasks) {
  const open = tasks.filter((t) => t.status === "open" && t.allowed_decisions.length > 0);
  if (open.length === 0) return null;
  return [...open].sort((a, b) => new Date(a.due_at) - new Date(b.due_at))[0];
}
