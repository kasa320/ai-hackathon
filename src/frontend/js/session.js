// 開催回の画面。契約 第1部 第6・7・8・9・11節、第3部「画面ごとの利用API」。
//
// 並び順の理由：
//   1. あなたへの依頼   … 読めば次にすることが決まる。最大の面
//   2. 今回の案         … 実寸の帯と押印欄。合意がどこまで集まったか
//   3. 開催回とみんなの回答
//   4. 実行の記録       … 管理者のみ
//
// 入力中はポーリングで画面を書き換えない（打ち込んだ内容が消えるため）。
// 送信時の expected_revision は常に最新の detail から取る。

import { el, mount, formatDateTime } from "./dom.js";
import { client } from "./client.js";
import { createPoller } from "./poll.js";
import { renderNav, renderDevBar, renderHealth, setStatus } from "./components/chrome.js";
import { renderCall } from "./components/tasks.js";
import { approvalSeals, assignmentSeals } from "./components/seals.js";
import { renderActivity } from "./components/activity.js";
import { agendaBar, planDiff, preparationRows } from "./features/reading/plan.js";
import { preparationForm } from "./features/reading/preparation.js";
import { planForm } from "./features/reading/planForm.js";

const sessionId = new URLSearchParams(location.search).get("id");

const $call = document.getElementById("call");
const $plan = document.getElementById("plan");
const $facts = document.getElementById("facts");
const $activity = document.getElementById("activity");

let detail = null;
let me = null;
let mode = "view";          // "view" | "prep" | "proposal"
let activity = null;
let activityRevision = null;
let poller = null;

boot();

async function boot() {
  const health = await renderHealth();
  renderDevBar({ devMode: health.dev_mode });

  if (!sessionId) {
    return mount($call, el("p", { class: "empty" },
      el("strong", {}, "開催回が指定されていません"),
      "リンクに id が含まれていません。通知のリンクから開き直してください。"));
  }

  try {
    me = await client.me();
  } catch (err) {
    if (err.status === 401) {
      renderNav(null, "session");
      return mount($call, el(
        "section",
        { class: "call call--act" },
        el("p", { class: "call__kind" }, "ログインが必要です"),
        el("h1", { class: "call__title" }, "この開催回を見るにはログインしてください。"),
        el("div", { class: "btns" },
          el("a", { class: "btn btn--main", href: client.loginUrl(location.pathname + location.search) }, "Discord でログイン")),
      ));
    }
    throw err;
  }

  renderNav(me, "session");

  poller = createPoller({
    fetcher: () => client.session(sessionId),
    onData(next) {
      detail = next;
      setStatus("");
      render();
    },
    onError(err, { fatal }) {
      if (fatal) return setStatus(fatalText(err), { warn: true });
      setStatus("サーバーに接続できません。間隔をあけて再接続します。", { warn: true });
    },
  });
  poller.start();
}

function fatalText(err) {
  if (err.status === 401) return "ログインの期限が切れました。もう一度ログインしてください。";
  if (err.status === 403) return "この開催回を見る権限がありません。";
  return "この開催回は見つかりません。";
}

// ------------------------------------------------------------------
// 描画
// ------------------------------------------------------------------

function render() {
  mount($call, renderCall(detail, {
    onDecision: respond,
    onOpenPreparation: () => { mode = "prep"; drawPlanArea(); },
    onOpenProposal: () => { mode = "proposal"; drawPlanArea(); },
  }));

  // 入力中はこの領域に触らない。3秒ごとの取り直しで打ち込んだ内容が消えるため
  if (mode === "view") drawPlanArea();

  mount($facts, factsSection(), membersSection(), myActionsSection());
  drawActivity();
}

function drawPlanArea() {
  if (mode === "prep") {
    return mount($plan, preparationForm(detail, submitPreparation, () => { mode = "view"; drawPlanArea(); }));
  }
  if (mode === "proposal") {
    return mount($plan, planForm(detail, submitProposal, () => { mode = "view"; drawPlanArea(); }));
  }
  mount($plan, ...planSections());
}

function planSections() {
  const { current_proposal: current, confirmed_plan: confirmed, session } = detail;
  const out = [];

  if (current) {
    const author = current.author === "agent" ? "AIが起案" : "管理者が提出";
    const kind = current.change_kind === "initial" ? "初回の案" : "組み直した案";
    const superseded = current.status === "superseded" || current.status === "rejected";

    out.push(el(
      "section",
      { class: "sec" },
      el(
        "div",
        { class: "sec__head" },
        el("h2", { class: "sec__title" }, current.status === "confirmed" ? "確定した計画" : kind),
        el("span", { class: "sec__meta" }, `版 ${current.version}・${author}`),
      ),

      superseded && el("p", { class: "note note--warn", style: "margin-bottom:24px" },
        "この案は最新ではありません。新しい案が出るまで、内容は記録として表示しています。"),

      current.summary && el("p", { class: "prose", style: "margin-bottom:24px" }, current.summary),

      agendaBar(current.data, detail.data, detail.members, session.duration_minutes),

      diffBlock(confirmed, current),

      el("div", { style: "display:grid;gap:24px;margin-top:40px" },
        assignmentSeals(current.assignments, detail.members, detail.current_member_id),
        ...current.approvals.map((a) => approvalSeals(a, detail.members, detail.current_member_id))),
    ));
  }

  // 確定計画が実行不能になった場合は、履歴として出す（契約 第6節）
  if (confirmed && session.status === "needs_attention" && confirmed.id !== current?.id) {
    out.push(el(
      "section",
      { class: "sec" },
      el(
        "div",
        { class: "sec__head" },
        el("h2", { class: "sec__title" }, "前に確定していた計画"),
        el("span", { class: "sec__meta" }, `版 ${confirmed.version}`),
      ),
      el("p", { class: "note note--warn", style: "margin-bottom:24px" },
        "担当の辞退により、この計画はこのままでは実施できません。記録として残しています。"),
      agendaBar(confirmed.data, detail.data, detail.members, session.duration_minutes, { before: true }),
    ));
  }

  if (out.length === 0) {
    out.push(el(
      "section",
      { class: "sec" },
      el("div", { class: "sec__head" }, el("h2", { class: "sec__title" }, "今回の案")),
      el("p", { class: "empty" },
        el("strong", {}, "まだ案がありません"),
        "全員の参加条件が揃うと、AIが最初の案を作ります。"),
    ));
  }

  return out;
}

function diffBlock(confirmed, current) {
  if (!confirmed || confirmed.id === current.id) return null;
  const diff = planDiff(confirmed.data, current.data, detail.data, detail.members);
  if (!diff) return null;
  return el(
    "div",
    { style: "margin-top:40px" },
    el("p", { class: "seals__label" }, "前の計画から変わったところ"),
    diff,
  );
}

function factsSection() {
  const s = detail.session;
  const STATUS = { draft: "登録中", confirmed: "確定", needs_attention: "要調整" };

  const row = (key, value) => el("div", { class: "facts__row" },
    el("dt", {}, key), el("dd", {}, value));

  return el(
    "section",
    { class: "sec" },
    el(
      "div",
      { class: "sec__head" },
      el("h2", { class: "sec__title" }, "開催回"),
      el("span", { class: "sec__meta" }, `最終更新 ${formatDateTime(s.updated_at)}`),
    ),
    el(
      "dl",
      { class: "facts" },
      row("日時", formatDateTime(s.starts_at)),
      row("持ち時間", `${s.duration_minutes}分`),
      row("本", detail.data.book_title),
      row("今回の範囲", detail.data.target_section_ids
        .map((id) => detail.data.sections.find((x) => x.id === id)?.title ?? id).join("・")),
      row("状態", STATUS[s.status] ?? s.status),
    ),
  );
}

function membersSection() {
  return el(
    "section",
    { class: "sec" },
    el(
      "div",
      { class: "sec__head" },
      el("h2", { class: "sec__title" }, "みんなの回答"),
      el("span", { class: "sec__meta" }, "本人が共有を確認して送った内容です"),
    ),
    preparationRows(detail),
  );
}

function myActionsSection() {
  const p = detail.permissions;
  const buttons = [];

  if (p.can_update_preparation) {
    buttons.push(el("button", {
      type: "button", class: "btn",
      onClick: () => { mode = "prep"; drawPlanArea(); $plan.scrollIntoView({ block: "start" }); },
    }, "参加条件を出す・変える"));
  }
  if (p.can_withdraw_assignment) {
    buttons.push(el("button", {
      type: "button", class: "btn",
      onClick: () => withdraw("assignment", "今回の自分の担当をすべて辞退します。よろしいですか。"),
    }, "担当を辞退する"));
  }
  if (p.can_withdraw_attendance) {
    buttons.push(el("button", {
      type: "button", class: "btn",
      onClick: () => withdraw("attendance", "今回の欠席を登録します。よろしいですか。"),
    }, "欠席する"));
  }

  if (buttons.length === 0) return null;

  return el(
    "section",
    { class: "sec" },
    el("div", { class: "sec__head" }, el("h2", { class: "sec__title" }, "あなたの操作")),
    el("p", { class: "field__note", style: "margin-bottom:16px" }, "理由の入力は求めません。"),
    el("div", { class: "btns" }, ...buttons),
  );
}

function drawActivity() {
  if (!detail.permissions.can_view_activity) return mount($activity);

  if (activityRevision !== detail.session.revision) {
    activityRevision = detail.session.revision;
    client.activity(sessionId)
      .then((data) => { activity = data; if (detail.permissions.can_view_activity) mount($activity, renderActivity(activity)); })
      .catch(() => { /* 記録が取れなくても本体の表示は続ける */ });
  }
  if (activity) mount($activity, renderActivity(activity));
}

// ------------------------------------------------------------------
// 送信
// ------------------------------------------------------------------

async function respond(task, decision) {
  try {
    await client.respondToTask(task.id, {
      decision,
      proposal_id: task.proposal_id,
      proposal_version: task.proposal_version,
    });
    setStatus("受け付けました。反映まで少しかかります。");
    poller.refreshNow();
  } catch (err) {
    handleWriteError(err);
  }
}

async function submitPreparation(preparation) {
  await client.updatePreparation(sessionId, detail.session.revision, preparation);
  mode = "view";
  setStatus("参加条件を送りました。");
  poller.refreshNow();
  drawPlanArea();
}

async function submitProposal(data) {
  await client.submitProposal(sessionId, detail.session.revision, data);
  mode = "view";
  setStatus("代案を提出しました。担当者の引き受けと同意を集めます。");
  poller.refreshNow();
  drawPlanArea();
}

async function withdraw(scope, confirmText) {
  if (!window.confirm(confirmText)) return;
  try {
    await client.withdraw(sessionId, detail.session.revision, scope);
    setStatus("受け付けました。組み直しを始めます。");
    poller.refreshNow();
  } catch (err) {
    handleWriteError(err);
  }
}

/** 入力を消さずに、最新を確認してもらう（契約 第12節・モックの状態10）。 */
function handleWriteError(err) {
  if (err.isConflict) {
    setStatus("新しい案が出ています。最新の内容を確認してから、もう一度お願いします。", { warn: true });
    poller.refreshNow();
    return;
  }
  if (err.status === 401) return setStatus("ログインの期限が切れました。もう一度ログインしてください。", { warn: true });
  setStatus(err.message || "送信できませんでした。時間をおいて試してください。", { warn: true });
}
