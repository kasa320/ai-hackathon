// ブック1冊分の入力フォーム。グループ作成（複数冊）・ブック追加・ブック編集で共有する。
//
// 入力するのは書名・目次（範囲）・期間・開催回数・所要時間・日程調整のリード日数だけ。
// 初回セッションの日時や範囲は入力させない（枠と日程調整はサーバーが計画する）。
// 目次の取り込みは features/reading/toc.js のコンポーネントをそのまま使う。

import { el, mount } from "./dom.js";
import { featureFor } from "./features/index.js";
import { DATE_MAX } from "./ui.js";

const LEAD_PRESETS = [7, 14];
const LEAD_MIN = 1;
const LEAD_MAX = 30;
const COUNT_MAX = 52;
const DURATION_MIN = 15;
const DURATION_MAX = 180;

let seq = 0;

/** 今日（ブラウザーの時刻）を YYYY-MM-DD で返す。日付入力の下限に使う。 */
export function today() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/**
 * @param {object} options
 * @param {object} options.client       api オブジェクト（目次の取得に使う）
 * @param {string} options.groupId
 * @param {boolean} [options.withToc]   目次の取り込みを出すか（編集では出さない）
 * @param {object} [options.initial]    { title, isbn, tocSource, sections, periodStart, periodEnd, plannedSessionCount, durationMinutes, adjustmentLeadDays }
 * @param {string} [options.legend]     見出し
 * @returns {{ node: HTMLElement, read: () => object, validate: () => boolean, setLocked: (locked: boolean) => void, stop: () => void, focus: () => void }}
 */
export function createBookForm({ client, groupId, withToc = true, initial = null, legend = "ブック" }) {
  const uid = `bf${++seq}`;
  const state = {
    isbn: initial?.isbn ?? null,
    tocSource: initial?.tocSource ?? (withToc ? { kind: "manual", urls: [] } : null),
    sections: (initial?.sections ?? []).map((s) => ({ id: s.id, title: s.title })),
  };
  let picker = null;

  const errorBox = el("p", { class: "field__error", role: "alert", hidden: true });

  const title = el("input", { type: "text", value: initial?.title ?? "", placeholder: "例：サンプル技術書", autocomplete: "off" });
  const from = el("input", { type: "date", value: initial?.periodStart ?? "", min: withToc ? today() : null, max: DATE_MAX });
  const to = el("input", { type: "date", value: initial?.periodEnd ?? "", min: initial?.periodStart || (withToc ? today() : null), max: DATE_MAX });
  const count = el("input", { type: "number", min: "1", max: String(COUNT_MAX), step: "1", value: String(initial?.plannedSessionCount ?? 6), inputmode: "numeric" });
  const duration = el("input", { type: "number", min: String(DURATION_MIN), max: String(DURATION_MAX), step: "5", value: String(initial?.durationMinutes ?? 60), inputmode: "numeric" });

  // 下限は開始日が変わったときだけ直す。入力中の欄の属性を書き換えると、打ちかけの数字が消える
  from.addEventListener("input", () => {
    const min = from.value || (withToc ? today() : "");
    if (min) to.min = min;
    else to.removeAttribute("min");
  });

  // ---- 日程調整のリード日数 ----
  const initialLead = initial?.adjustmentLeadDays ?? 7;
  const leadIsCustom = !LEAD_PRESETS.includes(initialLead);
  const leadName = `${uid}-lead`;
  const leadCustom = el("input", {
    type: "number", min: String(LEAD_MIN), max: String(LEAD_MAX), step: "1", inputmode: "numeric",
    value: leadIsCustom ? String(initialLead) : "", disabled: !leadIsCustom, "aria-label": "リード日数（1〜30日）",
  });
  const leadRadio = (value, label, checked) =>
    el("label", {},
      el("input", {
        type: "radio", name: leadName, value: String(value), checked,
        onChange: () => {
          leadCustom.disabled = value !== "custom";
          if (value === "custom") leadCustom.focus();
        },
      }),
      label);
  const leadGroup = el(
    "div",
    { class: "inline", role: "radiogroup", "aria-label": "日程調整を始めるタイミング" },
    leadRadio(7, "1週間前（7日前）", !leadIsCustom && initialLead === 7),
    leadRadio(14, "2週間前（14日前）", !leadIsCustom && initialLead === 14),
    leadRadio("custom", "日数を指定", leadIsCustom),
    leadCustom,
    el("span", {}, "日前"),
  );

  // ---- 範囲（章）の一覧 ----
  const sectionBox = el("div", {});
  const nextSectionId = () => {
    const used = new Set(state.sections.map((s) => s.id));
    for (let n = 1; ; n++) if (!used.has(`sec_${n}`)) return `sec_${n}`;
  };
  const renderSections = () => {
    const add = el("div", { style: "margin-top:12px" },
      el("button", {
        class: "btn btn--quiet", type: "button",
        onClick: () => {
          const section = { id: nextSectionId(), title: "" };
          state.sections.push(section);
          renderSections();
          sectionBox.querySelector(`[data-section="${section.id}"]`)?.focus();
        },
      }, "＋ 範囲を足す"));
    if (state.sections.length === 0) {
      mount(sectionBox, el("p", { class: "help" }, "扱う章や節を入力してください。目次を取り込むと、ここに入ります。"), add);
      return;
    }
    mount(
      sectionBox,
      el("p", { class: "help" }, "ブック全体で扱う範囲です。名前はここで直せます。"),
      el("div", { class: "toc-list" }, state.sections.map((s) =>
        el("div", { class: "toc-row" },
          el("input", {
            type: "text", class: "toc-list__title", value: s.title, placeholder: "範囲の名前（例：第2章 設計）",
            "data-section": s.id, "aria-label": "範囲の名前",
            // 打つたびに組み直すと入力欄から焦点が外れるので、状態だけ更新する
            onInput: (e) => { s.title = e.target.value; },
          }),
          el("button", {
            type: "button", class: "toc-list__drop", "aria-label": `${s.title || "この範囲"}を消す`,
            onClick: () => {
              state.sections = state.sections.filter((x) => x.id !== s.id);
              renderSections();
              sectionBox.querySelector("input, button")?.focus();
            },
          }, "×"))),
      ),
      add,
    );
  };
  renderSections();

  // ---- 目次の取り込み（新規のときだけ） ----
  let tocNode = null;
  if (withToc) {
    const feature = featureFor("reading");
    picker = feature.createTocPicker(client, groupId, {
      picked: state.sections.length ? state.tocSource : null,
      onPick: (sections, source, book) => {
        state.sections = sections;
        state.tocSource = source;
        if (book?.title && !title.value) title.value = book.title;
        state.isbn = book?.isbn ?? null;
        renderSections();
      },
      onManual: () => {
        state.tocSource = { kind: "manual", urls: [] };
        renderSections();
        if (state.sections.length === 0) sectionBox.querySelector("button")?.focus();
      },
    });
    tocNode = el("div", { style: "margin-top:24px" }, picker.node);
  }

  const body = el(
    "fieldset",
    { class: "book-form__body" },
    el("legend", { class: "visually-hidden" }, legend),
    el("label", { class: "field" }, el("span", {}, "書名"), title),
    tocNode,
    el("div", { style: "margin-top:24px" }, el("span", { class: "field-label" }, "範囲"), sectionBox),
    el("div", { class: "grid grid--2" },
      el("label", { class: "field" }, el("span", {}, "期間の開始日"), from),
      el("label", { class: "field" }, el("span", {}, "期間の終了目安日"), to),
    ),
    el("div", { class: "grid grid--2" },
      el("label", { class: "field" }, el("span", {}, `開催回数（1〜${COUNT_MAX}回）`), count),
      el("label", { class: "field" }, el("span", {}, `1回の所要時間（${DURATION_MIN}〜${DURATION_MAX}分）`), duration),
    ),
    el("div", { style: "margin-top:20px" },
      el("span", { class: "field-label" }, "日程調整を始めるタイミング"),
      leadGroup,
      el("p", { class: "help" }, "各回の開催目安のこの日数前から、参加できる日を集めはじめます。"),
    ),
    errorBox,
  );

  const node = el("form", { class: "book-form", novalidate: true, onSubmit: (e) => e.preventDefault() }, body);

  function leadValue() {
    const checked = leadGroup.querySelector(`input[name="${leadName}"]:checked`)?.value;
    return checked === "custom" ? Number(leadCustom.value) : Number(checked);
  }

  function read() {
    return {
      title: title.value.trim(),
      isbn: state.isbn,
      tocSource: state.tocSource,
      sections: state.sections.map((s) => ({ id: s.id, title: s.title.trim() })),
      periodStart: from.value,
      periodEnd: to.value,
      plannedSessionCount: Number(count.value),
      durationMinutes: Number(duration.value),
      adjustmentLeadDays: leadValue(),
    };
  }

  /** 問題があれば最初の欄へ焦点を移してメッセージを出す。 */
  function validate() {
    const v = read();
    const problems = [
      [!v.title, "書名を入れてください。", title],
      [v.sections.length === 0, "扱う範囲を1つ以上入力してください。", sectionBox.querySelector("input, button")],
      [v.sections.some((s) => !s.title), "名前のない範囲があります。名前を入れるか、×で消してください。", sectionBox.querySelector("input:placeholder-shown")],
      [!v.periodStart, "期間の開始日を入れてください。", from],
      [!v.periodEnd, "期間の終了目安日を入れてください。", to],
      [v.periodStart && v.periodEnd && v.periodEnd < v.periodStart, "終了目安日は開始日以降にしてください。", to],
      [!Number.isInteger(v.plannedSessionCount) || v.plannedSessionCount < 1 || v.plannedSessionCount > COUNT_MAX, `開催回数は1〜${COUNT_MAX}回で入力してください。`, count],
      [!Number.isInteger(v.durationMinutes) || v.durationMinutes < DURATION_MIN || v.durationMinutes > DURATION_MAX, `所要時間は${DURATION_MIN}〜${DURATION_MAX}分で入力してください。`, duration],
      [!Number.isInteger(v.adjustmentLeadDays) || v.adjustmentLeadDays < LEAD_MIN || v.adjustmentLeadDays > LEAD_MAX, `日程調整の開始は${LEAD_MIN}〜${LEAD_MAX}日前で指定してください。`, leadCustom.disabled ? leadGroup.querySelector("input") : leadCustom],
    ];
    const hit = problems.find(([bad]) => bad);
    errorBox.textContent = hit ? hit[1] : "";
    errorBox.hidden = !hit;
    if (hit) hit[2]?.focus();
    return !hit;
  }

  return {
    node,
    read,
    validate,
    /** 登録済みのブックなど、入力を止めたいとき。フィールドセットの disabled で動的な部品ごと止まる。 */
    setLocked(locked) { body.disabled = locked; },
    stop() { picker?.stop(); },
    focus() { title.focus(); },
  };
}
