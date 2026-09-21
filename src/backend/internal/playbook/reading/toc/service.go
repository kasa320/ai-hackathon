package toc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// ErrNotConfigured は外部サービス（LLM）が設定されていないこと。
var ErrNotConfigured = errors.New("toc: not configured")

// Service は目次の取得を進める。処理状態は DB に保存し、再起動後も続きから処理する（画像の読み取りを除く）。
type Service struct {
	st       *store.Store
	clock    clock.Clock
	faults   *fault.Registry
	bib      Bibliography
	contents Contents // nil なら登録済みの目次を引かない
	search   Searcher // nil なら Web 検索しない（toc_not_found）
	fetch    Fetcher
	reader   ImageReader // nil なら画像を読めない（model_error）
	log      *slog.Logger
	// Async は画像の読み取りを実行する。テストでは同期実行に差し替える。
	Async func(func())
	wake  chan struct{}
}

type Deps struct {
	Store    *store.Store
	Clock    clock.Clock
	Faults   *fault.Registry
	Bib      Bibliography
	Contents Contents
	Searcher Searcher
	Fetcher  Fetcher
	Reader   ImageReader
	Log      *slog.Logger
}

func NewService(d Deps) *Service {
	return &Service{st: d.Store, clock: d.Clock, faults: d.Faults, bib: d.Bib, contents: d.Contents, search: d.Searcher, fetch: d.Fetcher, reader: d.Reader, log: d.Log,
		Async: func(f func()) { go f() }, wake: make(chan struct{}, 1)}
}

func (s *Service) now() time.Time { return s.clock.Now().UTC().Truncate(time.Millisecond) }

func view(l store.TocLookup) Lookup {
	out := Lookup{ID: l.ID, Status: l.Status, SourceURLs: l.SourceURLs, Entries: []Entry{}, UnreadableCount: l.UnreadableCount, ExpiresAt: l.ExpiresAt}
	if out.SourceURLs == nil {
		out.SourceURLs = []string{}
	}
	if len(l.Book) > 0 {
		var b Book
		if json.Unmarshal(l.Book, &b) == nil {
			out.Book = &b
		}
	}
	// 照合を終えるまでは候補を見せない。
	if l.Status == StatusSucceeded {
		_ = json.Unmarshal(l.Entries, &out.Entries)
		if out.Entries == nil {
			out.Entries = []Entry{}
		}
	} else {
		out.SourceURLs = []string{}
	}
	if l.Source != "" {
		src := l.Source
		out.Source = &src
	}
	if l.ReasonCode != "" {
		r := l.ReasonCode
		out.ReasonCode = &r
	}
	return out
}

func encode(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Start は ISBN から目次の取得を開始する。1日の上限を超えた場合も受け付けた上で failed（budget_exceeded）にする。
// ISBN が空なら書誌は調べず、目次ページの写真を受け付ける状態（needs_image）で始める。
func (s *Service) Start(ctx context.Context, groupID, isbn string, idem *store.IdemKey) (store.Response, error) {
	now := s.now()
	res, err := s.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		if isbn != "" && !reading.ValidISBN13(isbn) {
			return store.Response{}, apperr.Validation(apperr.Field{Path: "isbn", Message: "ISBNはハイフンなしの13桁で入力してください"})
		}
		n, err := tx.CountTocLookupsSince(ctx, groupID, now.Add(-24*time.Hour))
		if err != nil {
			return store.Response{}, err
		}
		l := store.TocLookup{ID: store.NewID("toc"), GroupID: groupID, ISBN: isbn, Status: StatusResolvingBook, Entries: json.RawMessage("[]"),
			NextRunAt: now, CreatedAt: now, ExpiresAt: now.Add(LookupTTL)}
		if isbn == "" {
			l.Status = StatusNeedsImage
		}
		if n >= DailyLimit {
			l.Status, l.ReasonCode = StatusFailed, ReasonBudget
		}
		if err := tx.CreateTocLookup(ctx, l); err != nil {
			return store.Response{}, err
		}
		return store.Response{Status: http.StatusAccepted, Body: encode(view(l))}, nil
	})
	if err == nil {
		s.Wake()
	}
	return res, err
}

// Get は取得状況と候補を返す。別のグループ・期限切れは 404。
func (s *Service) Get(ctx context.Context, groupID, lookupID string) (Lookup, error) {
	var out Lookup
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		l, err := s.load(ctx, tx, groupID, lookupID)
		out = view(l)
		return err
	})
	return out, err
}

func (s *Service) load(ctx context.Context, tx *store.Tx, groupID, lookupID string) (store.TocLookup, error) {
	l, err := tx.TocLookup(ctx, lookupID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (l.GroupID != groupID || !s.now().Before(l.ExpiresAt))) {
		return l, apperr.NotFoundErr()
	}
	return l, err
}

// SubmitImages は needs_image のときだけ目次ページの画像を受け付け、読み取りを開始する。
// 画像は LLM への送信にだけ使い、処理が終われば破棄する。
func (s *Service) SubmitImages(ctx context.Context, groupID, lookupID string, images []Image, idem *store.IdemKey) (store.Response, error) {
	now := s.now()
	started := false
	res, err := s.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		l, err := s.load(ctx, tx, groupID, lookupID)
		if err != nil {
			return store.Response{}, err
		}
		if l.Status != StatusNeedsImage {
			return store.Response{}, apperr.InvalidStateErr("画像の提出を依頼している状態ではありません。")
		}
		l.Status, l.ReasonCode = StatusReadingImage, ""
		if err := tx.UpdateTocLookup(ctx, l); err != nil {
			return store.Response{}, err
		}
		started = true
		return store.Response{Status: http.StatusAccepted, Body: encode(view(l))}, nil
	})
	if err == nil && started && !res.Replayed {
		s.Async(func() { s.readImages(context.WithoutCancel(ctx), lookupID, images) })
	}
	return res, err
}

func (s *Service) readImages(ctx context.Context, lookupID string, images []Image) {
	var (
		result ReadResult
		calls  []coord.LLMCall
		err    error
	)
	if s.reader == nil {
		err = ErrNotConfigured
	} else {
		for attempt := 0; attempt < 3; attempt++ {
			var c []coord.LLMCall
			result, c, err = s.reader.Read(ctx, images)
			calls = append(calls, c...)
			if !errors.Is(err, coord.ErrTransient) {
				break
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}
	images = nil // 参照を残さない
	_ = images
	uerr := s.update(ctx, lookupID, calls, func(l *store.TocLookup) {
		switch {
		case err != nil:
			s.log.Warn("目次画像の読み取りに失敗", "lookup", lookupID, "err", err)
			l.Status, l.ReasonCode = StatusFailed, ReasonModelError
		case len(result.Entries) == 0:
			l.Status, l.ReasonCode = StatusFailed, ReasonImageUnread
		default:
			l.Status, l.Source, l.ReasonCode = StatusSucceeded, SourceImage, ""
			l.Entries, l.UnreadableCount, l.SourceURLs = encode(result.Entries), result.UnreadableCount, nil
		}
	})
	if uerr != nil {
		s.log.Error("目次の取得状態を保存できない", "lookup", lookupID, "err", uerr)
	}
}

// update は取得状態を更新し、LLM 呼び出しを記録する。
func (s *Service) update(ctx context.Context, lookupID string, calls []coord.LLMCall, fn func(l *store.TocLookup)) error {
	return s.st.Tx(ctx, func(tx *store.Tx) error {
		now := s.now()
		for _, c := range calls {
			if err := tx.AddLLMCall(ctx, store.LLMCall{LookupID: lookupID, Model: c.Model, InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
				Currency: c.Currency, EstimatedAmount: c.EstimatedAmount, BilledAmount: c.BilledAmount, Succeeded: c.Succeeded, CreatedAt: now}); err != nil {
				return err
			}
		}
		l, err := tx.TocLookup(ctx, lookupID)
		if err != nil {
			return err
		}
		fn(&l)
		return tx.UpdateTocLookup(ctx, l)
	})
}

// Recover は画像の読み取り中に停止したものを失敗にする（画像は保存していないため再開できない）。
func (s *Service) Recover(ctx context.Context) error {
	return s.st.Tx(ctx, func(tx *store.Tx) error { return tx.FailInterruptedImageReads(ctx) })
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run は処理待ちの取得を進め続ける。
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.ProcessDue(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("目次の取得処理に失敗", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// ProcessDue は処理待ちの取得を1段階ずつ進め、進めた件数を返す。
func (s *Service) ProcessDue(ctx context.Context) (int, error) {
	n := 0
	for i := 0; i < 50; i++ {
		var due []store.TocLookup
		if err := s.st.Tx(ctx, func(tx *store.Tx) error {
			var err error
			due, err = tx.DueTocLookups(ctx, s.now(), 10)
			return err
		}); err != nil {
			return n, err
		}
		if len(due) == 0 {
			return n, nil
		}
		for _, l := range due {
			if err := s.step(ctx, l); err != nil {
				return n, fmt.Errorf("目次の取得 %s: %w", l.ID, err)
			}
			n++
		}
	}
	return n, nil
}

// retryOrFail は一時的な障害を指数バックオフで再試行し、作成から10分を過ぎたら fail を適用する。
func (s *Service) retryOrFail(l *store.TocLookup, fail func(l *store.TocLookup)) {
	delay := 5 * time.Second << min(l.RetryCount, 6)
	next := s.now().Add(delay)
	if next.Sub(l.CreatedAt) > RetryWindow {
		fail(l)
		return
	}
	l.RetryCount++
	l.NextRunAt = next
}

func (s *Service) step(ctx context.Context, l store.TocLookup) error {
	switch l.Status {
	case StatusResolvingBook:
		book, err := s.bib.Lookup(ctx, l.ISBN)
		return s.update(ctx, l.ID, nil, func(cur *store.TocLookup) {
			switch {
			case err != nil:
				s.log.Warn("書誌の取得に失敗", "lookup", l.ID, "err", err)
				// 書誌DBに届かない状態が続けば、Web 検索はせず画像の提出を依頼する。
				s.retryOrFail(cur, func(cur *store.TocLookup) { cur.Status, cur.ReasonCode = StatusNeedsImage, ReasonBookNotFound })
			case book == nil:
				cur.Status, cur.ReasonCode = StatusNeedsImage, ReasonBookNotFound
			default:
				cur.Book, cur.Status, cur.RetryCount, cur.NextRunAt = encode(book), StatusSearching, 0, s.now()
			}
		})
	case StatusSearching:
		return s.searchStep(ctx, l)
	case StatusVerifying:
		return s.verifyStep(ctx, l)
	}
	return nil
}

func (s *Service) searchStep(ctx context.Context, l store.TocLookup) error {
	var book Book
	_ = json.Unmarshal(l.Book, &book)
	var (
		res   SearchResult
		calls []coord.LLMCall
		err   error
	)
	switch s.faults.Get(FaultKey) {
	case ReasonTocNotFoundFault:
		res = SearchResult{Found: false}
	case ReasonSourceMismatchFault:
		// 取得元に書かれていない目次が返ってきた状況を再現する。照合で不一致になる。
		res = SearchResult{Found: true, URLs: []string{"https://example.com/fault-injection"}, Entries: []Entry{{Title: "障害注入による架空の章", Level: 1}}}
	default:
		// まず国立国会図書館サーチに登録された目次を引く。構造化データなので照合せずに使う
		if s.contents != nil {
			entries, cerr := s.contents.Contents(ctx, l.ISBN)
			switch {
			case errors.Is(cerr, coord.ErrTransient):
				s.log.Info("目次の取得元が混雑しているため再試行", "lookup", l.ID, "err", cerr)
				return s.update(ctx, l.ID, nil, func(cur *store.TocLookup) {
					s.retryOrFail(cur, func(cur *store.TocLookup) { cur.Status, cur.ReasonCode = StatusNeedsImage, ReasonTocNotFound })
				})
			case cerr != nil:
				s.log.Warn("登録済みの目次の取得に失敗", "lookup", l.ID, "err", cerr)
			case len(entries) > 0:
				return s.update(ctx, l.ID, nil, func(cur *store.TocLookup) {
					cur.Status, cur.Source, cur.ReasonCode = StatusSucceeded, SourceNDL, ""
					cur.SourceURLs, cur.Entries, cur.RetryCount = []string{NDLPageURL(l.ISBN)}, encode(entries), 0
				})
			}
		}
		if s.search == nil {
			res = SearchResult{Found: false}
		} else {
			res, calls, err = s.search.Search(ctx, book)
		}
	}
	var used int
	_ = s.st.Tx(ctx, func(tx *store.Tx) error { used, _ = countLookupCalls(ctx, tx, l.ID); return nil })
	return s.update(ctx, l.ID, calls, func(cur *store.TocLookup) {
		switch {
		case errors.Is(err, coord.ErrTransient):
			if used+len(calls) >= maxLLMPerLookup {
				cur.Status, cur.ReasonCode = StatusFailed, ReasonBudget
				return
			}
			s.retryOrFail(cur, func(cur *store.TocLookup) { cur.Status, cur.ReasonCode = StatusFailed, ReasonModelError })
		case err != nil:
			s.log.Warn("目次の検索に失敗", "lookup", l.ID, "err", err)
			cur.Status, cur.ReasonCode = StatusFailed, ReasonModelError
		case !res.Found || !validSearchResult(res):
			cur.Status, cur.ReasonCode = StatusNeedsImage, ReasonTocNotFound
		default:
			cur.Status, cur.SourceURLs, cur.Entries, cur.RetryCount, cur.NextRunAt = StatusVerifying, res.URLs, encode(res.Entries), 0, s.now()
		}
	})
}

func countLookupCalls(ctx context.Context, tx *store.Tx, lookupID string) (int, error) {
	return tx.CountLLMCallsByLookup(ctx, lookupID)
}

func validSearchResult(r SearchResult) bool {
	if len(r.URLs) == 0 || len(r.Entries) == 0 || len(r.Entries) > MaxEntries {
		return false
	}
	for _, u := range r.URLs {
		if !isHTTPS(u) {
			return false
		}
	}
	for _, e := range r.Entries {
		if e.Level < 1 || e.Level > 3 || normalize(e.Title) == "" {
			return false
		}
	}
	return true
}

// verifyStep は取得元ページを取得し、候補の各項目名が本文に含まれるかを確かめる。9割未満なら採用しない。
func (s *Service) verifyStep(ctx context.Context, l store.TocLookup) error {
	var entries []Entry
	_ = json.Unmarshal(l.Entries, &entries)
	var pages []string
	for _, u := range l.SourceURLs {
		text, err := s.fetch.Fetch(ctx, u)
		if err != nil {
			s.log.Info("取得元ページを取得できない", "lookup", l.ID, "url", u, "err", err)
			continue
		}
		pages = append(pages, text)
	}
	ok := MatchRatio(entries, pages) >= MatchThreshold
	return s.update(ctx, l.ID, nil, func(cur *store.TocLookup) {
		if ok {
			cur.Status, cur.Source, cur.ReasonCode = StatusSucceeded, SourceWeb, ""
			return
		}
		cur.Status, cur.ReasonCode, cur.SourceURLs, cur.Entries = StatusNeedsImage, ReasonSourceMismatch, nil, json.RawMessage("[]")
	})
}
