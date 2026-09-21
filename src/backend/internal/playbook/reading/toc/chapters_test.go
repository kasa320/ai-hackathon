package toc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
)

// readWith は、モデルが content を返したものとして LLMImageReader に読ませる。
func readWith(t *testing.T, content string) (ReadResult, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer srv.Close()
	rd := &LLMImageReader{Client: agent.NewClient(srv.URL, "key", 5*time.Second), Model: "vision"}
	res, _, err := rd.Read(ctx, []Image{{ContentType: "image/png", Data: pngImg}})
	return res, err
}

func TestReaderKeepsOnlyTopLevelChapters(t *testing.T) {
	res, err := readWith(t, `{"entries":[
		{"title":"はじめに","level":1},
		{"title":"第1章 基礎","level":1},
		{"title":"1.1 変数","level":2},
		{"title":"1.1.1 宣言","level":3},
		{"title":"第2章 応用","level":1},
		{"title":"2.1 関数","level":2},
		{"title":"付録","level":1},
		{"title":"範囲外","level":0},
		{"title":"深すぎる","level":4}
	],"unreadable_count":1}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"はじめに", 1}, {"第1章 基礎", 1}, {"第2章 応用", 1}, {"付録", 1}}
	if !reflect.DeepEqual(res.Entries, want) || res.UnreadableCount != 1 {
		t.Fatalf("最上位だけを掲載順に残す: %+v", res)
	}
}

func TestReaderDoesNotInventChapters(t *testing.T) {
	res, err := readWith(t, `{"entries":[
		{"title":"","level":1},
		{"title":"  　 ","level":1},
		{"title":"","level":2},
		{"title":"第1章 基礎","level":1},
		{"title":"壊れた階層","level":"one"},
		{"level":1},
		"文字列の項目",
		null
	],"unreadable_count":-3}`)
	if err != nil {
		t.Fatal(err)
	}
	// 空タイトルの最上位2件は読めなかった箇所に数え、章にはしない。負の unreadable_count は 0 に丸める。
	if !reflect.DeepEqual(res.Entries, []Entry{{"第1章 基礎", 1}}) || res.UnreadableCount != 3 {
		t.Fatalf("空タイトル・不正な項目は章にしない: %+v", res)
	}

	for name, content := range map[string]string{"JSONでない": "目次は読めませんでした", "entriesが配列でない": `{"entries":"第1章"}`} {
		if res, err := readWith(t, content); err == nil && len(res.Entries) != 0 {
			t.Errorf("%s: 章を作ってはいけない: %+v", name, res)
		}
	}
	if res, err := readWith(t, "```json\n{\"entries\":[{\"title\":\"序章\",\"level\":1}]}\n```"); err != nil || len(res.Entries) != 1 {
		t.Fatalf("コードブロックで囲まれていても読む: %+v %v", res, err)
	}
}

func TestSubmitImagesDropsNonChapterEntries(t *testing.T) {
	// 読み取り側が節・項や空タイトルを返しても、章の一覧には最上位だけを載せる。
	e := newEnv(t, Deps{Bib: fakeBib{book: nil}, Reader: fakeReader{res: ReadResult{Entries: []Entry{
		{Title: "序章", Level: 1}, {Title: "1.1 節", Level: 2}, {Title: "1.1.1 項", Level: 3}, {Title: " ", Level: 1}, {Title: "終章", Level: 1},
	}}}})
	l := e.start(t)
	if _, err := e.svc.SubmitImages(ctx, "grp_1", l.ID, []Image{{ContentType: "image/png", Data: pngImg}}, nil); err != nil {
		t.Fatal(err)
	}
	l, _ = e.svc.Get(ctx, "grp_1", l.ID)
	if l.Status != StatusSucceeded || !reflect.DeepEqual(l.Entries, []Entry{{"序章", 1}, {"終章", 1}}) {
		t.Fatalf("章だけを保存: %+v", l)
	}

	// 最上位の項目が1つも残らなければ、章を作らず読み取り失敗にする。
	e = newEnv(t, Deps{Bib: fakeBib{book: nil}, Reader: fakeReader{res: ReadResult{Entries: []Entry{{Title: "1.1 節", Level: 2}, {Title: "", Level: 1}}}}})
	l = e.start(t)
	_, _ = e.svc.SubmitImages(ctx, "grp_1", l.ID, []Image{{ContentType: "image/png", Data: pngImg}}, nil)
	if l, _ = e.svc.Get(ctx, "grp_1", l.ID); l.Status != StatusFailed || reason(l) != ReasonImageUnread || len(l.Entries) != 0 {
		t.Fatalf("最上位がなければ image_unreadable: %+v", l)
	}
}
