// 輪読の参加条件（ReadingPreparationData）の入力と表示。
//
// 入力は4項目の構造化フォームに固定する。辞退の理由は集めない。
// 自由文は「下書きを作るため」だけに使い、サーバーが解釈した結果を本人が確認してから送る
// （解釈しただけでは保存されない）。
// サーバーが最終判定するが、同じ規則を送る前に当てて往復を減らす。

import { el } from "../../dom.js";
import { sectionTitle } from "./plan.js";

/** 説明できる節は読んできた節の部分集合。担当するなら1件以上＋1〜持ち時間の整数。 */
export function validate(preparation, durationMinutes) {
  const errors = [];
  const d = preparation.data;

  if (preparation.attendance === "attending" && d.willing_to_present) {
    if (d.explainable_section_ids.length === 0) {
      errors.push("説明できる範囲を1つ以上選んでください。");
    }
    if (!Number.isInteger(d.max_presentation_minutes) || d.max_presentation_minutes < 1 || d.max_presentation_minutes > durationMinutes) {
      errors.push(`説明できる時間は1〜${durationMinutes}分で指定してください。`);
    }
    if (d.explainable_section_ids.some((id) => !d.prepared_section_ids.includes(id))) {
      errors.push("説明できる範囲は、読んできた範囲の中から選んでください。");
    }
  }
  return errors;
}

/** 欠席・担当しないときの形に揃える。出られない日は担当の有無と関係ないので残す。 */
function normalize(preparation) {
  const d = preparation.data;
  if (preparation.attendance !== "attending" || !d.willing_to_present) {
    return {
      attendance: preparation.attendance,
      data: {
        willing_to_present: false,
        prepared_section_ids: d.prepared_section_ids,
        explainable_section_ids: [],
        max_presentation_minutes: 0,
        unavailable_dates: d.unavailable_dates ?? [],
      },
    };
  }
  return preparation;
}

const EMPTY = {
  attendance: "attending",
  data: { willing_to_present: false, prepared_section_ids: [], explainable_section_ids: [], max_presentation_minutes: 0, unavailable_dates: [] },
};

/**
 * 参加条件のフォーム。read() が契約どおりの Preparation を返す。
 * fill() は自由文の解釈結果を流し込むために使う（本人が直せる状態にする）。
 */
export function createPreparationForm(sessionData, current, durationMinutes, session = {}) {
  const value = current ?? EMPTY;
  // 日時がまだ決まっていない回だけ、出られない日を聞く。決まっている回では聞かない。
  const askDates = session.schedule_status === "proposed";

  const attendance = el(
    "select",
    { name: "attendance" },
    el("option", { value: "attending", selected: value.attendance === "attending" }, "参加します"),
    el("option", { value: "absent", selected: value.attendance === "absent" }, "欠席します"),
  );

  const willing = el(
    "select",
    { name: "willing" },
    el("option", { value: "false", selected: !value.data.willing_to_present }, "今回は説明できません"),
    el("option", { value: "true", selected: value.data.willing_to_present }, "説明を担当できます"),
  );

  const checkboxes = (name, checkedIds) =>
    el(
      "div",
      { class: "inline" },
      sessionData.sections.map((s) =>
        el(
          "label",
          {},
          el("input", { type: "checkbox", name, value: s.id, checked: checkedIds.includes(s.id) }),
          s.title,
        ),
      ),
    );

  const prepared = checkboxes("prepared", value.data.prepared_section_ids);
  const explainable = checkboxes("explainable", value.data.explainable_section_ids);
  const minutes = el("input", {
    type: "number",
    name: "minutes",
    min: "1",
    max: String(durationMinutes),
    step: "1",
    value: value.data.max_presentation_minutes ? String(value.data.max_presentation_minutes) : "",
  });

  const presentFields = el(
    "div",
    {},
    el(
      "fieldset",
      { class: "fieldset", style: "font-size:.82rem;color:var(--ink-2)" },
      el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "3　説明できる範囲"),
      explainable,
      el("p", { class: "help" }, "読んできた範囲の中から選びます。"),
    ),
    el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, `4　説明できる時間（1〜${durationMinutes}分）`), minutes),
  );

  const dates = el("div", { class: "toc-list", style: "max-height:220px" });
  const dateValues = [...(value.data.unavailable_dates ?? [])];
  const renderDates = () => {
    const rows = dateValues.map((v, i) =>
      el(
        "div",
        { style: "display:flex;align-items:center;gap:8px" },
        el("input", {
          type: "date",
          class: "toc-list__date",
          value: v,
          min: session.period_start || null,
          max: session.period_end || null,
          "data-date": String(i),
          "aria-label": "出られない日",
          onInput: (e) => { dateValues[i] = e.target.value; },
        }),
        el("button", {
          type: "button",
          class: "toc-list__drop",
          "aria-label": "この日を消す",
          onClick: () => { dateValues.splice(i, 1); renderDates(); },
        }, "×"),
      ),
    );
    dates.replaceChildren(
      ...(rows.length ? rows : [el("p", { class: "help", style: "margin:0" }, "出られない日がなければ、このままで大丈夫です。")]),
      el(
        "div",
        { style: "margin-top:8px" },
        el("button", {
          class: "btn btn--quiet",
          type: "button",
          onClick: () => {
            dateValues.push("");
            renderDates();
            dates.querySelector(`[data-date="${dateValues.length - 1}"]`)?.focus();
          },
        }, "＋ 出られない日を足す"),
      ),
    );
  };
  renderDates();

  const datesField = el(
    "fieldset",
    { class: "fieldset", style: "font-size:.82rem;color:var(--ink-2)" },
    el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "　出られない日"),
    el("p", { class: "help", style: "margin-top:0" }, "この日は無理、という日だけ入れてください。空けられる日を書き出す必要はありません。"),
    dates,
  );
  datesField.hidden = !askDates;

  function sync() {
    const attending = attendance.value === "attending";
    willingField.hidden = !attending;
    presentFields.hidden = !attending || willing.value !== "true";
  }

  const willingField = el("label", { class: "field" }, el("span", {}, "2　説明を担当できますか"), willing);
  attendance.addEventListener("change", sync);
  willing.addEventListener("change", sync);

  const node = el(
    "div",
    {},
    el("p", { style: "font-size:.82rem;color:var(--ink-2)" }, "回答は4項目だけです。都合がつかない理由は入力しません。"),
    el("label", { class: "field" }, el("span", {}, "1　参加できますか"), attendance),
    willingField,
    el(
      "fieldset",
      { class: "fieldset", style: "font-size:.82rem;color:var(--ink-2)" },
      el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "　準備できた範囲"),
      prepared,
    ),
    presentFields,
    datesField,
    el("p", { class: "help" }, "入力した内容はこの会の参加者に共有されます。辞退の理由は記録も共有もしません。"),
  );

  sync();

  const checked = (box) => [...box.querySelectorAll("input:checked")].map((i) => i.value);

  return {
    node,
    read() {
      return normalize({
        attendance: attendance.value,
        data: {
          willing_to_present: willing.value === "true",
          prepared_section_ids: checked(prepared),
          explainable_section_ids: checked(explainable),
          max_presentation_minutes: Number(minutes.value || 0),
          unavailable_dates: [...new Set(dateValues.filter(Boolean))].sort(),
        },
      });
    },
    /** 解釈結果を入力欄へ入れる。保存はしない。本人がこのあと直して送る。 */
    fill(preparation) {
      attendance.value = preparation.attendance;
      willing.value = String(preparation.data.willing_to_present);
      for (const [box, ids] of [
        [prepared, preparation.data.prepared_section_ids],
        [explainable, preparation.data.explainable_section_ids],
      ]) {
        for (const input of box.querySelectorAll("input")) input.checked = ids.includes(input.value);
      }
      minutes.value = preparation.data.max_presentation_minutes ? String(preparation.data.max_presentation_minutes) : "";
      if (Array.isArray(preparation.data.unavailable_dates)) {
        dateValues.length = 0;
        dateValues.push(...preparation.data.unavailable_dates);
        renderDates();
      }
      sync();
    },
  };
}

/**
 * 解釈した下書きの中身を、本人が見て確かめられる形にする。
 * unclear は「読み取れなかった項目」＝まだ確定していない項目で、値は推測していない。
 */
export function renderDraft(sessionData, interpretation, durationMinutes) {
  const d = interpretation.preparation.data;
  const list = [
    ["参加", interpretation.preparation.attendance === "attending" ? "参加します" : "欠席します"],
    ["説明の担当", d.willing_to_present ? "担当できます" : "今回は説明できません"],
    ["読んできた範囲", d.prepared_section_ids.map((id) => sectionTitle(sessionData, id)).join("、") || "—"],
    ["説明できる範囲", d.explainable_section_ids.map((id) => sectionTitle(sessionData, id)).join("、") || "—"],
    ["出られない日", (d.unavailable_dates ?? []).join("、") || "—"],
    ["説明できる時間", d.max_presentation_minutes ? `${d.max_presentation_minutes}分（持ち時間は${durationMinutes}分）` : "—"],
  ];

  const LABEL = {
    attendance: "参加できるか",
    prepared_section_ids: "読んできた範囲",
    explainable_section_ids: "説明できる範囲",
    max_presentation_minutes: "説明できる時間",
    willing_to_present: "説明の担当",
  };

  return el(
    "div",
    { class: "draft" },
    el("h4", {}, "このように読み取りました（まだ保存していません）"),
    el("ul", {}, list.map(([key, val]) => el("li", {}, el("b", {}, key), val))),
    interpretation.unclear.length
      ? el(
          "p",
          { class: "unclear" },
          `確かめてほしい項目：${interpretation.unclear.map((k) => LABEL[k] ?? k).join("、")}。読み取れなかったので推測していません。下の欄で確かめてから送ってください。`,
        )
      : interpretation.needs_followup
        ? el("p", { class: "unclear" }, "読み取りに自信のない項目があります。下の欄で確かめてから送ってください。")
        : null,
  );
}

/** 全員の回答。未回答は「まだ」と出し、欠席と区別する。 */
export function renderAnswers(sessionData, detail) {
  const rows = detail.members.map((m) => {
    const value = detail.preparations.find((p) => p.member_id === m.id)?.value ?? null;
    if (!value) {
      return el(
        "div",
        { class: "answer answer--none" },
        el("span", { class: "answer__who" }, m.display_name),
        el("span", { class: "answer__body" }, "まだ回答していません"),
      );
    }
    if (value.attendance === "absent") {
      return el(
        "div",
        { class: "answer" },
        el("span", { class: "answer__who" }, m.display_name),
        el("span", { class: "answer__body" }, "欠席"),
      );
    }
    const d = value.data;
    const read = d.prepared_section_ids.map((id) => sectionTitle(sessionData, id)).join("、") || "なし";
    return el(
      "div",
      { class: "answer" },
      el("span", { class: "answer__who" }, m.display_name),
      el(
        "span",
        { class: "answer__body" },
        d.willing_to_present
          ? el("span", {}, el("b", {}, `説明できます（最大${d.max_presentation_minutes}分）`), "　")
          : el("span", {}, "説明の担当はしません　"),
        `読んだ範囲：${read}`,
        (d.unavailable_dates ?? []).length
          ? el("span", {}, `　出られない日：${d.unavailable_dates.join("、")}`)
          : null,
      ),
    );
  });
  return el("div", { class: "answers" }, rows);
}

export function emptyPreparation() {
  return structuredClone(EMPTY);
}
