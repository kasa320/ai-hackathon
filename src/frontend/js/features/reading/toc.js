// 輪読：ISBNからの目次の取得。
//
// 取得できたものは候補にすぎない。管理者が選んで直したものだけが開催回の範囲になる。
// 取得元と照合できなかったときはサーバーが画像を求めるので、ここでは推測して埋めない。
// 画像はサーバーがモデルへ送るためだけに使い、保存されない。

import { el, mount } from "../../dom.js";
import { placeholder } from "../../ui.js";

const POLL_MS = 3000;

const STATUS_TEXT = {
  resolving_book: "本を調べています…",
  searching: "目次を探しています…",
  verifying: "取得元のページと照合しています…",
  reading_image: "画像を読み取っています…",
};

const REASON_TEXT = {
  book_not_found: "この ISBN の本が書誌データベースで見つかりませんでした。",
  toc_not_found: "目次を載せたページが見つかりませんでした。",
  source_mismatch: "見つかった目次が取得元のページと一致しませんでした。推測では埋めません。",
  image_unreadable: "画像から目次を読み取れませんでした。",
  budget_exceeded: "今日の取得回数の上限に達しました。",
  model_error: "取得処理が失敗しました。",
};

/**
 * 目次の取得と選択。onPick(sections) で確定した節の一覧を返す。
 * 手入力に切り替えたいときは onManual() を呼ぶ。
 */
export function createTocPicker(client, groupId, { onPick, onManual }) {
  const node = el("div", {});
  let timer = null;
  let lookup = null;
  let active = true;
  let lastIsbn = "";

  const stop = () => { active = false; clearTimeout(timer); };
  const manual = () => { stop(); onManual(); };

  function renderIsbn(message = null) {
    const input = el("input", { type: "text", inputmode: "numeric", placeholder: "9784297127831", value: lastIsbn });
    mount(
      node,
      el(
        "div",
        {},
        el("label", { class: "field" }, el("span", {}, "ISBN（13桁）"), input),
        message ? el("p", { class: "field__error", style: "margin-top:8px" }, message) : null,
        el(
          "div",
          { style: "display:flex;gap:12px;margin-top:16px" },
          el("button", { type: "button", class: "btn", onClick: () => start(input.value.trim()) }, "目次を取得する"),
          el("button", { type: "button", class: "btn btn--quiet", onClick: manual }, "手入力する"),
        ),
      ),
    );
  }

  async function start(isbn) {
    lastIsbn = isbn;
    if (!/^\d{10}$|^\d{13}$/.test(isbn)) {
      renderIsbn("ISBNは10桁か13桁の数字で入れてください。");
      return;
    }
    renderWaiting("resolving_book");
    try {
      lookup = await client.startTocLookup(groupId, isbn);
      if (!active) return;
      poll();
    } catch (err) {
      if (!active) return;
      renderIsbn(err.message ?? "取得を開始できませんでした。");
    }
  }

  function renderWaiting(status) {
    mount(node, placeholder(STATUS_TEXT[status] ?? "取得しています…", ""),
      el("button", { type: "button", class: "btn btn--quiet", onClick: manual }, "手入力に切り替える"));
  }

  function poll() {
    clearTimeout(timer);
    if (!active) return;
    timer = setTimeout(async () => {
      try {
        lookup = await client.tocLookup(groupId, lookup.id);
        if (!active) return;
      } catch (err) {
        if (!active) return;
        renderIsbn(err.message ?? "状況を取得できませんでした。");
        return;
      }
      route();
    }, POLL_MS);
  }

  function route() {
    switch (lookup.status) {
      case "succeeded":
        renderEntries();
        break;
      case "needs_image":
        renderNeedsImage();
        break;
      case "failed":
        renderFailed();
        break;
      default:
        renderWaiting(lookup.status);
        poll();
    }
  }

  function renderNeedsImage() {
    const input = el("input", { type: "file", accept: "image/jpeg,image/png,image/webp", multiple: true });
    const error = el("p", { class: "field__error", role: "alert" });
    mount(
      node,
      el(
        "div",
        {},
        el(
          "div",
          { class: "notice", "data-tone": "warn" },
          el("span", { class: "notice__mark" }, "!"),
          el(
            "span",
            {},
            el("strong", {}, "目次を取得できませんでした"),
            el("small", {}, REASON_TEXT[lookup.reason_code] ?? "取得元と照合できませんでした。"),
          ),
        ),
        bookLine(),
        el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "目次ページの写真（1〜5枚、1枚4MBまで）"), input),
        el("p", { class: "help" }, "画像は保存しません。"),
        error,
        el(
          "div",
          { style: "display:flex;gap:12px;margin-top:16px" },
          el(
            "button",
            {
              type: "button",
              class: "btn",
              onClick: async () => {
                if (!input.files?.length) { error.textContent = "目次の画像を選んでください。"; return; }
                renderWaiting("reading_image");
                try {
                  lookup = await client.submitTocImages(groupId, lookup.id, input.files);
                  if (!active) return;
                  poll();
                } catch (err) {
                  if (!active) return;
                  renderNeedsImage();
                  node.append(el("p", { class: "field__error" }, err.message ?? "画像を送れませんでした。"));
                }
              },
            },
            "この画像から読み取る",
          ),
          el("button", { type: "button", class: "btn btn--quiet", onClick: manual }, "手入力する"),
        ),
      ),
    );
  }

  function renderFailed() {
    mount(
      node,
      el(
        "div",
        {},
        el(
          "div",
          { class: "notice", "data-tone": "warn" },
          el("span", { class: "notice__mark" }, "!"),
          el("span", {}, el("strong", {}, "目次を用意できませんでした"), el("small", {}, REASON_TEXT[lookup.reason_code] ?? "")),
        ),
        el(
          "div",
          { style: "display:flex;gap:12px;margin-top:16px" },
          el("button", { type: "button", class: "btn", onClick: manual }, "手入力する"),
          el("button", { type: "button", class: "btn btn--quiet", onClick: () => renderIsbn() }, "別のISBNで試す"),
        ),
      ),
    );
  }

  function bookLine() {
    if (!lookup?.book) return null;
    const b = lookup.book;
    return el(
      "p",
      { class: "source" },
      el("span", {}, `${b.title}${b.publisher ? `（${b.publisher}）` : ""}`),
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

    mount(
      node,
      el(
        "div",
        {},
        bookLine(),
        el(
          "p",
          { class: "source" },
          el("span", {}, lookup.source === "image" ? "画像から読み取りました" : "Webから取得し、取得元と照合しました"),
          ...(lookup.source_urls ?? []).map((url) =>
            el("a", { href: url, target: "_blank", rel: "noreferrer noopener" }, "取得元"),
          ),
          lookup.unreadable_count > 0 ? el("span", {}, `読めなかった箇所 ${lookup.unreadable_count}件`) : null,
        ),
        checks,
        error,
        el(
          "div",
          { style: "display:flex;gap:12px;margin-top:16px" },
          el(
            "button",
            {
              type: "button",
              class: "btn",
              onClick: () => {
                const picked = [...checks.querySelectorAll("input:checked")].map((input, n) => ({
                  id: `sec_${n + 1}`,
                  title: lookup.entries[Number(input.value)].title,
                }));
                if (picked.length === 0) { error.textContent = "今回扱う範囲を1つ以上選んでください。"; return; }
                stop();
                onPick(picked, { kind: lookup.source === "image" ? "image" : "web", urls: lookup.source_urls ?? [] }, lookup.book);
              },
            },
            "この内容で進む",
          ),
          el("button", { type: "button", class: "btn btn--quiet", onClick: manual }, "手入力に切り替える"),
        ),
      ),
    );
  }

  renderIsbn();
  return { node, stop };
}
