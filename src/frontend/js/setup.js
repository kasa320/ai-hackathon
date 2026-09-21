// 会の登録（/setup.html）。
//
// 入力の順番を「参加者 → 教材 → 次回」に固定する。
// 現在使える用途は輪読だけなので、形式は選ばせない。
// 参加者は Discord ユーザーIDで招待する。IDを登録しただけでは権限は渡らず、
// 本人が Discord でログインした時点で所属が有効になる。

import { el, mount } from "./dom.js";
import { api, ApiError } from "./api.js";
import { featureFor } from "./features/index.js";
import { renderTopbar, renderDevBar, flash, clearFlash, reportMutationError, placeholder, DATE_MAX } from "./ui.js";

const $ = (id) => document.getElementById(id);
const loginUrl = api.loginUrl("/setup.html");

const STEPS = [
  ["01", "参加者"],
  ["02", "本と範囲"],
  ["03", "初回セッション"],
];

const state = {
  step: 0,
  playbookId: "reading",
  group: null, // 作成済みのグループ（作り直さないよう保持する）
  groupName: "",
  invitees: [{ discord_user_id: "", display_name: "" }],
  book: { title: "", isbn: null },
  sections: [],
  tocSource: { kind: "manual", urls: [] },
  title: "",
  periodStart: "",
  periodEnd: "",
  duration: 60,
  plannedSessionCount: 8,
  sessionCreationMode: "sequential",
};

let me = null;
let picker = null;
let submitting = false;

function lockForm(button, label) {
  submitting = true;
  const controls = [...$("form").querySelectorAll("input, select, button")].map(node => [node, node.disabled]);
  const text = button.textContent;
  for (const [node] of controls) node.disabled = true;
  button.textContent = label;
  $("form").setAttribute("aria-busy", "true");
  return () => {
    submitting = false;
    for (const [node, disabled] of controls) if (node.isConnected) node.disabled = disabled;
    button.textContent = text;
    $("form").removeAttribute("aria-busy");
  };
}

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

  render();
}

function go(step) {
  picker?.stop();
  picker = null;
  state.step = step;
  clearFlash($("flash"));
  render();
  window.scrollTo({ top: 0, behavior: "smooth" });
  $("form").querySelector("input:not(:disabled), button:not(:disabled)")?.focus({ preventScroll: true });
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
  const name = el("input", { type: "text", value: state.groupName, placeholder: "技術書輪読", disabled: !!state.group, onInput: e => { state.groupName = e.target.value; } });

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
            disabled: !!state.group,
            placeholder: "Discord ユーザーID（17〜20桁）",
            value: inv.discord_user_id,
            onInput: (e) => (state.invitees[i].discord_user_id = e.target.value.trim()),
          }),
          el("input", {
            type: "text",
            placeholder: "表示名（仮）",
            disabled: !!state.group,
            value: inv.display_name,
            onInput: (e) => (state.invitees[i].display_name = e.target.value),
          }),
          el(
            "button",
            {
              type: "button",
              disabled: !!state.group,
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
      el("legend", {}, "参加者"),
      el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "サークル名"), name),
      state.group ? el("p", { class: "help" }, "このサークルの参加者は登録済みです。続けてブックと初回セッションを設定してください。") : null,
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
            disabled: !!state.group,
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
      el(
        "button",
        {
          class: "btn",
          type: "button",
          onClick: async (event) => {
            if (submitting) return;
            state.groupName = name.value.trim();
            if (state.group) return go(1);
            await createGroup(event.currentTarget);
          },
        },
        "次へ",
      ),
    ),
  );
}

async function createGroup(button) {
  const invitees = state.invitees.filter((i) => i.discord_user_id);
  if (!state.groupName) return flash($("flash"), { title: "サークル名を入れてください", tone: "warn" });
  if (invitees.length === 0) return flash($("flash"), { title: "参加者を1人以上入れてください", tone: "warn" });

  clearFlash($("flash"));
  const unlock = lockForm(button, "登録しています…");
  try {
    state.group = await api.createGroup({
      name: state.groupName,
      invitees: invitees.map((i) => ({ discord_user_id: i.discord_user_id, display_name: i.display_name || "（未設定）" })),
    });
    go(1);
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), loginUrl });
  } finally {
    unlock();
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
  const title = el("input", { type: "text", value: state.book.title, placeholder: "サンプル技術書", onInput: e => { state.book.title = e.target.value; } });

  const renderSections = () => {
    if (state.sections.length === 0) {
      mount(
        box,
        addSectionButton(renderSections, box),
      );
      return;
    }
    mount(
      box,
      el("p", { class: "help" }, "今回扱う範囲を入力してください。"),
      el(
        "div",
        { class: "toc-list" },
        state.sections.map((s) =>
          el(
            "div",
            { style: "display:flex;align-items:center;gap:12px;font-size:.86rem" },
            el("input", {
              type: "text",
              class: "toc-list__title",
              value: s.title,
              placeholder: "範囲の名前（例：第2章 設計）",
              "data-section": s.id,
              "aria-label": "範囲の名前",
              // 打つたびに組み直すと入力欄から焦点が外れるので、状態だけ更新する
              onInput: (e) => { s.title = e.target.value; },
            }),
            el(
              "button",
              {
                type: "button",
                class: "toc-list__drop",
                "aria-label": `${s.title || "この範囲"}を消す`,
                onClick: () => {
                  state.sections = state.sections.filter((x) => x.id !== s.id);
                  renderSections();
                  box.querySelector("input, button")?.focus();
                },
              },
              "×",
            ),
          ),
        ),
      ),
      addSectionButton(renderSections, box),
    );
  };

  const tocBox = el("div", {});
  // 取り込み方はいつでも切り替えられる。手入力に切り替えても、取り込んだ範囲は残して直せる
  picker = feature.createTocPicker(api, state.group.id, {
    picked: state.sections.length ? state.tocSource : null,
    onPick: (sections, source, book) => {
      state.sections = sections;
      state.tocSource = source;
      if (book?.title && !title.value) {
        title.value = book.title;
        state.book.title = book.title;
      }
      state.book.isbn = book?.isbn ?? null;
      renderSections();
    },
    onManual: () => {
      state.tocSource = { kind: "manual", urls: [] };
      renderSections();
      if (state.sections.length === 0) box.querySelector("button")?.focus();
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
      el("button", { class: "btn btn--quiet", type: "button", onClick: () => go(0) }, "戻る"),
      el(
        "button",
        {
          class: "btn",
          type: "button",
          onClick: () => {
            state.book.title = title.value.trim();
            if (!state.book.title) return flash($("flash"), { title: "書名を入れてください", tone: "warn" });
            for (const s of state.sections) s.title = s.title.trim();
            if (state.sections.some((s) => !s.title)) {
              return flash($("flash"), { title: "名前のない範囲があります", detail: "名前を入れるか、×で消してください。", tone: "warn" });
            }
            if (state.sections.length === 0) return flash($("flash"), { title: "今回扱う範囲を1つ以上入力してください", tone: "warn" });
            go(2);
          },
        },
        "次へ",
      ),
    ),
  );
}

/** 使われていない番号を返す。行を消しても前の id と衝突しない。 */
function nextSectionId() {
  const used = new Set(state.sections.map((s) => s.id));
  for (let n = 1; ; n++) {
    const id = `sec_${n}`;
    if (!used.has(id)) return id;
  }
}

/** ＋ は空の行を足すだけ。名前はその行に直接書く。 */
function addSectionButton(rerender, box) {
  return el(
    "div",
    { style: "margin-top:12px" },
    el(
      "button",
      {
        class: "btn btn--quiet",
        type: "button",
        onClick: () => {
          const section = { id: nextSectionId(), title: "" };
          state.sections.push(section);
          rerender();
          box.querySelector(`[data-section="${section.id}"]`)?.focus();
        },
      },
      "＋ 範囲を足す",
    ),
  );
}

// ---- 03 いつまでに開くか ----------------------------------------------------

function renderSessionStep() {
  const from = el("input", { type: "date", value: state.periodStart, min: today(), max: DATE_MAX });
  const to = el("input", { type: "date", value: state.periodEnd, min: state.periodStart || today(), max: DATE_MAX });
  const duration = el("input", { type: "number", min: "15", max: "180", step: "5", value: String(state.duration) });
  const count = el("input", { type: "number", min: "1", max: "52", step: "1", value: String(state.plannedSessionCount) });
  const mode = el("div", { class: "choice-cards", role: "radiogroup", "aria-label": "セッションの作り方" },
    modeChoice("sequential", "1回ずつ作る", "最初のセッションだけ作り、進捗に合わせて次を追加します。"),
    modeChoice("all", "全回の枠を作る", "予定回数分の枠を作ります。調整を始めるのは最初のセッションだけです。"),
  );
  const periodSummary = el("span", {}, `${from.value || "—"}〜${to.value || "—"}`);
  const durationSummary = el("span", {}, `${duration.value}分`);
  const syncSummary = () => {
    state.periodStart = from.value;
    state.periodEnd = to.value;
    state.duration = Number(duration.value);
    state.plannedSessionCount = Number(count.value);
    periodSummary.textContent = `${from.value || "—"}〜${to.value || "—"}`;
    durationSummary.textContent = `${duration.value || "—"}分`;
  };
  from.addEventListener("input", () => {
    // 下限は開始日が変わったときだけ直す。入力中の欄の属性を書き換えると、打ちかけの数字が消える
    const min = from.value || today();
    if (to.min !== min) to.min = min;
    syncSummary();
  });
  to.addEventListener("input", syncSummary);
  duration.addEventListener("input", syncSummary);
  count.addEventListener("input", syncSummary);

  mount(
    $("form"),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "初回セッション"),
      el(
        "div",
        { class: "grid grid--2", style: "margin-top:16px" },
        el("label", { class: "field" }, el("span", {}, "開始日"), from),
        el("label", { class: "field" }, el("span", {}, "終了日"), to),
      ),
      el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "所要時間（15〜180分）"), duration),
      el("div", { class: "checks", style: "margin-top:20px" }, el("span", {}, "初回の対象範囲"),
        state.sections.map((section) => el("label", {}, el("input", { type: "checkbox", value: section.id, checked: true, "data-target-section": section.id }), section.title))),
    ),
    el("fieldset", { class: "fieldset" },
      el("legend", {}, "ブック全体の設定"),
      el("label", { class: "field", style: "margin-top:16px" }, el("span", {}, "予定セッション数（1〜52回）"), count),
      el("div", { style: "margin-top:16px" }, mode),
      el("p", { class: "help" }, "「全回の枠を作る」を選んでも、対象範囲は自動分割されません。各セッションを始めるときに範囲を選びます。"),
    ),
    el(
      "fieldset",
      { class: "fieldset" },
      el("legend", {}, "登録する内容"),
      el(
        "table",
        { class: "table" },
        el("tr", {}, el("th", {}, "サークル"), el("td", {}, state.groupName)),
        el("tr", {}, el("th", {}, "参加者"), el("td", {}, `あなたを含めて ${state.invitees.filter((i) => i.discord_user_id).length + 1}人`)),
        el("tr", {}, el("th", {}, "本"), el("td", {}, state.book.title)),
        el("tr", {}, el("th", {}, "初回の範囲"), el("td", {}, state.sections.filter(s => $("form").querySelector(`[data-target-section=\"${s.id}\"]`)?.checked).map(s => s.title).join("、") || "—")),
        el("tr", {}, el("th", {}, "期間"), el("td", {}, periodSummary)),
        el("tr", {}, el("th", {}, "長さ"), el("td", {}, durationSummary)),
      ),
    ),
    el(
      "div",
      { class: "submit" },
      el("button", { class: "btn btn--quiet", type: "button", onClick: () => go(1) }, "戻る"),
      el(
        "button",
        {
          class: "btn",
          type: "button",
          onClick: (event) => {
            if (submitting) return;
            state.periodStart = from.value;
            state.periodEnd = to.value;
            state.duration = Number(duration.value);
            state.targetSectionIds = state.sections.filter(s => $("form").querySelector(`[data-target-section=\"${s.id}\"]`)?.checked).map(s => s.id);
            createBook(event.currentTarget);
          },
        },
        "ブックを登録する",
      ),
    ),
  );
}

function modeChoice(value, label, description) {
  const input = el("input", { type: "radio", name: "session-creation-mode", value, checked: state.sessionCreationMode === value,
    onChange: () => { state.sessionCreationMode = value; },
  });
  return el("label", { class: "choice-card" }, input, el("span", {}, el("strong", {}, label), el("small", {}, description)));
}

/** 今日（ブラウザーの時刻）を YYYY-MM-DD で返す。日付入力の下限に使う。 */
function today() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

async function createBook(button) {
  if (!state.periodStart) return flash($("flash"), { title: "開始日を入れてください", tone: "warn" });
  if (!state.periodEnd) return flash($("flash"), { title: "終了目安日を入れてください", tone: "warn" });
  if (state.periodEnd < state.periodStart) {
    return flash($("flash"), { title: "終了目安日は開始日以降にしてください", tone: "warn" });
  }
  if (!Number.isInteger(state.duration) || state.duration < 15 || state.duration > 180) {
    return flash($("flash"), { title: "所要時間は15〜180分で入力してください", tone: "warn" });
  }
  if (!Number.isInteger(state.plannedSessionCount) || state.plannedSessionCount < 1 || state.plannedSessionCount > 52) return flash($("flash"), { title: "予定セッション数は1〜52回で入力してください", tone: "warn" });
  if (!state.targetSectionIds?.length) return flash($("flash"), { title: "初回の対象範囲を1つ以上選んでください", tone: "warn" });

  clearFlash($("flash"));
  const unlock = lockForm(button, "登録しています…");
  try {
    const created = await api.createBook(state.group.id, {
      title: state.book.title,
      isbn: state.book.isbn,
      toc_source: state.tocSource,
      sections: state.sections,
      planned_session_count: state.plannedSessionCount,
      session_creation_mode: state.sessionCreationMode,
      initial_session: {
        period_start: state.periodStart,
        period_end: state.periodEnd,
        duration_minutes: state.duration,
        target_section_ids: state.targetSectionIds,
      },
    });
    location.href = `/book.html?group_id=${encodeURIComponent(state.group.id)}&id=${encodeURIComponent(created.book.id)}`;
  } catch (err) {
    await reportMutationError(err, { node: $("flash"), loginUrl });
    unlock();
  }
}
