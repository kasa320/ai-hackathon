package devapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// Seeder は評価・デモ用の初期状態を作る。状態は通常の業務処理（仮の判断処理）を通して作り、
// DB に直接書き込んだ不整合な状態を作らない。投入時の依頼通知は送らない。
type Seeder struct {
	st    *store.Store
	clock clock.Clock
	coord *coord.Coordinator
	lock  *sync.Mutex
}

// NewSeeder を作る。LLM を呼ばないよう、規則だけの Planner を使う専用の Coordinator で投入する。
// runLock には本体の Coordinator の Options.RunLock を渡し、投入中は本体のイベント処理を止める。
func NewSeeder(reg *coord.Service, st *store.Store, clk clock.Clock, publicBaseURL string, runLock *sync.Mutex) *Seeder {
	return &Seeder{st: st, clock: clk, lock: runLock, coord: coord.NewCoordinator(reg, st, clk, coord.DraftOnlyPlanner{}, coord.Options{PublicBaseURL: publicBaseURL})}
}

// SeedUser は投入した利用者。/api/dev/login の discord_user_id に使う。
type SeedUser struct {
	DiscordUserID string `json:"discord_user_id"`
	DisplayName   string `json:"display_name"`
	Role          string `json:"role"`
}

type SeedResult struct {
	Scenario  string     `json:"scenario"`
	Users     []SeedUser `json:"users"`
	GroupID   string     `json:"group_id"`
	SessionID string     `json:"session_id"`
}

// デモの4人（名前・IDは架空）。
var demoUsers = []SeedUser{
	{DiscordUserID: "100000000000000001", DisplayName: "A", Role: "owner"},
	{DiscordUserID: "100000000000000002", DisplayName: "B", Role: "member"},
	{DiscordUserID: "100000000000000003", DisplayName: "C", Role: "member"},
	{DiscordUserID: "100000000000000004", DisplayName: "D", Role: "member"},
}

const demoSessionData = `{
  "book_title": "サンプル技術書",
  "isbn": null,
  "toc_source": {"kind": "manual", "urls": []},
  "sections": [
    {"id": "sec_1", "title": "前回の範囲"},
    {"id": "sec_2", "title": "今回の前半"},
    {"id": "sec_3", "title": "今回の後半"}
  ],
  "completed_section_ids": ["sec_1"],
  "target_section_ids": ["sec_2", "sec_3"]
}`

func prep(willing bool, prepared, explainable []string, minutes int) *apitypes.Preparation {
	data, _ := json.Marshal(map[string]any{"willing_to_present": willing, "prepared_section_ids": prepared, "explainable_section_ids": explainable, "max_presentation_minutes": minutes})
	return &apitypes.Preparation{Attendance: "attending", Data: data}
}

// Seed は DB を初期化してシナリオを投入する。
//   - initial_demo：開催回を登録した直後（全員の参加条件が未回答）。
//   - replan_demo：B が全範囲を担当する初回計画が確定済み。C は第2節を読んできたが担当の申し出はまだ。
//     B の辞退 → AI が C に確認 → C が第2節15分と回答 → 再計画 → 引き受け・投票 → 確定、を実演する。
func (s *Seeder) Seed(ctx context.Context, scenario string) (SeedResult, error) {
	if scenario != "replan_demo" && scenario != "initial_demo" {
		return SeedResult{}, apperr.Validation(apperr.Field{Path: "scenario", Message: "replan_demo または initial_demo を指定してください"})
	}
	if s.lock != nil {
		s.lock.Lock()
		defer s.lock.Unlock()
	}
	now := s.clock.Now().UTC()
	users := map[string]string{}
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Reset(ctx); err != nil {
			return err
		}
		for _, u := range demoUsers {
			created, err := tx.UpsertUser(ctx, u.DiscordUserID, u.DisplayName, now)
			if err != nil {
				return err
			}
			users[u.DisplayName] = created.ID
		}
		return nil
	})
	if err != nil {
		return SeedResult{}, err
	}
	var invitees []apitypes.Invitee
	for _, u := range demoUsers[1:] {
		invitees = append(invitees, apitypes.Invitee{DiscordUserID: u.DiscordUserID, DisplayName: u.DisplayName})
	}
	res, err := s.coord.CreateGroup(ctx, users["A"], apitypes.CreateGroupInput{Name: "技術書輪読（デモ）", Invitees: invitees}, nil)
	if err != nil {
		return SeedResult{}, err
	}
	var g apitypes.Group
	_ = json.Unmarshal(res.Body, &g)

	startsAt := now.Add(72 * time.Hour).Truncate(time.Hour)
	res, err = s.coord.CreateSession(ctx, users["A"], g.ID, apitypes.CreateSessionInput{
		PlaybookID: "reading", Title: "第2回", StartsAt: startsAt.Format(time.RFC3339), DurationMinutes: 60, Data: json.RawMessage(demoSessionData),
	}, nil)
	if err != nil {
		return SeedResult{}, err
	}
	var created apitypes.SessionCreated
	_ = json.Unmarshal(res.Body, &created)
	sessionID := created.Session.ID

	if scenario == "replan_demo" {
		preps := map[string]*apitypes.Preparation{
			"A": prep(false, []string{"sec_1"}, []string{}, 0),
			"B": prep(true, []string{"sec_1", "sec_2", "sec_3"}, []string{"sec_2", "sec_3"}, 40),
			"C": prep(false, []string{"sec_1", "sec_2"}, []string{}, 0),
			"D": prep(false, []string{"sec_1"}, []string{}, 0),
		}
		for _, name := range []string{"A", "B", "C", "D"} {
			if err := s.putPrep(ctx, users[name], sessionID, preps[name]); err != nil {
				return SeedResult{}, fmt.Errorf("%s の参加条件: %w", name, err)
			}
		}
		if _, err := s.coord.ProcessDue(ctx); err != nil {
			return SeedResult{}, err
		}
		for _, step := range []struct{ name, kind, decision string }{{"B", "assignment", "accept"}, {"A", "owner_approval", "approve"}} {
			if err := s.respond(ctx, users[step.name], sessionID, step.kind, step.decision); err != nil {
				return SeedResult{}, fmt.Errorf("%s の回答: %w", step.name, err)
			}
		}
	}

	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.DeleteNotifications(ctx); err != nil {
			return err
		}
		cs, err := tx.LatestCase(ctx, sessionID)
		if err != nil {
			return err
		}
		return tx.AddActivity(ctx, store.Activity{SessionID: sessionID, CaseID: cs.ID, Kind: "input_received",
			Summary: "デモ用の初期データを投入しました（" + scenario + "。投入時の回答・同意は初期データとして記録）。", OccurredAt: s.clock.Now().UTC()})
	})
	if err != nil {
		return SeedResult{}, err
	}
	return SeedResult{Scenario: scenario, Users: demoUsers, GroupID: g.ID, SessionID: sessionID}, nil
}

func (s *Seeder) putPrep(ctx context.Context, userID, sessionID string, p *apitypes.Preparation) error {
	d, err := s.coord.SessionDetail(ctx, userID, sessionID)
	if err != nil {
		return err
	}
	rev := d.Session.Revision
	_, err = s.coord.PutPreparation(ctx, userID, sessionID, apitypes.PutPreparationInput{ExpectedRevision: &rev, Preparation: p}, nil)
	return err
}

func (s *Seeder) respond(ctx context.Context, userID, sessionID, kind, decision string) error {
	d, err := s.coord.SessionDetail(ctx, userID, sessionID)
	if err != nil {
		return err
	}
	for _, tk := range d.MyTasks {
		if tk.Kind == kind && tk.Status == "open" {
			_, err := s.coord.RespondTask(ctx, userID, tk.ID, apitypes.TaskResponseInput{Decision: decision, ProposalID: tk.ProposalID, ProposalVersion: tk.ProposalVersion}, nil)
			return err
		}
	}
	return fmt.Errorf("open な %s タスクがありません", kind)
}
