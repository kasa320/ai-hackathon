// 参加できる時間帯（ScheduleAvailability）の入力欄と表示。
//
// 日時がまだ決まっていない回でだけ使う。ここでいう「普段の空き時間」（weekly_windows）は
// ユーザー共通の週間空き時間そのもので、明示的に入力・変更すると保存時に全置換される
// （update_weekly_availability）。「今回だけ参加できる日時」（date_windows）はこの回限りの例外で、
// その日の週間設定を置換するだけで、普段の空き時間には影響しない。両者を見出しで区別する。

import { el } from "../../dom.js";
import { DATE_MAX } from "../../ui.js";

const WEEKDAYS = ["日", "月", "火", "水", "木", "金", "土"];
const MAX_WINDOWS = 60;

/** 参加できる時間帯を1行の文にする。未回答・未定は「予定は未定」。 */
export function describeSchedule(s) {
  if (!s || s.status === "unknown") return "予定は未定（登録済みの普段の空き時間を使います）";
  if (s.status === "unavailable") return "期間内は参加不可";
  return [
    ...s.weekly_windows.map((w) => `毎週${WEEKDAYS[w.weekday]}曜 ${w.start}〜${w.end}`),
    ...s.date_windows.map((w) => `${w.date} ${w.start}〜${w.end}（この日だけの例外）`),
    ...(s.max_duration_minutes ? [`最大${s.max_duration_minutes}分`] : []),
  ].join("、") + "（日本時間）";
}

/** 登録済みの普段の空き時間を1行の文にする。未登録は null。 */
export function describeStanding(standing) {
  if (!standing || !Array.isArray(standing.windows) || standing.windows.length === 0) return null;
  return standing.windows.map((w) => `毎週${WEEKDAYS[w.weekday === 7 ? 0 : w.weekday]}曜 ${w.start}〜${w.end}`).join("、") + `（${standing.timezone}）`;
}

/**
 * 登録済みの普段の空き時間を読みやすく表示し、編集画面へのリンクを置く。
 * standing は api.weeklyAvailability() の結果、failed は取得に失敗したこと。
 * 取得失敗を「未登録」として表示しない。
 */
export function renderStandingAvailability(standing, failed) {
  const editLink = el("a", { href: "/weekly.html", target: "_blank", rel: "noopener" }, "普段の空き時間を編集");
  let body;
  if (failed) {
    body = el("p", { class: "field__error" }, "普段の空き時間を取得できませんでした。時間をおいて開き直してください。");
  } else {
    const text = describeStanding(standing);
    body = el("p", { class: "help", style: "margin:4px 0" }, text ?? "まだ登録されていません。");
  }
  return el(
    "fieldset",
    { class: "fieldset", style: "font-size:.82rem;color:var(--ink-2)" },
    el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "普段の空き時間（登録済み）"),
    body,
    el("p", { style: "margin-top:4px" }, editLink),
  );
}

/**
 * 入力欄を作る。read() は送信用の ScheduleAvailability を返す。
 * fill() は自由文の下書きを入れるときに使う（保存はしない）。
 * weeklyTouched() は、本人がこの画面で週間の曜日・時間帯を明示的に入力・変更したかを返す
 * （サーバー読み込み時の初期値反映では立たない）。
 */
export function createAvailabilityForm(initial, session) {
  const status = el(
    "select",
    { name: "schedule_status" },
    el("option", { value: "unknown" }, "まだ予定が分かりません（登録済みの普段の空き時間を使います）"),
    el("option", { value: "provided" }, "普段の空き時間をまとめて変更する／今回だけ入力する"),
    el("option", { value: "unavailable" }, "期間内は参加できません"),
  );
  // 最大参加時間は聞かない。本人が会話で言った値があれば、そのまま引き継ぐ
  let maxMinutes = 0;
  let weeklyTouched = false;

  let weekly = [];
  let dates = [];
  const weeklyBox = el("div", { class: "toc-list availability__list" });
  const datesBox = el("div", { class: "toc-list availability__list" });
  const weeklyNotice = el(
    "div", { class: "notice", "data-tone": "warn", role: "status", hidden: true },
    el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
    el("span", {}, el("strong", {}, "この内容で保存すると、普段の空き時間も全置換されます。"), el("small", {}, "以後の開催回すべてに使われます。今回だけの例外は下の「今回だけ参加できる日時」で入力してください。")),
  );
  const syncWeeklyNotice = () => { weeklyNotice.hidden = !(weeklyTouched && status.value === "provided" && weekly.length > 0); };

  const timeInput = (w, key, label, onTouch) =>
    el("input", {
      type: "time",
      class: "toc-list__date",
      // 終端の 24:00 は入力欄では 0:00 と表示する
      value: key === "end" && w.end === "24:00" ? "00:00" : w[key],
      "aria-label": label,
      onInput: (e) => {
        w[key] = key === "end" && e.target.value === "00:00" ? "24:00" : e.target.value;
        onTouch?.();
      },
    });

  const renderRows = (box, values, isWeekly) => {
    const onTouch = isWeekly ? () => { weeklyTouched = true; syncWeeklyNotice(); } : null;
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
                onChange: (e) => { w.weekday = Number(e.target.value); onTouch(); },
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
        timeInput(w, "start", "開始時刻", onTouch),
        el("span", { "aria-hidden": "true" }, "〜"),
        timeInput(w, "end", "終了時刻", onTouch),
        el("button", {
          type: "button",
          class: "toc-list__drop",
          "aria-label": "この時間帯を消す",
          onClick: () => {
            values.splice(i, 1);
            onTouch?.();
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
            onTouch?.();
            renderRows(box, values, isWeekly);
            [...box.querySelectorAll(".availability__row")].at(-1)?.querySelector("select, input")?.focus();
          },
        }, isWeekly ? "＋ 曜日と時間帯を足す" : "＋ 日付を足す"),
      ),
    );
  };

  const weeklyField = el(
    "div",
    { style: "margin-top:8px" },
    el("h4", { style: "font-size:.82rem;margin:0 0 4px" }, "普段の空き時間をまとめて変更する"),
    el("p", { class: "help" }, "ここで曜日・時間帯を入力・変更すると、保存時に普段の空き時間（全開催回で使う設定）を全置換します。"),
    weeklyBox,
    weeklyNotice,
  );

  const datesDetails = el(
    "details",
    { class: "form-details" },
    el("summary", {}, "今回だけ参加できる日時（この回限りの例外）"),
    el("p", { class: "help" }, "指定した日の週間設定をその日だけ置き換えます。普段の空き時間は変わりません。"),
    datesBox,
  );

  const fields = el(
    "div",
    {},
    weeklyField,
    datesDetails,
  );

  function sync() {
    fields.hidden = status.value !== "provided";
    for (const input of fields.querySelectorAll("input, select, button")) input.disabled = fields.hidden;
    if (!fields.hidden) {
      weeklyBox.querySelector(".btn").disabled = weekly.length >= MAX_WINDOWS;
      datesBox.querySelector(".btn").disabled = dates.length >= MAX_WINDOWS;
    }
    syncWeeklyNotice();
  }

  status.addEventListener("change", () => {
    // 入力に切り替えたら、空の1行を用意して最初から打てるようにする（まだ触っていないので weeklyTouched は立てない）
    if (status.value === "provided" && weekly.length === 0 && dates.length === 0) {
      weekly.push({ weekday: 3, start: "", end: "" });
      renderRows(weeklyBox, weekly, true);
    }
    sync();
  });

  const node = el(
    "fieldset",
    { class: "fieldset", style: "font-size:.82rem;color:var(--ink-2)" },
    el("legend", { style: "border:0;padding:0;font-size:.82rem;font-weight:400" }, "今回の予定（日本時間）"),
    el("label", { class: "field", style: "margin-top:8px" }, el("span", { class: "visually-hidden" }, "予定の状況"), status),
    fields,
    el("p", { class: "help" }, "終了の 0:00 は翌日の 0:00 です。"),
  );

  // fromDraft: 自由文の解釈結果を流し込むとき true。本人が入力した文章から読み取った内容なので、
  // このあと本人が確認して送信すれば「明示的に入力」として扱う。保存済みの値の単なる再表示では false。
  function fill(s, { fromDraft = false } = {}) {
    status.value = s?.status ?? "unknown";
    maxMinutes = s?.max_duration_minutes ?? 0;
    weekly = structuredClone(s?.weekly_windows ?? []);
    dates = structuredClone(s?.date_windows ?? []);
    weeklyTouched = fromDraft && status.value === "provided" && weekly.length > 0;
    renderRows(weeklyBox, weekly, true);
    renderRows(datesBox, dates, false);
    datesDetails.open = dates.length > 0;
    sync();
  }
  fill(initial);

  return {
    node,
    fill,
    weeklyTouched: () => weeklyTouched && status.value === "provided" && weekly.length > 0,
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
