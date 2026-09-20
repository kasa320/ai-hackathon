// 「会議の日程調整へ広げる」ページ。中身は静的で、APIを呼ばない。
// ここで読み込むのは共通の枠（ナビ・デモ帯・接続確認）だけ。

import { client } from "./client.js";
import { renderNav, renderDevBar, renderHealth } from "./components/chrome.js";

boot();

async function boot() {
  const health = await renderHealth();
  renderDevBar({ devMode: health.dev_mode });

  let me = null;
  try {
    me = await client.me();
  } catch {
    // 未ログインでも読めるページ。ナビだけ切り替える
  }
  renderNav(me, "meeting");
}
