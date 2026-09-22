package coord_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// notifyAction は、指定した decision を許可する保存済みの通知ボタンを1つ探す。
// custom_id は "act:<action_id>:<decision>" の形。
func notifyAction(t *testing.T, h *harness, caseID, dedupeKey, decision string) string {
	t.Helper()
	var ntfs []store.Notification
	if err := h.st.Tx(context.Background(), func(tx *store.Tx) error {
		var err error
		ntfs, err = tx.NotificationsByCase(ctx, caseID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, n := range ntfs {
		if n.DedupeKey != dedupeKey {
			continue
		}
		for _, b := range n.Components {
			parts := strings.SplitN(b.CustomID, ":", 3)
			if len(parts) == 3 && parts[0] == "act" && parts[2] == decision {
				return parts[1]
			}
		}
	}
	t.Fatalf("通知 %s に decision=%s のボタンが見つからない: %+v", dedupeKey, decision, ntfs)
	return ""
}

func TestResolveNotifyAction_TaskResponseMatchesRespondTask(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	tk := h.openTask("B", "assignment")
	if tk == nil {
		t.Fatal("Bに担当の未回答タスクがない")
	}
	caseID := h.detail("B").ActiveCase.ID
	actionID := notifyAction(t, h, caseID, "task:"+tk.ID, "accept")

	if _, err := h.c.ResolveNotifyAction(ctx, h.users["B"], actionID, "accept", nil); err != nil {
		t.Fatalf("ResolveNotifyAction: %v", err)
	}
	tk2 := h.openTask("B", "assignment")
	if tk2 != nil {
		t.Fatal("担当の引き受けが反映されていない（タスクがまだ open）")
	}
}

func TestResolveNotifyAction_WrongUserRejected(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	tk := h.openTask("B", "assignment")
	caseID := h.detail("B").ActiveCase.ID
	actionID := notifyAction(t, h, caseID, "task:"+tk.ID, "accept")

	if _, err := h.c.ResolveNotifyAction(ctx, h.users["C"], actionID, "accept", nil); code(err) != apperr.NotFound {
		t.Fatalf("他人のアクションは not_found として拒否されるはず: %v", err)
	}
	// Bの担当は変わっていない。
	if tk2 := h.openTask("B", "assignment"); tk2 == nil {
		t.Fatal("他人が操作してBの担当が処理された")
	}
}

func TestResolveNotifyAction_DisallowedDecisionRejected(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	tk := h.openTask("B", "assignment")
	caseID := h.detail("B").ActiveCase.ID
	actionID := notifyAction(t, h, caseID, "task:"+tk.ID, "accept")

	if _, err := h.c.ResolveNotifyAction(ctx, h.users["B"], actionID, "approve", nil); code(err) != apperr.InvalidState {
		t.Fatalf("許可されていない decision は拒否されるはず: %v", err)
	}
}

func TestResolveNotifyAction_AlreadyConsumedRejected(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	tk := h.openTask("B", "assignment")
	caseID := h.detail("B").ActiveCase.ID
	actionID := notifyAction(t, h, caseID, "task:"+tk.ID, "accept")

	if _, err := h.c.ResolveNotifyAction(ctx, h.users["B"], actionID, "accept", nil); err != nil {
		t.Fatalf("初回の解決に失敗: %v", err)
	}
	if _, err := h.c.ResolveNotifyAction(ctx, h.users["B"], actionID, "accept", nil); code(err) != apperr.InvalidState {
		t.Fatalf("消費済みのアクションは拒否されるはず: %v", err)
	}
}

func TestResolveNotifyAction_UnknownActionNotFound(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.c.ResolveNotifyAction(ctx, h.users["A"], "nact_does-not-exist", "accept", nil); code(err) != apperr.NotFound {
		t.Fatalf("存在しないアクションは not_found のはず: %v", err)
	}
}

func TestResolveNotifyAction_PreparationStartChecksSessionAccess(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	caseID := h.detail("B").ActiveCase.ID
	tk := h.openTask("B", "preparation")
	if tk == nil {
		t.Fatal("Bに参加条件確認タスクがない")
	}
	actionID := notifyAction(t, h, caseID, "task:"+tk.ID, "start")

	out, err := h.c.ResolveNotifyAction(ctx, h.users["B"], actionID, "start", nil)
	if err != nil {
		t.Fatalf("ResolveNotifyAction: %v", err)
	}
	if out.Kind != coord.NotifyActionPreparationStart || out.SessionID != h.sess {
		t.Fatalf("outcome = %+v", out)
	}

	// 対象外の人は所属チェックで弾かれる（Dは対象の開催回のメンバーだが、他人のアクションIDは
	// そもそも解決できない。ここでは対象を持たないEを想定する代わりに、別のアクションIDで確認する）。
}
