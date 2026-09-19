package toc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var (
	ctx    = context.Background()
	t0     = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	quiet  = slog.New(slog.NewTextHandler(io.Discard, nil))
	isbn   = "9784297127831"
	pngImg = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)
)

type fakeBib struct {
	book *Book
	err  error
}

func (f fakeBib) Lookup(context.Context, string) (*Book, error) { return f.book, f.err }

type fakeSearch struct{ res SearchResult }

func (f fakeSearch) Search(context.Context, Book) (SearchResult, []coord.LLMCall, error) {
	return f.res, []coord.LLMCall{{Model: "search", Currency: "unknown", Succeeded: true}}, nil
}

type fakeFetch map[string]string

func (f fakeFetch) Fetch(_ context.Context, u string) (string, error) {
	if s, ok := f[u]; ok {
		return s, nil
	}
	return "", errors.New("not found")
}

type fakeReader struct {
	res ReadResult
	err error
}

func (f fakeReader) Read(context.Context, []Image) (ReadResult, []coord.LLMCall, error) {
	return f.res, []coord.LLMCall{{Model: "vision", Currency: "unknown", Succeeded: f.err == nil}}, f.err
}

type env struct {
	svc    *Service
	st     *store.Store
	clk    *clock.Offset
	faults *fault.Registry
}

func newEnv(t *testing.T, d Deps) env {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		u, _ := tx.UpsertUser(ctx, "111111111111111111", "A", t0)
		_ = tx.CreateGroup(ctx, store.Group{ID: "grp_1", Name: "g", OwnerUserID: u.ID, CreatedAt: t0})
		return tx.CreateGroup(ctx, store.Group{ID: "grp_2", Name: "g2", OwnerUserID: u.ID, CreatedAt: t0})
	})
	clk := clock.NewOffset(clock.Fixed{T: t0})
	faults := fault.New()
	DefineFaults(faults)
	d.Store, d.Clock, d.Faults, d.Log = st, clk, faults, quiet
	if d.Fetcher == nil {
		d.Fetcher = fakeFetch{}
	}
	svc := NewService(d)
	svc.Async = func(f func()) { f() }
	return env{svc: svc, st: st, clk: clk, faults: faults}
}

func (e env) start(t *testing.T) Lookup {
	t.Helper()
	res, err := e.svc.Start(ctx, "grp_1", isbn, nil)
	if err != nil || res.Status != http.StatusAccepted {
		t.Fatalf("start: %+v %v", res, err)
	}
	var l Lookup
	_ = json.Unmarshal(res.Body, &l)
	if _, err := e.svc.ProcessDue(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Get(ctx, "grp_1", l.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var book = &Book{ISBN: isbn, Title: "サンプル技術書", Authors: []string{"著者"}}

func reason(l Lookup) string {
	if l.ReasonCode == nil {
		return ""
	}
	return *l.ReasonCode
}

func TestWebResultIsVerifiedAgainstSource(t *testing.T) {
	entries := []Entry{{Title: "第1章 はじめに", Level: 1}, {Title: "1.1 背景", Level: 2}}
	page := "<html><body><h2>第１章　はじめに</h2><p>1.1 背景</p><script>var x='第9章';</script></body></html>"
	e := newEnv(t, Deps{Bib: fakeBib{book: book}, Searcher: fakeSearch{SearchResult{Found: true, URLs: []string{"https://pub.example/book"}, Entries: entries}},
		Fetcher: fakeFetch{"https://pub.example/book": HTMLText(page)}})
	l := e.start(t)
	if l.Status != StatusSucceeded || *l.Source != "web" || len(l.SourceURLs) != 1 || len(l.Entries) != 2 || l.Book == nil || l.Book.Title != "サンプル技術書" {
		t.Fatalf("lookup = %+v", l)
	}
}

func TestMismatchedWebResultAsksForImage(t *testing.T) {
	entries := []Entry{{Title: "第1章 はじめに", Level: 1}, {Title: "第2章 架空の章", Level: 1}}
	e := newEnv(t, Deps{Bib: fakeBib{book: book}, Searcher: fakeSearch{SearchResult{Found: true, URLs: []string{"https://pub.example/book"}, Entries: entries}},
		Fetcher: fakeFetch{"https://pub.example/book": "第1章 はじめに"}})
	l := e.start(t)
	if l.Status != StatusNeedsImage || reason(l) != ReasonSourceMismatch || len(l.Entries) != 0 || l.Source != nil {
		t.Fatalf("照合で不一致なら採用しない: %+v", l)
	}
}

func TestNeedsImageReasons(t *testing.T) {
	e := newEnv(t, Deps{Bib: fakeBib{book: nil}, Searcher: fakeSearch{SearchResult{Found: true}}})
	if l := e.start(t); l.Status != StatusNeedsImage || reason(l) != ReasonBookNotFound {
		t.Fatalf("書誌が見つからなければ Web 検索せず画像を依頼: %+v", l)
	}
	e = newEnv(t, Deps{Bib: fakeBib{book: book}, Searcher: fakeSearch{SearchResult{Found: false}}})
	if l := e.start(t); l.Status != StatusNeedsImage || reason(l) != ReasonTocNotFound {
		t.Fatalf("not_found: %+v", l)
	}
	// Web 検索が設定されていない（fake モード）場合も画像の提出へ進む。
	e = newEnv(t, Deps{Bib: fakeBib{book: book}})
	if l := e.start(t); l.Status != StatusNeedsImage || reason(l) != ReasonTocNotFound {
		t.Fatalf("検索なし: %+v", l)
	}
}

func TestFaultInjectionSwitchesToImage(t *testing.T) {
	for value, want := range map[string]string{ReasonTocNotFoundFault: ReasonTocNotFound, ReasonSourceMismatchFault: ReasonSourceMismatch} {
		e := newEnv(t, Deps{Bib: fakeBib{book: book}, Searcher: fakeSearch{SearchResult{Found: true, URLs: []string{"https://x.example"}, Entries: []Entry{{Title: "a", Level: 1}}}},
			Fetcher: fakeFetch{"https://x.example": "a"}})
		v := value
		_ = e.faults.Replace(map[string]*string{FaultKey: &v})
		if l := e.start(t); l.Status != StatusNeedsImage || reason(l) != want {
			t.Fatalf("%s: %+v", value, l)
		}
	}
}

func TestBibliographyOutageRetriesThenAsksForImage(t *testing.T) {
	e := newEnv(t, Deps{Bib: fakeBib{err: errors.New("timeout")}})
	l := e.start(t)
	if l.Status != StatusResolvingBook {
		t.Fatalf("一時的な障害は再試行: %+v", l)
	}
	for i := 0; i < 10; i++ {
		e.clk.Advance(2 * time.Minute)
		_, _ = e.svc.ProcessDue(ctx)
	}
	l, _ = e.svc.Get(ctx, "grp_1", l.ID)
	if l.Status != StatusNeedsImage || reason(l) != ReasonBookNotFound {
		t.Fatalf("10分を過ぎたら画像の提出へ: %+v", l)
	}
}

func TestImageSubmission(t *testing.T) {
	e := newEnv(t, Deps{Bib: fakeBib{book: nil}, Reader: fakeReader{res: ReadResult{Entries: []Entry{{Title: "第1章", Level: 1}}, UnreadableCount: 2}}})
	l := e.start(t)
	if _, err := e.svc.SubmitImages(ctx, "grp_2", l.ID, []Image{{ContentType: "image/png", Data: pngImg}}, nil); code(err) != apperr.NotFound {
		t.Fatalf("別グループの取得は 404: %v", err)
	}
	if _, err := e.svc.SubmitImages(ctx, "grp_1", l.ID, []Image{{ContentType: "image/png", Data: pngImg}}, nil); err != nil {
		t.Fatal(err)
	}
	l, _ = e.svc.Get(ctx, "grp_1", l.ID)
	if l.Status != StatusSucceeded || *l.Source != "image" || l.UnreadableCount != 2 || len(l.Entries) != 1 {
		t.Fatalf("画像からの取得: %+v", l)
	}
	if _, err := e.svc.SubmitImages(ctx, "grp_1", l.ID, []Image{{ContentType: "image/png", Data: pngImg}}, nil); code(err) != apperr.InvalidState {
		t.Fatalf("needs_image 以外では 409: %v", err)
	}
	// 画像は DB に保存しない（LLM 呼び出しの記録だけが残る）。
	_ = e.st.Tx(ctx, func(tx *store.Tx) error {
		row, _ := tx.TocLookup(ctx, l.ID)
		if bytes.Contains(row.Entries, pngImg[:8]) {
			t.Fatal("画像が保存されている")
		}
		n, _ := tx.CountLLMCallsByLookup(ctx, l.ID)
		if n != 1 {
			t.Fatalf("LLM 呼び出しの記録 = %d", n)
		}
		return nil
	})

	e = newEnv(t, Deps{Bib: fakeBib{book: nil}, Reader: fakeReader{res: ReadResult{}}})
	l = e.start(t)
	_, _ = e.svc.SubmitImages(ctx, "grp_1", l.ID, []Image{{ContentType: "image/png", Data: pngImg}}, nil)
	if l, _ = e.svc.Get(ctx, "grp_1", l.ID); l.Status != StatusFailed || reason(l) != ReasonImageUnread {
		t.Fatalf("読み取れなければ手入力へ: %+v", l)
	}
}

func TestDailyLimitAndValidation(t *testing.T) {
	e := newEnv(t, Deps{Bib: fakeBib{book: nil}})
	if _, err := e.svc.Start(ctx, "grp_1", "9784297127830", nil); code(err) != apperr.ValidationFailed {
		t.Fatalf("不正なISBN: %v", err)
	}
	var last Lookup
	for i := 0; i < DailyLimit+1; i++ {
		res, err := e.svc.Start(ctx, "grp_1", isbn, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(res.Body, &last)
	}
	if last.Status != StatusFailed || reason(last) != ReasonBudget {
		t.Fatalf("1日10回を超えたら budget_exceeded: %+v", last)
	}
	e.clk.Advance(LookupTTL)
	if _, err := e.svc.Get(ctx, "grp_1", last.ID); code(err) != apperr.NotFound {
		t.Fatalf("期限切れは 404: %v", err)
	}
}

func code(err error) string {
	var e *apperr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestMatchRatioNormalizes(t *testing.T) {
	entries := []Entry{{Title: "Chapter 1 Ｇｏ入門", Level: 1}, {Title: "１．２ 型", Level: 2}}
	if r := MatchRatio(entries, []string{"chapter1 go入門 ... 1.2 型"}); r != 1 {
		t.Fatalf("NFKC・空白除去・小文字化で一致するはず: %v", r)
	}
	if r := MatchRatio(entries, []string{"chapter1 go入門"}); r != 0.5 {
		t.Fatalf("ratio = %v", r)
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1::1": true,
		"127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false, "172.16.0.1": false, "169.254.169.254": false,
		"::1": false, "fe80::1": false, "fc00::1": false, "0.0.0.0": false, "100.64.0.1": false, "::ffff:127.0.0.1": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddr(%s) = %v", addr, got)
		}
	}
}

func TestSafeFetcherRejectsInternalTargets(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	f := NewSafeFetcher()
	if _, err := f.Fetch(ctx, srv.URL); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("ループバックへの接続を拒否すべき: %v", err)
	}
	if _, err := f.Fetch(ctx, "http://example.com"); err == nil {
		t.Fatal("http は拒否")
	}
}

func TestBibliographySources(t *testing.T) {
	openbd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("isbn") == isbn {
			_, _ = w.Write([]byte(`[{"summary":{"title":"サンプル技術書","publisher":"技術評論社","author":"山田太郎／著 鈴木花子／訳"},"onix":{"DescriptiveDetail":{"Extent":[{"ExtentValue":"432"}]}}}]`))
			return
		}
		_, _ = w.Write([]byte(`[null]`))
	}))
	defer openbd.Close()
	o := &OpenBD{BaseURL: openbd.URL, HTTP: openbd.Client()}
	b, err := o.Lookup(ctx, isbn)
	if err != nil || b == nil || b.Title != "サンプル技術書" || strings.Join(b.Authors, ",") != "山田太郎,鈴木花子" || *b.Pages != 432 || *b.Publisher != "技術評論社" {
		t.Fatalf("openBD: %+v %v", b, err)
	}
	if b, err := o.Lookup(ctx, "9784873115658"); b != nil || err != nil {
		t.Fatalf("未収録は nil: %+v %v", b, err)
	}

	ndl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/"><channel>
<item><title>NDLの本</title><dc:creator>作者A</dc:creator><dc:creator>作者B</dc:creator><dc:publisher>出版社X</dc:publisher><dcterms:extent>320p ; 24cm</dcterms:extent></item>
</channel></rss>`))
	}))
	defer ndl.Close()
	n := &NDLSearch{BaseURL: ndl.URL, HTTP: ndl.Client()}
	b, err = Chain{o, n}.Lookup(ctx, "9784873115658")
	if err != nil || b == nil || b.Title != "NDLの本" || len(b.Authors) != 2 || *b.Pages != 320 {
		t.Fatalf("openBD になければ NDL: %+v %v", b, err)
	}
}

func multipartBody(t *testing.T, files map[string][]byte) (string, []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		field := "images"
		if strings.HasPrefix(name, "other") {
			field = "other"
		}
		w, _ := mw.CreateFormFile(field, name)
		_, _ = w.Write(data)
	}
	_ = mw.Close()
	return mw.FormDataContentType(), buf.Bytes()
}

func TestParseImages(t *testing.T) {
	ct, body := multipartBody(t, map[string][]byte{"a.png": pngImg})
	imgs, hash, err := parseImages(ct, body)
	if err != nil || len(imgs) != 1 || imgs[0].ContentType != "image/png" {
		t.Fatalf("png: %v %v", imgs, err)
	}
	// 区切り文字列が変わっても同じ画像なら同じハッシュ（再送判定用）。
	ct2, body2 := multipartBody(t, map[string][]byte{"a.png": pngImg})
	if _, hash2, _ := parseImages(ct2, body2); hash2 != hash {
		t.Fatal("同じ画像のハッシュが変わった")
	}
	cases := map[string]struct {
		files map[string][]byte
		code  string
	}{
		"画像以外":  {map[string][]byte{"a.txt": []byte("hello")}, apperr.UnsupportedMediaType},
		"大きすぎる": {map[string][]byte{"a.png": append(append([]byte{}, pngImg...), make([]byte, MaxImageBytes)...)}, apperr.PayloadTooLarge},
		"多すぎる":  {map[string][]byte{"1.png": pngImg, "2.png": pngImg, "3.png": pngImg, "4.png": pngImg, "5.png": pngImg, "6.png": pngImg}, apperr.ValidationFailed},
		"別の項目":  {map[string][]byte{"other.png": pngImg}, apperr.ValidationFailed},
	}
	for name, tc := range cases {
		ct, body := multipartBody(t, tc.files)
		if _, _, err := parseImages(ct, body); code(err) != tc.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := parseImages("application/json", []byte("{}")); code(err) != apperr.UnsupportedMediaType {
		t.Fatalf("multipart 以外: %v", err)
	}
}
