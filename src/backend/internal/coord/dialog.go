package coord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// 対話での参加条件の更新（.agent/kasa/decisions/discord-interactive-agent.md）。
// ここが持つのは DB・認可・検証だけで、会話の状態は呼び出し側（Discord 層）がメモリに持つ。
// 原文は解釈用モデルへ渡すだけで、状態にも記録にも残さない。

// MaxDialogTargets は対象の選択肢として返す開催回の数の上限。
const MaxDialogTargets = 25

// DialogTarget は対話の対象にできる開催回。
type DialogTarget struct {
	SessionID      string
	Title          string
	StartsAt       time.Time
	ScheduleStatus string
	// HasOpenTask は本人宛ての未回答・期限内の参加条件確認タスクがあること。
	HasOpenTask bool
}

// DialogState は対話の途中経過。保存しない値だけを持つ。
type DialogState struct {
	SessionID string
	// Revision は会話の開始時に読んだ開催回の版。保存時に競合の検出に使う。
	Revision   int64
	Attendance string
	Data       json.RawMessage
	// Unclear はまだ確定していない項目名。
	Unclear []string
	// Pending はいま聞いている項目名。
	Pending string
}

// DialogResult は1ターンの結果。Bot の発話はこの値からプログラムが組み立てる。
type DialogResult struct {
	State DialogState
	// Question は次に聞く定型文。空なら確認へ進める。
	Question string
	// Confirm は確認表示に出す全項目。Ready のときだけ埋まる。
	Confirm []string
	// Ready は全項目が確定し、保存前の全体検証も通ったこと。
	Ready bool
	// OutOfScope は参加条件では扱えない依頼だったこと。このとき値は変えていない。
	OutOfScope string
	// Progressed は値か未確定項目が動いたこと。空振りの判定に使う。
	Progressed bool
}

// PreparationPrompter は未確定の項目を本人に聞く定型文を用途ごとに返す。
// 文面はプログラムが用意し、LLM には書かせない。
type PreparationPrompter interface {
	SlotQuestion(s Snapshot, slot string) string
}

// PreparationTargets は本人が参加条件を更新できる開催回を、開催が近い順に返す。
// 未回答の確認タスクがある開催回を先に置く。
func (c *Coordinator) PreparationTargets(ctx context.Context, userID string) ([]DialogTarget, error) {
	var out []DialogTarget
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		targets, err := tx.PreparationTargetsByUser(ctx, userID, c.now(), MaxDialogTargets)
		if err != nil {
			return err
		}
		out = make([]DialogTarget, 0, len(targets))
		for _, t := range targets {
			out = append(out, DialogTarget{
				SessionID: t.Session.ID, Title: t.Session.Title, StartsAt: t.Session.StartsAt, ScheduleStatus: scheduleStatus(t.Session), HasOpenTask: t.HasOpenTask,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 未回答の依頼がある回を先に出す。同じ条件なら開催が近い順（DB の並び）のまま。
	withTask := make([]DialogTarget, 0, len(out))
	rest := make([]DialogTarget, 0, len(out))
	for _, t := range out {
		if t.HasOpenTask {
			withTask = append(withTask, t)
			continue
		}
		rest = append(rest, t)
	}
	return append(withTask, rest...), nil
}

// StartDialog は保存済みの参加条件と版を同じスナップショットから読み、会話の初期状態を作る。
// 保存済みの値があればそれを初期値とし、未登録なら全項目を未確定にする。
func (c *Coordinator) StartDialog(ctx context.Context, userID, sessionID string) (DialogResult, error) {
	var out DialogResult
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		sess, m, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return err
		}
		if err := checkNotStarted(sess, c.now()); err != nil {
			return err
		}
		pi, err := c.preparationInterpreter(sess.PlaybookID)
		if err != nil {
			return err
		}
		snap, err := c.snapshot(ctx, tx, sess, nil)
		if err != nil {
			return err
		}
		preps, err := tx.Preparations(ctx, sess.ID)
		if err != nil {
			return err
		}
		st := DialogState{SessionID: sess.ID, Revision: sess.Revision}
		if p, ok := preps[m.ID]; ok {
			// 保存済みの値はすべて確定扱い。触れなかった項目はそのまま残る。
			st.Attendance, st.Data, st.Unclear = p.Attendance, p.Data, []string{}
			st.Data, st.Unclear, err = pi.ValidatePartialPreparation(ctx, snap, st.Attendance, st.Data, st.Unclear)
			if err != nil {
				return err
			}
		} else {
			st.Attendance = AttendanceAttending
			st.Data, st.Unclear, err = pi.ValidatePartialPreparation(ctx, snap, st.Attendance, emptyPreparationData, allSlots(pi))
			if err != nil {
				return err
			}
		}
		out, err = c.dialogResult(ctx, pi, snap, st, false)
		return err
	})
	return out, err
}

// ContinueDialog は1つの発言を解釈して会話の状態を進める。保存はしない。
func (c *Coordinator) ContinueDialog(ctx context.Context, userID string, st DialogState, rawText string) (DialogResult, error) {
	var out DialogResult
	text, err := checkInterpretText(rawText)
	if err != nil {
		return out, err
	}
	if c.interpreter == nil {
		return out, apperr.New(apperr.UnsupportedPlaybook, "自由文の解釈は利用できません。")
	}
	var (
		snap   Snapshot
		pi     PreparationInterpreter
		caseID string
	)
	err = c.st.Tx(ctx, func(tx *store.Tx) error {
		sess, _, err := c.access(ctx, tx, userID, st.SessionID)
		if err != nil {
			return err
		}
		if err := checkNotStarted(sess, c.now()); err != nil {
			return err
		}
		if pi, err = c.preparationInterpreter(sess.PlaybookID); err != nil {
			return err
		}
		if caseID, err = latestCaseID(ctx, tx, sess.ID); err != nil {
			return err
		}
		snap, err = c.snapshot(ctx, tx, sess, nil)
		return err
	})
	if err != nil {
		return out, err
	}

	current := Interpretation{Attendance: st.Attendance, Data: st.Data, Unclear: nonNilStrings(st.Unclear), NeedsFollowup: len(st.Unclear) > 0}
	req := InterpretRequest{
		Snapshot: snap, Playbook: pi, Text: text, MaxLLMCalls: MaxLLMCallsPerInterpretation,
		Current: &current, Pending: st.Pending, Partial: true,
		Check: func(ctx context.Context, got Interpretation) error {
			_, _, err := c.checkInterpretation(ctx, pi, snap, got, true)
			return err
		},
	}
	c.budgetHooks(&req, st.SessionID, caseID)
	res, _, ierr := c.interpreter.Interpret(ctx, req)
	if err := c.interpretError(ierr, st.SessionID); err != nil {
		return out, err
	}
	if res.OutOfScope != "" {
		// 扱えない依頼。値は変えず、進捗にも数えない。
		out, err = c.dialogResult(ctx, pi, snap, st, false)
		out.OutOfScope = res.OutOfScope
		return out, err
	}
	data, unclear, err := c.checkInterpretation(ctx, pi, snap, res, true)
	if err != nil {
		return out, apperr.New(apperr.TemporarilyUnavailable, "発言から項目を取り出せませんでした。もう一度、短く書いてください。")
	}

	next := DialogState{SessionID: st.SessionID, Revision: st.Revision, Attendance: res.Attendance, Data: data, Unclear: unclear}
	progressed := next.Attendance != st.Attendance || !jsonEqual(next.Data, st.Data) || !sameStrings(next.Unclear, st.Unclear)
	return c.dialogResult(ctx, pi, snap, next, progressed)
}

// dialogResult は状態から次の質問または確認表示を決める。文面はここで決め、LLM には書かせない。
func (c *Coordinator) dialogResult(ctx context.Context, pi PreparationInterpreter, snap Snapshot, st DialogState, progressed bool) (DialogResult, error) {
	out := DialogResult{State: st, Progressed: progressed}
	out.State.Unclear = nonNilStrings(out.State.Unclear)
	if len(out.State.Unclear) == 0 {
		// 全項目が確定したら、保存と同じ全体検証をここで当てる。
		data, err := pi.ValidatePreparation(ctx, snap, st.Attendance, st.Data)
		if err == nil {
			out.State.Data, out.State.Pending = data, ""
			out.Ready = true
			out.Confirm = preparationDiff(pi, snap, nil, Preparation{Attendance: st.Attendance, Data: data})
			return out, nil
		}
		var v *ValidationError
		if !errors.As(err, &v) {
			return out, err
		}
		// 項目間の矛盾は、対応する項目を未確定に戻して聞き直す（勝手に値を足さない）。
		out.State.Unclear = conflictingSlots(pi, v)
		if len(out.State.Unclear) == 0 {
			return out, err
		}
	}
	out.State.Pending = out.State.Unclear[0]
	out.Question = slotQuestion(pi, snap, out.State.Pending)
	return out, nil
}

// SaveDialogPreparation は確認済みの下書きを保存する。保存の経路・認可・冪等性は Web と同じ。
func (c *Coordinator) SaveDialogPreparation(ctx context.Context, userID string, st DialogState, idem *store.IdemKey) (store.Response, error) {
	if len(st.Unclear) > 0 {
		return store.Response{}, apperr.InvalidStateErr("まだ確定していない項目があります。")
	}
	rev := st.Revision
	in := apitypes.PutPreparationInput{
		ExpectedRevision: &rev,
		Preparation:      &apitypes.Preparation{Attendance: st.Attendance, Data: st.Data},
	}
	return c.putPreparation(ctx, userID, st.SessionID, in, idem, EntryDiscord)
}

// emptyPreparationData は未登録のときの出発点。用途の検証で型どおりの初期値に正規化される。
var emptyPreparationData = json.RawMessage(`{}`)

// allSlots は共通項目と用途の項目を合わせた全項目名。
func allSlots(pi PreparationInterpreter) []string {
	return append([]string{SlotAttendance}, pi.PreparationSlots()...)
}

func slotQuestion(pi PreparationInterpreter, s Snapshot, slot string) string {
	if p, ok := pi.(PreparationPrompter); ok {
		if q := p.SlotQuestion(s, slot); q != "" {
			return q
		}
	}
	return "「" + slot + "」を教えてください。"
}

// conflictingSlots は検証エラーの項目名を未確定へ戻す対象として返す。
func conflictingSlots(pi PreparationInterpreter, v *ValidationError) []string {
	known := map[string]bool{}
	for _, name := range allSlots(pi) {
		known[name] = true
	}
	var out []string
	for _, f := range v.Fields {
		name := f.Path
		if i := strings.IndexAny(name, "[."); i >= 0 {
			name = name[:i]
		}
		if known[name] && !containsString(out, name) {
			out = append(out, name)
		}
	}
	// 聞き取りの順に並べ直す。
	ordered := make([]string, 0, len(out))
	for _, name := range allSlots(pi) {
		if containsString(out, name) {
			ordered = append(ordered, name)
		}
	}
	return ordered
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DialogUser は Discord の発言者に対応する利用者。
type DialogUser struct {
	ID          string
	DisplayName string
}

// UserByDiscordID は Discord のユーザーIDから登録済みの利用者を引く。
// 見つからなければ NotFound を返す。DM の受信を理由に利用者や所属を作らない。
func (c *Coordinator) UserByDiscordID(ctx context.Context, discordUserID string) (DialogUser, error) {
	var out DialogUser
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		u, err := tx.UserByDiscordID(ctx, discordUserID)
		if errors.Is(err, store.ErrNotFound) {
			return apperr.NotFoundErr()
		}
		if err != nil {
			return err
		}
		out = DialogUser{ID: u.ID, DisplayName: u.DisplayName}
		return nil
	})
	return out, err
}
