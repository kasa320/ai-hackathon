// トップ画面（/）。契約 第13節で固定されている URL。
// OAuth callback の既定の戻り先でもあるので、?auth_error= を受け取る。

import { api } from "./api.js";
import { createMockApi, MOCK_STATES } from "./mock.js";
import { el, mount, formatDateTime } from "./dom.js";
import { renderHealth } from "./components/health.js";
import { mountBackdrop, mountTopbar } from "./components/layout.js";

const params = new URLSearchParams(location.search);
const useLive = params.get("live") === "1";
const mockOpts = { state: params.get("mock") ?? "replan", viewer: params.get("as") ?? "mem_c" };
const client = useLive ? api : createMockApi(mockOpts);

const $ = (id) => document.getElementById(id);

const AUTH_ERROR = {
  access_denied: "Discordでの許可が得られませんでした。もう一度お試しください。",
  invalid_state: "ログインの手続きが途中で切れました。もう一度お試しください。",
  provider_unavailable: "Discordに接続できませんでした。しばらくしてからお試しください。",
};

const SESSION_LABEL = {
  draft: { text: "準備中", cls: "badge--warn" },
  confirmed: { text: "確定", cls: "badge--ok" },
  needs_attention: { text: "要調整", cls: "badge--danger" },
};

function withParams(next) {
  const p = new URLSearchParams(location.search);
  for (const [k, v] of Object.entries(next)) v === null ? p.delete(k) : p.set(k, v);
  return `${location.pathname}?${p}`;
}

function buildDemoBar() {
  if (useLive) { $("demo-controls").hidden = true; return; }
  mount($("demo-controls"),
    el("label", {}, "状態 ",
      el("select", { onchange: (e) => { location.href = withParams({ mock: e.target.value }); } },
        Object.entries(MOCK_STATES).map(([v, label]) => el("option", { value: v, selected: v === mockOpts.state }, label)))),
    el("label", {}, "表示中 ",
      el("select", { onchange: (e) => { location.href = withParams({ as: e.target.value }); } },
        [["mem_a", "A（管理者）"], ["mem_b", "B"], ["mem_c", "C"], ["mem_d", "D"]].map(([v, label]) =>
          el("option", { value: v, selected: v === mockOpts.viewer }, label)))),
    el("a", { class: "demobar__link", href: withParams({ live: "1" }) }, "実APIで開く"),
  );
}

function renderLoggedOut() {
  $("hero").className = "hero";
  mount($("hero"),
    el("p", { class: "hero__eyebrow" }, el("span", {}, "輪読運営エージェント")),
    el("h1", { class: "hero__title" }, "担当が決まらない日も、", el("br"), "次の輪読は開ける。"),
    el("p", { class: "hero__lead" },
      "担当者が準備できなくなっても、参加者の条件に合わせて次回の計画を組み直し、"
      + "必要な同意を集めて、決まったことを全員に知らせます。読むことと議論することは人がやります。"),
    el("div", { class: "hero__actions" },
      el("a", { class: "btn btn--primary", href: client.loginUrl("/") }, "Discordでログイン")),
  );
  mount($("content"),
    el("p", { class: "section-title" }, "この仕組みが守ること"),
    el("div", { class: "pledges" },
      el("article", { class: "pledge" },
        el("h3", {}, "返事がないことを賛成にしない"),
        el("p", {}, "未回答を同意として数えません。期限までに必要な同意が揃わなければ、勝手に確定せず管理者に判断を返します。"),
      ),
      el("article", { class: "pledge" },
        el("h3", {}, "担当は本人しか引き受けられない"),
        el("p", {}, "集団の賛成票や管理者の承認で、誰かの担当を代わりに引き受けることはできません。"),
      ),
      el("article", { class: "pledge" },
        el("h3", {}, "都合がつかない理由は集めない"),
        el("p", {}, "共有されるのは参加できるかどうかと、担当できる範囲だけです。私的な事情は入力欄そのものがありません。"),
      ),
    ),
  );
}

function renderSessionRow(session) {
  const label = SESSION_LABEL[session.status] ?? { text: session.status, cls: "badge--muted" };
  return el("a", { class: "row", href: `/session.html?id=${encodeURIComponent(session.id)}${useLive ? "&live=1" : `&mock=${mockOpts.state}&as=${mockOpts.viewer}`}` },
    el("span", { class: "row__main" },
      el("span", { class: "row__title" }, session.title),
      el("span", { class: "row__sub" }, formatDateTime(session.starts_at), " ・ ", `${session.duration_minutes}分`),
    ),
    el("span", { class: `badge ${label.cls}` }, label.text),
  );
}

async function renderGroups(me) {
  $("hero").className = "hero hero--work";
  mount($("hero"),
    el("p", { class: "hero__eyebrow" },
      el("span", {}, `${me.user.display_name} さん`),
      el("span", {}, "ログイン中"),
    ),
    el("h1", { class: "hero__title" }, "輪読会を選ぶ"),
    el("p", { class: "hero__lead" }, "参加しているグループと、その開催回が出ます。"),
  );

  mountTopbar({ crumbs: [{ label: "グループ" }], me: { display_name: me.user.display_name } });

  const { items: groups } = await client.groups();
  const blocks = [];

  if (groups.length === 0) {
    blocks.push(el("article", { class: "card" },
      el("div", { class: "card__head" }, el("span", { class: "card__title" }, "まだグループがありません")),
      el("p", { class: "card__note" }, "メンバーのDiscordユーザーIDを登録してグループを作ります。招待された人がログインすると所属が有効になります。"),
      el("p", { style: "margin-top:16px" }, el("a", { class: "btn btn--primary", href: "/setup.html" }, "輪読会をつくる")),
    ));
  }

  for (const group of groups) {
    let sessions = [];
    try {
      sessions = (await client.sessions(group.id)).items;
    } catch { /* 一覧が取れなくてもグループは出す */ }

    blocks.push(el("article", { class: "card" },
      el("div", { class: "card__head" },
        el("span", { class: "card__title" }, group.name),
        group.role === "owner" ? el("span", { class: "badge badge--muted" }, "管理者") : null,
        el("span", { class: "card__note" }, `${group.member_count}人`),
      ),
      sessions.length
        ? el("div", { class: "rows" }, sessions.map(renderSessionRow))
        : el("p", { class: "card__note" }, "開催回がまだありません。"),
      group.role === "owner"
        ? el("p", { style: "margin-top:16px" },
            el("a", { class: "btn btn--sm", href: `/setup.html?group=${encodeURIComponent(group.id)}` }, "開催回を登録する"))
        : null,
    ));
  }

  mount($("content"), el("p", { class: "section-title" }, "あなたの輪読会"), el("div", { class: "stack" }, blocks));
}

async function boot() {
  mountBackdrop();
  mountTopbar({ crumbs: [{ label: "グループ" }] });
  buildDemoBar();
  const health = await renderHealth($("health"), client);
  $("demo-mode").textContent = client.isMock ? "モック" : health?.dev_mode ? "デモモード" : "本番";

  const authError = params.get("auth_error");
  if (authError) {
    const box = $("notice");
    box.hidden = false;
    box.className = "notice notice--warn";
    box.textContent = AUTH_ERROR[authError] ?? "ログインできませんでした。";
  }

  try {
    const me = await client.me();
    await renderGroups(me);
  } catch (err) {
    if (err.status === 401) { renderLoggedOut(); return; }
    mount($("content"), el("article", { class: "card" },
      el("p", { class: "card__note" }, `読み込めません（${err.code ?? err.message}）`)));
  }
}

boot();
