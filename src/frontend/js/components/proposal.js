// 案の表示のうち、用途によらない部分。契約 第1部 第4節・第8節。
//
// 版・引き受け・承認の集まり具合を出す。必要数はサーバーが返した required_count を
// そのまま使い、画面で過半数を計算しない。
// 計画の中身（範囲・進行表）は用途固有なので features/<playbook_id>/ が描く。

import { el, memberName, memberTone } from "../dom.js";

const STATUS = {
  pending: { label: "同意を集めています", cls: "badge--warn" },
  confirmed: { label: "確定しました", cls: "badge--ok" },
  superseded: { label: "無効", cls: "badge--muted" },
  rejected: { label: "棄却", cls: "badge--muted" },
};

const APPROVAL_KIND = { majority: "参加予定者の過半数", owner: "管理者の承認" };

const CHANGE_KIND = { initial: "初回の計画", replan: "計画の変更" };

function personChip(members, id, state, tone) {
  return el("span", { class: `person person--${memberTone(members, id)} ${tone}` },
    el("span", { class: "person__avatar" }, memberName(members, id).slice(0, 1)),
    el("span", { class: "person__name" }, memberName(members, id)),
    el("span", { class: "person__state" }, state),
  );
}

/** 承認の進み具合。マスの数は required_count ではなく対象人数で描く。 */
function renderApproval(approval, members) {
  const { eligible_member_ids: eligible, approved_member_ids: yes, rejected_member_ids: no } = approval;

  const cells = eligible.map((id) => {
    const cls = yes.includes(id) ? "consent__dot--yes" : no.includes(id) ? "consent__dot--no" : "";
    return el("span", { class: `consent__dot ${cls}` });
  });

  return el("div", {},
    el("p", { class: "consent__rule" },
      "必要な同意：", APPROVAL_KIND[approval.kind] ?? approval.kind,
      "（", el("span", { class: "num" }, `${eligible.length}人中${approval.required_count}人`), "）",
    ),
    el("div", { class: "consent__dots", "aria-hidden": "true" }, cells),
    el("div", { class: "people" }, eligible.map((id) => {
      if (yes.includes(id)) return personChip(members, id, "賛成", "person--yes");
      if (no.includes(id)) return personChip(members, id, "反対", "person--no");
      return personChip(members, id, "未回答", "");
    })),
  );
}

function renderAssignments(assignments, members) {
  if (assignments.length === 0) return null;
  const label = { pending: "確認中", accepted: "引き受け済み", declined: "断られた" };
  return el("div", { style: "margin-top:14px" },
    el("p", { class: "consent__rule" }, "担当の引き受け（本人だけが操作できます）"),
    el("div", { class: "people", style: "margin-top:8px" },
      assignments.map((a) => {
        const tone = a.status === "accepted" ? "person--yes" : a.status === "declined" ? "person--no" : "";
        return personChip(members, a.member_id, label[a.status] ?? a.status, tone);
      }),
    ),
  );
}

/**
 * @param {object} proposal 契約の Proposal
 * @param {object[]} members
 * @param {(data: object) => Node|null} renderPlan 用途固有の計画の描画
 * @param {object} opts { heading, void: boolean }
 */
export function renderProposalCard(proposal, members, renderPlan, opts = {}) {
  const status = STATUS[proposal.status] ?? { label: proposal.status, cls: "badge--muted" };
  const isVoid = proposal.status === "superseded" || proposal.status === "rejected";

  const head = el("div", { class: "card__head" },
    el("span", { class: isVoid ? "version version--void" : "version" }, `案 v${proposal.version}`),
    el("span", { class: "card__title" }, opts.heading ?? CHANGE_KIND[proposal.change_kind] ?? "計画"),
    el("span", { class: `badge ${status.cls}` }, status.label),
    proposal.author === "owner" ? el("span", { class: "badge badge--muted" }, "管理者の代案") : null,
  );

  if (isVoid) {
    return el("article", { class: "card card--void" }, head,
      el("p", { class: "card__note" },
        "新しい案が出たため、この案に集まっていた同意と引き受けは無効になりました。古い案のまま確定することはありません。"),
    );
  }

  const consent = (proposal.approvals.length || proposal.assignments.length)
    ? el("div", { class: "consent" },
        proposal.approvals.map((a) => renderApproval(a, members)),
        renderAssignments(proposal.assignments, members),
      )
    : null;

  return el("article", { class: "card" }, head,
    proposal.summary ? el("p", { class: "card__note" }, proposal.summary) : null,
    renderPlan(proposal.data),
    consent,
  );
}
