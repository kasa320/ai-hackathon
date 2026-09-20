// 会の登録（/setup.html）。
//
// 入力の順番を「形式 → 参加者 → 教材 → 次回」に固定する。形式を先に選ばせるのは、
// 共通の仕組みと用途固有の入力の境界を見せるため。
// 参加者は Discord ユーザーIDで招待する。IDを登録しただけでは権限は渡らず、
// 本人が Discord でログインした時点で所属が有効になる。

import { el, mount } from "./dom.js";
import { api, ApiError } from "./api.js";
import { featureFor } from "./features/index.js";
import { renderTopbar, renderDevBar, flash, clearFlash, reportMutationError, placeholder, pluginTag } from "./ui.js";

const $ = (id) => document.getElementById(id);
const loginUrl = api.loginUrl("/setup.html");

const STEPS = [
  ["01", "形式と参加者"],
  ["02", "教材と範囲"],
  ["03", "次回の日時"],
];

const state = {
  step: 0,
  playbooks: [],
  playbookId: "reading",
  group: null, // 作成済みのグループ（作り直さないよう保持する）
  groupName: "",
  invitees: [{ discord_user_id: "", display_name: "" }],
  book: { title: "", isbn: null },
  sections: [],
  tocSource: { kind: "manual", urls: [] },
  completed: new Set(),
  target: new Set(),
  title: "",
  startsAt: "",
  duration: 60,
};

let me = null;
let picker = null;

boot();

async function boot() {
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
  renderTopbar($("topbar"), { me, current: "setup" });

  try {
    state.playbooks = (await api.playbooks()).playbooks;
  } catch {
    state.playbooks = [{ id: "reading", name: "輪読" }];
  }
  render();
}

function go(step) {
  picker?.stop();
  picker = null;
  state.step = step;
  clearFlash($("flash"));
  render();
  window.scrollTo({ top: 0, behavior: "smooth" });
}

function render() {
  mount(
    $("steps"),
    STEPS.map(([no, label], i) =>
      el(
        "span",
        { style: i === state.step ? "background:var(--ink);color:#fff" : null },
        el("b", { style: i === state.step ? "color:rgba(255,255,255,.7)" : null }, no),
        label,
      ),
    ),
  );

  if (state.step === 0) renderGroupStep();
  else if (state.step === 1) renderMaterialStep();
  else renderSessionStep();
}

// ---- 01 形式と参加者 --------------------------------------------------------

function renderGroupStep() {
  const name = el("input", { type: "text", value: state.groupName, placeholder: "技術書輪読" });

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
            placeholder: "Discord ユーザーID（17〜20桁）",
            value: inv.discord_user_id,
            onInput: (e) => (state.invitees[i].discord_user_id = e.target.value.trim()),
          }),
          el("input", {
            type: "text",
            placeholder: "表示名（仮）",
            value: inv.display_name,
            onInput: (e) => (state.invitees[i].display_name = e.target.value),
          }),
          el(
            "button",
            {
              type: "button",
              "aria-label": "この行を消す",
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

  mount(
    $("form"),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "形式"),
      el(
        "div",
        { class: "checks" },
        state.playbooks.map((p) =>
          el(
            "label",
            {},
            el("input", {
              type: "radio",
              name: "playbook",
              value: p.id,
              checked: state.playbookId === p.id,
              onChange: () => (state.playbookId = p.id),
            }),
            el(
              "span",
              {},
              p.name,
              el("small", {}, p.id === "reading" ? "本を分担して読む会。教材の範囲と説明の担当を調整します。" : ""),
            ),
          ),
        ),
      ),
      el("p", { class: "help" }, "いま動くのは輪読だけです。共通の仕組み（参加者・案・同意・通知）は形式によらず同じです。"),
    ),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "参加者"),
      el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "会の名前"), name),
      el("p", { class: "help", style: "margin-top:16px" },
        "参加者の Discord ユーザーIDを登録します。IDを登録しただけでは、その人として操作する権限は渡りません。本人が Discord でログインした時点で参加が有効になります。"),
      el("p", { class: "help" },
        "IDは Discord の設定で開発者モードを有効にし、相手を右クリックして「ユーザーIDをコピー」で取れます。"),
      rows,
      el(
        "p",
        { style: "margin-top:8px" },
        el(
          "button",
          {
            type: "button",
            class: "btn btn--quiet",
            onClick: () => {
              state.invitees.push({ discord_user_id: "", display_name: "" });
              renderRows();
            },
          },
          "参加者を追加",
        ),
      ),
    ),
    el(
      "div",
      { class: "submit" },
      el("p", {}, state.group ? "この会は作成済みです。次へ進んで教材を入れてください。" : "あなたは管理者として登録されます。人数は自分を含めて2〜10人です。"),
      el(
        "button",
        {
          class: "btn",
          type: "button",
          onClick: async () => {
            state.groupName = name.value.trim();
            if (state.group) return go(1);
            await createGroup();
          },
        },
        state.group ? "次へ" : "この内容で会を作る",
      ),
    ),
  );
}

async function createGroup() {
  const invitees = state.invitees.filter((i) => i.discord_user_id);
  if (!state.groupName) return flash($("flash"), { title: "会の名前を入れてください", tone: "warn" });
  if (invitees.length === 0) return flash($("flash"), { title: "参加者を1人以上入れてください", tone: "warn" });

  clearFlash($("flash"));
  try {
    state.group = await api.createGroup({
      name: state.groupName,
      invitees: invitees.map((i) => ({ discord_user_id: i.discord_user_id, display_name: i.display_name || "（未設定）" })),
    });
    go(1);
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), loginUrl });
  }
}

// ---- 02 教材と範囲 ----------------------------------------------------------

function renderMaterialStep() {
  const feature = featureFor(state.playbookId);
  if (!feature) {
    mount($("form"), placeholder("この形式はまだ使えません", "輪読を選んでやり直してください。"));
    return;
  }

  const box = el("div", {});
  const title = el("input", { type: "text", value: state.book.title, placeholder: "サンプル技術書" });

  const renderSections = () => {
    if (state.sections.length === 0) {
      mount(
        box,
        el("p", { class: "help" }, "目次を取得するか、手入力で範囲を足してください。"),
        sectionEditor(renderSections),
      );
      return;
    }
    mount(
      box,
      el("p", { class: "help" }, "今回扱う範囲と、前回までに読んだ範囲を選びます。どちらでもない範囲は将来の回に残ります。"),
      el(
        "div",
        { class: "toc-list" },
        state.sections.map((s) =>
          el(
            "div",
            { style: "display:flex;align-items:center;gap:12px;font-size:.86rem" },
            el("span", { style: "flex:1;min-width:0" }, s.title),
            el(
              "label",
              { style: "display:inline-flex;gap:6px;color:var(--ink-2);font-size:.78rem" },
              el("input", {
                type: "checkbox",
                checked: state.completed.has(s.id),
                onChange: (e) => {
                  e.target.checked ? state.completed.add(s.id) : state.completed.delete(s.id);
                  if (e.target.checked) state.target.delete(s.id);
                  renderSections();
                },
              }),
              "前回まで",
            ),
            el(
              "label",
              { style: "display:inline-flex;gap:6px;color:var(--ink-2);font-size:.78rem" },
              el("input", {
                type: "checkbox",
                checked: state.target.has(s.id),
                onChange: (e) => {
                  e.target.checked ? state.target.add(s.id) : state.target.delete(s.id);
                  if (e.target.checked) state.completed.delete(s.id);
                  renderSections();
                },
              }),
              "今回",
            ),
          ),
        ),
      ),
      sectionEditor(renderSections),
    );
  };

  const tocBox = el("div", {});
  picker = feature.createTocPicker(api, state.group.id, {
    onPick: (sections, source, book) => {
      state.sections = sections;
      state.tocSource = source;
      if (book?.title && !title.value) {
        title.value = book.title;
        state.book.title = book.title;
      }
      state.book.isbn = book?.isbn ?? null;
      state.completed = new Set();
      state.target = new Set(sections.map((s) => s.id));
      mount(tocBox, el("p", { class: "help" }, "目次を取り込みました。下で範囲を選んでください。"));
      renderSections();
    },
    onManual: () => {
      state.tocSource = { kind: "manual", urls: [] };
      mount(tocBox, el("p", { class: "help" }, "手入力にしました。下で範囲を足してください。"));
      renderSections();
    },
  });
  mount(tocBox, picker.node);
  renderSections();

  mount(
    $("form"),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "本"),
      el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "書名"), title),
      el("div", { style: "margin-top:24px" }, tocBox),
    ),
    el("fieldset", { class: "fieldset" }, el("legend", {}, "範囲"), el("div", { style: "margin-top:16px" }, box)),
    el(
      "div",
      { class: "submit" },
      el("p", {}, "取得した目次は候補です。ここで確認して直したものだけが記録されます。"),
      el("button", { class: "btn btn--quiet", type: "button", onClick: () => go(0) }, "戻る"),
      el(
        "button",
        {
          class: "btn",
          type: "button",
          onClick: () => {
            state.book.title = title.value.trim();
            if (!state.book.title) return flash($("flash"), { title: "書名を入れてください", tone: "warn" });
            if (state.target.size === 0) return flash($("flash"), { title: "今回扱う範囲を1つ以上選んでください", tone: "warn" });
            go(2);
          },
        },
        "次へ",
      ),
    ),
  );
}

function sectionEditor(rerender) {
  const input = el("input", { type: "text", placeholder: "範囲の名前（例：第2章 設計）" });
  return el(
    "div",
    { class: "repeater__row", style: "margin-top:12px" },
    input,
    el(
      "button",
      {
        type: "button",
        "aria-label": "範囲を足す",
        onClick: () => {
          if (!input.value.trim()) return;
          state.sections.push({ id: `sec_${state.sections.length + 1}`, title: input.value.trim() });
          state.target.add(state.sections[state.sections.length - 1].id);
          input.value = "";
          rerender();
        },
      },
      "＋",
    ),
  );
}

// ---- 03 次回の日時 ----------------------------------------------------------

function renderSessionStep() {
  const title = el("input", { type: "text", value: state.title, placeholder: "第1回" });
  const when = el("input", { type: "datetime-local", value: state.startsAt });
  const duration = el("input", { type: "number", min: "15", max: "180", step: "5", value: String(state.duration) });

  mount(
    $("form"),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "次回"),
      el(
        "div",
        { class: "grid grid--2" },
        el("label", { class: "field" }, el("span", {}, "回の名前"), title),
        el("label", { class: "field" }, el("span", {}, "持ち時間（15〜180分）"), duration),
      ),
      el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "開始日時"), when),
      el("p", { class: "help" }, "登録できるのは1時間より先の日時です。登録すると、参加者全員に参加条件の確認が送られます。"),
    ),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "登録する内容"),
      el(
        "table",
        { class: "table" },
        el("tr", {}, el("th", {}, "形式"), el("td", {}, pluginTag(state.playbookId, featureFor(state.playbookId)?.name))),
        el("tr", {}, el("th", {}, "会"), el("td", {}, state.groupName)),
        el("tr", {}, el("th", {}, "参加者"), el("td", {}, `あなたを含めて ${state.invitees.filter((i) => i.discord_user_id).length + 1}人`)),
        el("tr", {}, el("th", {}, "本"), el("td", {}, state.book.title)),
        el("tr", {}, el("th", {}, "今回の範囲"), el("td", {}, sectionNames(state.target))),
        el("tr", {}, el("th", {}, "前回まで"), el("td", {}, state.completed.size ? sectionNames(state.completed) : "なし")),
        el("tr", {}, el("th", {}, "目次の出どころ"), el("td", {}, { web: "Webから取得", image: "画像から読み取り", manual: "手入力" }[state.tocSource.kind])),
      ),
    ),
    el(
      "div",
      { class: "submit" },
      el("p", {}, "登録した時点では計画はまだありません。全員の参加条件が揃うと、エージェントが最初の案を作ります。"),
      el("button", { class: "btn btn--quiet", type: "button", onClick: () => go(1) }, "戻る"),
      el(
        "button",
        {
          class: "btn",
          type: "button",
          onClick: () => {
            state.title = title.value.trim();
            state.startsAt = when.value;
            state.duration = Number(duration.value);
            createSession();
          },
        },
        "この内容で登録する",
      ),
    ),
  );
}

function sectionNames(ids) {
  return state.sections.filter((s) => ids.has(s.id)).map((s) => s.title).join("、") || "—";
}

async function createSession() {
  if (!state.title) return flash($("flash"), { title: "回の名前を入れてください", tone: "warn" });
  if (!state.startsAt) return flash($("flash"), { title: "開始日時を入れてください", tone: "warn" });

  // datetime-local はタイムゾーンを持たないので、ブラウザーの時刻として送る。
  const startsAt = new Date(state.startsAt);
  if (Number.isNaN(startsAt.getTime())) return flash($("flash"), { title: "開始日時の形式が正しくありません", tone: "warn" });

  clearFlash($("flash"));
  try {
    const created = await api.createSession(state.group.id, {
      playbook_id: state.playbookId,
      title: state.title,
      starts_at: startsAt.toISOString(),
      duration_minutes: state.duration,
      data: {
        book_title: state.book.title,
        isbn: state.book.isbn,
        toc_source: state.tocSource,
        sections: state.sections,
        completed_section_ids: state.sections.filter((s) => state.completed.has(s.id)).map((s) => s.id),
        target_section_ids: state.sections.filter((s) => state.target.has(s.id)).map((s) => s.id),
      },
    });
    location.href = `/session.html?id=${encodeURIComponent(created.session.id)}`;
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), loginUrl });
  }
}
