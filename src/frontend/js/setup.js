// 登録ウィザード（/setup.html）。
// 01 グループ → 02 教材（ISBNから目次を取得）→ 03 開催回
//
// 契約 第1部 第5・6節、第2部 R7。
// 目次の取得結果は候補にすぎず、管理者が確認・修正して開催回登録に使うまで保存されない。

import { api } from "./api.js";
import { createMockApi } from "./mock.js";
import { el, mount, clear } from "./dom.js";
import { renderHealth } from "./components/health.js";
import { mountBackdrop, mountTopbar } from "./components/layout.js";

const params = new URLSearchParams(location.search);
const useLive = params.get("live") === "1";
const client = useLive ? api : createMockApi({ state: "registering", viewer: "mem_a", tocVariant: params.get("toc") ?? "succeeded_web" });

const $ = (id) => document.getElementById(id);

const state = {
  step: 1,
  groupId: params.get("group"),
  groupName: "",
  book: { title: "", isbn: null, tocSource: { kind: "manual", urls: [] } },
  sections: [],           // { id, title }
  completed: new Set(),
  target: new Set(),
  lookupId: null,
};

// ---- ステップ表示 ----------------------------------------------------------

const STEPS = [["01", "グループ"], ["02", "教材"], ["03", "開催回"]];

function renderSteps() {
  mount($("steps"), STEPS.map(([num, label], i) => {
    const n = i + 1;
    const cls = n === state.step ? "steps__item steps__item--now" : n < state.step ? "steps__item steps__item--done" : "steps__item";
    return el("li", { class: cls }, el("span", { class: "steps__num" }, num), el("span", { class: "steps__label" }, label));
  }));
}

function go(step) {
  state.step = step;
  renderSteps();
  render();
  window.scrollTo({ top: 0, behavior: "smooth" });
}

function notice(message, tone = "warn") {
  const box = $("notice");
  box.hidden = false;
  box.className = `notice notice--${tone}`;
  box.textContent = message;
}

const clearNotice = () => { $("notice").hidden = true; };

// ---- 01 グループ -----------------------------------------------------------

function renderGroupStep() {
  const name = el("input", { type: "text", id: "group-name", value: state.groupName || "技術書輪読", placeholder: "例：技術書輪読" });
  const rows = el("div", { class: "stack" });

  const addRow = (discordId = "", display = "") => {
    const row = el("div", { class: "invitee" },
      el("input", { type: "text", class: "invitee__id", value: discordId, placeholder: "DiscordユーザーID（17〜20桁）", inputmode: "numeric" }),
      el("input", { type: "text", class: "invitee__name", value: display, placeholder: "表示名（仮）" }),
      el("button", { type: "button", class: "btn btn--sm", onclick: () => row.remove() }, "削除"),
    );
    rows.append(row);
  };
  addRow("222222222222222222", "B");
  addRow("333333333333333333", "C");
  addRow("444444444444444444", "D");

  const submit = async () => {
    clearNotice();
    const invitees = [...rows.querySelectorAll(".invitee")].map((r) => ({
      discord_user_id: r.querySelector(".invitee__id").value.trim(),
      display_name: r.querySelector(".invitee__name").value.trim(),
    })).filter((x) => x.discord_user_id);

    if (invitees.length < 1 || invitees.length > 9) { notice("メンバーは1〜9人で登録してください。"); return; }
    const bad = invitees.find((x) => !/^\d{17,20}$/.test(x.discord_user_id));
    if (bad) { notice("DiscordユーザーIDは17〜20桁の数字です。"); return; }

    try {
      const group = await client.createGroup({ name: name.value.trim(), invitees });
      state.groupId = group.id;
      state.groupName = group.name;
      const pending = group.members.filter((m) => !m.joined);
      if (pending.length) {
        notice(`${pending.map((m) => m.display_name).join("、")} さんはまだログインしていません。全員がログインするまで開催回は作れません。`);
      }
      go(2);
    } catch (err) {
      notice(err.message);
    }
  };

  return {
    title: ["誰と、", el("br"), "読みますか。"],
    lead: "メンバーのDiscordユーザーIDを登録します。招待された本人がログインすると所属が有効になります。"
      + "アプリから自動でDMを送ることはありません。",
    panel: el("div", {},
      el("div", { class: "field" }, el("label", { class: "field__label", for: "group-name" }, "輪読会の名前"), name),
      el("div", { class: "field" },
        el("span", { class: "field__label" }, "メンバー（あなた以外）"),
        el("p", { class: "field__hint" }, "1〜9人。あなたは自動で管理者になります。"),
        rows,
        el("p", { style: "margin-top:10px" }, el("button", { type: "button", class: "btn btn--sm", onclick: () => addRow() }, "行を追加")),
      ),
      el("button", { type: "button", class: "btn btn--primary btn--block", onclick: submit }, "グループを作る"),
    ),
  };
}

// ---- 02 教材（ISBN → 目次） ------------------------------------------------

function sectionRows() {
  const box = el("div", { class: "choice" });
  if (state.sections.length === 0) {
    return el("p", { class: "field__hint" }, "まだ節がありません。ISBNから取得するか、下の欄に手入力してください。");
  }
  for (const s of state.sections) {
    box.append(el("label", { class: "choice__item" },
      el("input", {
        type: "checkbox", checked: state.target.has(s.id),
        onchange: (e) => { e.target.checked ? state.target.add(s.id) : state.target.delete(s.id); state.completed.delete(s.id); render(); },
      }),
      el("span", {},
        el("span", { class: "choice__name" }, s.title),
        el("span", { class: "choice__desc" },
          state.target.has(s.id) ? "今回扱う" : state.completed.has(s.id) ? "前回までに読んだ" : "今回は扱わない",
          " ／ ",
          el("button", {
            type: "button", class: "linklike",
            onclick: (e) => { e.preventDefault(); state.completed.has(s.id) ? state.completed.delete(s.id) : (state.completed.add(s.id), state.target.delete(s.id)); render(); },
          }, state.completed.has(s.id) ? "「読んだ」を外す" : "前回までに読んだ"),
        ),
      ),
    ));
  }
  return box;
}

function renderBookStep() {
  const isbn = el("input", { type: "text", id: "isbn", placeholder: "9784297127831", inputmode: "numeric", value: state.book.isbn ?? "" });
  const title = el("input", { type: "text", id: "book-title", value: state.book.title, placeholder: "例：サンプル技術書" });
  const manual = el("textarea", { id: "manual-toc", spellcheck: "false", placeholder: "1行に1項目" });
  const result = el("div", {});

  const applyEntries = (lookup) => {
    state.sections = lookup.entries.map((e, i) => ({ id: `sec_${i + 1}`, title: e.title }));
    state.target = new Set(state.sections.map((s) => s.id));
    state.completed = new Set();
    state.book.isbn = lookup.book?.isbn ?? null;
    state.book.title = state.book.title || lookup.book?.title || "";
    state.book.tocSource = lookup.source === "web"
      ? { kind: "web", urls: lookup.source_urls }
      : lookup.source === "image" ? { kind: "image", urls: [] } : { kind: "manual", urls: [] };
    render();
  };

  const showLookup = (lookup) => {
    const byReason = {
      book_not_found: "書誌情報が見つかりませんでした。",
      toc_not_found: "目次が見つかりませんでした。",
      source_mismatch: "取得元のページと内容が一致しませんでした。推測で目次を作ることはしません。",
      image_unreadable: "画像から読み取れませんでした。",
      budget_exceeded: "本日の取得回数の上限に達しました。",
      model_error: "処理に失敗しました。",
    };

    if (lookup.status === "succeeded") {
      mount(result,
        el("div", { class: "card", style: "margin-top:14px" },
          el("div", { class: "card__head" },
            el("span", { class: "card__title" }, "目次の候補"),
            el("span", { class: "badge badge--ok" }, lookup.source === "web" ? "Webから取得" : "画像から読み取り"),
          ),
          lookup.book ? el("p", { class: "card__note" }, `${lookup.book.title}／${lookup.book.authors.join("、")}／${lookup.book.publisher ?? "出版社不明"}`) : null,
          lookup.source_urls.length
            ? el("p", { class: "card__note" }, "取得元：", el("a", { href: lookup.source_urls[0], target: "_blank", rel: "noreferrer" }, lookup.source_urls[0]))
            : null,
          lookup.unreadable_count > 0
            ? el("p", { class: "waiting" }, `読み取れなかった箇所が ${lookup.unreadable_count} 件あります。手元の画像と見比べて、足りない項目を足してください。`)
            : null,
          el("p", { style: "margin-top:14px" },
            el("button", { type: "button", class: "btn btn--primary btn--sm", onclick: () => applyEntries(lookup) }, "この候補を使う")),
          el("p", { class: "card__note", style: "margin-top:10px" }, "取り込んだあとで自由に修正できます。確定するのは開催回を登録したときです。"),
        ));
      return;
    }

    if (lookup.status === "needs_image") {
      const file = el("input", { type: "file", id: "toc-images", accept: "image/jpeg,image/png,image/webp", multiple: true });
      mount(result,
        el("div", { class: "card", style: "margin-top:14px" },
          el("div", { class: "card__head" },
            el("span", { class: "card__title" }, "目次ページの写真を送ってください"),
            el("span", { class: "badge badge--warn" }, "取得できず"),
          ),
          el("p", { class: "card__note" }, byReason[lookup.reason_code] ?? "取得できませんでした。"),
          el("div", { class: "field", style: "margin-top:14px" },
            el("span", { class: "field__label" }, "目次ページの画像（1〜5枚）"),
            el("p", { class: "field__hint" }, "JPEG・PNG・WebP、1枚4MBまで。画像は読み取りにだけ使い、保存しません。"),
            file,
          ),
          el("button", {
            type: "button", class: "btn btn--primary btn--sm",
            onclick: async () => {
              if (!file.files.length) { notice("画像を選んでください。"); return; }
              try {
                const next = await client.submitTocImages(state.groupId, lookup.id, file.files);
                showLookup(next);
              } catch (err) { notice(err.message); }
            },
          }, "画像から読み取る"),
        ));
      return;
    }

    mount(result,
      el("div", { class: "card", style: "margin-top:14px" },
        el("div", { class: "card__head" },
          el("span", { class: "card__title" }, "自動では取得できませんでした"),
          el("span", { class: "badge badge--danger" }, "手入力へ"),
        ),
        el("p", { class: "card__note" }, byReason[lookup.reason_code] ?? "取得できませんでした。下の欄に手入力してください。"),
      ));
  };

  const lookup = async () => {
    clearNotice();
    const value = isbn.value.replace(/-/g, "").trim();
    if (!/^\d{13}$/.test(value)) { notice("ISBNはハイフンなしの13桁で入力してください。"); return; }
    if (!state.groupId) { notice("先にグループを作ってください。"); return; }
    mount(result, el("p", { class: "waiting", style: "margin-top:14px" }, "目次を探しています…"));
    try {
      const res = await client.startTocLookup(state.groupId, value);
      showLookup(res);
    } catch (err) { notice(err.message); }
  };

  const applyManual = () => {
    const lines = manual.value.split("\n").map((l) => l.trim()).filter(Boolean);
    if (!lines.length) { notice("目次を1行以上入力してください。"); return; }
    state.sections = lines.map((t, i) => ({ id: `sec_${i + 1}`, title: t }));
    state.target = new Set(state.sections.map((s) => s.id));
    state.completed = new Set();
    state.book.tocSource = { kind: "manual", urls: [] };
    render();
  };

  return {
    title: ["読む本を、", el("br"), "ここへ。"],
    lead: "ISBNから目次を探します。見つからないときは目次ページの写真、それでも読めなければ手入力です。"
      + "書名から目次を推測して作ることはしません。",
    panel: el("div", {},
      el("div", { class: "field" }, el("label", { class: "field__label", for: "book-title" }, "本のタイトル"), title,
        el("span", { class: "field__hint" }, "入力すると、ISBNから取得した書名より優先します。")),
      el("div", { class: "field" },
        el("label", { class: "field__label", for: "isbn" }, "ISBN（13桁・任意）"),
        el("p", { class: "field__hint" }, "ハイフンなし。省略して手入力でも構いません。"),
        el("div", { class: "inline" }, isbn, el("button", { type: "button", class: "btn btn--sm", onclick: lookup }, "目次を探す")),
      ),
      result,
      el("div", { class: "field", style: "margin-top:20px" },
        el("label", { class: "field__label", for: "manual-toc" }, "手入力（1行に1項目）"),
        manual,
        el("button", { type: "button", class: "btn btn--sm", onclick: applyManual }, "この内容を使う"),
      ),
      state.sections.length
        ? el("div", { class: "field", style: "margin-top:20px" },
            el("span", { class: "field__label" }, `節の一覧（${state.sections.length}件）`),
            el("p", { class: "field__hint" }, "チェックした節が今回扱う範囲になります。"),
            sectionRows(),
          )
        : null,
      el("div", { class: "hero__actions" },
        el("button", { type: "button", class: "btn btn--sm", onclick: () => go(1) }, "← 戻る"),
        el("button", {
          type: "button", class: "btn btn--primary", disabled: state.sections.length === 0 || state.target.size === 0,
          onclick: () => { state.book.title = title.value.trim(); go(3); },
        }, "次へ"),
      ),
    ),
  };
}

// ---- 03 開催回 -------------------------------------------------------------

function renderSessionStep() {
  const title = el("input", { type: "text", id: "session-title", value: "第2回" });
  const starts = el("input", { type: "datetime-local", id: "starts-at" });
  const minutes = el("input", { type: "number", id: "duration", value: "60", min: "15", max: "180", step: "5" });

  const soon = new Date(Date.now() + 1000 * 60 * 60 * 24 * 7);
  soon.setMinutes(0, 0, 0);
  starts.value = new Date(soon.getTime() - soon.getTimezoneOffset() * 60000).toISOString().slice(0, 16);

  const submit = async () => {
    clearNotice();
    if (!state.groupId) { notice("先にグループを作ってください。"); return; }
    if (state.target.size === 0) { notice("今回扱う範囲を1つ以上選んでください。"); return; }

    const startsAt = new Date(starts.value);
    if (startsAt - Date.now() < 60 * 60 * 1000) { notice("開始時刻は現在から1時間より先にしてください。"); return; }

    const payload = {
      playbook_id: "reading",
      title: title.value.trim(),
      starts_at: startsAt.toISOString(),
      duration_minutes: Number(minutes.value),
      data: {
        book_title: state.book.title || "（書名未入力）",
        isbn: state.book.isbn,
        toc_source: state.book.tocSource,
        sections: state.sections,
        completed_section_ids: [...state.completed],
        target_section_ids: [...state.target],
      },
    };

    try {
      const res = await client.createSession(state.groupId, payload);
      location.href = `/session.html?id=${encodeURIComponent(res.session.id)}${useLive ? "&live=1" : "&mock=registering&as=mem_a"}`;
    } catch (err) {
      if (err.status === 409 && err.code === "members_not_joined") {
        notice("まだログインしていないメンバーがいます。全員がログインしてから登録してください。");
        return;
      }
      notice(err.fieldErrors?.map((f) => f.message).join(" ") || err.message);
    }
  };

  const titles = (set) => state.sections.filter((s) => set.has(s.id)).map((s) => s.title).join("、") || "—";

  return {
    title: ["いつ、", el("br"), "集まりますか。"],
    lead: "次回の日時は決まっている前提です。日程の自動調整はこのMVPでは行いません。"
      + "登録すると、全員に参加条件の確認が届きます。",
    panel: el("div", {},
      el("div", { class: "field" }, el("label", { class: "field__label", for: "session-title" }, "回のタイトル"), title),
      el("div", { class: "field" },
        el("label", { class: "field__label", for: "starts-at" }, "開始日時"), starts,
        el("span", { class: "field__hint" }, "現在から1時間より先。回答期限を確保できる日時にしてください。")),
      el("div", { class: "field" }, el("label", { class: "field__label", for: "duration" }, "持ち時間（分）"), minutes,
        el("span", { class: "field__hint" }, "15〜180分。進行表の合計はこの時間に収まります。")),
      el("div", { class: "card", style: "margin-bottom:20px" },
        el("div", { class: "card__head" }, el("span", { class: "card__title" }, "登録する内容")),
        el("div", { class: "facts" },
          el("div", { class: "facts__row" }, el("span", { class: "facts__key" }, "本"), el("span", { class: "facts__val" }, state.book.title || "（未入力）")),
          el("div", { class: "facts__row" }, el("span", { class: "facts__key" }, "今回"), el("span", { class: "facts__val" }, titles(state.target))),
          el("div", { class: "facts__row" }, el("span", { class: "facts__key" }, "前回まで"), el("span", { class: "facts__val facts__val--tbd" }, titles(state.completed))),
        ),
      ),
      el("div", { class: "hero__actions" },
        el("button", { type: "button", class: "btn btn--sm", onclick: () => go(2) }, "← 戻る"),
        el("button", { type: "button", class: "btn btn--primary", onclick: submit }, "開催回を登録する"),
      ),
    ),
  };
}

// ---- 描画 ------------------------------------------------------------------

function render() {
  const step = state.step === 1 ? renderGroupStep() : state.step === 2 ? renderBookStep() : renderSessionStep();
  mount($("wizard"),
    el("div", {},
      el("p", { class: "hero__eyebrow" }, el("span", {}, "輪読会をつくる")),
      el("h1", { class: "hero__title" }, step.title),
      el("p", { class: "hero__lead" }, step.lead),
    ),
    el("div", { class: "panel" }, step.panel),
  );
}

async function boot() {
  mountBackdrop();
  mountTopbar({ crumbs: [{ label: "グループ", href: "/" }, { label: "輪読会をつくる" }] });
  const health = await renderHealth($("health"), client);
  $("demo-mode").textContent = client.isMock ? "モック" : health?.dev_mode ? "デモモード" : "本番";
  if (state.groupId) state.step = 2;
  renderSteps();
  render();
}

boot();
