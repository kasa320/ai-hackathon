package devapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	Scenario         string     `json:"scenario"`
	Users            []SeedUser `json:"users"`
	GroupID          string     `json:"group_id"`
	BookIDs          []string   `json:"book_ids"`
	SlotIDs          []string   `json:"slot_ids"`
	SessionID        string     `json:"session_id,omitempty"`
	NextTransitionAt *time.Time `json:"next_transition_at,omitempty"`
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

// prep は参加予定の参加条件。declined なら今回の説明の担当を辞退している。
func prep(declined bool) *apitypes.Preparation {
	data, _ := json.Marshal(map[string]any{"declined_presentation": declined})
	return &apitypes.Preparation{Attendance: "attending", Data: data}
}

var scenarios = map[string]bool{
	"book_plan_demo":             true,
	"book_schedule_demo":         true,
	"assignee_confirmation_demo": true,
	// 旧デモと既存E2Eの互換用。
	"replan_demo":  true,
	"initial_demo": true,
}

// Seed は DB を初期化してシナリオを投入する。
//   - book_plan_demo：2冊の全体計画が作られ、各担当者の承認待ち。
//   - book_schedule_demo：2冊の担当計画が承認済みで、翌日の調整開始を待つ。
//   - assignee_confirmation_demo：第1回の日程が確定済みで、開催3日前の担当確認を待つ。
//   - initial_demo：開催回を登録した直後（全員の参加条件が未回答）。
//   - replan_demo：B が前半、C が後半を担当する初回計画が確定済み（A・D は今回の担当を辞退）。
//     B の担当辞退 → 再計画（C が全範囲）→ 引き受け・投票 → 確定、を実演する。
func (s *Seeder) Seed(ctx context.Context, scenario string) (SeedResult, error) {
	if !scenarios[scenario] {
		return SeedResult{}, apperr.Validation(apperr.Field{Path: "scenario", Message: "定義済みのデモシナリオを指定してください"})
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
	res, err := s.coord.CreateGroup(ctx, users["A"], apitypes.CreateGroupInput{Name: "技術書輪読（デモ）", PlaybookID: "reading", Invitees: invitees}, nil)
	if err != nil {
		return SeedResult{}, err
	}
	var g apitypes.Group
	_ = json.Unmarshal(res.Body, &g)
	if strings.HasPrefix(scenario, "book_") || scenario == "assignee_confirmation_demo" {
		return s.seedBookScenario(ctx, scenario, now, users, g)
	}

	startsAt := now.Add(72 * time.Hour).Truncate(time.Hour)
	res, err = s.coord.CreateSession(ctx, users["A"], g.ID, apitypes.CreateSessionInput{
		PlaybookID: "reading", Title: g.Name + " 第2回", StartsAt: startsAt.Format(time.RFC3339), DurationMinutes: 60, Data: json.RawMessage(demoSessionData),
	}, nil)
	if err != nil {
		return SeedResult{}, err
	}
	var created apitypes.SessionCreated
	_ = json.Unmarshal(res.Body, &created)
	sessionID := created.Session.ID

	if scenario == "replan_demo" {
		preps := map[string]*apitypes.Preparation{
			"A": prep(true),
			"B": prep(false),
			"C": prep(false),
			"D": prep(true),
		}
		for _, name := range []string{"A", "B", "C", "D"} {
			if err := s.putPrep(ctx, users[name], sessionID, preps[name]); err != nil {
				return SeedResult{}, fmt.Errorf("%s の参加条件: %w", name, err)
			}
		}
		if _, err := s.coord.ProcessDue(ctx); err != nil {
			return SeedResult{}, err
		}
		for _, step := range []struct{ name, kind, decision string }{{"B", "assignment", "accept"}, {"C", "assignment", "accept"}, {"A", "approval", "approve"}, {"B", "approval", "approve"}, {"C", "approval", "approve"}, {"D", "approval", "approve"}} {
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
	return SeedResult{Scenario: scenario, Users: demoUsers, GroupID: g.ID, BookIDs: []string{}, SlotIDs: []string{}, SessionID: sessionID}, nil
}

var demoBookSections = json.RawMessage(`[
  {"id":"sec_1","title":"第1章 はじめに"},
  {"id":"sec_2","title":"第2章 基本設計"},
  {"id":"sec_3","title":"第3章 境界"},
  {"id":"sec_4","title":"第4章 状態管理"},
  {"id":"sec_5","title":"第5章 失敗への対応"},
  {"id":"sec_6","title":"第6章 テスト"},
  {"id":"sec_7","title":"第7章 運用"},
  {"id":"sec_8","title":"付録"}
]`)

func day(now time.Time, offset int) string {
	jst := time.FixedZone("JST", 9*60*60)
	return now.In(jst).AddDate(0, 0, offset).Format("2006-01-02")
}

func (s *Seeder) setDemoAvailability(ctx context.Context, users map[string]string) error {
	windows := []apitypes.WeeklyWindow{}
	for weekday := 1; weekday <= 7; weekday++ {
		windows = append(windows, apitypes.WeeklyWindow{Weekday: weekday, Start: "19:00", End: "22:00"})
	}
	for _, name := range []string{"A", "B", "C", "D"} {
		if _, err := s.coord.PutWeeklyAvailability(ctx, users[name], apitypes.PutWeeklyAvailabilityInput{Timezone: "Asia/Tokyo", Windows: &windows}); err != nil {
			return fmt.Errorf("%s の週間空き時間: %w", name, err)
		}
	}
	return nil
}

func (s *Seeder) createDemoBook(ctx context.Context, ownerID, groupID, title, from, to string) (apitypes.ReadingBook, error) {
	res, err := s.coord.CreateReadingBook(ctx, ownerID, groupID, apitypes.CreateReadingBookInput{
		Title: title, TocSource: json.RawMessage(`{"kind":"manual","urls":[]}`), Sections: demoBookSections,
		PeriodStart: from, PeriodEnd: to, PlannedSessionCount: 4, DurationMinutes: 60, AdjustmentLeadDays: 7,
	}, nil)
	if err != nil {
		return apitypes.ReadingBook{}, err
	}
	var out apitypes.CreateReadingBookResult
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return apitypes.ReadingBook{}, err
	}
	return out.Book, nil
}

func (s *Seeder) approveBook(ctx context.Context, users map[string]string, groupID, bookID string) error {
	for _, name := range []string{"A", "B", "C", "D"} {
		d, err := s.coord.ReadingBookDetail(ctx, users[name], groupID, bookID)
		if err != nil {
			return err
		}
		if !d.Permissions.CanRespondAssignment {
			continue
		}
		if _, err := s.coord.RespondBookAssignment(ctx, users[name], groupID, bookID, apitypes.BookAssignmentInput{Decision: "accept"}, nil); err != nil {
			return fmt.Errorf("%s の担当承認: %w", name, err)
		}
	}
	d, err := s.coord.ReadingBookDetail(ctx, users["A"], groupID, bookID)
	if err != nil {
		return err
	}
	if d.Book.PlanStatus != "approved" {
		return fmt.Errorf("ブック計画が成立しませんでした: %s", d.Book.PlanStatus)
	}
	return nil
}

func (s *Seeder) scheduleFirstSession(ctx context.Context, users map[string]string, groupID, bookID string) (apitypes.ReadingBookDetail, error) {
	if _, err := s.coord.ProcessDue(ctx); err != nil {
		return apitypes.ReadingBookDetail{}, err
	}
	d, err := s.coord.ReadingBookDetail(ctx, users["A"], groupID, bookID)
	if err != nil {
		return d, err
	}
	if len(d.Sessions) == 0 || d.Sessions[0].Session == nil {
		return d, fmt.Errorf("第1回の調整が始まりませんでした")
	}
	sessionID := d.Sessions[0].Session.ID
	for _, name := range []string{"A", "B", "C", "D"} {
		if err := s.putPrep(ctx, users[name], sessionID, prep(false)); err != nil {
			return d, err
		}
	}
	if _, err := s.coord.ProcessDue(ctx); err != nil {
		return d, err
	}
	for _, name := range []string{"A", "B", "C", "D"} {
		if _, err := s.respondIfOpen(ctx, users[name], sessionID, "assignment", "accept"); err != nil {
			return d, err
		}
	}
	for _, name := range []string{"A", "B", "C", "D"} {
		if _, err := s.respondIfOpen(ctx, users[name], sessionID, "approval", "approve"); err != nil {
			return d, err
		}
	}
	return s.coord.ReadingBookDetail(ctx, users["A"], groupID, bookID)
}

func (s *Seeder) respondIfOpen(ctx context.Context, userID, sessionID, kind, decision string) (bool, error) {
	d, err := s.coord.SessionDetail(ctx, userID, sessionID)
	if err != nil {
		return false, err
	}
	for _, task := range d.MyTasks {
		if task.Kind != kind || task.Status != "open" {
			continue
		}
		_, err := s.coord.RespondTask(ctx, userID, task.ID, apitypes.TaskResponseInput{Decision: decision, ProposalID: task.ProposalID, ProposalVersion: task.ProposalVersion}, nil)
		return true, err
	}
	return false, nil
}

func (s *Seeder) seedBookScenario(ctx context.Context, scenario string, now time.Time, users map[string]string, g apitypes.Group) (SeedResult, error) {
	if err := s.setDemoAvailability(ctx, users); err != nil {
		return SeedResult{}, err
	}
	startOffset := 8
	if scenario == "assignee_confirmation_demo" {
		startOffset = 7 // 7日前の調整開始日は投入時点ですでに到来している。
	}
	var books []apitypes.ReadingBook
	first, err := s.createDemoBook(ctx, users["A"], g.ID, "エージェント設計入門", day(now, startOffset), day(now, startOffset+27))
	if err != nil {
		return SeedResult{}, err
	}
	books = append(books, first)
	if scenario != "assignee_confirmation_demo" {
		second, err := s.createDemoBook(ctx, users["A"], g.ID, "信頼できるAIシステム", day(now, startOffset), day(now, startOffset+27))
		if err != nil {
			return SeedResult{}, err
		}
		books = append(books, second)
	}
	if _, err := s.coord.ProcessDue(ctx); err != nil { // 全体計画を作る
		return SeedResult{}, err
	}
	if scenario != "book_plan_demo" {
		for _, book := range books {
			if err := s.approveBook(ctx, users, g.ID, book.ID); err != nil {
				return SeedResult{}, err
			}
		}
	}

	result := SeedResult{Scenario: scenario, Users: demoUsers, GroupID: g.ID, BookIDs: []string{}, SlotIDs: []string{}}
	for _, book := range books {
		result.BookIDs = append(result.BookIDs, book.ID)
		d, err := s.coord.ReadingBookDetail(ctx, users["A"], g.ID, book.ID)
		if err != nil {
			return SeedResult{}, err
		}
		for _, slot := range d.Sessions {
			result.SlotIDs = append(result.SlotIDs, slot.SlotID)
		}
		if scenario == "book_schedule_demo" && len(d.Sessions) > 0 && d.Sessions[0].AdjustmentStartsOn != nil && result.NextTransitionAt == nil {
			at, err := time.ParseInLocation("2006-01-02", *d.Sessions[0].AdjustmentStartsOn, time.FixedZone("JST", 9*60*60))
			if err == nil {
				result.NextTransitionAt = &at
			}
		}
	}
	if scenario == "assignee_confirmation_demo" {
		d, err := s.scheduleFirstSession(ctx, users, g.ID, first.ID)
		if err != nil {
			return SeedResult{}, err
		}
		if len(d.Sessions) == 0 || d.Sessions[0].Session == nil || d.Sessions[0].SchedulingStatus != "scheduled" {
			return SeedResult{}, fmt.Errorf("第1回の日程を確定できませんでした")
		}
		result.SessionID = d.Sessions[0].Session.ID
		at := d.Sessions[0].Session.StartsAt.Add(-72 * time.Hour)
		result.NextTransitionAt = &at
	}
	if err := s.st.Tx(ctx, func(tx *store.Tx) error { return tx.DeleteNotifications(ctx) }); err != nil {
		return SeedResult{}, err
	}
	return result, nil
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
