// 小さな描画ヘルパー。テンプレート記法を持ち込まず、これだけで組み立てる。

/** el("div", {class: "card"}, "文字", el("b", {}, "太字")) */
export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key === "style") node.setAttribute("style", value);
    else if (key.startsWith("on")) node.addEventListener(key.slice(2).toLowerCase(), value);
    else if (value === true) node.setAttribute(key, "");
    else node.setAttribute(key, value);
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function clear(node) {
  node.replaceChildren();
  return node;
}

export function mount(node, ...children) {
  clear(node);
  node.append(...children.flat().filter(Boolean));
  return node;
}

/** RFC 3339 の UTC を利用者のタイムゾーンで表示する。 */
export function formatDateTime(iso) {
  if (!iso) return "—";
  return new Date(iso).toLocaleString("ja-JP", {
    month: "numeric", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit",
  });
}

export function formatTime(iso) {
  if (!iso) return "—";
  return new Date(iso).toLocaleTimeString("ja-JP", { hour: "2-digit", minute: "2-digit" });
}

/** 期限までの残り。サーバー時刻を基準にする（デモで時計を進めるため）。 */
export function remaining(dueAt, serverNow) {
  const ms = new Date(dueAt) - new Date(serverNow);
  if (ms <= 0) return "期限切れ";
  const h = Math.floor(ms / 3600000);
  const m = Math.floor((ms % 3600000) / 60000);
  return h > 0 ? `${h}時間${m}分` : `${m}分`;
}

export function memberName(members, id) {
  return members.find((m) => m.id === id)?.display_name ?? "不明";
}

/** メンバーごとの色を並び順で固定する。画面をまたいで同じ人が同じ色になる。 */
export function memberTone(members, id) {
  const i = members.findIndex((m) => m.id === id);
  return ["a", "b", "c", "d"][i % 4] ?? "a";
}
