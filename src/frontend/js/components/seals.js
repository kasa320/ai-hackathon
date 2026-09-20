// 押印欄：誰が同意・引き受けを済ませ、あと何人分要るか。
//
// 契約 第4節 ApprovalProgress / Proposal.assignments をそのまま絵にする。
// 画面側で過半数や確定可否を計算しない（第3部「実装分担」）。サーバーが返した数を出す。
//
// 色の決まり：
//   済み          = 紺ベタ
//   あなたの未回答 = 朱の枠（あなたが操作する場所）
//   他人の未回答   = 灰の破線
//   拒否           = 打ち消し

import { el, memberName } from "../dom.js";

const STATE_TEXT = {
  done: "済み",
  wait: "未回答",
  no: "拒否",
};

function seal(members, memberId, state, meId, doneText) {
  const name = memberName(members, memberId);
  const mine = memberId === meId;
  const cls = [
    "seal",
    state === "done" && "seal--done",
    state === "no" && "seal--no",
    state === "wait" && mine && "seal--you",
  ].filter(Boolean).join(" ");

  const text = state === "done" ? doneText : STATE_TEXT[state];
  return el(
    "li",
    { class: cls },
    el("span", { "aria-hidden": "true" }, name.slice(0, 1)),
    el("span", { class: "sr-only" }, `${name}さん：${text}${mine ? "（あなた）" : ""}`),
  );
}

/**
 * 同意（投票・管理者承認）の押印欄。
 * @param {object} approval ApprovalProgress
 */
export function approvalSeals(approval, members, meId) {
  const label = approval.kind === "owner" ? "管理者の承認" : "参加予定者の同意";
  const entries = approval.eligible_member_ids.map((id) => {
    if (approval.approved_member_ids.includes(id)) return [id, "done"];
    if (approval.rejected_member_ids.includes(id)) return [id, "no"];
    return [id, "wait"];
  });

  return el(
    "div",
    {},
    el("p", { class: "seals__label" }, label),
    el(
      "div",
      { class: "seals" },
      el("ul", { class: "seals", role: "list" },
        ...entries.map(([id, state]) => seal(members, id, state, meId, "同意済み"))),
      el(
        "p",
        { class: "seals__count" },
        "同意 ", el("b", { class: "tnum" }, String(approval.approved_member_ids.length)),
        " / 必要 ", el("b", { class: "tnum" }, String(approval.required_count)),
      ),
    ),
  );
}

/**
 * 担当の引き受けの押印欄。集団の同意とは別に記録される（契約 第4節）。
 * @param {Array} assignments Proposal.assignments
 */
export function assignmentSeals(assignments, members, meId) {
  if (assignments.length === 0) return null;

  const accepted = assignments.filter((a) => a.status === "accepted").length;
  const entries = assignments.map((a) => [
    a.member_id,
    a.status === "accepted" ? "done" : a.status === "declined" ? "no" : "wait",
  ]);

  return el(
    "div",
    {},
    el("p", { class: "seals__label" }, "担当者本人の引き受け"),
    el(
      "div",
      { class: "seals" },
      el("ul", { class: "seals", role: "list" },
        ...entries.map(([id, state]) => seal(members, id, state, meId, "引き受け済み"))),
      el(
        "p",
        { class: "seals__count" },
        "引き受け ", el("b", { class: "tnum" }, String(accepted)),
        " / ", el("b", { class: "tnum" }, String(assignments.length)),
      ),
    ),
  );
}
