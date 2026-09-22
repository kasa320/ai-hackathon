package coord_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// groupNotifyAction は group_notifications から、指定した kind・decision のボタンを持つ通知を探し、
// custom_id "act:<action_id>:<decision>" からアクションIDを取り出す。
func groupNotifyAction(t *testing.T, notifs []store.GroupNotification, kind, decision string) (string, store.GroupNotification) {
	t.Helper()
	for _, n := range notifs {
		if n.Kind != kind {
			continue
		}
		for _, b := range n.Components {
			parts := strings.SplitN(b.CustomID, ":", 3)
			if len(parts) == 3 && parts[0] == "act" && parts[2] == decision {
				return parts[1], n
			}
		}
	}
	t.Fatalf("kind=%s decision=%s のボタンが見つからない: %+v", kind, decision, notifs)
	return "", store.GroupNotification{}
}

// discordIDs は newHarnessFull が割り当てる固定の Discord ユーザーID。
var discordIDs = map[string]string{"A": "111111111111111111", "B": "222222222222222222", "C": "333333333333333333", "D": "444444444444444444"}

func userForDiscordID(discordID string) string {
	for name, id := range discordIDs {
		if id == discordID {
			return name
		}
	}
	return ""
}

func TestResolveNotifyAction_BookAssignmentMatchesRespondBookAssignment(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.process()

	notifs := h.groupNotifs()
	actionID, ntf := groupNotifyAction(t, notifs, "book_plan_proposed", "accept")
	name := userForDiscordID(ntf.RecipientDiscordUserID)
	if name == "" {
		t.Fatalf("通知の宛先が分からない: %+v", ntf)
	}

	if _, err := h.c.ResolveNotifyAction(ctx, h.users[name], actionID, "accept", nil); err != nil {
		t.Fatalf("ResolveNotifyAction: %v", err)
	}
	d := h.book("A", b.ID)
	for _, s := range d.Sessions {
		if s.AssigneeMemberID != nil && *s.AssigneeMemberID == h.groupMemberID(name) && s.AssignmentStatus != store.AssignAccepted {
			t.Fatalf("担当の引き受けが反映されていない: %+v", s)
		}
	}
}

func TestResolveNotifyAction_BookAssignmentWrongUserRejected(t *testing.T) {
	h := newHarness(t, nil)
	h.createBook(bookInput("設計の本", 6, 3))
	h.process()

	notifs := h.groupNotifs()
	actionID, ntf := groupNotifyAction(t, notifs, "book_plan_proposed", "accept")
	name := userForDiscordID(ntf.RecipientDiscordUserID)
	other := "A"
	if other == name {
		other = "B"
	}
	if _, err := h.c.ResolveNotifyAction(ctx, h.users[other], actionID, "accept", nil); code(err) != apperr.NotFound {
		t.Fatalf("他人のアクションは not_found のはず: %v", err)
	}
}

func TestResolveNotifyAction_BookConfirmationMatchesRespondAssigneeConfirmation(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID)
	h.setNow(jst0(2026, 9, 24))
	h.process()
	h.sess = h.startedSession(b.ID, 1)
	d := h.book("A", b.ID)
	assignee := h.memberName(*d.Sessions[0].AssigneeMemberID)

	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustPrep(name, "attending", weeklyAll())
	}
	h.process()
	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustRespond(name, "approval", "approve")
	}
	starts := h.book("A", b.ID).Sessions[0].Session.StartsAt

	// 開催3日前になったら、担当者へ確認を作る。
	h.setNow(starts.Add(-72 * time.Hour))
	h.process()
	if h.kinds()["book_assignee_confirm"] != 1 {
		t.Fatalf("最終確認の通知 = %v", h.kinds())
	}

	notifs := h.groupNotifs()
	actionID, ntf := groupNotifyAction(t, notifs, "book_assignee_confirm", "confirm")
	if name := userForDiscordID(ntf.RecipientDiscordUserID); name != assignee {
		t.Fatalf("宛先が担当者と一致しない: got=%s want=%s", name, assignee)
	}
	if _, err := h.c.ResolveNotifyAction(ctx, h.users[assignee], actionID, "confirm", nil); err != nil {
		t.Fatalf("ResolveNotifyAction: %v", err)
	}
	if s := h.book("A", b.ID).Sessions[0]; s.AssigneeConfirmationStatus == nil || *s.AssigneeConfirmationStatus != "confirmed" {
		t.Fatalf("直前確認が反映されていない: %+v", s)
	}
}

func TestResolveNotifyAction_OldBookPlanCannotAffectNewPlanVersion(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.process()
	actionID, ntf := groupNotifyAction(t, h.groupNotifs(), "book_plan_proposed", "accept")
	name := userForDiscordID(ntf.RecipientDiscordUserID)
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		cur, err := tx.ReadingBook(ctx, b.ID)
		if err != nil {
			return err
		}
		cur.PlanVersion++
		return tx.UpdateReadingBook(ctx, cur)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ResolveNotifyAction(ctx, h.users[name], actionID, "accept", nil); code(err) != apperr.InvalidState {
		t.Fatalf("古い計画の通知は拒否する: %v", err)
	}
}
