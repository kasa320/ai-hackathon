package store_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func mustUser(t *testing.T, st *store.Store, discordID, name string) store.User {
	t.Helper()
	var u store.User
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		u, err = tx.UpsertUser(ctx, discordID, name, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestNotifyActionInsertAndGet(t *testing.T) {
	st := openTest(t)
	u := mustUser(t, st, "111", "A")
	a := store.NotifyAction{
		ID: "nact_1", UserID: u.ID, Kind: "task_response",
		Ref: json.RawMessage(`{"task_id":"task_1"}`), Decisions: json.RawMessage(`["accept","decline"]`),
		ExpiresAt: t0.Add(24 * time.Hour), CreatedAt: t0,
	}
	if err := st.Tx(ctx, func(tx *store.Tx) error { return tx.InsertNotifyAction(ctx, a) }); err != nil {
		t.Fatal(err)
	}
	var got store.NotifyAction
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		got, err = tx.NotifyAction(ctx, "nact_1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.UserID != u.ID || got.Kind != "task_response" || got.ConsumedAt != nil {
		t.Fatalf("got = %+v", got)
	}
	if string(got.Ref) != `{"task_id":"task_1"}` {
		t.Fatalf("ref = %s", got.Ref)
	}
}

func TestNotifyActionUnknownIsNotFound(t *testing.T) {
	st := openTest(t)
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.NotifyAction(ctx, "nact_missing")
		return err
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ConsumeNotifyAction は最初の1回だけ成功する。連打・イベント再送で二重に処理しないための土台。
func TestNotifyActionConsumeOnce(t *testing.T) {
	st := openTest(t)
	u := mustUser(t, st, "111", "A")
	a := store.NotifyAction{
		ID: "nact_1", UserID: u.ID, Kind: "weekly_prompt",
		Ref: json.RawMessage(`{}`), Decisions: json.RawMessage(`["start"]`),
		ExpiresAt: t0.Add(24 * time.Hour), CreatedAt: t0,
	}
	if err := st.Tx(ctx, func(tx *store.Tx) error { return tx.InsertNotifyAction(ctx, a) }); err != nil {
		t.Fatal(err)
	}
	var first, second bool
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		first, err = tx.ConsumeNotifyAction(ctx, "nact_1", t0.Add(time.Minute))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		second, err = tx.ConsumeNotifyAction(ctx, "nact_1", t0.Add(2*time.Minute))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !first || second {
		t.Fatalf("first=%v (want true) second=%v (want false)", first, second)
	}
}

func TestNotifyActionConsumeAfterExpiryFails(t *testing.T) {
	st := openTest(t)
	u := mustUser(t, st, "111", "A")
	a := store.NotifyAction{
		ID: "nact_1", UserID: u.ID, Kind: "weekly_prompt",
		Ref: json.RawMessage(`{}`), Decisions: json.RawMessage(`["start"]`),
		ExpiresAt: t0.Add(time.Minute), CreatedAt: t0,
	}
	if err := st.Tx(ctx, func(tx *store.Tx) error { return tx.InsertNotifyAction(ctx, a) }); err != nil {
		t.Fatal(err)
	}
	var ok bool
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		ok, err = tx.ConsumeNotifyAction(ctx, "nact_1", t0.Add(time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("期限切れのアクションが消費できてしまった")
	}
}
