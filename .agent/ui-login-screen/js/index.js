// トップ画面。契約 第3部「初期表示・ログイン」「グループ選択」。
//
// 未ログインのときは、この仕組みが何をするかを組み直しの実例で見せる（HTML に静的に置いてある）。
// ログイン後は輪読会と開催回の選択に切り替える。

import { el, mount, formatDateTime } from "./dom.js";
import { client, href } from "./client.js";
import { renderNav, renderDevBar, renderHealth } from "./components/chrome.js";

const $guest = document.getElementById("guest");
const $member = document.getElementById("member");

boot();

async function boot() {
  const health = await renderHealth();
  renderDevBar({ devMode: health.dev_mode });

  const login = document.getElementById("login");
  if (login) login.href = client.loginUrl("/");

  let me = null;
  try {
    me = await client.me();
  } catch (err) {
    if (err.status !== 401) console.error(err);
    renderNav(null, "top");
    return;                    // 未ログインの画面のまま
  }

  renderNav(me, "top");
  $guest.hidden = true;
  $member.hidden = false;
  await renderMember();
}

async function renderMember() {
  let groups;
  try {
    groups = await client.groups();
  } catch (err) {
    return mount($member, el("div", { class: "wrap" }, el("p", { class: "note note--warn" },
      "輪読会の一覧を取得できませんでした。時間をおいて開き直してください。")));
  }

  if (groups.items.length === 0) {
    return mount($member, el(
      "div",
      { class: "wrap" },
      el(
        "section",
        { class: "sec" },
        el("div", { class: "sec__head" }, el("h1", { class: "sec__title" }, "輪読会")),
        el("p", { class: "empty" },
          el("strong", {}, "まだ輪読会がありません"),
          "管理者が輪読会を作り、メンバーの Discord ユーザーIDを登録すると、ここに出ます。"),
      ),
    ));
  }

  const params = new URLSearchParams(location.search);
  const selected = groups.items.find((g) => g.id === params.get("group")) ?? groups.items[0];

  const container = el("div", { class: "wrap" });
  mount($member, container);

  const groupSection = groups.items.length > 1
    ? el(
      "section",
      { class: "sec" },
      el("div", { class: "sec__head" }, el("h1", { class: "sec__title" }, "輪読会")),
      el(
        "ul",
        { class: "rows", role: "list" },
        ...groups.items.map((g) => el(
          "li",
          {},
          el(
            "a",
            { class: "row row--link", href: href("/", { group: g.id }), style: "display:grid" },
            el("span", { class: "row__time" }, g.role === "owner" ? "管理者" : "メンバー"),
            el("span", { class: "row__body" }, g.name),
            el("span", { class: "row__sub" }, `${g.member_count}人`),
          ),
        )),
      ),
    )
    : null;

  const sessionSection = el("section", { class: "sec" });
  mount(container, groupSection, sessionSection);

  let sessions;
  try {
    sessions = await client.sessions(selected.id);
  } catch {
    return mount(sessionSection, el("p", { class: "note note--warn" }, "開催回の一覧を取得できませんでした。"));
  }

  const isOwner = selected.role === "owner";

  mount(
    sessionSection,
    el(
      "div",
      { class: "sec__head" },
      el("h1", { class: "sec__title" }, "開催回"),
      el("span", { class: "sec__meta" }, selected.name),
    ),

    sessions.items.length === 0
      ? el("p", { class: "empty" },
        el("strong", {}, "開催回がまだありません"),
        isOwner
          ? "本と今回の範囲、日時を登録すると、全員に参加条件の確認が届きます。"
          : "管理者が登録すると、参加条件の確認がここと Discord に届きます。")
      : el(
        "ul",
        { class: "rows", role: "list" },
        ...sessions.items.map(sessionRow),
      ),

    isOwner && el(
      "div",
      { class: "btns", style: "margin-top:24px" },
      el("a", { class: "btn btn--main", href: href("/setup.html", { group: selected.id }) }, "開催回を登録する"),
    ),
  );
}

function sessionRow(s) {
  const STATUS = {
    draft: ["登録中", "tag"],
    confirmed: ["確定", "tag tag--kon"],
    needs_attention: ["要調整", "tag tag--shu"],
  }[s.status] ?? [s.status, "tag"];

  return el(
    "li",
    {},
    el(
      "a",
      { class: "row row--link", href: href("/session.html", { id: s.id }), style: "display:grid" },
      el("time", { class: "row__time" }, formatDateTime(s.starts_at)),
      el("span", { class: "row__body" }, s.title),
      el("span", { class: STATUS[1] }, STATUS[0]),
    ),
  );
}
