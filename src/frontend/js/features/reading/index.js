// 輪読 Playbook のフロント側の入口。契約 第2部。
//
// 第1部の画面（session.js など）はこのモジュール越しにだけ輪読固有の中身を触る。
// 汎用側に「節」「発表」「進行表」といった語を持ち込まない。

import { el } from "../../dom.js";
import { renderPlan, renderPlanDiff, renderEmptyAgenda } from "./plan.js";
import { createPreparationForm, renderPreparations, validate } from "./preparation.js";
import { createPlanForm, validatePlan } from "./planForm.js";

export const id = "reading";
export const name = "輪読";

/** 開催回の見出しに出す1行。 */
export function sessionHeadline(sessionData) {
  return sessionData.book_title;
}

/** 開催回データ（教材と範囲）の表示。 */
export function renderSessionData(sessionData) {
  const titles = (ids) => ids.map((id) => sessionData.sections.find((s) => s.id === id)?.title ?? id).join("、") || "—";

  const source = {
    web: "Webから取得", image: "画像から読み取り", manual: "手入力",
  }[sessionData.toc_source?.kind] ?? "手入力";

  return el("div", { class: "facts" },
    el("div", { class: "facts__row" },
      el("span", { class: "facts__key" }, "本"),
      el("span", { class: "facts__val" }, sessionData.book_title),
    ),
    el("div", { class: "facts__row" },
      el("span", { class: "facts__key" }, "今回の予定"),
      el("span", { class: "facts__val" }, titles(sessionData.target_section_ids)),
    ),
    el("div", { class: "facts__row" },
      el("span", { class: "facts__key" }, "前回まで"),
      el("span", { class: "facts__val facts__val--tbd" }, titles(sessionData.completed_section_ids)),
    ),
    el("div", { class: "facts__row" },
      el("span", { class: "facts__key" }, "目次"),
      el("span", { class: "facts__val facts__val--tbd" },
        source,
        sessionData.toc_source?.urls?.length
          ? el("a", { href: sessionData.toc_source.urls[0], target: "_blank", rel: "noreferrer" }, "（取得元）")
          : null,
      ),
    ),
  );
}

export {
  renderPlan, renderPlanDiff, renderEmptyAgenda,
  createPreparationForm, renderPreparations, validate,
  createPlanForm, validatePlan,
};
