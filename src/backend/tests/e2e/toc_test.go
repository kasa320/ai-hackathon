package e2e

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading/toc"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func multipartImages(t *testing.T, field string, files ...[]byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="p`+string(rune('0'+i))+`.png"`)
		h.Set("Content-Type", "image/png")
		w, _ := mw.CreatePart(h)
		_, _ = w.Write(f)
	}
	_ = mw.Close()
	return mw.FormDataContentType(), buf.Bytes()
}

func (c *client) lookup(groupID, id string) toc.Lookup {
	c.t.Helper()
	var l toc.Lookup
	c.get("/api/groups/"+groupID+"/reading/toc-lookups/"+id).mustStatus(c.t, 200).decode(c.t, &l)
	return l
}

// R7：ISBN → 書誌 → Web 検索 → 取得元との照合 → 候補。候補は開催回登録に使うまで保存しない。
func TestTocLookupFromWeb(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	a := s.client().devLogin("A")
	base := "/api/groups/" + seed.GroupID + "/reading/toc-lookups"

	var l toc.Lookup
	a.send(http.MethodPost, base, map[string]string{"isbn": tocISBN}).mustStatus(t, http.StatusAccepted).decode(t, &l)
	if l.Status != "resolving_book" || len(l.Entries) != 0 || l.ExpiresAt.Sub(t0).Hours() != 24 {
		t.Fatalf("開始直後: %s", dump(l))
	}
	s.process()
	l = a.lookup(seed.GroupID, l.ID)
	if l.Status != "succeeded" || str(l.Source) != "web" || len(l.SourceURLs) != 1 || len(l.Entries) != 3 || l.Book == nil || l.Book.Title != "サンプル技術書" {
		t.Fatalf("Web からの候補: %s", dump(l))
	}
	// 管理者が確認した候補から節を作り、取得元を記録して開催回を登録できる。
	data := `{"book_title":"サンプル技術書","isbn":"` + tocISBN + `","toc_source":{"kind":"web","urls":["` + l.SourceURLs[0] + `"]},
	  "sections":[{"id":"s1","title":"` + l.Entries[0].Title + `"},{"id":"s2","title":"` + l.Entries[2].Title + `"}],
	  "completed_section_ids":["s1"],"target_section_ids":["s2"]}`
	a.send(http.MethodPost, "/api/groups/"+seed.GroupID+"/sessions",
		`{"playbook_id":"reading","title":"第3回","starts_at":"2026-09-26T02:00:00Z","duration_minutes":60,"data":`+data+`}`).mustStatus(t, http.StatusCreated)

	// 画像の提出は needs_image のときだけ。
	ct, body := multipartImages(t, "images", pngBytes)
	r := a.do(http.MethodPost, base+"/"+l.ID+"/images", body, map[string]string{"Content-Type": ct, "X-CSRF-Token": a.csrf, "Idempotency-Key": newKey()})
	if r.status != 409 || r.errorCode(t) != "invalid_state" {
		t.Fatalf("needs_image 以外の画像提出: %d %s", r.status, r.body)
	}
}

// 書誌が見つからない → 画像の提出を依頼 → 画像から書き写し（読めない箇所の数付き）。
func TestTocLookupFromImage(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	a := s.client().devLogin("A")
	base := "/api/groups/" + seed.GroupID + "/reading/toc-lookups"
	var l toc.Lookup
	a.send(http.MethodPost, base, map[string]string{"isbn": "9784873115658"}).mustStatus(t, 202).decode(t, &l)
	s.process()
	l = a.lookup(seed.GroupID, l.ID)
	if l.Status != "needs_image" || str(l.ReasonCode) != "book_not_found" {
		t.Fatalf("書誌なし: %s", dump(l))
	}

	upload := func(ct string, body []byte, key string) response {
		return a.do(http.MethodPost, base+"/"+l.ID+"/images", body, map[string]string{"Content-Type": ct, "X-CSRF-Token": a.csrf, "Idempotency-Key": key})
	}
	// 形式違い・キーなし・フィールド違いは拒否する。
	ct, body := multipartImages(t, "images", []byte("not an image"))
	if r := upload(ct, body, newKey()); r.status != 415 {
		t.Fatalf("画像以外: %d", r.status)
	}
	ct, body = multipartImages(t, "images", pngBytes)
	if r := upload(ct, body, ""); r.status != 400 || r.errorCode(t) != "idempotency_key_required" {
		t.Fatalf("キーなし: %d", r.status)
	}
	if r := upload("application/json", []byte(`{}`), newKey()); r.status != 415 {
		t.Fatalf("multipart 以外: %d", r.status)
	}
	key := newKey()
	upload(ct, body, key).mustStatus(t, 202)
	l = a.lookup(seed.GroupID, l.ID)
	if l.Status != "succeeded" || str(l.Source) != "image" || l.UnreadableCount != 1 || len(l.Entries) != 1 || len(l.SourceURLs) != 0 {
		t.Fatalf("画像からの候補: %s", dump(l))
	}
	// 同じ画像・同じキーの再送（区切り文字列は変わる）は最初の成功を返す。
	ct2, body2 := multipartImages(t, "images", pngBytes)
	upload(ct2, body2, key).mustStatus(t, 202)
}

// 障害注入で Web 検索の結果を取得元と一致しない状態にすると、採用せず画像の提出を求める。
func TestTocLookupSourceMismatchFault(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	a := s.client().devLogin("A")
	s.setFaults(map[string]any{"reading.toc_search": "source_mismatch"})
	var l toc.Lookup
	a.send(http.MethodPost, "/api/groups/"+seed.GroupID+"/reading/toc-lookups", map[string]string{"isbn": tocISBN}).mustStatus(t, 202).decode(t, &l)
	s.process()
	l = a.lookup(seed.GroupID, l.ID)
	if l.Status != "needs_image" || str(l.ReasonCode) != "source_mismatch" || len(l.Entries) != 0 {
		t.Fatalf("照合で不一致: %s", dump(l))
	}
	if r := a.send(http.MethodPost, "/api/groups/"+seed.GroupID+"/reading/toc-lookups", map[string]string{"isbn": "123"}); r.status != 422 {
		t.Fatalf("不正な ISBN: %d", r.status)
	}
	if r := a.get("/api/groups/" + seed.GroupID + "/reading/toc-lookups/toc_unknown"); r.status != 404 {
		t.Fatalf("存在しない取得: %d", r.status)
	}
}
