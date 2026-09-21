// 輪読：目次の取り込み。
//
// 入れ方は3つ：ISBNから（国立国会図書館サーチに登録された目次）、目次ページの写真から、手入力。
// どの入れ方でもいつでも切り替えられる。取り込んだ目次は候補にすぎず、管理者が選んで直したものだけが
// 開催回の範囲になる。登録された目次がなければサーバーが写真を求めるので、推測して埋めない。
// 写真はサーバーがモデルへ送るためだけに使い、保存されない。

import { el, mount } from "../../dom.js";
import { placeholder } from "../../ui.js";

const POLL_MS = 3000;

// 目次画像の上限と文言。バックエンド（toc/model.go・toc/http.go）と揃える。
const MAX_IMAGES = 10;
const MAX_IMAGE_BYTES = 4 * 1024 * 1024;
const MAX_TOTAL_BYTES = 20 * 1024 * 1024;
const IMAGE_COUNT_MESSAGE = `画像は1〜${MAX_IMAGES}枚にしてください`;
const IMAGE_SIZE_MESSAGE = "画像は1枚4 MBまでにしてください。";
const IMAGE_TOTAL_MESSAGE = "画像は合計20 MBまでにしてください。";

const STATUS_TEXT = {
  resolving_book: "本を調べています…",
  searching: "目次を探しています…",
  verifying: "取得元のページと照合しています…",
  reading_image: "写真を読み取っています…",
};

const REASON_TEXT = {
  book_not_found: "このISBNの本が見つかりませんでした。",
  toc_not_found: "この本の目次は登録されていませんでした。",
  source_mismatch: "見つかった目次が取得元のページと一致しませんでした。推測では埋めません。",
  image_unreadable: "写真から目次を読み取れませんでした。",
  budget_exceeded: "今日の取得回数の上限に達しました。",
  model_error: "取得処理が失敗しました。",
};

const SOURCE_TEXT = {
  ndl: "国立国会図書館サーチ（出版情報登録センターの登録データ）から取得しました",
  web: "Webから取得し、取得元と照合しました",
  image: "写真から読み取りました",
};

const MODES = [
  ["isbn", "ISBNから"],
  ["image", "目次の写真から"],
  ["manual", "手入力"],
];

/**
 * ISBN-10・ハイフン入りも受け付け、ハイフンなしの ISBN-13 にする。形やチェック数字が違えば null。
 * @param {string} raw
 */
export function normalizeISBN(raw) {
  const s = raw.replace(/[\s\-‐－]/g, "").toUpperCase();
  if (/^97[89]\d{10}$/.test(s)) {
    const sum = [...s].reduce((n, d, i) => n + Number(d) * (i % 2 ? 3 : 1), 0);
    return sum % 10 === 0 ? s : null;
  }
  if (!/^\d{9}[\dX]$/.test(s)) return null;
  const sum10 = [...s].reduce((n, d, i) => n + (d === "X" ? 10 : Number(d)) * (10 - i), 0);
  if (sum10 % 11 !== 0) return null;
  const body = `978${s.slice(0, 9)}`;
  const sum = [...body].reduce((n, d, i) => n + Number(d) * (i % 2 ? 3 : 1), 0);
  return body + String((10 - (sum % 10)) % 10);
}

/**
 * 目次の取り込み。onPick(sections, source, book) で選んだ節の一覧を返す。
 * 手入力を選んだときは onManual() を呼ぶ（範囲の欄はそのまま編集できる）。
 * picked は前に取り込んだ目次の取得元（戻ってきたとき、取り込み済みと表示する）。
 */
export function createTocPicker(client, groupId, { onPick, onManual, picked = null }) {
  const node = el("div", {});
  const body = el("div", { style: "margin-top:16px" });
  let mode = picked?.kind === "image" ? "image" : picked?.kind === "manual" ? "manual" : "isbn";
  let timer = null;
  let lookup = null;
  let run = 0; // 切り替えたら古い取得の結果を捨てる
  let lastIsbn = "";

  const radios = el(
    "div",
    { class: "inline", role: "radiogroup", "aria-label": "目次の入れ方" },
    MODES.map(([value, label]) =>
      el(
        "label",
        {},
        el("input", {
          type: "radio",
          name: "toc-mode",
          value,
          checked: mode === value,
          onChange: () => switchTo(value),
        }),
        label,
      ),
    ),
  );
  mount(node, el("div", { class: "field" }, el("span", {}, "目次の入れ方")), radios, body);

  function cancel() {
    run++;
    clearTimeout(timer);
    lookup = null;
  }

  function switchTo(next) {
    cancel();
    mode = next;
    for (const input of radios.querySelectorAll("input")) input.checked = input.value === next;
    if (next === "isbn") renderIsbn();
    if (next === "image") startImage();
    if (next === "manual") {
      mount(body, el("p", { class: "help", style: "margin-top:0" }, "下の「範囲」に章や節の名前を入力してください。"));
      onManual();
    }
  }

  function renderIsbn(message = null) {
    const input = el("input", { type: "text", inputmode: "numeric", autocomplete: "off", placeholder: "978-4-297-12783-1", value: lastIsbn });
    const submit = () => start(input.value);
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); submit(); }
    });
    mount(
      body,
      el("label", { class: "field" }, el("span", {}, "ISBN（10桁・13桁、ハイフンありでも可）"), input),
      message ? el("p", { class: "field__error", role: "alert", style: "margin-top:8px" }, message) : null,
      el("p", { class: "help" }, "出版社が目次を登録している本だけ取得できます。見つからなければ写真か手入力に切り替えられます。"),
      el("div", { style: "margin-top:12px" }, el("button", { type: "button", class: "btn", onClick: submit }, "目次を取得する")),
    );
  }

  async function start(raw) {
    lastIsbn = raw.trim();
    const isbn = normalizeISBN(lastIsbn);
    if (!isbn) {
      renderIsbn("ISBNは10桁か13桁の数字で入れてください。");
      body.querySelector("input")?.focus();
      return;
    }
    const current = ++run;
    renderWaiting("resolving_book");
    try {
      lookup = await client.startTocLookup(groupId, isbn);
      if (current !== run) return;
      route(current);
    } catch (err) {
      if (current !== run) return;
      renderIsbn(err.message ?? "取得を開始できませんでした。");
    }
  }

  /** 写真から読み取る。取得は写真を送るときに始める（切り替えただけでは1日の回数を使わない）。 */
  function startImage() {
    renderNeedsImage(++run);
  }

  function renderWaiting(status) {
    mount(
      body,
      placeholder(
        STATUS_TEXT[status] ?? "取得しています…",
        status === "searching" ? "国立国会図書館サーチへの問い合わせに数十秒かかることがあります。待たずに入れ方を切り替えてもかまいません。" : "",
      ),
    );
  }

  function poll(current) {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      try {
        const next = await client.tocLookup(groupId, lookup.id);
        if (current !== run) return;
        lookup = next;
      } catch (err) {
        if (current !== run) return;
        mount(body, el("p", { class: "field__error", role: "alert" }, err.message ?? "状況を取得できませんでした。"));
        return;
      }
      route(current);
    }, POLL_MS);
  }

  function route(current) {
    switch (lookup.status) {
      case "succeeded":
        renderEntries();
        break;
      case "needs_image":
        renderNeedsImage(current);
        break;
      case "failed":
        renderFailed();
        break;
      default:
        renderWaiting(lookup.status);
        poll(current);
    }
  }

  function renderNeedsImage(current, message = null) {
    const input = el("input", { type: "file", accept: "image/jpeg,image/png,image/webp", multiple: true });
    const error = el("p", { class: "field__error", role: "alert" }, message ?? "");
    const reason = lookup?.reason_code;
    mount(
      body,
      reason
        ? el(
            "div",
            { class: "notice", "data-tone": "warn", style: "margin-bottom:16px" },
            el("span", { class: "notice__mark" }, "!"),
            el(
              "span",
              {},
              el("strong", {}, "ISBNからは目次を取得できませんでした"),
              el("small", {}, `${REASON_TEXT[reason] ?? ""}目次ページの写真を送るか、手入力に切り替えてください。`),
            ),
          )
        : null,
      bookLine(),
      el("label", { class: "field" }, el("span", {}, "目次ページの写真（1〜10枚、1枚4MB・合計20MBまで）"), input),
      el("p", { class: "help" }, "写真は読み取りにだけ使い、保存しません。"),
      error,
      el(
        "div",
        { style: "margin-top:12px" },
        el(
          "button",
          {
            type: "button",
            class: "btn",
            onClick: async () => {
              const files = [...(input.files ?? [])];
              if (!files.length) { error.textContent = "目次の写真を選んでください。"; return; }
              if (files.length > MAX_IMAGES) { error.textContent = IMAGE_COUNT_MESSAGE; return; }
              if (files.some((f) => f.size > MAX_IMAGE_BYTES)) { error.textContent = IMAGE_SIZE_MESSAGE; return; }
              if (files.reduce((n, f) => n + f.size, 0) > MAX_TOTAL_BYTES) { error.textContent = IMAGE_TOTAL_MESSAGE; return; }
              renderWaiting("reading_image");
              try {
                // 写真から始めた場合は、ここで ISBN なしの取得を作る
                if (!lookup) lookup = await client.startTocLookup(groupId, "");
                if (current !== run) return;
                if (lookup.status !== "needs_image") return route(current);
                lookup = await client.submitTocImages(groupId, lookup.id, files);
                if (current !== run) return;
                route(current);
              } catch (err) {
                if (current !== run) return;
                renderNeedsImage(current, err.message ?? "写真を送れませんでした。");
              }
            },
          },
          "この写真から読み取る",
        ),
      ),
    );
  }

  function renderFailed() {
    const retry = mode === "image"
      ? el("button", { type: "button", class: "btn btn--quiet", onClick: () => switchTo("image") }, "別の写真で試す")
      : el("button", { type: "button", class: "btn btn--quiet", onClick: () => switchTo("isbn") }, "別のISBNで試す");
    mount(
      body,
      el(
        "div",
        { class: "notice", "data-tone": "warn" },
        el("span", { class: "notice__mark" }, "!"),
        el("span", {}, el("strong", {}, "目次を用意できませんでした"), el("small", {}, REASON_TEXT[lookup.reason_code] ?? "")),
      ),
      el("div", { style: "display:flex;flex-wrap:wrap;gap:12px;margin-top:12px" }, retry),
    );
  }

  function bookLine() {
    if (!lookup?.book) return null;
    const b = lookup.book;
    return el("p", { class: "source", style: "margin-bottom:12px" }, el("span", {}, `${b.title}${b.publisher ? `（${b.publisher}）` : ""}`));
  }

  function sourceLine(source, urls, unreadable = 0) {
    return el(
      "p",
      { class: "source" },
      el("span", {}, SOURCE_TEXT[source] ?? "取り込みました"),
      ...(urls ?? []).map((url) => el("a", { href: url, target: "_blank", rel: "noreferrer noopener" }, "取得元")),
      unreadable > 0 ? el("span", {}, `読めなかった箇所 ${unreadable}件`) : null,
    );
  }

  function renderEntries() {
    const error = el("p", { class: "field__error", role: "alert" });
    const checks = el(
      "div",
      { class: "toc-list" },
      lookup.entries.map((entry, i) =>
        el(
          "label",
          { "data-level": String(entry.level) },
          el("input", { type: "checkbox", value: String(i), checked: entry.level <= 2 }),
          entry.title,
        ),
      ),
    );
    const setAll = (on) => { for (const input of checks.querySelectorAll("input")) input.checked = on; };

    mount(
      body,
      bookLine(),
      sourceLine(lookup.source, lookup.source_urls, lookup.unreadable_count),
      el("p", { class: "help" }, "今回扱う範囲にチェックを入れてください。取り込んだあとも下の「範囲」で直せます。"),
      el(
        "div",
        { style: "display:flex;gap:8px;margin-top:8px" },
        el("button", { type: "button", class: "btn btn--quiet", onClick: () => setAll(true) }, "すべて選ぶ"),
        el("button", { type: "button", class: "btn btn--quiet", onClick: () => setAll(false) }, "すべて外す"),
      ),
      checks,
      error,
      el(
        "div",
        { style: "margin-top:12px" },
        el(
          "button",
          {
            type: "button",
            class: "btn",
            onClick: () => {
              const chosen = [...checks.querySelectorAll("input:checked")].map((input, n) => ({
                id: `sec_${n + 1}`,
                title: lookup.entries[Number(input.value)].title,
              }));
              if (chosen.length === 0) { error.textContent = "今回扱う範囲を1つ以上選んでください。"; return; }
              const source = { kind: lookup.source === "image" ? "image" : "web", urls: lookup.source_urls ?? [] };
              const done = lookup;
              cancel();
              renderPicked(done.source, done.source_urls, chosen.length);
              onPick(chosen, source, done.book);
            },
          },
          "選んだ範囲を取り込む",
        ),
      ),
    );
  }

  /** 取り込んだあと。取り直しもできる。 */
  function renderPicked(source, urls, count) {
    mount(
      body,
      sourceLine(source, urls),
      el("p", { class: "help" }, count ? `${count}件を下の「範囲」に取り込みました。` : "下の「範囲」に取り込み済みです。"),
      el(
        "div",
        { style: "margin-top:12px" },
        el("button", { type: "button", class: "btn btn--quiet", onClick: () => switchTo(mode) }, "目次を取り直す"),
      ),
    );
  }

  // 前に取り込んだ目次があれば（前の手順から戻ってきたとき）、取り込み済みとして表示する
  if (picked && picked.kind !== "manual") renderPicked(picked.kind === "image" ? "image" : picked.urls?.[0]?.includes("ndlsearch.ndl.go.jp") ? "ndl" : "web", picked.urls, 0);
  else if (mode === "manual") mount(body, el("p", { class: "help", style: "margin-top:0" }, "下の「範囲」に章や節の名前を入力してください。"));
  else renderIsbn();

  return { node, stop: cancel };
}
