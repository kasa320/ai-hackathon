package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// 期限と上限（spec.md 第4節）。
const (
	// TaskTTL はタスクの回答期限の基本値。開催1時間前の方が早ければそちらを使う。
	TaskTTL = 24 * time.Hour
	// ResponseCutoff は開催前に判断を終えるための余裕。回答期限は開催のこの時間前まで。
	ResponseCutoff = time.Hour
	// MinResponseWindow は「回答期限を確保できる」とみなす最短の回答時間。
	MinResponseWindow = 30 * time.Minute
	// SessionMinLead は開催回を登録できる最短の開催までの時間。
	SessionMinLead = time.Hour

	MaxLLMCallsPerRun   = 8
	MaxLLMCallsPerCase  = 24
	MaxToolCallsPerCase = 24

	RetryBase   = 30 * time.Second
	RetryMax    = 10 * time.Minute
	RetryJitter = 0.2
)

// displayZone は利用者向けの説明文で時刻を表示するタイムゾーン。API の日時は常に UTC で返す。
var displayZone = time.FixedZone("JST", 9*60*60)

// Options は Coordinator の設定。
type Options struct {
	// PublicBaseURL は通知に載せる画面URLの基点（例：https://example.com）。
	PublicBaseURL string
	// Rand は再試行のゆらぎに使う [0,1) の乱数。nil なら math/rand。
	Rand func() float64
	Log  *slog.Logger
	// RunLock はイベント処理を他の処理（開発用の初期データ投入）と排他にするためのロック。nil なら使わない。
	RunLock *sync.Mutex
	// Interpreter は自由文から参加条件を取り出す処理。nil なら自由文の解釈を提供しない。
	Interpreter Interpreter
	// WeeklyInterpreter は自由文から普段の空き時間の下書きを取り出す処理。nil なら規則だけで抽出する（DraftOnlyWeeklyInterpreter）。
	WeeklyInterpreter WeeklyInterpreter
	// BookAgent はブックの全体計画と担当変更の候補を作る処理。nil なら規則だけで作る（DraftBookAgent）。
	BookAgent BookAgent
}

// Coordinator は用途共通の調整処理。状態遷移・本人と版の検証・同意管理・イベント処理を担う。
// 用途固有の判断は Playbook に委ね、playbook_id による分岐を持たない。
type Coordinator struct {
	reg               *Service
	st                *store.Store
	clock             clock.Clock
	planner           Planner
	interpreter       Interpreter
	weeklyInterpreter WeeklyInterpreter
	bookAgent         BookAgent
	opts              Options
	log               *slog.Logger
	wake              chan struct{}
}

func NewCoordinator(reg *Service, st *store.Store, clk clock.Clock, planner Planner, opts Options) *Coordinator {
	if opts.Rand == nil {
		opts.Rand = rand.Float64
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	agent := opts.BookAgent
	if agent == nil {
		agent = DraftBookAgent{}
	}
	wi := opts.WeeklyInterpreter
	if wi == nil {
		wi = DraftOnlyWeeklyInterpreter{}
	}
	return &Coordinator{reg: reg, st: st, clock: clk, planner: planner, interpreter: opts.Interpreter, weeklyInterpreter: wi, bookAgent: agent, opts: opts, log: log, wake: make(chan struct{}, 1)}
}

// Playbooks は登録済み用途の一覧。
func (c *Coordinator) Playbooks() []Descriptor { return c.reg.Playbooks() }

// now はミリ秒に丸めた現在時刻（UTC）。
func (c *Coordinator) now() time.Time { return c.clock.Now().UTC().Truncate(time.Millisecond) }

// Wake はイベント処理を待たずに実行させる。
func (c *Coordinator) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// dueAt は今から依頼するタスクの回答期限を返す。回答期限を確保できなければ ok=false。
func dueAt(now, startsAt time.Time) (time.Time, bool) {
	due := now.Add(TaskTTL)
	if limit := startsAt.Add(-ResponseCutoff); limit.Before(due) {
		due = limit
	}
	return due, due.Sub(now) >= MinResponseWindow
}

// retryCutoff は AI 処理の再試行を打ち切る時刻（次に必要な回答期限を確保できなくなる時刻）。
func retryCutoff(startsAt time.Time) time.Time {
	return startsAt.Add(-ResponseCutoff - MinResponseWindow)
}

// Before a date is agreed, the arbitrary placeholder must not close the whole period.
func responseHorizon(sess store.Session) time.Time {
	if sess.ScheduleStatus == ScheduleProposed {
		if day, err := time.ParseInLocation("2006-01-02", sess.PeriodEnd, time.FixedZone("JST", 9*3600)); err == nil {
			return day.Add(24 * time.Hour)
		}
	}
	return sess.StartsAt
}

func (c *Coordinator) retryDelay(retryCount int) time.Duration {
	d := RetryBase
	for i := 0; i < retryCount && d < RetryMax; i++ {
		d *= 2
	}
	if d > RetryMax {
		d = RetryMax
	}
	jitter := 1 + RetryJitter*(2*c.opts.Rand()-1)
	return time.Duration(float64(d) * jitter)
}

func (c *Coordinator) sessionURL(sessionID string) string {
	return c.opts.PublicBaseURL + "/session.html?id=" + sessionID
}

func (c *Coordinator) playbook(id string) (Playbook, error) {
	return c.reg.Playbook(id)
}

// access は利用者が開催回のグループに所属していることを確認する。所属していなければ存在も含めて 404。
func (c *Coordinator) access(ctx context.Context, tx *store.Tx, userID, sessionID string) (store.Session, store.Member, error) {
	sess, err := tx.Session(ctx, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return sess, store.Member{}, apperr.NotFoundErr()
	}
	if err != nil {
		return sess, store.Member{}, err
	}
	m, err := tx.MemberByUser(ctx, sess.GroupID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return sess, m, apperr.NotFoundErr()
	}
	return sess, m, err
}

func (c *Coordinator) groupAccess(ctx context.Context, tx *store.Tx, userID, groupID string) (store.Group, store.Member, error) {
	g, err := tx.Group(ctx, groupID)
	if errors.Is(err, store.ErrNotFound) {
		return g, store.Member{}, apperr.NotFoundErr()
	}
	if err != nil {
		return g, store.Member{}, err
	}
	m, err := tx.MemberByUser(ctx, groupID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return g, m, apperr.NotFoundErr()
	}
	return g, m, err
}

// CheckSessionAccess は再送判定の前に現在のアクセス権だけを確認する。
func (c *Coordinator) CheckSessionAccess(ctx context.Context, userID, sessionID string) error {
	return c.st.Tx(ctx, func(tx *store.Tx) error {
		_, _, err := c.access(ctx, tx, userID, sessionID)
		return err
	})
}

// CheckGroupAccess はグループへの所属（ownerOnly なら管理者）を確認する。
func (c *Coordinator) CheckGroupAccess(ctx context.Context, userID, groupID string, ownerOnly bool) (store.Member, error) {
	var m store.Member
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		_, m, err = c.groupAccess(ctx, tx, userID, groupID)
		if err == nil && ownerOnly && m.Role != "owner" {
			return apperr.ForbiddenErr()
		}
		return err
	})
	return m, err
}

// CheckTaskAccess はタスクが本人宛てであることを確認する。自分以外のタスクは同じグループでも 404。
func (c *Coordinator) CheckTaskAccess(ctx context.Context, userID, taskID string) error {
	return c.st.Tx(ctx, func(tx *store.Tx) error {
		_, _, _, err := c.taskAccess(ctx, tx, userID, taskID)
		return err
	})
}

func (c *Coordinator) taskAccess(ctx context.Context, tx *store.Tx, userID, taskID string) (store.Task, store.Session, store.Member, error) {
	tk, err := tx.Task(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return tk, store.Session{}, store.Member{}, apperr.NotFoundErr()
	}
	if err != nil {
		return tk, store.Session{}, store.Member{}, err
	}
	sess, m, err := c.access(ctx, tx, userID, tk.SessionID)
	if err != nil {
		return tk, sess, m, err
	}
	if tk.MemberID != m.ID {
		return tk, sess, m, apperr.NotFoundErr()
	}
	return tk, sess, m, nil
}

// snapshot は Playbook に渡す開催回の状態を組み立てる。cs が nil なら案件内の経過は空。
func (c *Coordinator) snapshot(ctx context.Context, tx *store.Tx, sess store.Session, cs *store.Case) (Snapshot, error) {
	s := Snapshot{
		SessionID:       sess.ID,
		Now:             c.now(),
		Revision:        sess.Revision,
		StartsAt:        sess.StartsAt,
		ScheduleStatus:  scheduleStatus(sess),
		PeriodStart:     sess.PeriodStart,
		PeriodEnd:       sess.PeriodEnd,
		DurationMinutes: sess.DurationMinutes,
		SessionData:     sess.Data,
		Case:            CaseContext{DeclinedMemberIDs: []string{}, WithdrawnMemberIDs: []string{}, AskedMemberIDs: []string{}},
	}
	members, err := tx.SessionMembers(ctx, sess.ID)
	if err != nil {
		return s, err
	}
	preps, err := tx.Preparations(ctx, sess.ID)
	if err != nil {
		return s, err
	}
	for _, m := range members {
		if m.LeftAt != nil {
			continue
		}
		sm := SnapshotMember{ID: m.ID, DisplayName: m.DisplayName, Role: m.Role}
		if m.UserID != "" {
			if sm.Standing, err = c.standingAvailability(ctx, tx, m.UserID); err != nil {
				return s, err
			}
		}
		s.Members = append(s.Members, sm)
		if m.Role == "owner" {
			s.OwnerMemberID = m.ID
		}
		mp := MemberPreparation{MemberID: m.ID}
		if p, ok := preps[m.ID]; ok {
			mp.Value = &Preparation{Attendance: p.Attendance, Data: p.Data}
		}
		s.Preparations = append(s.Preparations, mp)
	}
	s.ConfirmedPlans, err = c.confirmedPlans(ctx, tx, sess.ID)
	if err != nil {
		return s, err
	}
	if sess.ScheduleStatus == ScheduleProposed {
		// 日時を決める回だけ、メンバーの別の確定済み予定を確認する。
		busy, err := tx.ConfirmedBusyForSession(ctx, sess.ID, s.Now.Add(-24*time.Hour))
		if err != nil {
			return s, err
		}
		for _, b := range busy {
			s.BusyIntervals = append(s.BusyIntervals, BusyInterval{StartsAt: b.StartsAt, EndsAt: b.StartsAt.Add(time.Duration(b.DurationMinutes) * time.Minute)})
		}
	}
	// 同じグループの過去の開催回（新しい順）。代役の偏りの判定に使う。
	all, err := tx.SessionsByGroup(ctx, sess.GroupID)
	if err != nil {
		return s, err
	}
	for _, other := range all {
		if other.ID == sess.ID || !other.StartsAt.Before(sess.StartsAt) {
			continue
		}
		plans, err := c.confirmedPlans(ctx, tx, other.ID)
		if err != nil {
			return s, err
		}
		if len(plans) > 0 {
			s.History = append(s.History, PastSession{SessionID: other.ID, StartsAt: other.StartsAt, ConfirmedPlans: plans})
		}
		if len(s.History) >= 5 {
			break
		}
	}
	if cs != nil {
		s.CaseID = cs.ID
		s.Case.WithdrawnMemberIDs = append(s.Case.WithdrawnMemberIDs, cs.WithdrawnMemberIDs...)
		tasks, err := tx.TasksByCase(ctx, cs.ID)
		if err != nil {
			return s, err
		}
		for _, tk := range tasks {
			switch {
			case tk.Kind == store.TaskAssignment && tk.Decision == "decline":
				s.Case.DeclinedMemberIDs = appendUnique(s.Case.DeclinedMemberIDs, tk.MemberID)
			case tk.Kind == store.TaskPreparation && tk.RequestedBy == "agent":
				s.Case.AskedMemberIDs = appendUnique(s.Case.AskedMemberIDs, tk.MemberID)
			}
		}
		if s.Case.RejectedProposals, err = tx.CountRejectedProposals(ctx, cs.ID); err != nil {
			return s, err
		}
		if s.ScheduleStatus == ScheduleProposed {
			pb, err := c.playbook(sess.PlaybookID)
			if err != nil {
				return s, err
			}
			rejected, err := tx.RejectedProposals(ctx, cs.ID)
			if err != nil {
				return s, err
			}
			for _, p := range rejected {
				if at, ok := plannedStart(pb, p.Data); ok {
					s.Case.RejectedStartsAt = append(s.Case.RejectedStartsAt, at)
				}
			}
		}
	}
	return s, nil
}

func (c *Coordinator) confirmedPlans(ctx context.Context, tx *store.Tx, sessionID string) ([]PlanRecord, error) {
	ps, err := tx.ConfirmedProposals(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := []PlanRecord{}
	for _, p := range ps {
		out = append(out, PlanRecord{ProposalID: p.ID, Version: p.Version, ChangeKind: p.ChangeKind, Data: p.Data})
	}
	return out, nil
}

func appendUnique(ids []string, id string) []string {
	for _, x := range ids {
		if x == id {
			return ids
		}
	}
	return append(ids, id)
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// jsonEqual は2つの JSON が同じ値かを比べる（キーの順序・空白は無視）。
func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

// bump は開催回の revision を1つ進める（参加条件・有効な案・確定計画が変わったとき）。
func bump(ctx context.Context, tx *store.Tx, sess *store.Session, now time.Time) error {
	sess.Revision++
	sess.UpdatedAt = now
	return tx.UpdateSessionState(ctx, *sess)
}

func (c *Coordinator) activity(ctx context.Context, tx *store.Tx, sess store.Session, caseID, kind, summary, proposalID string, now time.Time) error {
	return tx.AddActivity(ctx, store.Activity{SessionID: sess.ID, CaseID: caseID, Kind: kind, Summary: summary, ProposalID: proposalID, OccurredAt: now})
}

// notify は通知待ちを登録する。本文には共有可能な情報とWeb画面へのリンクだけを含める。
func (c *Coordinator) notify(ctx context.Context, tx *store.Tx, sess store.Session, caseID, kind, dedupe, text string, mentions []store.Member, now time.Time) error {
	return c.notifyWithButtons(ctx, tx, sess, caseID, kind, dedupe, text, mentions, nil, now)
}

// notifyWithButtons は notify に、宛先が1人のときだけ有効なボタンを添える。
func (c *Coordinator) notifyWithButtons(ctx context.Context, tx *store.Tx, sess store.Session, caseID, kind, dedupe, text string, mentions []store.Member, buttons []ActionButton, now time.Time) error {
	var ids []string
	prefix := ""
	for _, m := range mentions {
		ids = append(ids, m.DiscordUserID)
		prefix += "<@" + m.DiscordUserID + "> "
	}
	content := fmt.Sprintf("%s【%s】%s\n%s", prefix, sess.Title, text, c.sessionURL(sess.ID))
	return tx.EnqueueNotification(ctx, store.Notification{
		ID: store.NewID("ntf"), SessionID: sess.ID, CaseID: caseID, Kind: kind, DedupeKey: dedupe,
		Content: content, Mentions: ids, Components: notifyButtons(buttons), CreatedAt: now,
	})
}

// notifyButtons は ActionButton を保存用の store.NotifyButton へ直す。custom_id は
// 「アクションID:decision」の形にし、値そのものは埋め込まない。
func notifyButtons(buttons []ActionButton) []store.NotifyButton {
	if len(buttons) == 0 {
		return nil
	}
	out := make([]store.NotifyButton, len(buttons))
	for i, b := range buttons {
		out[i] = store.NotifyButton{Label: b.Label, CustomID: "act:" + b.ActionID + ":" + b.Decision, Primary: b.Primary}
	}
	return out
}

func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func formatClock(t time.Time) string { return t.In(displayZone).Format("1/2 15:04") }
