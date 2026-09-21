// Package toc は輪読の教材登録で使う目次の取得（docs/api-endpoint.md）。
// ISBN から国立国会図書館サーチに登録された目次を引くか、目次ページの写真から書き写す。
// 取得結果は候補にすぎず、管理者が確認・修正して開催回登録に使うまで保存しない。
// LLM に書名や記憶から目次を作らせる経路は設けず、Web 検索の結果は取得元ページとの照合をコードで行う。
package toc

import (
	"context"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
)

// 取得の状態。
const (
	StatusResolvingBook = "resolving_book"
	StatusSearching     = "searching"
	StatusVerifying     = "verifying"
	StatusNeedsImage    = "needs_image"
	StatusReadingImage  = "reading_image"
	StatusSucceeded     = "succeeded"
	StatusFailed        = "failed"
)

// 目次の取得元（Lookup.source）。
const (
	SourceNDL   = "ndl"   // 国立国会図書館サーチ（出版情報登録センターの登録データ）
	SourceWeb   = "web"   // Web 検索し、取得元ページと照合した
	SourceImage = "image" // 目次ページの写真から書き写した
)

// 理由コード。
const (
	ReasonBookNotFound   = "book_not_found"
	ReasonTocNotFound    = "toc_not_found"
	ReasonSourceMismatch = "source_mismatch"
	ReasonImageUnread    = "image_unreadable"
	ReasonBudget         = "budget_exceeded"
	ReasonModelError     = "model_error"
)

// 上限（docs/api-endpoint.md）。
const (
	LookupTTL        = 24 * time.Hour
	RetryWindow      = 10 * time.Minute
	DailyLimit       = 10
	MaxImages        = 10
	MaxImageBytes    = 4 << 20
	MaxTotalBytes    = 20 << 20
	MatchThreshold   = 0.9
	MaxEntries       = 300
	maxLLMPerLookup  = 6
	fetchTimeout     = 5 * time.Second
	fetchMaxBytes    = 2 << 20
	fetchMaxRedirect = 3
)

// FaultKey は開発モードの障害注入のキー（PUT /api/dev/faults）。
const FaultKey = "reading.toc_search"

// DefineFaults は障害注入のキーを登録する。
func DefineFaults(r *fault.Registry) {
	r.Define(FaultKey, ReasonTocNotFoundFault, ReasonSourceMismatchFault)
}

// 障害注入の値。
const (
	ReasonTocNotFoundFault    = "not_found"
	ReasonSourceMismatchFault = "source_mismatch"
)

// Book は書誌DBで確定した本の情報。
type Book struct {
	ISBN      string   `json:"isbn"`
	Title     string   `json:"title"`
	Authors   []string `json:"authors"`
	Publisher *string  `json:"publisher"`
	Pages     *int     `json:"pages"`
}

// Entry は目次の候補1件（章=1、節=2、項=3）。
type Entry struct {
	Title string `json:"title"`
	Level int    `json:"level"`
}

// Lookup は公開 API の TocLookup。
type Lookup struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	Book            *Book     `json:"book"`
	Source          *string   `json:"source"`
	SourceURLs      []string  `json:"source_urls"`
	Entries         []Entry   `json:"entries"`
	UnreadableCount int       `json:"unreadable_count"`
	ReasonCode      *string   `json:"reason_code"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// Bibliography は ISBN から書誌を確定する（openBD → 国立国会図書館サーチ）。見つからなければ nil, nil。
type Bibliography interface {
	Lookup(ctx context.Context, isbn string) (*Book, error)
}

// Contents は ISBN から登録済みの目次を取得する（国立国会図書館サーチの JPRO データ）。
// 登録されていなければ nil, nil。混雑・通信の失敗は coord.ErrTransient を包んで返す。
type Contents interface {
	Contents(ctx context.Context, isbn string) ([]Entry, error)
}

// SearchResult は Web 検索で見つけた目次と取得元。
type SearchResult struct {
	Found   bool
	URLs    []string
	Entries []Entry
}

// Searcher は Web 検索で目次を探す。
type Searcher interface {
	Search(ctx context.Context, b Book) (SearchResult, []coord.LLMCall, error)
}

// Fetcher は取得元ページの本文（テキスト）を安全に取得する。
type Fetcher interface {
	Fetch(ctx context.Context, url string) (string, error)
}

// Image は提出された画像。DB・ログ・ファイルに保存しない。
type Image struct {
	ContentType string
	Data        []byte
}

// ReadResult は画像から書き写した目次。
type ReadResult struct {
	Entries         []Entry
	UnreadableCount int
}

// ImageReader は画像に写っている文字だけを書き写す。
type ImageReader interface {
	Read(ctx context.Context, images []Image) (ReadResult, []coord.LLMCall, error)
}
