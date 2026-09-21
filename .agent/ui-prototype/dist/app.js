/* Marunage UIプロトタイプ。APIはまだ繋がず、状態見本だけを描画します。 */

const schedules = {
  old: [
    { label: "説明", who: "文 20分", minutes: 20 },
    { label: "説明", who: "文 20分", minutes: 20 },
    { label: "議論", who: "全員 20分", minutes: 20, tone: "mid" }
  ],
  current: [
    { label: "復習", who: "全員 20分", minutes: 20 },
    { label: "説明", who: "千恵 15分", minutes: 15, tone: "solid" },
    { label: "議論", who: "全員 25分", minutes: 25, tone: "mid" }
  ],
  latest: [
    { label: "復習", who: "全員 15分", minutes: 15 },
    { label: "説明", who: "千恵 20分", minutes: 20, tone: "solid" },
    { label: "議論", who: "全員 25分", minutes: 25, tone: "mid" }
  ]
};

const states = {
  waiting: {
    stamp: ["案はまだありません", ""],
    state: "参加条件を集めています",
    title: "今は待っていて大丈夫です",
    text: "千恵さんの参加条件が届くと、エージェントが案づくりを再開します。あなたへの依頼はありません。",
    plan: null,
    action: null,
    agent: { title: "千恵さんの回答を待っています", text: "停止はしていません。回答が届くか期限になると再開します。" },
    notifications: [["done", "送信済み", "綾さん・文さん・大さんへの確認依頼"], ["", "送信待ち", "千恵さんへの確認依頼"]],
    reason: "「待っていてよい」という結論です。誰の返事を待っているのか、エージェントが止まっていないことまで同じ面で読めます。"
  },
  consent: {
    stamp: ["案 3", "open"],
    state: "同意を集めています",
    title: "この変更に同意するか答えてください",
    text: "千恵さんが第2節の説明を15分で引き受けました。担当の引き受けとは別に、範囲が変わることへのあなたの同意が必要です。",
    plan: { next: schedules.current, prev: schedules.old },
    action: { label: "案の内容を読む", kind: "scroll" },
    tally: { agreed: 2, total: 4, deadline: "14:30" },
    agent: { title: "あなたの回答を待っています", text: "必要な回答が揃うまで進みません。停止はしていません。" },
    notifications: [["done", "送信済み", "回答の依頼 3件"], ["", "送信待ち", "催促 1件"]],
    reason: "あなたの意思表示です。必要な同意数と担当者の引き受けを、別々のものとして読めるようにしています。"
  },
  superseded: {
    stamp: ["案 4", "open"],
    state: "案 3 は無効になりました",
    title: "最新の案 4 を確認してください",
    text: "参加条件が変わったため、案 3 は無効になりました。案 3 へのあなたの同意も引き継いでいません。",
    plan: { next: schedules.latest, prev: schedules.current },
    action: { label: "案 4 を読む", kind: "scroll" },
    tally: { agreed: 1, total: 4, deadline: "15:00" },
    agent: { title: "最新の案への回答を待っています", text: "古い案への回答では確定しません。停止はしていません。" },
    notifications: [["done", "送信済み", "案が変わったことの連絡 3件"], ["warn", "成否不明", "文さんへの通知 1件"]],
    reason: "最新版への回答です。版が変わったことと、前の同意が無効になったことを、行動の文と同じ位置で伝えます。"
  },
  owner: {
    stamp: ["自動調整を停止しました", "void"],
    state: "管理者の判断待ち",
    title: "実施できる代案を選んでください",
    text: "欠席が重なり、60分の計画が成立しませんでした。エージェントは案を作れないまま停止しています。",
    plan: null,
    action: { label: "代案の条件を選ぶ", kind: "dialog" },
    agent: { title: "停止しました", text: "停止理由は「成立する案がない」です。自動での再試行は予定していません。", stopped: true },
    notifications: [["warn", "失敗", "千恵さんへの通知 1件"], ["warn", "成否不明", "文さんへの通知 1件"], ["done", "送信済み", "綾さん・大さんへの通知 2件"]],
    reason: "管理者の判断です。停止の理由と届かなかった通知を同じ視界に置き、動き続けているようには見せません。"
  },
  confirmed: {
    stamp: ["確定案 4", "done"],
    state: "確定しました",
    title: "計画は確定しました。いますることはありません",
    text: "9月21日 20:00から、この内容で実施します。変更が必要になったら参加条件を更新してください。",
    plan: { next: schedules.latest, prev: schedules.current },
    action: null,
    agent: { title: "処理を終えました", text: "返事は待っていません。計画の保存と通知の登録まで終わっています。" },
    notifications: [["done", "送信済み", "確定の連絡 4件"]],
    reason: "確定した結果です。することがないこと、どの版で確定したか、通知が全部送れたかを一つの面で確認できます。"
  }
};

const esc = value => String(value).replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

function planBar(items) {
  const total = items.reduce((sum, item) => sum + item.minutes, 0);
  const marks = items.reduce((acc, item) => acc.concat(acc[acc.length - 1] + item.minutes), [0]);
  const segments = items.map(item => `<span style="flex:${item.minutes}"${item.tone ? ` data-tone="${item.tone}"` : ""}><b>${esc(item.label)}</b>${esc(item.who)}</span>`).join("");
  const label = items.map(item => `${item.label} ${item.who}`).join("、");
  return `<div class="plan__bar" role="img" aria-label="進行表。${esc(label)}。">${segments}</div>
    <p class="plan__scale num">${marks.map(m => `<span>${m === total ? `${m}分` : m}</span>`).join("")}</p>`;
}

function headline(state) {
  const button = state.action
    ? `<button class="btn" type="button" data-action="${state.action.kind}">${esc(state.action.label)}</button>`
    : "";
  const tally = state.tally
    ? `<span class="tally">${Array.from({ length: state.tally.total }, (_, i) => `<i${i < state.tally.agreed ? " data-on" : ""}></i>`).join("")}<span class="num">同意 ${state.tally.agreed}/${state.tally.total}　期限 ${state.tally.deadline}</span></span>`
    : "";
  return `<div class="headline__top">
      <span class="stamp"${state.stamp[1] ? ` data-tone="${state.stamp[1]}"` : ""}>${esc(state.stamp[0])}</span>
      <span class="headline__state">${esc(state.state)}</span>
    </div>
    <div class="headline__body">
      <h2 style="font-size:clamp(1.3rem,3vw,1.7rem);font-weight:700;letter-spacing:-.02em;line-height:1.4">${esc(state.title)}</h2>
      <p class="change" style="margin-top:8px">${esc(state.text)}</p>
      ${button || tally ? `<div class="actions">${button}${tally}</div>` : ""}
    </div>`;
}

function consentBlock(version, done) {
  const members = [["綾", true], ["文", true], ["大", done]];
  return `<div class="consent">
    <div>
      <h3>必要な同意</h3>
      <strong>3人のうち ${done ? 3 : 2}人が同意しました</strong>
      <small>必要な人数はサーバーが返した値です。返事のない人は同意として数えません。</small>
      <div class="people">${members.map(([name, on]) => `<i${on ? " data-on" : ""}>${name}</i>`).join("")}</div>
    </div>
    <div>
      <h3>担当者の引き受け</h3>
      <strong>${done ? "千恵さんが引き受けました" : "千恵さんの回答待ちです"}</strong>
      <small>案 ${version} の担当内容に対する引き受けです。ほかの人が代わりに承諾することはできません。</small>
      <div class="people"><i${done ? " data-on" : ""}>千</i></div>
    </div>
  </div>`;
}

function proposal({ version, status, title, prev, next, actions = true, created }) {
  const tone = { open: "open", done: "done", void: "void" }[status] || "";
  const label = { open: `案 ${version}`, done: `確定案 ${version}`, void: `案 ${version}` }[status];
  return `<article class="proposal${status === "void" ? " proposal--void" : ""}" id="proposal-${version}">
    <div class="proposal__head">
      <span class="stamp"${tone ? ` data-tone="${tone}"` : ""}>${label}</span>
      <h2>${esc(title)}</h2>
      <time class="num">${esc(created)}</time>
    </div>
    <div class="proposal__body">
      ${status === "void" ? `<div class="notice"><span class="notice__mark">×</span><span><strong>この案への同意は無効です</strong><small>参加条件が変わったため、最新の案には引き継がれません。</small></span></div>` : ""}
      ${status === "done" ? `<div class="notice"><span class="notice__mark">✓</span><span><strong>この内容で確定しました</strong><small>これは履歴ではなく、実施する計画です。</small></span></div>` : ""}
      ${status === "void" ? "" : `<div class="plan" style="margin-top:24px">${planBar(next)}
        <div class="plan__prev"><p>変更前</p><div class="plan__bar plan__bar--prev" aria-hidden="true">${prev.map(i => `<span style="flex:${i.minutes}">${esc(i.who)}</span>`).join("")}</div></div></div>`}
      <table class="table">
        <tbody>
          <tr><th>担当の交代</th><td><span class="person" data-tone="declined"><i>文</i>文（辞退）</span> から <span class="person"${status === "done" ? ' data-tone="accepted"' : ""}><i>千</i>千恵（第2節 ${version === 4 ? 20 : 15}分）</span> へ</td></tr>
          <tr><th>見送る範囲</th><td>第1節の新しい説明です。次回へ引き継ぎます。</td></tr>
          <tr><th>この案の扱い</th><td>${status === "done" ? "確定した内容です。" : status === "void" ? "古い案です。回答できません。" : "確定前です。回答によって変わることがあります。"}</td></tr>
        </tbody>
      </table>
      ${status === "void" ? "" : consentBlock(version, status === "done")}
    </div>
    ${actions && status === "open" ? `<div class="proposal__foot">
      <p>案 ${version} への意思表示として記録します。</p>
      <button class="btn btn--quiet" type="button" data-vote="reject">同意しません</button>
      <button class="btn" type="button" data-vote="approve">同意します</button>
    </div>` : ""}
  </article>`;
}

function renderSession() {
  const root = document.querySelector('[data-page="session"]');
  if (!root) return;
  const params = new URLSearchParams(location.search);
  const key = states[params.get("state")] ? params.get("state") : "consent";
  const state = states[key];

  document.querySelectorAll("[data-state-link]").forEach(link => {
    link.setAttribute("aria-current", String(link.dataset.stateLink === key));
  });

  document.querySelector("#headline").innerHTML = headline(state);
  document.querySelector("#note-reason").textContent = state.reason;
  document.querySelector("#agent").innerHTML = `<p><strong>${esc(state.agent.title)}</strong>${esc(state.agent.text)}</p>`;
  document.querySelector("#agent").closest("section").classList.toggle("panel--stopped", Boolean(state.agent.stopped));
  document.querySelector("#notifications").innerHTML = `<div class="notify">${state.notifications
    .map(([tone, label, detail]) => `<p${tone ? ` data-tone="${tone}"` : ""}><b>${tone === "done" ? "✓" : tone === "warn" ? "?" : "…"}</b><span>${esc(label)}<small>${esc(detail)}</small></span></p>`)
    .join("")}</div>`;

  const area = document.querySelector("#proposals");
  if (key === "waiting") {
    area.innerHTML = `<div class="panels"><article class="panel"><h3>案の作成前</h3><strong>参加条件を集めています</strong><p>4人のうち3人が回答しました。千恵さんの回答が届き次第、案をつくります。</p></article></div>`;
  } else if (key === "superseded") {
    area.innerHTML = proposal({ version: 3, status: "void", title: "第2節を中心に組み替える", prev: schedules.old, next: schedules.current, actions: false, created: "9月19日 13:48" })
      + proposal({ version: 4, status: "open", title: "説明の時間を20分に延ばす", prev: schedules.current, next: schedules.latest, created: "9月19日 14:22" });
  } else if (key === "owner") {
    area.innerHTML = `<article class="proposal"><div class="proposal__head"><span class="stamp" data-tone="void">停止</span><h2>60分の計画が成立しません</h2><time class="num">9月19日 14:08</time></div>
      <div class="proposal__body"><table class="table"><tbody>
        <tr><th>停止の理由</th><td>成立する案がありません（no_feasible_plan）</td></tr>
        <tr><th>解けていない点</th><td>第1節と第2節を説明できる参加者がいません。</td></tr>
        <tr><th>再試行</th><td>予定していません。参加条件が変わるか、管理者が代案を出すまで止まります。</td></tr>
        <tr><th>ここまでの費用</th><td class="num">18円（呼び出し 11回）</td></tr>
      </tbody></table></div></article>`;
  } else if (key === "confirmed") {
    area.innerHTML = proposal({ version: 4, status: "done", title: "第2節を中心に組み替える", prev: schedules.current, next: schedules.latest, actions: false, created: "9月19日 14:12" });
  } else {
    area.innerHTML = proposal({ version: 3, status: "open", title: "第2節を中心に組み替える", prev: schedules.old, next: schedules.current, created: "9月19日 13:48" });
  }

  document.querySelector('[data-action="scroll"]')?.addEventListener("click", () => {
    document.querySelector(".proposal")?.scrollIntoView({ behavior: "smooth", block: "start" });
  });
  document.querySelector('[data-action="dialog"]')?.addEventListener("click", () => {
    document.querySelector("#answer-dialog")?.showModal();
  });
  document.querySelectorAll("[data-vote]").forEach(button => {
    button.addEventListener("click", () => {
      const card = button.closest(".proposal");
      card.querySelectorAll("button").forEach(b => { b.disabled = true; });
      const receipt = document.createElement("div");
      receipt.className = "receipt";
      receipt.setAttribute("role", "status");
      receipt.innerHTML = `<span class="spinner" aria-hidden="true"></span><span><strong>意思表示を受け付けました</strong><small>まだ確定していません。最新の案と必要な条件を照合しています。</small></span>`;
      card.querySelector(".proposal__body").appendChild(receipt);
    });
  });
}

function initDialog() {
  const dialog = document.querySelector("#answer-dialog");
  if (!dialog) return;
  const conflict = document.querySelector("#answer-conflict");
  const receipt = document.querySelector("#answer-receipt");
  dialog.querySelector("[data-close]")?.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", event => { if (event.target === dialog) dialog.close(); });
  document.querySelector("#answer-form")?.addEventListener("submit", event => {
    event.preventDefault();
    conflict.hidden = true;
    receipt.hidden = false;
  });
  document.querySelector("#simulate-conflict")?.addEventListener("click", () => {
    receipt.hidden = true;
    conflict.hidden = false;
  });
}

function initRegister() {
  const form = document.querySelector("#register-form");
  form?.addEventListener("submit", event => {
    event.preventDefault();
    const receipt = document.querySelector("#register-receipt");
    receipt.hidden = false;
    receipt.scrollIntoView({ behavior: "smooth", block: "center" });
  });
}

renderSession();
initDialog();
initRegister();
