// グループの登録（/setup.html）。
//
// 入力の順番は「グループ → ブック」。ブックの目次取得はグループに紐づくため、
// 先にグループを作り、そのあとで1冊以上のブックを1冊ずつ登録する。
// ブックの登録が途中で失敗しても、作成済みのグループと登録済みのブックは残す。
// 失敗したブックだけを、内容を直して、または直さずに再試行できる。
// 参加者は Discord ユーザーIDで招待する。IDを登録しただけでは権限は渡らず、
// 本人が Discord でログインした時点で所属が有効になる。

import { el, mount } from "./dom.js";
import { api, ApiError, createIdempotencyTracker } from "./api.js";
import { createBookForm } from "./bookForm.js";
import { renderTopbar, renderDevBar, flash, clearFlash, reportMutationError, playbookName, describeError } from "./ui.js";

const $ = (id) => document.getElementById(id);
const loginUrl = api.loginUrl("/setup.html");

const STEPS = [
  ["01", "グループ"],
  ["02", "ブック"],
];

// 現時点で選べる種別は輪読だけ。会議・遊びは選択肢に出さない。
const PLAYBOOK_ID = "reading";

const state = {
  step: 0,
  group: null, // 作成済みのグループ（作り直さないよう保持する）
  groupName: "",
  invitees: [{ discord_user_id: "", display_name: "" }],
  entries: [], // ブックの入力欄。登録の進み具合もここで持つ
};

let submitting = false;
let entrySeq = 0;

boot();

async function boot() {
  await renderDevBar($("devbar"), api, { onChange: () => location.reload() });
  let me;
  try {
    me = await api.me();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    throw err;
  }
  renderTopbar($("topbar"), { me, current: "setup" });
  render();
}

function go(step) {
  state.step = step;
  clearFlash($("flash"));
  render();
  window.scrollTo({ top: 0 });
  $("form").querySelector("input:not(:disabled), button:not(:disabled)")?.focus({ preventScroll: true });
}

function render() {
  mount(
    $("steps"),
    STEPS.map(([no, label], i) =>
      el(
        "span",
        { style: i === state.step ? "background:var(--ink);color:#fff" : null, "aria-current": i === state.step ? "step" : null },
        el("b", { style: i === state.step ? "color:rgba(255,255,255,.7)" : null }, no),
        label,
      ),
    ),
  );
  if (state.step === 0) renderGroupStep();
  else renderBooksStep();
}

// ---- 01 グループ ------------------------------------------------------------

function renderGroupStep() {
  const locked = !!state.group;
  const name = el("input", { type: "text", value: state.groupName, placeholder: "技術書輪読", disabled: locked, onInput: (e) => { state.groupName = e.target.value; } });

  const rows = el("div", { class: "repeater" });
  const renderRows = () => {
    mount(
      rows,
      state.invitees.map((inv, i) =>
        el(
          "div",
          { class: "repeater__row" },
          el("input", {
            type: "text",
            inputmode: "numeric",
            disabled: locked,
            placeholder: "Discord ユーザーID（17〜20桁）",
            "aria-label": `参加者${i + 1}のDiscord ユーザーID`,
            value: inv.discord_user_id,
            onInput: (e) => (state.invitees[i].discord_user_id = e.target.value.trim()),
          }),
          el("input", {
            type: "text",
            placeholder: "表示名（仮）",
            "aria-label": `参加者${i + 1}の表示名`,
            disabled: locked,
            value: inv.display_name,
            onInput: (e) => (state.invitees[i].display_name = e.target.value),
          }),
          el(
            "button",
            {
              type: "button",
              disabled: locked,
              "aria-label": `参加者${i + 1}の行を消す`,
              onClick: () => {
                state.invitees.splice(i, 1);
                if (state.invitees.length === 0) state.invitees.push({ discord_user_id: "", display_name: "" });
                renderRows();
              },
            },
            "×",
          ),
        ),
      ),
    );
  };
  renderRows();

  const next = el("button", { class: "btn", type: "button" }, "次へ");
  next.addEventListener("click", async () => {
    if (submitting) return;
    state.groupName = name.value.trim();
    if (state.group) return go(1);
    await createGroup(next);
  });

  mount(
    $("form"),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "グループ"),
      el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "グループ名"), name),
      el(
        "div",
        { style: "margin-top:20px" },
        el("span", { class: "field-label" }, "種別"),
        el(
          "div",
          { class: "choice-cards", role: "radiogroup", "aria-label": "グループの種別" },
          el(
            "label",
            { class: "choice-card" },
            el("input", { type: "radio", name: "playbook", value: PLAYBOOK_ID, checked: true, disabled: locked }),
            el("span", {}, el("strong", {}, playbookName(PLAYBOOK_ID)), el("small", {}, "本を分担して読み、開催日を調整します。")),
          ),
        ),
        el("p", { class: "help" }, "種別はグループを作るときだけ決められ、あとから変更できません。現在選べるのは輪読だけです。"),
      ),
    ),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "参加者"),
      state.group ? el("p", { class: "help" }, "このグループの参加者は登録済みです。続けてブックを登録してください。") : null,
      el(
        "details",
        { class: "form-details" },
        el("summary", {}, "Discord IDの確認方法"),
        el("p", {}, "Discordの設定で開発者モードを有効にし、相手を右クリックして「ユーザーIDをコピー」を選びます。"),
      ),
      rows,
      el(
        "p",
        { style: "margin-top:8px" },
        el(
          "button",
          {
            type: "button",
            class: "btn btn--quiet",
            disabled: locked,
            onClick: () => {
              state.invitees.push({ discord_user_id: "", display_name: "" });
              renderRows();
              rows.querySelector(".repeater__row:last-child input")?.focus();
            },
          },
          "参加者を追加",
        ),
      ),
    ),
    el("div", { class: "submit" }, next),
  );
}

async function createGroup(button) {
  const invitees = state.invitees.filter((i) => i.discord_user_id);
  if (!state.groupName) return flash($("flash"), { title: "グループ名を入れてください", tone: "warn" });
  if (invitees.length === 0) return flash($("flash"), { title: "参加者を1人以上入れてください", tone: "warn" });

  clearFlash($("flash"));
  submitting = true;
  button.disabled = true;
  button.textContent = "登録しています…";
  try {
    state.group = await api.createGroup({
      name: state.groupName,
      playbookId: PLAYBOOK_ID,
      invitees: invitees.map((i) => ({ discord_user_id: i.discord_user_id, display_name: i.display_name || "（未設定）" })),
    });
    state.entries = [newEntry()];
    submitting = false;
    go(1);
  } catch (err) {
    submitting = false;
    button.disabled = false;
    button.textContent = "次へ";
    await reportMutationError(err, { node: $("flash"), loginUrl });
  }
}

// ---- 02 ブック --------------------------------------------------------------

function newEntry() {
  return {
    uid: ++entrySeq,
    form: createBookForm({ client: api, groupId: state.group.id, legend: "ブック" }),
    status: "draft", // draft | saving | done | failed
    error: "",
    bookId: null,
    idempotency: createIdempotencyTracker(), // 通信が不確かな失敗のあと、同じ操作として再送する
  };
}

const STATUS_TEXT = { draft: "未登録", saving: "登録中", done: "登録済み", failed: "登録できませんでした" };

const groupHref = () => `/group.html?id=${encodeURIComponent(state.group.id)}`;
const bookHref = (bookId) => `/book.html?group_id=${encodeURIComponent(state.group.id)}&id=${encodeURIComponent(bookId)}`;

function renderBooksStep() {
  const list = el("div", { class: "book-entries" });
  const results = el("div", { class: "book-results", "aria-live": "polite" });
  const add = el("button", { class: "btn btn--quiet", type: "button", id: "add-entry" }, "＋ 別のブックを追加");
  const submit = el("button", { class: "btn", type: "button", id: "submit-books" }, "ブックを登録する");

  add.addEventListener("click", () => {
    if (submitting) return;
    const entry = newEntry();
    state.entries.push(entry);
    renderEntries();
    entry.form.focus();
  });
  submit.addEventListener("click", () => submitBooks());

  mount(
    $("form"),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "最初に登録するブック"),
      el("p", { class: "help" }, `「${state.groupName}」を作成しました。ブックは1冊ずつ登録します。あとからグループの画面でも追加できます。`),
      list,
      el("p", { style: "margin-top:16px" }, add),
    ),
    results,
    el("div", { class: "submit" }, el("a", { class: "btn btn--quiet", href: groupHref() }, "あとで登録する（グループを開く）"), submit),
  );
  renderEntries();

  function renderEntries() {
    mount(list, state.entries.map((entry, i) => entryCard(entry, i, renderEntries)));
    refreshControls();
  }
}

function entryCard(entry, index, rerender) {
  const remove = el("button", {
    type: "button", class: "btn btn--quiet", "data-role": "remove",
    "aria-label": `ブック${index + 1}を削除`,
    onClick: () => {
      if (submitting) return;
      entry.form.stop();
      state.entries = state.entries.filter((e) => e !== entry);
      rerender();
      document.getElementById("add-entry")?.focus();
    },
  }, "削除");
  const status = el("span", { class: "stamp", "data-role": "status" });
  const result = el("div", { class: "book-entry__result", "data-role": "result", role: "status" });

  const card = el(
    "section",
    { class: "book-entry", "data-uid": String(entry.uid), "aria-label": `ブック${index + 1}` },
    el("div", { class: "book-entry__head" }, el("h2", {}, `ブック${index + 1}`), status, remove),
    entry.form.node,
    result,
  );
  entry.card = card;
  paintEntry(entry);
  return card;
}

/** 1冊分の登録状況を表示に反映する。入力欄そのものは作り直さない。 */
function paintEntry(entry) {
  const card = entry.card;
  if (!card) return;
  const stamp = card.querySelector('[data-role="status"]');
  stamp.textContent = STATUS_TEXT[entry.status];
  stamp.dataset.tone = entry.status === "done" ? "done" : entry.status === "failed" ? "warn" : "";
  const result = card.querySelector('[data-role="result"]');
  mount(
    result,
    entry.status === "done"
      ? el("p", { class: "book-entry__ok" }, "登録しました。", entry.bookId ? el("a", { class: "link", href: bookHref(entry.bookId) }, "ブックを開く") : null)
      : null,
    entry.status === "failed"
      ? el(
          "div",
          { class: "notice", "data-tone": "warn" },
          el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
          el("span", {}, el("strong", {}, "このブックは登録できていません"), el("small", {}, entry.error)),
          el("button", { type: "button", class: "btn btn--quiet", style: "margin-left:auto", onClick: () => submitBooks(entry) }, "このブックだけ再試行"),
        )
      : null,
  );
  card.dataset.status = entry.status;
}

/** 追加・削除・登録ボタンの有効状態と、まとめの文言。 */
function refreshControls() {
  const remaining = state.entries.filter((e) => e.status !== "done");
  const submit = $("submit-books");
  const add = $("add-entry");
  if (submit) {
    submit.disabled = submitting || remaining.length === 0;
    const failed = state.entries.some((e) => e.status === "failed");
    submit.textContent = submitting ? "登録しています…" : failed ? "失敗したブックを再試行" : state.entries.length > 1 ? `${remaining.length}冊のブックを登録する` : "ブックを登録する";
  }
  if (add) add.disabled = submitting;
  for (const entry of state.entries) {
    const remove = entry.card?.querySelector('[data-role="remove"]');
    if (remove) remove.hidden = state.entries.length < 2 || entry.status === "done";
    if (remove) remove.disabled = submitting;
  }
}

/**
 * 未登録のブックを1冊ずつ送る。only を渡すとその1冊だけ。
 * 先に全件の入力を確かめ、1件でも不備があれば何も送らない。
 */
async function submitBooks(only = null) {
  if (submitting) return;
  const targets = only ? [only] : state.entries.filter((e) => e.status !== "done");
  if (targets.length === 0) return;

  for (const entry of targets) {
    if (!entry.form.validate()) {
      entry.card?.scrollIntoView({ block: "nearest" });
      flash($("flash"), { title: "入力を確認してください", detail: "不備のあるブックは、まだ送っていません。", tone: "warn" });
      return;
    }
  }

  clearFlash($("flash"));
  submitting = true;
  for (const entry of targets) entry.form.setLocked(true);
  refreshControls();

  for (const entry of targets) {
    entry.status = "saving";
    entry.error = "";
    paintEntry(entry);

    const book = entry.form.read();
    const idempotencyKey = entry.idempotency.keyFor(book);

    try {
      const created = await api.createBook(state.group.id, book, { idempotencyKey });
      entry.status = "done";
      entry.bookId = created?.book?.id ?? null;
      entry.idempotency.succeeded();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        location.href = loginUrl;
        return;
      }
      entry.status = "failed";
      entry.error = describeError(err);
      entry.idempotency.failed(err);
    }
    paintEntry(entry);
  }

  submitting = false;
  for (const entry of targets) entry.form.setLocked(entry.status === "done");
  refreshControls();
  renderSummary();
}

function renderSummary() {
  const done = state.entries.filter((e) => e.status === "done");
  const failed = state.entries.filter((e) => e.status === "failed");
  const box = document.querySelector(".book-results");
  if (!box) return;

  if (failed.length === 0) {
    mount(
      box,
      el(
        "div",
        { class: "notice" },
        el("span", { class: "notice__mark", "aria-hidden": "true" }, "✓"),
        el("span", {}, el("strong", {}, `グループと${done.length}冊のブックを登録しました`), el("small", {}, "各ブックの担当と日程は、ブックの画面で確認できます。")),
        el("a", { class: "btn", style: "margin-left:auto", href: groupHref() }, "グループを開く"),
      ),
    );
    return;
  }

  mount(
    box,
    el(
      "div",
      { class: "notice", "data-tone": "warn" },
      el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
      el(
        "span",
        {},
        el("strong", {}, `${state.entries.length}冊中${done.length}冊を登録しました。${failed.length}冊が未登録です`),
        el("small", {}, "グループと登録済みのブックはそのまま残っています。失敗したブックは、内容を直してから再試行できます。"),
      ),
      el("a", { class: "btn btn--quiet", style: "margin-left:auto", href: groupHref() }, "グループを開く"),
    ),
  );
}
