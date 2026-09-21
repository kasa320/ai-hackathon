// トップ画面。
//
// ログイン後にここが最大にするのは「会の一覧」ではなく、あなたが返事をする1件です。
// 何がどう変わるのかを、詳細画面へ進む前に同じ面で読めるようにします。
// 返事をする会がなければ、待っていてよいことを先に伝えます。

import { el, mount, formatDateTime } from "./dom.js";
import { api, ApiError } from "./api.js";
import { featureFor } from "./features/index.js";
import { createDialog, renderTopbar, renderPlanBar, renderDevBar, pluginTag, placeholder, tally, whenLabel } from "./ui.js";

const $ = (id) => document.getElementById(id);

const TASK_LABEL = {
  preparation: "参加条件の回答",
  assignment: "担当の引き受け",
  approval: "変更への同意",
  owner_approval: "管理者の承認",
};

const STATE_LABEL = {
  draft: "調整中",
  confirmed: "確定",
  needs_attention: "要調整",
};

const CASE_LABEL = {
  collecting: "参加条件を集めています",
  planning: "案を作っています",
  awaiting_consent: "同意を集めています",
  confirmed: "確定しました",
  needs_owner: "管理者の判断待ち",
};

boot();

async function boot() {
  await renderDevBar($("devbar"), api, { onChange: () => location.reload() });

  let me = null;
  try {
    me = await api.me();
  } catch (err) {
    if (!(err instanceof ApiError) || err.status !== 401) console.error(err);
  }

  renderTopbar($("topbar"), { me, current: "home" });

  if (!me) {
    $("hero").hidden = false;
    $("login").href = api.loginUrl("/");
    $("guest").hidden = false;
    return;
  }

  document.body.dataset.auth = "in";
  $("member").hidden = false;
  await renderMember();
}

async function renderMember() {
  let groups;
  try {
    groups = await api.groups();
  } catch (err) {
    mount($("member"), placeholder("会の一覧を取得できませんでした", "時間をおいて開き直してください。"));
    return;
  }

  if (groups.items.length === 0) {
    mount(
      $("member"),
      el("div", { class: "page-head" }, el("h1", {}, "会")),
      placeholder(
        "まだ会がありません",
        el(
          "span",
          {},
          "会を作って、参加者の Discord ユーザーIDを登録すると、ここに出ます。",
          el("p", { style: "margin-top:16px" }, el("a", { class: "btn", href: "/setup.html" }, "会を登録する")),
        ),
      ),
    );
    return;
  }

  // すべてのグループの開催回を集め、日付の近い順に並べる。
  const sessions = [];
  for (const group of groups.items) {
    try {
      const list = await api.sessions(group.id);
      for (const s of list.items) sessions.push({ ...s, group });
    } catch (err) {
      console.error(err);
    }
  }
  sessions.sort((a, b) => new Date(a.starts_at) - new Date(b.starts_at));

  // 詳細は最大8件まで取る（MVPの件数なら全部に届く）。
  const details = new Map();
  for (const s of sessions.slice(0, 8)) {
    try {
      details.set(s.id, await api.session(s.id));
    } catch (err) {
      console.error(err);
    }
  }

  // 一番上に置くのは「あなたが返事をする1件」。
  // 依頼が無ければ直近の会を出す。確定済みでも、何がどうなったかを帯で読めるようにする。
  let headline = null;
  for (const s of sessions) {
    const detail = details.get(s.id);
    const open = detail?.my_tasks.find((t) => t.status === "open");
    if (open) {
      headline = { session: s, detail, task: open };
      break;
    }
  }
  if (!headline) {
    const now = Date.now();
    const upcoming = sessions.find((s) => details.has(s.id) && new Date(s.starts_at) >= now)
      ?? [...sessions].reverse().find((s) => details.has(s.id));
    if (upcoming) headline = { session: upcoming, detail: details.get(upcoming.id), task: null };
  }

  mount(
    $("member"),
    headline ? renderHeadline(headline) : nothingToDo(),
    renderList(sessions, details, headline?.session.id),
    headline ? renderAgentPanels(headline.detail) : null,
    renderGroups(groups.items, sessions),
  );
}

function renderGroups(groups, sessions) {
  const now = Date.now();
  return el(
    "section",
    { class: "section" },
    el("div", { class: "section__head" }, el("h2", {}, "参加中のグループ")),
    el(
      "div",
      { class: "group-list" },
      groups.map((group) => {
        const running = sessions.some((session) => session.group_id === group.id
          && session.schedule_status === "confirmed"
          && now >= new Date(session.starts_at).getTime()
          && now < new Date(session.starts_at).getTime() + session.duration_minutes * 60000);
        const owner = group.role === "owner";
        const action = el(
          "button",
          {
            type: "button",
            class: owner ? "btn btn--danger" : "btn btn--quiet",
            disabled: running,
            title: running ? "開催中の回が終了してから操作できます" : null,
            onClick: () => owner ? openDeleteGroup(group) : openLeaveGroup(group),
          },
          owner ? "グループを削除" : "グループを脱退",
        );
        return el(
          "div",
          { class: "group-row" },
          el("div", { class: "group-row__main" }, el("strong", {}, group.name), el("small", {}, `${owner ? "管理者" : "メンバー"}・${group.member_count}人`)),
          el("div", { class: "group-row__action" }, action, running ? el("small", {}, "開催中は操作できません") : null),
        );
      }),
    ),
  );
}

function openLeaveGroup(group) {
  createDialog({
    title: `${group.name}から脱退しますか`,
    body: el(
      "div",
      {},
      el("p", {}, "開始前の開催回は欠席扱いとなり、残るメンバーで再調整されます。"),
      el("p", { class: "help" }, "脱退後は、このグループの過去の開催回も閲覧できません。残るメンバーには個人DMで知らせます。"),
    ),
    submitLabel: "脱退する",
    onSubmit: async ({ showError, close }) => {
      showError("");
      try {
        await api.leaveGroup(group.id);
        close();
        await renderMember();
      } catch (err) {
        showError(err instanceof ApiError ? err.message : "脱退を完了できませんでした。時間をおいてお試しください。");
      }
    },
  });
}

function openDeleteGroup(group) {
  let modal;
  modal = createDialog({
    title: `${group.name}を削除しますか`,
    body: el(
      "div",
      {},
      el("p", {}, "メンバー全員がこのグループと開催回を閲覧できなくなります。削除のお知らせは全員の個人DMへ送ります。"),
      el("p", { class: "help" }, "保存済みデータは論理削除として保持されます。"),
    ),
    submitLabel: "削除する",
    onSubmit: async ({ showError, close }) => {
      showError("");
      try {
        await api.deleteGroup(group.id);
        close();
        await renderMember();
      } catch (err) {
        showError(err instanceof ApiError ? err.message : "削除を完了できませんでした。時間をおいてお試しください。");
      }
    },
  });
  modal.dialog.querySelector('button[type="submit"]').classList.add("btn--danger");
}

/** 返事をする1件。進行表と「何がどう変わるか」を同じ面に置く。 */
function renderHeadline({ session, detail, task }) {
  const feature = featureFor(session.playbook_id);
  const proposal = detail.current_proposal ?? detail.confirmed_plan;
  const previous = detail.confirmed_plan && detail.current_proposal && detail.confirmed_plan.id !== detail.current_proposal.id
    ? detail.confirmed_plan
    : null;

  const items = feature && proposal ? feature.planItems(proposal.data, detail.members, detail.current_member_id) : [];
  const prevItems = feature && previous ? feature.planItems(previous.data, detail.members, detail.current_member_id) : null;

  const approval = proposal?.approvals?.[0] ?? null;

  return el(
    "section",
    { class: "headline" },
    el(
      "div",
      { class: "headline__top" },
      pluginTag(session.playbook_id, feature?.name),
      el("span", { class: "headline__when num" }, whenLabel(session, formatDateTime)),
      el("span", { class: "headline__state" }, detail.active_case ? CASE_LABEL[detail.active_case.status] : STATE_LABEL[session.status]),
    ),
    el(
      "div",
      { class: "headline__body" },
      el("h1", {}, session.title),
      el("p", { class: "headline__sub" }, feature ? feature.summary(detail.data) : session.group.name),
      items.length ? renderPlanBar(items, { previous: prevItems, total: session.duration_minutes }) : null,
      el(
        "p",
        { class: "change" },
        task ? taskSentence(task, proposal) : detail.active_case?.summary ?? "いまあなたにできることはありません。",
      ),
      el(
        "div",
        { class: "actions" },
        el("a", { class: "btn", href: `/session.html?id=${encodeURIComponent(session.id)}` }, task ? "内容を見て回答する" : "会の詳細を見る"),
        approval
          ? tally({
              done: approval.approved_member_ids.length,
              total: approval.eligible_member_ids.length,
              note: `同意 ${approval.approved_member_ids.length}/${approval.required_count}　必要 ${approval.required_count}人`,
            })
          : null,
      ),
    ),
  );
}

function taskSentence(task, proposal) {
  const label = TASK_LABEL[task.kind] ?? "回答";
  if (task.kind === "approval" || task.kind === "owner_approval") {
    return `${label}をお願いします。${proposal ? `案 ${proposal.version} の内容を確認してください。` : ""}期限は${formatDateTime(task.due_at)}です。`;
  }
  if (task.kind === "assignment") {
    return `${label}をお願いします。引き受けるのはこの版のあなたの担当すべてです。期限は${formatDateTime(task.due_at)}です。`;
  }
  return `${label}をお願いします。期限は${formatDateTime(task.due_at)}です。`;
}

function nothingToDo() {
  return el(
    "section",
    { class: "headline" },
    el("div", { class: "headline__top" }, el("span", { class: "stamp" }, "依頼なし"), el("span", { class: "headline__state" }, "待っていて大丈夫です")),
    el(
      "div",
      { class: "headline__body" },
      el("h1", {}, "いまあなたが答えることはありません"),
      el("p", { class: "headline__sub" }, "回答が必要になったら通知します。"),
    ),
  );
}

function renderList(sessions, details, excludeId) {
  const rows = sessions.filter((s) => s.id !== excludeId);
  return el(
    "section",
    { class: "section" },
    el(
      "div",
      { class: "section__head" },
      el("h2", {}, "ほかの会"),
      el("a", { class: "link", href: "/setup.html" }, "会を登録する"),
    ),
    rows.length
      ? el("div", { class: "rows" }, rows.map((s) => renderRow(s, details.get(s.id))))
      : placeholder("ほかの会はありません", "登録すると、ここに近い順で並びます。"),
  );
}

function renderRow(session, detail) {
  const feature = featureFor(session.playbook_id);
  const openTask = detail?.my_tasks.find((t) => t.status === "open");
  const notify = detail?.notification_summary;
  const unknown = (notify?.unknown_count ?? 0) + (notify?.failed_count ?? 0);

  let state = STATE_LABEL[session.status] ?? session.status;
  let tone = session.status === "confirmed" ? "done" : null;
  if (unknown > 0) {
    state = "通知の成否不明";
    tone = "warn";
  }
  if (openTask) {
    state = "要回答";
    tone = "attention";
  }

  return el(
    "a",
    { class: "row", href: `/session.html?id=${encodeURIComponent(session.id)}` },
    session.schedule_status === "proposed"
      ? el("span", { class: "row__date" }, "調整中")
      : el("time", { class: "row__date num", datetime: session.starts_at }, shortDate(session.starts_at)),
    el(
      "span",
      { class: "row__main" },
      el("span", { class: "row__title" }, session.title, pluginTag(session.playbook_id, feature?.name)),
      el("span", { class: "row__meta" }, rowMeta(session, detail, openTask)),
    ),
    el("span", { class: "row__state", "data-tone": tone }, state),
  );
}

function rowMeta(session, detail, openTask) {
  if (!detail) return session.group?.name ?? "";
  if (openTask) return `${TASK_LABEL[openTask.kind] ?? "回答"}の期限は${formatDateTime(openTask.due_at)}です。`;
  if (detail.active_case) return detail.active_case.summary;
  return session.group?.name ?? "";
}

function shortDate(iso) {
  const d = new Date(iso);
  const week = ["日", "月", "火", "水", "木", "金", "土"][d.getDay()];
  return `${d.getMonth() + 1}/${d.getDate()} ${week}`;
}

/** エージェントが止まっていないか、通知が届いたかを同じ視界に置く。 */
function renderAgentPanels(detail) {
  const n = detail.notification_summary;
  const c = detail.active_case;
  const stopped = c?.status === "needs_owner";

  const problems = [];
  if (stopped) {
    problems.push(
      el(
        "article",
        { class: "panel panel--stopped" },
        el("h3", {}, "調整を停止しました"),
        el("strong", {}, c.summary),
        el("p", {}, "管理者が代案を出すか、参加条件が変わると再開します。"),
      ),
    );
  }
  if (n.failed_count + n.unknown_count > 0) {
    problems.push(
      el(
        "article",
        { class: "panel panel--stopped" },
        el("h3", {}, "通知を確認してください"),
        el("strong", { class: "num" }, `失敗 ${n.failed_count}件・成否不明 ${n.unknown_count}件`),
        el("p", {}, "成否不明は、届いていないとは限りません。"),
      ),
    );
  }
  if (!problems.length) return null;

  return el(
    "section",
    { class: "section" },
    el("div", { class: "section__head" }, el("h2", {}, "確認が必要です")),
    el("div", { class: "panels" }, problems),
  );
}
