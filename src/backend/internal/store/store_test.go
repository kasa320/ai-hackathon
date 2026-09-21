package store_test

import (
	"context"
	"database/sql"
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

func TestOpenAddsGroupLifecycleColumnsToLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY, discord_user_id TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE groups (
			id TEXT PRIMARY KEY, name TEXT NOT NULL,
			owner_user_id TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL
		);
		CREATE TABLE members (
			id TEXT PRIMARY KEY, group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
			discord_user_id TEXT NOT NULL, user_id TEXT REFERENCES users(id),
			display_name TEXT NOT NULL, role TEXT NOT NULL, seq INTEGER NOT NULL,
			UNIQUE (group_id, discord_user_id)
		);
		INSERT INTO users VALUES ('usr_1', '111111111111111111', 'A', '2026-09-19T09:00:00Z', '2026-09-19T09:00:00Z');
		INSERT INTO groups VALUES ('grp_1', '輪読', 'usr_1', '2026-09-19T09:00:00Z');
		INSERT INTO members VALUES ('mem_1', 'grp_1', '111111111111111111', 'usr_1', 'A', 'owner', 0);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("旧DBを開けない: %v", err)
	}
	defer st.Close()
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		groups, err := tx.GroupsForUser(ctx, "usr_1")
		if err != nil {
			return err
		}
		if len(groups) != 1 || groups[0].ID != "grp_1" {
			t.Fatalf("groups = %+v", groups)
		}
		// 種別を持たない既存グループは reading として読める。
		if groups[0].PlaybookID != "reading" {
			t.Fatalf("旧グループの種別 = %q", groups[0].PlaybookID)
		}
		g, err := tx.Group(ctx, "grp_1")
		if err != nil || g.PlaybookID != "reading" {
			t.Fatalf("Group = %+v, %v", g, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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

// 対話の対象は、本人が所属し、まだ開催していない回だけ。未回答の確認タスクの有無も返す。
func TestPreparationTargetsByUser(t *testing.T) {
	st := openTest(t)
	seedSession(t, st)
	var userID, otherID string
	err := st.Tx(ctx, func(tx *store.Tx) error {
		u, _ := tx.UserByDiscordID(ctx, "111111111111111111")
		userID = u.ID
		other, err := tx.UpsertUser(ctx, "999999999999999999", "Z", t0)
		if err != nil {
			return err
		}
		otherID = other.ID
		// 過去の回（開催済み）と、本人が所属しない別グループの回。
		past := store.Session{ID: "ses_past", GroupID: "grp_1", PlaybookID: "reading", Title: "前回", StartsAt: t0.Add(-time.Hour), DurationMinutes: 60, Revision: 1, Status: "draft", Data: []byte(`{}`), CreatedAt: t0, UpdatedAt: t0}
		if err := tx.CreateSession(ctx, past, []string{"mem_a"}); err != nil {
			return err
		}
		if err := tx.CreateGroup(ctx, store.Group{ID: "grp_2", Name: "別", OwnerUserID: otherID, CreatedAt: t0}); err != nil {
			return err
		}
		if err := tx.AddMember(ctx, store.Member{ID: "mem_z", GroupID: "grp_2", DiscordUserID: "999999999999999999", UserID: otherID, DisplayName: "Z", Role: "owner"}, 0); err != nil {
			return err
		}
		other2 := store.Session{ID: "ses_other", GroupID: "grp_2", PlaybookID: "reading", Title: "別の会", StartsAt: t0.Add(72 * time.Hour), DurationMinutes: 60, Revision: 1, Status: "draft", Data: []byte(`{}`), CreatedAt: t0, UpdatedAt: t0}
		return tx.CreateSession(ctx, other2, []string{"mem_z"})
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = st.Tx(ctx, func(tx *store.Tx) error {
		got, err := tx.PreparationTargetsByUser(ctx, userID, t0, 25)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Session.ID != "ses_1" || got[0].MemberID != "mem_a" {
			t.Fatalf("対象 = %+v", got)
		}
		if got[0].HasOpenTask {
			t.Fatal("タスクがないのに未回答になっている")
		}
		// 別グループの回は本人に見えない。
		if got, _ := tx.PreparationTargetsByUser(ctx, otherID, t0, 25); len(got) != 1 || got[0].Session.ID != "ses_other" {
			t.Fatalf("他の利用者の対象 = %+v", got)
		}
		return nil
	})

	// 期限内の未回答の参加条件確認タスクがあれば HasOpenTask になる。
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		return tx.CreateTask(ctx, store.Task{ID: "tsk_1", SessionID: "ses_1", CaseID: "case_1", MemberID: "mem_a",
			Kind: store.TaskPreparation, Status: store.TaskOpen, Title: "確認", DueAt: t0.Add(24 * time.Hour), RequestedBy: "agent", CreatedAt: t0})
	})
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		got, _ := tx.PreparationTargetsByUser(ctx, userID, t0, 25)
		if len(got) != 1 || !got[0].HasOpenTask {
			t.Fatalf("未回答のタスクを拾えていない: %+v", got)
		}
		// 期限を過ぎたタスクは数えない。
		got, _ = tx.PreparationTargetsByUser(ctx, userID, t0.Add(25*time.Hour), 25)
		if len(got) != 1 || got[0].HasOpenTask {
			t.Fatalf("期限切れのタスクを数えている: %+v", got)
		}
		return nil
	})
}

// 自由文の解釈は呼び出し前に枠を確保し、結果で同じ行を更新する（二重計上しない）。
func TestLLMCallReserveAndUpdate(t *testing.T) {
	st := openTest(t)
	seedSession(t, st)
	lookup := "preparation:ses_1"
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		return tx.AddLLMCall(ctx, store.LLMCall{ID: "llm_1", CaseID: "case_1", LookupID: lookup, Model: "m", Currency: "unknown", CreatedAt: t0})
	})
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		n, err := tx.CountLLMCallsByLookup(ctx, lookup)
		if err != nil || n != 1 {
			t.Fatalf("予約が数えられていない: %d %v", n, err)
		}
		// 案件の合計にも入る（計画用の予算と共有する）。
		if n, _ := tx.CountLLMCallsByCase(ctx, "case_1"); n != 1 {
			t.Fatalf("案件の合計 = %d", n)
		}
		return nil
	})

	in, out := 100, 20
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		return tx.UpdateLLMCall(ctx, store.LLMCall{ID: "llm_1", Model: "m", InputTokens: &in, OutputTokens: &out, Currency: "unknown", Succeeded: true})
	})
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		n, _ := tx.CountLLMCallsByLookup(ctx, lookup)
		if n != 1 {
			t.Fatalf("更新で行が増えた: %d", n)
		}
		calls, err := tx.LLMCallsByCase(ctx, "case_1")
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || !calls[0].Succeeded || *calls[0].InputTokens != 100 {
			t.Fatalf("結果が記録されていない: %+v", calls)
		}
		return nil
	})
	// IDのない更新は受け付けない（新しい行を作らない）。
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateLLMCall(ctx, store.LLMCall{Model: "m"}); err == nil {
			t.Fatal("IDなしの更新を受け付けた")
		}
		return nil
	})
}

// 自動進行の前に作られたDBを、削除せずに移行できる：列の追加、旧ブックの legacy 扱い、
// 通知の種別制約の緩和（既存の行は保持）。
func TestOpenMigratesLegacyBooksAndNotificationKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-books.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE users (id TEXT PRIMARY KEY, discord_user_id TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, created_at TEXT NOT NULL);
		CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, owner_user_id TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL, deleted_at TEXT);
		CREATE TABLE reading_books (
			id TEXT PRIMARY KEY, group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE, title TEXT NOT NULL, isbn TEXT,
			toc_source TEXT NOT NULL, sections TEXT NOT NULL,
			planned_session_count INTEGER NOT NULL CHECK (planned_session_count BETWEEN 1 AND 52),
			session_creation_mode TEXT NOT NULL CHECK (session_creation_mode IN ('sequential', 'all')),
			status TEXT NOT NULL CHECK (status IN ('in_progress', 'completed')),
			completed_section_ids TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE reading_book_slots (
			id TEXT PRIMARY KEY, book_id TEXT NOT NULL REFERENCES reading_books(id) ON DELETE CASCADE,
			sequence_number INTEGER NOT NULL, status TEXT NOT NULL CHECK (status IN ('planned', 'active', 'completed')),
			session_id TEXT UNIQUE, covered_section_ids TEXT NOT NULL DEFAULT '[]', UNIQUE (book_id, sequence_number)
		);
		CREATE TABLE group_notifications (
			id TEXT PRIMARY KEY, group_id TEXT NOT NULL REFERENCES groups(id),
			kind TEXT NOT NULL CHECK (kind IN ('member_left', 'group_deleted')),
			recipient_discord_user_id TEXT NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, content TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'unknown')),
			error_code TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, seq INTEGER NOT NULL
		);
		INSERT INTO users VALUES ('usr_1', '111111111111111111', 'A', '2026-09-19T09:00:00Z');
		INSERT INTO groups VALUES ('grp_1', '輪読', 'usr_1', '2026-09-19T09:00:00Z', NULL);
		INSERT INTO reading_books VALUES ('book_old', 'grp_1', '旧ブック', NULL, '{"kind":"manual","urls":[]}', '[{"id":"s1","title":"1"}]', 2, 'sequential', 'in_progress', '[]', '2026-09-19T09:00:00Z', '2026-09-19T09:00:00Z');
		INSERT INTO reading_book_slots VALUES ('slot_old', 'book_old', 1, 'active', NULL, '[]');
		INSERT INTO group_notifications VALUES ('gntf_old', 'grp_1', 'member_left', '222222222222222222', 'k1', '脱退', 'sent', NULL, '2026-09-19T09:00:00Z', '2026-09-19T09:00:00Z', 1);
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	for i := 0; i < 2; i++ { // 2回目の起動でも壊れない
		st, err := store.Open(ctx, path)
		if err != nil {
			t.Fatalf("%d回目: 旧DBを開けない: %v", i+1, err)
		}
		if err := st.Tx(ctx, func(tx *store.Tx) error {
			b, err := tx.ReadingBook(ctx, "book_old")
			if err != nil {
				return err
			}
			// 旧ブックは自動進行の対象外（legacy）で、既存の値は保たれる。
			if b.PlanStatus != store.BookPlanLegacy || b.PlanVersion != 0 || b.AdjustmentLeadDays != 7 || b.Title != "旧ブック" || b.SessionCreationMode != "sequential" {
				t.Fatalf("旧ブック = %+v", b)
			}
			slots, err := tx.ReadingBookSlots(ctx, "book_old")
			if err != nil || len(slots) != 1 || slots[0].AssignmentStatus != store.AssignUnassigned || slots[0].AssigneeMemberID != "" || slots[0].Status != "active" {
				t.Fatalf("旧枠 = %+v, %v", slots, err)
			}
			if approved, err := tx.ApprovedBooks(ctx); err != nil || len(approved) != 0 {
				t.Fatalf("旧ブックが自動進行の対象になった: %+v, %v", approved, err)
			}
			// 既存の通知は保たれ、新しい種別を登録できる。
			ns, err := tx.GroupNotifications(ctx, "grp_1")
			if err != nil {
				return err
			}
			if len(ns) < 1 || ns[0].ID != "gntf_old" || ns[0].Status != "sent" {
				t.Fatalf("既存の通知 = %+v", ns)
			}
			return tx.EnqueueGroupNotification(ctx, store.GroupNotification{ID: "gntf_new" + string(rune('0'+i)), GroupID: "grp_1", Kind: "book_plan_proposed",
				RecipientDiscordUserID: "222222222222222222", DedupeKey: "book_plan:x:" + string(rune('0'+i)), Content: "x", CreatedAt: time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)})
		}); err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
}
