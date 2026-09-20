package toc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

// Extension は /api/groups/{group_id}/reading/toc-lookups... を提供する。すべて管理者専用。
// 認証・CSRF・Origin・Idempotency-Key・所属の確認は API 層が行う。
type Extension struct {
	svc *Service
}

func NewExtension(svc *Service) *Extension { return &Extension{svc: svc} }

func (e *Extension) PlaybookID() string { return reading.ID }

func (e *Extension) Routes() []httpx.Route {
	return []httpx.Route{
		{Method: http.MethodPost, Pattern: "toc-lookups", OwnerOnly: true, Handler: e.start},
		{Method: http.MethodGet, Pattern: "toc-lookups/{lookup_id}", OwnerOnly: true, Handler: e.get},
		// multipart の区切りの分だけ上限に余裕を持たせる。画像ごとの上限は本文の解析時に確認する。
		{Method: http.MethodPost, Pattern: "toc-lookups/{lookup_id}/images", OwnerOnly: true, MaxBody: MaxTotalBytes + 64<<10, Handler: e.images},
	}
}

func (e *Extension) start(w http.ResponseWriter, r *http.Request, req httpx.Request) {
	if err := httpx.CheckJSONContentType(r); err != nil {
		httpx.WriteError(w, r, e.svc.log, err)
		return
	}
	var in struct {
		ISBN string `json:"isbn"`
	}
	if err := httpx.DecodeStrict(req.Body, &in); err != nil {
		httpx.WriteError(w, r, e.svc.log, err)
		return
	}
	res, err := e.svc.Start(r.Context(), req.GroupID, in.ISBN, req.Idem)
	if err != nil {
		httpx.WriteError(w, r, e.svc.log, err)
		return
	}
	httpx.WriteResponse(w, res)
}

func (e *Extension) get(w http.ResponseWriter, r *http.Request, req httpx.Request) {
	out, err := e.svc.Get(r.Context(), req.GroupID, r.PathValue("lookup_id"))
	if err != nil {
		httpx.WriteError(w, r, e.svc.log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

var allowedImageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

func (e *Extension) images(w http.ResponseWriter, r *http.Request, req httpx.Request) {
	images, hash, err := parseImages(r.Header.Get("Content-Type"), req.Body)
	if err != nil {
		httpx.WriteError(w, r, e.svc.log, err)
		return
	}
	// 再送時は multipart の区切り文字列が変わるため、画像の内容から再送判定のハッシュを作る。
	req.Idem.BodyHash = hash
	res, err := e.svc.SubmitImages(r.Context(), req.GroupID, r.PathValue("lookup_id"), images, req.Idem)
	if err != nil {
		httpx.WriteError(w, r, e.svc.log, err)
		return
	}
	httpx.WriteResponse(w, res)
}

// parseImages は multipart/form-data の images フィールドから1〜5枚の画像を取り出す。
// 形式は本文の先頭バイトから判定し、申告された Content-Type を信用しない。
func parseImages(contentType string, body []byte) ([]Image, string, error) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return nil, "", apperr.New(apperr.UnsupportedMediaType, "multipart/form-data で送信してください。")
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var images []Image
	total := 0
	h := sha256.New()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", apperr.New(apperr.InvalidJSON, "multipart の形式が正しくありません。")
		}
		if part.FormName() != "images" {
			return nil, "", apperr.Validation(apperr.Field{Path: part.FormName(), Message: "images 以外の項目は受け付けません"})
		}
		data, err := io.ReadAll(io.LimitReader(part, MaxImageBytes+1))
		if err != nil {
			return nil, "", apperr.New(apperr.InvalidJSON, "画像を読めません。")
		}
		if len(data) > MaxImageBytes {
			return nil, "", apperr.New(apperr.PayloadTooLarge, "画像は1枚4 MBまでにしてください。")
		}
		total += len(data)
		if total > MaxTotalBytes {
			return nil, "", apperr.New(apperr.PayloadTooLarge, "画像は合計10 MBまでにしてください。")
		}
		ct := http.DetectContentType(data)
		if !allowedImageTypes[ct] {
			return nil, "", apperr.New(apperr.UnsupportedMediaType, "画像は JPEG・PNG・WebP にしてください。")
		}
		sum := sha256.Sum256(data)
		h.Write(sum[:])
		images = append(images, Image{ContentType: ct, Data: data})
		if len(images) > MaxImages {
			return nil, "", apperr.Validation(apperr.Field{Path: "images", Message: "画像は1〜5枚にしてください"})
		}
	}
	if len(images) == 0 {
		return nil, "", apperr.Validation(apperr.Field{Path: "images", Message: "画像は1〜5枚にしてください"})
	}
	return images, hex.EncodeToString(h.Sum(nil)), nil
}
