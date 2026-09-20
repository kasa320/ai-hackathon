// 輪読の計画（ReadingPlanData）の表示。契約 第2部 R1。
//
// この画面でいちばん目立たせる部品。進行表を持ち時間に対する実寸の帯で描く。
// 「15分しか説明できない」が、数字ではなく長さで伝わるようにするため。

import { el, memberName } from "../../dom.js";

const ACTIVITY = {
  presentation: "説明",
  review: "復習",
  discussion: "議論",
  joint_reading: "共同読解",
};

function sectionTitles(sessionData, ids) {
  const byId = new Map(sessionData.sections.map((s) => [s.id, s.title]));
  return ids.map((id) => byId.get(id) ?? id);
}

/**
 * 進行表の帯。
 * @param {object} plan ReadingPlanData
 * @param {object} sessionData ReadingSessionData
 * @param {Array} members Member[]
 * @param {number} durationMinutes 開催回の持ち時間
 * @param {{caption?: string, before?: boolean, reveal?: boolean}} opts
 */
export function agendaBar(plan, sessionData, members, durationMinutes, opts = {}) {
  const items = plan.agenda;
  const used = items.reduce((sum, it) => sum + it.minutes, 0);

  let start = 0;
  const segs = [];
  const ticks = [];

  for (const item of items) {
    const titles = sectionTitles(sessionData, item.section_ids).join("・");
    const presenter = item.presenter_member_id ? memberName(members, item.presenter_member_id) : null;

    segs.push(el(
      "li",
      { class: `bar__seg bar__seg--${item.activity}`, style: `--min:${item.minutes}` },
      presenter && el("span", { class: "bar__who" }, `${presenter}さん`),
      el("span", { class: "bar__what" }, `${ACTIVITY[item.activity] ?? item.activity}：${titles}`),
      el("span", { class: "bar__min" }, `${item.minutes}分`),
    ));

    ticks.push(el("li", { class: "bar__tick", style: `--min:${item.minutes}` }, String(start)));
    start += item.minutes;
  }

  // 持ち時間が余っている場合は空きとして見せる。合計が分かる方が判断しやすい
  if (used < durationMinutes) {
    const rest = durationMinutes - used;
    segs.push(el(
      "li",
      { class: "bar__seg", style: `--min:${rest}` },
      el("span", { class: "bar__what" }, "未割り当て"),
      el("span", { class: "bar__min" }, `${rest}分`),
    ));
    ticks.push(el("li", { class: "bar__tick", style: `--min:${rest}` }, String(start)));
  }

  ticks.push(el("li", { class: "bar__tick bar__tick--end" }, `${durationMinutes}分`));

  const deferred = sectionTitles(sessionData, plan.deferred_section_ids);

  const cls = ["bar", opts.before && "bar--before", opts.reveal && "bar--reveal"].filter(Boolean).join(" ");

  return el(
    "figure",
    { class: cls },
    opts.caption && el("figcaption", { class: "bar__caption" }, opts.caption),
    el("ol", { class: "bar__track" }, ...segs),
    el("ol", { class: "bar__ticks", "aria-hidden": "true" }, ...ticks),
    deferred.length > 0 && el(
      "p",
      { class: "bar__deferred" },
      `次回へ持ち越す範囲：${deferred.join("・")}`,
    ),
  );
}

/** 案が前の計画からどこを変えたか。数字を並べて比べられるようにする。 */
export function planDiff(from, to, sessionData, members) {
  if (!from || !to) return null;

  const presenters = (plan) => {
    const ids = plan.agenda.map((i) => i.presenter_member_id).filter(Boolean);
    const uniq = [...new Set(ids)];
    return uniq.length ? uniq.map((id) => `${memberName(members, id)}さん`).join("・") : "なし";
  };
  const minutesOf = (plan) => plan.agenda.reduce((s, i) => s + i.minutes, 0);
  const listOf = (ids) => (ids.length ? sectionTitles(sessionData, ids).join("・") : "なし");

  const rows = [
    ["今回読む範囲", listOf(from.covered_section_ids), listOf(to.covered_section_ids)],
    ["説明の担当", presenters(from), presenters(to)],
    ["次回へ持ち越し", listOf(from.deferred_section_ids), listOf(to.deferred_section_ids)],
    ["進行の合計", `${minutesOf(from)}分`, `${minutesOf(to)}分`],
  ].filter(([, a, b]) => a !== b);

  if (rows.length === 0) return null;

  return el(
    "div",
    { class: "diff" },
    ...rows.map(([key, a, b]) => el(
      "div",
      { class: "diff__row" },
      el("span", { class: "diff__key" }, key),
      el("span", {},
        el("span", { class: "diff__from" }, a),
        " → ",
        el("span", { class: "diff__to" }, b)),
    )),
  );
}

/** 参加条件の一覧。本人が共有を確認して送った構造化情報だけを出す（契約 第6節）。 */
export function preparationRows(detail) {
  const { members, preparations, data } = detail;
  const byId = new Map(preparations.map((p) => [p.member_id, p.value]));

  return el(
    "ul",
    { class: "rows", role: "list" },
    ...members.map((m) => {
      const value = byId.get(m.id) ?? null;
      let body;
      let sub = "";

      if (value === null) {
        body = "まだ回答がありません";
      } else if (value.attendance === "absent") {
        body = "欠席";
      } else if (!value.data.willing_to_present) {
        body = "参加できる。今回は説明を担当しない";
      } else {
        const titles = sectionTitles(data, value.data.explainable_section_ids).join("・");
        body = `参加できる。説明できる範囲：${titles || "なし"}`;
        sub = `最大 ${value.data.max_presentation_minutes}分`;
      }

      return el(
        "li",
        { class: "row" },
        el("span", { class: "row__time" }, `${m.display_name}さん${m.role === "owner" ? "（管理者）" : ""}`),
        el("span", { class: "row__body" }, body),
        el("span", { class: "row__sub" }, sub),
      );
    }),
  );
}
