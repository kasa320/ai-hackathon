import { api } from "./api.js";

const el = document.getElementById("health");

try {
  const res = await api.health();
  el.textContent = `${res.status}（${res.now}）`;
} catch (err) {
  el.textContent = `接続できません（${err.message}）`;
}
