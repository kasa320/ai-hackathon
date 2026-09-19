// 実行履歴・費用・通知。管理者だけ。契約 第1部 第11節。
//
// 費用は通貨ごとに分け、不明を0円として扱わない。
// 通知の failed と unknown を区別し、unknown を「未送信」と書かない。

import { el, formatTime } from "../dom.js";

const KIND = {
  input_received: "入力を受け取った",
  proposal_created: "案を作った",
  response_recorded: "回答を記録した",
  plan_confirmed: "計画を確定した",
  agent_retry_scheduled: "再試行を予約した",
  agent_stopped: "処理を止めた",
  notification_updated: "通知の状態が変わった",
};

const NOTIFY = {
  sent: { label: "送信済み", cls: "badge--ok" },
  pending: { label: "送信待ち", cls: "badge--warn" },
  failed: { label: "失敗", cls: "badge--danger" },
  unknown: { label: "成否不明", cls: "badge--muted" },
};

function renderCosts(costs, llmCalls, toolCalls) {
  const figures = costs.length
    ? costs.map((c) => {
        const amount = c.billed_amount ?? c.estimated_amount;
        const known = amount !== null && amount !== undefined;
        return el("div", { class: "cost__figure" },
          el("span", { class: "cost__value" }, known ? `${c.currency} ${amount}` : "不明"),
          el("span", { class: "cost__label" },
            c.billed_amount ? "確定した請求額" : known ? "推定額（請求前）" : `費用を取得できていません（${c.currency}）`),
        );
      })
    : [el("div", { class: "cost__figure" },
        el("span", { class: "cost__value" }, "—"),
        el("span", { class: "cost__label" }, "LLMの呼び出しなし"))];

  return el("div", {},
    el("div", { class: "cost" },
      figures,
      el("div", { class: "cost__figure" },
        el("span", { class: "cost__value" }, String(llmCalls)),
        el("span", { class: "cost__label" }, "LLMの呼び出し回数"),
      ),
      el("div", { class: "cost__figure" },
        el("span", { class: "cost__value" }, String(toolCalls)),
        el("span", { class: "cost__label" }, "ツールの実行回数"),
      ),
    ),
    costs.some((c) => (c.billed_amount ?? c.estimated_amount) === null)
      ? el("p", { class: "card__note", style: "margin-top:12px" },
          "費用が取得できていない呼び出しがあります。0円として扱っていません。")
      : null,
  );
}

function renderNotifications(notifications) {
  if (notifications.length === 0) {
    return el("p", { class: "card__note" }, "この案件に関する通知はまだありません。");
  }
  return el("div", {},
    el("div", { class: "people" }, notifications.map((n) => {
      const tone = NOTIFY[n.status] ?? { label: n.status, cls: "badge--muted" };
      return el("span", { class: `badge ${tone.cls}` }, `${tone.label} ${formatTime(n.updated_at)}`);
    })),
    notifications.some((n) => n.status === "unknown")
      ? el("p", { class: "card__note", style: "margin-top:12px" },
          "「成否不明」は届いたかどうか確認できていない通知です。未送信とは限らないため、自動で再送しません。Discord側で確認してください。")
      : null,
  );
}

export function renderActivity(data) {
  const s = data.summary;

  return [
    el("article", { class: "card" },
      el("div", { class: "card__head" }, el("span", { class: "card__title" }, "この案件でかかった費用")),
      renderCosts(s.costs, s.llm_call_count, s.tool_call_count),
    ),

    el("article", { class: "card" },
      el("div", { class: "card__head" }, el("span", { class: "card__title" }, "通知の状態")),
      renderNotifications(s.notifications),
    ),

    el("article", { class: "card" },
      el("div", { class: "card__head" },
        el("span", { class: "card__title" }, "AIの動き"),
        el("span", { class: "card__note" }, `新しい順に ${data.items.length} 件`),
      ),
      el("div", { class: "log" }, data.items.map((item) => el("div", {
        class: item.kind === "agent_stopped" ? "log__row log__row--waiting" : "log__row",
      },
        el("span", { class: "log__time" }, formatTime(item.occurred_at)),
        el("span", {}, item.summary, el("span", { class: "card__note" }, `　${KIND[item.kind] ?? item.kind}`)),
      ))),
    ),
  ];
}
