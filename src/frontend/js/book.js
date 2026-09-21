import { el, mount } from "./dom.js";
import { api, ApiError } from "./api.js";
import { renderTopbar, renderDevBar, placeholder, flash, clearFlash, reportMutationError, createDialog, DATE_MAX } from "./ui.js";

const $ = (id) => document.getElementById(id);
const query = new URLSearchParams(location.search);
const groupId = query.get("group_id");
const bookId = query.get("id");
const loginUrl = api.loginUrl(location.pathname + location.search);
let detail = null;
let submitting = false;

boot();

async function boot() {
  if (!groupId || !bookId) {
    mount($("book"), placeholder("ブックが指定されていません", "サークル一覧から開き直してください。"));
    return;
  }
  await renderDevBar($("devbar"), api, { onChange: () => location.reload() });
  let me;
  try { me = await api.me(); }
  catch (err) {
    if (err instanceof ApiError && err.status === 401) { location.href = loginUrl; return; }
    mount($("book"), placeholder("ブックを表示できません", "時間をおいて開き直してください。")); return;
  }
  renderTopbar($("topbar"), { me, current: null });
  await refresh();
}

async function refresh() {
  try {
    detail = await api.book(groupId, bookId);
    render();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) location.href = loginUrl;
    else mount($("book"), placeholder("ブックを表示できません", "アクセスできるブックか確認してください。"));
  }
}

function render() {
  const book = detail.book;
  document.title = `${book.title} — Marunage`;
  const sections = new Map(book.sections.map((s) => [s.id, s.title]));
  mount($("book"),
    el("div", { class: "page-head" },
      el("p", {}, "ブック"), el("h1", {}, book.title),
      el("p", {}, book.isbn ? `ISBN：${book.isbn}` : "ISBN：未登録"),
    ),
    el("section", { class: "book-progress" },
      el("div", { class: "section__head" }, el("h2", {}, "全体の進捗"), el("strong", { class: "num" }, `全${book.planned_session_count}回中${book.completed_session_count}回完了`)),
      el("p", { class: "help" }, `完了した範囲：${names(book.completed_section_ids, sections) || "まだありません"}`),
    ),
    el("section", { class: "section" },
      el("div", { class: "section__head" }, el("h2", {}, "セッション"),
        detail.permissions.can_manage && book.session_creation_mode === "sequential" && detail.sessions.length < book.planned_session_count
          ? el("button", { class: "btn", type: "button", disabled: submitting, onClick: () => openSessionForm(null) }, "次のセッションを作る")
          : null),
      el("div", { class: "book-sessions" }, detail.sessions.map((slot) => renderSlot(slot, sections))),
    ),
  );
}

function renderSlot(slot, sections) {
  const session = slot.session;
  const state = { planned: "開始前", active: "調整中", completed: "完了" }[slot.status] ?? slot.status;
  const body = slot.status === "planned"
    ? el("p", { class: "help" }, "まだ通知・調整は始まっていません。")
    : slot.status === "completed"
      ? el("p", { class: "help" }, `扱った範囲：${names(slot.covered_section_ids, sections) || "記録なし"}`)
      : el("p", { class: "help" }, names(slot.covered_section_ids, sections) ? `対象範囲：${names(slot.covered_section_ids, sections)}` : "対象範囲を確認中です。");
  const actions = [];
  if (slot.status === "active" && session) actions.push(el("a", { class: "btn btn--quiet", href: `/session.html?id=${encodeURIComponent(session.id)}` }, "セッション詳細を見る"));
  if (detail.permissions.can_manage && slot.status === "planned" && detail.book.session_creation_mode === "all") actions.push(el("button", { class: "btn", type: "button", disabled: submitting, onClick: () => openSessionForm(slot) }, "このセッションを開始"));
  if (detail.permissions.can_manage && slot.status === "active" && session?.schedule_status === "confirmed") actions.push(el("button", { class: "btn btn--quiet", type: "button", disabled: submitting, onClick: () => complete(slot) }, "セッションを完了する"));
  return el("article", { class: "book-session" },
    el("div", {}, el("strong", {}, `第${slot.sequence_number}回`), el("span", { class: "stamp", "data-tone": slot.status === "completed" ? "done" : null }, state)),
    body, actions.length ? el("div", { class: "actions", style: "margin-top:12px" }, actions) : null,
  );
}

function names(ids, sections) { return (ids ?? []).map((id) => sections.get(id) ?? id).join("、"); }

function openSessionForm(slot) {
  const from = el("input", { type: "date", min: today(), max: DATE_MAX });
  const to = el("input", { type: "date", min: today(), max: DATE_MAX });
  const duration = el("input", { type: "number", min: "15", max: "180", step: "5", value: "60" });
  const checks = el("div", { class: "checks" }, detail.book.sections.map((section) =>
    el("label", {}, el("input", { type: "checkbox", value: section.id }), section.title,
      detail.book.completed_section_ids.includes(section.id) ? el("small", {}, "完了済み") : null)));
  createDialog({
    title: slot ? `第${slot.sequence_number}回を開始` : "次のセッションを作る",
    body: el("div", { class: "prep-form" },
      el("div", { class: "grid grid--2" }, el("label", { class: "field" }, el("span", {}, "開始日"), from), el("label", { class: "field" }, el("span", {}, "終了日"), to)),
      el("label", { class: "field" }, el("span", {}, "所要時間（分）"), duration),
      el("fieldset", { class: "fieldset" }, el("legend", {}, "対象範囲"), checks)),
    submitLabel: slot ? "開始する" : "作る",
    onSubmit: async ({ showError, close }) => {
      const target = [...checks.querySelectorAll("input:checked")].map((input) => input.value);
      if (!from.value || !to.value || to.value < from.value || !target.length || !Number.isInteger(Number(duration.value)) || Number(duration.value) < 15 || Number(duration.value) > 180) {
        showError("期間、所要時間、対象範囲を確認してください。"); return;
      }
      try {
        submitting = true;
        const payload = { period_start: from.value, period_end: to.value, duration_minutes: Number(duration.value), target_section_ids: target };
        if (slot) payload.slot_id = slot.slot_id;
        await api.createBookSession(groupId, bookId, payload);
        submitting = false;
        close(); clearFlash($("flash")); await refresh();
      } catch (err) {
        await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
        showError(err.message ?? "開始できませんでした。");
      } finally { submitting = false; }
    },
  });
}

async function complete(slot) {
  if (submitting) return;
  submitting = true;
  try {
    const revision = slot.session?.revision ?? slot.session?.current_revision;
    if (!Number.isInteger(revision)) throw new Error("最新のセッション版を取得できません。再読み込みしてください。");
    detail = await api.completeBookSession(groupId, bookId, slot.slot_id, revision);
    submitting = false;
    flash($("flash"), { title: "セッションを完了しました" });
    render();
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
  } finally { submitting = false; }
}

function today() { const d = new Date(); return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`; }
