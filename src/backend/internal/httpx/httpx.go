// Package httpx は API 層と用途別の HTTP 拡張が共有する入出力の規約（docs/api-endpoint.md）。
// エラーは常に {"error": {code, message, details, request_id}} の形で返す。
package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// MaxJSONBody は JSON 本文の上限。
const MaxJSONBody = 64 << 10

var statusByCode = map[string]int{
	apperr.InvalidJSON:            http.StatusBadRequest,
	apperr.IdempotencyKeyRequired: http.StatusBadRequest,
	apperr.Unauthenticated:        http.StatusUnauthorized,
	apperr.Forbidden:              http.StatusForbidden,
	apperr.CSRFInvalid:            http.StatusForbidden,
	apperr.NotFound:               http.StatusNotFound,
	apperr.MethodNotAllowed:       http.StatusMethodNotAllowed,
	apperr.RevisionConflict:       http.StatusConflict,
	apperr.ProposalSuperseded:     http.StatusConflict,
	apperr.TaskClosed:             http.StatusConflict,
	apperr.TaskExpired:            http.StatusConflict,
	apperr.InvalidState:           http.StatusConflict,
	apperr.MembersNotJoined:       http.StatusConflict,
	apperr.IdempotencyKeyReused:   http.StatusConflict,
	apperr.PayloadTooLarge:        http.StatusRequestEntityTooLarge,
	apperr.UnsupportedMediaType:   http.StatusUnsupportedMediaType,
	apperr.ValidationFailed:       http.StatusUnprocessableEntity,
	apperr.UnsupportedPlaybook:    http.StatusUnprocessableEntity,
	apperr.Internal:               http.StatusInternalServerError,
	apperr.TemporarilyUnavailable: http.StatusServiceUnavailable,
}

type ctxKey int

const requestIDKey ctxKey = 1

// WithRequestID はリクエストIDを context に入れる。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteResponse は保存済みの成功応答（再送時も同じ内容）を返す。
func WriteResponse(w http.ResponseWriter, res store.Response) {
	if res.Location != "" {
		w.Header().Set("Location", res.Location)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

// ToAppError は内部のエラーを公開用のエラーに変換する。変換できなければ nil。
func ToAppError(err error) *apperr.Error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	var ve *coord.ValidationError
	if errors.As(err, &ve) {
		fields := []apperr.Field{}
		for _, f := range ve.Fields {
			fields = append(fields, apperr.Field{Path: f.Path, Message: f.Message})
		}
		return apperr.Validation(fields...)
	}
	if errors.Is(err, store.ErrIdempotencyKeyReused) {
		return apperr.New(apperr.IdempotencyKeyReused, "このキーは別の操作に使われています。新しいキーで送信してください。")
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperr.New(apperr.PayloadTooLarge, "入力が大きすぎます。")
	}
	return nil
}

// WriteError はエラーを共通の形で返す。想定外のエラーは内容を返さずログに残す。
func WriteError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	ae := ToAppError(err)
	if ae == nil {
		log.Error("内部エラー", "request_id", RequestID(r.Context()), "method", r.Method, "path", r.URL.Path, "err", err)
		ae = apperr.New(apperr.Internal, "処理に失敗しました。時間をおいて再度お試しください。")
	}
	status, ok := statusByCode[ae.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	details := ae.Details
	if details == nil {
		details = map[string]any{}
	}
	if status == http.StatusMethodNotAllowed {
		if allow, ok := details["allow"].(string); ok {
			w.Header().Set("Allow", allow)
		}
	}
	WriteJSON(w, status, map[string]any{"error": map[string]any{
		"code": ae.Code, "message": ae.Message, "details": details, "request_id": RequestID(r.Context()),
	}})
}

// ReadBody は本文を上限付きで読む。
func ReadBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, apperr.New(apperr.PayloadTooLarge, "入力が大きすぎます。")
		}
		return nil, apperr.New(apperr.InvalidJSON, "本文を読めません。")
	}
	return b, nil
}

// CheckJSONContentType は Content-Type が application/json かを確認する。
func CheckJSONContentType(r *http.Request) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return apperr.New(apperr.UnsupportedMediaType, "Content-Type は application/json にしてください。")
	}
	return nil
}

// DecodeStrict は未知のフィールドを拒否して JSON をデコードする。
func DecodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.New(apperr.InvalidJSON, "JSON の形式が正しくないか、未知の項目があります。").With("reason", err.Error())
	}
	if dec.More() {
		return apperr.New(apperr.InvalidJSON, "JSON の後に余分なデータがあります。")
	}
	return nil
}

// ReadJSON は Content-Type・サイズを確認して本文を厳密にデコードし、生の本文も返す。
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) ([]byte, error) {
	if err := CheckJSONContentType(r); err != nil {
		return nil, err
	}
	raw, err := ReadBody(w, r, MaxJSONBody)
	if err != nil {
		return nil, err
	}
	return raw, DecodeStrict(raw, v)
}

// BodyHash は再送判定用に本文のハッシュを返す。JSON ならキー順・空白を正規化する。
func BodyHash(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			raw = b
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CheckOrigin は変更系のリクエストが同一オリジンから来たかを確認する。
// Origin があれば許可リストかリクエスト先と一致すること、なければ Sec-Fetch-Site が cross-site でないこと。
func CheckOrigin(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	for _, a := range allowed {
		if origin == a {
			return true
		}
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// Request は用途別の HTTP 拡張に渡す、認証・所属確認済みのリクエスト情報。
type Request struct {
	UserID   string
	GroupID  string
	MemberID string
	Role     string
	// Idem は変更系リクエストの再送判定キー（GET では nil）。
	Idem *store.IdemKey
	// Body は上限内で読み込んだ本文（GET では nil）。
	Body []byte
}

// Route は用途固有の API（/api/groups/{group_id}/{playbook_id}/...）の1つ。
type Route struct {
	Method string
	// Pattern は playbook_id より後ろのパス（例：toc-lookups/{lookup_id}）。
	Pattern   string
	OwnerOnly bool
	// MaxBody は本文の上限。0 なら JSON の上限。
	MaxBody int64
	Handler func(w http.ResponseWriter, r *http.Request, req Request)
}

// Extension は用途別の HTTP 拡張。認証・CSRF・Origin・再送キー・所属の確認は API 層が行う。
type Extension interface {
	PlaybookID() string
	Routes() []Route
}
