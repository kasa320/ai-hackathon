// グループ詳細（/group.html?id=…）。
//
// グループ名・種別・メンバー・登録済みのブックを見せる。
// 種別は表示だけで変更できない。管理者はグループ名の変更とブックの追加ができる。
// 脱退・削除は、開催中かどうかの判断も含めて一覧（/）にある既存の操作のまま。

import { el, mount } from "./dom.js";
import { api, ApiError, createIdempotencyTracker } from "./api.js";
import { createBookForm } from "./bookForm.js";
import { formatPeriod } from "./features/reading/planning.js";
import { createDialog, renderTopbar, renderDevBar, pluginTag, playbookName, placeholder, flash, clearFlash, describeError } from "./ui.js";

const $ = (id) => document.getElementById(id);
const query = new URLSearchParams(location.search);
const groupId = query.get("id");
const loginUrl = api.loginUrl(location.pathname + location.search);

let group = null;
let books = null; // null: 取得失敗
let addPanel = null; // 開いているブック追加パネル
let submitting = false;

boot();

async function boot() {
  if (!groupId) {
    mount($("group"), placeholder("グループが指定されていません", "グループ一覧から開き直してください。"));
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
    mount($("group"), placeholder("グループを表示できません", "時間をおいて開き直してください。"));
    return;
  }
  renderTopbar($("topbar"), { me, current: "home" });
  await load();
  if (group && query.get("add_book") === "1" && isOwner()) openAddBook();
}

/** グループ本体とブック一覧を取り直す。ブックだけ失敗しても、グループの情報は出す。 */
async function load() {
  try {
    group = await api.group(groupId);
  } catch (err) {
    group = null;
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    const forbidden = err instanceof ApiError && (err.status === 403 || err.status === 404);
    mount(
      $("group"),
      forbidden
        ? placeholder("このグループは表示できません", "参加しているグループか、削除されていないかを確認してください。")
        : placeholder("グループを表示できません", el("span", {}, describeError(err), " ", el("button", { type: "button", class: "btn btn--quiet", onClick: load }, "再読み込み"))),
    );
    return;
  }
  await loadBooks();
  render();
}

async function loadBooks() {
  try {
    books = (await api.books(groupId)).items;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = loginUrl;
      return;
    }
    books = null;
  }
}

const currentMember = () => group.members.find((m) => m.id === group.current_member_id);
const isOwner = () => currentMember()?.role === "owner";

function render() {
  document.title = `${group.name} — Marunage`;
  const owner = isOwner();
  const addButton = el("button", { class: "btn", type: "button", id: "add-book", onClick: openAddBook }, "ブックを追加");
  const panel = el("div", { id: "add-book-panel", hidden: true });

  mount(
    $("group"),
    el(
      "div",
      { class: "page-head" },
      el("div", { class: "page-head__title" }, el("h1", {}, group.name), owner ? el("button", { class: "btn btn--quiet", type: "button", id: "rename", onClick: openRename }, "グループ名を変更") : null),
      el("p", {}, pluginTag(group.playbook_id, playbookName(group.playbook_id)), ` ${owner ? "あなたは管理者です" : "あなたはメンバーです"}`),
    ),
    el(
      "dl",
      { class: "dl" },
      el("dt", {}, "種別"),
      el("dd", {}, playbookName(group.playbook_id), el("small", { class: "help", style: "display:block;margin-top:2px" }, "種別はグループを作るときに決まり、変更できません。")),
      el("dt", {}, "メンバー"),
      el("dd", {}, `${group.members.length}人`),
    ),
    el("section", { class: "section" },
      el("div", { class: "section__head" }, el("h2", {}, "メンバー")),
      el("ul", { class: "member-list" }, group.members.map((m) =>
        el("li", {},
          el("span", { class: "person person--plain" }, m.display_name, m.id === group.current_member_id ? "（あなた）" : ""),
          el("small", {}, `${m.role === "owner" ? "管理者" : "メンバー"}${m.joined ? "" : "・招待中（未ログイン）"}`),
        ))),
      el("p", { class: "help" }, el("a", { class: "link", href: "/" }, "脱退・削除はグループ一覧から行えます。")),
    ),
    el("section", { class: "section" },
      el("div", { class: "section__head" }, el("h2", {}, "ブック"), owner ? addButton : null),
      panel,
      renderBooks(owner),
    ),
  );
}

function renderBooks(owner) {
  if (books === null) {
    return placeholder("ブックを取得できませんでした", el("button", { type: "button", class: "btn btn--quiet", onClick: async () => { await loadBooks(); render(); } }, "再読み込み"));
  }
  if (books.length === 0) {
    return placeholder("ブックはまだありません", owner ? "「ブックを追加」から、読む本を登録してください。" : "管理者がブックを登録すると、ここに出ます。");
  }
  return el("ul", { class: "book-cards" }, books.map((book) => {
    const done = book.status === "completed";
    const progress = Number.isInteger(book.planned_session_count) && Number.isInteger(book.completed_session_count)
      ? `全${book.planned_session_count}回中${book.completed_session_count}回完了`
      : null;
    return el("li", { class: "book-card" },
      el("div", { class: "book-card__head" },
        el("a", { class: "book-card__title", href: `/book.html?group_id=${encodeURIComponent(groupId)}&id=${encodeURIComponent(book.id)}` }, book.title),
        el("span", { class: "stamp", "data-tone": done ? "done" : null }, done ? "完了" : "進行中"),
      ),
      el("p", { class: "help" },
        [progress, book.period_start ? `期間 ${formatPeriod(book.period_start, book.period_end)}` : null].filter(Boolean).join("・") || "詳細を開くと、セッション計画を確認できます。"),
    );
  }));
}

// ---- グループ名の変更 --------------------------------------------------------

function openRename() {
  const input = el("input", { type: "text", value: group.name, autocomplete: "off" });
  createDialog({
    title: "グループ名を変更",
    body: el("div", {}, el("label", { class: "field" }, el("span", {}, "グループ名"), input), el("p", { class: "help" }, "種別は変更できません。")),
    submitLabel: "変更する",
    onSubmit: async ({ showError, close }) => {
      showError("");
      const name = input.value.trim();
      if (!name) return showError("グループ名を入れてください。");
      if (name === group.name) return close();
      try {
        await api.renameGroup(groupId, name);
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) {
          location.href = loginUrl;
          return;
        }
        return showError(err instanceof ApiError && err.status === 403 ? "グループ名を変更できるのは管理者だけです。" : describeError(err));
      }
      close();
      // 開いているブック追加パネルの入力を消さないよう、画面全体は組み直さず名前だけ差し替える
      group.name = name;
      document.title = `${name} — Marunage`;
      $("group").querySelector("h1").textContent = name;
      flash($("flash"), { title: "グループ名を変更しました" });
    },
  });
}

// ---- ブックの追加 ------------------------------------------------------------

function openAddBook() {
  const panel = $("add-book-panel");
  if (!panel || addPanel) return;
  const form = createBookForm({ client: api, groupId, legend: "追加するブック" });
  const tracker = createIdempotencyTracker();
  const error = el("div", { role: "alert" });
  const submit = el("button", { class: "btn", type: "button" }, "このブックを登録する");
  const cancel = el("button", { class: "btn btn--quiet", type: "button" }, "キャンセル");

  const close = () => {
    form.stop();
    addPanel = null;
    mount(panel);
    panel.hidden = true;
    $("add-book").disabled = false;
    $("add-book").focus();
  };
  cancel.addEventListener("click", () => { if (!submitting) close(); });
  submit.addEventListener("click", async () => {
    if (submitting) return;
    clearFlash($("flash"));
    mount(error);
    if (!form.validate()) return;
    submitting = true;
    form.setLocked(true);
    submit.disabled = cancel.disabled = true;
    submit.textContent = "登録しています…";
    const book = form.read();
    try {
      await api.createBook(groupId, book, { idempotencyKey: tracker.keyFor(book) });
      tracker.succeeded();
      submitting = false;
      close();
      await loadBooks();
      render();
      flash($("flash"), { title: `「${book.title}」を追加しました`, detail: "登録済みのブックは、この一覧から開けます。" });
    } catch (err) {
      tracker.failed(err);
      submitting = false;
      form.setLocked(false);
      submit.disabled = cancel.disabled = false;
      submit.textContent = "もう一度登録する";
      if (err instanceof ApiError && err.status === 401) {
        location.href = loginUrl;
        return;
      }
      mount(error, el("div", { class: "notice", "data-tone": "warn" },
        el("span", { class: "notice__mark", "aria-hidden": "true" }, "!"),
        el("span", {}, el("strong", {}, "ブックを登録できませんでした"), el("small", {}, err instanceof ApiError && err.status === 403 ? "ブックを追加できるのは管理者だけです。" : describeError(err)))));
    }
  });

  addPanel = { close };
  $("add-book").disabled = true;
  panel.hidden = false;
  mount(
    panel,
    el("div", { class: "book-entry" },
      el("div", { class: "book-entry__head" }, el("h3", {}, "ブックを追加")),
      form.node,
      error,
      el("div", { class: "submit" }, cancel, submit),
    ),
  );
  form.focus();
  panel.scrollIntoView({ block: "nearest" });
}
