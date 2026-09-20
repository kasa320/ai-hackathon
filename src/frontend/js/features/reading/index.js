// 輪読 Playbook のフロント側の入口。
//
// 汎用の画面（index.js・session.js）はこのモジュール越しにだけ輪読固有の中身を触る。
// 「節」「説明」「進行表」といった語を汎用側へ持ち込まない。

export const id = "reading";
export const name = "輪読";

export {
  planItems, renderScope, renderAgendaTable, renderMaterial,
  activityLabel, sectionTitle, sectionTitles,
} from "./plan.js";

export {
  createPreparationForm, renderAnswers, renderDraft, validate, emptyPreparation,
} from "./preparation.js";

export { createPlanForm, validatePlan } from "./planForm.js";
export { createTocPicker } from "./toc.js";

/** 会の一覧に出す短い説明。 */
export function summary(sessionData) {
  return sessionData.book_title;
}
