// 画面をまたいで使う部品。用途固有の語（節・発表など）はここに置かない。
//
// 表示の約束（css/styles.css の先頭も参照）：
// - 色は「あなたの行動」と「進行表」にだけ使う。状態は記号と罫で示す
// - 賛成数や必要人数はブラウザーで計算せず、受け取った値をそのまま出す
// - 送信直後は必ず「受け付けました」を出し、確定と区別する

import { el, mount } from "./dom.js";
import { api, ApiError, NetworkError } from "./api.js";

/** 上部の帯。現在のページに aria-current を付ける。 */
export function renderTopbar(node, { me, current }) {
  const link = (href, label, key) =>
    el("a", { href, "aria-current": current === key ? "page" : null }, label);

  mount(
    node,
    el("a", { class: "brand", href: "/" }, "Marunage", el("small", {}, "幹事エージェント")),
    el(
      "nav",
      { class: "nav", "aria-label": "主なページ" },
      link("/", "サークル", "home"),
      me ? link("/setup.html", "サークルを作る", "setup") : null,
    ),
    me ? accountMenu(me) : null,
  );
}

/** 右上のアカウント。押すとメニューが開き、ログアウトできる。 */
function accountMenu(me) {
  const logout = el(
    "button",
    {
      type: "button",
      role: "menuitem",
      onClick: async () => {
        logout.disabled = true;
        logout.textContent = "ログアウトしています…";
        try {
          await api.logout();
        } catch (err) {
          // すでにログアウト済み（401）なら、そのまま最初の画面へ戻す
          if (!(err instanceof ApiError && err.status === 401)) {
            logout.disabled = false;
            logout.textContent = "ログアウト";
            alert("ログアウトできませんでした。通信を確認してもう一度お試しください。");
            return;
          }
        }
        location.href = "/";
      },
    },
    "ログアウト",
  );
  const menu = el(
    "details",
    { class: "account" },
    el(
      "summary",
      { class: "viewer", "aria-label": `${me.user.display_name}のアカウント` },
      el("i", { "aria-hidden": "true" }, initial(me.user.display_name)),
      el("span", {}, me.user.display_name),
    ),
    el(
      "div",
      { class: "account__menu", role: "menu" },
      el("p", {}, "Discordでログイン中", el("b", {}, me.user.display_name)),
      logout,
    ),
  );
  // メニューの外を押す・Esc で閉じる
  document.addEventListener("click", (e) => {
    if (menu.open && !menu.contains(e.target)) menu.open = false;
  });
  menu.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && menu.open) {
      menu.open = false;
      menu.querySelector("summary").focus();
    }
  });
  return menu;
}

/**
 * 開催日時の表示。日時がまだ決まっていない回では、仮の候補を確定した日時のように見せない。
 * 期間だけが決まっている状態は「調整中」と書く。
 */
export function whenLabel(session, format) {
  if (session.schedule_status === "proposed") {
    return session.period_start && session.period_end
      ? `日時調整中（${session.period_start.slice(5).replace("-", "/")}〜${session.period_end.slice(5).replace("-", "/")}）`
      : "日時調整中";
  }
  return format(session.starts_at);
}

/**
 * 日付入力の上限。上限がないとブラウザーは年を6桁まで受け付け、4桁打っても月の欄へ進まない。
 * 上限を付けると年が4桁になり、月・日と同じように自動で次の欄へ進む。
 */
export const DATE_MAX = "9999-12-31";

export function initial(name) {
  return (name ?? "?").trim().slice(0, 1) || "?";
}

/**
 * 進行表の帯。持ち時間を幅の比率で描き、変更前があれば薄い帯として下に並べる。
 * 差し替えずに必ず比較で見せる。items は [{ label, who, minutes, tone }]。
 */
const animatedPlanVersions = new Set();

export function renderPlanBar(items, { previous = null, total = null, enter = true, animationKey = null } = {}) {
  if (!items || items.length === 0) return null;

  // ポーリングで同じ案を描き直しても、帯を毎回伸ばさない。版が変われば新しい key となり一度だけ動く。
  const shouldEnter = enter && (!animationKey || !animatedPlanVersions.has(animationKey));
  if (shouldEnter && animationKey) animatedPlanVersions.add(animationKey);

  const sum = items.reduce((n, item) => n + item.minutes, 0);
  const marks = items.reduce((acc, item) => acc.concat(acc[acc.length - 1] + item.minutes), [0]);
  const span = total ?? sum;

  const bar = el(
    "div",
    { class: "plan__bar", role: "img", "aria-label": `進行表。${items.map((i) => `${i.label} ${i.who}`).join("、")}。` },
    items.map((item) =>
      el(
        "span",
        { style: `flex:${item.minutes}`, "data-tone": item.tone || null },
        el("b", {}, item.label),
        item.who,
      ),
    ),
  );

  const scale = el(
    "p",
    { class: "plan__scale num" },
    marks.map((m) => el("span", {}, m === span ? `${m}分` : String(m))),
  );

  const prev = previous && previous.length
    ? el(
        "div",
        { class: "plan__prev" },
        el("p", {}, "変更前"),
        el(
          "div",
          { class: "plan__bar plan__bar--prev", role: "img", "aria-label": `変更前の進行表。${previous.map((i) => `${i.label} ${i.who}`).join("、")}。` },
          previous.map((item) => el("span", { style: `flex:${item.minutes}` }, `${item.label} ${item.minutes}分`)),
        ),
      )
    : null;

  return el("div", { class: `plan${shouldEnter ? " plan--enter" : ""}` }, bar, scale, prev);
}

/** 進行表の色の意味。色は行動と進行表にしか使わないので、凡例もここだけ。 */
export function planLegend() {
  return el(
    "div",
    { class: "legend" },
    el("span", {}, el("i", { "data-tone": "solid" }), "あなたが担当"),
    el("span", {}, el("i", { "data-tone": "mid" }), "ほかの人が担当"),
    el("span", {}, el("i", {}), "担当者なし"),
  );
}

/** 同意や引き受けの進み具合。数は受け取った値をそのまま出す。 */
export function tally({ done, total, note }) {
  return el(
    "span",
    { class: "tally" },
    Array.from({ length: total }, (_, i) => el("i", { "data-on": i < done ? "" : null })),
    el("span", { class: "num" }, note),
  );
}

/** 画面上部の知らせ。tone は "ok" か "warn"。 */
export function flash(node, { title, detail = null, tone = "ok" }) {
  mount(
    node,
    el(
      "div",
      { class: "flash", "data-tone": tone === "warn" ? "warn" : null, role: "status" },
      el("span", {}, el("strong", {}, title), detail ? el("small", {}, detail) : null),
      el("button", { type: "button", "aria-label": "閉じる", onClick: () => (node.hidden = true) }, "×"),
    ),
  );
  node.hidden = false;
}

export function clearFlash(node) {
  node.hidden = true;
  mount(node);
}

/**
 * 更新が失敗したときの共通処理。409 は「最新を読み直して再確認」で、入力は消さない。
 * 再読み込みが要るときだけ refresh を呼ぶ。
 */
export async function reportMutationError(err, { node, refresh, loginUrl }) {
  if (err instanceof NetworkError) {
    flash(node, { title: "サーバーに接続できません", detail: "通信が戻ってから、もう一度送ってください。", tone: "warn" });
    return;
  }
  if (!(err instanceof ApiError)) {
    flash(node, { title: "処理できませんでした", detail: String(err.message), tone: "warn" });
    return;
  }
  if (err.status === 401) {
    location.href = loginUrl;
    return;
  }
  if (err.isConflict) {
    flash(node, { title: err.message, detail: "最新の内容を読み込みました。もう一度確認してください。", tone: "warn" });
    await refresh?.();
    return;
  }
  if (err.status === 422) {
    const fields = err.fieldErrors.map((f) => f.message).join(" ");
    flash(node, { title: "入力を確認してください", detail: fields || err.message, tone: "warn" });
    return;
  }
  flash(node, { title: err.message, detail: err.requestId ? `記録番号 ${err.requestId}` : null, tone: "warn" });
}

/** 送信を待っている間の表示。確定ではないことを必ず添える。 */
export function receipt(text) {
  return el(
    "div",
    { class: "receipt", role: "status" },
    el("span", { class: "spinner", "aria-hidden": "true" }),
    el("span", {}, el("strong", {}, "受け付けました"), el("small", {}, text)),
  );
}

export function placeholder(title, body) {
  return el("div", { class: "placeholder" }, el("strong", {}, title), body);
}

/** 用途の札。輪読以外の用途が増えてもここだけで済むようにしておく。 */
export function pluginTag(playbookId, name) {
  const icon = playbookId === "reading"
    ? '<rect x="2" y="2.5" width="3" height="11" rx="1"/><rect x="6.5" y="2.5" width="3" height="11" rx="1"/><path d="M11.4 3.2l2.6.7-2.4 9.3-2.6-.7z"/>'
    : '<circle cx="8" cy="8" r="4.2"/><circle cx="8" cy="1.8" r="1.2"/><circle cx="14.2" cy="8" r="1.2"/><circle cx="8" cy="14.2" r="1.2"/><circle cx="1.8" cy="8" r="1.2"/>';
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  svg.innerHTML = icon;
  return el("span", { class: "plugin" }, svg, name ?? playbookId);
}

/**
 * ダイアログ。送信中は二重操作を防ぎ、失敗時は入力を残す。
 */
export function createDialog({ title, body, submitLabel, onSubmit, extra = null }) {
  const opener = document.activeElement;
  const token = globalThis.crypto?.randomUUID?.() ?? Date.now();
  const titleId = `dialog-title-${token}`;
  const errorId = `dialog-error-${token}`;
  let pending = false;
  let restore = [];
  const errorBox = el("p", { class: "field__error", id: errorId, role: "alert", "aria-live": "assertive", hidden: true });
  const receiptBox = el("div", {});

  const dialog = el(
    "dialog",
    { "aria-labelledby": titleId, onClose: () => cleanup() },
    el(
      "form",
      {
        method: "dialog",
        onSubmit: async (event) => {
          event.preventDefault();
          if (pending) return;
          setPending(true);
          try {
            await onSubmit({ showError, showReceipt, close });
          } catch (err) {
            showReceipt(null);
            showError(err.message ?? "送信できませんでした。もう一度お試しください。");
          } finally {
            setPending(false);
          }
        },
      },
      el(
        "div",
        { class: "dialog__head" },
        el("h2", { id: titleId }, title),
        el("button", { type: "button", "aria-label": "閉じる", onClick: () => { if (!pending) close(); } }, "×"),
      ),
      el("div", { class: "dialog__body" }, body, errorBox, receiptBox),
      el(
        "div",
        { class: "dialog__foot" },
        extra,
        el("button", { class: "btn", type: "submit" }, submitLabel),
      ),
    ),
  );

  function showError(message) {
    errorBox.textContent = message ?? "";
    errorBox.hidden = !message;
  }

  function showReceipt(text) {
    mount(receiptBox, text ? receipt(text) : null);
  }

  function close() {
    if (dialog.open) dialog.close();
    else cleanup();
  }

  function cleanup() {
    if (!dialog.isConnected) return;
    dialog.remove();
    if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
  }

  function setPending(value, label = "送信しています…") {
    if (pending === value) return;
    pending = value;
    dialog.setAttribute("aria-busy", String(value));
    const submit = dialog.querySelector('[type="submit"]');
    if (value) {
      restore = [...dialog.querySelectorAll("input,select,textarea,button")].map((node) => [node, node.disabled]);
      for (const [node] of restore) node.disabled = true;
      submit.textContent = label;
    } else {
      for (const [node, disabled] of restore) node.disabled = disabled;
      restore = [];
      submit.textContent = submitLabel;
    }
  }

  // 送信中は Esc での取り消しも止める。閉じたあとの後始末は cleanup に一本化する。
  dialog.addEventListener("cancel", (event) => { if (pending) event.preventDefault(); });

  document.body.append(dialog);
  dialog.showModal();
  (dialog.querySelector("input, select, textarea") ?? dialog.querySelector('button[type="submit"]'))?.focus();
  return { dialog, close, showError, showReceipt, setPending, errorId };
}

/**
 * デモ用の帯。DEV_MODE のときだけ出す。本番では health.dev_mode が false なので描画しない。
 * 時計を進める・人物を切り替える・障害を入れるのはここからだけ。
 */
export async function renderDevBar(node, client, { onChange } = {}) {
  let health;
  try {
    health = await client.health();
  } catch {
    return null;
  }
  if (!health?.dev_mode) {
    node.hidden = true;
    return health;
  }

  const people = [
    ["100000000000000001", "A（管理者）"],
    ["100000000000000002", "B"],
    ["100000000000000003", "C"],
    ["100000000000000004", "D"],
  ];

  const run = async (fn) => {
    try {
      await fn();
      await onChange?.();
    } catch (err) {
      console.error(err);
    }
  };

  const who = el(
    "select",
    {
      "aria-label": "人物を切り替える",
      onChange: (event) => run(() => client.dev.login(event.target.value)),
    },
    el("option", { value: "" }, "人物を切り替える"),
    people.map(([id, name]) => el("option", { value: id }, name)),
  );

  const scenario = el(
    "select",
    { "aria-label": "初期データ" },
    el("option", { value: "replan_demo" }, "replan_demo"),
    el("option", { value: "initial_demo" }, "initial_demo"),
  );

  mount(
    node,
    el("b", {}, "デモモード"),
    el("span", {}, "時計と人物を操作できます。本番では出ません。"),
    who,
    el("button", { type: "button", onClick: () => run(() => client.dev.advanceClock(3600)) }, "1時間進める"),
    el("button", { type: "button", onClick: () => run(() => client.dev.advanceClock(43200)) }, "12時間進める"),
    el("span", { class: "devbar__end" }, "初期データ"),
    scenario,
    el(
      "button",
      {
        type: "button",
        onClick: () => {
          if (confirm("DBを初期化して初期データを入れ直します。よろしいですか？")) {
            run(async () => {
              await client.dev.seed(scenario.value);
              location.href = "/";
            });
          }
        },
      },
      "投入",
    ),
  );
  node.hidden = false;
  return health;
}
