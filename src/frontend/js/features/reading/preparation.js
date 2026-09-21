// 輪読の参加条件（ReadingPreparationData）の入力と表示。
//
// 聞くのは「参加できるか」と、日時が決まっていない回の「参加できる時間帯」だけ。
// 輪読は日程を決めてから範囲を読んでくるので、準備状況は聞かない。説明の担当は AI が割り振り、
// 本人が引き受けるかを答える。辞退の理由は集めない。
// 自由文は「下書きを作るため」だけに使い、サーバーが解釈した結果を本人が確認してから送る
// （解釈しただけでは保存されない）。サーバーが最終判定するが、同じ規則を送る前に当てて往復を減らす。

import { el } from "../../dom.js";
import { DATE_MAX } from "../../ui.js";
import { createAvailabilityForm, describeSchedule } from "./availability.js";

/** 参加できる時間帯の形を確かめる。 */
export function validate(preparation) {
  const errors = [];
  const d = preparation.data;
  if (preparation.attendance === "attending" && d.schedule?.status === "provided") {
    if (!d.schedule.weekly_windows.length && !d.schedule.date_windows.length) errors.push("参加できる時間帯を1つ以上入力してください。");
    const clock = value => /^([01]\d|2[0-3]):[0-5]\d$/.test(value);
    for (const w of [...d.schedule.weekly_windows, ...d.schedule.date_windows]) {
      if (!clock(w.start) || !(clock(w.end) || w.end === "24:00") || w.start >= w.end) {
        errors.push("参加できる時間帯の開始・終了を確認してください。終了は開始より後にしてください。");
        break;
      }
      if (Object.hasOwn(w, "date") && !w.date) { errors.push("参加できる日付を入力してください。"); break; }
    }
  }
  return errors;
}

const EMPTY = {
  attendance: "attending",
  data: { declined_presentation: false, unavailable_dates: [] },
};

/**
 * 参加条件のフォーム。read() が契約どおりの Preparation を返す。
 * fill() は自由文の解釈結果を流し込むために使う（本人が直せる状態にする）。
 */
export function createPreparationForm(sessionData, current, durationMinutes, session = {}) {
  const value = current ?? EMPTY;
  // 日時がまだ決まっていない回だけ、参加できる時間帯と出られない日を聞く。
  const askDates = session.schedule_status === "proposed";
  const availability = createAvailabilityForm(value.data.schedule, session);
  // 担当の辞退は「担当を辞退する」から付く。フォームでは変えず、保存済みの値を引き継ぐ
  let declined = value.data.declined_presentation === true;

  const attendance = el(
    "select",
    { name: "attendance" },
    el("option", { value: "attending", selected: value.attendance === "attending" }, askDates ? "参加を希望します（日時はこれから調整）" : "参加します"),
    el("option", { value: "absent", selected: value.attendance === "absent" }, "欠席します"),
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
          max: session.period_end || DATE_MAX,
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
      ...rows,
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
    el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "出られない日（任意）"),
    dates,
  );
  const scheduleFields = el("div", { class: "prep-form" }, availability.node, datesField);
  function sync() {
    // 欠席なら時間帯は聞かない
    scheduleFields.hidden = !askDates || attendance.value !== "attending";
    for (const input of datesField.querySelectorAll("input, button")) input.disabled = scheduleFields.hidden;
  }
  attendance.addEventListener("change", sync);

  const node = el(
    "div",
    { class: "prep-form" },
    el("label", { class: "field" }, el("span", {}, "参加できますか"), attendance),
    scheduleFields,
    el("p", { class: "help" }, "説明の担当はエージェントが割り振り、割り振られた人に引き受けられるかを確認します。回答は参加者に共有されます。欠席・辞退の理由は記録しません。"),
  );

  sync();

  return {
    node,
    read() {
      const attending = attendance.value === "attending";
      return {
        attendance: attendance.value,
        data: {
          declined_presentation: attending && declined,
          unavailable_dates: attending && askDates ? [...new Set(dateValues.filter(Boolean))].sort() : [],
          ...(askDates && attending ? { schedule: availability.read() } : value.data.schedule ? { schedule: value.data.schedule } : {}),
        },
      };
    },
    /** 解釈結果を入力欄へ入れる。保存はしない。本人がこのあと直して送る。 */
    fill(preparation) {
      if (Object.hasOwn(preparation.data, "schedule")) availability.fill(preparation.data.schedule);
      attendance.value = preparation.attendance;
      if (typeof preparation.data.declined_presentation === "boolean") declined = preparation.data.declined_presentation;
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
export function renderDraft(sessionData, interpretation) {
  const d = interpretation.preparation.data;
  const attending = interpretation.preparation.attendance === "attending";
  const list = [
    ["参加", attending ? "参加します" : "欠席します"],
    ...(attending && d.schedule ? [["参加できる時間帯", describeSchedule(d.schedule)]] : []),
    ...((d.unavailable_dates ?? []).length ? [["出られない日", d.unavailable_dates.join("、")]] : []),
    ...(d.declined_presentation ? [["説明の担当", "今回は辞退"]] : []),
  ];

  const LABEL = {
    attendance: "参加できるか",
    schedule: "参加できる時間帯",
    unavailable_dates: "出られない日",
    declined_presentation: "説明の担当",
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
    const parts = [
      d.schedule ? describeSchedule(d.schedule) : "参加",
      (d.unavailable_dates ?? []).length ? `出られない日：${d.unavailable_dates.join("、")}` : null,
      d.declined_presentation ? "今回は説明の担当を辞退" : null,
    ].filter(Boolean);
    return el(
      "div",
      { class: "answer" },
      el("span", { class: "answer__who" }, m.display_name),
      el("span", { class: "answer__body" }, parts.join("　")),
    );
  });
  return el("div", { class: "answers" }, rows);
}

export function emptyPreparation() {
  return structuredClone(EMPTY);
}
