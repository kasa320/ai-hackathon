// 画面の枠。3画面で共通して使う。
// - グローバルナビの中身（ログイン状態で変わる）
// - デモモードの帯（契約 第14節。全画面に出す）
// - 状態表示（読み込み中・通信断・競合）
// - フッターの接続確認

import { el, mount } from "../dom.js";
import { client, isMock, mockState, mockViewer, href, switchMock, MOCK_STATES } from "../client.js";

const VIEWERS = [
  ["mem_a", "Aさん（管理者）"],
  ["mem_b", "Bさん"],
  ["mem_c", "Cさん"],
  ["mem_d", "Dさん"],
];

/**
 * グローバルナビの右側を描く。
 * @param {object|null} me GET /api/me の結果。未ログインなら null
 * @param {string} current 現在の画面（"top" | "session" | "setup"）
 */
export function renderNav(me, current) {
  const node = document.getElementById("gnav-links");
  if (!node) return;

  const meetingLink = el(
    "a",
    { href: "/meeting.html", "aria-current": current === "meeting" ? "page" : null },
    "会議での使い方",
  );

  if (!me) {
    mount(
      node,
      meetingLink,
      el("a", { href: client.loginUrl(location.pathname + location.search) }, "ログイン"),
    );
    return;
  }

  mount(
    node,
    el("a", { href: href("/"), "aria-current": current === "top" ? "page" : null }, "輪読会"),
    el("a", { href: href("/setup.html"), "aria-current": current === "setup" ? "page" : null }, "開催回の登録"),
    meetingLink,
    el("a", {
      href: "#",
      onClick: async (e) => {
        e.preventDefault();
        await client.logout();
        location.href = href("/");
      },
    }, el("span", { class: "gnav__name" }, `${me.user.display_name}さん・`), "ログアウト"),
  );
}

/**
 * デモモードの帯。モック表示中か、サーバーが dev_mode を返したときだけ出す。
 * 本番の見た目にデモ用の操作を混ぜないよう、ここに閉じ込める。
 */
export function renderDevBar({ devMode = false } = {}) {
  const node = document.getElementById("devbar");
  if (!node) return;
  if (!isMock && !devMode) return mount(node);

  const label = isMock ? "デモモード：モックの状態を表示しています" : "デモモード：サーバーが開発設定で動いています";

  const stateSelect = el(
    "select",
    {
      class: "select",
      "aria-label": "モックの状態",
      onChange: (e) => switchMock({ mock: e.target.value, as: mockViewer }),
    },
    ...Object.entries(MOCK_STATES).map(([value, text]) =>
      el("option", { value, selected: value === mockState }, text)),
  );

  const viewerSelect = el(
    "select",
    {
      class: "select",
      "aria-label": "表示する人",
      onChange: (e) => switchMock({ mock: mockState, as: e.target.value }),
    },
    ...VIEWERS.map(([value, text]) => el("option", { value, selected: value === mockViewer }, text)),
  );

  node.className = "devbar";
  mount(
    node,
    el(
      "div",
      { class: "wrap devbar__in" },
      el("span", {}, label),
      isMock && stateSelect,
      isMock && viewerSelect,
    ),
  );
}

/** 画面上部の知らせ。空文字で消える。 */
export function setStatus(text, { warn = false } = {}) {
  const node = document.getElementById("status");
  if (!node) return;
  if (!text) return mount(node);
  mount(node, el("p", { class: warn ? "note note--warn" : "note", style: "margin-bottom:24px" }, text));
}

/** フッターの接続確認。失敗しても画面は動かす。 */
export async function renderHealth() {
  const node = document.getElementById("foot-health");
  if (!node) return { dev_mode: false };
  try {
    const health = await client.health();
    mount(node, `サーバー：正常${health.dev_mode ? "（開発設定）" : ""}`);
    return health;
  } catch {
    mount(node, "サーバー：応答がありません");
    return { dev_mode: false };
  }
}
