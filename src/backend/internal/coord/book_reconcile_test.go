package coord_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// TestExistingDuplicateAssignmentTaskIsReconciled は、修正前のDBにすでに作られてしまった
// 重複した担当引き受けタスク（ブック側 reading_book_slots.assignment_status で本人承認済みなのに、
// セッション側にも残っている assignment タスク）を、起動時の自動処理（ProcessDue）が
// 安全に解消することを確認する。ブックの担当承認状態は書き換えず、古いタスクへの回答は拒否する。
func TestExistingDuplicateAssignmentTaskIsReconciled(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID)
	h.setNow(jst0(2026, 9, 24))
	h.process()
	h.sess = h.startedSession(b.ID, 1)
	d := h.book("A", b.ID)
	assigneeID := *d.Sessions[0].AssigneeMemberID
	assignee := h.memberName(assigneeID)

	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustPrep(name, "attending", weeklyAll())
	}
	h.process()
	det := h.detail("A")
	if det.CurrentProposal == nil {
		t.Fatalf("案が作られていない: %+v", det.ActiveCase)
	}
	propID := det.CurrentProposal.ID

	// 修正前のDBを模して、担当がすでに固定されているのに重複したタスクを直接作る。
	var taskID string
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		p, err := tx.Proposal(ctx, propID)
		if err != nil {
			return err
		}
		var req struct {
			Acceptors []string          `json:"acceptors"`
			Approvals []json.RawMessage `json:"approvals"`
		}
		if err := json.Unmarshal(p.Requirements, &req); err != nil {
			return err
		}
		req.Acceptors = append(req.Acceptors, assigneeID)
		encoded, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if err := tx.SetProposalRequirements(ctx, p.ID, encoded); err != nil {
			return err
		}
		cs, err := tx.Case(ctx, p.CaseID)
		if err != nil {
			return err
		}
		taskID = store.NewID("task")
		return tx.CreateTask(ctx, store.Task{
			ID: taskID, SessionID: h.sess, CaseID: p.CaseID, MemberID: assigneeID,
			Kind: store.TaskAssignment, Status: store.TaskOpen, Title: "担当を引き受けられるか回答してください。",
			DueAt: cs.UpdatedAt.Add(24 * time.Hour), ProposalID: p.ID, ProposalVersion: p.Version,
			RequestedBy: "system", CreatedAt: cs.UpdatedAt,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if tk := h.openTask(assignee, "assignment"); tk == nil {
		t.Fatal("テストの前提が崩れている：重複タスクを作れていない")
	}

	// 重複タスクが残ったままでは、ほかの条件が揃っても確定しない。
	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustRespond(name, "approval", "approve")
	}
	if got := h.book("A", b.ID).Sessions[0]; got.SchedulingStatus == "scheduled" {
		t.Fatal("重複タスクが残ったまま確定してしまった")
	}

	// ProcessDue（再起動後の再処理も含む）が重複タスクを無効化し、残りの条件だけで確定させる。
	h.restart()
	h.process()
	h.process() // 冪等：2回実行しても状態は変わらない

	if tk := h.openTask(assignee, "assignment"); tk != nil {
		t.Fatalf("重複タスクが解消されていない: %+v", tk)
	}
	got := h.book("A", b.ID).Sessions[0]
	if got.SchedulingStatus != "scheduled" || got.Session == nil || got.Session.ScheduleStatus != "confirmed" {
		t.Fatalf("重複タスクの解消後も確定しない: %+v", got)
	}
	if got.AssignmentStatus != "accepted" {
		t.Fatalf("担当を勝手に承認扱いにせず、ブック側の状態を保つべき: %s", got.AssignmentStatus)
	}

	// 古いタスク（無効化済み・案は既に確定済み）への回答は拒否される。
	in := apitypes.TaskResponseInput{Decision: "accept", ProposalID: &propID, ProposalVersion: &det.CurrentProposal.Version}
	if _, err := h.c.RespondTask(ctx, h.users[assignee], taskID, in, nil); code(err) != apperr.ProposalSuperseded {
		t.Fatalf("無効化済みタスクへの回答 = %v", err)
	}
}
