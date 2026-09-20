// 実行の記録（管理者のみ）。契約 第11節。
//
// 出すのは「通知の状態」と「AIの動き」の2つだけ。
// 費用と呼び出し回数は契約では返るが画面には出さない（2026-09-20 の判断を引き継ぐ）。
// 出し直すときは renderCosts を足すだけでよいよう、契約もモックも形は変えていない。

import { el, formatDateTime } from "../dom.js";

const NOTIFY_TEXT = {
  sent: "送信済み",
  pending: "送信待ち",
  failed: "失敗",
  unknown: "成否未確認",
};

const KIND_TEXT = {
  input_received: "入力",
  proposal_created: "案の作成",
  response_recorded: "回答の記録",
  plan_confirmed: "確定",
  agent_retry_scheduled: "再試行の予約",
  agent_stopped: "停止",
  notification_updated: "通知",
};

/** 通知の状態。送信済みはDiscord APIの成功応答であり、既読ではない。 */
function notifications(list) {
  if (list.length === 0) {
    return el("p", { class: "empty" }, "この案件の通知はまだありません。");
  }

  const count = (status) => list.filter((n) => n.status === status).length;
  const unknown = count("unknown");
  const failed = count("failed");

  return el(
    "div",
    {},
    el(
      "div",
      { class: "tags", style: "margin-bottom:16px" },
      el("span", { class: "tag tag--kon" }, `送信済み ${count("sent")}`),
      el("span", { class: "tag" }, `送信待ち ${count("pending")}`),
      failed > 0 && el("span", { class: "tag tag--shu" }, `失敗 ${failed}`),
      unknown > 0 && el("span", { class: "tag tag--shu" }, `成否未確認 ${unknown}`),
    ),
    unknown > 0 && el(
      "p",
      { class: "note note--warn", style: "margin-bottom:16px" },
      "成否未確認の通知は「未送信」とは限りません。自動で送り直すと二重に届くため、Discord 側で届いているかを確認してください。",
    ),
    el(
      "ul",
      { class: "rows", role: "list" },
      ...list.map((n) => el(
        "li",
        { class: "row" },
        el("time", { class: "row__time" }, formatDateTime(n.updated_at)),
        el("span", { class: "row__body" }, NOTIFY_TEXT[n.status] ?? n.status),
        el("span", { class: "row__sub" }, n.error_code ?? ""),
      )),
    ),
  );
}

/** AIと人の動き。新しい順（契約どおりの並びをそのまま出す）。 */
function history(items) {
  if (items.length === 0) {
    return el("p", { class: "empty" }, "まだ記録がありません。");
  }
  return el(
    "ul",
    { class: "rows", role: "list" },
    ...items.map((item) => el(
      "li",
      { class: "row" },
      el("time", { class: "row__time" }, formatDateTime(item.occurred_at)),
      el("span", { class: "row__body" }, item.summary),
      el("span", { class: "row__sub" }, KIND_TEXT[item.kind] ?? item.kind),
    )),
  );
}

export function renderActivity(activity) {
  return el(
    "div",
    {},
    el(
      "section",
      { class: "sec" },
      el("div", { class: "sec__head" }, el("h2", { class: "sec__title" }, "通知の状態")),
      notifications(activity.summary.notifications),
    ),
    el(
      "section",
      { class: "sec" },
      el("div", { class: "sec__head" }, el("h2", { class: "sec__title" }, "AIと人の動き")),
      history(activity.items),
    ),
  );
}
