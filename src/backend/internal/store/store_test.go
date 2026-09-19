package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
)

func openTest(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSchemaIsReapplicable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for i := 0; i < 2; i++ {
		st, err := store.Open(ctx, path)
		if err != nil {
			t.Fatalf("%d回目の起動: %v", i+1, err)
		}
		st.Close()
	}
}

func TestInvitedMemberJoinsOnLogin(t *testing.T) {
	st := openTest(t)
	err := st.Tx(ctx, func(tx *store.Tx) error {
		owner, err := tx.UpsertUser(ctx, "111111111111111111", "A", t0)
		if err != nil {
			return err
		}
		g := store.Group{ID: "grp_1", Name: "輪読", OwnerUserID: owner.ID, CreatedAt: t0}
		if err := tx.CreateGroup(ctx, g); err != nil {
			return err
		}
		if err := tx.AddMember(ctx, store.Member{ID: "mem_a", GroupID: g.ID, DiscordUserID: owner.DiscordUserID, UserID: owner.ID, DisplayName: "A", Role: "owner"}, 0); err != nil {
			return err
		}
		return tx.AddMember(ctx, store.Member{ID: "mem_b", GroupID: g.ID, DiscordUserID: "222222222222222222", DisplayName: "仮B", Role: "member"}, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		m, _ := tx.Member(ctx, "mem_b")
		if m.Joined() {
			t.Fatal("本人のログイン前に参加済みになっている")
		}
		u, err := tx.UpsertUser(ctx, "222222222222222222", "B本人", t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.ActivateMemberships(ctx, u); err != nil {
			t.Fatal(err)
		}
		m, _ = tx.Member(ctx, "mem_b")
		if !m.Joined() || m.DisplayName != "B本人" {
			t.Fatalf("ログイン後に所属が有効になっていない: %+v", m)
		}
		groups, _ := tx.GroupsForUser(ctx, u.ID)
		if len(groups) != 1 || groups[0].MemberCount != 2 || groups[0].Role != "member" {
			t.Fatalf("groups = %+v", groups)
		}
		return nil
	})
}

func TestIdempotentReplaysAndRejectsReuse(t *testing.T) {
	st := openTest(t)
	key := &store.IdemKey{UserID: "usr_1", Key: "k1", Method: "POST", Path: "/api/x", BodyHash: "h1"}
	calls := 0
	fn := func(tx *store.Tx) (store.Response, error) {
		calls++
		return store.Response{Status: 202, Body: []byte(`{"n":1}`)}, nil
	}
	first, err := st.Idempotent(ctx, key, t0, fn)
	if err != nil || first.Replayed {
		t.Fatalf("初回: %+v %v", first, err)
	}
	again, err := st.Idempotent(ctx, key, t0, fn)
	if err != nil || !again.Replayed || string(again.Body) != `{"n":1}` || again.Status != 202 {
		t.Fatalf("再送: %+v %v", again, err)
	}
	if calls != 1 {
		t.Fatalf("再送で業務処理が %d 回実行された", calls)
	}
	other := *key
	other.BodyHash = "h2"
	if _, err := st.Idempotent(ctx, &other, t0, fn); !errors.Is(err, store.ErrIdempotencyKeyReused) {
		t.Fatalf("別の操作へのキー再利用を拒否すべき: %v", err)
	}
	// 検証で拒否した更新は記録しない。
	failKey := &store.IdemKey{UserID: "usr_1", Key: "k2", Method: "POST", Path: "/api/x", BodyHash: "h1"}
	boom := errors.New("validation")
	if _, err := st.Idempotent(ctx, failKey, t0, func(tx *store.Tx) (store.Response, error) { return store.Response{}, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	res, err := st.Idempotent(ctx, failKey, t0, fn)
	if err != nil || res.Replayed {
		t.Fatalf("失敗した操作のキーは再利用できる: %+v %v", res, err)
	}
}

func TestNotificationDedupeAndInterruptedSend(t *testing.T) {
	st := openTest(t)
	seedSession(t, st)
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		for i := 0; i < 2; i++ {
			if err := tx.EnqueueNotification(ctx, store.Notification{ID: store.NewID("ntf"), SessionID: "ses_1", CaseID: "case_1", Kind: "plan_confirmed", DedupeKey: "confirm:prop_1", Content: "確定", CreatedAt: t0}); err != nil {
				t.Fatal(err)
			}
		}
		list, _ := tx.NotificationsByCase(ctx, "case_1")
		if len(list) != 1 {
			t.Fatalf("同じ通知が %d 件登録された", len(list))
		}
		n, err := tx.ClaimNotification(ctx, t0)
		if err != nil || n.Status != store.NotifySending {
			t.Fatalf("claim: %+v %v", n, err)
		}
		// 送信中に停止した通知は再送せず、成否不明にする。
		changed, err := tx.MarkInterruptedNotificationsUnknown(ctx, t0)
		if err != nil || len(changed) != 1 {
			t.Fatalf("interrupted: %v %v", changed, err)
		}
		if _, err := tx.ClaimNotification(ctx, t0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("成否不明の通知を再送しようとした: %v", err)
		}
		return nil
	})
}

func TestDueEventsOrderAndCancel(t *testing.T) {
	st := openTest(t)
	seedSession(t, st)
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		for i, run := range []time.Time{t0.Add(time.Minute), t0, t0.Add(time.Hour)} {
			if err := tx.CreateEvent(ctx, store.Event{ID: store.NewID("evt"), SessionID: "ses_1", CaseID: "case_1", Kind: store.EventPlan, RunAt: run, CreatedAt: t0.Add(time.Duration(i))}); err != nil {
				t.Fatal(err)
			}
		}
		due, _ := tx.DueEvents(ctx, t0.Add(time.Minute), 10)
		if len(due) != 2 || !due[0].RunAt.Equal(t0) {
			t.Fatalf("due = %+v", due)
		}
		_ = tx.CancelPendingEvents(ctx, "case_1", store.EventPlan)
		due, _ = tx.DueEvents(ctx, t0.Add(2*time.Hour), 10)
		if len(due) != 0 {
			t.Fatalf("取り消したイベントが残っている: %+v", due)
		}
		return nil
	})
}

func seedSession(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Tx(ctx, func(tx *store.Tx) error {
		u, err := tx.UpsertUser(ctx, "111111111111111111", "A", t0)
		if err != nil {
			return err
		}
		if err := tx.CreateGroup(ctx, store.Group{ID: "grp_1", Name: "g", OwnerUserID: u.ID, CreatedAt: t0}); err != nil {
			return err
		}
		if err := tx.AddMember(ctx, store.Member{ID: "mem_a", GroupID: "grp_1", DiscordUserID: u.DiscordUserID, UserID: u.ID, DisplayName: "A", Role: "owner"}, 0); err != nil {
			return err
		}
		s := store.Session{ID: "ses_1", GroupID: "grp_1", PlaybookID: "reading", Title: "第1回", StartsAt: t0.Add(48 * time.Hour), DurationMinutes: 60, Revision: 1, Status: "draft", Data: []byte(`{}`), CreatedAt: t0, UpdatedAt: t0}
		if err := tx.CreateSession(ctx, s, []string{"mem_a"}); err != nil {
			return err
		}
		return tx.CreateCase(ctx, store.Case{ID: "case_1", SessionID: "ses_1", Status: store.CaseCollecting, Summary: "", CreatedAt: t0, UpdatedAt: t0})
	})
	if err != nil {
		t.Fatal(err)
	}
}
