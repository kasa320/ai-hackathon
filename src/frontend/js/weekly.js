// 普段の空き時間（/weekly.html）。ユーザー共通の、毎週くり返す参加できる時間帯を登録する。
//
// セッションごとの参加不可日は、各セッションの参加条件（session.html）で答える別のもの。
// ここでは理由などの自由文は受け付けない。曜日・開始・終了・タイムゾーンだけを扱う。

import { el, mount } from "./dom.js";
import { api, ApiError } from "./api.js";
import { renderTopbar, renderDevBar, placeholder, flash, clearFlash, describeError } from "./ui.js";
import { DEFAULT_TIMEZONE, WEEKDAYS, availabilityStatus, validateWindows, isValidTimezone, sortWindows } from "./weeklyAvailability.js";

const $ = (id) => document.getElementById(id);
const loginUrl = api.loginUrl("/weekly.html");

let saved = null; // サーバーの最新の表現
let timezone = DEFAULT_TIMEZONE;
let windows = []; // 編集中の { weekday, start, end }
let saving = false;
let dirty = false;

boot();

async function boot() {
  await renderDevBar($("devbar"), api, { onChange: () => location.reload() });
  let me;
  try {
    me = await api.me();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    mount($("weekly"), placeholder("普段の空き時間を表示できません", "時間をおいて開き直してください。"));
    return;
  }
  renderTopbar($("topbar"), { me, current: "weekly" });
  await load();
}

async function load() {
  try {
    adopt(await api.weeklyAvailability());
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    // 取得できなかったことを空の状態に見せない。空のフォームで保存すると既存の登録を消しかねない
    mount(
      $("weekly"),
      placeholder("普段の空き時間を取得できませんでした", el("span", {}, describeError(err), " ", el("button", { type: "button", class: "btn btn--quiet", onClick: load }, "再読み込み"))),
    );
    return;
  }
  render();
}

function adopt(data) {
  saved = data;
  timezone = data?.timezone || DEFAULT_TIMEZONE;
  windows = (data?.windows ?? []).map(({ weekday, start, end }) => ({ weekday, start, end }));
  dirty = false;
}

window.addEventListener("beforeunload", (event) => {
  if (dirty) event.preventDefault();
});

function render() {
  const status = availabilityStatus(saved);
  const tzInput = el("input", { type: "text", value: timezone, list: "tz-list", autocomplete: "off", spellcheck: "false", "aria-describedby": "tz-help" });
  tzInput.addEventListener("input", () => { timezone = tzInput.value.trim(); dirty = true; });
  const zones = typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : [DEFAULT_TIMEZONE];

  const days = el("div", { class: "week" });
  const errors = new Map(); // window index -> message
  const paintDays = (focusIndex = null) => {
    mount(days, WEEKDAYS.map(([weekday, label]) => dayRow(weekday, label, errors, paintDays)));
    if (focusIndex !== null) days.querySelector(`[data-index="${focusIndex}"] input`)?.focus();
  };
  paintDays();

  // 再確認のときも、送るのは同じ「いまの内容の保存」。変えなくても保存すると最終更新が新しくなる
  const submit = el("button", { class: "btn", type: "button", id: "save" }, status.kind === "stale" ? "この内容で合っている（確認して保存）" : "この内容で保存する");
  submit.addEventListener("click", () => save(errors, paintDays, [submit]));

  mount(
    $("weekly"),
    el("div", { class: "page-head" },
      el("h1", {}, "普段の空き時間"),
      el("p", {}, "毎週くり返し参加できる時間帯です。日程調整で、候補の日時を選ぶときに使います。"),
    ),
    statusNotice(status),
    el("div", { class: "notice", style: "margin-top:16px" },
      el("span", { class: "notice__mark", "aria-hidden": "true" }, "i"),
      el("span", {},
        el("strong", {}, "特定の回だけ参加できない日は、ここには入れません"),
        el("small", {}, "各セッションの参加条件で答えます。この画面は理由を聞かず、時間帯だけを登録します。")),
    ),
    el("div", { class: "form", style: "margin-top:32px" },
      el("fieldset", { class: "fieldset" },
        el("legend", {}, "タイムゾーン"),
        el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "時刻はこのタイムゾーンで登録します"), tzInput),
        el("datalist", { id: "tz-list" }, zones.map((z) => el("option", { value: z }))),
        el("p", { class: "help", id: "tz-help" }, `初期値は ${DEFAULT_TIMEZONE} です。`),
      ),
      el("fieldset", { class: "fieldset" },
        el("legend", {}, "曜日ごとの空き時間"),
        el("p", { class: "help" }, "同じ曜日に複数の時間帯を登録できます。空いていない曜日は、何も入れません。"),
        days,
      ),
      el("div", { class: "submit" }, submit),
    ),
  );
}

function statusNotice(status) {
  if (status.kind === "ok") {
    return el("p", { class: "help", role: "status" }, "登録済みです。変わったときは、下の内容を直して保存してください。");
  }
  const stale = status.kind === "stale";
  return el("div", { class: "notice", "data-tone": "warn", role: "status" },
    el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
    el("span", {},
      el("strong", {}, stale ? "普段の空き時間の再確認をお願いします" : "普段の空き時間はまだ登録されていません"),
      el("small", {}, stale
        ? `最後の更新から${status.days}日たっています。いまも合っていれば、そのまま確認してください。`
        : "曜日ごとに、参加できる時間帯を入れてください。")));
}

function dayRow(weekday, label, errors, repaint) {
  const rows = windows.map((w, index) => [w, index]).filter(([w]) => w.weekday === weekday);
  const add = el("button", {
    type: "button", class: "btn btn--quiet", "aria-label": `${label}に時間帯を追加`,
    onClick: () => {
      if (saving) return;
      const last = rows.at(-1)?.[0];
      const start = last ? last.end : "19:00";
      windows.push({ weekday, start, end: start < "22:00" ? "22:00" : "23:59" });
      dirty = true;
      repaint(windows.length - 1);
    },
  }, "＋ 時間帯を追加");

  return el("div", { class: "week__day" },
    el("h3", { class: "week__label" }, label),
    el("div", { class: "week__windows" },
      rows.length === 0 ? el("span", { class: "help", style: "margin:0" }, "登録なし") : null,
      rows.map(([w, index]) => {
        const start = el("input", { type: "time", value: w.start, required: true, "aria-label": `${label}の開始時刻`, onInput: (e) => { w.start = e.target.value; dirty = true; } });
        const end = el("input", { type: "time", value: w.end, required: true, "aria-label": `${label}の終了時刻`, onInput: (e) => { w.end = e.target.value; dirty = true; } });
        return el("div", { class: "week__window", "data-index": String(index) },
          start, el("span", { "aria-hidden": "true" }, "〜"), end,
          el("button", {
            type: "button", class: "toc-list__drop", "aria-label": `${label}の${w.start || "この"}〜${w.end || ""}を消す`,
            onClick: () => {
              if (saving) return;
              windows.splice(index, 1);
              dirty = true;
              errors.clear();
              repaint();
            },
          }, "×"),
          errors.has(index) ? el("span", { class: "field__error", role: "alert" }, errors.get(index)) : null,
        );
      }),
      add,
    ),
  );
}

async function save(errors, repaint, buttons) {
  if (saving) return;
  clearFlash($("flash"));

  errors.clear();
  const problems = validateWindows(windows);
  if (!isValidTimezone(timezone)) {
    flash($("flash"), { title: "タイムゾーンを確認してください", detail: "例：Asia/Tokyo", tone: "warn" });
    return;
  }
  if (problems.length) {
    for (const p of problems) errors.set(p.index, p.message);
    repaint(problems[0].index);
    flash($("flash"), { title: "時間帯を確認してください", detail: "赤字の行を直すと保存できます。", tone: "warn" });
    return;
  }

  saving = true;
  const labels = buttons.map((b) => b?.textContent);
  for (const b of buttons) if (b) { b.disabled = true; }
  buttons[0].textContent = "保存しています…";
  let result;
  try {
    result = await api.saveWeeklyAvailability({ timezone, windows: sortWindows(windows) });
  } catch (err) {
    saving = false;
    buttons.forEach((b, i) => { if (b) { b.disabled = false; b.textContent = labels[i]; } });
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    // 入力は残して、もう一度送れるようにする
    flash($("flash"), { title: "空き時間を保存できませんでした", detail: describeError(err), tone: "warn" });
    return;
  }
  // 保存は通っている。サーバーの表現を使い、返らなければ取り直す（取れなくても保存済みとして扱う）
  let refreshed = true;
  if (result && Array.isArray(result.windows)) adopt(result);
  else {
    try {
      adopt(await api.weeklyAvailability());
    } catch (err) {
      refreshed = false;
      dirty = false;
      console.error(err);
    }
  }
  saving = false;
  render();
  flash($("flash"), refreshed
    ? { title: "普段の空き時間を保存しました" }
    : { title: "保存しました", detail: "ただし最新の状態を取得できていません。ページを読み込み直して確認してください。", tone: "warn" });
}
