// 輪読の計画（ReadingPlanData）の表示。契約 第2部 R1。
//
// 進行表は文字で並べず、持ち時間に対する実寸の帯で描く。
// この製品が実際に組み替えているのは時間配分なので、そこを絵にする。

import { el, memberName } from "../../dom.js";

const ACTIVITY = {
  presentation: { label: "発表", cls: "agenda__seg--presentation" },
  review: { label: "復習", cls: "agenda__seg--review" },
  discussion: { label: "議論", cls: "agenda__seg--discussion" },
  joint_reading: { label: "共同読解", cls: "agenda__seg--joint" },
};

const sectionTitle = (sessionData, id) =>
  sessionData.sections.find((s) => s.id === id)?.title ?? id;

const sectionTitles = (sessionData, ids) =>
  ids.length ? ids.map((id) => sectionTitle(sessionData, id)).join("、") : "—";

/**
 * 進行表の帯。合計が持ち時間に満たない場合は残りを斜線で埋め、
 * 「60分のうち何分を使うか」が見えるようにする。
 */
export function renderAgenda(plan, sessionData, members, durationMinutes) {
  const used = plan.agenda.reduce((sum, item) => sum + item.minutes, 0);
  const spare = Math.max(0, durationMinutes - used);

  const segments = plan.agenda.map((item) => {
    const kind = ACTIVITY[item.activity] ?? { label: item.activity, cls: "" };
    const presenter = item.presenter_member_id ? memberName(members, item.presenter_member_id) : null;
    return el("span", {
      class: `agenda__seg ${kind.cls}`,
      style: `--min:${item.minutes}`,
      title: `${kind.label}${presenter ? `（${presenter}）` : ""} ${item.minutes}分／${sectionTitles(sessionData, item.section_ids)}`,
    },
      el("b", {}, presenter ? `${kind.label}・${presenter}` : kind.label),
      el("span", {}, `${item.minutes}分`),
    );
  });

  if (spare > 0) {
    segments.push(el("span", { class: "agenda__seg agenda__seg--empty", style: `--min:${spare}` },
      el("b", {}, "未割当"), el("span", {}, `${spare}分`)));
  }

  return el("div", { class: "agenda" },
    el("div", { class: "agenda__bar" }, segments),
    el("div", { class: "agenda__scale" },
      el("span", {}, "0"),
      el("span", {}, `${Math.round(durationMinutes / 2)}分`),
      el("span", {}, `${durationMinutes}分`),
    ),
  );
}

/** 未確定のときに出す空の帯。 */
export function renderEmptyAgenda(durationMinutes) {
  return el("div", { class: "agenda" },
    el("div", { class: "agenda__bar" },
      el("span", { class: "agenda__seg agenda__seg--empty", style: `--min:${durationMinutes}` },
        el("b", {}, "未確定"), el("span", {}, `${durationMinutes}分`)),
    ),
    el("div", { class: "agenda__scale" },
      el("span", {}, "0"), el("span", {}, `${Math.round(durationMinutes / 2)}分`), el("span", {}, `${durationMinutes}分`)),
  );
}

/** 案カードの中身：範囲と進行表。 */
export function renderPlan(plan, sessionData, members, durationMinutes) {
  return el("div", {},
    el("div", { class: "facts", style: "margin-bottom:18px" },
      el("div", { class: "facts__row" },
        el("span", { class: "facts__key" }, "今回の範囲"),
        el("span", { class: "facts__val" }, sectionTitles(sessionData, plan.covered_section_ids)),
      ),
      plan.deferred_section_ids.length
        ? el("div", { class: "facts__row" },
            el("span", { class: "facts__key" }, "次回へ"),
            el("span", { class: "facts__val" }, sectionTitles(sessionData, plan.deferred_section_ids)),
          )
        : null,
    ),
    el("p", { class: "field__label", style: "margin-bottom:7px" }, `進行表（${durationMinutes}分）`),
    renderAgenda(plan, sessionData, members, durationMinutes),
  );
}

/**
 * 確定計画と新しい案の差分。前後の帯を並べると、何が縮んで何が入ったかが一目で分かる。
 */
export function renderPlanDiff(before, after, sessionData, members, durationMinutes) {
  return el("div", {},
    el("div", { class: "diff", style: "margin-bottom:18px" },
      el("div", { class: "diff__row" },
        el("span", { class: "diff__key" }, "範囲"),
        el("span", { class: "diff__from" }, sectionTitles(sessionData, before.covered_section_ids)),
        el("span", { class: "diff__arrow", "aria-hidden": "true" }, "→"),
        el("span", { class: "diff__to" }, sectionTitles(sessionData, after.covered_section_ids)),
      ),
      el("div", { class: "diff__row" },
        el("span", { class: "diff__key" }, "次回へ"),
        el("span", { class: "diff__from" }, sectionTitles(sessionData, before.deferred_section_ids)),
        el("span", { class: "diff__arrow", "aria-hidden": "true" }, "→"),
        el("span", { class: "diff__to" }, sectionTitles(sessionData, after.deferred_section_ids)),
      ),
    ),
    el("p", { class: "field__label", style: "margin-bottom:7px" }, `進行表の変更（${durationMinutes}分）`),
    el("p", { class: "card__note", style: "margin-bottom:5px" }, "変更前"),
    renderAgenda(before, sessionData, members, durationMinutes),
    el("p", { class: "card__note", style: "margin:12px 0 5px" }, "変更後"),
    renderAgenda(after, sessionData, members, durationMinutes),
  );
}
