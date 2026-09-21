// 会の詳細（/session.html?id=...）。
//
// この画面が守ること：
// - 確定可否や必要票数を自分で計算しない。permissions・task・proposal の値をそのまま使う
// - 更新は 202 を受けても確定扱いにせず、「受け付けました」と出して取り直す
// - 409 は入力を保ったまま最新状態を出し、再確認を求める
// - 停止しているときは動いているように見せない

import { el, mount, formatDateTime, remaining } from "./dom.js";
import { api, ApiError, NetworkError } from "./api.js";
import { createPoller } from "./poll.js";
import { featureFor } from "./features/index.js";
import {
  renderTopbar, renderPlanBar, planLegend, renderDevBar, pluginTag,
  placeholder, flash, clearFlash, reportMutationError, createDialog, tally, initial, receipt, whenLabel,
} from "./ui.js";

const $ = (id) => document.getElementById(id);
const sessionId = new URLSearchParams(location.search).get("id");
const loginUrl = api.loginUrl(location.pathname + location.search);

const CASE_LABEL = {
  collecting: "参加条件を集めています",
  planning: "案を作っています",
  awaiting_consent: "同意を集めています",
  confirmed: "確定しました",
  needs_owner: "管理者の判断待ち",
};

const REASON_LABEL = {
  no_feasible_plan: "条件を満たす案を作れませんでした",
  deadline_expired: "回答の期限までに条件が揃いませんでした",
  model_error: "AIの処理が失敗しました",
  budget_exceeded: "AIの呼び出し回数の上限に達しました",
};

const TASK_TITLE = {
  preparation: "参加条件を答えてください",
  assignment: "担当を引き受けるか答えてください",
  approval: "この案に同意するか答えてください",
  owner_approval: "管理者として承認するか答えてください",
};

const NOTIFY_LABEL = {
  pending: ["", "送信待ち"],
  sent: ["done", "送信済み"],
  failed: ["warn", "失敗"],
  unknown: ["warn", "成否不明"],
};

let detail = null;
let me = null;
let feature = null;
let busy = false;
let poller = null;
// 直近に見た自分の回答。Discord など別の入口で変わったことに気づくために持つ。
let ownAnswer = null;
// 開いている参加条件のダイアログ。別の入口で更新されたらその場に知らせる。
let prepDialog = null;

boot();

async function boot() {
  if (!sessionId) {
    mount($("headline"), placeholder("会が指定されていません", "会の一覧から開き直してください。"));
    return;
  }

  await renderDevBar($("devbar"), api, { onChange: () => location.reload() });

  try {
    me = await api.me();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    throw err;
  }
  renderTopbar($("topbar"), { me, current: null });

  poller = createPoller({
    fetcher: () => api.session(sessionId),
    onData: (data) => {
      detail = data;
      render();
    },
    onError: (err, { fatal }) => {
      if (!fatal) return;
      if (err.status === 401) location.href = loginUrl;
      else mount($("headline"), placeholder("この会は表示できません", "アクセスできる会か確認してください。"));
    },
  });
  poller.start();
}

async function refresh() {
  detail = await api.session(sessionId);
  render();
}

function setBusy(on) {
  busy = on;
  render();
}

// ---- 描画 ------------------------------------------------------------------

function render() {
  if (!detail) return;
  feature = featureFor(detail.session.playbook_id);
  document.title = `${detail.session.title} — Marunage`;

  renderHead();
  renderHeadline();
  renderProposals();
  renderAnswers();
  renderAgent();
  renderNotifications();
  renderMyActions();
  renderAsideVisibility();
  watchOwnAnswer();
  if (detail.permissions.can_view_activity) loadActivity();
}

function renderHead() {
  mount(
    $("session-head"),
    el(
      "div",
      { class: "page-head", style: "margin-bottom:0" },
      el(
        "p",
        { style: "display:flex;align-items:center;gap:12px;margin-bottom:8px" },
        pluginTag(detail.session.playbook_id, feature?.name),
        el("span", { class: "num", style: "color:var(--ink-2);font-size:.84rem" },
          `${whenLabel(detail.session, formatDateTime)}・${detail.session.duration_minutes}分`),
      ),
      el("h1", {}, detail.session.title),
    ),
    el(
      "div",
      { class: "people-row", "aria-label": "参加者" },
      detail.members.map((m) =>
        el(
          "span",
          { class: "person person--plain" },
          m.display_name,
          m.joined ? null : el("small", { style: "color:var(--ink-3)" }, "（未参加）"),
        ),
      ),
    ),
  );
}

/** 一番上は「あなたが次にすること」。無ければ「待っていてよい」という結論。 */
function renderHeadline() {
  const task = detail.my_tasks.find((t) => t.status === "open");
  const proposal = detail.current_proposal ?? detail.confirmed_plan;
  const previous =
    detail.confirmed_plan && detail.current_proposal && detail.confirmed_plan.id !== detail.current_proposal.id
      ? detail.confirmed_plan
      : null;

  const items = feature && proposal ? feature.planItems(proposal.data, detail.members, detail.current_member_id) : [];
  const prevItems = feature && previous ? feature.planItems(previous.data, detail.members, detail.current_member_id) : null;

  const stamp = proposal
    ? [proposal.status === "confirmed" ? `確定案 ${proposal.version}` : `案 ${proposal.version}`,
       proposal.status === "confirmed" ? "done" : proposal.status === "pending" ? "open" : "void"]
    : ["案はまだありません", ""];

  mount(
    $("headline"),
    el(
      "div",
      { class: "headline__top" },
      el("span", { class: "stamp", "data-tone": stamp[1] || null }, stamp[0]),
      el("span", { class: "headline__state" }, detail.active_case ? CASE_LABEL[detail.active_case.status] : ""),
    ),
    el(
      "div",
      { class: "headline__body" },
      el("h2", { style: "font-size:clamp(1.3rem,3vw,1.7rem);font-weight:700;letter-spacing:-.02em;line-height:1.4" },
        task ? TASK_TITLE[task.kind] ?? "回答してください" : headlineTitle()),
      el("p", { class: "change", style: "margin-top:8px" }, headlineText(task)),
      items.length
        ? el("div", {}, renderPlanBar(items, { previous: prevItems, total: detail.session.duration_minutes }), planLegend())
        : null,
      renderActions(task, proposal),
    ),
  );
}

function headlineTitle() {
  const c = detail.active_case;
  if (c?.status === "needs_owner") {
    return detail.permissions.can_submit_proposal ? "実施できる代案を選んでください" : "管理者の判断を待っています";
  }
  if (c?.status === "confirmed" || detail.session.status === "confirmed") return "計画は確定しました";
  if (detail.session.status === "needs_attention") return "この回は組み直しが必要です";
  return "今は待っていて大丈夫です";
}

function headlineText(task) {
  if (task) {
    const left = remaining(task.due_at, detail.server_now);
    // タスク名は Discord 向けの回答例を含むので、Web では1行目の目的だけを出す
    return `${task.title.split("\n")[0]} 期限：${formatDateTime(task.due_at)}（残り${left}）`;
  }
  const c = detail.active_case;
  if (c?.status === "needs_owner") {
    return `${c.summary || REASON_LABEL[c.reason_code]} 自動での再試行は予定していません。`;
  }
  return c?.summary ?? "この会でいま動いている調整はありません。";
}

function renderActions(task, proposal) {
  const buttons = [];

  if (task) {
    for (const decision of task.allowed_decisions) {
      if (decision === "submit") {
        buttons.push(el("button", { class: "btn", type: "button", disabled: busy, onClick: openPreparation }, "参加条件を答える"));
      } else {
        const primary = decision === "accept" || decision === "approve";
        buttons.push(
          el(
            "button",
            {
              class: `btn${primary ? "" : " btn--quiet"}`,
              type: "button",
              disabled: busy,
              onClick: () => decide(task, decision),
            },
            DECISION_LABEL[decision],
          ),
        );
      }
    }
  }

  if (detail.permissions.can_submit_proposal) {
    buttons.push(el("button", { class: "btn", type: "button", disabled: busy, onClick: openProposalForm }, "代案を出す"));
  }

  // 自分のタスクに対応する承認の進み具合を出す。管理者の承認と全員の同意は別に数える
  const approvals = proposal?.approvals ?? [];
  const approval =
    (task?.kind === "owner_approval" ? approvals.find((a) => a.kind === "owner") : approvals.find((a) => a.kind !== "owner")) ??
    approvals[0] ??
    null;
  const progress = approval
    ? tally({
        done: approval.approved_member_ids.length,
        total: approval.eligible_member_ids.length,
        note: `同意 ${approval.approved_member_ids.length}／必要 ${approval.required_count}人`,
      })
    : null;

  if (!buttons.length && !progress) return null;
  return el("div", { class: "actions" }, buttons, progress);
}

const DECISION_LABEL = {
  accept: "引き受ける",
  decline: "引き受けられない",
  approve: "同意する",
  reject: "同意しない",
};

/** 案の中身。旧版・棄却された案も、そのstatusを付けて残す。 */
function renderProposals() {
  const list = [];
  if (detail.current_proposal) list.push(detail.current_proposal);
  if (detail.confirmed_plan && detail.confirmed_plan.id !== detail.current_proposal?.id) list.push(detail.confirmed_plan);

  if (!list.length) {
    const status = CASE_LABEL[detail.active_case?.status];
    mount($("proposals"), el("div", { style: "margin-top:24px" }, placeholder("案はまだありません", status ? `${status}。` : "")));
    return;
  }

  mount($("proposals"), list.map(renderProposal));
}

function renderProposal(proposal) {
  const void_ = proposal.status === "superseded" || proposal.status === "rejected";
  const tone = proposal.status === "confirmed" ? "done" : proposal.status === "pending" ? "open" : "void";
  const STATUS = { pending: "回答を集めています", confirmed: "確定済み", superseded: "古い案（無効）", rejected: "棄却されました" };

  return el(
    "article",
    { class: `proposal${void_ ? " proposal--void" : ""}` },
    el(
      "div",
      { class: "proposal__head" },
      el("h2", {}, `${proposal.change_kind === "initial" ? "初回計画" : "変更案"} ${proposal.version}`),
      el("span", { class: "stamp", "data-tone": tone }, STATUS[proposal.status] ?? proposal.status),
      el("time", {}, proposal.author === "agent" ? "エージェントの案" : "管理者の代案"),
    ),
    el(
      "div",
      { class: "proposal__body" },
      proposal.data.starts_at ? el("p", {}, `開催日時：${new Date(proposal.data.starts_at).toLocaleString("ja-JP", { timeZone: "Asia/Tokyo", year: "numeric", month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}（日本時間）・${detail.session.duration_minutes}分`) : null,
      el("p", {}, proposal.summary),
      feature ? feature.renderScope(proposal.data, detail.data) : null,
      feature ? feature.renderAgendaTable(proposal.data, detail.data, detail.members) : null,
      renderConsent(proposal),
    ),
  );
}

/** 必要な同意と本人の引き受けを、別のものとして並べる。 */
function renderConsent(proposal) {
  const blocks = [];

  for (const approval of proposal.approvals ?? []) {
    const label = approval.kind === "owner" ? "管理者の承認" : approval.kind === "all" ? "全員の参加可否・同意" : "参加予定者の同意";
    blocks.push(
      el(
        "div",
        {},
        el("h3", {}, label),
        el("strong", {}, `${approval.eligible_member_ids.length}人のうち ${approval.approved_member_ids.length}人が同意しました`),
        el("small", {}, `必要 ${approval.required_count}人`),
        el(
          "div",
          { class: "people" },
          approval.eligible_member_ids.map((id) =>
            el("i", { "data-on": approval.approved_member_ids.includes(id) ? "" : null }, initial(nameOf(id))),
          ),
        ),
      ),
    );
  }

  if (proposal.assignments?.length) {
    blocks.push(
      el(
        "div",
        {},
        el("h3", {}, "担当者の引き受け"),
        el("strong", {}, `${proposal.assignments.filter((a) => a.status === "accepted").length}／${proposal.assignments.length}人が引き受けました`),
        el(
          "div",
          { class: "people-row", style: "margin-top:8px" },
          proposal.assignments.map((a) =>
            el(
              "span",
              { class: "person", "data-tone": a.status === "accepted" ? "accepted" : a.status === "declined" ? "declined" : null },
              el("i", {}, initial(nameOf(a.member_id))),
              nameOf(a.member_id),
            ),
          ),
        ),
      ),
    );
  }

  return blocks.length ? el("div", { class: "consent" }, blocks) : null;
}

function nameOf(memberId) {
  return detail.members.find((m) => m.id === memberId)?.display_name ?? "不明";
}

function renderAnswers() {
  if (!feature) return mount($("answers"));
  mount(
    $("answers"),
    el("div", { class: "section__head" }, el("h2", {}, "参加条件")),
    feature.renderAnswers(detail.data, detail),
    el("div", { style: "margin-top:24px" }, feature.renderMaterial(detail.data)),
  );
}

function renderAgent() {
  const c = detail.active_case;
  const stopped = c?.status === "needs_owner";
  const section = $("agent-section");
  section.hidden = !stopped;
  section.className = stopped ? "panel--stopped" : "";
  if (!stopped) {
    mount($("agent"));
    return;
  }

  mount(
    $("agent"),
    el(
      "p",
      {},
      el("strong", {}, "調整を停止しました"),
      REASON_LABEL[c.reason_code] ?? c.summary,
    ),
  );
}

function renderNotifications() {
  const n = detail.notification_summary;
  const rows = [
    ["failed", n.failed_count],
    ["unknown", n.unknown_count],
  ].filter(([, count]) => count > 0);

  const section = $("notifications-section");
  section.hidden = rows.length === 0;
  if (!rows.length) {
    mount($("notifications"));
    return;
  }

  mount(
    $("notifications"),
    rows.length
      ? el(
          "div",
          { class: "notify" },
          rows.map(([status, count]) => {
            const [tone, label] = NOTIFY_LABEL[status];
            return el(
              "p",
              { "data-tone": tone || null },
              el("b", {}, count),
              el(
                "span",
                {},
                label,
                el("small", {}, status === "unknown" ? "届いていないとは限りません。Discord 側で確認してください。" : ""),
              ),
            );
          }),
        )
      : null,
  );
}

/** 依頼が無くてもできること（参加条件の更新・辞退）を、依頼とは分けて置く。 */
function renderMyActions() {
  const p = detail.permissions;
  const buttons = [];

  if (p.can_update_preparation) {
    buttons.push(el("button", { class: "btn btn--quiet", type: "button", disabled: busy, onClick: openPreparation }, "参加条件を変える"));
  }
  if (p.can_withdraw_assignment) {
    buttons.push(el("button", { class: "btn btn--quiet", type: "button", disabled: busy, onClick: () => withdraw("assignment") }, "担当を辞退する"));
  }
  if (p.can_withdraw_attendance) {
    buttons.push(el("button", { class: "btn btn--quiet", type: "button", disabled: busy, onClick: () => withdraw("attendance") }, "欠席と伝える"));
  }
  if (p.can_delete_session) {
    buttons.push(el("button", { class: "btn btn--quiet", type: "button", "data-tone": "warn", disabled: busy, onClick: deleteSession }, "この回を削除する"));
  }

  $("my-actions-section").hidden = buttons.length === 0;
  mount(
    $("my-actions"),
    buttons.length
      ? el("div", { style: "display:grid;gap:8px;margin-top:12px" }, buttons)
      : null,
  );
}

function renderAsideVisibility() {
  const aside = $("aside");
  aside.hidden = [...aside.children].every((section) => section.hidden);
  aside.parentElement.classList.toggle("split--single", aside.hidden);
}

/**
 * 自分の回答が別の入口（Discord の DM）で変わったことに気づかせる。
 * この画面からの送信は「受け付けました」で別に知らせているので、そのときは出さない。
 */
function watchOwnAnswer() {
  const value = detail.preparations.find((p) => p.member_id === detail.current_member_id)?.value ?? null;
  const next = JSON.stringify(value);
  if (ownAnswer === null) {
    ownAnswer = next;
    return;
  }
  if (next === ownAnswer) return;
  ownAnswer = next;
  if (busy) return;

  flash($("flash"), {
    title: "あなたの回答が別の入口で更新されました",
    detail: "Discord からの変更かもしれません。最新の内容に読み直しました。",
    tone: "warn",
  });
  prepDialog?.showError("別の入口で回答が更新されました。いったん閉じて開き直すと、最新の内容から直せます。");
}

async function loadActivity() {
  let data;
  try {
    data = await api.activity(sessionId);
  } catch {
    return;
  }
  const s = data.summary;
  const cost = s.costs.length
    ? s.costs.map((c) => (c.billed_amount ?? c.estimated_amount ?? "不明") + (c.currency === "unknown" ? "" : ` ${c.currency}`)).join("・")
    : "0回";

  mount(
    $("activity"),
    el(
      "details",
      { class: "activity-details", open: $("activity").querySelector("details")?.open ?? false },
      el("summary", {}, "実行記録"),
      el(
        "div",
        { class: "metrics" },
        metric(String(s.llm_call_count), "AIの呼び出し", "失敗した呼び出しも含む"),
        metric(String(s.tool_call_count), "ツールの実行", "案の作成と確認依頼"),
        metric(cost, "費用", "不明な費用は不明のまま表示"),
      ),
      data.items.length
        ? el(
            "table",
            { class: "ledger" },
            el("thead", {}, el("tr", {}, el("th", {}, "とき"), el("th", {}, "できごと"))),
            el(
              "tbody",
              {},
              data.items.map((item) =>
                el(
                  "tr",
                  {},
                  el("td", {}, el("time", { class: "num" }, formatDateTime(item.occurred_at))),
                  el("td", {}, activityEntry(item.summary)),
                ),
              ),
            ),
          )
        : el("p", { class: "help", style: "margin-top:16px" }, "まだ記録はありません。"),
    ),
  );
  $("activity").hidden = false;
}

/**
 * 記録1件の本文。参加条件の更新は「見出し＋項目の差分」で届くので、差分は行ごとに分ける。
 * 入口（Web / Discord）は本文から外して札にする。文字列はそのまま差し込まない（el が文字として扱う）。
 */
function activityEntry(summary) {
  const [head, ...rest] = String(summary ?? "").split("\n");
  // 「（Web）」「（変更なし・Discord）」の入口だけを外に出し、ほかの注記は本文に残す。
  const via = head.match(/（(?:([^（）]*)・)?(Web|Discord)）/);
  const text = via ? head.replace(via[0], via[1] ? `（${via[1]}）` : "") : head;
  const diff = rest.map((line) => line.replace(/^・/, "").trim()).filter(Boolean);

  return el(
    "div",
    { class: "entry" },
    el(
      "div",
      { class: "entry__head" },
      el("span", {}, text),
      via ? el("span", { class: "entry__via" }, via[2]) : null,
    ),
    diff.length ? el("ul", { class: "entry__diff" }, diff.map((line) => el("li", {}, line))) : null,
  );
}

function metric(value, label, note) {
  return el("div", { class: "metric" }, el("b", { class: "num" }, value), el("span", {}, label), el("small", {}, note));
}

// ---- 操作 ------------------------------------------------------------------

async function decide(task, decision) {
  if (busy) return;
  clearFlash($("flash"));
  setBusy(true);
  try {
    await api.respondToTask(task.id, {
      decision,
      proposal_id: task.proposal_id,
      proposal_version: task.proposal_version,
    });
    flash($("flash"), { title: "回答を受け付けました", detail: "まだ確定していません。最新の案と照合しています。" });
    await refresh();
    // 自分の回答で確定した場合は「まだ確定していません」を残さない
    if (detail.session.status === "confirmed" && detail.confirmed_plan?.id === task.proposal_id) {
      flash($("flash"), { title: "回答を受け付けました", detail: "全員の回答がそろい、計画が確定しました。" });
    }
    poller.refreshNow();
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
  } finally {
    setBusy(false);
  }
}

async function withdraw(scope) {
  if (busy) return;
  const label = scope === "assignment" ? "今回の担当をすべて辞退します。" : "今回は欠席と伝えます。";
  if (!confirm(`${label}\n理由は送られません。よろしいですか？`)) return;

  clearFlash($("flash"));
  setBusy(true);
  try {
    await api.withdraw(sessionId, detail.session.revision, scope);
    flash($("flash"), { title: "受け付けました", detail: "エージェントが代わりの案を検討します。" });
    await refresh();
    poller.refreshNow();
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
  } finally {
    setBusy(false);
  }
}

/** 開催回の削除。取り消せないので、回の名前を出して確認する。 */
async function deleteSession() {
  if (busy) return;
  const message = `「${detail.session.title}」を削除します。\n参加条件・案・実行記録も消え、元に戻せません。参加者への通知は送られません。よろしいですか？`;
  if (!confirm(message)) return;

  clearFlash($("flash"));
  setBusy(true);
  try {
    await api.deleteSession(sessionId);
    poller?.stop();
    location.href = "/";
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
    setBusy(false);
  }
}

/** 参加条件の入力。自由文で下書きを作れるが、保存は本人が確認してからになる。 */
function openPreparation() {
  if (busy || prepDialog?.dialog.isConnected) return;
  const snapshot = detail;
  const current = snapshot.preparations.find((p) => p.member_id === snapshot.current_member_id)?.value ?? null;
  const form = feature.createPreparationForm(snapshot.data, current, snapshot.session.duration_minutes, snapshot.session);

  const draftBox = el("div", {});
  const text = el("textarea", {
    placeholder: "例：参加します。今回の前半は読んできました。15分なら説明できます。",
    maxlength: "2000",
  });

  const interpret = el(
    "button",
    {
      type: "button",
      class: "btn btn--quiet",
      // AIの呼び出しはこの会の枠を1回使うので、返ってくるまで押せないようにする。
      onClick: async () => {
        const value = text.value.trim();
        if (!value || interpret.disabled) return;
        dialog.setPending(true, "読み取っています…");
        mount(draftBox, receipt("読み取っています。保存はされません。"));
        try {
          const result = await api.interpretPreparation(sessionId, value);
          dialog.setPending(false);
          form.fill(result.preparation);
          mount(draftBox, feature.renderDraft(snapshot.data, result, snapshot.session.duration_minutes));
        } catch (err) {
          mount(draftBox, el("p", { class: "field__error" }, interpretMessage(err)));
        } finally {
          dialog.setPending(false);
        }
      },
    },
    "文章から下書きを作る",
  );

  const body = el(
    "div",
    {},
    el(
      "details",
      {},
      el("summary", { style: "font-size:.84rem;color:var(--ink-2);cursor:pointer" }, "文章で書いて下書きを作る"),
      el(
        "div",
        { style: "margin-top:12px" },
        el("label", { class: "field" }, el("span", {}, "いまの状況を書いてください"), text),
        el("p", { class: "help" }, "入力欄を埋めるために使います。送信するまで保存されません。"),
        el("p", { style: "margin-top:12px" }, interpret),
        draftBox,
      ),
    ),
    el("hr", { style: "margin:24px 0;border:0;border-top:1px solid var(--line)" }),
    form.node,
  );

  const dialog = createDialog({
    title: "参加条件を答える",
    body,
    submitLabel: "回答を送る",
    onSubmit: async ({ showError, showReceipt, close }) => {
      const preparation = form.read();
      const errors = feature.validate(preparation, snapshot.session.duration_minutes);
      if (errors.length) return showError(errors.join(" "));

      showError(null);
      showReceipt("まだ確定していません。最新の状態と照合しています。");
      setBusy(true);
      try {
        const openPrep = snapshot.my_tasks.find((t) => t.kind === "preparation" && t.status === "open");
        if (openPrep) {
          await api.respondToTask(openPrep.id, {
            decision: "submit",
            expected_revision: snapshot.session.revision,
            preparation,
          });
        } else {
          await api.updatePreparation(sessionId, snapshot.session.revision, preparation);
        }
        close();
        flash($("flash"), { title: "回答を受け付けました", detail: "エージェントが内容を確認します。" });
        await refresh();
        poller.refreshNow();
      } catch (err) {
        showReceipt(null);
        showError(err.message ?? "送信できませんでした。通信を確認してもう一度お試しください。");
        await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
        if (err instanceof ApiError && err.isConflict) {
          showError("状態が変わりました。入力内容を控えてから、いったん閉じて開き直し、最新の内容を確認してください。");
        }
      } finally {
        setBusy(false);
      }
    },
  });
  prepDialog = dialog;
  dialog.dialog.addEventListener("close", () => {
    if (prepDialog === dialog) prepDialog = null;
  });
  return dialog;
}

/**
 * 文章の読み取りが失敗したときの案内。上限や扱えない依頼はサーバーの定型文をそのまま出す
 * （原文は含まれない）。どの場合も「下の項目で直接答える」道が残っていることを示す。
 */
function interpretMessage(err) {
  if (err instanceof NetworkError) {
    return "サーバーに接続できません。通信が戻ってからもう一度試すか、下の項目で直接答えてください。";
  }
  if (!(err instanceof ApiError)) {
    return "読み取れませんでした。下の項目で直接答えてください。";
  }
  if (err.status === 422) {
    return err.fieldErrors.map((f) => f.message).join(" ") || err.message;
  }
  return err.message;
}

/** 管理者の代案。案件が needs_owner のときだけ開く。 */
function openProposalForm() {
  const form = feature.createPlanForm(
    detail.data,
    detail.members,
    detail.session.duration_minutes,
    detail.confirmed_plan?.data ?? null,
    detail.current_member_id,
  );

  createDialog({
    title: "管理者の代案",
    body: form.node,
    submitLabel: "この案を出す",
    onSubmit: async ({ showError, showReceipt, close }) => {
      const plan = form.read();
      const errors = feature.validatePlan(plan, detail.data, detail.session.duration_minutes);
      if (errors.length) return showError(errors.join(" "));

      showError(null);
      showReceipt("担当者の引き受けと必要な同意を集めます。");
      try {
        await api.submitProposal(sessionId, detail.session.revision, plan);
        close();
        flash($("flash"), { title: "代案を受け付けました", detail: "担当者の引き受けと必要な同意を集めます。" });
        await refresh();
        poller.refreshNow();
      } catch (err) {
        showReceipt(null);
        await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
      }
    },
  });
}
