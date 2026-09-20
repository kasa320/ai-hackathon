// 開催回の登録。契約 第1部 第6節、第2部 R1〜R3・R7。
//
// 3段階にしたのは、目次の取得が失敗しうるため。
// 取得できなくても手入力で先へ進めるようにして、登録そのものが止まらないようにする。
// 取得した目次は候補にすぎない。管理者が確認・修正するまで保存しない（R7）。

import { el, mount } from "./dom.js";
import { client, href } from "./client.js";
import { renderNav, renderDevBar, renderHealth } from "./components/chrome.js";

const STEPS = ["本と目次", "今回の範囲", "日時"];
const TERMINAL = new Set(["succeeded", "needs_image", "failed"]);

const REASON_TEXT = {
  book_not_found: "ISBN から本を特定できませんでした。",
  toc_not_found: "公開されているページから目次を見つけられませんでした。",
  source_mismatch: "見つけた目次が取得元のページと一致しませんでした。推測で作らないため採用しません。",
  image_unreadable: "画像から目次を読み取れませんでした。",
  budget_exceeded: "今日の取得回数の上限に達しました。",
  model_error: "取得の処理が失敗しました。",
};

const state = {
  step: 0,
  group: null,
  isbn: "",
  bookTitle: "",
  lookup: null,
  lookupBusy: false,
  entryTitles: new Map(),  // 候補の番号 → 編集後のタイトル（チェックの有無に関わらず保つ）
  chosen: new Set(),       // 節にする候補の番号
  manualText: "",
  sections: [],
  tocSource: { kind: "manual", urls: [] },
  completed: new Set(),
  target: new Set(),
  title: "",
  startsAt: "",
  duration: 60,
  error: "",
  busy: false,
};

const $steps = document.getElementById("steps");
const $panel = document.getElementById("panel");

boot();

async function boot() {
  const health = await renderHealth();
  renderDevBar({ devMode: health.dev_mode });

  let me = null;
  try {
    me = await client.me();
  } catch (err) {
    renderNav(null, "setup");
    return mount($panel, el("p", { class: "empty" },
      el("strong", {}, "ログインが必要です"),
      el("a", { class: "btn btn--main", href: client.loginUrl("/setup.html"), style: "margin-top:16px" }, "Discord でログイン")));
  }
  renderNav(me, "setup");

  const groups = await client.groups();
  const wanted = new URLSearchParams(location.search).get("group");
  state.group = groups.items.find((g) => g.id === wanted) ?? groups.items[0] ?? null;

  if (!state.group) {
    return mount($panel, el("p", { class: "empty" },
      el("strong", {}, "輪読会がありません"), "先に輪読会を作ってください。"));
  }
  if (state.group.role !== "owner") {
    return mount($panel, el("p", { class: "empty" },
      el("strong", {}, "登録できるのは管理者だけです"),
      "管理者が登録すると、参加条件の確認が届きます。"));
  }

  draw();
}

function draw() {
  mount($steps, ...STEPS.map((name, i) => el(
    "li",
    { class: `step${i === state.step ? " step--now" : i < state.step ? " step--done" : ""}` },
    el("span", { class: "step__no" }, String(i + 1)),
    el("span", { class: "step__name" }, name),
  )));

  mount($panel, [stepBook, stepScope, stepWhen][state.step]());
}

function nav({ back = true, next, nextText = "次へ", disabled = false }) {
  return el(
    "div",
    { class: "btns", style: "margin-top:40px" },
    next && el("button", { type: "button", class: "btn btn--main", disabled, onClick: next }, nextText),
    back && state.step > 0 && el("button", {
      type: "button", class: "btn",
      onClick: () => { state.step -= 1; state.error = ""; draw(); },
    }, "戻る"),
  );
}

// ------------------------------------------------------------------
// 1. 本と目次
// ------------------------------------------------------------------

function stepBook() {
  return el(
    "section",
    { class: "sec" },
    el("div", { class: "sec__head" }, el("h1", { class: "sec__title" }, "本と目次")),
    el("p", { class: "prose", style: "margin-bottom:24px" },
      "ISBN から目次の候補を探します。見つからないときは目次ページの写真、それも読めないときは手入力に切り替わります。"
      + "書名から目次を推測して作ることはありません。"),

    el(
      "label",
      { class: "field" },
      el("span", { class: "field__label" }, "本のタイトル"),
      el("input", {
        class: "input", type: "text", maxlength: "100", value: state.bookTitle,
        onInput: (e) => { state.bookTitle = e.target.value; },
      }),
    ),

    el(
      "label",
      { class: "field" },
      el("span", { class: "field__label" }, "ISBN（13桁・ハイフンなし）"),
      el("input", {
        class: "input", type: "text", inputmode: "numeric", maxlength: "13",
        value: state.isbn, style: "max-width:16em",
        onInput: (e) => { state.isbn = e.target.value.trim(); },
      }),
      el("p", { class: "field__note" }, "分からなければ空のまま、下の手入力へ進めます。"),
    ),

    el(
      "div",
      { class: "btns" },
      el("button", {
        type: "button", class: "btn", disabled: state.lookupBusy,
        onClick: startLookup,
      }, state.lookupBusy ? "探しています" : "目次を探す"),
    ),

    state.error && el("p", { class: "err", style: "margin-top:16px" }, state.error),

    lookupPanel(),
    manualPanel(),
    sectionsPreview(),

    nav({
      next: () => {
        if (state.sections.length === 0) {
          state.error = "節を1つ以上決めてください。";
          return draw();
        }
        if (!state.bookTitle.trim()) {
          state.error = "本のタイトルを入れてください。";
          return draw();
        }
        state.error = "";
        state.step = 1;
        draw();
      },
      disabled: state.sections.length === 0,
    }),
  );
}

function lookupPanel() {
  const lk = state.lookup;
  if (!lk) return null;

  if (!TERMINAL.has(lk.status)) {
    const text = {
      resolving_book: "本を特定しています",
      searching: "公開されているページを探しています",
      verifying: "見つけた目次を取得元のページと照合しています",
      reading_image: "画像から文字を書き写しています",
    }[lk.status] ?? "処理しています";
    return el("p", { class: "note", style: "margin-top:24px" }, `${text}…`);
  }

  if (lk.status === "needs_image") {
    return el(
      "div",
      { style: "margin-top:24px" },
      el("p", { class: "note note--warn" }, `${REASON_TEXT[lk.reason_code] ?? ""}目次ページの写真を送ってください。`),
      el(
        "label",
        { class: "field", style: "margin-top:16px" },
        el("span", { class: "field__label", id: "toc-img-label" }, "目次ページの画像（1〜5枚）"),
        el("input", {
          class: "input", type: "file", id: "toc-images",
          accept: "image/jpeg,image/png,image/webp", multiple: true,
          "aria-describedby": "toc-img-note",
        }),
        el("p", { class: "field__note", id: "toc-img-note" },
          "1枚4MB・合計10MBまで。画像は文字の書き写しにだけ使い、保存しません。"),
      ),
      el("div", { class: "btns" },
        el("button", { type: "button", class: "btn", onClick: submitImages }, "画像を送る")),
    );
  }

  if (lk.status === "failed") {
    return el("p", { class: "note note--warn", style: "margin-top:24px" },
      `${REASON_TEXT[lk.reason_code] ?? "取得できませんでした。"}下の手入力で節を作ってください。`);
  }

  // succeeded：候補を確認・修正してもらう
  const source = lk.source === "web"
    ? el("p", { class: "field__note" }, "取得元：", ...lk.source_urls.map((u) =>
      el("a", { href: u, target: "_blank", rel: "noreferrer noopener" }, u)))
    : el("p", { class: "field__note" },
      `写真から書き写しました。読み取れなかった箇所が ${lk.unreadable_count} 件あります。手元の写真と見比べてください。`);

  return el(
    "div",
    { style: "margin-top:24px" },
    el("p", { class: "seals__label" }, "見つかった目次の候補"),
    source,
    // チェックと文字の修正を分ける。label で囲むと、文字を直すたびにチェックが動く
    el(
      "div",
      { class: "checks", style: "margin-top:16px" },
      ...lk.entries.map((entry, i) => el(
        "div",
        { class: "check", style: `padding-left:${16 + (entry.level - 1) * 20}px` },
        el("input", {
          type: "checkbox",
          checked: state.chosen.has(i),
          "aria-label": `「${entry.title}」を今回の節にする`,
          onChange: (e) => {
            if (e.target.checked) state.chosen.add(i); else state.chosen.delete(i);
            draw();
          },
        }),
        el("input", {
          class: "input",
          type: "text",
          value: state.entryTitles.get(i) ?? entry.title,
          maxlength: "200",
          style: "min-height:36px",
          "aria-label": `${i + 1}件目の節の名前`,
          // ここで draw() すると入力のたびに文字位置が戻るので、state だけ更新する
          onInput: (e) => { state.entryTitles.set(i, e.target.value); },
        }),
      )),
    ),
    el(
      "div",
      { class: "btns", style: "margin-top:16px" },
      el("button", {
        type: "button", class: "btn", disabled: state.chosen.size === 0,
        onClick: () => {
          state.sections = [...state.chosen]
            .sort((a, b) => a - b)
            .map((i) => (state.entryTitles.get(i) ?? lk.entries[i].title).trim())
            .filter((title) => title.length > 0)
            .map((title, n) => ({ id: `sec_${n + 1}`, title }));
          state.tocSource = lk.source === "web"
            ? { kind: "web", urls: lk.source_urls }
            : { kind: "image", urls: [] };
          if (lk.book?.title && !state.bookTitle) state.bookTitle = lk.book.title;
          state.error = "";
          draw();
        },
      }, "選んだ項目を今回の節にする"),
    ),
  );
}

function manualPanel() {
  return el(
    "details",
    { style: "margin-top:24px" },
    el("summary", { style: "cursor:pointer;font-size:15px" }, "手で入力する"),
    el(
      "label",
      { class: "field", style: "margin-top:16px" },
      el("span", { class: "field__label" }, "節・テーマを1行に1つ"),
      // textarea の中身は属性ではなく文字として渡す（value 属性は効かない）
      el("textarea", {
        class: "textarea", rows: "6",
        onInput: (e) => { state.manualText = e.target.value; },
      }, state.manualText),
    ),
    el("div", { class: "btns" }, el("button", {
      type: "button", class: "btn",
      onClick: () => {
        state.sections = state.manualText.split("\n")
          .map((line) => line.trim())
          .filter((line) => line.length > 0 && line.length <= 200)
          .map((title, i) => ({ id: `sec_${i + 1}`, title }));
        state.tocSource = { kind: "manual", urls: [] };
        state.error = "";
        draw();
      },
    }, "この内容を今回の節にする")),
  );
}

function sectionsPreview() {
  if (state.sections.length === 0) return null;
  const KIND = { web: "取得元ページと照合済み", image: "写真からの書き写し", manual: "手入力" };
  return el(
    "div",
    { style: "margin-top:40px" },
    el("p", { class: "seals__label" }, `今回の節（${state.sections.length}件・${KIND[state.tocSource.kind]}）`),
    el("ul", { class: "rows", role: "list" }, ...state.sections.map((s, i) => el(
      "li",
      { class: "row" },
      el("span", { class: "row__time" }, String(i + 1)),
      el("span", { class: "row__body" }, s.title),
      el("span", { class: "row__sub" }, ""),
    ))),
  );
}

async function startLookup() {
  if (!isValidIsbn13(state.isbn)) {
    state.error = "ISBN はハイフンなしの13桁で入れてください。分からなければ手入力へ進めます。";
    return draw();
  }
  state.error = "";
  state.lookupBusy = true;
  draw();

  try {
    state.lookup = await client.startTocLookup(state.group.id, state.isbn);
    await pollLookup();
  } catch (err) {
    state.error = err.message || "目次を探せませんでした。手入力で進めてください。";
  } finally {
    state.lookupBusy = false;
    draw();
  }
}

/** 3秒間隔で取得状況を見る（R7）。終わる状態になったら止める。 */
async function pollLookup() {
  for (let i = 0; i < 100 && !TERMINAL.has(state.lookup.status); i++) {
    draw();
    await new Promise((r) => setTimeout(r, 3000));
    state.lookup = await client.tocLookup(state.group.id, state.lookup.id);
  }
  state.entryTitles = new Map();
  state.chosen = new Set();
  if (state.lookup.status === "succeeded") {
    // 候補は既定で全部チェックしておく。管理者は外す・直すだけで済む
    state.lookup.entries.forEach((entry, i) => {
      state.entryTitles.set(i, entry.title);
      state.chosen.add(i);
    });
  }
}

async function submitImages() {
  const input = document.getElementById("toc-images");
  const files = [...(input?.files ?? [])];
  if (files.length === 0 || files.length > 5) {
    state.error = "画像を1〜5枚選んでください。";
    return draw();
  }
  state.error = "";
  state.lookupBusy = true;
  draw();
  try {
    state.lookup = await client.submitTocImages(state.group.id, state.lookup.id, files);
    await pollLookup();
  } catch (err) {
    state.error = err.message || "画像を送れませんでした。";
  } finally {
    state.lookupBusy = false;
    draw();
  }
}

// ------------------------------------------------------------------
// 2. 今回の範囲
// ------------------------------------------------------------------

function stepScope() {
  const box = (set, other, s) => el(
    "label",
    { class: "check" },
    el("input", {
      type: "checkbox",
      checked: set.has(s.id),
      onChange: (e) => {
        if (e.target.checked) { set.add(s.id); other.delete(s.id); } else set.delete(s.id);
        draw();
      },
    }),
    el("span", {}, s.title),
  );

  return el(
    "section",
    { class: "sec" },
    el("div", { class: "sec__head" }, el("h1", { class: "sec__title" }, "今回の範囲")),
    el("p", { class: "prose", style: "margin-bottom:24px" },
      "同じ節を両方に入れることはできません。今回扱う範囲は1件以上選んでください。"),

    el(
      "div",
      { class: "field" },
      el("span", { class: "field__label" }, "前回までに読んだ範囲"),
      el("div", { class: "checks" }, ...state.sections.map((s) => box(state.completed, state.target, s))),
    ),

    el(
      "div",
      { class: "field" },
      el("span", { class: "field__label" }, "今回扱う範囲"),
      el("div", { class: "checks" }, ...state.sections.map((s) => box(state.target, state.completed, s))),
    ),

    state.error && el("p", { class: "err" }, state.error),

    nav({
      next: () => {
        if (state.target.size === 0) {
          state.error = "今回扱う範囲を1件以上選んでください。";
          return draw();
        }
        state.error = "";
        state.step = 2;
        draw();
      },
    }),
  );
}

// ------------------------------------------------------------------
// 3. 日時
// ------------------------------------------------------------------

function stepWhen() {
  return el(
    "section",
    { class: "sec" },
    el("div", { class: "sec__head" }, el("h1", { class: "sec__title" }, "日時")),
    el("p", { class: "prose", style: "margin-bottom:24px" },
      "登録すると、グループ全員に参加条件の確認が届きます。登録後の本・範囲・日時は変更できません。"),

    el(
      "label",
      { class: "field" },
      el("span", { class: "field__label" }, "回の名前"),
      el("input", {
        class: "input", type: "text", maxlength: "100", value: state.title,
        placeholder: "第2回",
        onInput: (e) => { state.title = e.target.value; },
      }),
    ),

    el(
      "label",
      { class: "field" },
      el("span", { class: "field__label" }, "開始日時"),
      el("input", {
        class: "input", type: "datetime-local", value: state.startsAt, style: "max-width:18em",
        onInput: (e) => { state.startsAt = e.target.value; },
      }),
      el("p", { class: "field__note" }, "いまから1時間より先の日時にしてください。"),
    ),

    el(
      "label",
      { class: "field" },
      el("span", { class: "field__label" }, "持ち時間（分）"),
      el("input", {
        class: "input", type: "number", min: "15", max: "480", step: "5",
        value: String(state.duration), style: "max-width:9em",
        onInput: (e) => { state.duration = Number(e.target.value); },
      }),
    ),

    state.error && el("p", { class: "err" }, state.error),

    nav({ next: create, nextText: state.busy ? "登録しています" : "この内容で登録する", disabled: state.busy }),
  );
}

async function create() {
  if (!state.title.trim()) { state.error = "回の名前を入れてください。"; return draw(); }
  if (!state.startsAt) { state.error = "開始日時を入れてください。"; return draw(); }

  const startsAt = new Date(state.startsAt);
  if (Number.isNaN(startsAt.getTime()) || startsAt.getTime() - Date.now() < 3600_000) {
    state.error = "開始日時は、いまから1時間より先にしてください。";
    return draw();
  }

  state.error = "";
  state.busy = true;
  draw();

  try {
    const res = await client.createSession(state.group.id, {
      playbook_id: "reading",
      title: state.title.trim(),
      starts_at: startsAt.toISOString(),
      duration_minutes: state.duration,
      data: {
        book_title: state.bookTitle.trim(),
        isbn: isValidIsbn13(state.isbn) ? state.isbn : null,
        toc_source: state.tocSource,
        sections: state.sections,
        completed_section_ids: state.sections.filter((s) => state.completed.has(s.id)).map((s) => s.id),
        target_section_ids: state.sections.filter((s) => state.target.has(s.id)).map((s) => s.id),
      },
    });
    location.href = href("/session.html", { id: res.session.id });
  } catch (err) {
    state.busy = false;
    state.error = err.fieldErrors?.[0]?.message || err.message || "登録できませんでした。";
    draw();
  }
}

/** ISBN-13 のチェックディジット（R3）。 */
function isValidIsbn13(value) {
  if (!/^\d{13}$/.test(value)) return false;
  const digits = [...value].map(Number);
  const sum = digits.slice(0, 12).reduce((acc, d, i) => acc + d * (i % 2 === 0 ? 1 : 3), 0);
  return (10 - (sum % 10)) % 10 === digits[12];
}
