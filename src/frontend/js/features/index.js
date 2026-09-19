// 用途別画面の登録。playbook_id とフロントのモジュールを対応させる。
// 契約 第3部：用途固有の部分は features/<playbook_id>/ に置き、第1部の描画に輪読用語を混ぜない。

import * as reading from "./reading/index.js";

const REGISTRY = { reading };

/** 未対応の playbook_id でも画面が落ちないようにする。 */
export function featureFor(playbookId) {
  return REGISTRY[playbookId] ?? null;
}

export function isSupported(playbookId) {
  return playbookId in REGISTRY;
}
