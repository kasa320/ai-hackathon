// 開催回の詳細を一定間隔で取り直す。契約 第3部「ポーリング」。
//
// - 表示中だけ3秒間隔。前回が終わるまで次を送らない
// - タブが非表示なら一時停止、画面を離れたら停止
// - 401 / 403 / 404 では停止する
// - 通信エラーは 3 → 6 → 12 → 最大30秒に広げ、成功したら3秒に戻す

const BASE_MS = 3000;
const MAX_MS = 30000;
const STOP_ON = new Set([401, 403, 404]);

export function createPoller({ fetcher, onData, onError }) {
  let timer = null;
  let inFlight = false;
  let stopped = false;
  let intervalMs = BASE_MS;

  async function tick() {
    if (stopped || inFlight || document.hidden) return schedule();
    inFlight = true;
    try {
      const data = await fetcher();
      intervalMs = BASE_MS;
      onData(data);
    } catch (err) {
      if (STOP_ON.has(err.status)) {
        stopped = true;
        onError?.(err, { fatal: true });
        return;
      }
      intervalMs = Math.min(intervalMs * 2, MAX_MS);
      onError?.(err, { fatal: false });
    } finally {
      inFlight = false;
    }
    schedule();
  }

  function schedule() {
    clearTimeout(timer);
    if (!stopped) timer = setTimeout(tick, intervalMs);
  }

  function onVisibility() {
    if (!document.hidden && !stopped) {
      clearTimeout(timer);
      tick();
    }
  }

  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("pagehide", () => stop());

  return {
    start() { tick(); },
    /** 更新の成功直後に呼ぶ。一度すぐ取り直す。 */
    refreshNow() { clearTimeout(timer); intervalMs = BASE_MS; tick(); },
    stop() {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    },
  };
}
