// 会の詳細（/session.html?id=...）。
//
// この画面が守ること：
// - 確定可否や必要票数を自分で計算しない。permissions・task・proposal の値をそのまま使う
// - 更新は 202 を受けても確定扱いにせず、「受け付けました」と出して取り直す
// - 409 は入力を保ったまま最新状態を出し、再確認を求める
// - 停止しているときは動いているように見せない

import { el, mount, formatDateTime, remaining } from "./dom.js";
import { api, ApiError } from "./api.js";
import { createPoller } from "./poll.js";
import { featureFor } from "./features/index.js";
import {
  renderTopbar, renderPlanBar, planLegend, renderDevBar, pluginTag,
  placeholder, flash, clearFlash, reportMutationError, createDialog, tally, initial, receipt,
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
  approval: "この変更に同意するか答えてください",
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
  document.title = `${detail.session.title} — Quorum`;

  renderHead();
  renderHeadline();
  renderProposals();
  renderAnswers();
  renderAgent();
  renderNotifications();
  renderMyActions();
  renderSharedNote();
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
          `${formatDateTime(detail.session.starts_at)}・${detail.session.duration_minutes}分`),
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
  if (c?.status === "confirmed" || detail.session.status === "confirmed") return "計画は確定しました。いますることはありません";
  if (detail.session.status === "needs_attention") return "この回は組み直しが必要です";
  return "今は待っていて大丈夫です";
}

function headlineText(task) {
  if (task) {
    const left = remaining(task.due_at, detail.server_now);
    return `${task.title}　回答の期限は${formatDateTime(task.due_at)}（残り${left}）です。`;
  }
  const c = detail.active_case;
  if (c?.status === "needs_owner") {
    return `${REASON_LABEL[c.reason_code] ?? c.summary} 自動での再試行は予定していません。`;
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

  const approval = proposal?.approvals?.[0] ?? null;
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
    mount($("proposals"), el("div", { style: "margin-top:24px" }, placeholder("案はまだありません", "全員の参加条件が揃うと、エージェントが案を作ります。")));
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
      el("p", {}, proposal.summary),
      feature ? feature.renderScope(proposal.data, detail.data) : null,
      feature ? feature.renderAgendaTable(proposal.data, detail.data, detail.members) : null,
      renderConsent(proposal),
    ),
    void_
      ? el("div", { class: "proposal__foot" }, el("p", {}, "この案への同意と引き受けは、新しい案には引き継がれません。"))
      : null,
  );
}

/** 必要な同意と本人の引き受けを、別のものとして並べる。 */
function renderConsent(proposal) {
  const blocks = [];

  for (const approval of proposal.approvals ?? []) {
    const label = approval.kind === "owner" ? "管理者の承認" : "参加予定者の同意";
    blocks.push(
      el(
        "div",
        {},
        el("h3", {}, label),
        el("strong", {}, `${approval.eligible_member_ids.length}人のうち ${approval.approved_member_ids.length}人が同意しました`),
        el("small", {}, `必要な人数は ${approval.required_count}人です。返事のない人は同意として数えません。`),
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
        el("small", {}, "引き受けは本人だけができます。集団の同意では代われません。"),
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
  $("agent").parentElement.className = stopped ? "panel--stopped" : "";

  mount(
    $("agent"),
    el(
      "p",
      {},
      el("strong", {}, stopped ? "停止しました" : c ? CASE_LABEL[c.status] : "動いている調整はありません"),
      stopped
        ? `停止理由は「${REASON_LABEL[c.reason_code] ?? c.reason_code}」です。自動での再試行は予定していません。`
        : c?.next_retry_at
          ? `${formatDateTime(c.next_retry_at)}に再試行します。`
          : "回答が届くか期限になると再開します。",
    ),
  );
}

function renderNotifications() {
  const n = detail.notification_summary;
  const rows = [
    ["sent", n.sent_count],
    ["pending", n.pending_count],
    ["failed", n.failed_count],
    ["unknown", n.unknown_count],
  ].filter(([, count]) => count > 0);

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
      : el("p", {}, "まだ通知はありません。"),
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

  mount(
    $("my-actions"),
    buttons.length
      ? el("div", { style: "display:grid;gap:8px;margin-top:12px" }, buttons)
      : el("p", {}, "いま変更できることはありません。"),
  );
}

function renderSharedNote() {
  $("shared-note").textContent = feature
    ? "参加可否、準備できた範囲、説明できる範囲、説明できる時間の4項目を、この会の参加者に共有します。辞退の理由は記録も共有もしません。"
    : "共有されるのは構造化された回答だけです。";
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
    el("div", { class: "section__head" }, el("h2", {}, "実行記録")),
    el(
      "div",
      { class: "metrics" },
      metric(String(s.llm_call_count), "AIの呼び出し", "失敗した呼び出しも数えます。"),
      metric(String(s.tool_call_count), "ツールの実行", "案の作成や確認依頼の回数です。"),
      metric(cost, "費用", "分からない費用は0円として扱いません。"),
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
                el("td", {}, item.summary),
              ),
            ),
          ),
        )
      : el("p", { class: "help", style: "margin-top:16px" }, "まだ記録はありません。"),
  );
  $("activity").hidden = false;
}

function metric(value, label, note) {
  return el("div", { class: "metric" }, el("b", { class: "num" }, value), el("span", {}, label), el("small", {}, note));
}

// ---- 操作 ------------------------------------------------------------------

async function decide(task, decision) {
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
    poller.refreshNow();
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
  } finally {
    setBusy(false);
  }
}

async function withdraw(scope) {
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

/** 参加条件の入力。自由文で下書きを作れるが、保存は本人が確認してからになる。 */
function openPreparation() {
  const current = detail.preparations.find((p) => p.member_id === detail.current_member_id)?.value ?? null;
  const form = feature.createPreparationForm(detail.data, current, detail.session.duration_minutes);

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
      onClick: async () => {
        const value = text.value.trim();
        if (!value) return;
        mount(draftBox, receipt("読み取っています。保存はされません。"));
        try {
          const result = await api.interpretPreparation(sessionId, value);
          form.fill(result.preparation);
          mount(draftBox, feature.renderDraft(detail.data, result, detail.session.duration_minutes));
        } catch (err) {
          mount(draftBox, el("p", { class: "field__error" },
            err instanceof ApiError ? err.message : "読み取れませんでした。下の項目で直接答えてください。"));
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
        el("p", { class: "help" }, "読み取った内容は下の項目に入るだけで、保存はされません。あなたが確認して送信したものだけが記録されます。文章そのものは保存しません。"),
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
    submitLabel: "この内容を送信する",
    onSubmit: async ({ showError, showReceipt, close }) => {
      const preparation = form.read();
      const errors = feature.validate(preparation, detail.session.duration_minutes);
      if (errors.length) return showError(errors.join(" "));

      showError(null);
      showReceipt("まだ確定していません。最新の状態と照合しています。");
      try {
        const openPrep = detail.my_tasks.find((t) => t.kind === "preparation" && t.status === "open");
        if (openPrep) {
          await api.respondToTask(openPrep.id, {
            decision: "submit",
            expected_revision: detail.session.revision,
            preparation,
          });
        } else {
          await api.updatePreparation(sessionId, detail.session.revision, preparation);
        }
        close();
        flash($("flash"), { title: "回答を受け付けました", detail: "エージェントが内容を確認します。" });
        await refresh();
        poller.refreshNow();
      } catch (err) {
        // 入力は消さない。409 でも同じ内容のまま送り直せるようにする。
        showReceipt(null);
        await reportMutationError(err, { node: $("flash"), refresh, loginUrl });
        if (err instanceof ApiError && err.isConflict) {
          showError("状態が変わりました。最新の内容を確認して、もう一度送信してください。");
        }
      }
    },
  });
  return dialog;
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
