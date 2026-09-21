package toc

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// NDLToc は国立国会図書館サーチの OAI-PMH から、出版情報登録センター（JPRO）が登録した目次を取得する。
//
// JPRO のレコードは識別子が ISBN から決まる（R100000137-I{ISBN13}）ので、検索せずに1回の取得で済む。
// 目次は出版社が登録した構造化データで、LLM を使わず、取得元ページとの照合も要らない。
// 目次が登録されていない本も多い（新しい本ほど登録されている）。その場合は画像か手入力に進む。
// 利用条件：非営利なら申請不要、NDLサーチの API を使っている旨を表示する（2026-09-21 確認
// https://ndlsearch.ndl.go.jp/help/api）。同時リクエスト数に制限があり、超えると 429 になる。
type NDLToc struct {
	BaseURL string
	HTTP    *http.Client
}

func NewNDLToc() *NDLToc {
	return &NDLToc{BaseURL: "https://ndlsearch.ndl.go.jp/api/oaipmh", HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// NDLPageURL は目次を載せている NDLサーチの書誌ページ。取得元として画面に出す。
func NDLPageURL(isbn string) string {
	return "https://ndlsearch.ndl.go.jp/books/R100000137-I" + isbn
}

// Contents は目次を返す。登録されていなければ nil, nil。
// 混雑（429・5xx）や通信の失敗は coord.ErrTransient を包んで返す。
func (n *NDLToc) Contents(ctx context.Context, isbn string) ([]Entry, error) {
	u := n.BaseURL + "?verb=GetRecord&metadataPrefix=dcndl&identifier=oai:ndlsearch.ndl.go.jp:R100000137-I" + isbn
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := n.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: NDL: %v", coord.ErrTransient, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: NDL: %v", coord.ErrTransient, err)
	}
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: NDL: status %d", coord.ErrTransient, res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NDL: status %d", res.StatusCode)
	}
	titles, err := parseNDLContents(body)
	if err != nil {
		return nil, err
	}
	if len(titles) == 0 {
		return nil, nil
	}
	return ndlEntries(titles), nil
}

var errNDLBusy = errors.New("NDL: busy")

// parseNDLContents は dcterms:tableOfContents の中の dcterms:title を順に取り出す。
// レコードがない（idDoesNotExist）・目次がない場合は空を返す。
func parseNDLContents(body []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var (
		titles  []string
		inTOC   bool
		inTitle bool
		buf     strings.Builder
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return titles, nil
		}
		if err != nil {
			return nil, fmt.Errorf("NDL: 応答を読めません: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "error":
				// 混雑時は HTTP 200 のまま <error><code>429</code> を返すことがある
				for _, a := range t.Attr {
					if a.Name.Local == "code" && a.Value == "idDoesNotExist" {
						return nil, nil
					}
				}
				var e struct {
					Code string `xml:"code"`
				}
				if dec.DecodeElement(&e, &t) == nil && (e.Code == "429" || strings.HasPrefix(e.Code, "5")) {
					return nil, fmt.Errorf("%w: %w", coord.ErrTransient, errNDLBusy)
				}
				return nil, nil
			case t.Name.Local == "tableOfContents":
				inTOC = true
			case inTOC && t.Name.Local == "title":
				inTitle = true
				buf.Reset()
			}
		case xml.CharData:
			if inTitle {
				buf.Write(t)
			}
		case xml.EndElement:
			switch {
			case t.Name.Local == "tableOfContents":
				inTOC = false
			case inTitle && t.Name.Local == "title":
				inTitle = false
				if s := strings.TrimSpace(buf.String()); s != "" && utf8.RuneCountInString(s) <= 200 {
					titles = append(titles, s)
				}
			}
		}
	}
}

// 番号の書き方から階層を推定する。JPRO の目次には階層の情報がなく、項目名だけが並ぶ。
var (
	partRe    = regexp.MustCompile(`^(第\s*[0-9０-９一二三四五六七八九十百IVXⅠ-Ⅻ]+\s*[部編]|(?i:part)\s*[0-9IVX]+)`)
	chapterRe = regexp.MustCompile(`^(第\s*[0-9０-９一二三四五六七八九十百]+\s*章|(?i:chapter)\s*[0-9０-９]+|[0-9０-９]+(\s|　|$))`)
	numberRe  = regexp.MustCompile(`^[0-9０-９]+((\.|-|－|‐)[0-9０-９]+)+`)
)

// ndlEntries は項目名に階層（部・章=1、節=2、項=3）を付ける。部があれば章以下を1段下げる。
// 項目が多すぎる場合は細かい階層から省く（範囲の選択に使うのは主に章と節）。
func ndlEntries(titles []string) []Entry {
	hasPart := false
	for _, t := range titles {
		if partRe.MatchString(t) {
			hasPart = true
			break
		}
	}
	shift := 0
	if hasPart {
		shift = 1
	}
	entries := make([]Entry, 0, len(titles))
	chapter := 0 // 直前の章の階層。コラムなど番号のない項目は章の下に置く
	for _, t := range titles {
		level := 1
		switch {
		case partRe.MatchString(t):
			level, chapter = 1, 0
		case chapterRe.MatchString(t):
			level = 1 + shift
			chapter = level
		case numberRe.MatchString(t):
			level = 1 + shift + strings.Count(normalizeSeparators(numberRe.FindString(t)), ".")
		case chapter > 0 && isColumn(t):
			level = chapter + 1
		}
		entries = append(entries, Entry{Title: t, Level: min(level, 3)})
	}
	for depth := 3; len(entries) > MaxEntries && depth > 1; depth-- {
		kept := entries[:0:0]
		for _, e := range entries {
			if e.Level < depth {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if len(entries) > MaxEntries {
		entries = entries[:MaxEntries]
	}
	return entries
}

func normalizeSeparators(s string) string {
	return strings.NewReplacer("-", ".", "－", ".", "‐", ".").Replace(s)
}

func isColumn(t string) bool {
	u := strings.ToUpper(t)
	return strings.HasPrefix(u, "COLUMN") || strings.HasPrefix(t, "コラム")
}
