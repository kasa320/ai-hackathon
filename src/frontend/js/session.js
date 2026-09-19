// 開催回の画面（/session.html?id=...）。契約 第1部 第6〜11節、第3部。
//
// この画面が守ること：
// - 確定可否や多数決を自分で計算しない。permissions・task・proposal の状態を使う
// - 更新は 202 を受けたら成功扱いにせず、詳細を取り直して表示を合わせる
// - 409 を受けたら入力を保ったまま最新状態を出し、再確認を求める

import { api, ApiError, NetworkError } from "./api.js";
import { createMockApi, MOCK_STATES } from "./mock.js";
import { createPoller } from "./poll.js";
import { el, mount, clear, formatDateTime, remaining, memberName } from "./dom.js";
import { featureFor } from "./features/index.js";
import { renderProposalCard } from "./components/proposal.js";
import { renderTaskCard, primaryTask } from "./components/tasks.js";
import { renderHealth } from "./components/health.js";
import { renderActivity } from "./components/activity.js";
import { mountBackdrop, mountTopbar } from "./components/layout.js";

const params = new URLSearchParams(location.search);
const sessionId = params.get("id") ?? "ses_demo";

// API 実装前なので既定はモック。?live=1 で実 API に切り替える。
const useLive = params.get("live") === "1";
const mockOpts = { state: params.get("mock") ?? "replan", viewer: params.get("as") ?? "mem_c" };
const client = useLive ? api : createMockApi(mockOpts);

const $ = (id) => document.getElementById(id);
let detail = null;
let busy = false;
let poller = null;

// ---- デモ用の帯（本番ビルドでは丸ごと消す） --------------------------------

function buildDemoBar() {
  if (useLive) {
    $("demo-mode").textContent = "実API";
    $("demo-controls").hidden = true;
    return;
  }
  const stateSel = el("select", { id: "demo-state", onchange: (e) => go({ mock: e.target.value }) },
    Object.entries(MOCK_STATES).map(([value, label]) =>
      el("option", { value, selected: value === mockOpts.state }, label)));

  const viewerSel = el("select", { id: "demo-viewer", onchange: (e) => go({ as: e.target.value }) },
    [["mem_a", "A（管理者）"], ["mem_b", "B"], ["mem_c", "C"], ["mem_d", "D"]].map(([value, label]) =>
      el("option", { value, selected: value === mockOpts.viewer }, label)));

  mount($("demo-controls"),
    el("label", {}, "状態 ", stateSel),
    el("label", {}, "表示中 ", viewerSel),
    el("a", { class: "demobar__link", href: withParams({ live: "1" }) }, "実APIで開く"),
  );
}

function withParams(next) {
  const p = new URLSearchParams(location.search);
  for (const [k, v] of Object.entries(next)) v === null ? p.delete(k) : p.set(k, v);
  return `${location.pathname}?${p}`;
}

const go = (next) => { location.href = withParams(next); };

// ---- 状態の文言（契約の enum をそのまま画面に出さない） ---------------------

const CASE_LABEL = {
  collecting: { text: "返事を集めています", cls: "badge--warn" },
  planning: { text: "案を作っています", cls: "badge--warn" },
  awaiting_consent: { text: "同意を集めています", cls: "badge--warn" },
  confirmed: { text: "確定", cls: "badge--ok" },
  needs_owner: { text: "管理者の判断待ち", cls: "badge--danger" },
};

const SESSION_LABEL = {
  draft: { text: "準備中", cls: "badge--warn" },
  confirmed: { text: "確定", cls: "badge--ok" },
  needs_attention: { text: "要調整", cls: "badge--danger" },
};

const REASON_LABEL = {
  no_feasible_plan: "条件を満たす案が作れませんでした",
  deadline_expired: "期限までに必要な回答が揃いませんでした",
  model_error: "AIの出力が不正でした",
  budget_exceeded: "この案件の上限に達しました",
};

// ---- 描画 ------------------------------------------------------------------

function render() {
  if (!detail) return;
  const feature = featureFor(detail.session.playbook_id);
  if (!feature) {
    mount($("main"), el("article", { class: "card" },
      el("p", {}, `この用途（${detail.session.playbook_id}）の画面はまだありません。`)));
    return;
  }
  renderCrumbs(feature);
  renderHero(feature);
  renderBody(feature);
}

/** 「自分がいまやること」。常に期限の近い1件だけを大きく出す。 */
function renderHero(feature) {
  const hero = $("hero");
  const task = primaryTask(detail.my_tasks);
  const others = detail.my_tasks.filter((t) => t.status === "open" && t !== task && t.allowed_decisions.length);

  hero.className = "hero";

  if (!task) {
    const confirmed = detail.confirmed_plan && detail.session.status === "confirmed";
    hero.classList.add(confirmed ? "hero--calm" : "hero--work");
    mount(hero,
      el("p", { class: "hero__eyebrow" },
        el("span", {}, confirmed ? "次回の準備はできています" : "いま確認をお願いすることはありません")),
      el("h1", { class: "hero__title" },
        formatDateTime(detail.session.starts_at), el("br"), feature.sessionHeadline(detail.data)),
      el("p", { class: "hero__lead" }, caseSentence()),
    );
    return;
  }

  mount(hero,
    el("p", { class: "hero__eyebrow" },
      el("span", {}, "あなたへの確認"),
      el("span", { class: "deadline" }, "回答期限まで ",
        el("span", { class: "num" }, remaining(task.due_at, detail.server_now))),
    ),
    el("h1", { class: "hero__title" }, task.title),
    el("p", { class: "hero__lead" }, heroLead(task)),
    el("div", { class: "hero__actions" }, decisionButtons(task)),
    others.length ? el("p", { class: "hero__lead", style: "font-size:13px;margin-top:18px" },
      `ほかに ${others.length} 件の確認があります。下の「あなたへの確認」に出ています。`) : null,
  );
}

function heroLead(task) {
  if (task.kind === "preparation") {
    return "参加できるか、担当して説明できるかを教えてください。都合がつかない理由は入力しません。";
  }
  if (task.kind === "assignment") {
    return "引き受けられるのは本人だけです。断っても構いません。その場合はAIが別の案を作ります。";
  }
  if (task.kind === "owner_approval") {
    return "承認しても、担当者本人の引き受けを代わりに行うことはできません。";
  }
  return "変更に賛成か反対かを選んでください。返事をしないことは賛成として扱われません。";
}

function decisionButtons(task) {
  const label = { submit: "回答する", accept: "引き受ける", decline: "引き受けない", approve: "賛成する", reject: "反対する" };
  return task.allowed_decisions.map((decision, i) => el("button", {
    type: "button",
    class: i === 0 ? "btn btn--primary" : "btn",
    disabled: busy,
    onclick: () => (task.kind === "preparation" ? openPreparation() : decide(task, decision)),
  }, label[decision] ?? decision));
}

function caseSentence() {
  const c = detail.active_case;
  if (!c) return "この開催回に進行中の調整はありません。";
  return c.summary || "調整を進めています。";
}

/** 止まった理由は本文に混ぜず、別の一行として出す。 */
function caseReason() {
  const c = detail.active_case;
  if (!c || !c.reason_code) return null;
  return REASON_LABEL[c.reason_code] ?? "自動では進められませんでした";
}

/** ヘッダーの現在地。契約の用語ではなく利用者の言葉で書く。 */
function renderCrumbs(feature) {
  mountTopbar({
    crumbs: [
      { label: "グループ", href: "/" },
      { label: feature.sessionHeadline(detail.data) },
      { label: detail.session.title },
    ],
    me: { display_name: memberName(detail.members, detail.current_member_id) },
  });
}

function renderBody(feature) {
  const left = [];
  const right = [];

  // --- いま調整していること ---
  const c = detail.active_case;
  if (c) {
    left.push(el("p", { class: "section-title" }, "いま調整していること"));
    left.push(el("article", { class: "card" },
      el("div", { class: "card__head" },
        el("span", { class: "card__title" }, CASE_LABEL[c.status]?.text ?? c.status),
        el("span", { class: `badge ${CASE_LABEL[c.status]?.cls ?? "badge--muted"}` },
          SESSION_LABEL[detail.session.status]?.text ?? detail.session.status),
      ),
      caseReason() ? el("p", { class: "badge badge--danger", style: "margin-bottom:10px" }, caseReason()) : null,
      el("p", { class: "card__note" }, caseSentence()),
      c.next_retry_at
        ? el("p", { class: "waiting" }, `AIの応答に失敗したため、${formatDateTime(c.next_retry_at)} に再試行します。待っている間はLLMを呼びません。`)
        : null,
      c.status === "collecting" || c.status === "awaiting_consent"
        ? el("p", { class: "waiting" }, "返事を待っています。届いたら自動で再開します。")
        : null,
    ));
  }

  // --- 案（用途固有の中身は feature に描かせる） ---
  const current = detail.current_proposal;
  const confirmed = detail.confirmed_plan;
  const duration = detail.session.duration_minutes;

  if (current && current.status === "pending") {
    const before = confirmed && confirmed.id !== current.id ? confirmed.data : null;
    left.push(renderProposalCard(current, detail.members,
      (plan) => before
        ? feature.renderPlanDiff(before, plan, detail.data, detail.members, duration)
        : feature.renderPlan(plan, detail.data, detail.members, duration)));
  }

  if (confirmed && (!current || confirmed.id !== current.id)) {
    const superseded = detail.session.status === "needs_attention";
    left.push(el("p", { class: "section-title" }, superseded ? "実行できなくなった計画" : "確定した計画"));
    left.push(renderProposalCard(
      { ...confirmed, status: superseded ? "superseded" : confirmed.status },
      detail.members,
      (plan) => feature.renderPlan(plan, detail.data, detail.members, duration),
      { heading: superseded ? "以前の確定計画" : "確定した計画" },
    ));
    if (superseded) {
      left.push(el("p", { class: "card__note" },
        "この計画は履歴です。担当の辞退で実行できなくなっているため、いまは有効ではありません。"));
    }
  }

  if (!current && !confirmed) {
    left.push(el("article", { class: "card" },
      el("div", { class: "card__head" }, el("span", { class: "card__title" }, "計画はまだありません")),
      el("p", { class: "card__note" }, "全員の参加条件が揃うと、AIが最初の案を作ります。"),
      feature.renderEmptyAgenda(duration),
    ));
  }

  // --- 自分への確認（ヒーローに出した1件も含めて一覧に出す） ---
  const openTasks = detail.my_tasks.filter((t) => t.allowed_decisions.length > 0);
  if (openTasks.length > 0) {
    left.push(el("p", { class: "section-title", id: "your-tasks" }, "あなたへの確認"));
    for (const t of openTasks) {
      left.push(renderTaskCard(t, {
        serverNow: detail.server_now,
        busy,
        onDecide: decide,
        onOpenPreparation: openPreparation,
      }));
    }
  }

  // --- 右：開催回の事実 ---
  right.push(el("p", { class: "section-title" }, "この回について"));
  right.push(el("article", { class: "card" },
    el("div", { class: "card__head" },
      el("span", { class: "card__title" }, formatDateTime(detail.session.starts_at)),
      el("span", { class: `badge ${SESSION_LABEL[detail.session.status]?.cls}` },
        SESSION_LABEL[detail.session.status]?.text ?? detail.session.status),
    ),
    feature.renderSessionData(detail.data),
  ));

  right.push(el("article", { class: "card" },
    el("div", { class: "card__head" }, el("span", { class: "card__title" }, "参加条件")),
    feature.renderPreparations(detail.preparations, detail.members, detail.data),
    el("p", { class: "card__note", style: "margin-top:14px" },
      "共有されるのは参加可否と担当できる範囲だけです。都合がつかない理由は集めていません。"),
    myActions(),
  ));

  right.push(el("article", { class: "card" },
    el("div", { class: "card__head" }, el("span", { class: "card__title" }, "通知")),
    renderNotifications(),
  ));

  mount($("left"), left);
  mount($("right"), right);

  // --- 管理者だけ：実行履歴・費用 ---
  $("activity-section").hidden = !detail.permissions.can_view_activity;
  if (detail.permissions.can_view_activity) loadActivity();
}

function myActions() {
  const p = detail.permissions;
  const buttons = [];
  if (p.can_update_preparation) {
    buttons.push(el("button", { type: "button", class: "btn btn--sm", disabled: busy, onclick: openPreparation },
      detail.preparations.find((x) => x.member_id === detail.current_member_id)?.value ? "参加条件を変更する" : "参加条件を回答する"));
  }
  if (p.can_withdraw_assignment) {
    buttons.push(el("button", { type: "button", class: "btn btn--sm", disabled: busy, onclick: () => withdraw("assignment") }, "担当を辞退する"));
  }
  if (p.can_submit_proposal) {
    buttons.push(el("button", { type: "button", class: "btn btn--primary btn--sm", disabled: busy, onclick: openProposalForm }, "代案を出す"));
  }
  if (p.can_withdraw_attendance) {
    buttons.push(el("button", { type: "button", class: "btn btn--sm", disabled: busy, onclick: () => withdraw("attendance") }, "欠席を伝える"));
  }
  if (!buttons.length) return null;
  return el("div", { class: "hero__actions", style: "margin-top:16px" }, buttons);
}

function renderNotifications() {
  const n = detail.notification_summary;
  const rows = [
    ["送信済み", n.sent_count],
    ["送信待ち", n.pending_count],
    ["失敗", n.failed_count],
    ["成否不明", n.unknown_count],
  ];
  return el("div", {},
    el("div", { class: "facts" }, rows.map(([label, count]) => el("div", { class: "facts__row" },
      el("span", { class: "facts__key" }, label),
      el("span", { class: count > 0 ? "facts__val" : "facts__val facts__val--tbd" }, el("span", { class: "num" }, String(count))),
    ))),
    el("p", { class: "card__note", style: "margin-top:12px" },
      "「送信済み」はDiscordが受け付けたという意味で、読まれたという意味ではありません。"),
  );
}

async function loadActivity() {
  try {
    const data = await client.activity(sessionId);
    mount($("activity"), renderActivity(data));
  } catch (err) {
    if (err.status === 403) { $("activity-section").hidden = true; return; }
    mount($("activity"), el("p", { class: "card__note" }, `実行履歴を取得できません（${err.code ?? err.message}）`));
  }
}

// ---- 操作 ------------------------------------------------------------------

function setBusy(on) { busy = on; render(); }

function notice(message, tone = "warn") {
  const box = $("notice");
  box.hidden = false;
  box.className = `notice notice--${tone}`;
  box.textContent = message;
}

function clearNotice() { $("notice").hidden = true; }

/** 409 は「最新を取り直して再確認」。入力は消さない（契約 第2節）。 */
async function handleMutationError(err) {
  if (err instanceof NetworkError) {
    notice("サーバーに接続できません。通信が戻ったらもう一度お試しください。");
    return;
  }
  if (!(err instanceof ApiError)) { notice(String(err.message)); return; }

  if (err.status === 401) { location.href = client.loginUrl(location.pathname + location.search); return; }
  if (err.isConflict) {
    notice(`${err.message} 最新の内容を読み込みました。もう一度確認してください。`);
    await refresh();
    return;
  }
  if (err.status === 422) {
    notice(err.fieldErrors.map((f) => f.message).join(" ") || err.message);
    return;
  }
  notice(`${err.message}${err.requestId ? `（${err.requestId}）` : ""}`);
}

async function decide(task, decision) {
  clearNotice();
  setBusy(true);
  try {
    await client.respondToTask(task.id, {
      decision,
      proposal_id: task.proposal_id,
      proposal_version: task.proposal_version,
    });
    notice("回答を受け付けました。反映を待っています。", "ok");
    await refresh();
  } catch (err) {
    await handleMutationError(err);
  } finally {
    setBusy(false);
  }
}

async function withdraw(scope) {
  const label = scope === "assignment" ? "今回の担当をすべて辞退します。" : "今回は欠席と伝えます。";
  if (!confirm(`${label}\n理由は送られません。よろしいですか？`)) return;
  clearNotice();
  setBusy(true);
  try {
    await client.withdraw(sessionId, detail.session.revision, scope);
    notice("受け付けました。AIが代わりの案を検討します。", "ok");
    await refresh();
  } catch (err) {
    await handleMutationError(err);
  } finally {
    setBusy(false);
  }
}

// ---- ドロワー（参加条件の入力・管理者の代案） -------------------------------

let formHandle = null;
let submitHandler = null;

function openDrawer({ title, handle, submitLabel, onSubmit }) {
  formHandle = handle;
  submitHandler = onSubmit;
  mount($("drawer-form"), handle.node);
  $("drawer-title").textContent = title;
  $("drawer-submit").textContent = submitLabel;
  $("drawer").setAttribute("open", "");
  document.body.style.overflow = "hidden";
  handle.node.querySelector("input, select")?.focus();
}

function closeDrawer() {
  $("drawer").removeAttribute("open");
  document.body.style.overflow = "";
  formHandle = null;
  submitHandler = null;
}

function openPreparation() {
  const feature = featureFor(detail.session.playbook_id);
  const current = detail.preparations.find((x) => x.member_id === detail.current_member_id)?.value ?? null;
  openDrawer({
    title: `${detail.session.title}　参加条件の回答`,
    handle: feature.createPreparationForm(detail.data, current, detail.session.duration_minutes),
    submitLabel: "回答を送る",
    onSubmit: submitPreparation,
  });
}

function openProposalForm() {
  const feature = featureFor(detail.session.playbook_id);
  const base = detail.confirmed_plan?.data ?? null;
  openDrawer({
    title: `${detail.session.title}　管理者の代案`,
    handle: feature.createPlanForm(detail.data, detail.members, detail.session.duration_minutes, base),
    submitLabel: "この案を出す",
    onSubmit: submitProposal,
  });
}

/** 管理者の代案を送る。検証は R3 と同じ規則を先に当ててから。 */
async function submitProposal() {
  if (!formHandle) return;
  const feature = featureFor(detail.session.playbook_id);
  const plan = formHandle.read();

  const errors = feature.validatePlan(plan, detail.data, detail.session.duration_minutes);
  if (errors.length) { formHandle.showErrors(errors); return; }

  clearNotice();
  setBusy(true);
  try {
    await client.submitProposal(sessionId, detail.session.revision, plan);
    closeDrawer();
    notice("代案を受け付けました。担当者の引き受けと必要な同意を集めます。", "ok");
    await refresh();
  } catch (err) {
    await handleMutationError(err);
  } finally {
    setBusy(false);
  }
}

async function submitPreparation() {
  if (!formHandle) return;
  const feature = featureFor(detail.session.playbook_id);
  const preparation = formHandle.read();

  const errors = feature.validate(preparation, detail.session.duration_minutes);
  if (errors.length) { formHandle.showErrors(errors); return; }

  clearNotice();
  setBusy(true);
  try {
    const openPrep = detail.my_tasks.find((t) => t.kind === "preparation" && t.status === "open");
    if (openPrep) {
      await client.respondToTask(openPrep.id, {
        decision: "submit",
        expected_revision: detail.session.revision,
        preparation,
      });
    } else {
      await client.updatePreparation(sessionId, detail.session.revision, preparation);
    }
    closeDrawer();
    notice("回答を受け付けました。", "ok");
    await refresh();
  } catch (err) {
    // 入力は消さない。409 なら最新を読み直したうえで、下書きのまま再確認させる
    await handleMutationError(err);
  } finally {
    setBusy(false);
  }
}

// ---- 起動 ------------------------------------------------------------------

async function refresh() {
  detail = await client.session(sessionId);
  render();
}

async function boot() {
  mountBackdrop();
  mountTopbar({ crumbs: [{ label: "開催回" }] });
  buildDemoBar();
  const health = await renderHealth($("health"), client);
  $("demo-mode").textContent = client.isMock ? "モック" : health?.dev_mode ? "デモモード" : "本番";

  $("drawer-submit").addEventListener("click", () => submitHandler?.());
  for (const b of document.querySelectorAll("[data-close-drawer]")) b.addEventListener("click", closeDrawer);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeDrawer(); });

  try {
    await client.me();
  } catch (err) {
    if (err.status === 401) {
      mount($("main"), el("article", { class: "card" },
        el("div", { class: "card__head" }, el("span", { class: "card__title" }, "ログインが必要です")),
        el("p", { class: "card__note" }, "この開催回を見るには、Discordでログインしてください。"),
        el("p", { style: "margin-top:16px" },
          el("a", { class: "btn btn--primary", href: client.loginUrl(location.pathname + location.search) }, "Discordでログイン")),
      ));
      clear($("hero"));
      return;
    }
    throw err;
  }

  try {
    await refresh();
  } catch (err) {
    mount($("main"), el("article", { class: "card" },
      el("p", { class: "card__note" }, `開催回を取得できません（${err.code ?? err.message}）`)));
    return;
  }

  poller = createPoller({
    fetcher: () => client.session(sessionId),
    onData: (data) => { detail = data; render(); },
    onError: (err, { fatal }) => {
      if (fatal) notice("この開催回を表示できなくなりました。画面を読み込み直してください。");
    },
  });
  poller.start();
}

boot();
