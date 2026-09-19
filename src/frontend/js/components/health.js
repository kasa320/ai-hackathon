// サーバーの状態表示。用途に依存しない。
// health は dev_mode を返すので、開発モードなら全画面に「デモモード」を出す（契約 第14節）。

import { el } from "../dom.js";

export async function renderHealth(node, client) {
  try {
    const res = await client.health();
    node.textContent = `${res.status}（${new Date(res.now).toLocaleTimeString("ja-JP")}）`;
    return res;
  } catch (err) {
    node.textContent = `接続できません（${err.message}）`;
    return null;
  }
}

/** dev_mode のときだけ出す帯。本番では表示しない。 */
export function devModeBanner(on) {
  if (!on) return null;
  return el("span", { class: "demobar__tag" }, "デモモード");
}
