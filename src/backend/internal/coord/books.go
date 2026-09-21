package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// createReadingSessionTx creates only an active reading slot. Planned slots never call it.
// assignee が空でなければ、その人を今回の発表の担当としてセッションデータに固定する。
func (c *Coordinator) createReadingSessionTx(ctx context.Context, tx *store.Tx, g store.Group, members []store.Member, b store.ReadingBook, slot *store.ReadingBookSlot, in apitypes.ReadingBookSessionInput, assignee string) (store.Session, error) {
	if len(members) < MinGroupSize {
		return store.Session{}, apperr.InvalidStateErr("開催回を作るには、在籍メンバーが2人以上必要です。")
	}
	for _, m := range members {
		if !m.Joined() {
			return store.Session{}, apperr.New(apperr.MembersNotJoined, "まだログインしていないメンバーがいます。")
		}
	}
	if in.DurationMinutes < MinDuration || in.DurationMinutes > MaxDuration {
		return store.Session{}, apperr.Validation(apperr.Field{Path: "duration_minutes", Message: fmt.Sprintf("%d〜%d分で指定してください", MinDuration, MaxDuration)})
	}
	var sections []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(b.Sections, &sections); err != nil {
		return store.Session{}, err
	}
	known := map[string]bool{}
	for _, s := range sections {
		known[s.ID] = true
	}
	if len(in.TargetSectionIDs) == 0 {
		return store.Session{}, apperr.Validation(apperr.Field{Path: "target_section_ids", Message: "今回扱う範囲を1件以上選んでください"})
	}
	for i, id := range in.TargetSectionIDs {
		if !known[id] {
			return store.Session{}, apperr.Validation(apperr.Field{Path: fmt.Sprintf("target_section_ids[%d]", i), Message: "登録済みの節を指定してください"})
		}
	}
	// SessionData is intentionally a snapshot: later book changes cannot rewrite an active session.
	payload := map[string]any{"book_title": b.Title, "isbn": b.ISBN, "toc_source": json.RawMessage(b.TocSource), "sections": json.RawMessage(b.Sections), "completed_section_ids": nonNil(b.CompletedSectionIDs), "target_section_ids": in.TargetSectionIDs}
	if assignee != "" {
		payload["assignee_member_id"] = assignee
	}
	data, _ := json.Marshal(payload)
	pb, err := c.playbook("reading")
	if err != nil {
		return store.Session{}, err
	}
	norm, err := pb.ValidateSessionData(ctx, SessionParams{DurationMinutes: in.DurationMinutes}, data)
	if err := validationErr(err, "data"); err != nil {
		return store.Session{}, err
	}
	now := c.now()
	start, end, starts, fields := parsePeriod(in.PeriodStart, in.PeriodEnd, now)
	if len(fields) > 0 {
		return store.Session{}, apperr.Validation(fields...)
	}
	sess := store.Session{ID: store.NewID("ses"), GroupID: g.ID, PlaybookID: "reading", Title: fmt.Sprintf("%s 第%d回", b.Title, slot.SequenceNumber), StartsAt: starts, PeriodStart: start, PeriodEnd: end, ScheduleStatus: store.ScheduleProposed, DurationMinutes: in.DurationMinutes, Revision: 1, Status: sessionDraft, Data: norm, CreatedAt: now, UpdatedAt: now}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	if err := tx.CreateSession(ctx, sess, ids); err != nil {
		return store.Session{}, err
	}
	cs := store.Case{ID: store.NewID("case"), SessionID: sess.ID, Status: store.CaseCollecting, Summary: "参加できる日時を確認しています。", CreatedAt: now, UpdatedAt: now}
	if err := tx.CreateCase(ctx, cs); err != nil {
		return store.Session{}, err
	}
	due, _ := dueAt(now, responseHorizon(sess))
	for _, m := range members {
		if err := c.createTask(ctx, tx, sess, cs, m, store.TaskPreparation, "参加できそうな日や時間帯を教えてください。日時はこのあと提案します。", nil, "system", due, now); err != nil {
			return store.Session{}, err
		}
	}
	if err := c.activity(ctx, tx, sess, cs.ID, "input_received", fmt.Sprintf("ブックの第%d回を開始し、参加条件と出られない日を集めています。", slot.SequenceNumber), "", now); err != nil {
		return store.Session{}, err
	}
	return sess, nil
}

func bookView(b store.ReadingBook, slots []store.ReadingBookSlot) apitypes.ReadingBook {
	n := 0
	for _, s := range slots {
		if s.Status == "completed" {
			n++
		}
	}
	return apitypes.ReadingBook{ID: b.ID, GroupID: b.GroupID, Title: b.Title, ISBN: b.ISBN, TocSource: b.TocSource, Sections: b.Sections,
		PeriodStart: b.PeriodStart, PeriodEnd: b.PeriodEnd, PlannedSessionCount: b.PlannedSessionCount, DurationMinutes: b.DurationMinutes,
		AdjustmentLeadDays: b.AdjustmentLeadDays, PlanStatus: b.PlanStatus, PlanVersion: b.PlanVersion, PlanSummary: b.PlanSummary,
		SessionCreationMode: b.SessionCreationMode, Status: b.Status, CompletedSectionIDs: nonNil(b.CompletedSectionIDs), CompletedSessionCount: n, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
}
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (c *Coordinator) ListReadingBooks(ctx context.Context, userID, groupID string) (apitypes.ReadingBookList, error) {
	out := apitypes.ReadingBookList{Items: []apitypes.ReadingBook{}}
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		if _, _, e := c.groupAccess(ctx, tx, userID, groupID); e != nil {
			return e
		}
		bs, e := tx.ReadingBooks(ctx, groupID)
		if e != nil {
			return e
		}
		for _, b := range bs {
			ss, e := tx.ReadingBookSlots(ctx, b.ID)
			if e != nil {
				return e
			}
			v := bookView(b, ss)
			v.TocSource, v.Sections = nil, nil // list contract intentionally excludes book-detail fields
			out.Items = append(out.Items, v)
		}
		return nil
	})
	return out, err
}

func (c *Coordinator) ReadingBookDetail(ctx context.Context, userID, groupID, bookID string) (apitypes.ReadingBookDetail, error) {
	var out apitypes.ReadingBookDetail
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		_, m, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return e
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return apperr.NotFoundErr()
		}
		if e != nil {
			return e
		}
		out, e = c.bookDetail(ctx, tx, b, m)
		return e
	})
	return out, err
}

// bookDetail は本人から見たブックの詳細を組み立てる。本人向けの操作可否は本人のメンバーIDで決まる。
func (c *Coordinator) bookDetail(ctx context.Context, tx *store.Tx, b store.ReadingBook, me store.Member) (apitypes.ReadingBookDetail, error) {
	out := apitypes.ReadingBookDetail{Sessions: []apitypes.ReadingBookSlot{}}
	slots, err := tx.ReadingBookSlots(ctx, b.ID)
	if err != nil {
		return out, err
	}
	out.Book = bookView(b, slots)
	out.Permissions.CanManage = me.Role == RoleOwner
	started := false
	for _, s := range slots {
		started = started || s.SessionID != nil || s.Status != "planned"
		v, err := c.slotView(ctx, tx, b, s, me)
		if err != nil {
			return out, err
		}
		if s.AssigneeMemberID == me.ID && s.AssignmentStatus == store.AssignPending || s.ProposedAssigneeID == me.ID && s.AssignmentStatus == store.AssignChangeProposed {
			out.Permissions.CanRespondAssignment = true
		}
		out.Sessions = append(out.Sessions, v)
	}
	out.Permissions.CanEdit = out.Permissions.CanManage && !started
	return out, nil
}

func (c *Coordinator) slotView(ctx context.Context, tx *store.Tx, b store.ReadingBook, s store.ReadingBookSlot, me store.Member) (apitypes.ReadingBookSlot, error) {
	v := apitypes.ReadingBookSlot{SlotID: s.ID, SequenceNumber: s.SequenceNumber, Status: s.Status, CoveredSectionIDs: nonNil(s.CoveredSectionIDs),
		PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd, TargetSectionIDs: nonNil(s.TargetSectionIDs), AssignmentStatus: s.AssignmentStatus}
	if s.AssigneeMemberID != "" {
		v.AssigneeMemberID = &s.AssigneeMemberID
	}
	if s.ProposedAssigneeID != "" {
		v.ProposedAssigneeMemberID = &s.ProposedAssigneeID
	}
	var sess *store.Session
	if s.SessionID != nil {
		x, e := tx.Session(ctx, *s.SessionID)
		if e != nil {
			return v, e
		}
		sess = &x
		z := summaryView(x)
		v.Session = &z
	}
	v.SchedulingStatus = c.schedulingStatus(ctx, tx, b, s, sess)
	if s.Status == "planned" && b.PlanStatus != store.BookPlanLegacy && s.PeriodStart != "" {
		on := adjustmentStart(b, s).Format("2006-01-02")
		v.AdjustmentStartsOn = &on
	}
	if s.AssigneeMemberID != "" {
		conf, e := tx.SlotConfirmation(ctx, s.ID, s.AssigneeMemberID)
		switch {
		case e == nil:
			v.AssigneeConfirmationStatus = &conf.Status
			v.CanConfirmAssignment = s.AssigneeMemberID == me.ID && (conf.Status == store.ConfirmOpen || conf.Status == store.ConfirmNeedsOwner)
		case !errors.Is(e, store.ErrNotFound):
			return v, e
		}
	}
	return v, nil
}

// schedulingStatus は枠の日程調整の進み具合を、実セッションの状態から導く。別に保存しないため、
// セッション側の確定・管理者判断待ちと食い違わない。
func (c *Coordinator) schedulingStatus(ctx context.Context, tx *store.Tx, b store.ReadingBook, s store.ReadingBookSlot, sess *store.Session) string {
	switch {
	case s.Status == "completed":
		return "completed"
	case sess == nil:
		if s.AttentionReason != "" {
			return "needs_attention"
		}
		return "waiting"
	case scheduleStatus(*sess) == store.ScheduleConfirmed && sess.Status != sessionAttn:
		return "scheduled"
	}
	if cs, err := tx.LatestCase(ctx, sess.ID); err == nil && cs.Status == store.CaseNeedsOwner {
		return "needs_attention"
	}
	if scheduleStatus(*sess) == store.ScheduleConfirmed {
		return "scheduled"
	}
	return "scheduling"
}

// bookDraft は登録・編集で検証する入力。編集では未指定の項目を現在の値で埋めてから検証する。
type bookDraft struct {
	Title                  string
	ISBN                   *string
	Toc, Sections          json.RawMessage
	PeriodStart, PeriodEnd string
	Count, Duration, Lead  int
}

// 日程調整リード日数の範囲。推奨は7日（UIは7日・14日を提示する）。
const (
	MinLeadDays = 1
	MaxLeadDays = 30
	// bookPeriodMaxDays は1冊で扱える全体期間の上限（毎週52回を超えて収まる長さ）。
	bookPeriodMaxDays = 731
)

func (c *Coordinator) bookPlanner(g store.Group) (BookPlanner, error) {
	pb, err := c.playbook(g.PlaybookID)
	if err != nil {
		return nil, apperr.New(apperr.UnsupportedPlaybook, "対応していない用途です。")
	}
	bp, ok := pb.(BookPlanner)
	if !ok {
		return nil, apperr.New(apperr.UnsupportedPlaybook, "この用途ではブックを扱えません。")
	}
	return bp, nil
}

func validationFields(err error) []apperr.Field {
	var v *ValidationError
	if !errors.As(err, &v) {
		return nil
	}
	var out []apperr.Field
	for _, f := range v.Fields {
		out = append(out, apperr.Field{Path: f.Path, Message: f.Message})
	}
	return out
}

// checkBookDraft は入力を検証し、正規化した値と全枠の基本の割り当て（章割り・開催目安）を返す。
// checkPast が true のとき、全体開始日が今日より前なら拒否する（登録時と、開始日を変える編集時）。
func (c *Coordinator) checkBookDraft(bp BookPlanner, d bookDraft, now time.Time, checkPast bool) (bookDraft, []BookPlanSlot, error) {
	var fields []apperr.Field
	title, ok := validName(d.Title)
	if !ok {
		fields = append(fields, apperr.Field{Path: "title", Message: fmt.Sprintf("1〜%d文字で入力してください", MaxNameLen)})
	}
	if d.Count < 1 || d.Count > 52 {
		fields = append(fields, apperr.Field{Path: "planned_session_count", Message: "1〜52で指定してください"})
	}
	if d.Duration < MinDuration || d.Duration > MaxDuration {
		fields = append(fields, apperr.Field{Path: "duration_minutes", Message: fmt.Sprintf("%d〜%d分で指定してください", MinDuration, MaxDuration)})
	}
	if d.Lead < MinLeadDays || d.Lead > MaxLeadDays {
		fields = append(fields, apperr.Field{Path: "adjustment_lead_days", Message: fmt.Sprintf("%d〜%d日で指定してください", MinLeadDays, MaxLeadDays)})
	}
	start, errStart := time.ParseInLocation("2006-01-02", strings.TrimSpace(d.PeriodStart), displayZone)
	end, errEnd := time.ParseInLocation("2006-01-02", strings.TrimSpace(d.PeriodEnd), displayZone)
	if errStart != nil {
		fields = append(fields, apperr.Field{Path: "period_start", Message: "開始日を YYYY-MM-DD で入力してください"})
	}
	if errEnd != nil {
		fields = append(fields, apperr.Field{Path: "period_end", Message: "終了日を YYYY-MM-DD で入力してください"})
	}
	if errStart == nil && errEnd == nil {
		today := now.In(displayZone)
		today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, displayZone)
		switch {
		case end.Before(start):
			fields = append(fields, apperr.Field{Path: "period_end", Message: "開始日と同じ日か、それより後の日を指定してください"})
		case end.After(start.AddDate(0, 0, bookPeriodMaxDays)):
			fields = append(fields, apperr.Field{Path: "period_end", Message: "開始日から2年以内で指定してください"})
		case checkPast && start.Before(today):
			fields = append(fields, apperr.Field{Path: "period_start", Message: "開始日は今日以降にしてください"})
		}
	}
	mat, err := bp.ValidateBookMaterial(title, d.ISBN, d.Toc, d.Sections)
	if err != nil {
		if extra := validationFields(err); extra != nil {
			fields = append(fields, extra...)
		} else {
			return d, nil, err
		}
	}
	if len(fields) > 0 {
		return d, nil, apperr.Validation(fields...)
	}
	slots, err := bp.SplitBook(mat.List, start.Format("2006-01-02"), end.Format("2006-01-02"), d.Count)
	if err != nil {
		if extra := validationFields(err); extra != nil {
			return d, nil, apperr.Validation(extra...)
		}
		return d, nil, err
	}
	d.Title, d.ISBN, d.Toc, d.Sections = mat.Title, mat.ISBN, mat.TocSource, mat.Sections
	d.PeriodStart, d.PeriodEnd = start.Format("2006-01-02"), end.Format("2006-01-02")
	return d, slots, nil
}

// createSlots は全枠を登録する。実セッションは作らないため、参加条件のタスクも通知も作られない。
func createSlots(ctx context.Context, tx *store.Tx, bookID string, plan []BookPlanSlot) error {
	for _, ps := range plan {
		s := store.ReadingBookSlot{ID: store.NewID("slot"), BookID: bookID, SequenceNumber: ps.Sequence, Status: "planned", CoveredSectionIDs: []string{},
			PeriodStart: ps.PeriodStart, PeriodEnd: ps.PeriodEnd, TargetSectionIDs: ps.SectionIDs, AssignmentStatus: store.AssignUnassigned}
		if err := tx.CreateReadingBookSlot(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func bookLog(ctx context.Context, tx *store.Tx, b store.ReadingBook, slotID, memberID, kind, summary string, now time.Time) error {
	return tx.AddBookLog(ctx, store.BookLog{BookID: b.ID, SlotID: slotID, MemberID: memberID, Kind: kind, Summary: summary, OccurredAt: now})
}

// CreateReadingBook はブックを1冊登録する。全枠（章割り・開催目安）を作り、AIの全体計画を待つ状態にする。
// 実セッション・参加条件のタスク・通知は作らない。1冊ごとに独立した再送可能な登録で、
// 複数のブックを同じグループへ登録して同時に進められる。既存のブックの割り当ては変更しない。
func (c *Coordinator) CreateReadingBook(ctx context.Context, userID, groupID string, in apitypes.CreateReadingBookInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		g, m, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		bp, e := c.bookPlanner(g)
		if e != nil {
			return store.Response{}, e
		}
		d, plan, e := c.checkBookDraft(bp, bookDraft{Title: in.Title, ISBN: in.ISBN, Toc: in.TocSource, Sections: in.Sections, PeriodStart: in.PeriodStart, PeriodEnd: in.PeriodEnd,
			Count: in.PlannedSessionCount, Duration: in.DurationMinutes, Lead: in.AdjustmentLeadDays}, now, true)
		if e != nil {
			return store.Response{}, e
		}
		b := store.ReadingBook{ID: store.NewID("book"), GroupID: g.ID, Title: d.Title, ISBN: d.ISBN, TocSource: d.Toc, Sections: d.Sections, PlannedSessionCount: d.Count,
			SessionCreationMode: "all", Status: "in_progress", CompletedSectionIDs: []string{}, CreatedAt: now, UpdatedAt: now,
			PeriodStart: d.PeriodStart, PeriodEnd: d.PeriodEnd, DurationMinutes: d.Duration, AdjustmentLeadDays: d.Lead,
			PlanStatus: store.BookPlanPlanning, PlanVersion: 1}
		if e := tx.CreateReadingBook(ctx, b); e != nil {
			return store.Response{}, e
		}
		if e := createSlots(ctx, tx, b.ID, plan); e != nil {
			return store.Response{}, e
		}
		if e := bookLog(ctx, tx, b, "", m.ID, "book_registered", fmt.Sprintf("ブックを登録し、全%d回の枠を作りました。全体計画の作成を待っています。", d.Count), now); e != nil {
			return store.Response{}, e
		}
		slots, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		return store.Response{Status: http.StatusCreated, Body: encode(apitypes.CreateReadingBookResult{Book: bookView(b, slots)}), Location: "/api/groups/" + g.ID + "/reading/books/" + b.ID}, nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// resetBookPlan は構造・計画の変更を受けて、以前の担当と承認を無効にし、計画を作り直す状態へ戻す。
func resetBookPlan(ctx context.Context, tx *store.Tx, b *store.ReadingBook, reason string, now time.Time) error {
	b.PlanStatus, b.PlanSummary, b.PlanAttempts, b.PlanNextAt, b.PlanReason = store.BookPlanPlanning, "", 0, nil, ""
	b.PlanVersion++
	b.UpdatedAt = now
	return bookLog(ctx, tx, *b, "", "", "plan_invalidated", reason, now)
}

// UpdateReadingBook は最初の実セッションが始まる前のブックを編集する。始まった後は拒否する。
// 構造・計画に関わる項目（章・期間・回数・長さ・リード日数）を変えると、担当の承認を無効にして計画を再生成する。
// 書名・ISBN・目次情報だけの変更では承認を保つ。
func (c *Coordinator) UpdateReadingBook(ctx context.Context, userID, groupID, bookID string, in apitypes.UpdateReadingBookInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		g, m, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		slots, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		if b.PlanStatus == store.BookPlanLegacy || hasStartedSlot(slots) {
			return store.Response{}, apperr.InvalidStateErr("最初のセッションが始まったブックは編集できません。")
		}
		bp, e := c.bookPlanner(g)
		if e != nil {
			return store.Response{}, e
		}
		d := bookDraft{Title: b.Title, ISBN: b.ISBN, Toc: b.TocSource, Sections: b.Sections, PeriodStart: b.PeriodStart, PeriodEnd: b.PeriodEnd,
			Count: b.PlannedSessionCount, Duration: b.DurationMinutes, Lead: b.AdjustmentLeadDays}
		if in.Title != nil {
			d.Title = *in.Title
		}
		if in.ISBN != nil {
			d.ISBN = in.ISBN
		}
		if len(in.TocSource) > 0 {
			d.Toc = in.TocSource
		}
		if len(in.Sections) > 0 {
			d.Sections = in.Sections
		}
		if in.PeriodStart != nil {
			d.PeriodStart = *in.PeriodStart
		}
		if in.PeriodEnd != nil {
			d.PeriodEnd = *in.PeriodEnd
		}
		if in.PlannedSessionCount != nil {
			d.Count = *in.PlannedSessionCount
		}
		if in.DurationMinutes != nil {
			d.Duration = *in.DurationMinutes
		}
		if in.AdjustmentLeadDays != nil {
			d.Lead = *in.AdjustmentLeadDays
		}
		nd, plan, e := c.checkBookDraft(bp, d, now, in.PeriodStart != nil && *in.PeriodStart != b.PeriodStart)
		if e != nil {
			return store.Response{}, e
		}
		planChanged := !jsonEqual(nd.Sections, b.Sections) || nd.PeriodStart != b.PeriodStart || nd.PeriodEnd != b.PeriodEnd ||
			nd.Count != b.PlannedSessionCount || nd.Duration != b.DurationMinutes || nd.Lead != b.AdjustmentLeadDays
		metaChanged := nd.Title != b.Title || !jsonEqual(nd.Toc, b.TocSource) || (nd.ISBN == nil) != (b.ISBN == nil) || (nd.ISBN != nil && *nd.ISBN != *b.ISBN)
		if planChanged || metaChanged {
			b.Title, b.ISBN, b.TocSource, b.Sections = nd.Title, nd.ISBN, nd.Toc, nd.Sections
			b.PeriodStart, b.PeriodEnd, b.PlannedSessionCount, b.DurationMinutes, b.AdjustmentLeadDays = nd.PeriodStart, nd.PeriodEnd, nd.Count, nd.Duration, nd.Lead
			b.UpdatedAt = now
			if planChanged {
				if ok, e := tx.DeleteReadingBookSlots(ctx, b.ID); e != nil {
					return store.Response{}, e
				} else if !ok {
					return store.Response{}, apperr.InvalidStateErr("最初のセッションが始まったブックは編集できません。")
				}
				if e := createSlots(ctx, tx, b.ID, plan); e != nil {
					return store.Response{}, e
				}
				if e := resetBookPlan(ctx, tx, &b, "構造・計画が変更されたため、以前の担当承認を無効にして計画を再生成します。", now); e != nil {
					return store.Response{}, e
				}
			}
			if e := tx.UpdateReadingBook(ctx, b); e != nil {
				return store.Response{}, e
			}
		}
		out, e := c.bookDetail(ctx, tx, b, m)
		return store.Response{Status: http.StatusOK, Body: encode(out)}, e
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

func hasStartedSlot(slots []store.ReadingBookSlot) bool {
	for _, s := range slots {
		if s.SessionID != nil || s.Status != "planned" {
			return true
		}
	}
	return false
}

// RegenerateBookPlan は管理者が全体計画を作り直させる。AIの失敗で止まったブックや、承認前に
// 割り当てを見直したいときに使う。以前の担当承認は無効になる。最初のセッションが始まった後は使えない。
func (c *Coordinator) RegenerateBookPlan(ctx context.Context, userID, groupID, bookID string, _ apitypes.RegenerateBookPlanInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		_, m, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		slots, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		if b.PlanStatus != store.BookPlanNeedsAttention && b.PlanStatus != store.BookPlanAwaitingApproval && b.PlanStatus != store.BookPlanApproved || hasStartedSlot(slots) {
			return store.Response{}, apperr.InvalidStateErr("この状態のブックは計画を再生成できません。")
		}
		// 承認済みの担当・変更中の候補を空に戻し、計画を作り直す（章割り・開催目安も作り直される）。
		for _, s := range slots {
			s.AssigneeMemberID, s.ProposedAssigneeID, s.AssignmentStatus = "", "", store.AssignUnassigned
			s.ExcludedMemberIDs, s.ChangeAttempts, s.ChangeNextAt, s.AttentionReason = nil, 0, nil, ""
			if e := tx.UpdateReadingBookSlot(ctx, s); e != nil {
				return store.Response{}, e
			}
		}
		if e := resetBookPlan(ctx, tx, &b, "管理者の依頼で、担当承認を無効にして計画を再生成します。", now); e != nil {
			return store.Response{}, e
		}
		if e := tx.UpdateReadingBook(ctx, b); e != nil {
			return store.Response{}, e
		}
		out, e := c.bookDetail(ctx, tx, b, m)
		return store.Response{Status: http.StatusOK, Body: encode(out)}, e
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

func (c *Coordinator) StartReadingBookSession(ctx context.Context, userID, groupID, bookID string, in apitypes.ReadingBookSessionInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		g, m, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		if b.PlanStatus != store.BookPlanLegacy {
			// 承認済みの計画を持つブックは、各回の調整開始日に自動で始まる。手動で先に始めて計画を迂回させない。
			return store.Response{}, apperr.InvalidStateErr("このブックのセッションは、調整開始日になると自動で始まります。")
		}
		slots, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		var slot *store.ReadingBookSlot
		if b.SessionCreationMode == "all" {
			if in.SlotID == "" {
				return store.Response{}, apperr.Validation(apperr.Field{Path: "slot_id", Message: "all モードでは必要です"})
			}
			for i := range slots {
				if slots[i].ID == in.SlotID {
					slot = &slots[i]
				}
			}
			if slot == nil || slot.Status != "planned" {
				return store.Response{}, apperr.InvalidStateErr("未開始の枠だけを開始できます。")
			}
		} else {
			if in.SlotID != "" {
				return store.Response{}, apperr.Validation(apperr.Field{Path: "slot_id", Message: "sequential モードでは指定しません"})
			}
			if len(slots) >= b.PlannedSessionCount {
				return store.Response{}, apperr.InvalidStateErr("予定回数を超えて作成できません。")
			}
			x := store.ReadingBookSlot{ID: store.NewID("slot"), BookID: b.ID, SequenceNumber: len(slots) + 1, Status: "planned", CoveredSectionIDs: []string{}}
			if e := tx.CreateReadingBookSlot(ctx, x); e != nil {
				return store.Response{}, e
			}
			slot = &x
		}
		members, e := tx.Members(ctx, g.ID)
		if e != nil {
			return store.Response{}, e
		}
		sess, e := c.createReadingSessionTx(ctx, tx, g, members, b, slot, in, "")
		if e != nil {
			return store.Response{}, e
		}
		started, e := tx.StartReadingBookSlot(ctx, slot.ID, sess.ID)
		if e != nil {
			return store.Response{}, e
		}
		if !started {
			return store.Response{}, apperr.InvalidStateErr("未開始の枠だけを開始できます。")
		}
		return store.Response{Status: http.StatusCreated, Body: encode(apitypes.StartReadingBookSessionResult{SlotID: slot.ID, Session: summaryView(sess)})}, nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}
func (c *Coordinator) CompleteReadingBookSession(ctx context.Context, userID, groupID, bookID, slotID string, in apitypes.CompleteReadingBookSessionInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	return c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		_, m, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		slot, e := tx.ReadingBookSlot(ctx, slotID)
		if errors.Is(e, store.ErrNotFound) || slot.BookID != b.ID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		if slot.Status != "active" || slot.SessionID == nil {
			return store.Response{}, apperr.InvalidStateErr("開始済みの枠だけを完了できます。")
		}
		sess, e := tx.Session(ctx, *slot.SessionID)
		if e != nil {
			return store.Response{}, e
		}
		if in.ExpectedRevision == nil || *in.ExpectedRevision != sess.Revision {
			return store.Response{}, apperr.RevisionConflictErr(sess.Revision)
		}
		if sess.ConfirmedProposalID == "" {
			return store.Response{}, apperr.InvalidStateErr("確定した計画がないため完了できません。")
		}
		p, e := tx.Proposal(ctx, sess.ConfirmedProposalID)
		if e != nil {
			return store.Response{}, e
		}
		var d struct {
			Covered []string `json:"covered_section_ids"`
		}
		if e := json.Unmarshal(p.Data, &d); e != nil {
			return store.Response{}, e
		}
		completed, e := tx.CompleteReadingBookSlot(ctx, slot.ID, d.Covered)
		if e != nil {
			return store.Response{}, e
		}
		if !completed {
			return store.Response{}, apperr.InvalidStateErr("開始済みの枠だけを完了できます。")
		}
		seen := map[string]bool{}
		for _, id := range b.CompletedSectionIDs {
			seen[id] = true
		}
		for _, id := range d.Covered {
			seen[id] = true
		}
		b.CompletedSectionIDs = []string{}
		for _, id := range sectionsOrder(b.Sections) {
			if seen[id] {
				b.CompletedSectionIDs = append(b.CompletedSectionIDs, id)
			}
		}
		if len(b.CompletedSectionIDs) == len(sectionsOrder(b.Sections)) {
			b.Status = "completed"
		}
		b.UpdatedAt = now
		if e := tx.UpdateReadingBook(ctx, b); e != nil {
			return store.Response{}, e
		}
		out, e := c.bookDetail(ctx, tx, b, m)
		return store.Response{Status: http.StatusOK, Body: encode(out)}, e
	})
}
func sectionsOrder(raw json.RawMessage) []string {
	var xs []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &xs)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}
