// 背景の書棚を Canvas で描く。画像は読み込まない。
//
// 本を扱うアプリなので、背景も本で作る。棚板と背表紙を手続きで並べ、
// 上からの電球の光を重ねる。種を固定しているので、開き直しても同じ棚になる。

/** 同じ見た目を保つための擬似乱数（mulberry32）。 */
function makeRandom(seed) {
  let t = seed >>> 0;
  return () => {
    t = (t + 0x6d2b79f5) >>> 0;
    let x = Math.imul(t ^ (t >>> 15), 1 | t);
    x = (x + Math.imul(x ^ (x >>> 7), 61 | x)) ^ x;
    return ((x ^ (x >>> 14)) >>> 0) / 4294967296;
  };
}

/** 古い装丁の色。彩度を落とした暖色を中心にする。 */
const SPINES = [
  "#7d2f23", "#8d4a1e", "#a8762a", "#5c6b33", "#2f5548", "#2b3f63",
  "#5a3358", "#6b2f3c", "#3c3730", "#8a6b3a", "#95402e", "#41543a",
  "#6d5633", "#243f4a", "#7b3b2a", "#4a4238",
];

const PLANK = "#241811";
const BACKWALL = "#171009";

function drawBook(ctx, rand, x, y, w, h) {
  const color = SPINES[(rand() * SPINES.length) | 0];
  ctx.fillStyle = color;
  ctx.fillRect(x, y, w, h);

  // 背の丸み：左右にわずかな陰影を置く
  const shade = ctx.createLinearGradient(x, 0, x + w, 0);
  shade.addColorStop(0, "rgba(0,0,0,.42)");
  shade.addColorStop(0.32, "rgba(255,255,255,.07)");
  shade.addColorStop(1, "rgba(0,0,0,.5)");
  ctx.fillStyle = shade;
  ctx.fillRect(x, y, w, h);

  // 題箋と箔押しの線。幅のある本にだけ入れる
  if (w >= 13 && rand() > 0.35) {
    const bandH = Math.min(26, h * 0.2);
    const bandY = y + h * (0.16 + rand() * 0.16);
    ctx.fillStyle = "rgba(0,0,0,.30)";
    ctx.fillRect(x + 2, bandY, w - 4, bandH);
    ctx.fillStyle = "rgba(226,184,109,.5)";
    ctx.fillRect(x + 3, bandY + 3, w - 6, 1.2);
    ctx.fillRect(x + 3, bandY + bandH - 4, w - 6, 1.2);
  }
  if (rand() > 0.72) {
    ctx.fillStyle = "rgba(226,184,109,.34)";
    ctx.fillRect(x + 2, y + h * 0.62, w - 4, 1);
  }
}

/** 棚の端に横倒しで積んだ本。ぎっしり感が出る */
function drawStack(ctx, rand, x, baseY, maxW, shelfH) {
  let y = baseY;
  const count = 2 + ((rand() * 4) | 0);
  for (let i = 0; i < count; i++) {
    const h = 7 + rand() * 7;
    const w = maxW * (0.6 + rand() * 0.4);
    if (y - h < baseY - shelfH) break;
    y -= h;
    ctx.fillStyle = SPINES[(rand() * SPINES.length) | 0];
    ctx.fillRect(x, y, w, h);
    ctx.fillStyle = "rgba(0,0,0,.34)";
    ctx.fillRect(x, y + h - 1.4, w, 1.4);
  }
  return count;
}

function paint(canvas) {
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  const w = canvas.clientWidth;
  const h = canvas.clientHeight;
  if (w === 0 || h === 0) return;

  canvas.width = Math.round(w * dpr);
  canvas.height = Math.round(h * dpr);

  const ctx = canvas.getContext("2d");
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

  const rand = makeRandom(20260919);

  ctx.fillStyle = BACKWALL;
  ctx.fillRect(0, 0, w, h);

  const shelfH = 168;
  const plankH = 13;
  const rows = Math.ceil(h / (shelfH + plankH)) + 1;

  for (let r = 0; r < rows; r++) {
    const top = r * (shelfH + plankH);
    const baseY = top + shelfH;

    // 棚の奥。手前より暗くして奥行きを作る
    ctx.fillStyle = "rgba(0,0,0,.34)";
    ctx.fillRect(0, top, w, shelfH);

    let x = 2 + rand() * 10;
    while (x < w - 6) {
      // ときどき積み本、ときどき隙間
      const roll = rand();
      if (roll > 0.94) {
        const stackW = 34 + rand() * 30;
        drawStack(ctx, rand, x, baseY, stackW, shelfH * 0.7);
        x += stackW + 3;
        continue;
      }
      if (roll > 0.9) { x += 6 + rand() * 14; continue; }

      const bw = 8 + rand() * 20;
      const bh = shelfH * (0.58 + rand() * 0.37);
      drawBook(ctx, rand, x, baseY - bh, bw, bh);
      x += bw + (rand() > 0.85 ? 1.5 : 0.6);
    }

    // 棚板
    ctx.fillStyle = PLANK;
    ctx.fillRect(0, baseY, w, plankH);
    ctx.fillStyle = "rgba(226,184,109,.10)";
    ctx.fillRect(0, baseY, w, 1.2);
    ctx.fillStyle = "rgba(0,0,0,.4)";
    ctx.fillRect(0, baseY + plankH - 2, w, 2);
  }

  // 電球の光。上から落ちて、下にいくほど暗くなる
  const lamp = ctx.createRadialGradient(w * 0.5, -h * 0.12, 0, w * 0.5, -h * 0.12, h * 1.15);
  lamp.addColorStop(0, "rgba(255, 196, 104, .40)");
  lamp.addColorStop(0.45, "rgba(255, 170, 80, .12)");
  lamp.addColorStop(1, "rgba(0, 0, 0, 0)");
  ctx.globalCompositeOperation = "screen";
  ctx.fillStyle = lamp;
  ctx.fillRect(0, 0, w, h);

  ctx.globalCompositeOperation = "multiply";
  const fade = ctx.createLinearGradient(0, 0, 0, h);
  fade.addColorStop(0, "rgba(255,255,255,1)");
  fade.addColorStop(0.55, "rgba(120,96,72,1)");
  fade.addColorStop(1, "rgba(58,44,34,1)");
  ctx.fillStyle = fade;
  ctx.fillRect(0, 0, w, h);

  ctx.globalCompositeOperation = "source-over";
}

/** 背景を描き、画面幅が変わったら描き直す。 */
export function mountBookshelf(canvas) {
  if (!canvas) return;
  paint(canvas);

  let lastW = window.innerWidth;
  let timer = null;
  window.addEventListener("resize", () => {
    // 縦だけの変化（スマホのアドレスバー）では描き直さない
    if (Math.abs(window.innerWidth - lastW) < 40) return;
    lastW = window.innerWidth;
    clearTimeout(timer);
    timer = setTimeout(() => paint(canvas), 180);
  });
}
