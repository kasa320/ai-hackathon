package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/notify"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	log = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// setupKind は種類を指定して通知を1件積む。
func setupKind(t *testing.T, kind string) *store.Store {
	t.Helper()
	st := setup(t, 0)
	err := st.Tx(ctx, func(tx *store.Tx) error {
		return tx.EnqueueNotification(ctx, store.Notification{ID: store.NewID("ntf"), SessionID: "s", CaseID: "c", Kind: kind,
			DedupeKey: store.NewID("d"), Content: "hello", Mentions: []string{"222222222222222222"}, CreatedAt: t0})
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// captureSender は送信内容を記録するだけの送信先。
type captureSender struct{ msgs []notify.Message }

func (c *captureSender) Send(_ context.Context, m notify.Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func setup(t *testing.T, n int) *store.Store {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	err = st.Tx(ctx, func(tx *store.Tx) error {
		u, _ := tx.UpsertUser(ctx, "111111111111111111", "A", t0)
		_ = tx.CreateGroup(ctx, store.Group{ID: "g", Name: "g", OwnerUserID: u.ID, CreatedAt: t0})
		_ = tx.CreateSession(ctx, store.Session{ID: "s", GroupID: "g", PlaybookID: "reading", Title: "t", StartsAt: t0.Add(48 * time.Hour), DurationMinutes: 60, Revision: 1, Status: "draft", Data: []byte(`{}`), CreatedAt: t0, UpdatedAt: t0}, nil)
		for i := 0; i < n; i++ {
			if err := tx.EnqueueNotification(ctx, store.Notification{ID: store.NewID("ntf"), SessionID: "s", CaseID: "c", Kind: "k", DedupeKey: store.NewID("d"), Content: "hello", Mentions: []string{"222222222222222222"}, CreatedAt: t0}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func statuses(t *testing.T, st *store.Store) map[string]int {
	out := map[string]int{}
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		list, err := tx.NotificationsByCase(ctx, "c")
		for _, n := range list {
			out[n.Status]++
		}
		return err
	})
	return out
}

func TestDiscordSenderStatusMapping(t *testing.T) {
	var got map[string]any
	var auth string
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(code)
	}))
	defer srv.Close()
	s := notify.NewDiscordSender("bot-token", "chan")
	s.BaseURL = srv.URL

	if err := s.Send(ctx, notify.Message{Content: "hi", MentionUserIDs: []string{"1"}}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bot bot-token" || got["content"] != "hi" {
		t.Fatalf("auth=%q body=%v", auth, got)
	}
	// 全員へのメンション（@everyone 等）は許可しない。
	am := got["allowed_mentions"].(map[string]any)
	if len(am["parse"].([]any)) != 0 || am["users"].([]any)[0] != "1" {
		t.Fatalf("allowed_mentions = %v", am)
	}
	for c, want := range map[int]error{403: notify.ErrDeliveryFailed, 500: notify.ErrDeliveryUnknown, 429: notify.ErrRetryLater} {
		code = c
		if err := s.Send(ctx, notify.Message{Content: "x"}); !errors.Is(err, want) {
			t.Fatalf("status %d: %v", c, err)
		}
	}
	s.BaseURL = "http://127.0.0.1:1"
	if err := s.Send(ctx, notify.Message{Content: "x"}); !errors.Is(err, notify.ErrDeliveryFailed) {
		t.Fatalf("接続できなければ送信失敗: %v", err)
	}
}

// E11：通知の失敗・成否不明を区別し、成否不明は再送しない。
func TestDispatcherRecordsFailureAndUnknown(t *testing.T) {
	st := setup(t, 1)
	faults := fault.New()
	faults.Define("notify", "fail", "unknown")
	v := "unknown"
	_ = faults.Replace(map[string]*string{"notify": &v})
	d := notify.NewDispatcher(st, clock.Fixed{T: t0}, notify.WithFaults(notify.LogSender{Log: log}, faults), log)
	if _, err := d.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	if s := statuses(t, st); s["unknown"] != 1 {
		t.Fatalf("statuses = %v", s)
	}
	// 障害を解除しても成否不明の通知は自動で再送しない。
	_ = faults.Replace(map[string]*string{})
	if n, _ := d.DispatchPending(ctx); n != 0 {
		t.Fatalf("成否不明の通知を再送した: %d", n)
	}

	st = setup(t, 1)
	v = "fail"
	_ = faults.Replace(map[string]*string{"notify": &v})
	d = notify.NewDispatcher(st, clock.Fixed{T: t0}, notify.WithFaults(notify.LogSender{Log: log}, faults), log)
	_, _ = d.DispatchPending(ctx)
	if s := statuses(t, st); s["failed"] != 1 {
		t.Fatalf("statuses = %v", s)
	}
}

type blockingSender struct{}

func (blockingSender) Send(ctx context.Context, _ notify.Message) error { panic("送信中に停止") }

// E12：送信中にプロセスが停止した通知は、再起動時に成否不明にして二重送信しない。
func TestRecoverMarksInterruptedAsUnknown(t *testing.T) {
	st := setup(t, 2)
	d := notify.NewDispatcher(st, clock.Fixed{T: t0}, blockingSender{}, log)
	func() {
		defer func() { _ = recover() }()
		_, _ = d.DispatchPending(ctx)
	}()
	if s := statuses(t, st); s["sending"] != 1 || s["pending"] != 1 {
		t.Fatalf("停止直前の状態: %v", s)
	}
	d = notify.NewDispatcher(st, clock.Fixed{T: t0}, notify.LogSender{Log: log}, log)
	if err := d.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DispatchPending(ctx); err != nil || n != 1 {
		t.Fatalf("残りの1件だけ送る: %d %v", n, err)
	}
	if s := statuses(t, st); s["unknown"] != 1 || s["sent"] != 1 {
		t.Fatalf("statuses = %v", s)
	}
}

// 本人宛ての依頼は DM を試し、DM を開けないことが確実なときだけチャンネルへ退避する。
// 成否が分からない失敗では退避せず、二重に送らない。
func TestDiscordSenderPrefersDM(t *testing.T) {
	var paths []string
	dmStatus := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/users/@me/channels" {
			if dmStatus != http.StatusOK {
				w.WriteHeader(dmStatus)
				return
			}
			_, _ = w.Write([]byte(`{"id":"dm_1"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := notify.NewDiscordSender("bot-token", "chan")
	s.BaseURL = srv.URL

	msg := notify.Message{Kind: "task_requested", Content: "hi", MentionUserIDs: []string{"1"}, DMUserIDs: []string{"1"}}
	if err := s.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[1] != "/channels/dm_1/messages" {
		t.Fatalf("DM へ送っていない: %v", paths)
	}

	// DM 拒否（403）はチャンネルへ退避する。
	paths, dmStatus = nil, http.StatusForbidden
	if err := s.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[1] != "/channels/chan/messages" {
		t.Fatalf("チャンネルへ退避していない: %v", paths)
	}

	// 成否不明（500）は退避しない。
	paths, dmStatus = nil, http.StatusInternalServerError
	if err := s.Send(ctx, msg); !errors.Is(err, notify.ErrDeliveryUnknown) {
		t.Fatalf("成否不明のはず: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("成否不明なのに二重に送った: %v", paths)
	}

	// 全員が知るべき通知は DM を試さない。
	paths, dmStatus = nil, http.StatusOK
	if err := s.Send(ctx, notify.Message{Kind: "plan_confirmed", Content: "hi", DMUserIDs: []string{"1"}}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/channels/chan/messages" {
		t.Fatalf("確定の連絡はチャンネルへ: %v", paths)
	}
}

// 通知の種類と DM の宛先は送信先へ渡す。本文とメンションから送り先を推測しない。
func TestDispatcherPassesKindAndDMRecipients(t *testing.T) {
	tests := map[string]struct{ wantDM bool }{
		"task_requested": {true},
		"reminder":       {true},
		"plan_confirmed": {false},
		"needs_owner":    {false},
	}
	for kind, tt := range tests {
		t.Run(kind, func(t *testing.T) {
			st := setupKind(t, kind)
			sender := &captureSender{}
			d := notify.NewDispatcher(st, clock.Fixed{T: t0}, sender, log)
			if _, err := d.DispatchPending(ctx); err != nil {
				t.Fatal(err)
			}
			if len(sender.msgs) != 1 {
				t.Fatalf("送信 = %d 件", len(sender.msgs))
			}
			m := sender.msgs[0]
			if m.Kind != kind {
				t.Fatalf("kind = %q", m.Kind)
			}
			if got := len(m.DMUserIDs) > 0; got != tt.wantDM {
				t.Fatalf("DM の宛先 = %v, want %v", m.DMUserIDs, tt.wantDM)
			}
			if tt.wantDM && m.DMUserIDs[0] != "222222222222222222" {
				t.Fatalf("DM の宛先 = %v", m.DMUserIDs)
			}
		})
	}
}
