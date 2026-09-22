package coord

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// weeklyDayJA は ISO 曜日番号（1=月曜〜7=日曜）に対応する日本語の1文字表記。
var weeklyDayJA = [...]string{"", "月", "火", "水", "木", "金", "土", "日"}

// parseWeeklyText は AGENT_MODE=fake 用の決まった書き方だけを読む。
// 例："水,金 19:00-22:00"（曜日はカンマ区切り、時間帯は1つ）。任意の自然文は unclear のままにする。
func parseWeeklyText(text string) ([]apitypes.WeeklyWindow, bool) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) != 2 {
		return nil, false
	}
	span := strings.Split(parts[1], "-")
	if len(span) != 2 {
		return nil, false
	}
	days := strings.Split(parts[0], ",")
	var out []apitypes.WeeklyWindow
	for _, d := range days {
		d = strings.TrimSuffix(strings.TrimSuffix(d, "曜日"), "曜")
		found := false
		for wd, label := range weeklyDayJA {
			if wd == 0 {
				continue
			}
			if d == label {
				out = append(out, apitypes.WeeklyWindow{Weekday: wd, Start: span[0], End: span[1]})
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// 普段の空き時間の自由文解釈（.agent/kasa/decisions/discord-availability-home-refresh.md）。
// LLM にできるのは決められた項目（曜日・時間帯）の値を決めることだけで、保存・同意は行わない。
// 対象は常に呼び出した本人で、入力からメンバーIDを受け取らない。原文はこの解釈にだけ渡し、
// アプリDB・実行ログへは保存しない。Web・Discord 共通で使う。
const (
	// MaxLLMCallsPerWeeklyInterpretation は1回の解釈で使える LLM 呼び出し回数（初回＋やり直し1回）。
	MaxLLMCallsPerWeeklyInterpretation = 2
	// MaxWeeklyInterpretCallsPerUser は本人あたり、普段の空き時間の解釈に使える LLM 呼び出しの総数（累計）。
	// Web と Discord で共通の枠とし、呼び出しの直前に1回分ずつ確保する。
	MaxWeeklyInterpretCallsPerUser = 24
)

// WeeklyInterpretation は自由文から取り出した普段の空き時間の下書き。保存はしない。
type WeeklyInterpretation struct {
	Windows []apitypes.WeeklyWindow
	// Unclear は読み取れなかった内容の短い説明。値は推測しない。
	Unclear []string
	// NeedsFollowup は本人に確認すべき内容が残っていること。
	NeedsFollowup bool
	// Question は AI が書いた、聞き直しの質問文（対話では未使用。将来の拡張用に保持）。
	Question string
}

// WeeklyInterpretRequest は1回の解釈への入力。
type WeeklyInterpretRequest struct {
	// Text は本人の自由文。保存も転記もしない。
	Text string
	// Check は取り出した値をサーバーの規則で検証する。検証エラーは AI に返してやり直させる。
	Check func(ctx context.Context, in WeeklyInterpretation) error
	// MaxLLMCalls はこの解釈で使える LLM 呼び出し回数。
	MaxLLMCalls int

	// Reserve は LLM を呼ぶ直前に予算枠を確保し、記録IDを返す。
	Reserve func(ctx context.Context, model string) (string, error)
	// Record は呼び出しの結果を確保済みの記録へ書き込む。失敗しても枠は戻さない。
	Record func(ctx context.Context, id string, call LLMCall) error
}

func (r WeeklyInterpretRequest) ReserveCall(ctx context.Context, model string) (string, error) {
	if r.Reserve == nil {
		return "", nil
	}
	return r.Reserve(ctx, model)
}

func (r WeeklyInterpretRequest) RecordCall(ctx context.Context, id string, call LLMCall) {
	if r.Record == nil || id == "" {
		return
	}
	_ = r.Record(ctx, id, call)
}

// WeeklyInterpreter は自由文から普段の空き時間の下書きを取り出す。保存・通知の作成は行わない。
type WeeklyInterpreter interface {
	InterpretWeekly(ctx context.Context, req WeeklyInterpretRequest) (WeeklyInterpretation, Usage, error)
}

// DraftOnlyWeeklyInterpreter は LLM を使わず、決まった書き方だけを読む（AGENT_MODE=fake 用）。
type DraftOnlyWeeklyInterpreter struct{}

var _ WeeklyInterpreter = DraftOnlyWeeklyInterpreter{}

func (DraftOnlyWeeklyInterpreter) InterpretWeekly(ctx context.Context, req WeeklyInterpretRequest) (WeeklyInterpretation, Usage, error) {
	windows, ok := parseWeeklyText(req.Text)
	out := WeeklyInterpretation{Windows: windows}
	if !ok {
		out.Unclear = []string{"windows"}
		out.NeedsFollowup = true
	}
	if err := req.Check(ctx, out); err != nil {
		return WeeklyInterpretation{}, Usage{}, ErrInvalidOutput
	}
	return out, Usage{}, nil
}

// weeklyInterpretLookupID は本人あたりの解釈呼び出しを数えるための集計キー。
func weeklyInterpretLookupID(userID string) string { return "weekly:" + userID }

// reserveWeeklyInterpretCall は LLM 呼び出し1回分の枠を確保し、記録の行を先に作る。
func (c *Coordinator) reserveWeeklyInterpretCall(ctx context.Context, userID, model string) (string, error) {
	lookupID := weeklyInterpretLookupID(userID)
	var id string
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		used, err := tx.CountLLMCallsByLookup(ctx, lookupID)
		if err != nil {
			return err
		}
		if used+1 > MaxWeeklyInterpretCallsPerUser {
			return ErrBudgetExceeded
		}
		id = store.NewID("llm")
		return tx.AddLLMCall(ctx, store.LLMCall{
			ID: id, LookupID: lookupID, Model: model, Currency: "unknown", CreatedAt: c.now(),
		})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (c *Coordinator) recordWeeklyInterpretCall(ctx context.Context, id string, call LLMCall) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		return tx.UpdateLLMCall(ctx, store.LLMCall{
			ID: id, Model: call.Model, InputTokens: call.InputTokens, OutputTokens: call.OutputTokens,
			Currency: call.Currency, EstimatedAmount: call.EstimatedAmount, BilledAmount: call.BilledAmount, Succeeded: call.Succeeded,
		})
	})
	if err != nil {
		c.log.Warn("週間空き時間の解釈の記録に失敗", "llm_call_id", id)
	}
	return err
}

func (c *Coordinator) weeklyBudgetHooks(req *WeeklyInterpretRequest, userID string) {
	req.Reserve = func(ctx context.Context, model string) (string, error) {
		return c.reserveWeeklyInterpretCall(ctx, userID, model)
	}
	req.Record = func(ctx context.Context, id string, call LLMCall) error {
		return c.recordWeeklyInterpretCall(ctx, id, call)
	}
}

// InterpretWeeklyAvailability は本人の自由文から普段の空き時間の下書きを作って返す。保存はしない。
func (c *Coordinator) InterpretWeeklyAvailability(ctx context.Context, userID string, in apitypes.WeeklyAvailabilityInterpretationInput) (apitypes.WeeklyAvailabilityInterpretation, error) {
	var out apitypes.WeeklyAvailabilityInterpretation
	text, err := checkInterpretText(in.Text)
	if err != nil {
		return out, err
	}
	if err := c.st.Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.User(ctx, userID)
		return err
	}); err != nil {
		return out, err
	}

	req := WeeklyInterpretRequest{
		Text: text, MaxLLMCalls: MaxLLMCallsPerWeeklyInterpretation,
		Check: func(ctx context.Context, got WeeklyInterpretation) error {
			_, _, err := c.checkWeeklyInterpretation(got)
			return err
		},
	}
	c.weeklyBudgetHooks(&req, userID)
	res, _, ierr := c.weeklyInterpreter.InterpretWeekly(ctx, req)
	if err := c.weeklyInterpretError(ierr); err != nil {
		return out, err
	}
	windows, unclear, err := c.checkWeeklyInterpretation(res)
	if err != nil {
		return out, apperr.New(apperr.TemporarilyUnavailable, "発言から週間の空き時間を取り出せませんでした。フォームから入力してください。")
	}
	return apitypes.WeeklyAvailabilityInterpretation{
		Availability:  apitypes.WeeklyAvailability{Timezone: DefaultAvailabilityZone, Windows: windows},
		Unclear:       unclear,
		NeedsFollowup: res.NeedsFollowup || len(unclear) > 0,
		Saved:         false,
	}, nil
}

// checkWeeklyInterpretation はサーバーの規則で検証し、正規化した区間と未確定の説明を返す。
// JSON Schema だけに頼らず、曜日・時刻の形式・重複時間帯をここで確かめる。
func (c *Coordinator) checkWeeklyInterpretation(in WeeklyInterpretation) ([]apitypes.WeeklyWindow, []string, error) {
	var fields []apperr.Field
	if len(in.Unclear) > maxWeeklyUnclear {
		fields = append(fields, apperr.Field{Path: "unclear", Message: "項目が多すぎます"})
	}
	for _, u := range in.Unclear {
		if utf8RuneCount(u) > maxWeeklyUnclearLen {
			fields = append(fields, apperr.Field{Path: "unclear", Message: "説明が長すぎます"})
			break
		}
	}
	windows, err := validateWeeklyWindows(nonNilWindows(in.Windows))
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			if fs, ok := ae.Details["fields"].([]apperr.Field); ok {
				fields = append(fields, fs...)
			}
		} else {
			return nil, nil, err
		}
	}
	if len(fields) > 0 {
		return nil, nil, apperr.Validation(fields...)
	}
	return windows, nonNilStrings(in.Unclear), nil
}

const (
	maxWeeklyUnclear    = 10
	maxWeeklyUnclearLen = 120
)

func nonNilWindows(w []apitypes.WeeklyWindow) []apitypes.WeeklyWindow {
	if w == nil {
		return []apitypes.WeeklyWindow{}
	}
	return w
}

func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// weeklyInterpretError は解釈の失敗を利用者向けのエラーに直す。原文は混ぜない。
func (c *Coordinator) weeklyInterpretError(ierr error) error {
	switch {
	case ierr == nil:
		return nil
	case errors.Is(ierr, ErrBudgetExceeded):
		return apperr.InvalidStateErr("AIの呼び出し回数の上限に達したため、解釈は使えません。フォームから入力してください。")
	case errors.Is(ierr, ErrTransient):
		c.log.Warn("週間空き時間の解釈でAIが応答しませんでした", "err", ierr)
		return apperr.New(apperr.TemporarilyUnavailable, "AIが応答しませんでした。フォームから入力するか、しばらくしてからやり直してください。")
	case errors.Is(ierr, ErrInvalidOutput):
		c.log.Warn("週間空き時間の解釈でAIの出力が形式を満たしませんでした")
		return apperr.New(apperr.TemporarilyUnavailable, "発言から週間の空き時間を取り出せませんでした。フォームから入力してください。")
	default:
		c.log.Error("週間空き時間の解釈に失敗")
		return apperr.New(apperr.Internal, "解釈に失敗しました。")
	}
}
