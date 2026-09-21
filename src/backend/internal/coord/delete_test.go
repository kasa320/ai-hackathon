package coord_test

import (
	"context"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func TestOwnerDeletesSession(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	if h.detail("A").CurrentProposal == nil {
		t.Fatal("案ができていない")
	}
	caseID := h.detail("A").ActiveCase.ID
	if !h.detail("A").Permissions.CanDeleteSession || h.detail("B").Permissions.CanDeleteSession {
		t.Fatal("削除できるのは管理者だけのはず")
	}

	if _, err := h.c.DeleteSession(ctx, h.users["B"], h.sess, nil); code(err) != apperr.Forbidden {
		t.Fatalf("管理者以外の削除: %v", err)
	}
	if _, err := h.c.DeleteSession(ctx, h.users["A"], h.sess, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SessionDetail(ctx, h.users["A"], h.sess); code(err) != apperr.NotFound {
		t.Fatalf("削除後の取得: %v", err)
	}
	if _, err := h.c.DeleteSession(ctx, h.users["A"], h.sess, nil); code(err) != apperr.NotFound {
		t.Fatalf("2回目の削除: %v", err)
	}
	// 期限・催促のイベントと未送信の通知も一緒に消え、処理が止まらない。
	h.clk.Advance(48 * time.Hour)
	h.process()
	_ = h.st.Tx(ctx, func(tx *store.Tx) error {
		ns, err := tx.NotificationsByCase(ctx, caseID)
		if err != nil || len(ns) != 0 {
			t.Fatalf("削除した回の通知が残っている: %d 件 %v", len(ns), err)
		}
		return nil
	})
}

// deletingPlanner は AI の処理中に開催回が削除された状況を作る。
type deletingPlanner struct{ del func() }

func (p deletingPlanner) Plan(ctx context.Context, req coord.PlanRequest) (coord.Outcome, coord.Usage, error) {
	p.del()
	return coord.Outcome{}, coord.Usage{LLMCalls: []coord.LLMCall{{Model: "m", Currency: "unknown", Succeeded: true}}}, nil
}

func TestSessionDeletedWhilePlanning(t *testing.T) {
	var h *harness
	h = newHarness(t, deletingPlanner{del: func() {
		if _, err := h.c.DeleteSession(ctx, h.users["A"], h.sess, nil); err != nil {
			t.Fatal(err)
		}
	}})
	h.createSession()
	h.allPrepared()
	caseID := h.detail("A").ActiveCase.ID
	h.process() // エラーにならず、ほかのイベントの処理を止めない

	var calls int
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		n, err := tx.CountLLMCallsByCase(ctx, caseID)
		calls = n
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("費用の記録: %d 件", calls)
	}
}
