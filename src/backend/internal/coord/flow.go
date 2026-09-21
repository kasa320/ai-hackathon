package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// storedRequirements は案の作成時に固定した条件。対象者と必要数は版ごとに固定し、あとから分母を縮めない。
type storedRequirements struct {
	Acceptors []string         `json:"acceptors"`
	Approvals []storedApproval `json:"approvals"`
}

type storedApproval struct {
	Kind     ApprovalKind `json:"kind"`
	Eligible []string     `json:"eligible"`
	Required int          `json:"required"`
}

var errNoEligibleVoters = errors.New("参加予定者がいないため、承認を得られません")

// normalizeRequirements は Playbook の条件を共通側の規則で確認し、必要数を算出する。
// 引き受け・投票の対象者は所属していて参加予定（attending）の人に限る。
func normalizeRequirements(s Snapshot, req ApprovalRequirements) (storedRequirements, error) {
	attending := s.Attending()
	out := storedRequirements{Acceptors: []string{}, Approvals: []storedApproval{}}
	for _, id := range req.RequiredAcceptorIDs {
		if !contains(attending, id) {
			return out, &ValidationError{Fields: []FieldError{{Path: "", Message: fmt.Sprintf("担当者 %s は参加予定のメンバーではありません", id)}}}
		}
		out.Acceptors = appendUnique(out.Acceptors, id)
	}
	for _, a := range req.Approvals {
		switch a.Kind {
		case ApprovalOwner:
			if s.OwnerMemberID == "" {
				return out, fmt.Errorf("coord: 管理者が見つかりません")
			}
			out.Approvals = append(out.Approvals, storedApproval{Kind: ApprovalOwner, Eligible: []string{s.OwnerMemberID}, Required: 1})
		case ApprovalMajority, ApprovalAll:
			var eligible []string
			for _, id := range a.EligibleMemberIDs {
				if !contains(attending, id) {
					return out, &ValidationError{Fields: []FieldError{{Path: "", Message: fmt.Sprintf("投票対象 %s は参加予定のメンバーではありません", id)}}}
				}
				eligible = appendUnique(eligible, id)
			}
			if len(eligible) == 0 {
				return out, errNoEligibleVoters
			}
			required := len(eligible)/2 + 1
			if a.Kind == ApprovalAll {
				if len(eligible) != len(s.Members) {
					return out, errNoEligibleVoters
				}
				required = len(eligible)
			}
			out.Approvals = append(out.Approvals, storedApproval{Kind: a.Kind, Eligible: eligible, Required: required})
		default:
			return out, fmt.Errorf("coord: 不明な承認の種類 %q", a.Kind)
		}
	}
	return out, nil
}

// checkProposal は案を用途固有の規則と共通の規則で検証し、固定する条件を返す。
func (c *Coordinator) checkProposal(ctx context.Context, pb Playbook, s Snapshot, changeKind string, plan json.RawMessage) (storedRequirements, error) {
	prop := Proposal{ChangeKind: changeKind, Data: plan}
	if err := pb.ValidatePlan(ctx, s, prop); err != nil {
		return storedRequirements{}, err
	}
	req, err := pb.ApprovalRequirements(ctx, s, prop)
	if err != nil {
		return storedRequirements{}, err
	}
	return normalizeRequirements(s, req)
}

// checkOutcome は計画処理の結果を検証する。検証エラーは AI に返してやり直させる。
func (c *Coordinator) checkOutcome(ctx context.Context, pb Playbook, s Snapshot, changeKind string, o Outcome) error {
	switch o.Kind {
	case DraftProposal:
		_, err := c.checkProposal(ctx, pb, s, changeKind, o.Plan)
		return err
	case DraftAsk:
		v := &ValidationError{}
		if len(o.AskMemberIDs) == 0 {
			v.Add("member_ids", "確認する相手を1人以上指定してください")
		}
		for i, id := range o.AskMemberIDs {
			known := false
			for _, m := range s.Members {
				known = known || m.ID == id
			}
			switch {
			case !known:
				v.Add(fmt.Sprintf("member_ids[%d]", i), "開催回のメンバーではありません")
			case contains(s.Case.AskedMemberIDs, id):
				v.Add(fmt.Sprintf("member_ids[%d]", i), "この案件で既に確認を依頼しています")
			}
		}
		return v.Err()
	case DraftNoFeasible:
		return nil
	default:
		return &ValidationError{Fields: []FieldError{{Path: "kind", Message: "不明な結果です"}}}
	}
}

func changeKindFor(s Snapshot) string {
	if len(s.ConfirmedPlans) == 0 {
		return ChangeInitial
	}
	return ChangeReplan
}

// createTask は本人宛てのタスクと、催促・期限のイベント、依頼の通知を登録する。
func (c *Coordinator) createTask(ctx context.Context, tx *store.Tx, sess store.Session, cs store.Case, m store.Member, kind, title string, p *store.Proposal, requestedBy string, due, now time.Time) error {
	if kind == store.TaskPreparation {
		pb, err := c.playbook(sess.PlaybookID)
		if err != nil {
			return err
		}
		if describer, ok := pb.(PreparationRequestDescriber); ok {
			s, err := c.snapshot(ctx, tx, sess, &cs)
			if err != nil {
				return err
			}
			title = describer.PreparationRequest(s, m.ID)
		}
	}
	tk := store.Task{
		ID: store.NewID("task"), SessionID: sess.ID, CaseID: cs.ID, MemberID: m.ID, Kind: kind, Status: store.TaskOpen,
		Title: title, DueAt: due, RequestedBy: requestedBy, CreatedAt: now,
	}
	if p != nil {
		tk.ProposalID, tk.ProposalVersion = p.ID, p.Version
	}
	if err := tx.CreateTask(ctx, tk); err != nil {
		return err
	}
	remindAt := now.Add(due.Sub(now) / 2)
	for _, e := range []store.Event{
		{ID: store.NewID("evt"), SessionID: sess.ID, CaseID: cs.ID, Kind: store.EventReminder, RefID: tk.ID, RunAt: remindAt, CreatedAt: now},
		{ID: store.NewID("evt"), SessionID: sess.ID, CaseID: cs.ID, Kind: store.EventDeadline, RefID: tk.ID, RunAt: due, CreatedAt: now},
	} {
		if err := tx.CreateEvent(ctx, e); err != nil {
			return err
		}
	}
	text := fmt.Sprintf("%s（回答期限：%s）", title, formatClock(due))
	text += taskReplyInstructions(kind)
	return c.notify(ctx, tx, sess, cs.ID, "task_requested", "task:"+tk.ID, text, []store.Member{m}, now)
}

func taskReplyInstructions(kind string) string {
	if kind == store.TaskPreparation {
		return "\n返信方法：このBotのDMに「参加条件」と送ってください。1項目ずつ質問します。最後に内容を確認し、保存ボタンを押すと回答が登録されます。Webからも入力できます。"
	}
	return "\n返信方法：このBotのDMに「回答」と送ってください。案の詳しい内容と回答ボタンを表示します。"
}

// supersedePending は pending の案を旧版にし、その未回答タスクを無効にする。案が変わったら返す。
func supersedePending(ctx context.Context, tx *store.Tx, sessionID string) (bool, error) {
	pending, err := tx.PendingProposals(ctx, sessionID)
	if err != nil {
		return false, err
	}
	for _, p := range pending {
		if err := tx.SetProposalStatus(ctx, p.ID, store.ProposalSuperseded, nil); err != nil {
			return false, err
		}
		if err := tx.CloseOpenTasksByProposal(ctx, p.ID, store.TaskObsolete); err != nil {
			return false, err
		}
	}
	return len(pending) > 0, nil
}

// onInputChanged は参加条件の変更・辞退を保存した後の共通処理。
// 現在の案を旧版にし、確定計画が実行不能になれば新しい案件を開き、必要なら再計画を予定する。
// withdrawn は辞退した人（なければ空）。戻り値は処理後の案件。
func (c *Coordinator) onInputChanged(ctx context.Context, tx *store.Tx, sess *store.Session, withdrawn string, now time.Time) (store.Case, error) {
	pb, err := c.playbook(sess.PlaybookID)
	if err != nil {
		return store.Case{}, err
	}
	if _, err := supersedePending(ctx, tx, sess.ID); err != nil {
		return store.Case{}, err
	}
	cs, err := tx.LatestCase(ctx, sess.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return cs, err
	}
	if errors.Is(err, store.ErrNotFound) || !cs.Open() {
		// 案件が完了している場合、確定計画がまだ実行可能なら新しい案件は作らない。
		if sess.ConfirmedProposalID != "" {
			s, err := c.snapshot(ctx, tx, *sess, nil)
			if err != nil {
				return cs, err
			}
			cur, err := tx.Proposal(ctx, sess.ConfirmedProposalID)
			if err != nil {
				return cs, err
			}
			if pb.ValidatePlan(ctx, s, Proposal{Version: cur.Version, ChangeKind: cur.ChangeKind, Data: cur.Data}) == nil {
				return cs, nil
			}
			sess.Status = sessionAttn
			if err := tx.UpdateSessionState(ctx, *sess); err != nil {
				return cs, err
			}
		}
		cs = store.Case{ID: store.NewID("case"), SessionID: sess.ID, Status: store.CaseCollecting, Summary: "変更を受け付けました。", CreatedAt: now, UpdatedAt: now}
		if err := tx.CreateCase(ctx, cs); err != nil {
			return cs, err
		}
	}
	if withdrawn != "" {
		cs.WithdrawnMemberIDs = appendUnique(cs.WithdrawnMemberIDs, withdrawn)
	}
	// 新しい入力で案件を再開する（管理者判断待ちからの復帰も含む）。
	cs.Status, cs.RetryCount, cs.NextRetryAt, cs.ReasonCode = store.CaseCollecting, 0, nil, ""
	if err := tx.CancelPendingEvents(ctx, cs.ID, store.EventPlan); err != nil {
		return cs, err
	}
	return cs, c.advance(ctx, tx, *sess, &cs, now)
}

// advance は未完了の案件を、参加条件の回答状況に応じて情報収集中・案作成中・管理者判断待ちへ進める。
func (c *Coordinator) advance(ctx context.Context, tx *store.Tx, sess store.Session, cs *store.Case, now time.Time) error {
	if cs.Status == store.CaseAwaitingConsent || !cs.Open() {
		return nil
	}
	preps, err := tx.Preparations(ctx, sess.ID)
	if err != nil {
		return err
	}
	members, err := tx.SessionMembers(ctx, sess.ID)
	if err != nil {
		return err
	}
	missing := 0
	for _, m := range members {
		if _, ok := preps[m.ID]; !ok {
			missing++
		}
	}
	tasks, err := tx.TasksByCase(ctx, cs.ID)
	if err != nil {
		return err
	}
	openPrep := 0
	for _, tk := range tasks {
		if tk.Kind == store.TaskPreparation && tk.Status == store.TaskOpen {
			openPrep++
		}
	}
	cs.UpdatedAt = now
	switch {
	case missing > 0 || openPrep > 0:
		// 初回の未回答者を欠席扱いせず、回答が揃うまで案を作らない。
		cs.Status, cs.Summary = store.CaseCollecting, "参加条件を確認しています。"
		return tx.UpdateCase(ctx, *cs)
	case !canSecure(now, responseHorizon(sess)):
		return c.needsOwner(ctx, tx, sess, cs, "deadline_expired", "開催までに回答期限を確保できないため、管理者の判断が必要です。", now)
	default:
		cs.Status, cs.Summary = store.CasePlanning, "AIが案を作成しています。"
		if err := tx.UpdateCase(ctx, *cs); err != nil {
			return err
		}
		return tx.CreateEvent(ctx, store.Event{ID: store.NewID("evt"), SessionID: sess.ID, CaseID: cs.ID, Kind: store.EventPlan, RunAt: now, CreatedAt: now})
	}
}

func canSecure(now, startsAt time.Time) bool {
	_, ok := dueAt(now, startsAt)
	return ok
}

// needsOwner は案件を管理者の判断待ちにし、管理者へ通知する。未承認の案は確定しない。
func (c *Coordinator) needsOwner(ctx context.Context, tx *store.Tx, sess store.Session, cs *store.Case, reason, summary string, now time.Time) error {
	cs.Status, cs.ReasonCode, cs.Summary, cs.NextRetryAt, cs.UpdatedAt = store.CaseNeedsOwner, reason, summary, nil, now
	if err := tx.UpdateCase(ctx, *cs); err != nil {
		return err
	}
	if err := tx.CancelPendingEvents(ctx, cs.ID, store.EventPlan); err != nil {
		return err
	}
	if err := c.activity(ctx, tx, sess, cs.ID, "agent_stopped", summary, "", now); err != nil {
		return err
	}
	owner, err := ownerOf(ctx, tx, sess)
	if err != nil {
		return err
	}
	return c.notify(ctx, tx, sess, cs.ID, "needs_owner", "needs_owner:"+store.NewID("n"), summary, []store.Member{owner}, now)
}

func ownerOf(ctx context.Context, tx *store.Tx, sess store.Session) (store.Member, error) {
	members, err := tx.SessionMembers(ctx, sess.ID)
	if err != nil {
		return store.Member{}, err
	}
	for _, m := range members {
		if m.Role == RoleOwner {
			return m, nil
		}
	}
	return store.Member{}, fmt.Errorf("coord: 管理者が見つかりません")
}

// createProposal は検証済みの案を新しい版として保存し、必要な本人の引き受け・承認のタスクを作る。
func (c *Coordinator) createProposal(ctx context.Context, tx *store.Tx, sess *store.Session, cs *store.Case, s Snapshot, author, summary string, plan json.RawMessage, now time.Time) (store.Proposal, error) {
	pb, err := c.playbook(sess.PlaybookID)
	if err != nil {
		return store.Proposal{}, err
	}
	changeKind := changeKindFor(s)
	req, err := c.checkProposal(ctx, pb, s, changeKind, plan)
	if err != nil {
		return store.Proposal{}, err
	}
	// Keep provisional deadline calculations aligned with the date shown in the proposal.
	if sess.ScheduleStatus == ScheduleProposed {
		if at, ok := plannedStart(pb, plan); ok {
			sess.StartsAt = at.UTC()
		}
	}
	due, ok := dueAt(now, sess.StartsAt)
	if !ok {
		return store.Proposal{}, errDeadline
	}
	if _, err := supersedePending(ctx, tx, sess.ID); err != nil {
		return store.Proposal{}, err
	}
	version, err := tx.NextProposalVersion(ctx, sess.ID)
	if err != nil {
		return store.Proposal{}, err
	}
	if err := bump(ctx, tx, sess, now); err != nil {
		return store.Proposal{}, err
	}
	p := store.Proposal{
		ID: store.NewID("prop"), SessionID: sess.ID, CaseID: cs.ID, Version: version, Status: store.ProposalPending,
		ChangeKind: changeKind, Author: author, Summary: summary, Data: plan, Requirements: encode(req), Revision: sess.Revision, CreatedAt: now,
	}
	if err := tx.CreateProposal(ctx, p); err != nil {
		return p, err
	}
	members, err := tx.SessionMembers(ctx, sess.ID)
	if err != nil {
		return p, err
	}
	byID := map[string]store.Member{}
	for _, m := range members {
		byID[m.ID] = m
	}
	label := "変更案"
	if changeKind == ChangeInitial {
		label = "初回案"
	}
	for _, id := range req.Acceptors {
		title := fmt.Sprintf("%s（版%d）の担当を引き受けられるか回答してください。", label, version)
		if err := c.createTask(ctx, tx, *sess, *cs, byID[id], store.TaskAssignment, title, &p, "system", due, now); err != nil {
			return p, err
		}
	}
	for _, a := range req.Approvals {
		kind, title := store.TaskApproval, fmt.Sprintf("%s（版%d）に賛成か反対かを回答してください。", label, version)
		if a.Kind == ApprovalAll {
			at, ok := plannedStart(pb, plan)
			if !ok {
				at = sess.StartsAt
			}
			title = fmt.Sprintf("%s（版%d）：%sから%d分、提案された内容で参加できますか？全員の回答後に確定します。", label, version, formatClock(at), sess.DurationMinutes)
		}
		if a.Kind == ApprovalOwner {
			kind, title = store.TaskOwnerApproval, fmt.Sprintf("%s（版%d）を承認するか回答してください。", label, version)
		}
		for _, id := range a.Eligible {
			if err := c.createTask(ctx, tx, *sess, *cs, byID[id], kind, title, &p, "system", due, now); err != nil {
				return p, err
			}
		}
	}
	cs.Status, cs.ReasonCode, cs.NextRetryAt, cs.UpdatedAt = store.CaseAwaitingConsent, "", nil, now
	cs.Summary = fmt.Sprintf("案（版%d）への回答を待っています。", version)
	if err := tx.UpdateCase(ctx, *cs); err != nil {
		return p, err
	}
	who := "AI"
	if author == "owner" {
		who = "管理者"
	}
	if err := c.activity(ctx, tx, *sess, cs.ID, "proposal_created", fmt.Sprintf("%sが案（版%d）を作成しました：%s", who, version, summary), p.ID, now); err != nil {
		return p, err
	}
	// 必要な条件がない（担当だけの変更で担当者もいない）案は、そのまま確定条件を満たす。
	return p, c.evaluate(ctx, tx, sess, cs, p, now)
}

var errDeadline = errors.New("回答期限を確保できません")

// decisionsFor は案に対する回答を（種類, メンバー）ごとにまとめる。
func decisionsFor(tasks []store.Task) map[string]string {
	out := map[string]string{}
	for _, tk := range tasks {
		if tk.Status == store.TaskAnswered {
			out[tk.Kind+":"+tk.MemberID] = tk.Decision
		}
	}
	return out
}

// evaluate は案の必要条件を確認し、揃えば確定、成立しなければ棄却して再計画する。
func (c *Coordinator) evaluate(ctx context.Context, tx *store.Tx, sess *store.Session, cs *store.Case, p store.Proposal, now time.Time) error {
	if p.Status != store.ProposalPending {
		return nil
	}
	var req storedRequirements
	if err := json.Unmarshal(p.Requirements, &req); err != nil {
		return err
	}
	tasks, err := tx.TasksByProposal(ctx, p.ID)
	if err != nil {
		return err
	}
	d := decisionsFor(tasks)
	satisfied, impossible := true, false
	for _, id := range req.Acceptors {
		switch d[store.TaskAssignment+":"+id] {
		case "accept":
		case "decline":
			impossible = true
		default:
			satisfied = false
		}
	}
	for _, a := range req.Approvals {
		kind := store.TaskApproval
		if a.Kind == ApprovalOwner {
			kind = store.TaskOwnerApproval
		}
		approved, rejected := 0, 0
		for _, id := range a.Eligible {
			switch d[kind+":"+id] {
			case "approve":
				approved++
			case "reject":
				rejected++
			}
		}
		if approved < a.Required {
			satisfied = false
		}
		// 残り全員が賛成しても必要数に届かなければ成立しない。管理者の reject は即棄却。
		if rejected > len(a.Eligible)-a.Required {
			impossible = true
		}
	}
	switch {
	case impossible:
		return c.reject(ctx, tx, sess, cs, p, now)
	case satisfied:
		return c.confirm(ctx, tx, sess, cs, p, now)
	}
	return nil
}

// confirm は最新版と参加条件を再確認し、計画保存と通知待ち登録を同じトランザクションで行う。
func (c *Coordinator) confirm(ctx context.Context, tx *store.Tx, sess *store.Session, cs *store.Case, p store.Proposal, now time.Time) error {
	pb, err := c.playbook(sess.PlaybookID)
	if err != nil {
		return err
	}
	if sess.Revision != p.Revision {
		// 案の作成後に参加条件などが変わっていれば、この案では確定しない。
		return c.reject(ctx, tx, sess, cs, p, now)
	}
	s, err := c.snapshot(ctx, tx, *sess, cs)
	if err != nil {
		return err
	}
	if err := pb.ValidatePlan(ctx, s, Proposal{Version: p.Version, ChangeKind: p.ChangeKind, Data: p.Data}); err != nil {
		c.log.Warn("確定前の再検証に失敗", "proposal", p.ID, "err", err)
		return c.reject(ctx, tx, sess, cs, p, now)
	}
	if sess.ConfirmedProposalID != "" {
		if err := tx.SetProposalStatus(ctx, sess.ConfirmedProposalID, store.ProposalSuperseded, nil); err != nil {
			return err
		}
	}
	if err := tx.SetProposalStatus(ctx, p.ID, store.ProposalConfirmed, &now); err != nil {
		return err
	}
	sess.ConfirmedProposalID, sess.Status = p.ID, sessionConfirm
	// 期間だけで登録した回は、この案で開催日時も決まる。
	scheduled := ""
	if sess.ScheduleStatus == store.ScheduleProposed {
		if at, ok := plannedStart(pb, p.Data); ok {
			sess.StartsAt, sess.ScheduleStatus = at.UTC(), store.ScheduleConfirmed
			scheduled = formatClock(at)
		}
	}
	if err := bump(ctx, tx, sess, now); err != nil {
		return err
	}
	// 確定後に追加の投票で結果を変えないよう、未回答の残りタスクは無効にする。
	if err := tx.CloseOpenTasksByCase(ctx, cs.ID, store.TaskObsolete); err != nil {
		return err
	}
	cs.Status, cs.ReasonCode, cs.NextRetryAt, cs.UpdatedAt = store.CaseConfirmed, "", nil, now
	cs.Summary = fmt.Sprintf("計画が確定しました（版%d）。", p.Version)
	if scheduled != "" {
		cs.Summary = fmt.Sprintf("開催日時が %s に決まりました（版%d）。", scheduled, p.Version)
	}
	if err := tx.UpdateCase(ctx, *cs); err != nil {
		return err
	}
	if err := c.activity(ctx, tx, *sess, cs.ID, "plan_confirmed", cs.Summary, p.ID, now); err != nil {
		return err
	}
	return c.notify(ctx, tx, *sess, cs.ID, "plan_confirmed", "confirm:"+p.ID, fmt.Sprintf("計画が確定しました（版%d）。%s", p.Version, p.Summary), nil, now)
}

// reject は案を棄却し、未回答タスクを無効にして再計画へ戻す。
func (c *Coordinator) reject(ctx context.Context, tx *store.Tx, sess *store.Session, cs *store.Case, p store.Proposal, now time.Time) error {
	if err := tx.SetProposalStatus(ctx, p.ID, store.ProposalRejected, nil); err != nil {
		return err
	}
	if err := tx.CloseOpenTasksByProposal(ctx, p.ID, store.TaskObsolete); err != nil {
		return err
	}
	if err := bump(ctx, tx, sess, now); err != nil {
		return err
	}
	if err := c.activity(ctx, tx, *sess, cs.ID, "response_recorded", fmt.Sprintf("案（版%d）は成立しなかったため、別の案を検討します。", p.Version), p.ID, now); err != nil {
		return err
	}
	cs.Status = store.CasePlanning
	return c.advance(ctx, tx, *sess, cs, now)
}
