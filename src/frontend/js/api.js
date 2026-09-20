// バックエンド API の呼び出しをまとめる。画面側の JS はここを通して API を使う。
// 契約：.agent/decisions/api.md（mvp-2）第1部。
//
// ここが守ること：
// - 認証済みの POST / PUT に X-CSRF-Token と Idempotency-Key を付ける
// - エラーは必ず ApiError にして、画面側は code で分岐する（message で分岐しない）
// - 本人 ID や権限を本文に入れない。サーバーがセッションから解決する

const BASE = "/api";

/** 契約 第12節のエラー。画面側は error.code を見る。 */
export class ApiError extends Error {
  constructor(status, payload) {
    const err = payload && payload.error ? payload.error : {};
    super(err.message || `API error: ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = err.code || "internal_error";
    this.details = err.details || {};
    this.requestId = err.request_id || null;
  }

  /** 最新状態を取り直して利用者に再確認を求めるべきエラーか（第12節 409 の行）。 */
  get isConflict() {
    return this.status === 409;
  }

  /** 入力欄に出すべき検証エラーの一覧。validation_failed 以外では空。 */
  get fieldErrors() {
    return Array.isArray(this.details.fields) ? this.details.fields : [];
  }
}

/** 通信そのものが失敗した場合。再送は同じ Idempotency-Key で行う。 */
export class NetworkError extends Error {
  constructor(cause) {
    super("サーバーに接続できません");
    this.name = "NetworkError";
    this.code = "network_error";
    this.cause = cause;
  }
}

let csrfToken = null;

export function setCsrfToken(token) {
  csrfToken = token;
}

export function clearSession() {
  csrfToken = null;
}

/** 1回の送信操作に1つ。通信失敗の再送では同じ値を使い回す。 */
export function newIdempotencyKey() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  return `key-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

async function request(method, path, { body, idempotencyKey, formData } = {}) {
  const headers = {};
  const isWrite = method === "POST" || method === "PUT";

  if (isWrite && csrfToken) headers["X-CSRF-Token"] = csrfToken;
  if (isWrite && idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  let res;
  try {
    res = await fetch(BASE + path, {
      method,
      headers,
      body: formData ?? (body !== undefined ? JSON.stringify(body) : undefined),
      credentials: "same-origin",
    });
  } catch (cause) {
    throw new NetworkError(cause);
  }

  if (res.status === 204) return null;

  const isJson = res.headers.get("Content-Type")?.includes("application/json");
  const payload = isJson ? await res.json().catch(() => null) : null;

  if (!res.ok) throw new ApiError(res.status, payload);
  return payload;
}

/**
 * 書き込み系。1回の操作につきキーを1つ作り、通信エラーのときだけ同じキーで再送する。
 * 業務エラー（409・422）は再送しない。利用者に再確認を求める。
 */
async function write(method, path, body, { retries = 1 } = {}) {
  const key = newIdempotencyKey();
  for (let attempt = 0; ; attempt++) {
    try {
      return await request(method, path, { body, idempotencyKey: key });
    } catch (err) {
      const retriable = err instanceof NetworkError || err.status === 503;
      if (!retriable || attempt >= retries) throw err;
      await new Promise((r) => setTimeout(r, 1000 * (attempt + 1)));
    }
  }
}

export const api = {
  // ---- 公開 ----
  health: () => request("GET", "/health"),
  playbooks: () => request("GET", "/playbooks"),

  /** ログイン画面へ遷移する。return_to は同一サイト内の相対パスのみ。 */
  loginUrl(returnTo) {
    const ok = typeof returnTo === "string" && returnTo.startsWith("/") && !returnTo.startsWith("//");
    return ok ? `${BASE}/auth/discord?return_to=${encodeURIComponent(returnTo)}` : `${BASE}/auth/discord`;
  },

  // ---- 認証 ----
  /** 200 なら CSRF トークンを保持する。401 は未ログイン。 */
  async me() {
    const data = await request("GET", "/me");
    setCsrfToken(data.csrf_token);
    return data;
  },

  async logout() {
    await request("POST", "/auth/logout");
    clearSession();
  },

  // ---- グループ ----
  groups: () => request("GET", "/groups"),
  group: (groupId) => request("GET", `/groups/${encodeURIComponent(groupId)}`),
  createGroup: (payload) => write("POST", "/groups", payload),

  // ---- 開催回 ----
  sessions: (groupId) => request("GET", `/groups/${encodeURIComponent(groupId)}/sessions`),
  createSession: (groupId, payload) => write("POST", `/groups/${encodeURIComponent(groupId)}/sessions`, payload),
  session: (sessionId) => request("GET", `/sessions/${encodeURIComponent(sessionId)}`),
  activity: (sessionId) => request("GET", `/sessions/${encodeURIComponent(sessionId)}/activity`),

  /** 参加条件の全置換。preparation.data は用途固有（輪読は第2部 R1）。 */
  updatePreparation: (sessionId, expectedRevision, preparation) =>
    write("PUT", `/sessions/${encodeURIComponent(sessionId)}/preparations/me`, {
      expected_revision: expectedRevision,
      preparation,
    }),

  /** scope は "assignment"（担当の辞退）か "attendance"（欠席）。理由は送らない。 */
  withdraw: (sessionId, expectedRevision, scope) =>
    write("POST", `/sessions/${encodeURIComponent(sessionId)}/withdrawals`, {
      expected_revision: expectedRevision,
      scope,
    }),

  /** タスクへの回答。本文の形は task.kind で決まる（第8節）。 */
  respondToTask: (taskId, payload) =>
    write("POST", `/tasks/${encodeURIComponent(taskId)}/responses`, payload),

  /** 管理者の代案。案件が needs_owner のときだけ。 */
  submitProposal: (sessionId, expectedRevision, data) =>
    write("POST", `/sessions/${encodeURIComponent(sessionId)}/proposals`, {
      expected_revision: expectedRevision,
      data,
    }),

  // ---- 輪読：目次の取得（第2部 R7） ----
  startTocLookup: (groupId, isbn) =>
    write("POST", `/groups/${encodeURIComponent(groupId)}/reading/toc-lookups`, { isbn }),

  tocLookup: (groupId, lookupId) =>
    request("GET", `/groups/${encodeURIComponent(groupId)}/reading/toc-lookups/${encodeURIComponent(lookupId)}`),

  submitTocImages(groupId, lookupId, files) {
    const form = new FormData();
    for (const file of files) form.append("images", file);
    return request("POST", `/groups/${encodeURIComponent(groupId)}/reading/toc-lookups/${encodeURIComponent(lookupId)}/images`, {
      idempotencyKey: newIdempotencyKey(),
      formData: form,
    });
  },

  // ---- 開発・デモ用（DEV_MODE=1 のときだけ存在する。第14節） ----
  dev: {
    status: () => request("GET", "/dev/status"),
    advanceClock: (seconds) => request("POST", "/dev/clock/advance", { body: { seconds } }),
    login: (discordUserId) => request("POST", "/dev/login", { body: { discord_user_id: discordUserId } }),
    faults: (faults) => request("PUT", "/dev/faults", { body: faults }),
    seed: (scenario) => request("POST", "/dev/seed", { body: { scenario } }),
  },
};
