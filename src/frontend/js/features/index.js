// 用途別画面の登録。playbook_id とフロントのモジュールを対応させる。
// 用途固有の部分は features/<playbook_id>/ に置き、汎用側の描画に輪読用語を混ぜない。

import * as reading from "./reading/index.js";

const REGISTRY = { reading };

/** 未対応の playbook_id でも画面が落ちないように null を返す。 */
export function featureFor(playbookId) {
  return REGISTRY[playbookId] ?? null;
}
