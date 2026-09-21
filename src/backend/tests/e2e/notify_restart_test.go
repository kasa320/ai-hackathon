package e2e

import (
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// confirmReplan は replan_demo から再計画を確定させる（通知はまだ送らない）。
func confirmReplan(t *testing.T, s *server, p map[string]*client, id string) {
	t.Helper()
	p["B"].withdraw(id, "assignment").mustStatus(t, 202)
	s.process()
	s.process()
	for _, name := range []string{"A", "B", "D"} {
		p[name].respond(id, "approval", "approve").mustStatus(t, 202)
	}
	p["C"].respond(id, "assignment", "accept").mustStatus(t, 202)
	if d := p["A"].detail(id); d.Session.Status != "confirmed" {
		t.Fatalf("確定していない: %s", dump(d.Session))
	}
}

// E11：通知の失敗・成否不明は計画の確定と分けて記録し、計画を再確定せず、無条件に再送しない。
func TestNotificationFailureAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		fault, status, code string
	}{
		{"fail", "failed", "delivery_failed"},
		{"unknown", "unknown", "delivery_unknown"},
	} {
		t.Run(tc.fault, func(t *testing.T) {
			s := newServer(t)
			seed := s.seed("replan_demo")
			p := s.personas()
			id := seed.SessionID
			s.setFaults(map[string]any{"notify": tc.fault})
			confirmReplan(t, s, p, id)
			before := p["A"].detail(id)
			s.process()

			d := p["A"].detail(id)
			if d.Session.Status != "confirmed" || d.ConfirmedPlan.ID != before.ConfirmedPlan.ID || d.Session.Revision != before.Session.Revision {
				t.Fatal("通知の失敗で計画を変えてはいけない")
			}
			ns := d.NotificationSummary
			if (tc.status == "failed" && ns.FailedCount == 0) || (tc.status == "unknown" && ns.UnknownCount == 0) || ns.PendingCount != 0 || ns.SentCount != 0 {
				t.Fatalf("通知状況: %s", dump(ns))
			}
			var act apitypes.ActivityResponse
			p["A"].get("/api/sessions/"+id+"/activity").mustStatus(t, 200).decode(t, &act)
			found := false
			for _, n := range act.Summary.Notifications {
				if n.Status == tc.status && str(n.ErrorCode) == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("管理者向けの通知状況に %s がない: %s", tc.status, dump(act.Summary.Notifications))
			}
			// 障害が解消しても、失敗・成否不明の通知を自動で再送しない。
			s.setFaults(map[string]any{})
			s.process()
			if len(s.sender.all()) != 0 {
				t.Fatalf("自動で再送した: %d 件", len(s.sender.all()))
			}
		})
	}
}

// E12（返信待ち）：返信待ちでプロセスを再起動しても、保存済みの依頼・期限から再開する。
func TestRestartWhileWaitingForReplies(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID
	p["B"].withdraw(id, "assignment").mustStatus(t, 202)

	// 計画イベントが未処理のまま再起動 → 再起動後に1回だけ処理され、案が二重にできない。
	s.restart()
	s.process()
	s.process()
	d := p["A"].detail(id)
	if d.CurrentProposal == nil || d.CurrentProposal.Version != 2 || d.ActiveCase.Status != "awaiting_consent" {
		t.Fatalf("再起動後の再計画は1回だけ: %s", dump(d.CurrentProposal))
	}

	// 返信待ちで再起動しても、案と依頼はそのまま残る（Cookie のセッションも DB に残っている）。
	s.restart()
	s.process()
	if again := p["A"].detail(id); again.CurrentProposal.ID != d.CurrentProposal.ID || p["C"].openTask(id, "assignment") == nil {
		t.Fatalf("再起動で案や依頼が変わった: %s", dump(again.CurrentProposal))
	}
	// 投票待ちで再起動しても、期限のイベントは残っていて管理者へ戻せる。
	p["C"].respond(id, "assignment", "accept").mustStatus(t, 202)
	s.restart()
	s.advance(25 * time.Hour)
	d = p["A"].detail(id)
	if d.ActiveCase.Status != "needs_owner" || str(d.ActiveCase.ReasonCode) != "deadline_expired" || d.CurrentProposal.Assignments[0].Status != "accepted" {
		t.Fatalf("再起動後の期限処理・引き受けの保持: %s", dump(d.ActiveCase))
	}
}

// E12（通知待ち）：送信途中で停止した通知は、再起動時に成否不明とし二重送信しない。未送信の通知は送る。
func TestRestartWithInterruptedNotification(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID
	confirmReplan(t, s, p, id)

	// 1件を「送信中」にしたまま停止したことにする。
	var claimed store.Notification
	if err := s.st.Tx(bg, func(tx *store.Tx) error {
		var err error
		claimed, err = tx.ClaimNotification(bg, s.clk.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// 送信中の通知は画面上は pending に数える。
	before := p["A"].detail(id).NotificationSummary
	s.restart()
	s.process()
	for _, m := range s.sender.all() {
		if m.Content == claimed.Content {
			t.Fatal("送信中に停止した通知を再送した")
		}
	}
	d := p["A"].detail(id)
	ns := d.NotificationSummary
	if claimed.CaseID != d.ActiveCase.ID || ns.PendingCount != 0 || ns.UnknownCount != 1 || ns.SentCount != before.SentCount+before.PendingCount-1 {
		t.Fatalf("停止した1件は成否不明、残りは送信済み: %s（停止前 %s）", dump(ns), dump(before))
	}
	if d.Session.Status != "confirmed" {
		t.Fatal("計画の確定は失われない")
	}
}
