package coord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
)

// ErrInterpretUnsupported は用途が自由文の解釈に対応していないこと。
var ErrInterpretUnsupported = errors.New("coord: interpretation is not supported")

// Interpretation は自由文から取り出した参加条件の下書き。保存はしない。
type Interpretation struct {
	Attendance string          `json:"attendance"`
	Data       json.RawMessage `json:"data"`
	// Unclear は発言から読み取れなかった項目名。推測した値を入れない。
	Unclear []string `json:"unclear"`
	// NeedsFollowup は本人に聞き直すべき項目が残っていること。
	NeedsFollowup bool `json:"needs_followup"`
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
	// InterpretInstructions は抽出時の用途ごとの判断指示。
	InterpretInstructions() string
	// InterpretContext は抽出に必要な最小限の判断材料（節一覧など）を返す。
	InterpretContext(ctx context.Context, s Snapshot) (json.RawMessage, error)
}

// DraftInterpreter は LLM を使わず用途の規則だけで抽出する。AGENT_MODE=fake で使う。
type DraftInterpreter interface {
	DraftInterpret(ctx context.Context, s Snapshot, text string) (Interpretation, error)
}

// DraftOnlyInterpreter は LLM を呼ばず、用途の規則（DraftInterpreter）だけで抽出する。
type DraftOnlyInterpreter struct{}

var _ Interpreter = DraftOnlyInterpreter{}

func (DraftOnlyInterpreter) Interpret(ctx context.Context, req InterpretRequest) (Interpretation, Usage, error) {
	di, ok := req.Playbook.(DraftInterpreter)
	if !ok {
		return Interpretation{}, Usage{}, ErrInterpretUnsupported
	}
	in, err := di.DraftInterpret(ctx, req.Snapshot, req.Text)
	if err != nil {
		return Interpretation{}, Usage{}, err
	}
	if err := req.Check(ctx, in); err != nil {
		return Interpretation{}, Usage{}, ErrInvalidOutput
	}
	return in, Usage{}, nil
}

// InterpretPreparation は本人の自由文から参加条件の下書きを作って返す。保存はしない。
// 対象者は常に呼び出した本人で、入力からメンバーIDを受け取らない。
func (c *Coordinator) InterpretPreparation(ctx context.Context, userID, sessionID string, in apitypes.InterpretPreparationInput) (apitypes.PreparationInterpretation, error) {
	var out apitypes.PreparationInterpretation
	text := strings.TrimSpace(in.Text)
	switch {
	case text == "":
		return out, apperr.Validation(apperr.Field{Path: "text", Message: "必須です"})
	case utf8.RuneCountInString(text) > MaxInterpretTextLen:
		return out, apperr.Validation(apperr.Field{Path: "text", Message: "長すぎます"})
	}
	if c.interpreter == nil {
		return out, apperr.New(apperr.UnsupportedPlaybook, "自由文の解釈は利用できません。")
	}

	var (
		snap   Snapshot
		pi     PreparationInterpreter
		caseID string
	)
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		sess, _, err := c.access(ctx, tx, userID, sessionID)
		if err != nil {
			return err
		}
		if err := checkNotStarted(sess, c.now()); err != nil {
			return err
		}
		pb, err := c.playbook(sess.PlaybookID)
		if err != nil {
			return err
		}
		var ok bool
		if pi, ok = pb.(PreparationInterpreter); !ok {
			return apperr.New(apperr.UnsupportedPlaybook, "この用途は自由文の解釈に対応していません。")
		}
		cs, err := tx.LatestCase(ctx, sess.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil {
			caseID = cs.ID
			used, err := tx.CountLLMCallsByCase(ctx, cs.ID)
			if err != nil {
				return err
			}
			if used+MaxLLMCallsPerInterpretation > MaxLLMCallsPerCase {
				return apperr.InvalidStateErr("AIの呼び出し回数の上限に達したため、解釈は使えません。フォームから入力してください。")
			}
		}
		snap, err = c.snapshot(ctx, tx, sess, nil)
		return err
	})
	if err != nil {
		return out, err
	}

	// LLM 呼び出しはトランザクションの外で行う。結果は保存せず、本人の確認後に通常の送信で保存される。
	res, usage, ierr := c.interpreter.Interpret(ctx, InterpretRequest{
		Snapshot: snap, Playbook: pi, Text: text, MaxLLMCalls: MaxLLMCallsPerInterpretation,
		Check: func(ctx context.Context, got Interpretation) error {
			_, err := c.checkInterpretation(ctx, pi, snap, got)
			return err
		},
	})
	if len(usage.LLMCalls) > 0 && caseID != "" {
		now := c.now()
		if err := c.st.Tx(ctx, func(tx *store.Tx) error {
			for _, call := range usage.LLMCalls {
				if err := tx.AddLLMCall(ctx, store.LLMCall{
					CaseID: caseID, Model: call.Model, InputTokens: call.InputTokens, OutputTokens: call.OutputTokens, Currency: call.Currency,
					EstimatedAmount: call.EstimatedAmount, BilledAmount: call.BilledAmount, Succeeded: call.Succeeded, CreatedAt: now,
				}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return out, err
		}
	}
	switch {
	case errors.Is(ierr, ErrInterpretUnsupported):
		return out, apperr.New(apperr.UnsupportedPlaybook, "この用途は自由文の解釈に対応していません。")
	case errors.Is(ierr, ErrTransient), errors.Is(ierr, ErrBudgetExceeded):
		return out, apperr.New(apperr.TemporarilyUnavailable, "AIが応答しませんでした。フォームから入力するか、しばらくしてからやり直してください。")
	case errors.Is(ierr, ErrInvalidOutput):
		return out, apperr.New(apperr.TemporarilyUnavailable, "発言から項目を取り出せませんでした。フォームから入力してください。")
	case ierr != nil:
		// 原文が混ざらないよう、詳細は返さずログにも出さない。
		c.log.Error("自由文の解釈に失敗", "session_id", sessionID)
		return out, apperr.New(apperr.Internal, "解釈に失敗しました。")
	}

	// 返す値はサーバーが正規化したもの。AI の出力をそのまま返さない。
	data, err := c.checkInterpretation(ctx, pi, snap, res)
	if err != nil {
		return out, apperr.New(apperr.TemporarilyUnavailable, "発言から項目を取り出せませんでした。フォームから入力してください。")
	}
	return apitypes.PreparationInterpretation{
		Preparation:   apitypes.Preparation{Attendance: res.Attendance, Data: data},
		Unclear:       nonNilStrings(res.Unclear),
		NeedsFollowup: res.NeedsFollowup || len(res.Unclear) > 0,
		Saved:         false,
	}, nil
}

// checkInterpretation は取り出した値を用途の規則で検証し、正規化した値を返す。
// 下書きは常に「そのまま保存できる形」であることを求める。
func (c *Coordinator) checkInterpretation(ctx context.Context, pb Playbook, s Snapshot, in Interpretation) (json.RawMessage, error) {
	if in.Attendance != AttendanceAttending && in.Attendance != AttendanceAbsent {
		v := &ValidationError{}
		v.Add("attendance", "attending または absent を指定してください")
		return nil, v
	}
	for _, u := range in.Unclear {
		if utf8.RuneCountInString(u) > 64 {
			v := &ValidationError{}
			v.Add("unclear", "項目名が長すぎます")
			return nil, v
		}
	}
	return pb.ValidatePreparation(ctx, s, in.Attendance, in.Data)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
