package coord

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sort"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// proposalView は案と回答状況を公開用の形にする。
func proposalView(p store.Proposal, tasks []store.Task) (*apitypes.Proposal, error) {
	var req storedRequirements
	if err := json.Unmarshal(p.Requirements, &req); err != nil {
		return nil, err
	}
	d := decisionsFor(tasks)
	out := &apitypes.Proposal{
		ID: p.ID, Version: p.Version, Status: p.Status, ChangeKind: p.ChangeKind, Author: p.Author, Summary: p.Summary, Data: p.Data,
		Approvals: []apitypes.ApprovalProgress{}, Assignments: []apitypes.AssignmentStatus{},
	}
	for _, a := range req.Approvals {
		kind := store.TaskApproval
		if a.Kind == ApprovalOwner {
			kind = store.TaskOwnerApproval
		}
		ap := apitypes.ApprovalProgress{Kind: string(a.Kind), EligibleMemberIDs: a.Eligible, RequiredCount: a.Required, ApprovedMemberIDs: []string{}, RejectedMemberIDs: []string{}}
		for _, id := range a.Eligible {
			switch d[kind+":"+id] {
			case "approve":
				ap.ApprovedMemberIDs = append(ap.ApprovedMemberIDs, id)
			case "reject":
				ap.RejectedMemberIDs = append(ap.RejectedMemberIDs, id)
			}
		}
		out.Approvals = append(out.Approvals, ap)
	}
	for _, id := range req.Acceptors {
		st := "pending"
		switch d[store.TaskAssignment+":"+id] {
		case "accept":
			st = "accepted"
		case "decline":
			st = "declined"
		}
		out.Assignments = append(out.Assignments, apitypes.AssignmentStatus{MemberID: id, Status: st})
	}
	return out, nil
}

func taskView(tk store.Task) apitypes.Task {
	out := apitypes.Task{ID: tk.ID, SessionID: tk.SessionID, Kind: tk.Kind, Status: tk.Status, Title: tk.Title, DueAt: tk.DueAt, AllowedDecisions: []string{}}
	if tk.ProposalID != "" {
		id, v := tk.ProposalID, tk.ProposalVersion
		out.ProposalID, out.ProposalVersion = &id, &v
	}
	if tk.Status == store.TaskOpen {
		out.AllowedDecisions = append(out.AllowedDecisions, allowedDecisions[tk.Kind]...)
	}
	return out
}

func caseView(cs store.Case) *apitypes.CaseSummary {
	out := &apitypes.CaseSummary{ID: cs.ID, Status: cs.Status, Summary: cs.Summary, NextRetryAt: cs.NextRetryAt}
	if cs.ReasonCode != "" {
		r := cs.ReasonCode
		out.ReasonCode = &r
	}
	return out
}

// SessionDetail は共有計画・進行状況・自分の操作をまとめて返す。GET では LLM を起動しない。
func (c *Coordinator) SessionDetail(ctx context.Context, userID, sessionID string) (apitypes.SessionDetail, error) {
	now := c.now()
	var out apitypes.SessionDetail
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		sess, me, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return err
		}
		pb, err := c.playbook(sess.PlaybookID)
		if err != nil {
			return err
		}
		out = apitypes.SessionDetail{
			Session: summaryView(sess), Data: sess.Data, Members: []apitypes.Member{}, Preparations: []apitypes.MemberPreparation{},
			MyTasks: []apitypes.Task{}, CurrentMemberID: me.ID, ServerNow: now,
		}
		members, err := tx.SessionMembers(ctx, sess.ID)
		if err != nil {
			return err
		}
		preps, err := tx.Preparations(ctx, sess.ID)
		if err != nil {
			return err
		}
		for _, m := range members {
			out.Members = append(out.Members, memberView(m))
			mp := apitypes.MemberPreparation{MemberID: m.ID}
			if p, ok := preps[m.ID]; ok {
				mp.Value = &apitypes.Preparation{Attendance: p.Attendance, Data: p.Data}
			}
			out.Preparations = append(out.Preparations, mp)
		}

		if sess.ConfirmedProposalID != "" {
			p, err := tx.Proposal(ctx, sess.ConfirmedProposalID)
			if err != nil {
				return err
			}
			tasks, err := tx.TasksByProposal(ctx, p.ID)
			if err != nil {
				return err
			}
			if out.ConfirmedPlan, err = proposalView(p, tasks); err != nil {
				return err
			}
		}
		latest, err := tx.LatestProposal(ctx, sess.ID)
		switch {
		case err == nil:
			tasks, err := tx.TasksByProposal(ctx, latest.ID)
			if err != nil {
				return err
			}
			if out.CurrentProposal, err = proposalView(latest, tasks); err != nil {
				return err
			}
		case !errors.Is(err, store.ErrNotFound):
			return err
		}

		cs, err := tx.LatestCase(ctx, sess.ID)
		hasCase := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if hasCase {
			out.ActiveCase = caseView(cs)
			tasks, err := tx.TasksByCase(ctx, cs.ID)
			if err != nil {
				return err
			}
			for _, tk := range tasks {
				if tk.MemberID == me.ID {
					out.MyTasks = append(out.MyTasks, taskView(tk))
				}
			}
			ns, err := tx.NotificationsByCase(ctx, cs.ID)
			if err != nil {
				return err
			}
			for _, n := range ns {
				switch n.Status {
				case store.NotifyPending, store.NotifySending:
					out.NotificationSummary.PendingCount++
				case store.NotifySent:
					out.NotificationSummary.SentCount++
				case store.NotifyFailed:
					out.NotificationSummary.FailedCount++
				case store.NotifyUnknown:
					out.NotificationSummary.UnknownCount++
				}
			}
		}

		// 画面表示用の権限。サーバーは実際の更新時に再検証する。
		before := now.Before(sess.StartsAt)
		isOwner := me.Role == RoleOwner
		var myPrep *store.Preparation
		if p, ok := preps[me.ID]; ok {
			myPrep = &p
		}
		out.Permissions.CanUpdatePreparation = before
		out.Permissions.CanWithdrawAttendance = before && (myPrep == nil || myPrep.Attendance != AttendanceAbsent)
		if before {
			has, err := c.hasAssignment(ctx, tx, pb, sess, me.ID)
			if err != nil {
				return err
			}
			if has && myPrep != nil {
				s, err := c.snapshot(ctx, tx, sess, nil)
				if err != nil {
					return err
				}
				withdrawn, err := pb.ApplyWithdrawal(ctx, s, WithdrawAssignment, myPrep.Data)
				if err != nil {
					return err
				}
				has = !jsonEqual(withdrawn, myPrep.Data)
			}
			out.Permissions.CanWithdrawAssignment = has
		}
		out.Permissions.CanSubmitProposal = isOwner && before && hasCase && cs.Status == store.CaseNeedsOwner && canSecure(now, sess.StartsAt)
		out.Permissions.CanViewActivity = isOwner
		// 開催回を作れるのは管理者だけなので、作った人＝管理者が削除できる
		out.Permissions.CanDeleteSession = isOwner
		return nil
	})
	return out, err
}

// Activity は実行履歴・費用・通知状況を返す（管理者専用）。
func (c *Coordinator) Activity(ctx context.Context, userID, sessionID string) (apitypes.ActivityResponse, error) {
	out := apitypes.ActivityResponse{Items: []apitypes.ActivityItem{}, Summary: apitypes.ActivitySummary{Costs: []apitypes.Cost{}, Notifications: []apitypes.NotificationStatus{}}}
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		sess, me, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return err
		}
		if me.Role != RoleOwner {
			return apperr.ForbiddenErr()
		}
		items, err := tx.ActivityBySession(ctx, sess.ID, 200)
		if err != nil {
			return err
		}
		for _, a := range items {
			item := apitypes.ActivityItem{ID: a.ID, OccurredAt: a.OccurredAt, CaseID: a.CaseID, Kind: a.Kind, Summary: a.Summary}
			if a.ProposalID != "" {
				id := a.ProposalID
				item.ProposalID = &id
			}
			out.Items = append(out.Items, item)
		}
		cs, err := tx.LatestCase(ctx, sess.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		id := cs.ID
		out.Summary.CaseID = &id
		out.Summary.LLMCallCount, out.Summary.ToolCallCount = cs.LLMCallCount, cs.ToolCallCount
		calls, err := tx.LLMCallsByCase(ctx, cs.ID)
		if err != nil {
			return err
		}
		out.Summary.Costs = aggregateCosts(calls)
		ns, err := tx.NotificationsByCase(ctx, cs.ID)
		if err != nil {
			return err
		}
		for _, n := range ns {
			st := n.Status
			if st == store.NotifySending {
				st = store.NotifyPending
			}
			ns := apitypes.NotificationStatus{ID: n.ID, Status: st, UpdatedAt: n.UpdatedAt}
			if n.ErrorCode != "" {
				code := n.ErrorCode
				ns.ErrorCode = &code
			}
			out.Summary.Notifications = append(out.Summary.Notifications, ns)
		}
		return nil
	})
	return out, err
}

// aggregateCosts は通貨ごとに費用を合計する。1件でも金額不明があればその合計は null（0 として扱わない）。
func aggregateCosts(calls []store.LLMCall) []apitypes.Cost {
	type acc struct {
		est, billed       *big.Rat
		estNull, billNull bool
	}
	byCur := map[string]*acc{}
	for _, c := range calls {
		a := byCur[c.Currency]
		if a == nil {
			a = &acc{est: new(big.Rat), billed: new(big.Rat)}
			byCur[c.Currency] = a
		}
		add := func(sum *big.Rat, v *string, null *bool) {
			if v == nil {
				*null = true
				return
			}
			r, ok := new(big.Rat).SetString(*v)
			if !ok {
				*null = true
				return
			}
			sum.Add(sum, r)
		}
		add(a.est, c.EstimatedAmount, &a.estNull)
		add(a.billed, c.BilledAmount, &a.billNull)
	}
	out := []apitypes.Cost{}
	for cur, a := range byCur {
		cost := apitypes.Cost{Currency: cur}
		if !a.estNull {
			s := a.est.FloatString(6)
			cost.EstimatedAmount = &s
		}
		if !a.billNull {
			s := a.billed.FloatString(6)
			cost.BilledAmount = &s
		}
		out = append(out, cost)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}
