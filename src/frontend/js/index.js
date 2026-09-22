// トップ画面。
//
// ログイン後にここが最大にするのは「会の一覧」ではなく、あなたが返事をする1件です。
// 何がどう変わるのかを、詳細画面へ進む前に同じ面で読めるようにします。
// 返事をする会がなければ、待っていてよいことを先に伝えます。

import { el, mount, formatDateTime, memberName } from "./dom.js";
import { api, ApiError } from "./api.js";
import { featureFor } from "./features/index.js";
import { createDialog, renderTopbar, renderPlanBar, renderDevBar, pluginTag, playbookName, placeholder, tally, whenLabel } from "./ui.js";
import { availabilityStatus } from "./weeklyAvailability.js";
import {
  ASSIGNMENT_KIND, SCHEDULING_KIND, assignmentState, schedulingState,
  slotAwaitsMyConfirmation, slotStatusLabel, formatPeriod,
} from "./features/reading/planning.js";

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
    if (err instanceof ApiError && err.status === 401) {
      location.href = api.loginUrl("/");
      return;
    }
    mount($("member"), placeholder("グループ一覧を取得できませんでした", "時間をおいて開き直してください。"));
    return;
  }

  if (groups.items.length === 0) {
    mount(
      $("member"),
      el("div", { class: "page-head" }, el("h1", {}, "グループ")),
      placeholder(
        "まだグループがありません",
        el(
          "span",
          {},
          "グループを作って、参加者の Discord ユーザーIDを登録すると、ここに出ます。",
          el("p", { style: "margin-top:16px" }, el("a", { class: "btn", href: "/setup.html" }, "グループを作る")),
        ),
      ),
    );
    return;
  }

  // すべてのグループの開催回を集め、日付の近い順に並べる。
  const sessions = [];
  await mapLimited(groups.items, FETCH_CONCURRENCY, async (group) => {
    try {
      const list = await api.sessions(group.id);
      for (const s of list.items) sessions.push({ ...s, group });
    } catch (err) {
      console.error(err);
    }
  });
  sessions.sort((a, b) => new Date(a.starts_at) - new Date(b.starts_at));

  // 詳細は全件取る（黙って一部だけに絞らない）。同時fetchは絞る。
  const details = new Map();
  await mapLimited(sessions, FETCH_CONCURRENCY, async (s) => {
    try {
      details.set(s.id, await api.session(s.id));
    } catch (err) {
      console.error(err);
    }
  });

  // 全所属グループの未完了book slot（実セッション作成前を含む）を状態別に集める。
  // メンバーIDはグループごとに別なので、グループごとの current_member_id を使う。
  const bookSlots = await loadIncompleteBookSlots(groups.items);

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
    availabilityNotice(),
    headline ? renderHeadline(headline) : nothingToDo(),
    renderIncompleteBookSlots(bookSlots),
    renderList(sessions, details, headline?.session.id, bookSlots.coveredSessionIds),
    headline ? renderAgentPanels(headline.detail) : null,
    renderGroups(groups.items, sessions),
  );
}

const FETCH_CONCURRENCY = 4;

/** 同時fetchの数を絞りつつ、件数を黙って切り詰めない。 */
async function mapLimited(items, limit, fn) {
  const results = new Array(items.length);
  let next = 0;
  async function worker() {
    while (next < items.length) {
      const idx = next++;
      results[idx] = await fn(items[idx], idx);
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}

/** 全所属グループの、完了していないbook slotを集める。取得できなかった箇所は空一覧に見せず記録する。 */
async function loadIncompleteBookSlots(groups) {
  const rows = [];
  const failedGroups = [];
  const failedBooks = [];
  const coveredSessionIds = new Set();

  await mapLimited(groups, FETCH_CONCURRENCY, async (group) => {
    let books, groupDetail;
    try {
      [books, groupDetail] = await Promise.all([
        api.books(group.id).then((r) => r.items),
        api.group(group.id).catch(() => null),
      ]);
    } catch (err) {
      console.error(err);
      failedGroups.push(group);
      return;
    }
    const members = groupDetail?.members ?? [];
    await mapLimited(books, FETCH_CONCURRENCY, async (book) => {
      let bookDetail;
      try {
        bookDetail = await api.book(group.id, book.id);
      } catch (err) {
        console.error(err);
        failedBooks.push({ group, book });
        return;
      }
      const sections = new Map(bookDetail.book.sections.map((s) => [s.id, s.title]));
      for (const slot of bookDetail.sessions) {
        if (slot.session) coveredSessionIds.add(slot.session.id);
        if (slot.status === "completed") continue;
        rows.push({ group, members, book: bookDetail.book, sections, slot, bucket: slotBucket(slot, group.current_member_id) });
      }
    });
  });

  return { rows, failedGroups, failedBooks, coveredSessionIds };
}

const BUCKET_ORDER = ["mine", "attention", "scheduling", "confirmed", "waiting"];
const BUCKET_LABEL = {
  mine: "自分の回答待ち",
  attention: "要確認・管理者判断待ち",
  scheduling: "日程調整中",
  confirmed: "日時確定・実施待ち",
  waiting: "調整開始待ち",
};

/** 固定の優先順で1つのバケツに分ける（重複表示しない）。 */
function slotBucket(slot, me) {
  const assignment = assignmentState(slot);
  const scheduling = schedulingState(slot);
  const myTurn = (!!me && slot.assignee_member_id === me && assignment.kind === ASSIGNMENT_KIND.unanswered)
    || (!!me && slot.proposed_assignee_member_id === me && slot.assignment_status === "change_proposed")
    || slotAwaitsMyConfirmation(slot, me);
  if (myTurn) return "mine";
  if (assignment.kind === ASSIGNMENT_KIND.unknown
    || assignment.kind === ASSIGNMENT_KIND.changeRequested
    || assignment.kind === ASSIGNMENT_KIND.replacementPending
    || scheduling.kind === SCHEDULING_KIND.unknown
    || scheduling.kind === SCHEDULING_KIND.needsAttention
    || slot.assignee_confirmation_status === "needs_owner") {
    return "attention";
  }
  if (scheduling.kind === SCHEDULING_KIND.collecting || scheduling.kind === SCHEDULING_KIND.proposing) return "scheduling";
  if (scheduling.kind === SCHEDULING_KIND.confirmed) return "confirmed";
  return "waiting";
}

/** ホーム最上部の1件の下に、全グループの未完了book slotを状態別に一覧する。 */
function renderIncompleteBookSlots({ rows, failedGroups, failedBooks }) {
  const buckets = new Map(BUCKET_ORDER.map((k) => [k, []]));
  for (const row of rows) buckets.get(row.bucket).push(row);
  for (const list of buckets.values()) {
    list.sort((a, b) => (a.slot.period_start ?? "").localeCompare(b.slot.period_start ?? ""));
  }
  const hasRows = rows.length > 0;
  const hasFailures = failedGroups.length > 0 || failedBooks.length > 0;
  if (!hasRows && !hasFailures) return null;

  const failureNames = [
    ...failedGroups.map((g) => `${g.name}のブック一覧`),
    ...failedBooks.map(({ group, book }) => `${group.name}の${book.title}`),
  ];

  return el(
    "section",
    { class: "section" },
    el("div", { class: "section__head" }, el("h2", {}, "未完了のブック枠")),
    hasFailures
      ? el(
          "div",
          { class: "notice", "data-tone": "warn", role: "status" },
          el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
          el("span", {}, el("strong", {}, "一部を取得できませんでした"), el("small", {}, `${failureNames.join("、")}は表示できていません。`)),
        )
      : null,
    hasRows
      ? el("div", { class: "book-slot-buckets" }, BUCKET_ORDER.filter((k) => buckets.get(k).length).map((k) => renderBookSlotBucket(k, buckets.get(k))))
      : (hasFailures ? null : placeholder("未完了のブック枠はありません", "ブックを登録すると、ここに並びます。")),
  );
}

function renderBookSlotBucket(key, rows) {
  return el(
    "div",
    { class: "book-slot-bucket" },
    el("h3", {}, BUCKET_LABEL[key], el("span", { class: "stamp" }, String(rows.length))),
    el("div", { class: "rows" }, rows.map((row) => renderBookSlotRow(row))),
  );
}

function renderBookSlotRow({ group, members, book, sections, slot }) {
  const assignment = assignmentState(slot);
  const scheduling = schedulingState(slot);
  const href = slot.session
    ? `/session.html?id=${encodeURIComponent(slot.session.id)}`
    : `/book.html?group_id=${encodeURIComponent(group.id)}&id=${encodeURIComponent(book.id)}`;
  const when = slot.session?.schedule_status === "confirmed" && slot.session.starts_at
    ? formatDateTime(slot.session.starts_at)
    : formatPeriod(slot.period_start, slot.period_end);
  const assignee = slot.assignee_member_id
    ? `${memberName(members, slot.assignee_member_id)}${slot.assignee_member_id === group.current_member_id ? "（あなた）" : ""}`
    : "未定";
  const targets = (slot.target_section_ids ?? slot.covered_section_ids ?? []).map((id) => sections.get(id) ?? id).join("、") || "未定";
  const warn = assignment.kind === ASSIGNMENT_KIND.unknown || scheduling.kind === SCHEDULING_KIND.unknown;

  return el(
    "a",
    { class: "row", href },
    el("span", { class: "row__date" }, when),
    el(
      "span",
      { class: "row__main" },
      el("span", { class: "row__title" }, `${group.name}・${book.title}・第${slot.sequence_number}回`),
      el("span", { class: "row__meta" }, `対象章：${targets}／担当：${assignee}／${slotStatusLabel(slot.status)}`),
    ),
    el("span", { class: "row__state", "data-tone": warn ? "warn" : null }, `${assignment.label}／${scheduling.label}`),
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
            title: running ? "開催中のセッションが終了してから操作できます" : null,
            onClick: () => owner ? openDeleteGroup(group) : openLeaveGroup(group),
          },
          owner ? "グループを削除" : "グループを脱退",
        );
        const groupUrl = `/group.html?id=${encodeURIComponent(group.id)}`;
        return el(
          "div",
          { class: "group-row" },
          el(
            "div",
            { class: "group-row__main" },
            el(
              "div",
              { class: "group-row__title" },
              el("a", { class: "group-row__name", href: groupUrl }, group.name),
              pluginTag(group.playbook_id, playbookName(group.playbook_id)),
            ),
            el("small", {}, `${owner ? "管理者" : "メンバー"}・${group.member_count}人`),
            bookLinks(group, groupUrl),
          ),
          el(
            "div",
            { class: "group-row__action" },
            el("a", { class: "btn btn--quiet", href: groupUrl }, "詳細"),
            owner ? el("a", { class: "btn btn--quiet", href: `${groupUrl}&add_book=1` }, "ブックを追加") : null,
            action,
            running ? el("small", {}, "セッション中は操作できません") : null,
          ),
        );
      }),
    ),
  );
}

const BOOKS_SHOWN = 3;

/** グループの進行中のブック。複数あっても、先頭の数冊だけ出して残りは詳細へ誘導する。 */
function bookLinks(group, groupUrl) {
  const node = el("div", { class: "group-books", "aria-live": "polite" }, "ブックを読み込み中…");
  api.books(group.id).then(({ items }) => {
    if (items.length === 0) {
      mount(node, el("span", {}, "ブックはまだありません"));
      return;
    }
    const active = items.filter((book) => book.status !== "completed");
    const shown = [...active, ...items.filter((book) => book.status === "completed")].slice(0, BOOKS_SHOWN);
    mount(
      node,
      el(
        "ul",
        {},
        shown.map((book) => el(
          "li",
          {},
          el("a", { class: "link", href: `/book.html?group_id=${encodeURIComponent(group.id)}&id=${encodeURIComponent(book.id)}` }, book.title),
          el("span", { class: "group-books__state" }, book.status === "completed" ? "完了" : "進行中"),
        )),
      ),
      items.length > shown.length
        ? el("a", { class: "link", href: groupUrl }, `ほか${items.length - shown.length}冊を見る`)
        : null,
    );
  }).catch((err) => {
    if (err instanceof ApiError && err.status === 401) {
      location.href = api.loginUrl("/");
      return;
    }
    mount(node, el("span", {}, "ブックを取得できませんでした"));
  });
  return node;
}

/** 普段の空き時間が未登録・再確認が必要なときの案内。取得できなくてもホームの表示は止めない。 */
function availabilityNotice() {
  const node = el("div", { class: "availability-notice", hidden: true });
  api.weeklyAvailability().then((data) => {
    const status = availabilityStatus(data);
    if (status.kind === "ok") return;
    const stale = status.kind === "stale";
    mount(
      node,
      el(
        "div",
        { class: "notice", "data-tone": "warn", role: "status" },
        el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
        el(
          "span",
          {},
          el("strong", {}, stale ? "普段の空き時間を確認してください" : "普段の空き時間が未登録です"),
          el("small", {}, stale
            ? `最後の更新から${status.days}日たっています。いまも合っているか確かめてください。`
            : "登録すると、日程調整で候補を出しやすくなります。"),
        ),
        el("a", { class: "btn btn--quiet", style: "margin-left:auto", href: "/weekly.html" }, stale ? "確認する" : "登録する"),
      ),
    );
    node.hidden = false;
  }).catch((err) => console.error(err));
  return node;
}

function openLeaveGroup(group) {
  createDialog({
    title: `${group.name}から脱退しますか`,
    body: el(
      "div",
      {},
      el("p", {}, "開始前のセッションは欠席扱いとなり、残るメンバーで再調整されます。"),
      el("p", { class: "help" }, "脱退後は、このグループの過去のセッションも閲覧できません。残るメンバーには個人DMで知らせます。"),
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
      el("p", { }, "メンバー全員がこのグループとセッションを閲覧できなくなります。削除のお知らせは全員の個人DMへ送ります。"),
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

  const approval = proposal?.approvals?.find((item) => item.kind !== "owner") ?? null;

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
      items.length ? renderPlanBar(items, { previous: prevItems, total: session.duration_minutes, animationKey: `${proposal.id}:${proposal.version}` }) : null,
      el(
        "p",
        { class: "change" },
        task ? taskSentence(task, proposal) : detail.active_case?.summary ?? "いまあなたにできることはありません。",
      ),
      el(
        "div",
        { class: "actions" },
        el("a", { class: "btn", href: `/session.html?id=${encodeURIComponent(session.id)}` }, task ? "内容を見て回答する" : "セッション詳細を見る"),
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

function renderList(sessions, details, excludeId, coveredSessionIds) {
  // ブック由来のセッションは、上の「未完了のブック枠」ですでに出しているので、ここでは重複させない。
  const rows = sessions.filter((s) => s.id !== excludeId && !coveredSessionIds?.has(s.id));
  return el(
    "section",
    { class: "section" },
    el(
      "div",
      { class: "section__head" },
      el("h2", {}, "ほかのセッション"),
    ),
    rows.length
      ? el("div", { class: "rows" }, rows.map((s) => renderRow(s, details.get(s.id))))
      : placeholder("ほかのセッションはありません", "ブックを登録すると、ここに近い順で並びます。"),
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
