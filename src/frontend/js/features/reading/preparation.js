// 輪読の参加条件（ReadingPreparationData）の入力と表示。契約 第2部 R1・R3。
//
// 入力は構造化フォームに固定する。自由文は送らない（辞退理由も集めない）。
// R3 の検証はサーバーが最終判定するが、送る前に同じ規則で弾いて往復を減らす。

import { el, memberName, memberTone } from "../../dom.js";

/** 契約 R3：説明できる節は読んできた節の部分集合。担当するなら1件以上＋1〜持ち時間の整数。 */
export function validate(preparation, durationMinutes) {
  const errors = [];
  const d = preparation.data;
  const attending = preparation.attendance === "attending";

  if (attending && d.willing_to_present) {
    if (d.explainable_section_ids.length === 0) {
      errors.push({ path: "preparation.data.explainable_section_ids", message: "説明できる節を1つ以上選んでください。" });
    }
    if (!Number.isInteger(d.max_presentation_minutes) || d.max_presentation_minutes < 1 || d.max_presentation_minutes > durationMinutes) {
      errors.push({ path: "preparation.data.max_presentation_minutes", message: `1〜${durationMinutes}分で指定してください。` });
    }
    const notPrepared = d.explainable_section_ids.filter((id) => !d.prepared_section_ids.includes(id));
    if (notPrepared.length > 0) {
      errors.push({ path: "preparation.data.explainable_section_ids", message: "説明できる節は、読んできた節の中から選んでください。" });
    }
  }
  return errors;
}

/** 欠席・担当しないときの形。R3 の「それ以外」の規則に合わせる。 */
function normalize(preparation) {
  const d = preparation.data;
  if (preparation.attendance !== "attending" || !d.willing_to_present) {
    return {
      attendance: preparation.attendance,
      data: { willing_to_present: false, prepared_section_ids: d.prepared_section_ids, explainable_section_ids: [], max_presentation_minutes: 0 },
    };
  }
  return preparation;
}

/**
 * 参加条件のフォーム。read() で契約どおりの Preparation を返す。
 */
export function createPreparationForm(sessionData, current, durationMinutes) {
  const value = current ?? {
    attendance: "attending",
    data: { willing_to_present: false, prepared_section_ids: [], explainable_section_ids: [], max_presentation_minutes: 0 },
  };

  const attendance = el("div", { class: "pickset" },
    ["attending", "absent"].map((v) => el("label", { class: "pick" },
      el("input", { type: "radio", name: "attendance", value: v, checked: value.attendance === v }),
      v === "attending" ? "参加できる" : "参加できない",
    )),
  );

  const willing = el("div", { class: "pickset" },
    [true, false].map((v) => el("label", { class: "pick" },
      el("input", { type: "radio", name: "willing", value: String(v), checked: value.data.willing_to_present === v }),
      v ? "説明できる" : "今回は難しい",
    )),
  );

  const sectionBox = (name, checkedIds) => el("div", { class: "choice" },
    sessionData.sections.map((s) => el("label", { class: "choice__item" },
      el("input", { type: "checkbox", name, value: s.id, checked: checkedIds.includes(s.id) }),
      el("span", {},
        el("span", { class: "choice__name" }, s.title),
        sessionData.completed_section_ids.includes(s.id)
          ? el("span", { class: "choice__desc" }, "前回までに読んだ範囲")
          : sessionData.target_section_ids.includes(s.id)
            ? el("span", { class: "choice__desc" }, "今回の対象")
            : null,
      ),
    )),
  );

  const prepared = sectionBox("prepared", value.data.prepared_section_ids);
  const explainable = sectionBox("explainable", value.data.explainable_section_ids);
  const minutes = el("input", {
    type: "number", id: "max-minutes", min: "1", max: String(durationMinutes), step: "1",
    value: String(value.data.max_presentation_minutes || ""),
  });

  const errorBox = el("p", { class: "field__hint", style: "color:var(--pink);font-weight:700" });

  const presentFields = el("div", {},
    el("div", { class: "field" },
      el("span", { class: "field__label" }, "説明できる節"),
      el("p", { class: "field__hint" }, "読んできた節の中から選びます。"),
      explainable,
    ),
    el("div", { class: "field" },
      el("label", { class: "field__label", for: "max-minutes" }, "説明できる時間（分）"),
      el("p", { class: "field__hint" }, `1〜${durationMinutes}分。実際の担当時間はこの範囲に収まります。`),
      minutes,
    ),
  );

  function sync() {
    const attending = attendance.querySelector("input:checked")?.value === "attending";
    const canPresent = willing.querySelector("input:checked")?.value === "true";
    willing.parentElement.hidden = !attending;
    presentFields.hidden = !attending || !canPresent;
  }

  attendance.addEventListener("change", sync);
  willing.addEventListener("change", sync);

  const form = el("form", { autocomplete: "off" },
    el("p", { class: "shared-note" },
      "参加条件はこのグループのメンバーに共有されます。都合がつかない理由は入力しません。"),

    el("div", { class: "field" }, el("span", { class: "field__label" }, "今回参加できますか"), attendance),
    el("div", { class: "field" }, el("span", { class: "field__label" }, "担当して説明できますか"), willing),
    el("div", { class: "field" },
      el("span", { class: "field__label" }, "読んできた節"),
      prepared,
    ),
    presentFields,
    errorBox,
  );

  sync();

  return {
    node: form,
    showErrors(errors) {
      errorBox.textContent = errors.map((e) => e.message).join(" ");
    },
    read() {
      const checked = (name) => [...form.querySelectorAll(`input[name="${name}"]:checked`)].map((i) => i.value);
      const raw = {
        attendance: attendance.querySelector("input:checked")?.value ?? "attending",
        data: {
          willing_to_present: willing.querySelector("input:checked")?.value === "true",
          prepared_section_ids: checked("prepared"),
          explainable_section_ids: checked("explainable"),
          max_presentation_minutes: Number(minutes.value) || 0,
        },
      };
      return normalize(raw);
    },
  };
}

/** 共有画面に出す、みんなの参加条件。私的な理由は存在しないので出しようがない。 */
export function renderPreparations(preparations, members, sessionData) {
  return el("div", { class: "people" },
    preparations.map(({ member_id: id, value }) => {
      let state = "未回答";
      let tone = "";
      if (value) {
        if (value.attendance === "absent") { state = "欠席"; tone = "person--no"; }
        else if (value.data.willing_to_present) {
          state = `説明可 ${value.data.max_presentation_minutes}分`;
          tone = "person--yes";
        } else { state = "参加のみ"; tone = "person--yes"; }
      }
      return el("span", { class: `person person--${memberTone(members, id)} ${tone}` },
        el("span", { class: "person__avatar" }, memberName(members, id).slice(0, 1)),
        el("span", { class: "person__name" }, memberName(members, id)),
        el("span", { class: "person__state" }, state),
      );
    }),
  );
}
