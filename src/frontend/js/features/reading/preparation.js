// 参加条件の入力（輪読）。契約 第2部 R1・R3、第1部 第7節。
//
// 送るのは構造化した項目だけ。辞退の理由や自由文は集めない（仕様書 第4節「個人情報と入力の扱い」）。
// ここでの検証は入力の手戻りを減らすためのもので、正しさはサーバーが決める。

import { el, mount } from "../../dom.js";

/**
 * @param {object} detail SessionDetail
 * @param {(preparation: object) => Promise<void>} onSubmit
 * @param {() => void} onCancel
 */
export function preparationForm(detail, onSubmit, onCancel) {
  const sections = detail.data.sections;
  const duration = detail.session.duration_minutes;
  const current = detail.preparations.find((p) => p.member_id === detail.current_member_id)?.value ?? null;

  const state = {
    attendance: current?.attendance ?? "attending",
    present: current?.data.willing_to_present ?? false,
    prepared: new Set(current?.data.prepared_section_ids ?? []),
    explainable: new Set(current?.data.explainable_section_ids ?? []),
    minutes: current?.data.max_presentation_minutes || Math.min(15, duration),
    error: "",
    busy: false,
  };

  const root = el("form", { class: "sec", onSubmit: submit });
  draw();
  return root;

  function pick(name, value, text, checked, onChange) {
    return el(
      "label",
      { class: "pick" },
      el("input", { type: "radio", name, value, checked, onChange }),
      text,
    );
  }

  function check(id, title, checked, disabled, onChange) {
    return el(
      "label",
      { class: "check" },
      el("input", { type: "checkbox", checked, disabled, onChange }),
      el("span", {}, title),
    );
  }

  function draw() {
    const attending = state.attendance === "attending";

    mount(
      root,
      el(
        "div",
        { class: "sec__head" },
        el("h2", { class: "sec__title" }, "あなたの参加条件"),
      ),

      el(
        "p",
        { class: "note", style: "margin-bottom:24px" },
        "ここで送った内容は、このグループのメンバーに共有されます。理由は送りません。",
      ),

      el(
        "div",
        { class: "field" },
        el("span", { class: "field__label" }, "今回参加できますか"),
        el(
          "div",
          { class: "picks" },
          pick("att", "attending", "参加できる", attending, () => { state.attendance = "attending"; draw(); }),
          pick("att", "absent", "欠席する", !attending, () => { state.attendance = "absent"; state.present = false; draw(); }),
        ),
      ),

      attending && el(
        "div",
        { class: "field" },
        el("span", { class: "field__label" }, "今回、説明を担当できますか"),
        el(
          "div",
          { class: "picks" },
          pick("pres", "yes", "担当できる", state.present, () => { state.present = true; draw(); }),
          pick("pres", "no", "今回は担当しない", !state.present, () => { state.present = false; draw(); }),
        ),
      ),

      attending && el(
        "div",
        { class: "field" },
        el("span", { class: "field__label" }, "読んでこられた範囲"),
        el(
          "div",
          { class: "checks" },
          ...sections.map((s) => check(s.id, s.title, state.prepared.has(s.id), false, (e) => {
            toggle(state.prepared, s.id, e.target.checked);
            if (!e.target.checked) state.explainable.delete(s.id);
            draw();
          })),
        ),
      ),

      attending && state.present && el(
        "div",
        { class: "field" },
        el("span", { class: "field__label" }, "そのうち、説明できる範囲"),
        el("p", { class: "field__note" }, "読んでこられた範囲からしか選べません。"),
        el(
          "div",
          { class: "checks" },
          ...sections.map((s) => check(
            s.id,
            s.title,
            state.explainable.has(s.id),
            !state.prepared.has(s.id),
            (e) => { toggle(state.explainable, s.id, e.target.checked); draw(); },
          )),
        ),
      ),

      attending && state.present && el(
        "label",
        { class: "field" },
        el("span", { class: "field__label" }, "説明にあてられる時間"),
        el("input", {
          class: "input",
          type: "number",
          min: "1",
          max: String(duration),
          step: "1",
          value: String(state.minutes),
          style: "max-width:9em",
          onInput: (e) => { state.minutes = Number(e.target.value); },
        }),
        el("p", { class: "field__note" }, `今回の持ち時間は ${duration}分です。`),
      ),

      state.error && el("p", { class: "err" }, state.error),

      el(
        "div",
        { class: "btns", style: "margin-top:24px" },
        el("button", { type: "submit", class: "btn btn--main", disabled: state.busy }, state.busy ? "送信しています" : "この内容で送る"),
        el("button", { type: "button", class: "btn", onClick: onCancel }, "やめる"),
      ),
    );
  }

  function toggle(set, id, on) {
    if (on) set.add(id); else set.delete(id);
  }

  function validate() {
    if (state.attendance === "absent" || !state.present) return "";
    if (state.explainable.size === 0) return "説明できる範囲を1つ以上選んでください。";
    if (!Number.isInteger(state.minutes) || state.minutes < 1 || state.minutes > duration) {
      return `説明にあてられる時間は 1〜${duration} の整数で入力してください。`;
    }
    return "";
  }

  async function submit(e) {
    e.preventDefault();
    state.error = validate();
    if (state.error) return draw();

    const attending = state.attendance === "attending";
    const present = attending && state.present;

    const preparation = {
      attendance: state.attendance,
      data: {
        willing_to_present: present,
        prepared_section_ids: attending ? sections.filter((s) => state.prepared.has(s.id)).map((s) => s.id) : [],
        explainable_section_ids: present ? sections.filter((s) => state.explainable.has(s.id)).map((s) => s.id) : [],
        max_presentation_minutes: present ? state.minutes : 0,
      },
    };

    state.busy = true;
    draw();
    try {
      await onSubmit(preparation);
    } catch (err) {
      state.busy = false;
      state.error = err.fieldErrors?.[0]?.message || err.message || "送信できませんでした。";
      draw();
    }
  }
}
