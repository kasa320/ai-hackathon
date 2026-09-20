// 輪読の計画（ReadingPlanData）の表示。
//
// 進行表は「誰が何分担当するか」を帯の幅で示す。色は担当の有無だけに使い、
// あなたが担当する項目だけを濃く塗る。合計や残り時間はここで計算せず、
// 受け取った minutes をそのまま幅に使う。

import { el } from "../../dom.js";

const ACTIVITY = {
  presentation: "説明",
  review: "復習",
  discussion: "議論",
  joint_reading: "輪読",
};

export function activityLabel(activity) {
  return ACTIVITY[activity] ?? activity;
}

export function sectionTitle(sessionData, id) {
  return sessionData.sections.find((s) => s.id === id)?.title ?? id;
}

export function sectionTitles(sessionData, ids) {
  if (!ids || ids.length === 0) return "—";
  return ids.map((id) => sectionTitle(sessionData, id)).join("、");
}

/** 進行表を ui.renderPlanBar が受け取る形に変える。 */
export function planItems(plan, members, currentMemberID) {
  if (!plan?.agenda) return [];
  return plan.agenda.map((item) => {
    const presenter = item.presenter_member_id;
    const name = presenter ? members.find((m) => m.id === presenter)?.display_name ?? "担当者" : "全員";
    return {
      label: activityLabel(item.activity),
      who: `${name} ${item.minutes}分`,
      minutes: item.minutes,
      tone: !presenter ? null : presenter === currentMemberID ? "solid" : "mid",
    };
  });
}

/** 今回の範囲と持ち越し。見送った範囲は必ず出す（次回に引き継ぐ対象なので）。 */
export function renderScope(plan, sessionData) {
  return el(
    "table",
    { class: "table" },
    el("tr", {}, el("th", {}, "今回の範囲"), el("td", {}, sectionTitles(sessionData, plan.covered_section_ids))),
    el(
      "tr",
      {},
      el("th", {}, "次回へ持ち越し"),
      el("td", {}, plan.deferred_section_ids?.length ? sectionTitles(sessionData, plan.deferred_section_ids) : "なし"),
    ),
  );
}

/** 進行表を表でも読めるようにする（帯だけでは細部が読めないため）。 */
export function renderAgendaTable(plan, sessionData, members) {
  if (!plan?.agenda?.length) return null;
  return el(
    "table",
    { class: "table" },
    plan.agenda.map((item) => {
      const presenter = item.presenter_member_id;
      const name = presenter ? members.find((m) => m.id === presenter)?.display_name ?? "担当者" : null;
      return el(
        "tr",
        {},
        el("th", {}, `${activityLabel(item.activity)}　${item.minutes}分`),
        el(
          "td",
          {},
          sectionTitles(sessionData, item.section_ids),
          name ? el("small", { style: "display:block;color:var(--ink-2)" }, `担当：${name}`) : null,
        ),
      );
    }),
  );
}

/** 教材と範囲。目次の出どころも出す（推測で作られていないことが読めるように）。 */
export function renderMaterial(sessionData) {
  const source = { web: "Webから取得", image: "画像から読み取り", manual: "手入力" }[sessionData.toc_source?.kind] ?? "手入力";
  const url = sessionData.toc_source?.urls?.[0];
  return el(
    "table",
    { class: "table" },
    el("tr", {}, el("th", {}, "本"), el("td", {}, sessionData.book_title)),
    el("tr", {}, el("th", {}, "今回の予定"), el("td", {}, sectionTitles(sessionData, sessionData.target_section_ids))),
    el("tr", {}, el("th", {}, "前回まで"), el("td", {}, sectionTitles(sessionData, sessionData.completed_section_ids))),
    el(
      "tr",
      {},
      el("th", {}, "目次"),
      el("td", {}, source, url ? el("a", { class: "link", href: url, target: "_blank", rel: "noreferrer noopener" }, "（取得元）") : null),
    ),
  );
}
