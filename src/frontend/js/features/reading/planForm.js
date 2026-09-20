// 管理者の代案の入力（ReadingPlanData）。
//
// 案件が「管理者の判断待ち」のときだけ使う。AIが調整中の案を上書きする用途ではない。
// 検証はサーバーが最終判定するが、同じ規則を先に当てて往復を減らす。
// 担当に指定した人には本人の引き受けが要る。ここで代わりに引き受けることはできない。

import { el, mount } from "../../dom.js";
import { activityLabel, sectionTitle } from "./plan.js";
import { renderPlanBar } from "../../ui.js";

const ACTIVITIES = ["presentation", "review", "discussion", "joint_reading"];

/** 計画の検証。重複したメッセージは1回だけ返す。 */
export function validatePlan(plan, sessionData, durationMinutes) {
  const errors = [];
  const covered = new Set(plan.covered_section_ids);
  const deferred = new Set(plan.deferred_section_ids);
  const target = new Set(sessionData.target_section_ids);
  const usable = new Set([...covered, ...sessionData.completed_section_ids]);

  for (const id of covered) if (deferred.has(id)) errors.push("今回扱う範囲と次回へ回す範囲が重なっています。");

  const union = new Set([...covered, ...deferred]);
  if (union.size !== target.size || [...target].some((id) => !union.has(id))) {
    errors.push("今回扱う範囲と次回へ回す範囲を合わせて、この回の対象範囲と一致させてください。");
  }

  if (plan.agenda.length === 0) errors.push("進行表を1件以上入れてください。");

  let total = 0;
  for (const item of plan.agenda) {
    total += item.minutes;
    if (item.section_ids.length === 0) errors.push("進行表のそれぞれの項目に、範囲を1つ以上選んでください。");
    if (!Number.isInteger(item.minutes) || item.minutes < 1) errors.push("進行表の分数は1以上の整数にしてください。");
    if (item.activity === "presentation" && !item.presenter_member_id) errors.push("説明には担当者が必要です。");
    if (item.section_ids.some((id) => !usable.has(id))) {
      errors.push("進行表には、今回扱う範囲か前回までに読んだ範囲だけを入れてください。");
    }
  }
  if (total > durationMinutes) {
    errors.push(`進行表の合計が持ち時間（${durationMinutes}分）を超えています。いまは${total}分です。`);
  }

  for (const id of covered) {
    if (!plan.agenda.some((item) => item.section_ids.includes(id))) {
      errors.push(`「${sectionTitle(sessionData, id)}」が進行表のどこにも入っていません。`);
    }
  }
  return [...new Set(errors)];
}

export function createPlanForm(sessionData, members, durationMinutes, base, currentMemberID) {
  const covered = new Set(base?.covered_section_ids ?? sessionData.target_section_ids);
  const rows = [];

  const preview = el("div", {});
  const rowsBox = el("div", { class: "repeater" });

  const usableSections = () => [...covered, ...sessionData.completed_section_ids];

  function currentPlan() {
    return {
      covered_section_ids: sessionData.target_section_ids.filter((id) => covered.has(id)),
      deferred_section_ids: sessionData.target_section_ids.filter((id) => !covered.has(id)),
      agenda: rows.map((r, i) => ({
        id: `item_${i + 1}`,
        activity: r.activity,
        section_ids: r.section_ids.filter((id) => usableSections().includes(id)),
        presenter_member_id: r.presenter_member_id,
        minutes: r.minutes,
      })),
    };
  }

  function sync() {
    const plan = currentPlan();
    const items = plan.agenda.map((item) => {
      const name = item.presenter_member_id
        ? members.find((m) => m.id === item.presenter_member_id)?.display_name ?? "担当者"
        : "全員";
      return {
        label: activityLabel(item.activity),
        who: `${name} ${item.minutes}分`,
        minutes: Math.max(item.minutes, 1),
        tone: !item.presenter_member_id ? null : item.presenter_member_id === currentMemberID ? "solid" : "mid",
      };
    });
    const total = plan.agenda.reduce((n, i) => n + i.minutes, 0);
    mount(
      preview,
      renderPlanBar(items, { enter: false }) ?? el("p", { class: "help" }, "項目を追加すると進行表が出ます。"),
      el("p", { class: "help num" }, `合計 ${total}分 / 持ち時間 ${durationMinutes}分`),
    );
  }

  function renderRows() {
    mount(
      rowsBox,
      rows.map((row, index) =>
        el(
          "div",
          { class: "block", style: "padding:16px" },
          el(
            "div",
            { style: "display:flex;align-items:center;gap:12px" },
            el("h3", {}, `${index + 1} 件目`),
            el(
              "button",
              {
                type: "button",
                class: "btn btn--quiet",
                style: "margin-left:auto;padding:5px 12px",
                onClick: () => {
                  rows.splice(index, 1);
                  renderRows();
                },
              },
              "削除",
            ),
          ),
          el(
            "div",
            { class: "grid grid--2" },
            el(
              "label",
              { class: "field" },
              el("span", {}, "内容"),
              el(
                "select",
                {
                  onChange: (e) => {
                    row.activity = e.target.value;
                    sync();
                  },
                },
                ACTIVITIES.map((v) => el("option", { value: v, selected: row.activity === v }, activityLabel(v))),
              ),
            ),
            el(
              "label",
              { class: "field" },
              el("span", {}, "時間（分）"),
              el("input", {
                type: "number",
                min: "1",
                max: String(durationMinutes),
                value: String(row.minutes),
                onInput: (e) => {
                  row.minutes = Number(e.target.value) || 0;
                  sync();
                },
              }),
            ),
          ),
          el(
            "label",
            { class: "field", style: "margin-top:12px" },
            el("span", {}, "担当（指定した人には本人の引き受けが必要）"),
            el(
              "select",
              {
                onChange: (e) => {
                  row.presenter_member_id = e.target.value || null;
                  sync();
                },
              },
              el("option", { value: "", selected: !row.presenter_member_id }, "担当なし"),
              members.map((m) => el("option", { value: m.id, selected: row.presenter_member_id === m.id }, m.display_name)),
            ),
          ),
          el(
            "fieldset",
            { class: "fieldset", style: "margin-top:12px;font-size:.82rem;color:var(--ink-2)" },
            el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "扱う範囲"),
            el(
              "div",
              { class: "inline" },
              usableSections().map((id) =>
                el(
                  "label",
                  {},
                  el("input", {
                    type: "checkbox",
                    value: id,
                    checked: row.section_ids.includes(id),
                    onChange: (e) => {
                      row.section_ids = e.target.checked
                        ? [...row.section_ids, id]
                        : row.section_ids.filter((x) => x !== id);
                      sync();
                    },
                  }),
                  sectionTitle(sessionData, id),
                ),
              ),
            ),
          ),
        ),
      ),
    );
    sync();
  }

  function addRow(item) {
    rows.push({
      activity: item?.activity ?? "presentation",
      section_ids: item?.section_ids ?? [],
      presenter_member_id: item?.presenter_member_id ?? null,
      minutes: item?.minutes ?? 15,
    });
    renderRows();
  }

  const node = el(
    "div",
    {},
    el(
      "p",
      { style: "font-size:.82rem;color:var(--ink-2)" },
      "AIが条件を満たす案を作れなかったため、管理者が代案を出します。この案にもAIの案と同じ検証と同意条件がかかります。日時の変更や中止は行えません。",
    ),
    el(
      "fieldset",
      { class: "fieldset", style: "margin-top:16px;font-size:.82rem;color:var(--ink-2)" },
      el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "今回扱う範囲（外した分は次回へ持ち越し）"),
      el(
        "div",
        { class: "inline" },
        sessionData.target_section_ids.map((id) =>
          el(
            "label",
            {},
            el("input", {
              type: "checkbox",
              checked: covered.has(id),
              onChange: (e) => {
                e.target.checked ? covered.add(id) : covered.delete(id);
                renderRows();
              },
            }),
            sectionTitle(sessionData, id),
          ),
        ),
      ),
    ),
    el("h3", { style: "margin-top:24px;font-size:.82rem;color:var(--ink-2)" }, `進行表（${durationMinutes}分まで）`),
    rowsBox,
    el(
      "p",
      { style: "margin-top:8px" },
      el("button", { type: "button", class: "btn btn--quiet", onClick: () => addRow() }, "項目を追加"),
    ),
    el("div", { style: "margin-top:16px" }, preview),
  );

  if (base?.agenda?.length) base.agenda.forEach(addRow);
  else addRow();

  return { node, read: currentPlan };
}
