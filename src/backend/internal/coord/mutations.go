package coord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func accepted(sess store.Session, caseID string) store.Response {
	return store.Response{Status: http.StatusAccepted, Body: encode(apitypes.MutationAccepted{
		SessionID: sess.ID, Revision: sess.Revision, CaseID: caseID, ProcessingStatus: "queued",
	})}
}

func latestCaseID(ctx context.Context, tx *store.Tx, sessionID string) (string, error) {
	cs, err := tx.LatestCase(ctx, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return cs.ID, err
}

// checkRevision は expected_revision が現在の revision と一致するかを確認する。
func checkRevision(sess store.Session, expected *int64) error {
	if expected == nil {
		return apperr.Validation(apperr.Field{Path: "expected_revision", Message: "必須です"})
	}
	if *expected != sess.Revision {
		return apperr.RevisionConflictErr(sess.Revision)
	}
	return nil
}

func checkNotStarted(sess store.Session, now time.Time) error {
	if !now.Before(responseHorizon(sess)) {
		return apperr.InvalidStateErr("開催時刻を過ぎたため、変更できません。")
	}
	return nil
}

// PutPreparation は本人の参加条件を全置換する（docs/api-endpoint.md）。
func (c *Coordinator) PutPreparation(ctx context.Context, userID, sessionID string, in apitypes.PutPreparationInput, idem *store.IdemKey) (store.Response, error) {
	return c.putPreparation(ctx, userID, sessionID, in, idem, EntryWeb)
}

// putPreparation は入口（Web / Discord）だけを変えて同じ保存処理を使う。
// 認可・版の確認・冪等性・整合性は入口によらず共通で、入口は記録にだけ残す。
func (c *Coordinator) putPreparation(ctx context.Context, userID, sessionID string, in apitypes.PutPreparationInput, idem *store.IdemKey, source string) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		sess, m, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return store.Response{}, err
		}
		if err := checkNotStarted(sess, now); err != nil {
			return store.Response{}, err
		}
		if err := checkRevision(sess, in.ExpectedRevision); err != nil {
			return store.Response{}, err
		}
		return c.submitPreparation(ctx, tx, &sess, m, in.Preparation, "preparation", source, now)
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// 参加条件を更新した入口。プログラムだけが渡す内部の情報で、公開APIの入力には含めない。
const (
	EntryWeb     = "web"
	EntryDiscord = "discord"
)

func entryLabel(source string) string {
	if source == EntryDiscord {
		return "Discord"
	}
	return "Web"
}

// submitPreparation は参加条件の保存と、本人宛ての未回答の確認タスクへの回答を行う。
// source は入口（web / discord）。記録に残すのは項目の差分と入口だけで、理由や発言は残さない。
func (c *Coordinator) submitPreparation(ctx context.Context, tx *store.Tx, sess *store.Session, m store.Member, in *apitypes.Preparation, path, source string, now time.Time) (store.Response, error) {
	pb, err := c.playbook(sess.PlaybookID)
	if err != nil {
		return store.Response{}, err
	}
	if in == nil {
		return store.Response{}, apperr.Validation(apperr.Field{Path: path, Message: "必須です"})
	}
	if in.Attendance != AttendanceAttending && in.Attendance != AttendanceAbsent {
		return store.Response{}, apperr.Validation(apperr.Field{Path: path + ".attendance", Message: "attending または absent を指定してください"})
	}
	s, err := c.snapshot(ctx, tx, *sess, nil)
	if err != nil {
		return store.Response{}, err
	}
	data, err := pb.ValidatePreparation(ctx, s, in.Attendance, in.Data)
	if err := validationErr(err, path+".data"); err != nil {
		return store.Response{}, err
	}

	preps, err := tx.Preparations(ctx, sess.ID)
	if err != nil {
		return store.Response{}, err
	}
	cur, exists := preps[m.ID]
	changed := !exists || cur.Attendance != in.Attendance || !jsonEqual(cur.Data, data)
	var before *Preparation
	if exists {
		before = &Preparation{Attendance: cur.Attendance, Data: cur.Data}
	}

	// 本人宛ての未回答の確認タスクは回答済みにする（担当の引き受けや投票は作らない）。
	cs, err := tx.LatestCase(ctx, sess.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Response{}, err
	}
	answered := false
	if err == nil {
		tasks, err := tx.TasksByCase(ctx, cs.ID)
		if err != nil {
			return store.Response{}, err
		}
		for _, tk := range tasks {
			if tk.MemberID == m.ID && tk.Kind == store.TaskPreparation && tk.Status == store.TaskOpen && now.Before(tk.DueAt) {
				ok, err := tx.AnswerTask(ctx, tk.ID, "submit", now)
				if err != nil {
					return store.Response{}, err
				}
				answered = answered || ok
			}
		}
	}

	if changed {
		if err := tx.PutPreparation(ctx, store.Preparation{SessionID: sess.ID, MemberID: m.ID, Attendance: in.Attendance, Data: data, UpdatedAt: now}); err != nil {
			return store.Response{}, err
		}
		if err := bump(ctx, tx, sess, now); err != nil {
			return store.Response{}, err
		}
		cs, err = c.onInputChanged(ctx, tx, sess, "", now)
		if err != nil {
			return store.Response{}, err
		}
		summary := m.DisplayName + "さんが参加条件を更新しました（" + entryLabel(source) + "）。"
		if lines := preparationDiff(pb, s, before, Preparation{Attendance: in.Attendance, Data: data}); len(lines) > 0 {
			summary += "\n・" + strings.Join(lines, "\n・")
		}
		if err := c.activity(ctx, tx, *sess, cs.ID, "input_received", summary, "", now); err != nil {
			return store.Response{}, err
		}
	} else if answered && cs.Open() {
		// 値が同じなら版は変えず、回答が揃ったかだけを確認する。
		if err := c.activity(ctx, tx, *sess, cs.ID, "input_received", m.DisplayName+"さんが参加条件を確認しました（変更なし・"+entryLabel(source)+"）。", "", now); err != nil {
			return store.Response{}, err
		}
		if err := c.advance(ctx, tx, *sess, &cs, now); err != nil {
			return store.Response{}, err
		}
	}
	caseID, err := latestCaseID(ctx, tx, sess.ID)
	if err != nil {
		return store.Response{}, err
	}
	return accepted(*sess, caseID), nil
}

// Withdraw は本人の担当辞退・欠席を登録する（docs/api-endpoint.md）。私的な理由は受け取らない。
func (c *Coordinator) Withdraw(ctx context.Context, userID, sessionID string, in apitypes.WithdrawalInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		sess, m, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return store.Response{}, err
		}
		if err := checkNotStarted(sess, now); err != nil {
			return store.Response{}, err
		}
		if err := checkRevision(sess, in.ExpectedRevision); err != nil {
			return store.Response{}, err
		}
		if in.Scope != WithdrawAssignment && in.Scope != WithdrawAttendance {
			return store.Response{}, apperr.Validation(apperr.Field{Path: "scope", Message: "assignment または attendance を指定してください"})
		}
		pb, err := c.playbook(sess.PlaybookID)
		if err != nil {
			return store.Response{}, err
		}
		s, err := c.snapshot(ctx, tx, sess, nil)
		if err != nil {
			return store.Response{}, err
		}
		cur := s.Preparation(m.ID)
		var curData []byte
		attendance := AttendanceAttending
		if cur != nil {
			curData, attendance = cur.Data, cur.Attendance
		}
		if in.Scope == WithdrawAttendance {
			attendance = AttendanceAbsent
		}
		data, err := pb.ApplyWithdrawal(ctx, s, in.Scope, curData)
		if err != nil {
			return store.Response{}, err
		}
		// 既に同じ辞退状態なら、二重の案件を作らず現在の受付結果を返す。
		if cur != nil && cur.Attendance == attendance && jsonEqual(cur.Data, data) {
			caseID, err := latestCaseID(ctx, tx, sess.ID)
			if err != nil {
				return store.Response{}, err
			}
			return accepted(sess, caseID), nil
		}
		if in.Scope == WithdrawAssignment {
			has, err := c.hasAssignment(ctx, tx, pb, sess, m.ID)
			if err != nil {
				return store.Response{}, err
			}
			if !has {
				return store.Response{}, apperr.InvalidStateErr("辞退できる担当がありません。")
			}
		}
		if err := tx.PutPreparation(ctx, store.Preparation{SessionID: sess.ID, MemberID: m.ID, Attendance: attendance, Data: data, UpdatedAt: now}); err != nil {
			return store.Response{}, err
		}
		// 辞退に伴う本人の未回答タスク（確認依頼）は回答済みとして閉じる。
		if cs, err := tx.LatestCase(ctx, sess.ID); err == nil {
			tasks, err := tx.TasksByCase(ctx, cs.ID)
			if err != nil {
				return store.Response{}, err
			}
			for _, tk := range tasks {
				if tk.MemberID == m.ID && tk.Kind == store.TaskPreparation && tk.Status == store.TaskOpen {
					if _, err := tx.AnswerTask(ctx, tk.ID, "submit", now); err != nil {
						return store.Response{}, err
					}
				}
			}
		}
		if err := bump(ctx, tx, &sess, now); err != nil {
			return store.Response{}, err
		}
		cs, err := c.onInputChanged(ctx, tx, &sess, m.ID, now)
		if err != nil {
			return store.Response{}, err
		}
		what := "担当を辞退"
		if in.Scope == WithdrawAttendance {
			what = "欠席を登録"
		}
		if err := c.activity(ctx, tx, sess, cs.ID, "input_received", fmt.Sprintf("%sさんが%sしました。", m.DisplayName, what), "", now); err != nil {
			return store.Response{}, err
		}
		caseID, err := latestCaseID(ctx, tx, sess.ID)
		if err != nil {
			return store.Response{}, err
		}
		return accepted(sess, caseID), nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// hasAssignment は確定計画または現在の案に本人の担当があるかを返す。
func (c *Coordinator) hasAssignment(ctx context.Context, tx *store.Tx, pb Playbook, sess store.Session, memberID string) (bool, error) {
	var plans []store.Proposal
	if sess.ConfirmedProposalID != "" {
		p, err := tx.Proposal(ctx, sess.ConfirmedProposalID)
		if err != nil {
			return false, err
		}
		plans = append(plans, p)
	}
	pending, err := tx.PendingProposals(ctx, sess.ID)
	if err != nil {
		return false, err
	}
	plans = append(plans, pending...)
	for _, p := range plans {
		ids, err := pb.Assignees(p.Data)
		if err != nil {
			return false, err
		}
		if contains(ids, memberID) {
			return true, nil
		}
	}
	return false, nil
}

var allowedDecisions = map[string][]string{
	store.TaskPreparation:   {"submit"},
	store.TaskAssignment:    {"accept", "decline"},
	store.TaskApproval:      {"approve", "reject"},
	store.TaskOwnerApproval: {"approve", "reject"},
}

// RespondTask は確認への回答・担当の引き受け・投票・承認を記録する（docs/api-endpoint.md）。
func (c *Coordinator) RespondTask(ctx context.Context, userID, taskID string, in apitypes.TaskResponseInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		tk, sess, m, err := c.taskAccess(ctx, tx, userID, taskID)
		if err != nil {
			return store.Response{}, err
		}
		if err := checkTaskResponseShape(tk, in); err != nil {
			return store.Response{}, err
		}
		// 回答済み・期限切れ・旧版のタスクは拒否する。
		switch {
		case tk.Status == store.TaskAnswered:
			return store.Response{}, apperr.New(apperr.TaskClosed, "このタスクには回答済みです。")
		case tk.Status == store.TaskObsolete && tk.ProposalID != "":
			return store.Response{}, apperr.New(apperr.ProposalSuperseded, "案が更新されています。最新の案を確認してください。")
		case tk.Status == store.TaskObsolete:
			return store.Response{}, apperr.New(apperr.TaskClosed, "このタスクは不要になりました。")
		case tk.Status == store.TaskExpired || !now.Before(tk.DueAt):
			return store.Response{}, apperr.New(apperr.TaskExpired, "回答期限を過ぎています。")
		}
		if err := checkNotStarted(sess, now); err != nil {
			return store.Response{}, err
		}
		if tk.Kind == store.TaskPreparation {
			if err := checkRevision(sess, in.ExpectedRevision); err != nil {
				return store.Response{}, err
			}
			return c.submitPreparation(ctx, tx, &sess, m, in.Preparation, "preparation", EntryWeb, now)
		}

		p, err := tx.Proposal(ctx, tk.ProposalID)
		if err != nil {
			return store.Response{}, err
		}
		if *in.ProposalID != p.ID || *in.ProposalVersion != p.Version || p.Status != store.ProposalPending {
			return store.Response{}, apperr.New(apperr.ProposalSuperseded, "案が更新されています。最新の案を確認してください。")
		}
		if ok, err := tx.AnswerTask(ctx, tk.ID, in.Decision, now); err != nil {
			return store.Response{}, err
		} else if !ok {
			return store.Response{}, apperr.New(apperr.TaskClosed, "このタスクには回答済みです。")
		}
		cs, err := tx.Case(ctx, tk.CaseID)
		if err != nil {
			return store.Response{}, err
		}
		if err := c.activity(ctx, tx, sess, cs.ID, "response_recorded", fmt.Sprintf("%sさんが案（版%d）に回答しました（%s）。", m.DisplayName, p.Version, decisionLabel(tk.Kind, in.Decision)), p.ID, now); err != nil {
			return store.Response{}, err
		}
		if err := c.evaluate(ctx, tx, &sess, &cs, p, now); err != nil {
			return store.Response{}, err
		}
		return accepted(sess, cs.ID), nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

func decisionLabel(kind, decision string) string {
	switch decision {
	case "accept":
		return "担当を引き受け"
	case "decline":
		return "担当を辞退"
	case "approve":
		if kind == store.TaskOwnerApproval {
			return "承認"
		}
		return "賛成"
	case "reject":
		if kind == store.TaskOwnerApproval {
			return "不承認"
		}
		return "反対"
	}
	return decision
}

// checkTaskResponseShape はタスクの種類に対応する形の本文だけを受け付ける。
func checkTaskResponseShape(tk store.Task, in apitypes.TaskResponseInput) error {
	allowed := allowedDecisions[tk.Kind]
	if !contains(allowed, in.Decision) {
		return apperr.Validation(apperr.Field{Path: "decision", Message: fmt.Sprintf("このタスクで使える回答は %v です", allowed)})
	}
	var fields []apperr.Field
	if tk.Kind == store.TaskPreparation {
		if in.ProposalID != nil {
			fields = append(fields, apperr.Field{Path: "proposal_id", Message: "このタスクでは指定できません"})
		}
		if in.ProposalVersion != nil {
			fields = append(fields, apperr.Field{Path: "proposal_version", Message: "このタスクでは指定できません"})
		}
		if in.ExpectedRevision == nil {
			fields = append(fields, apperr.Field{Path: "expected_revision", Message: "必須です"})
		}
		if in.Preparation == nil {
			fields = append(fields, apperr.Field{Path: "preparation", Message: "必須です"})
		}
	} else {
		if in.ExpectedRevision != nil {
			fields = append(fields, apperr.Field{Path: "expected_revision", Message: "このタスクでは指定できません"})
		}
		if in.Preparation != nil {
			fields = append(fields, apperr.Field{Path: "preparation", Message: "このタスクでは指定できません"})
		}
		if in.ProposalID == nil {
			fields = append(fields, apperr.Field{Path: "proposal_id", Message: "必須です"})
		}
		if in.ProposalVersion == nil {
			fields = append(fields, apperr.Field{Path: "proposal_version", Message: "必須です"})
		}
	}
	if len(fields) > 0 {
		return apperr.Validation(fields...)
	}
	return nil
}

// SubmitProposal は管理者の判断待ちのときに代案を提出する（docs/api-endpoint.md）。
// DeleteSession は開催回を削除する。開催回を作れるのは管理者だけなので、削除も管理者に限る。
// 開催前後・案件の状態によらず削除でき、未送信の通知や予定していた処理も取り消される。
func (c *Coordinator) DeleteSession(ctx context.Context, userID, sessionID string, idem *store.IdemKey) (store.Response, error) {
	return c.st.Idempotent(ctx, idem, c.now(), func(tx *store.Tx) (store.Response, error) {
		sess, m, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return store.Response{}, err
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		if err := tx.DeleteSession(ctx, sess.ID); err != nil {
			return store.Response{}, err
		}
		c.log.Info("開催回を削除", "session_id", sess.ID, "group_id", sess.GroupID, "member_id", m.ID)
		return store.Response{Status: http.StatusOK, Body: encode(apitypes.SessionDeleted{SessionID: sess.ID, GroupID: sess.GroupID})}, nil
	})
}

func (c *Coordinator) SubmitProposal(ctx context.Context, userID, sessionID string, in apitypes.SubmitProposalInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		sess, m, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return store.Response{}, err
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		if err := checkNotStarted(sess, now); err != nil {
			return store.Response{}, err
		}
		if err := checkRevision(sess, in.ExpectedRevision); err != nil {
			return store.Response{}, err
		}
		cs, err := tx.LatestCase(ctx, sess.ID)
		if err != nil || cs.Status != store.CaseNeedsOwner {
			return store.Response{}, apperr.InvalidStateErr("管理者の判断待ちのときだけ代案を提出できます。")
		}
		if !canSecure(now, sess.StartsAt) {
			return store.Response{}, apperr.InvalidStateErr("新しい回答期限を確保できないため、代案を提出できません。")
		}
		s, err := c.snapshot(ctx, tx, sess, &cs)
		if err != nil {
			return store.Response{}, err
		}
		for _, p := range s.Preparations {
			if p.Value == nil {
				return store.Response{}, apperr.InvalidStateErr("全員の参加条件の回答が揃っていません。")
			}
		}
		// 期限切れで残っていたタスクは無効にする。
		if err := tx.CloseOpenTasksByCase(ctx, cs.ID, store.TaskObsolete); err != nil {
			return store.Response{}, err
		}
		_, err = c.createProposal(ctx, tx, &sess, &cs, s, "owner", "管理者が代案を提出しました。", in.Data, now)
		switch {
		case errors.Is(err, errNoEligibleVoters):
			return store.Response{}, apperr.Validation(apperr.Field{Path: "data", Message: err.Error()})
		case err != nil:
			return store.Response{}, validationErr(err, "data")
		}
		return accepted(sess, cs.ID), nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}
