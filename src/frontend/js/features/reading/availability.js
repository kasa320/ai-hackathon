// 参加できる時間帯（ScheduleAvailability）の入力欄と表示。
//
// 日時がまだ決まっていない回でだけ使う。曜日ごとの時間帯と、特定の日だけの時間帯を
// 並べて入力する。見た目は「出られない日」の行と揃える。

import { el } from "../../dom.js";
import { DATE_MAX } from "../../ui.js";

const WEEKDAYS = ["日", "月", "火", "水", "木", "金", "土"];
const MAX_WINDOWS = 60;

/** 参加できる時間帯を1行の文にする。未回答・未定は「予定は未定」。 */
export function describeSchedule(s) {
  if (!s || s.status === "unknown") return "予定は未定";
  if (s.status === "unavailable") return "期間内は参加不可";
  return [
    ...s.weekly_windows.map((w) => `毎週${WEEKDAYS[w.weekday]}曜 ${w.start}〜${w.end}`),
    ...s.date_windows.map((w) => `${w.date} ${w.start}〜${w.end}`),
    ...(s.max_duration_minutes ? [`最大${s.max_duration_minutes}分`] : []),
  ].join("、") + "（日本時間）";
}

/**
 * 入力欄を作る。read() は送信用の ScheduleAvailability を返す。
 * fill() は自由文の下書きを入れるときに使う（保存はしない）。
 */
export function createAvailabilityForm(initial, session) {
  const status = el(
    "select",
    { name: "schedule_status" },
    el("option", { value: "unknown" }, "まだ予定が分かりません"),
    el("option", { value: "provided" }, "参加できる時間帯を入力する"),
    el("option", { value: "unavailable" }, "期間内は参加できません"),
  );
  // 最大参加時間は聞かない。本人が会話で言った値があれば、そのまま引き継ぐ
  let maxMinutes = 0;

  let weekly = [];
  let dates = [];
  const weeklyBox = el("div", { class: "toc-list availability__list" });
  const datesBox = el("div", { class: "toc-list availability__list" });

  const timeInput = (w, key, label) =>
    el("input", {
      type: "time",
      class: "toc-list__date",
      // 終端の 24:00 は入力欄では 0:00 と表示する
      value: key === "end" && w.end === "24:00" ? "00:00" : w[key],
      "aria-label": label,
      onInput: (e) => {
        w[key] = key === "end" && e.target.value === "00:00" ? "24:00" : e.target.value;
      },
    });

  const renderRows = (box, values, isWeekly) => {
    const rows = values.map((w, i) =>
      el(
        "div",
        { class: "availability__row" },
        isWeekly
          ? el(
              "select",
              {
                class: "toc-list__date",
                "aria-label": "曜日",
                onChange: (e) => { w.weekday = Number(e.target.value); },
              },
              WEEKDAYS.map((label, d) => el("option", { value: String(d), selected: w.weekday === d }, `${label}曜`)),
            )
          : el("input", {
              type: "date",
              class: "toc-list__date",
              value: w.date,
              min: session.period_start || null,
              max: session.period_end || DATE_MAX,
              "aria-label": "日付",
              onInput: (e) => { w.date = e.target.value; },
            }),
        timeInput(w, "start", "開始時刻"),
        el("span", { "aria-hidden": "true" }, "〜"),
        timeInput(w, "end", "終了時刻"),
        el("button", {
          type: "button",
          class: "toc-list__drop",
          "aria-label": "この時間帯を消す",
          onClick: () => {
            values.splice(i, 1);
            renderRows(box, values, isWeekly);
            box.querySelector("select, input, button")?.focus();
          },
        }, "×"),
      ),
    );
    box.replaceChildren(
      ...rows,
      el(
        "div",
        { style: "margin-top:8px" },
        el("button", {
          class: "btn btn--quiet",
          type: "button",
          disabled: values.length >= MAX_WINDOWS,
          onClick: () => {
            values.push(isWeekly ? { weekday: 3, start: "", end: "" } : { date: "", start: "", end: "" });
            renderRows(box, values, isWeekly);
            [...box.querySelectorAll(".availability__row")].at(-1)?.querySelector("select, input")?.focus();
          },
        }, isWeekly ? "＋ 曜日と時間帯を足す" : "＋ 特定の日を足す"),
      ),
    );
  };

  const datesDetails = el(
    "details",
    { class: "form-details" },
    el("summary", {}, "特定の日だけ時間帯が違う場合"),
    datesBox,
  );

  const fields = el(
    "div",
    {},
    weeklyBox,
    datesDetails,
  );

  function sync() {
    fields.hidden = status.value !== "provided";
    for (const input of fields.querySelectorAll("input, select, button")) input.disabled = fields.hidden;
    if (!fields.hidden) {
      weeklyBox.querySelector(".btn").disabled = weekly.length >= MAX_WINDOWS;
      datesBox.querySelector(".btn").disabled = dates.length >= MAX_WINDOWS;
    }
  }

  status.addEventListener("change", () => {
    // 入力に切り替えたら、空の1行を用意して最初から打てるようにする
    if (status.value === "provided" && weekly.length === 0 && dates.length === 0) {
      weekly.push({ weekday: 3, start: "", end: "" });
      renderRows(weeklyBox, weekly, true);
    }
    sync();
  });

  const node = el(
    "fieldset",
    { class: "fieldset", style: "font-size:.82rem;color:var(--ink-2)" },
    el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "参加できる時間帯（日本時間）"),
    el("label", { class: "field", style: "margin-top:8px" }, el("span", { class: "visually-hidden" }, "予定の状況"), status),
    fields,
    el("p", { class: "help" }, "終了の 0:00 は翌日の 0:00 です。特定の日の時間帯は、その曜日の時間帯より優先します。"),
  );

  function fill(s) {
    status.value = s?.status ?? "unknown";
    maxMinutes = s?.max_duration_minutes ?? 0;
    weekly = structuredClone(s?.weekly_windows ?? []);
    dates = structuredClone(s?.date_windows ?? []);
    renderRows(weeklyBox, weekly, true);
    renderRows(datesBox, dates, false);
    datesDetails.open = dates.length > 0;
    sync();
  }
  fill(initial);

  return {
    node,
    fill,
    read() {
      const provided = status.value === "provided";
      return {
        status: status.value,
        weekly_windows: provided ? structuredClone(weekly) : [],
        date_windows: provided ? structuredClone(dates) : [],
        max_duration_minutes: provided ? maxMinutes : 0,
      };
    },
  };
}
