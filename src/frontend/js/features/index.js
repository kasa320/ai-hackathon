// 用途別画面の登録。ID はバックエンドの Playbook ID（GET /api/playbooks の id）と合わせる。
//
// いまは輪読だけ。用途を増やすときはここに足し、画面側は playbook_id で引く。
// 用途を選ばせる画面はその時点で設計する（architecture.md「後から用途を追加する手順」）。

import { reading } from "./reading/index.js";

const FEATURES = { [reading.id]: reading };

export function featureFor(playbookId) {
  return FEATURES[playbookId] ?? null;
}

export function knownPlaybookIds() {
  return Object.keys(FEATURES);
}
