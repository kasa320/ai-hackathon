// バックエンド API の呼び出しをまとめる。画面側の JS はここを通して API を使う。

export class ApiError extends Error {
  constructor(status, body) {
    super(`API error: ${status}`);
    this.status = status;
    this.body = body;
  }
}

async function request(method, path, body) {
  const res = await fetch(`/api${path}`, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
  });
  const data = res.headers.get("Content-Type")?.includes("application/json")
    ? await res.json()
    : null;
  if (!res.ok) {
    throw new ApiError(res.status, data);
  }
  return data;
}

export const api = {
  health: () => request("GET", "/health"),
};
