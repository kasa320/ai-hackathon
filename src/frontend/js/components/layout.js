// 全画面共通のシェル：背景の書棚とヘッダー。
//
// 画面ごとの中身は各ページのスクリプトが描く。ここは現在地と利用者だけを扱う。

import { el, mount } from "../dom.js";
import { mountBookshelf } from "./bookshelf.js";

const $ = (id) => document.getElementById(id);

/** ヘッダー。いまどこを見ているかと、誰として見ているか。 */
export function mountTopbar({ crumbs = [], me = null } = {}) {
  const trail = [];
  crumbs.forEach((c, i) => {
    if (i > 0) trail.push(el("span", { class: "crumbs__sep" }, "／"));
    trail.push(c.href
      ? el("a", { class: "crumbs__item", href: c.href }, c.label)
      : el("span", { class: i === crumbs.length - 1 ? "crumbs__now" : "crumbs__item" }, c.label));
  });

  mount($("topbar"),
    el("a", { class: "logo", href: "/" },
      el("span", { class: "logo__spines", "aria-hidden": "true" },
        el("i"), el("i"), el("i"), el("i")),
      "輪読エージェント",
    ),
    trail.length ? el("nav", { class: "crumbs", "aria-label": "現在地" }, trail) : null,
    el("div", { class: "topbar__right" },
      me
        ? el("span", { class: "whoami" },
            el("span", { class: "whoami__avatar" }, me.display_name.slice(0, 1)),
            `${me.display_name} さん`)
        : null,
    ),
  );
}

/** 背景の書棚を出す。全画面で最初に1回呼ぶ。 */
export function mountBackdrop() {
  mountBookshelf($("backdrop"));
}
