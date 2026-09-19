package coord_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
)

// harness は A（管理者）・B・C・D の4人の輪読会を用意する。
type harness struct {
	t     *testing.T
	st    *store.Store
	clk   *clock.Offset
	c     *coord.Coordinator
	users map[string]string // 表示名 → user_id
	group string
	sess  string
}

const sessionData = `{
  "book_title": "サンプル技術書", "isbn": null, "toc_source": {"kind": "manual", "urls": []},
  "sections": [{"id": "sec_1", "title": "前回の範囲"}, {"id": "sec_2", "title": "今回の前半"}, {"id": "sec_3", "title": "今回の後半"}],
  "completed_section_ids": ["sec_1"], "target_section_ids": ["sec_2", "sec_3"]
}`

func newHarness(t *testing.T, planner coord.Planner) *harness {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg, err := coord.NewService(readingPlaybook())
	if err != nil {
		t.Fatal(err)
	}
	if planner == nil {
		planner = coord.DraftOnlyPlanner{}
	}
	clk := clock.NewOffset(clock.Fixed{T: t0})
	h := &harness{t: t, st: st, clk: clk, users: map[string]string{}}
	h.c = coord.NewCoordinator(reg, st, clk, planner, coord.Options{PublicBaseURL: "http://localhost:8080", Rand: func() float64 { return 0.5 }})

	ids := map[string]string{"A": "111111111111111111", "B": "222222222222222222", "C": "333333333333333333", "D": "444444444444444444"}
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		for _, name := range []string{"A", "B", "C", "D"} {
			u, err := tx.UpsertUser(ctx, ids[name], name, t0)
			if err != nil {
				t.Fatal(err)
			}
			h.users[name] = u.ID
		}
		return nil
	})
	res, err := h.c.CreateGroup(ctx, h.users["A"], apitypes.CreateGroupInput{Name: "技術書輪読", Invitees: []apitypes.Invitee{
		{DiscordUserID: ids["B"], DisplayName: "B"}, {DiscordUserID: ids["C"], DisplayName: "C"}, {DiscordUserID: ids["D"], DisplayName: "D"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var g apitypes.Group
	decode(t, res.Body, &g)
	h.group = g.ID
	return h
}

func decode(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func (h *harness) createSession() {
	h.t.Helper()
	res, err := h.c.CreateSession(ctx, h.users["A"], h.group, apitypes.CreateSessionInput{
		PlaybookID: "reading", Title: "第2回", StartsAt: t0.Add(50 * time.Hour).Format(time.RFC3339), DurationMinutes: 60, Data: json.RawMessage(sessionData),
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	var out apitypes.SessionCreated
	decode(h.t, res.Body, &out)
	h.sess = out.Session.ID
}

func (h *harness) detail(name string) apitypes.SessionDetail {
	h.t.Helper()
	d, err := h.c.SessionDetail(ctx, h.users[name], h.sess)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

func (h *harness) memberID(name string) string {
	for _, m := range h.detail("A").Members {
		if m.DisplayName == name {
			return m.ID
		}
	}
	h.t.Fatalf("member %s not found", name)
	return ""
}

func prepData(willing bool, prepared, explainable []string, minutes int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"willing_to_present": willing, "prepared_section_ids": prepared, "explainable_section_ids": explainable, "max_presentation_minutes": minutes})
	return b
}

func (h *harness) putPrep(name, attendance string, data json.RawMessage) (store.Response, error) {
	rev := h.detail(name).Session.Revision
	return h.c.PutPreparation(ctx, h.users[name], h.sess, apitypes.PutPreparationInput{ExpectedRevision: &rev, Preparation: &apitypes.Preparation{Attendance: attendance, Data: data}}, nil)
}

func (h *harness) mustPrep(name, attendance string, data json.RawMessage) {
	h.t.Helper()
	if _, err := h.putPrep(name, attendance, data); err != nil {
		h.t.Fatalf("%s の参加条件: %v", name, err)
	}
}

// allPrepared は B が全範囲を担当可能、C は第2節のみ15分、A・D は担当しない状態にする。
func (h *harness) allPrepared() {
	h.mustPrep("A", "attending", prepData(false, []string{"sec_1"}, []string{}, 0))
	h.mustPrep("B", "attending", prepData(true, []string{"sec_1", "sec_2", "sec_3"}, []string{"sec_2", "sec_3"}, 40))
	h.mustPrep("C", "attending", prepData(false, []string{"sec_1", "sec_2"}, []string{}, 0))
	h.mustPrep("D", "attending", prepData(false, []string{"sec_1"}, []string{}, 0))
}

func (h *harness) process() {
	h.t.Helper()
	if _, err := h.c.ProcessDue(ctx); err != nil {
		h.t.Fatal(err)
	}
}

// openTask は本人の open なタスクを種類で探す。
func (h *harness) openTask(name, kind string) *apitypes.Task {
	for _, tk := range h.detail(name).MyTasks {
		if tk.Kind == kind && tk.Status == "open" {
			tk := tk
			return &tk
		}
	}
	return nil
}

func (h *harness) respond(name, kind, decision string) (store.Response, error) {
	h.t.Helper()
	tk := h.openTask(name, kind)
	if tk == nil {
		h.t.Fatalf("%s に open な %s タスクがない", name, kind)
	}
	return h.c.RespondTask(ctx, h.users[name], tk.ID, apitypes.TaskResponseInput{Decision: decision, ProposalID: tk.ProposalID, ProposalVersion: tk.ProposalVersion}, nil)
}

func (h *harness) mustRespond(name, kind, decision string) {
	h.t.Helper()
	if _, err := h.respond(name, kind, decision); err != nil {
		h.t.Fatalf("%s の %s への %s: %v", name, kind, decision, err)
	}
}

// confirmInitial は初回案を B の引き受けと A の承認で確定させる。
func (h *harness) confirmInitial() {
	h.t.Helper()
	h.createSession()
	h.allPrepared()
	h.process()
	h.mustRespond("B", "assignment", "accept")
	h.mustRespond("A", "owner_approval", "approve")
	if d := h.detail("A"); d.Session.Status != "confirmed" {
		h.t.Fatalf("初回案が確定していない: %+v", d.ActiveCase)
	}
}

func (h *harness) withdraw(name, scope string) (store.Response, error) {
	rev := h.detail(name).Session.Revision
	return h.c.Withdraw(ctx, h.users[name], h.sess, apitypes.WithdrawalInput{ExpectedRevision: &rev, Scope: scope}, nil)
}

func (h *harness) notifications() []store.Notification {
	var out []store.Notification
	_ = h.st.Tx(ctx, func(tx *store.Tx) error {
		cs, err := tx.LatestCase(ctx, h.sess)
		if err != nil {
			return err
		}
		out, err = tx.NotificationsByCase(ctx, cs.ID)
		return err
	})
	return out
}

func readingPlaybook() coord.Playbook { return reading.New() }
