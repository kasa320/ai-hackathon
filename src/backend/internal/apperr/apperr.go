// Package apperr は業務処理が返すエラーコード。HTTP ステータスへの対応は api 層が行う。
// フロントは message ではなく code で処理を分ける（docs/api-endpoint.md のエラー表）。
package apperr

import "fmt"

// エラーコード（docs/api-endpoint.md のエラー表）。
const (
	InvalidJSON            = "invalid_json"
	IdempotencyKeyRequired = "idempotency_key_required"
	Unauthenticated        = "unauthenticated"
	Forbidden              = "forbidden"
	CSRFInvalid            = "csrf_invalid"
	NotFound               = "not_found"
	MethodNotAllowed       = "method_not_allowed"
	RevisionConflict       = "revision_conflict"
	ProposalSuperseded     = "proposal_superseded"
	TaskClosed             = "task_closed"
	TaskExpired            = "task_expired"
	InvalidState           = "invalid_state"
	MembersNotJoined       = "members_not_joined"
	IdempotencyKeyReused   = "idempotency_key_reused"
	PayloadTooLarge        = "payload_too_large"
	UnsupportedMediaType   = "unsupported_media_type"
	ValidationFailed       = "validation_failed"
	UnsupportedPlaybook    = "unsupported_playbook"
	Internal               = "internal_error"
	TemporarilyUnavailable = "temporarily_unavailable"
)

// Error はコード付きの業務エラー。Details は常に object として返す。
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func New(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Details: map[string]any{}}
}

// With は details に値を追加する。
func (e *Error) With(key string, value any) *Error {
	e.Details[key] = value
	return e
}

func NotFoundErr() *Error {
	return New(NotFound, "対象が見つからないか、アクセスできません。")
}

func ForbiddenErr() *Error {
	return New(Forbidden, "この操作は管理者だけが行えます。")
}

func RevisionConflictErr(current int64) *Error {
	return New(RevisionConflict, "状態が更新されています。最新の内容を確認してください。").With("current_revision", current)
}

func InvalidStateErr(format string, args ...any) *Error {
	return New(InvalidState, format, args...)
}

// Field は validation_failed の1項目。
type Field struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Validation は validation_failed のエラーを作る。
func Validation(fields ...Field) *Error {
	if fields == nil {
		fields = []Field{}
	}
	return New(ValidationFailed, "入力内容を確認してください。").With("fields", fields)
}
