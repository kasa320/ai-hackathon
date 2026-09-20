// 複数の画面で使う小さな操作をまとめる。
// 画面の見た目は HTML と CSS が持ち、ここでは表示の切り替えだけを扱う。

/** ドロワー（回答の入力）の開閉。data-open-drawer / data-close-drawer で指定する。 */
export function initDrawer(id = "answer-drawer") {
  const drawer = document.getElementById(id);
  if (!drawer) return;

  const open = () => {
    drawer.setAttribute("open", "");
    document.body.style.overflow = "hidden";
    drawer.querySelector("input, button, select, textarea")?.focus();
  };

  const close = () => {
    drawer.removeAttribute("open");
    document.body.style.overflow = "";
  };

  document.querySelectorAll("[data-open-drawer]").forEach((el) => {
    el.addEventListener("click", open);
  });
  drawer.querySelectorAll("[data-close-drawer]").forEach((el) => {
    el.addEventListener("click", close);
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && drawer.hasAttribute("open")) close();
  });
}

/** デモ用の「時計を進める」ボタン。回答期限の到達をデモで再現するために使う。 */
export function initDemoClock() {
  const btn = document.getElementById("demo-advance");
  if (!btn) return;

  const label = btn.textContent;
  let hours = 0;

  btn.addEventListener("click", () => {
    hours += 24;
    btn.textContent = `${label}（+${hours}h）`;
    // TODO: バックエンドの clock.Offset を進める API ができたら、ここで呼んで画面を再取得する。
  });
}

/**
 * 「調整中」と「同意が揃った」の表示を切り替える。
 * data-state-adjusting / data-state-confirmed を付けた要素を出し分ける。
 * 本番では API から受け取った状態で決める。ここはデモ用の切り替え。
 */
export function initStateToggle() {
  const btn = document.getElementById("demo-state");
  if (!btn) return;

  const apply = (state) => {
    document.body.dataset.state = state;
    document.querySelectorAll("[data-state-adjusting]").forEach((el) => {
      el.hidden = state !== "adjusting";
    });
    document.querySelectorAll("[data-state-confirmed]").forEach((el) => {
      el.hidden = state !== "confirmed";
    });

    const todo = document.getElementById("hero-todo");
    const done = document.getElementById("hero-done");
    if (todo) todo.hidden = state !== "adjusting";
    if (done) done.hidden = state !== "confirmed";

    btn.textContent = state === "confirmed" ? "調整中の状態に戻す" : "同意が揃った状態にする";
  };

  btn.addEventListener("click", () => {
    apply(document.body.dataset.state === "adjusting" ? "confirmed" : "adjusting");
  });
}
