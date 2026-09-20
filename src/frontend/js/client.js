// 本物の API とモックを、同じ呼び出し方で差し替える。
//
// ?mock=<状態>&as=<member_id> が付いていればモックを使う（契約 第3部「モックの状態」）。
// 付いていなければ本物の API。画面側のコードは違いを意識しない。

import { api } from "./api.js";
import { createMockApi, MOCK_STATES } from "./mock.js";

const params = new URLSearchParams(location.search);

const rawState = params.get("mock");

export const mockState = rawState && rawState in MOCK_STATES ? rawState : null;
export const mockViewer = params.get("as") || "mem_a";
export const isMock = mockState !== null;

export const client = isMock
  ? createMockApi({ state: mockState, viewer: mockViewer })
  : api;

export { MOCK_STATES };

/** モックの指定を保ったまま画面を移る。指定が無ければ何も足さない。 */
export function href(path, extra = {}) {
  const url = new URL(path, location.href);
  if (isMock) {
    url.searchParams.set("mock", mockState);
    url.searchParams.set("as", mockViewer);
  }
  for (const [k, v] of Object.entries(extra)) url.searchParams.set(k, v);
  return url.pathname + url.search;
}

/** 同じ画面のまま、モックの指定だけ差し替える。 */
export function switchMock(next) {
  const url = new URL(location.href);
  for (const [k, v] of Object.entries(next)) url.searchParams.set(k, v);
  location.href = url.toString();
}
