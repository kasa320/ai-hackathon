// 管理者の代案の入力（ReadingPlanData）。契約 第1部 第9節、第2部 R3。
//
// 案件が「管理者の判断待ち」のときだけ使う。AIが調整中の案を上書きする用途ではない。
// 検証はサーバーが最終判定するが、R3 と同じ規則を先に当てて往復を減らす。

import { el, memberName } from "../../dom.js";
import { renderAgenda } from "./plan.js";

const ACTIVITIES = [
  ["presentation", "発表"],
  ["review", "復習"],
  ["discussion", "議論"],
  ["joint_reading", "共同読解"],
];

/** 契約 R3 の計画の検証。 */
export function validatePlan(plan, sessionData, durationMinutes) {
  const errors = [];
  const covered = new Set(plan.covered_section_ids);
  const deferred = new Set(plan.deferred_section_ids);
  const target = new Set(sessionData.target_section_ids);
  const usable = new Set([...covered, ...sessionData.completed_section_ids]);

  for (const id of covered) if (deferred.has(id)) errors.push({ message: "今回扱う範囲と次回へ回す範囲が重なっています。" });
  const union = new Set([...covered, ...deferred]);
  if (union.size !== target.size || [...target].some((id) => !union.has(id))) {
    errors.push({ message: "今回扱う範囲と次回へ回す範囲を合わせて、この回の対象範囲と一致させてください。" });
  }

  if (plan.agenda.length === 0) errors.push({ message: "進行表を1件以上入れてください。" });

  let total = 0;
  for (const item of plan.agenda) {
    total += item.minutes;
    if (item.section_ids.length === 0) errors.push({ message: "進行表のそれぞれの項目に、節を1つ以上選んでください。" });
    if (!Number.isInteger(item.minutes) || item.minutes < 1) errors.push({ message: "進行表の分数は1以上の整数にしてください。" });
    if (item.activity === "presentation" && !item.presenter_member_id) {
      errors.push({ message: "発表には担当者が必要です。" });
    }
    if (item.section_ids.some((id) => !usable.has(id))) {
      errors.push({ message: "進行表には、今回扱う範囲か前回までに読んだ範囲の節だけを入れてください。" });
    }
  }
  if (total > durationMinutes) {
    errors.push({ message: `進行表の合計が持ち時間（${durationMinutes}分）を超えています。いまは${total}分です。` });
  }

  for (const id of covered) {
    if (!plan.agenda.some((item) => item.section_ids.includes(id))) {
      const title = sessionData.sections.find((s) => s.id === id)?.title ?? id;
      errors.push({ message: `「${title}」が進行表のどこにも入っていません。` });
    }
  }

  // 重複したメッセージは1回だけ出す
  return [...new Map(errors.map((e) => [e.message, e])).values()];
}

export function createPlanForm(sessionData, members, durationMinutes, base) {
  const covered = new Set(base?.covered_section_ids ?? sessionData.target_section_ids);
  let rows = [];

  const preview = el("div", {});
  const errorBox = el("p", { class: "field__hint", style: "color:var(--pink);font-weight:700" });
  const rowsBox = el("div", { class: "stack" });

  const sectionTitle = (id) => sessionData.sections.find((s) => s.id === id)?.title ?? id;
  const usableSections = () => [...covered, ...sessionData.completed_section_ids];

  function addRow(item) {
    const row = {
      activity: item?.activity ?? "presentation",
      section_ids: item?.section_ids ?? [],
      presenter_member_id: item?.presenter_member_id ?? null,
      minutes: item?.minutes ?? 15,
    };
    rows.push(row);
    renderRows();
  }

  function readRow(node, row) {
    row.activity = node.querySelector(".pf-activity").value;
    row.minutes = Number(node.querySelector(".pf-minutes").value) || 0;
    const presenter = node.querySelector(".pf-presenter").value;
    row.presenter_member_id = presenter === "" ? null : presenter;
    row.section_ids = [...node.querySelectorAll(".pf-section:checked")].map((i) => i.value);
  }

  function renderRows() {
    rowsBox.replaceChildren(...rows.map((row, i) => {
      const node = el("div", { class: "card", style: "padding:16px" },
        el("div", { class: "card__head", style: "margin-bottom:12px;padding-bottom:10px" },
          el("span", { class: "card__title" }, `${i + 1} 件目`),
          el("button", {
            type: "button", class: "btn btn--sm", style: "margin-left:auto",
            onclick: () => { rows.splice(i, 1); renderRows(); },
          }, "削除"),
        ),
        el("div", { class: "field" },
          el("span", { class: "field__label" }, "内容"),
          el("select", { class: "pf-activity", onchange: () => sync() },
            ACTIVITIES.map(([v, label]) => el("option", { value: v, selected: row.activity === v }, label))),
        ),
        el("div", { class: "field" },
          el("span", { class: "field__label" }, "扱う節"),
          el("div", { class: "choice" }, usableSections().map((id) => el("label", { class: "choice__item" },
            el("input", { type: "checkbox", class: "pf-section", value: id, checked: row.section_ids.includes(id), onchange: () => sync() }),
            el("span", {}, el("span", { class: "choice__name" }, sectionTitle(id))),
          ))),
        ),
        el("div", { class: "field" },
          el("span", { class: "field__label" }, "担当"),
          el("p", { class: "field__hint" }, "指定した人には本人の引き受けが必要になります。代わりに引き受けることはできません。"),
          el("select", { class: "pf-presenter", onchange: () => sync() },
            el("option", { value: "", selected: !row.presenter_member_id }, "なし"),
            members.map((m) => el("option", { value: m.id, selected: row.presenter_member_id === m.id }, m.display_name)),
          ),
        ),
        el("div", { class: "field", style: "margin-bottom:0" },
          el("span", { class: "field__label" }, "時間（分）"),
          el("input", { type: "number", class: "pf-minutes", min: "1", max: String(durationMinutes), value: String(row.minutes), oninput: () => sync() }),
        ),
      );
      node.addEventListener("change", () => readRow(node, row));
      node.addEventListener("input", () => readRow(node, row));
      return node;
    }));
    sync();
  }

  function currentPlan() {
    return {
      covered_section_ids: sessionData.target_section_ids.filter((id) => covered.has(id)),
      deferred_section_ids: sessionData.target_section_ids.filter((id) => !covered.has(id)),
      agenda: rows.map((r, i) => ({
        id: `item_${i + 1}`,
        activity: r.activity,
        section_ids: r.section_ids,
        presenter_member_id: r.presenter_member_id,
        minutes: r.minutes,
      })),
    };
  }

  function sync() {
    const plan = currentPlan();
    preview.replaceChildren(renderAgenda(plan, sessionData, members, durationMinutes));
    errorBox.textContent = "";
  }

  const coveredBox = el("div", { class: "choice" },
    sessionData.target_section_ids.map((id) => el("label", { class: "choice__item" },
      el("input", {
        type: "checkbox", checked: covered.has(id),
        onchange: (e) => { e.target.checked ? covered.add(id) : covered.delete(id); renderRows(); },
      }),
      el("span", {},
        el("span", { class: "choice__name" }, sectionTitle(id)),
        el("span", { class: "choice__desc" }, "外すと次回へ持ち越します"),
      ),
    )),
  );

  const node = el("form", { autocomplete: "off" },
    el("p", { class: "shared-note" },
      "AIが条件を満たす案を作れなかったため、管理者が代案を出します。"
      + "この案にもAIの案と同じ検証と同意条件が適用されます。日時の変更や中止は自動では行いません。"),

    el("div", { class: "field" },
      el("span", { class: "field__label" }, "今回扱う範囲"),
      el("p", { class: "field__hint" }, "チェックを外した節は自動的に次回へ持ち越しになります。"),
      coveredBox,
    ),

    el("div", { class: "field" },
      el("span", { class: "field__label" }, `進行表（${durationMinutes}分まで）`),
      rowsBox,
      el("p", { style: "margin-top:10px" },
        el("button", { type: "button", class: "btn btn--sm", onclick: () => addRow() }, "項目を追加")),
    ),

    el("div", { class: "field" },
      el("span", { class: "field__label" }, "この案の進行表"),
      preview,
    ),
    errorBox,
  );

  if (base?.agenda?.length) base.agenda.forEach(addRow);
  else addRow({ activity: "presentation", section_ids: [], presenter_member_id: null, minutes: 15 });

  return {
    node,
    showErrors(errors) { errorBox.textContent = errors.map((e) => e.message).join(" "); },
    read: currentPlan,
  };
}
