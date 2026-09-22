package coord

import (
	"context"
	"encoding/json"

	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// readingSessionAssignee は開催回データから固定担当者だけを読み取る。coord 側が認識している唯一の
// キーは "assignee_member_id"（createReadingSessionTx が書き込む値、books.go 参照）。
type readingSessionAssignee struct {
	AssigneeMemberID string `json:"assignee_member_id"`
}

// reconcileBookAssignmentTasks は、ブック側で本人承認済み（reading_book_slots.assignment_status）
// なのに、開催回側にも重複した担当引き受けタスクが残っている状態を解消する。
// DBの状態から毎回導くため、再実行しても安全（冪等）。既存DBの過去分もこの処理で解消する。
func (c *Coordinator) reconcileBookAssignmentTasks(ctx context.Context) (int, error) {
	n := 0
	for {
		var tasks []store.Task
		if err := c.st.Tx(ctx, func(tx *store.Tx) error {
			var err error
			tasks, err = tx.OpenTasksByKind(ctx, store.TaskAssignment)
			return err
		}); err != nil {
			return n, err
		}
		progressed := false
		for _, tk := range tasks {
			ok, err := c.reconcileOneAssignmentTask(ctx, tk.ID)
			if err != nil {
				return n, err
			}
			if ok {
				n++
				progressed = true
			}
		}
		if !progressed {
			return n, nil
		}
	}
}

// reconcileOneAssignmentTask は1件のタスクを確認し、ブック側の担当固定と重複していれば無効化する。
// 対象でなければ何もしない。
func (c *Coordinator) reconcileOneAssignmentTask(ctx context.Context, taskID string) (bool, error) {
	handled := false
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		tk, err := tx.Task(ctx, taskID)
		if err != nil {
			return err
		}
		if tk.Status != store.TaskOpen || tk.Kind != store.TaskAssignment || tk.ProposalID == "" {
			return nil
		}
		sess, err := tx.Session(ctx, tk.SessionID)
		if err != nil {
			return err
		}
		if sess.PlaybookID != "reading" {
			return nil
		}
		var d readingSessionAssignee
		if err := json.Unmarshal(sess.Data, &d); err != nil || d.AssigneeMemberID == "" || d.AssigneeMemberID != tk.MemberID {
			// 担当が固定されていない（旧方式・手動セッション）か、このタスクの対象者ではない。
			return nil
		}
		p, err := tx.Proposal(ctx, tk.ProposalID)
		if err != nil {
			return err
		}
		if p.Status != store.ProposalPending {
			return nil
		}
		var req storedRequirements
		if err := json.Unmarshal(p.Requirements, &req); err != nil {
			return err
		}
		kept := req.Acceptors[:0]
		removed := false
		for _, id := range req.Acceptors {
			if id == tk.MemberID {
				removed = true
				continue
			}
			kept = append(kept, id)
		}
		if !removed {
			return nil
		}
		req.Acceptors = kept
		encoded := encode(req)
		if err := tx.SetProposalRequirements(ctx, p.ID, encoded); err != nil {
			return err
		}
		if err := tx.SetTaskStatus(ctx, tk.ID, store.TaskObsolete); err != nil {
			return err
		}
		if err := tx.CancelPendingEventsByRef(ctx, tk.ID); err != nil {
			return err
		}
		cs, err := tx.Case(ctx, tk.CaseID)
		if err != nil {
			return err
		}
		if err := c.activity(ctx, tx, sess, cs.ID, "response_recorded",
			"ブック側ですでに本人承認済みのため、重複していた担当引き受けの依頼を取り消しました。", p.ID, now); err != nil {
			return err
		}
		p.Requirements = encoded
		handled = true
		return c.evaluate(ctx, tx, &sess, &cs, p, now)
	})
	return handled, err
}
