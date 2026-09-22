package coord

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// 通知のボタンから直接始める操作（.agent/kasa/decisions/discord-availability-home-refresh.md）。
// 通知に添えるボタンは opaque な参照（NotifyAction の ID）だけを持ち、値や識別子を custom_id に
// 埋め込まない。押下時に Coordinator が操作者・所属・対象・期限・許可された decision を再検証する。

// 通知アクションの種別。
const (
	NotifyActionTaskResponse     = "task_response"
	NotifyActionPreparationStart = "preparation_start"
	NotifyActionBookAssignment   = "book_assignment"
	NotifyActionBookConfirmation = "book_confirmation"
	NotifyActionWeeklyPrompt     = "weekly_prompt"
)

// notifyActionTTL は本人宛て操作の既定の有効期限。個別の期限（タスクの回答期限など）があれば
// そちらを使う。
const notifyActionTTL = 90 * 24 * time.Hour

type taskResponseRef struct {
	TaskID          string  `json:"task_id"`
	ProposalID      *string `json:"proposal_id,omitempty"`
	ProposalVersion *int    `json:"proposal_version,omitempty"`
}

type bookAssignmentRef struct {
	GroupID string `json:"group_id"`
	BookID  string `json:"book_id"`
	SlotID  string `json:"slot_id,omitempty"`
}

type bookConfirmationRef struct {
	GroupID string `json:"group_id"`
	BookID  string `json:"book_id"`
	SlotID  string `json:"slot_id"`
}

type preparationStartRef struct {
	SessionID string `json:"session_id"`
}

// ActionButton は通知に添えるボタン1個。Decision は押下時にサーバーへそのまま渡す値、
// Label は表示名。
type ActionButton struct {
	ActionID string
	Decision string
	Label    string
	Primary  bool
}

// createNotifyAction は通知の作成と同じトランザクションで操作の参照を登録する。
func createNotifyAction(ctx context.Context, tx *store.Tx, userID, kind string, ref any, decisions []string, expiresAt, now time.Time) (string, error) {
	rawRef, err := json.Marshal(ref)
	if err != nil {
		return "", err
	}
	rawDecisions, err := json.Marshal(decisions)
	if err != nil {
		return "", err
	}
	id := store.NewID("nact")
	if err := tx.InsertNotifyAction(ctx, store.NotifyAction{
		ID: id, UserID: userID, Kind: kind, Ref: rawRef, Decisions: rawDecisions, ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return "", err
	}
	return id, nil
}

// taskNotifyButtons は本人宛てタスクの通知に添えるボタンを、同じトランザクションで作る。
// 「preparation」は decision 式ではなく、押下で対象を束縛した対話をそのまま始めるボタンにする。
func (c *Coordinator) taskNotifyButtons(ctx context.Context, tx *store.Tx, userID, sessionID, taskID, kind string, p *store.Proposal, expiresAt, now time.Time) ([]ActionButton, error) {
	if userID == "" {
		// 本人が未ログインで user_id を持たない招待中のメンバーには、束縛できるアクションがない。
		return nil, nil
	}
	if kind == store.TaskPreparation {
		id, err := createNotifyAction(ctx, tx, userID, NotifyActionPreparationStart, preparationStartRef{SessionID: sessionID}, []string{"start"}, expiresAt, now)
		if err != nil {
			return nil, err
		}
		return []ActionButton{{ActionID: id, Decision: "start", Label: "参加条件を回答する", Primary: true}}, nil
	}
	var decisions []struct {
		value, label string
		primary      bool
	}
	switch kind {
	case store.TaskAssignment:
		decisions = []struct {
			value, label string
			primary      bool
		}{{"accept", "担当を引き受ける", true}, {"decline", "担当を辞退する", false}}
	case store.TaskApproval:
		decisions = []struct {
			value, label string
			primary      bool
		}{{"approve", "同意する", true}, {"reject", "同意できない", false}}
	case store.TaskOwnerApproval:
		decisions = []struct {
			value, label string
			primary      bool
		}{{"approve", "管理者として承認する", true}, {"reject", "承認しない", false}}
	default:
		return nil, nil
	}
	ref := taskResponseRef{TaskID: taskID}
	if p != nil {
		ref.ProposalID, ref.ProposalVersion = &p.ID, &p.Version
	}
	values := make([]string, len(decisions))
	for i, d := range decisions {
		values[i] = d.value
	}
	id, err := createNotifyAction(ctx, tx, userID, NotifyActionTaskResponse, ref, values, expiresAt, now)
	if err != nil {
		return nil, err
	}
	out := make([]ActionButton, len(decisions))
	for i, d := range decisions {
		out[i] = ActionButton{ActionID: id, Decision: d.value, Label: d.label, Primary: d.primary}
	}
	return out, nil
}

// bookAssignmentButtons はブック計画・担当変更候補の通知に添える引き受け／辞退のボタンを作る。
// slotID を空にすると、RespondBookAssignment の既存の意味どおり「本人の未回答の初期担当すべて」が対象になる。
func (c *Coordinator) bookAssignmentButtons(ctx context.Context, tx *store.Tx, userID, groupID, bookID, slotID string, now time.Time) ([]ActionButton, error) {
	if userID == "" {
		return nil, nil
	}
	ref := bookAssignmentRef{GroupID: groupID, BookID: bookID, SlotID: slotID}
	id, err := createNotifyAction(ctx, tx, userID, NotifyActionBookAssignment, ref, []string{DecisionAccept, DecisionRequestChange}, now.Add(notifyActionTTL), now)
	if err != nil {
		return nil, err
	}
	return []ActionButton{
		{ActionID: id, Decision: DecisionAccept, Label: "担当を引き受ける", Primary: true},
		{ActionID: id, Decision: DecisionRequestChange, Label: "担当の変更を希望する"},
	}, nil
}

// bookConfirmationButtons は開催3日前の担当確認に添える確認／変更希望のボタンを作る。
func (c *Coordinator) bookConfirmationButtons(ctx context.Context, tx *store.Tx, userID, groupID, bookID, slotID string, expiresAt, now time.Time) ([]ActionButton, error) {
	if userID == "" {
		return nil, nil
	}
	ref := bookConfirmationRef{GroupID: groupID, BookID: bookID, SlotID: slotID}
	id, err := createNotifyAction(ctx, tx, userID, NotifyActionBookConfirmation, ref, []string{DecisionConfirm, DecisionRequestChange}, expiresAt, now)
	if err != nil {
		return nil, err
	}
	return []ActionButton{
		{ActionID: id, Decision: DecisionConfirm, Label: "担当できます", Primary: true},
		{ActionID: id, Decision: DecisionRequestChange, Label: "担当の変更を希望する"},
	}, nil
}

// weeklyPromptButton は普段の空き時間の登録・再確認の依頼に添える開始ボタンを作る。
// 押下は操作者を確かめ直すだけで、以降の入力は Discord の会話として進む（保存はしない）。
func (c *Coordinator) weeklyPromptButton(ctx context.Context, tx *store.Tx, userID string, now time.Time) (ActionButton, error) {
	if userID == "" {
		return ActionButton{}, nil
	}
	id, err := createNotifyAction(ctx, tx, userID, NotifyActionWeeklyPrompt, struct{}{}, []string{"start"}, now.Add(notifyActionTTL), now)
	if err != nil {
		return ActionButton{}, err
	}
	return ActionButton{ActionID: id, Decision: "start", Label: "普段の空き時間を入力する", Primary: true}, nil
}

// NotifyActionOutcome は通知アクションの解決結果。Discord 層はこれを見て次の表示・会話を決める。
type NotifyActionOutcome struct {
	Kind string
	// SessionID は preparation_start のとき、束縛する対象の開催回。
	SessionID string
}

// ResolveNotifyAction はボタン押下を再検証し、束縛先の通常の Coordinator 操作へ接続する。
// 操作者・所属・対象・期限・許可された decision は常にここで再確認し、通知の本文・custom_id の値は
// 認可根拠にしない。冪等性は下流の操作が持つ idem キーに委ねるため、二重押下・再送でも安全。
func (c *Coordinator) ResolveNotifyAction(ctx context.Context, userID, actionID, decision string, idem *store.IdemKey) (NotifyActionOutcome, error) {
	var a store.NotifyAction
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		a, err = tx.NotifyAction(ctx, actionID)
		return err
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return NotifyActionOutcome{}, apperr.NotFoundErr()
	case err != nil:
		return NotifyActionOutcome{}, err
	}
	// 本人以外には存在も含めて知らせない。
	if a.UserID != userID {
		return NotifyActionOutcome{}, apperr.NotFoundErr()
	}
	if a.ConsumedAt != nil {
		return NotifyActionOutcome{}, apperr.InvalidStateErr("この回答はすでに処理済みです。")
	}
	if !c.now().Before(a.ExpiresAt) {
		return NotifyActionOutcome{}, apperr.InvalidStateErr("この操作は期限切れです。Webから最新の内容を確認してください。")
	}
	var decisions []string
	_ = json.Unmarshal(a.Decisions, &decisions)
	if !containsString(decisions, decision) {
		return NotifyActionOutcome{}, apperr.InvalidStateErr("この操作は選べません。")
	}

	var dispatchErr error
	sessionID := ""
	switch a.Kind {
	case NotifyActionPreparationStart:
		var ref preparationStartRef
		if err := json.Unmarshal(a.Ref, &ref); err != nil {
			return NotifyActionOutcome{}, err
		}
		sessionID = ref.SessionID
		dispatchErr = c.CheckSessionAccess(ctx, userID, ref.SessionID)
	case NotifyActionTaskResponse:
		var ref taskResponseRef
		if err := json.Unmarshal(a.Ref, &ref); err != nil {
			return NotifyActionOutcome{}, err
		}
		_, dispatchErr = c.RespondTask(ctx, userID, ref.TaskID, apitypes.TaskResponseInput{
			Decision: decision, ProposalID: ref.ProposalID, ProposalVersion: ref.ProposalVersion,
		}, idem)
	case NotifyActionBookAssignment:
		var ref bookAssignmentRef
		if err := json.Unmarshal(a.Ref, &ref); err != nil {
			return NotifyActionOutcome{}, err
		}
		_, dispatchErr = c.RespondBookAssignment(ctx, userID, ref.GroupID, ref.BookID,
			apitypes.BookAssignmentInput{Decision: decision, SlotID: ref.SlotID}, idem)
	case NotifyActionBookConfirmation:
		var ref bookConfirmationRef
		if err := json.Unmarshal(a.Ref, &ref); err != nil {
			return NotifyActionOutcome{}, err
		}
		_, dispatchErr = c.RespondAssigneeConfirmation(ctx, userID, ref.GroupID, ref.BookID, ref.SlotID,
			apitypes.AssigneeConfirmationInput{Decision: decision}, idem)
	case NotifyActionWeeklyPrompt:
		// バックエンドの状態は変えない。Discord 層が続けて対話を始めるだけ。
	default:
		return NotifyActionOutcome{}, apperr.New(apperr.Internal, "未知の操作です。")
	}
	if dispatchErr != nil {
		return NotifyActionOutcome{}, dispatchErr
	}
	// 消費の記録はベストエフォート。正しさは下流操作の冪等キーが担保する。
	_ = c.st.Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.ConsumeNotifyAction(ctx, actionID, c.now())
		return err
	})
	return NotifyActionOutcome{Kind: a.Kind, SessionID: sessionID}, nil
}
