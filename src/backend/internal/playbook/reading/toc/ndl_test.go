package toc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// ndlRecord は OAI-PMH の GetRecord（dcndl）の応答を、目次まわりだけ残して再現したもの。
func ndlRecord(titles ...string) string {
	var b strings.Builder
	for _, t := range titles {
		fmt.Fprintf(&b, "<rdf:Description><dcterms:title>%s</dcterms:title></rdf:Description>", t)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/"><GetRecord><record><metadata>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcndl="http://ndl.go.jp/dcndl/terms/">
<dcndl:BibResource><dcterms:title>本の題名</dcterms:title>
<dcterms:tableOfContents rdf:parseType="Collection">` + b.String() + `</dcterms:tableOfContents>
</dcndl:BibResource></rdf:RDF></metadata></record></GetRecord></OAI-PMH>`
}

func TestParseNDLContents(t *testing.T) {
	got, err := parseNDLContents([]byte(ndlRecord("1 悪しき構造", "1.1 意味不明な命名", "  ")))
	if err != nil || strings.Join(got, "|") != "1 悪しき構造|1.1 意味不明な命名" {
		t.Fatalf("目次: %q %v（本の題名は含めない）", got, err)
	}
	notFound := `<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/"><error code="idDoesNotExist"/></OAI-PMH>`
	if got, err := parseNDLContents([]byte(notFound)); err != nil || got != nil {
		t.Fatalf("レコードなし: %q %v", got, err)
	}
	busy := `<?xml version="1.0"?><error><code>429</code><message>Too Many Requests</message></error>`
	if _, err := parseNDLContents([]byte(busy)); !errors.Is(err, coord.ErrTransient) {
		t.Fatalf("混雑は一時的な失敗: %v", err)
	}
}

func TestNDLEntriesLevels(t *testing.T) {
	cases := []struct {
		titles []string
		levels []int
	}{
		// 章番号だけの形式
		{[]string{"はじめに", "1 悪しき構造", "1.1 命名", "1.3.1 重複コード", "2 設計の初歩"}, []int{1, 1, 2, 3, 1}},
		// 「第N章」「Chapter N」「N-N」、コラムは章の下
		{[]string{"第1章 概要", "1.1 背景", "Chapter 2　値", "2-1 値とは", "COLUMN｜不変", "付録"}, []int{1, 2, 1, 2, 2, 1}},
		// 部があれば章以下を1段下げる
		{[]string{"まえがき", "第I部 例題", "第1章 多国通貨", "1.1 はじめ", "第II部 xUnit"}, []int{1, 1, 2, 3, 1}},
	}
	for _, c := range cases {
		got := ndlEntries(c.titles)
		for i, e := range got {
			if e.Level != c.levels[i] || e.Title != c.titles[i] {
				t.Fatalf("%q: %d件目 = %+v, want level %d", c.titles, i, e, c.levels[i])
			}
		}
	}

	// 多すぎる場合は細かい階層から省く
	var many []string
	for ch := 1; ch <= 20; ch++ {
		many = append(many, fmt.Sprintf("第%d章 章", ch))
		for s := 1; s <= 5; s++ {
			many = append(many, fmt.Sprintf("%d.%d 節", ch, s))
			for k := 1; k <= 3; k++ {
				many = append(many, fmt.Sprintf("%d.%d.%d 項", ch, s, k))
			}
		}
	}
	got := ndlEntries(many)
	if len(got) != 120 {
		t.Fatalf("項を省いて章と節だけ残す: %d件", len(got))
	}
}

func TestNDLTocOverHTTP(t *testing.T) {
	status, body := http.StatusOK, ndlRecord("第1章 はじめに")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "R100000137-I"+isbn) {
			t.Errorf("識別子: %s", r.URL.RawQuery)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	n := &NDLToc{BaseURL: srv.URL, HTTP: srv.Client()}
	got, err := n.Contents(ctx, isbn)
	if err != nil || len(got) != 1 || got[0].Title != "第1章 はじめに" {
		t.Fatalf("取得: %+v %v", got, err)
	}
	status = http.StatusServiceUnavailable
	if _, err := n.Contents(ctx, isbn); !errors.Is(err, coord.ErrTransient) {
		t.Fatalf("5xx は一時的な失敗: %v", err)
	}
}

type fakeContents struct {
	entries []Entry
	err     error
}

func (f fakeContents) Contents(context.Context, string) ([]Entry, error) { return f.entries, f.err }

func TestRegisteredContentsAreUsedWithoutSearch(t *testing.T) {
	entries := []Entry{{Title: "第1章 はじめに", Level: 1}}
	e := newEnv(t, Deps{Bib: fakeBib{book: book}, Contents: fakeContents{entries: entries}, Searcher: fakeSearch{SearchResult{Found: false}}})
	l := e.start(t)
	if l.Status != StatusSucceeded || *l.Source != SourceNDL || len(l.Entries) != 1 || len(l.SourceURLs) != 1 || l.SourceURLs[0] != NDLPageURL(isbn) {
		t.Fatalf("登録済みの目次: %+v", l)
	}

	// 登録されていなければ、これまでどおり画像の提出へ進む
	e = newEnv(t, Deps{Bib: fakeBib{book: book}, Contents: fakeContents{}})
	if l := e.start(t); l.Status != StatusNeedsImage || reason(l) != ReasonTocNotFound {
		t.Fatalf("目次なし: %+v", l)
	}

	// 混雑している間は再試行し、10分を過ぎたら画像の提出へ進む
	e = newEnv(t, Deps{Bib: fakeBib{book: book}, Contents: fakeContents{err: fmt.Errorf("%w: busy", coord.ErrTransient)}})
	l = e.start(t)
	if l.Status != StatusSearching {
		t.Fatalf("混雑中は再試行を待つ: %+v", l)
	}
	for i := 0; i < 20 && l.Status == StatusSearching; i++ {
		e.clk.Advance(RetryWindow / 4)
		if _, err := e.svc.ProcessDue(ctx); err != nil {
			t.Fatal(err)
		}
		l, _ = e.svc.Get(ctx, "grp_1", l.ID)
	}
	if l.Status != StatusNeedsImage || reason(l) != ReasonTocNotFound {
		t.Fatalf("混雑が続いたら画像へ: %+v", l)
	}
}

func TestStartWithoutISBNAcceptsImages(t *testing.T) {
	e := newEnv(t, Deps{Bib: fakeBib{book: book}, Reader: fakeReader{res: ReadResult{Entries: []Entry{{Title: "第1章", Level: 1}}}}})
	res, err := e.svc.Start(ctx, "grp_1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var l Lookup
	if err := json.Unmarshal(res.Body, &l); err != nil {
		t.Fatal(err)
	}
	if l.Status != StatusNeedsImage || l.ReasonCode != nil || l.Book != nil {
		t.Fatalf("ISBN なしは写真の受付から: %+v", l)
	}
	if _, err := e.svc.SubmitImages(ctx, "grp_1", l.ID, []Image{{ContentType: "image/png", Data: pngImg}}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "grp_1", l.ID)
	if got.Status != StatusSucceeded || *got.Source != SourceImage {
		t.Fatalf("写真から読み取り: %+v", got)
	}
}
