// 普段の空き時間（ユーザー共通の週間空き時間）の共通部分。
// 画面（weekly.js）とホームの案内が同じ判断を使う。
//
// 表現：{ timezone, windows: [{ weekday: 1〜7（月〜日）, start: "HH:MM", end: "HH:MM" }], updated_at }
// セッションごとの参加不可日は別物で、各セッションの参加条件で答える。

export const DEFAULT_TIMEZONE = "Asia/Tokyo";

/** weekday は 1=月曜〜7=日曜。 */
export const WEEKDAYS = [
  [1, "月曜日"], [2, "火曜日"], [3, "水曜日"], [4, "木曜日"], [5, "金曜日"], [6, "土曜日"], [7, "日曜日"],
];

// 最後の更新からこの日数が過ぎたら、いまも合っているか確かめてもらう。
// バックエンドが再確認の要否を返すようになれば、その値を優先する。
export const RECONFIRM_AFTER_DAYS = 60;

/** "none"（未登録）| "stale"（再確認が必要）| "ok"。days は stale のときの経過日数。 */
export function availabilityStatus(data, now = Date.now()) {
  if (!data || !Array.isArray(data.windows) || data.windows.length === 0) return { kind: "none" };
  const updated = data.updated_at ? new Date(data.updated_at).getTime() : NaN;
  if (Number.isNaN(updated)) return { kind: "ok" };
  const days = Math.floor((now - updated) / 86400000);
  return days >= RECONFIRM_AFTER_DAYS ? { kind: "stale", days } : { kind: "ok", days };
}

const toMinutes = (hhmm) => {
  const [h, m] = hhmm.split(":").map(Number);
  return h * 60 + m;
};

/**
 * 入力の検査。問題があれば { weekday, index, message } の配列を返す（空なら問題なし）。
 * windows は編集中の { weekday, start, end } の配列。
 */
export function validateWindows(windows) {
  const problems = [];
  const time = /^([01]\d|2[0-3]):[0-5]\d$/;
  for (const [index, w] of windows.entries()) {
    if (!time.test(w.start) || !time.test(w.end)) {
      problems.push({ weekday: w.weekday, index, message: "開始と終了の時刻を入れてください。" });
    } else if (toMinutes(w.start) >= toMinutes(w.end)) {
      problems.push({ weekday: w.weekday, index, message: "終了は開始より後にしてください。" });
    }
  }
  if (problems.length) return problems;

  for (const [weekday] of WEEKDAYS) {
    const day = windows
      .map((w, index) => ({ ...w, index }))
      .filter((w) => w.weekday === weekday)
      .sort((a, b) => toMinutes(a.start) - toMinutes(b.start));
    for (let i = 1; i < day.length; i++) {
      if (toMinutes(day[i].start) < toMinutes(day[i - 1].end)) {
        problems.push({ weekday, index: day[i].index, message: "同じ曜日の時間帯が重なっています。" });
      }
    }
  }
  return problems;
}

export function isValidTimezone(tz) {
  if (!tz) return false;
  try {
    new Intl.DateTimeFormat("ja-JP", { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/** 曜日→開始時刻の順に並べる。 */
export function sortWindows(windows) {
  return [...windows].sort((a, b) => a.weekday - b.weekday || toMinutes(a.start) - toMinutes(b.start));
}
