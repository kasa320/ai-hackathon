package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// createReadingSessionTx creates only an active reading slot. Planned slots never call it.
func (c *Coordinator) createReadingSessionTx(ctx context.Context, tx *store.Tx, g store.Group, members []store.Member, b store.ReadingBook, slot *store.ReadingBookSlot, in apitypes.ReadingBookSessionInput) (store.Session, error) {
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
	data, _ := json.Marshal(map[string]any{"book_title": b.Title, "isbn": b.ISBN, "toc_source": json.RawMessage(b.TocSource), "sections": json.RawMessage(b.Sections), "completed_section_ids": b.CompletedSectionIDs, "target_section_ids": in.TargetSectionIDs})
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
	return apitypes.ReadingBook{ID: b.ID, GroupID: b.GroupID, Title: b.Title, ISBN: b.ISBN, TocSource: b.TocSource, Sections: b.Sections, PlannedSessionCount: b.PlannedSessionCount, SessionCreationMode: b.SessionCreationMode, Status: b.Status, CompletedSectionIDs: nonNil(b.CompletedSectionIDs), CompletedSessionCount: n, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
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
	out.Sessions = []apitypes.ReadingBookSlot{}
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
		ss, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return e
		}
		out.Book = bookView(b, ss)
		out.Permissions.CanManage = m.Role == RoleOwner
		for _, s := range ss {
			v := apitypes.ReadingBookSlot{SlotID: s.ID, SequenceNumber: s.SequenceNumber, Status: s.Status, CoveredSectionIDs: nonNil(s.CoveredSectionIDs)}
			if s.SessionID != nil {
				x, e := tx.Session(ctx, *s.SessionID)
				if e != nil {
					return e
				}
				z := summaryView(x)
				v.Session = &z
			}
			out.Sessions = append(out.Sessions, v)
		}
		return nil
	})
	return out, err
}
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
		title, ok := validName(in.Title)
		if !ok {
			return store.Response{}, apperr.Validation(apperr.Field{Path: "title", Message: "1〜100文字で入力してください"})
		}
		if in.PlannedSessionCount < 1 || in.PlannedSessionCount > 52 {
			return store.Response{}, apperr.Validation(apperr.Field{Path: "planned_session_count", Message: "1〜52で指定してください"})
		}
		if in.SessionCreationMode != "sequential" && in.SessionCreationMode != "all" {
			return store.Response{}, apperr.Validation(apperr.Field{Path: "session_creation_mode", Message: "sequential または all を指定してください"})
		}
		members, e := tx.Members(ctx, g.ID)
		if e != nil {
			return store.Response{}, e
		}
		b := store.ReadingBook{ID: store.NewID("book"), GroupID: g.ID, Title: title, ISBN: in.ISBN, TocSource: in.TocSource, Sections: in.Sections, PlannedSessionCount: in.PlannedSessionCount, SessionCreationMode: in.SessionCreationMode, Status: "in_progress", CompletedSectionIDs: []string{}, CreatedAt: now, UpdatedAt: now}
		if err := tx.CreateReadingBook(ctx, b); err != nil {
			return store.Response{}, err
		}
		count := 1
		if b.SessionCreationMode == "all" {
			count = b.PlannedSessionCount
		}
		var first store.ReadingBookSlot
		for i := 1; i <= count; i++ {
			s := store.ReadingBookSlot{ID: store.NewID("slot"), BookID: b.ID, SequenceNumber: i, Status: "planned", CoveredSectionIDs: []string{}}
			if err := tx.CreateReadingBookSlot(ctx, s); err != nil {
				return store.Response{}, err
			}
			if i == 1 {
				first = s
			}
		}
		sess, e := c.createReadingSessionTx(ctx, tx, g, members, b, &first, in.InitialSession)
		if e != nil {
			return store.Response{}, e
		}
		started, err := tx.StartReadingBookSlot(ctx, first.ID, sess.ID)
		if err != nil {
			return store.Response{}, err
		}
		if !started {
			return store.Response{}, apperr.InvalidStateErr("未開始の枠だけを開始できます。")
		}
		first.Status = "active"
		first.SessionID = &sess.ID
		allSlots, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		return store.Response{Status: http.StatusCreated, Body: encode(apitypes.CreateReadingBookResult{Book: bookView(b, allSlots), InitialSession: summaryView(sess)})}, nil
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
		sess, e := c.createReadingSessionTx(ctx, tx, g, members, b, slot, in)
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
		slots, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		out := apitypes.ReadingBookDetail{Book: bookView(b, slots), Sessions: []apitypes.ReadingBookSlot{}}
		out.Permissions.CanManage = true
		for _, s := range slots {
			v := apitypes.ReadingBookSlot{SlotID: s.ID, SequenceNumber: s.SequenceNumber, Status: s.Status, CoveredSectionIDs: nonNil(s.CoveredSectionIDs)}
			if s.SessionID != nil {
				x, e := tx.Session(ctx, *s.SessionID)
				if e != nil {
					return store.Response{}, e
				}
				z := summaryView(x)
				v.Session = &z
			}
			out.Sessions = append(out.Sessions, v)
		}
		return store.Response{Status: http.StatusOK, Body: encode(out)}, nil
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
