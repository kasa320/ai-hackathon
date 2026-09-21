package toc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// LLMSearcher は Web 検索付きのモデル（OrcaRouter 経由）で出版社等の公開ページから目次を探す。
// 結果は取得元ページとの照合を通るまで採用しない。
type LLMSearcher struct {
	Client *agent.Client
	Model  string
}

const searchPrompt = `次の本の目次（章・節）を、出版社や書店などの公開ページから Web 検索で探してください。
- 取得元のページに実際に書かれている目次の項目だけを、書かれている表記のまま返してください。
- 書名や記憶から目次を推測・作成してはいけません。見つからなければ not_found を返してください。
- 出力は次の JSON だけにしてください（説明文やコードブロックは不要）：
{"status":"found","source_urls":["https://..."],"entries":[{"title":"第1章 ...","level":1},{"title":"1.1 ...","level":2}]}
または {"status":"not_found"}
level は章=1、節=2、項=3。`

func (s *LLMSearcher) Search(ctx context.Context, b Book) (SearchResult, []coord.LLMCall, error) {
	user := fmt.Sprintf("ISBN: %s\n書名: %s\n著者: %s", b.ISBN, b.Title, strings.Join(b.Authors, "、"))
	if b.Publisher != nil {
		user += "\n出版社: " + *b.Publisher
	}
	res, call, err := s.Client.Chat(ctx, agent.ChatRequest{Model: s.Model, Messages: []agent.Message{
		{Role: "system", Content: searchPrompt}, {Role: "user", Content: user},
	}})
	calls := []coord.LLMCall{call}
	if err != nil {
		return SearchResult{}, calls, err
	}
	var out struct {
		Status     string   `json:"status"`
		SourceURLs []string `json:"source_urls"`
		Entries    []Entry  `json:"entries"`
	}
	if err := decodeContent(res, &out); err != nil || out.Status != "found" {
		// 形式が読めない出力も「見つからない」として扱い、画像の提出へ進む。
		return SearchResult{Found: false}, calls, nil
	}
	return SearchResult{Found: true, URLs: out.SourceURLs, Entries: out.Entries}, calls, nil
}

// LLMImageReader は画像に写っている目次の最上位の項目（章）だけを書き写す（読めない箇所は推測しない）。
type LLMImageReader struct {
	Client *agent.Client
	Model  string
}

const readPrompt = `画像は本の目次ページです。目次における最上位の階層の項目（章）だけを、写っている文字のまま書き写してください。
- 最上位とは、目次の中で最も大きな見出しの階層です。「第1章」「Chapter 1」「Part I」のような章立てのほか、番号のない「はじめに」「序章」「終章」「おわりに」「付録」「索引」なども、最上位の階層に並んでいれば含めてください。
- 節・項（1.1、1.1.1、「第1節」など章の下の階層）は含めないでください。ページ番号・リーダー線（……）も含めないでください。
- title には章番号と章タイトルだけを書いてください（例：「第1章 はじめてのGo」）。画像に書かれていない番号やタイトルを補ったり、推測したりしてはいけません。
- 掲載順のまま返してください。
- 読めない・欠けている最上位の項目は entries に入れず、その数を unreadable_count に数えてください。
- 出力は次の JSON だけにしてください：{"entries":[{"title":"第1章 ...","level":1}],"unreadable_count":0}
- level は最上位の項目なら常に 1 にしてください。目次が写っていなければ entries を空にしてください。`

func (r *LLMImageReader) Read(ctx context.Context, images []Image) (ReadResult, []coord.LLMCall, error) {
	content := []map[string]any{{"type": "text", "text": "目次ページの画像です。"}}
	for _, img := range images {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{
			"url": "data:" + img.ContentType + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
		}})
	}
	res, call, err := r.Client.Chat(ctx, agent.ChatRequest{Model: r.Model, Messages: []agent.Message{
		{Role: "system", Content: readPrompt}, {Role: "user", Content: content},
	}})
	calls := []coord.LLMCall{call}
	if err != nil {
		return ReadResult{}, calls, err
	}
	var out struct {
		Entries         []json.RawMessage `json:"entries"`
		UnreadableCount int               `json:"unreadable_count"`
	}
	if err := decodeContent(res, &out); err != nil {
		return ReadResult{}, calls, err
	}
	// 形の崩れた項目は、章として採用せずに読み飛ばす。
	raw := make([]Entry, 0, len(out.Entries))
	for _, item := range out.Entries {
		var e Entry
		if json.Unmarshal(item, &e) == nil {
			raw = append(raw, e)
		}
	}
	chapters, blank := TopLevelChapters(raw)
	return ReadResult{Entries: chapters, UnreadableCount: max(0, out.UnreadableCount) + blank}, calls, nil
}

// TopLevelChapters は目次の最上位（level 1）の項目だけを、掲載順のまま残す。
// 節・項（level 2 以上）や範囲外の level は、モデルの出力にあっても除く。
// タイトルが空の最上位の項目は章にせず、読めなかった箇所として blank に数える。
func TopLevelChapters(entries []Entry) (chapters []Entry, blank int) {
	for _, e := range entries {
		if e.Level != 1 {
			continue
		}
		title := strings.TrimSpace(e.Title)
		switch {
		case normalize(title) == "":
			blank++
		case len(chapters) < MaxEntries:
			chapters = append(chapters, Entry{Title: title, Level: 1})
		}
	}
	return chapters, blank
}

// decodeContent はモデルの本文から JSON を取り出す（コードブロックで囲まれていても読む）。
func decodeContent(res *agent.ChatResponse, v any) error {
	if res == nil || len(res.Choices) == 0 || res.Choices[0].Message.Content == nil {
		return errors.New("toc: 本文がありません")
	}
	s := strings.TrimSpace(*res.Choices[0].Message.Content)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	return json.Unmarshal([]byte(s), v)
}
