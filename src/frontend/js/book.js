// ブック詳細（/book.html?group_id=…&id=…）。
//
// ブックは期間・開催回数・所要時間・日程調整の開始タイミングを持ち、セッション枠はブック全体の計画として
// 最初から時系列に並ぶ。実セッションが作られる前の枠も表示する。
// 担当の仮割当への回答（自分の担当をまとめて）と、開催直前の担当確認はこの画面で答える。
// 担当の交代はAIが決めない。変更希望・交代候補の承認待ち・確定は区別して表示する。
// 実セッションの作成は日程調整の流れで自動的に行われるため、ここには「開始」の操作を置かない。

import { el, mount, formatDateTime, memberName } from "./dom.js";
import { api, ApiError, ASSIGNMENT_DECISION, SLOT_CONFIRMATION, createIdempotencyTracker } from "./api.js";
import { createBookForm } from "./bookForm.js";
import {
  ASSIGNMENT_KIND, assignmentState, schedulingState, slotStatusLabel, slotsAwaitingMyAnswer, slotAwaitsMyConfirmation,
  planLabel, editLockReason, formatPeriod,
} from "./features/reading/planning.js";
import { renderTopbar, renderDevBar, placeholder, flash, clearFlash, reportMutationError, describeError } from "./ui.js";

const $ = (id) => document.getElementById(id);
const query = new URLSearchParams(location.search);
const groupId = query.get("group_id");
const bookId = query.get("id");
const loginUrl = api.loginUrl(location.pathname + location.search);

let detail = null; // ブックの詳細（book・sessions・permissions）
let group = null; // メンバー名と自分の member_id を得るため。取れなければ null
let siblings = null; // 同じグループのほかのブック。取れなければ null
let busy = false;
let editPanel = null; // 開いている編集パネル

boot();

async function boot() {
  if (!groupId || !bookId) {
    mount($("book"), placeholder("ブックが指定されていません", "グループ一覧から開き直してください。"));
    return;
  }
  await renderDevBar($("devbar"), api, { onChange: () => location.reload() });
  let me;
  try {
    me = await api.me();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    mount($("book"), placeholder("ブックを表示できません", "時間をおいて開き直してください。"));
    return;
  }
  renderTopbar($("topbar"), { me, current: null });
  await refresh();
}

/** 最新を取り直して描く。すでに表示中なら、失敗しても表示は残して知らせる。 */
async function refresh({ notify = true } = {}) {
  const shown = detail !== null;
  try {
    [detail, group, siblings] = await Promise.all([
      api.book(groupId, bookId),
      api.group(groupId).catch(loginOr(null)),
      api.books(groupId).then((r) => r.items).catch(loginOr(null)),
    ]);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return false;
    }
    if (shown) {
      if (notify) flash($("flash"), { title: "最新の状態を取得できませんでした", detail: `${describeError(err)} 表示は前回の内容のままです。`, tone: "warn" });
      return false;
    }
    const denied = err instanceof ApiError && (err.status === 403 || err.status === 404);
    mount(
      $("book"),
      denied
        ? placeholder("このブックは表示できません", "参加しているグループのブックか、削除されていないかを確認してください。")
        : placeholder("ブックを表示できません", el("span", {}, describeError(err), " ", el("button", { type: "button", class: "btn btn--quiet", onClick: () => refresh() }, "再読み込み"))),
    );
    return false;
  }
  render();
  return true;
}

/** 操作が通ったあとの知らせ。最新を取れなかったときは、操作は受け付けたことと、表示が古いことを分けて伝える。 */
function reportDone(refreshed, title) {
  flash($("flash"), refreshed
    ? { title, detail: "最新の状態を読み込みました。" }
    : { title, detail: "ただし最新の状態を取得できていません。ページを読み込み直して確認してください。", tone: "warn" });
}

/** 補助の取得（メンバー・ほかのブック）が 401 なら、ログインへ。それ以外の失敗は fallback で続ける。 */
function loginOr(fallback) {
  return (err) => {
    if (err instanceof ApiError && err.status === 401) throw err;
    console.error(err);
    return fallback;
  };
}

const members = () => group?.members ?? [];
const myMemberId = () => group?.current_member_id ?? null;
const sectionTitles = () => new Map(detail.book.sections.map((s) => [s.id, s.title]));
const names = (ids, sections) => (ids ?? []).map((id) => sections.get(id) ?? id).join("、");
const slots = () => [...detail.sessions].sort((a, b) => a.sequence_number - b.sequence_number);

function render() {
  editPanel = null; // 組み直すと開いていた編集パネルは消える
  const book = detail.book;
  document.title = `${book.title} — Marunage`;
  if (group) {
    const back = $("back");
    back.href = `/group.html?id=${encodeURIComponent(groupId)}`;
    back.textContent = `${group.name}へ戻る`;
  }
  const sections = sectionTitles();
  const plan = planLabel(book);

  mount(
    $("book"),
    el("div", { class: "page-head" },
      el("p", {}, "ブック"),
      el("h1", {}, book.title),
      el("p", {}, book.isbn ? `ISBN：${book.isbn}` : "ISBN：未登録"),
    ),
    renderSiblings(),
    el("dl", { class: "dl book-facts" },
      el("dt", {}, "期間"), el("dd", { class: "num" }, formatPeriod(book.period_start, book.period_end)),
      el("dt", {}, "開催回数"), el("dd", { class: "num" }, `全${book.planned_session_count}回`),
      el("dt", {}, "1回の所要時間"), el("dd", { class: "num" }, book.duration_minutes ? `${book.duration_minutes}分` : "未設定"),
      el("dt", {}, "日程調整の開始"), el("dd", {}, Number.isInteger(book.adjustment_lead_days) ? `各回の開催目安の${book.adjustment_lead_days}日前から` : "未設定"),
      plan ? el("dt", {}, "計画の状態") : null, plan ? el("dd", {}, plan) : null,
    ),
    el("section", { class: "book-progress" },
      el("div", { class: "section__head" }, el("h2", {}, "全体の進捗"), el("strong", { class: "num" }, `全${book.planned_session_count}回中${book.completed_session_count}回完了`)),
      el("p", { class: "help" }, `完了した範囲：${names(book.completed_section_ids, sections) || "まだありません"}`),
    ),
    renderMyPlan(),
    renderEditSection(),
    el("section", { class: "section" },
      el("div", { class: "section__head" }, el("h2", {}, "セッション計画")),
      slots().length
        ? el("ol", { class: "book-sessions" }, slots().map((slot) => renderSlot(slot, sections)))
        : placeholder("セッション枠はまだありません", "計画が作られると、ここに全回の枠が並びます。"),
    ),
  );
}

/** 同じグループのブックを行き来できる。複数のブックが同時に進んでいても、それぞれ独立して見られる。 */
function renderSiblings() {
  if (!siblings || siblings.length < 2) return null;
  return el("nav", { class: "book-tabs", "aria-label": "このグループのブック" },
    siblings.map((b) => el("a", {
      class: "book-tabs__item",
      href: `/book.html?group_id=${encodeURIComponent(groupId)}&id=${encodeURIComponent(b.id)}`,
      "aria-current": b.id === bookId ? "page" : null,
    }, b.title)));
}

// ---- 自分の担当計画への回答 -----------------------------------------------------

function renderMyPlan() {
  const me = myMemberId();
  if (!me) return null;
  const mine = slots().filter((s) => s.assignee_member_id === me);
  if (mine.length === 0) return null;
  const awaiting = slotsAwaitingMyAnswer(slots(), me);
  const label = mine.map((s) => `第${s.sequence_number}回`).join("・");

  if (awaiting.length === 0) {
    return el("section", { class: "my-plan" },
      el("h2", {}, "あなたの担当"),
      el("p", {}, `${label}を担当する計画です。`),
      el("p", { class: "help" }, "各回の状態は、下のセッション計画で確認できます。"),
    );
  }
  return el("section", { class: "my-plan my-plan--ask", "aria-labelledby": "my-plan-title" },
    el("h2", { id: "my-plan-title" }, "あなたの担当を確認してください"),
    el("p", {}, `あなたの担当は${awaiting.map((s) => `第${s.sequence_number}回`).join("・")}の仮割当です。`),
    el("p", { class: "help" }, "回答は、この計画のあなたの担当すべてにまとめて適用されます。変更を希望しても、交代はすぐには決まりません。"),
    el("div", { class: "actions" },
      actionButton("引き受ける", "btn", () => answerAssignments(ASSIGNMENT_DECISION.accept, "担当の引き受けを受け付けました")),
      actionButton("変更を希望する", "btn btn--quiet", () => answerAssignments(ASSIGNMENT_DECISION.requestChange, "変更の希望を受け付けました")),
    ),
  );
}

async function answerAssignments(decision, message) {
  await act(() => api.answerAssignments(groupId, bookId, decision), message);
}

// ---- セッション枠 -----------------------------------------------------------------

function renderSlot(slot, sections) {
  const session = slot.session;
  const assignment = assignmentState(slot);
  const scheduling = schedulingState(slot);
  const me = myMemberId();
  const mine = !!me && slot.assignee_member_id === me;
  const targets = names(slot.target_section_ids ?? slot.covered_section_ids, sections);
  const assignee = slot.assignee_member_id ? `${memberName(members(), slot.assignee_member_id)}${mine ? "（あなた）" : ""}` : "未定";
  const confirmedTime = session?.schedule_status === "confirmed" && session.starts_at ? formatDateTime(session.starts_at) : null;

  const actions = [];
  if (slotAwaitsMyConfirmation(slot, me)) {
    actions.push(
      actionButton("このまま担当する", "btn", () => confirmAssignee(slot, SLOT_CONFIRMATION.confirm, "担当の確認を受け付けました")),
      actionButton("担当を交代したい", "btn btn--quiet", () => confirmAssignee(slot, SLOT_CONFIRMATION.requestChange, "交代の希望を受け付けました")),
    );
  }
  if (session) actions.push(el("a", { class: "btn btn--quiet", href: `/session.html?id=${encodeURIComponent(session.id)}` }, "セッション詳細を見る"));
  if (detail.permissions.can_manage && slot.status === "active" && session?.schedule_status === "confirmed") {
    actions.push(actionButton("セッションを完了する", "btn btn--quiet", () => complete(slot)));
  }

  return el("li", { class: "book-session slot", "data-status": slot.status },
    el("div", { class: "slot__head" },
      el("strong", {}, `第${slot.sequence_number}回`),
      el("span", { class: "stamp", "data-tone": slot.status === "completed" ? "done" : null }, slotStatusLabel(slot.status)),
    ),
    el("dl", { class: "slot__facts" },
      el("dt", {}, "開催目安"), el("dd", { class: "num" }, formatPeriod(slot.period_start, slot.period_end)),
      el("dt", {}, "対象章"), el("dd", {}, targets || "未定"),
      el("dt", {}, "担当者"), el("dd", {},
        assignee,
        " ",
        el("span", { class: "stamp", "data-assignment": assignment.kind, "data-tone": assignment.kind === ASSIGNMENT_KIND.confirmed ? "done" : assignment.kind === ASSIGNMENT_KIND.unknown ? "warn" : null }, assignment.label),
      ),
      el("dt", {}, "日程調整"), el("dd", {}, scheduling.label, confirmedTime ? `（${confirmedTime}）` : ""),
    ),
    el("p", { class: "help" }, assignment.hint),
    !session ? el("p", { class: "help" }, "実セッションはまだ作られていません。日程調整の開始時期になると、自動で始まります。") : null,
    assignment.kind === ASSIGNMENT_KIND.unknown && assignment.raw ? el("p", { class: "help" }, `状態値：${assignment.raw}`) : null,
    actions.length ? el("div", { class: "actions", style: "margin-top:12px" }, actions) : null,
  );
}

async function confirmAssignee(slot, decision, message) {
  await act(() => api.confirmSlotAssignee(groupId, bookId, slot.slot_id, decision), message);
}

async function complete(slot) {
  await act(async () => {
    const revision = slot.session?.revision ?? slot.session?.current_revision;
    if (!Number.isInteger(revision)) throw new Error("最新のセッション版を取得できません。再読み込みしてください。");
    return api.completeBookSession(groupId, bookId, slot.slot_id, revision);
  }, "セッションを完了しました");
}

// ---- 操作の共通処理 ---------------------------------------------------------------

/** 送信中は同じ画面のほかの操作も止める。成功したら最新を取り直す（画面側で「済み」にしない）。 */
function actionButton(label, cls, run) {
  const button = el("button", { class: cls, type: "button", "data-action": "" }, label);
  button.addEventListener("click", () => {
    if (busy) return;
    run(button);
  });
  return button;
}

function setBusy(value) {
  busy = value;
  $("book").setAttribute("aria-busy", String(value));
  for (const button of document.querySelectorAll("#book button[data-action]")) button.disabled = value;
}

async function act(run, successTitle) {
  if (busy) return;
  clearFlash($("flash"));
  setBusy(true);
  try {
    await run();
  } catch (err) {
    setBusy(false);
    await reportMutationError(err, { node: $("flash"), refresh: () => refresh(), loginUrl });
    return;
  }
  setBusy(false);
  reportDone(await refresh({ notify: false }), successTitle);
}

// ---- ブックの編集（最初の実セッションが作られる前だけ） -------------------------------

function renderEditSection() {
  if (!detail.permissions.can_manage) return null;
  const reason = editLockReason(slots());
  if (reason) {
    return el("section", { class: "edit-lock", "aria-label": "ブックの編集" },
      el("p", {}, el("strong", {}, "ブックの内容は変更できません")),
      el("p", { class: "help" }, reason),
    );
  }
  return el("section", { class: "edit-book", "aria-label": "ブックの編集" },
    el("div", { class: "actions", style: "margin-top:0" },
      el("button", { class: "btn btn--quiet", type: "button", id: "edit-book", onClick: openEdit }, "ブックを編集"),
      el("small", { class: "help", style: "margin:0" }, "最初のセッションが始まるまで、期間や開催回数を直せます。"),
    ),
    el("div", { id: "edit-panel", hidden: true }),
  );
}

function openEdit() {
  const panel = $("edit-panel");
  if (!panel || editPanel) return;
  const book = detail.book;
  const form = createBookForm({
    client: api, groupId, withToc: false, legend: "ブックの編集",
    initial: {
      title: book.title,
      isbn: book.isbn ?? null,
      tocSource: book.toc_source ?? null,
      sections: book.sections,
      periodStart: book.period_start,
      periodEnd: book.period_end,
      plannedSessionCount: book.planned_session_count,
      durationMinutes: book.duration_minutes,
      adjustmentLeadDays: book.adjustment_lead_days,
    },
  });
  const tracker = createIdempotencyTracker();
  const error = el("div", { role: "alert" });
  const save = el("button", { class: "btn", type: "button" }, "変更を保存する");
  const cancel = el("button", { class: "btn btn--quiet", type: "button" }, "キャンセル");

  const close = () => {
    editPanel = null;
    mount(panel);
    panel.hidden = true;
    $("edit-book").disabled = false;
    $("edit-book").focus();
  };
  cancel.addEventListener("click", () => { if (!busy) close(); });
  save.addEventListener("click", async () => {
    if (busy) return;
    clearFlash($("flash"));
    mount(error);
    if (!form.validate()) return;
    busy = true;
    form.setLocked(true);
    save.disabled = cancel.disabled = true;
    save.textContent = "保存しています…";
    const next = form.read();
    try {
      await api.updateBook(groupId, bookId, next, { idempotencyKey: tracker.keyFor(next) });
      tracker.succeeded();
      busy = false;
      close();
      reportDone(await refresh({ notify: false }), "ブックを更新しました");
    } catch (err) {
      tracker.failed(err);
      busy = false;
      form.setLocked(false);
      save.disabled = cancel.disabled = false;
      save.textContent = "もう一度保存する";
      if (err instanceof ApiError && err.status === 401) {
        location.href = loginUrl;
        return;
      }
      const message = err instanceof ApiError && err.isConflict
        ? `${err.message} 最初の実セッションが作られた可能性があります。ページを読み込み直してください。`
        : err instanceof ApiError && err.status === 403 ? "ブックを編集できるのは管理者だけです。" : describeError(err);
      mount(error, el("div", { class: "notice", "data-tone": "warn" },
        el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
        el("span", {}, el("strong", {}, "ブックを更新できませんでした"), el("small", {}, message))));
    }
  });

  editPanel = { close };
  $("edit-book").disabled = true;
  panel.hidden = false;
  mount(panel, el("div", { class: "book-entry" }, form.node, error, el("div", { class: "submit" }, cancel, save)));
  form.focus();
}
