package toc

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OpenBD は openBD（https://openbd.jp/）から書誌を取得する。
type OpenBD struct {
	BaseURL string
	HTTP    *http.Client
}

func NewOpenBD() *OpenBD {
	return &OpenBD{BaseURL: "https://api.openbd.jp/v1", HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func getBody(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 2<<20))
}

func (o *OpenBD) Lookup(ctx context.Context, isbn string) (*Book, error) {
	body, err := getBody(ctx, o.HTTP, o.BaseURL+"/get?isbn="+isbn)
	if err != nil {
		return nil, fmt.Errorf("openBD: %w", err)
	}
	var items []*struct {
		Summary struct {
			Title     string `json:"title"`
			Publisher string `json:"publisher"`
			Author    string `json:"author"`
		} `json:"summary"`
		Onix struct {
			DescriptiveDetail struct {
				Extent []struct {
					ExtentValue string `json:"ExtentValue"`
				} `json:"Extent"`
			} `json:"DescriptiveDetail"`
		} `json:"onix"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("openBD: %w", err)
	}
	if len(items) == 0 || items[0] == nil || strings.TrimSpace(items[0].Summary.Title) == "" {
		return nil, nil
	}
	it := items[0]
	b := &Book{ISBN: isbn, Title: strings.TrimSpace(it.Summary.Title), Authors: splitAuthors(it.Summary.Author)}
	if p := strings.TrimSpace(it.Summary.Publisher); p != "" {
		b.Publisher = &p
	}
	for _, e := range it.Onix.DescriptiveDetail.Extent {
		if n, err := strconv.Atoi(e.ExtentValue); err == nil && n > 0 {
			b.Pages = &n
			break
		}
	}
	return b, nil
}

// splitAuthors は「山田太郎／著 鈴木花子／訳」のような表記を人名の一覧にする。
func splitAuthors(s string) []string {
	out := []string{}
	for _, part := range strings.Fields(strings.ReplaceAll(s, ",", " ")) {
		name := strings.TrimSpace(strings.SplitN(part, "／", 2)[0])
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// NDLSearch は国立国会図書館サーチの OpenSearch から書誌を取得する。
type NDLSearch struct {
	BaseURL string
	HTTP    *http.Client
}

func NewNDLSearch() *NDLSearch {
	return &NDLSearch{BaseURL: "https://ndlsearch.ndl.go.jp/api/opensearch", HTTP: &http.Client{Timeout: 5 * time.Second}}
}

var pagesRe = regexp.MustCompile(`(\d+)\s*p`)

func (n *NDLSearch) Lookup(ctx context.Context, isbn string) (*Book, error) {
	body, err := getBody(ctx, n.HTTP, n.BaseURL+"?isbn="+isbn)
	if err != nil {
		return nil, fmt.Errorf("NDL: %w", err)
	}
	var rss struct {
		Items []struct {
			Title     string   `xml:"title"`
			Creators  []string `xml:"creator"`
			Publisher []string `xml:"publisher"`
			Extent    []string `xml:"extent"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("NDL: %w", err)
	}
	if len(rss.Items) == 0 || strings.TrimSpace(rss.Items[0].Title) == "" {
		return nil, nil
	}
	it := rss.Items[0]
	b := &Book{ISBN: isbn, Title: strings.TrimSpace(it.Title), Authors: []string{}}
	for _, c := range it.Creators {
		if c = strings.TrimSpace(c); c != "" {
			b.Authors = append(b.Authors, c)
		}
	}
	if len(it.Publisher) > 0 && strings.TrimSpace(it.Publisher[0]) != "" {
		p := strings.TrimSpace(it.Publisher[0])
		b.Publisher = &p
	}
	for _, e := range it.Extent {
		if m := pagesRe.FindStringSubmatch(e); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil {
				b.Pages = &v
				break
			}
		}
	}
	return b, nil
}

// Chain は書誌DBを順に照会する（openBD → 国立国会図書館サーチ）。
// 見つからなければ nil, nil。どれにも届かなかった場合だけエラーを返す。
type Chain []Bibliography

func (c Chain) Lookup(ctx context.Context, isbn string) (*Book, error) {
	var lastErr error
	reached := false
	for _, b := range c {
		book, err := b.Lookup(ctx, isbn)
		if err != nil {
			lastErr = err
			continue
		}
		reached = true
		if book != nil {
			return book, nil
		}
	}
	if !reached && lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}
