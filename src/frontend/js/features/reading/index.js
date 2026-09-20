// 輪読固有の画面をまとめて出す入口。
// 共通側（session.js・setup.js）はこの入口を通して用途固有の表示・入力を取る。
// 構成は .agent/decisions/architecture.md「ディレクトリ」に合わせる。

import { agendaBar, planDiff, preparationRows } from "./plan.js";
import { preparationForm } from "./preparation.js";
import { planForm } from "./planForm.js";

export const reading = {
  id: "reading",
  name: "輪読",

  /** 計画（PlanData）の表示 */
  planView: agendaBar,
  /** 前の計画との差分 */
  planDiff,
  /** 参加条件（PreparationData）の一覧表示 */
  preparationView: preparationRows,
  /** 参加条件の入力 */
  preparationForm,
  /** 管理者の代案の入力 */
  planForm,
};
