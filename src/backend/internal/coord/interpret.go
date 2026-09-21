package coord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// 自由文の解釈（spec.md 第4節）。LLM にできるのは「決められた項目の値を決める」ことだけで、
// 同意・引き受け・保存は行わない。保存は本人が確認したあとの通常の参加条件の送信で行う。
//
// 発言者と対象者は常に同一（呼び出した本人）で、対象者を入力から受け取らない。
// 原文はアプリDB・実行ログに保存せず、解釈用モデルへの送信にだけ使う。
const (
	// MaxLLMCallsPerInterpretation は1回の解釈で使える LLM 呼び出し回数（初回＋やり直し1回）。
	MaxLLMCallsPerInterpretation = 2
	// MaxInterpretTextLen は受け付ける自由文の長さ。
	MaxInterpretTextLen = 2000
	// MaxInterpretCallsPerSession は開催回ごとに自由文の解釈へ使える LLM 呼び出しの総数。
	// Web と Discord で共通の枠とし、呼び出しの直前に1回分ずつ確保する。
	MaxInterpretCallsPerSession = 24
)

// SlotAttendance は参加条件の共通項目。これ以外の項目名は用途（PreparationSlots）が決める。
const SlotAttendance = "attendance"

// 対話で扱えない依頼の分類。案内の文面を選ぶためだけに使い、認可や予算の代わりにはしない。
const (
	OutOfScopeScheduleChange    = "schedule_change"
	OutOfScopePartialAttendance = "partial_attendance"
	OutOfScopeOtherMember       = "other_member"
	OutOfScopeOther             = "other"
)

// OutOfScopeKinds は out_of_scope に指定できる値。
var OutOfScopeKinds = []string{OutOfScopeScheduleChange, OutOfScopePartialAttendance, OutOfScopeOtherMember, OutOfScopeOther}

func validOutOfScope(s string) bool {
	for _, k := range OutOfScopeKinds {
		if s == k {
			return true
		}
	}
	return false
}

// ErrInterpretUnsupported は用途が自由文の解釈に対応していないこと。
var ErrInterpretUnsupported = errors.New("coord: interpretation is not supported")

// Interpretation は自由文から取り出した参加条件の下書き。保存はしない。
type Interpretation struct {
	Attendance string          `json:"attendance"`
	Data       json.RawMessage `json:"data"`
	// Unclear はまだ確定していない項目名。推測した値を確定値として扱わない。
	Unclear []string `json:"unclear"`
	// NeedsFollowup は本人に聞き直すべき項目が残っていること。
	NeedsFollowup bool `json:"needs_followup"`
	// OutOfScope は参加条件の変更では扱えない依頼の分類。空なら扱える。
	// 空でないとき、このターンの候補は全体を捨てて値を変えない。
	OutOfScope string `json:"out_of_scope"`
	// Question は AI が書いた、未確定の項目を本人に聞く質問文。対話でだけ使い、
	// 送る前に CleanQuestion を通す。空なら用途の既定の文を使う。
	Question string `json:"-"`
}

// CleanQuestion は AI が書いた質問文を、本人へ送れる形に整える。使えなければ空を返す。
// 改行・メンション記号・URL・制御文字を落とし、長すぎる文は使わない（既定の文に戻す）。
func CleanQuestion(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if q == "" || utf8.RuneCountInString(q) > 200 {
		return ""
	}
	lower := strings.ToLower(q)
	for _, bad := range []string{"http", "://", "www.", "discord.gg", "@", "<", ">", "`"} {
		if strings.Contains(lower, bad) {
			return ""
		}
	}
	for _, r := range q {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return q
}

// InterpretRequest は1回の解釈への入力。
type InterpretRequest struct {
	Snapshot Snapshot
	Playbook PreparationInterpreter
	// Text は本人の自由文。保存も転記もしない。
	Text string
	// Check は取り出した値をサーバーの規則で検証する。検証エラーは AI に返してやり直させる。
	Check func(ctx context.Context, in Interpretation) error
	// MaxLLMCalls はこの解釈で使える LLM 呼び出し回数。
	MaxLLMCalls int

	// Current は対話の途中経過（確定済みの値）。nil なら初回の解釈。
	Current *Interpretation
	// Pending はいま本人に聞いている項目名。「はい」のような短い返答の宛先を決めるのに使う。
	Pending string
	// Partial は未確定の項目を許す対話モード。保存直前の全体検証は別に行う。
	Partial bool

	// Reserve は LLM を呼ぶ直前に予算枠を確保し、記録IDを返す。
	// ErrBudgetExceeded を返したら呼び出してはいけない。nil なら予算を管理しない。
	Reserve func(ctx context.Context, model string) (string, error)
	// Record は呼び出しの結果を確保済みの記録へ書き込む。失敗しても枠は戻さない。
	Record func(ctx context.Context, id string, call LLMCall) error
}

// ReserveCall は LLM 呼び出しの直前に予算枠を確保する。フックが無ければ何もしない。
func (r InterpretRequest) ReserveCall(ctx context.Context, model string) (string, error) {
	if r.Reserve == nil {
		return "", nil
	}
	return r.Reserve(ctx, model)
}

// RecordCall は呼び出しの結果を記録する。記録できなくても確保した枠は戻さない。
func (r InterpretRequest) RecordCall(ctx context.Context, id string, call LLMCall) {
	if r.Record == nil || id == "" {
		return
	}
	_ = r.Record(ctx, id, call)
}

// Interpreter は自由文から用途固有の参加条件を取り出す。保存・通知・同意の作成は行わない。
type Interpreter interface {
	Interpret(ctx context.Context, req InterpretRequest) (Interpretation, Usage, error)
}

// PreparationInterpreter は自由文からの参加条件の抽出に対応する用途。
// 抽出時に AI へ渡すのは InterpretContext が返す範囲だけで、他のメンバーの回答は渡さない。
type PreparationInterpreter interface {
	Playbook
	// PreparationSchema は PreparationData の JSON Schema。AI のツール定義に使う。
	PreparationSchema() json.RawMessage
	// PreparationSlots は data の項目名。unclear に入れてよい名前を決めるのに使う。
	PreparationSlots() []string
	// InterpretInstructions は抽出時の用途ごとの判断指示。
	InterpretInstructions() string
	// InterpretContext は抽出に必要な最小限の判断材料（節一覧など）を返す。
	InterpretContext(ctx context.Context, s Snapshot) (json.RawMessage, error)
	// ValidatePartialPreparation は未確定を許す検証。unclear の項目は確定値として扱わず、
	// 確定した値から導ける従属値は正規化して返す。返す unclear は正規化後の未確定項目。
	ValidatePartialPreparation(ctx context.Context, s Snapshot, attendance string, raw json.RawMessage, unclear []string) (json.RawMessage, []string, error)
}

// DraftInterpreter は LLM を使わず用途の規則だけで抽出する。AGENT_MODE=fake で使う。
type DraftInterpreter interface {
	DraftInterpret(ctx context.Context, req InterpretRequest) (Interpretation, error)
}

// DraftOnlyInterpreter は LLM を呼ばず、用途の規則（DraftInterpreter）だけで抽出する。
// LLM を呼ばないので予算の枠も確保しない。
type DraftOnlyInterpreter struct{}

var _ Interpreter = DraftOnlyInterpreter{}

func (DraftOnlyInterpreter) Interpret(ctx context.Context, req InterpretRequest) (Interpretation, Usage, error) {
	di, ok := req.Playbook.(DraftInterpreter)
	if !ok {
		return Interpretation{}, Usage{}, ErrInterpretUnsupported
	}
	in, err := di.DraftInterpret(ctx, req)
	if err != nil {
		return Interpretation{}, Usage{}, err
	}
	if err := req.Check(ctx, in); err != nil {
		return Interpretation{}, Usage{}, ErrInvalidOutput
	}
	return in, Usage{}, nil
}

// interpretLookupID は自由文の解釈に使った呼び出しを開催回ごとに数えるための集計キー。
// 目次取得の lookup ID と衝突しないよう名前空間を付ける。
func interpretLookupID(sessionID string) string { return "preparation:" + sessionID }

// reserveInterpretCall は LLM 呼び出し1回分の枠を確保し、記録の行を先に作る。
// 件数の確認と行の作成を同じトランザクションで行い、同時受信ですり抜けないようにする。
func (c *Coordinator) reserveInterpretCall(ctx context.Context, sessionID, caseID, model string) (string, error) {
	lookupID := interpretLookupID(sessionID)
	var id string
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		used, err := tx.CountLLMCallsByLookup(ctx, lookupID)
		if err != nil {
			return err
		}
		if used+1 > MaxInterpretCallsPerSession {
			return ErrBudgetExceeded
		}
		// 案件があるときは、案件全体の上限（計画の呼び出しを含む）も超えない。
		if caseID != "" {
			usedCase, err := tx.CountLLMCallsByCase(ctx, caseID)
			if err != nil {
				return err
			}
			if usedCase+1 > MaxLLMCallsPerCase {
				return ErrBudgetExceeded
			}
		}
		id = store.NewID("llm")
		// 費用が分かるまでは succeeded=0・金額 NULL。これを「未送信／無料」の証拠にしない。
		return tx.AddLLMCall(ctx, store.LLMCall{
			ID: id, CaseID: caseID, LookupID: lookupID, Model: model, Purpose: PurposeInterpreter,
			Currency: "unknown", CreatedAt: c.now(),
		})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// recordInterpretCall は確保済みの記録へ結果を書き込む。受信リクエストの取消からは独立させる。
func (c *Coordinator) recordInterpretCall(ctx context.Context, id string, call LLMCall) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		return tx.UpdateLLMCall(ctx, store.LLMCall{
			ID: id, Model: call.Model, ResolvedModel: call.ResolvedModel,
			FallbackLevel: call.FallbackLevel, RequestID: call.RequestID, LatencyMS: call.LatencyMS,
			InputTokens: call.InputTokens, OutputTokens: call.OutputTokens,
			Currency: call.Currency, EstimatedAmount: call.EstimatedAmount, BilledAmount: call.BilledAmount, Succeeded: call.Succeeded,
		})
	})
	if err != nil {
		// 記録できなくても確保した枠は残す（費用は不明のまま）。
		c.log.Warn("AI呼び出しの記録に失敗", "llm_call_id", id)
	}
	return err
}

// budgetHooks は解釈1回分の予算フックを組み立てる。
func (c *Coordinator) budgetHooks(req *InterpretRequest, sessionID, caseID string) {
	req.Reserve = func(ctx context.Context, model string) (string, error) {
		return c.reserveInterpretCall(ctx, sessionID, caseID, model)
	}
	req.Record = func(ctx context.Context, id string, call LLMCall) error {
		return c.recordInterpretCall(ctx, id, call)
	}
}

// InterpretPreparation は本人の自由文から参加条件の下書きを作って返す。保存はしない。
// 対象者は常に呼び出した本人で、入力からメンバーIDを受け取らない。
func (c *Coordinator) InterpretPreparation(ctx context.Context, userID, sessionID string, in apitypes.InterpretPreparationInput) (apitypes.PreparationInterpretation, error) {
	var out apitypes.PreparationInterpretation
	text, err := checkInterpretText(in.Text)
	if err != nil {
		return out, err
	}
	if c.interpreter == nil {
		return out, apperr.New(apperr.UnsupportedPlaybook, "自由文の解釈は利用できません。")
	}

	var (
		snap    Snapshot
		pi      PreparationInterpreter
		caseID  string
		current *Interpretation
	)
	err = c.st.Tx(ctx, func(tx *store.Tx) error {
		sess, member, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return err
		}
		if err := checkNotStarted(sess, c.now()); err != nil {
			return err
		}
		pi, err = c.preparationInterpreter(sess.PlaybookID)
		if err != nil {
			return err
		}
		caseID, err = latestCaseID(ctx, tx, sess.ID)
		if err != nil {
			return err
		}
		snap, err = c.snapshot(ctx, tx, sess, nil)
		if err == nil {
			if p := snap.Preparation(member.ID); p != nil {
				current = &Interpretation{Attendance: p.Attendance, Data: p.Data, Unclear: []string{}}
			}
		}
		return err
	})
	if err != nil {
		return out, err
	}

	// LLM 呼び出しはトランザクションの外で行う。結果は保存せず、本人の確認後に通常の送信で保存される。
	req := InterpretRequest{
		Snapshot: snap, Playbook: pi, Text: text, MaxLLMCalls: MaxLLMCallsPerInterpretation,
		Current: current,
		Check: func(ctx context.Context, got Interpretation) error {
			_, _, err := c.checkInterpretation(ctx, pi, snap, got, false)
			return err
		},
	}
	c.budgetHooks(&req, sessionID, caseID)
	res, _, ierr := c.interpreter.Interpret(ctx, req)
	if err := c.interpretError(ierr, sessionID); err != nil {
		return out, err
	}
	if res.OutOfScope != "" {
		return out, apperr.InvalidStateErr("この内容は参加条件の項目では扱えません。フォームから入力してください。")
	}

	// 返す値はサーバーが正規化したもの。AI の出力をそのまま返さない。
	data, unclear, err := c.checkInterpretation(ctx, pi, snap, res, false)
	if err != nil {
		return out, apperr.New(apperr.TemporarilyUnavailable, "発言から項目を取り出せませんでした。フォームから入力してください。")
	}
	return apitypes.PreparationInterpretation{
		Preparation:   apitypes.Preparation{Attendance: res.Attendance, Data: data},
		Unclear:       unclear,
		NeedsFollowup: res.NeedsFollowup || len(unclear) > 0,
		Saved:         false,
	}, nil
}

func checkInterpretText(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	switch {
	case text == "":
		return "", apperr.Validation(apperr.Field{Path: "text", Message: "必須です"})
	case utf8.RuneCountInString(text) > MaxInterpretTextLen:
		return "", apperr.Validation(apperr.Field{Path: "text", Message: "長すぎます"})
	}
	return text, nil
}

func (c *Coordinator) preparationInterpreter(playbookID string) (PreparationInterpreter, error) {
	pb, err := c.playbook(playbookID)
	if err != nil {
		return nil, err
	}
	pi, ok := pb.(PreparationInterpreter)
	if !ok {
		return nil, apperr.New(apperr.UnsupportedPlaybook, "この用途は自由文の解釈に対応していません。")
	}
	return pi, nil
}

// interpretError は解釈の失敗を利用者向けのエラーに直す。原文は混ぜない。
func (c *Coordinator) interpretError(ierr error, sessionID string) error {
	switch {
	case ierr == nil:
		return nil
	case errors.Is(ierr, ErrInterpretUnsupported):
		return apperr.New(apperr.UnsupportedPlaybook, "この用途は自由文の解釈に対応していません。")
	case errors.Is(ierr, ErrBudgetExceeded):
		return apperr.InvalidStateErr("AIの呼び出し回数の上限に達したため、解釈は使えません。フォームから入力してください。")
	case errors.Is(ierr, ErrTransient):
		// 通信・タイムアウト・429・5xx のエラーには原文が含まれないので、詳細を残す
		c.log.Warn("自由文の解釈でAIが応答しませんでした", "session_id", sessionID, "err", ierr)
		return apperr.New(apperr.TemporarilyUnavailable, "AIが応答しませんでした。フォームから入力するか、しばらくしてからやり直してください。")
	case errors.Is(ierr, ErrInvalidOutput):
		// 出力には原文の一部が含まれうるので、詳細は出さない
		c.log.Warn("自由文の解釈でAIの出力が形式を満たしませんでした", "session_id", sessionID)
		return apperr.New(apperr.TemporarilyUnavailable, "発言から項目を取り出せませんでした。フォームから入力してください。")
	default:
		// 原文が混ざらないよう、詳細は返さずログにも出さない。
		c.log.Error("自由文の解釈に失敗", "session_id", sessionID)
		return apperr.New(apperr.Internal, "解釈に失敗しました。")
	}
}

// checkInterpretation は取り出した値をサーバーの規則で検証し、正規化した値と未確定項目を返す。
// partial が false なら保存できる形（全体検証）を求め、true なら未確定を許す。
// JSON Schema だけに頼らず、必須の項目名・列挙値・重複をここで確かめる。
func (c *Coordinator) checkInterpretation(ctx context.Context, pi PreparationInterpreter, s Snapshot, in Interpretation, partial bool) (json.RawMessage, []string, error) {
	v := &ValidationError{}
	if in.OutOfScope != "" {
		if !validOutOfScope(in.OutOfScope) {
			v.Add("out_of_scope", "扱えない依頼の分類は %s のいずれかにしてください", strings.Join(OutOfScopeKinds, "・"))
			return nil, nil, v.Err()
		}
		// 扱えない依頼。このターンは値を変えないので中身は検証しない。
		return nil, nil, nil
	}
	if in.Attendance != AttendanceAttending && in.Attendance != AttendanceAbsent {
		v.Add("attendance", "attending または absent を指定してください")
	}
	known := map[string]bool{SlotAttendance: true}
	for _, name := range pi.PreparationSlots() {
		known[name] = true
	}
	seen := map[string]bool{}
	for _, u := range in.Unclear {
		switch {
		case !known[u]:
			v.Add("unclear", "%q は項目名ではありません", clip(u))
		case seen[u]:
			v.Add("unclear", "%q が重複しています", clip(u))
		}
		seen[u] = true
	}
	if in.NeedsFollowup != (len(in.Unclear) > 0) {
		v.Add("needs_followup", "unclear が空でないときだけ true にしてください")
	}
	if err := v.Err(); err != nil {
		return nil, nil, err
	}
	if partial {
		return pi.ValidatePartialPreparation(ctx, s, in.Attendance, in.Data, in.Unclear)
	}
	data, err := pi.ValidatePreparation(ctx, s, in.Attendance, in.Data)
	if err != nil {
		return nil, nil, err
	}
	return data, nonNilStrings(in.Unclear), nil
}

// clip は検証の指摘に載せる文字列を短く切る。原文の断片を長く持ち回らない。
func clip(s string) string {
	const max = 32
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
