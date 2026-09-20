// 管理者の代案の入力（輪読）。契約 第1部 第9節、第2部 R1・R3。
//
// 案件が needs_owner のときだけ使う。AIが調整中の案を上書きする用途には使わない。
// 入力しながら実寸の帯が動くので、持ち時間を超えたことがその場で分かる。

import { el, mount } from "../../dom.js";
import { agendaBar } from "./plan.js";

const ACTIVITIES = [
  ["presentation", "説明"],
  ["review", "復習"],
  ["discussion", "議論"],
  ["joint_reading", "共同読解"],
];

let nextId = 1;

export function planForm(detail, onSubmit, onCancel) {
  const data = detail.data;
  const duration = detail.session.duration_minutes;
  const members = detail.members;
  const attending = detail.preparations
    .filter((p) => p.value && p.value.attendance === "attending")
    .map((p) => p.member_id);

  const state = {
    covered: new Set(data.target_section_ids),
    items: [newItem()],
    error: "",
    busy: false,
  };

  const root = el("form", { class: "sec", onSubmit: submit });
  draw();
  return root;

  function newItem() {
    return {
      key: `it_${nextId++}`,
      activity: "presentation",
      sections: new Set(),
      presenter: "",
      minutes: 15,
    };
  }

  /** 進行表に置ける節：今回扱う範囲と、前回までに読んだ範囲（R3） */
  function selectableSections() {
    const ids = [...state.covered, ...data.completed_section_ids];
    return data.sections.filter((s) => ids.includes(s.id));
  }

  function plan() {
    return {
      covered_section_ids: data.target_section_ids.filter((id) => state.covered.has(id)),
      deferred_section_ids: data.target_section_ids.filter((id) => !state.covered.has(id)),
      agenda: state.items.map((it) => ({
        id: it.key,
        activity: it.activity,
        section_ids: [...it.sections],
        presenter_member_id: it.presenter || null,
        minutes: Number(it.minutes) || 0,
      })),
    };
  }

  function itemFields(item, index) {
    return el(
      "fieldset",
      { style: "border:1px solid var(--rule);padding:16px;margin:0 0 16px" },
      el("legend", { style: "font-size:13px;color:var(--ink-2);padding:0 8px" }, `進行 ${index + 1}`),

      el(
        "div",
        { style: "display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:16px" },
        el(
          "label",
          { class: "field", style: "margin:0" },
          el("span", { class: "field__label" }, "種類"),
          el(
            "select",
            { class: "select", onChange: (e) => { item.activity = e.target.value; draw(); } },
            ...ACTIVITIES.map(([v, t]) => el("option", { value: v, selected: item.activity === v }, t)),
          ),
        ),
        el(
          "label",
          { class: "field", style: "margin:0" },
          el("span", { class: "field__label" }, "担当者"),
          el(
            "select",
            { class: "select", onChange: (e) => { item.presenter = e.target.value; draw(); } },
            el("option", { value: "", selected: item.presenter === "" }, "なし"),
            ...members
              .filter((m) => attending.includes(m.id))
              .map((m) => el("option", { value: m.id, selected: item.presenter === m.id }, `${m.display_name}さん`)),
          ),
          el("p", { class: "field__note" }, "指定しても、引き受けは本人の操作が要ります。"),
        ),
        el(
          "label",
          { class: "field", style: "margin:0" },
          el("span", { class: "field__label" }, "分数"),
          el("input", {
            class: "input",
            type: "number", min: "1", max: String(duration), step: "1",
            value: String(item.minutes),
            onInput: (e) => { item.minutes = e.target.value; draw(); },
          }),
        ),
      ),

      el(
        "div",
        { class: "field", style: "margin:16px 0 0" },
        el("span", { class: "field__label" }, "扱う節"),
        el(
          "div",
          { class: "checks" },
          ...selectableSections().map((s) => el(
            "label",
            { class: "check" },
            el("input", {
              type: "checkbox",
              checked: item.sections.has(s.id),
              onChange: (e) => {
                if (e.target.checked) item.sections.add(s.id); else item.sections.delete(s.id);
                draw();
              },
            }),
            el("span", {}, s.title),
          )),
        ),
      ),

      state.items.length > 1 && el(
        "button",
        {
          type: "button",
          class: "btn btn--quiet",
          style: "margin-top:16px",
          onClick: () => { state.items = state.items.filter((x) => x !== item); draw(); },
        },
        `進行 ${index + 1} を削除`,
      ),
    );
  }

  function draw() {
    const p = plan();
    const used = p.agenda.reduce((s, i) => s + i.minutes, 0);

    mount(
      root,
      el("div", { class: "sec__head" }, el("h2", { class: "sec__title" }, "代案を入力する")),

      el(
        "div",
        { class: "field" },
        el("span", { class: "field__label" }, "今回扱う範囲"),
        el("p", { class: "field__note" }, "外した節は次回へ持ち越します。"),
        el(
          "div",
          { class: "checks" },
          ...data.target_section_ids.map((id) => {
            const s = data.sections.find((x) => x.id === id);
            return el(
              "label",
              { class: "check" },
              el("input", {
                type: "checkbox",
                checked: state.covered.has(id),
                onChange: (e) => {
                  if (e.target.checked) state.covered.add(id);
                  else {
                    state.covered.delete(id);
                    for (const it of state.items) it.sections.delete(id);
                  }
                  draw();
                },
              }),
              el("span", {}, s?.title ?? id),
            );
          }),
        ),
      ),

      el("div", { style: "margin:24px 0" },
        agendaBar(p, data, members, duration, { caption: `入力中の進行表（合計 ${used} / ${duration}分）` })),

      ...state.items.map(itemFields),

      el(
        "button",
        { type: "button", class: "btn", onClick: () => { state.items.push(newItem()); draw(); } },
        "進行を追加",
      ),

      state.error && el("p", { class: "err", style: "margin-top:16px" }, state.error),

      el(
        "div",
        { class: "btns", style: "margin-top:24px" },
        el("button", { type: "submit", class: "btn btn--main", disabled: state.busy }, state.busy ? "送信しています" : "この案を提出する"),
        el("button", { type: "button", class: "btn", onClick: onCancel }, "やめる"),
      ),
    );
  }

  /** R3 のうち、入力中に確かめられるものだけ見る。最終判断はサーバー。 */
  function validate(p) {
    if (p.covered_section_ids.length === 0 && p.deferred_section_ids.length === 0) {
      return "今回扱う範囲を決めてください。";
    }
    if (p.agenda.length === 0) return "進行を1件以上入れてください。";

    for (const [i, item] of p.agenda.entries()) {
      if (item.section_ids.length === 0) return `進行 ${i + 1} の扱う節を選んでください。`;
      if (!Number.isInteger(item.minutes) || item.minutes < 1) return `進行 ${i + 1} の分数を1以上で入れてください。`;
      if (item.activity === "presentation" && !item.presenter_member_id) {
        return `進行 ${i + 1} は説明なので、担当者が必要です。`;
      }
    }

    const used = p.agenda.reduce((s, i) => s + i.minutes, 0);
    if (used > duration) return `進行の合計が ${used}分で、持ち時間の ${duration}分を超えています。`;

    const inAgenda = new Set(p.agenda.flatMap((i) => i.section_ids));
    const missing = p.covered_section_ids.filter((id) => !inAgenda.has(id));
    if (missing.length > 0) {
      const titles = missing.map((id) => data.sections.find((s) => s.id === id)?.title ?? id);
      return `今回扱う範囲のうち、進行に入っていない節があります：${titles.join("・")}`;
    }
    return "";
  }

  async function submit(e) {
    e.preventDefault();
    const p = plan();
    state.error = validate(p);
    if (state.error) return draw();

    state.busy = true;
    draw();
    try {
      await onSubmit(p);
    } catch (err) {
      state.busy = false;
      state.error = err.fieldErrors?.[0]?.message || err.message || "提出できませんでした。";
      draw();
    }
  }
}
