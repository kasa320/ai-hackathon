package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// 輪読固有の保存処理。共通の案件・同意の処理とは分ける。

// TocLookup は目次取得の状態。Book・Entries の形は輪読側が決める。画像は保存しない。
type TocLookup struct {
	ID              string
	GroupID         string
	ISBN            string
	Status          string
	Book            json.RawMessage // null 可
	Source          string
	SourceURLs      []string
	Entries         json.RawMessage
	UnreadableCount int
	ReasonCode      string
	RetryCount      int
	NextRunAt       time.Time
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

const tocCols = "id, group_id, isbn, status, book, source, source_urls, entries, unreadable_count, reason_code, retry_count, next_run_at, created_at, expires_at"

func scanToc(row interface{ Scan(...any) error }) (TocLookup, error) {
	var l TocLookup
	var book, source, reason sql.NullString
	var urls, entries, next, created, expires string
	if err := row.Scan(&l.ID, &l.GroupID, &l.ISBN, &l.Status, &book, &source, &urls, &entries, &l.UnreadableCount, &reason, &l.RetryCount, &next, &created, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return l, ErrNotFound
		}
		return l, err
	}
	if book.Valid {
		l.Book = json.RawMessage(book.String)
	}
	l.Source, l.ReasonCode = source.String, reason.String
	_ = json.Unmarshal([]byte(urls), &l.SourceURLs)
	l.Entries = json.RawMessage(entries)
	l.NextRunAt, l.CreatedAt, l.ExpiresAt = parseTS(next), parseTS(created), parseTS(expires)
	return l, nil
}

func tocArgs(l TocLookup) []any {
	var book any
	if len(l.Book) > 0 && string(l.Book) != "null" {
		book = string(l.Book)
	}
	urls, _ := json.Marshal(nonNilStrings(l.SourceURLs))
	entries := string(l.Entries)
	if entries == "" {
		entries = "[]"
	}
	return []any{l.GroupID, l.ISBN, l.Status, book, nullStr(l.Source), string(urls), entries, l.UnreadableCount, nullStr(l.ReasonCode), l.RetryCount, ts(l.NextRunAt), ts(l.CreatedAt), ts(l.ExpiresAt)}
}

func (t *Tx) CreateTocLookup(ctx context.Context, l TocLookup) error {
	return t.exec(ctx, "INSERT INTO reading_toc_lookups ("+tocCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", append([]any{l.ID}, tocArgs(l)...)...)
}

func (t *Tx) UpdateTocLookup(ctx context.Context, l TocLookup) error {
	return t.exec(ctx, `UPDATE reading_toc_lookups SET group_id = ?, isbn = ?, status = ?, book = ?, source = ?, source_urls = ?, entries = ?,
		unreadable_count = ?, reason_code = ?, retry_count = ?, next_run_at = ?, created_at = ?, expires_at = ?
		WHERE id = ? AND EXISTS (SELECT 1 FROM groups g WHERE g.id = reading_toc_lookups.group_id AND g.deleted_at IS NULL)`, append(tocArgs(l), l.ID)...)
}

func (t *Tx) TocLookup(ctx context.Context, id string) (TocLookup, error) {
	return scanToc(t.row(ctx, `SELECT `+tocCols+` FROM reading_toc_lookups WHERE id = ?
		AND EXISTS (SELECT 1 FROM groups g WHERE g.id = reading_toc_lookups.group_id AND g.deleted_at IS NULL)`, id))
}

// CountTocLookupsSince はグループが since 以降に開始した取得の数を返す（1日の上限の判定用）。
func (t *Tx) CountTocLookupsSince(ctx context.Context, groupID string, since time.Time) (int, error) {
	var n int
	err := t.row(ctx, "SELECT COUNT(*) FROM reading_toc_lookups WHERE group_id = ? AND created_at >= ?", groupID, ts(since)).Scan(&n)
	return n, err
}

// DueTocLookups は処理待ち（書誌の確定・検索・照合）で実行時刻を過ぎたものを返す。
func (t *Tx) DueTocLookups(ctx context.Context, now time.Time, limit int) ([]TocLookup, error) {
	rows, err := t.query(ctx, `SELECT `+tocCols+` FROM reading_toc_lookups
		WHERE status IN ('resolving_book', 'searching', 'verifying') AND next_run_at <= ?
		AND EXISTS (SELECT 1 FROM groups g WHERE g.id = reading_toc_lookups.group_id AND g.deleted_at IS NULL)
		ORDER BY next_run_at LIMIT ?`, ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TocLookup
	for rows.Next() {
		l, err := scanToc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// FailInterruptedImageReads は画像の読み取り中に停止したものを失敗にする。画像は保存していないため再開できない。
func (t *Tx) FailInterruptedImageReads(ctx context.Context) error {
	return t.exec(ctx, "UPDATE reading_toc_lookups SET status = 'failed', reason_code = 'model_error' WHERE status = 'reading_image'")
}

// CountLLMCallsByLookup は目次の取得1件で使った LLM 呼び出しの数を返す。
func (t *Tx) CountLLMCallsByLookup(ctx context.Context, lookupID string) (int, error) {
	var n int
	err := t.row(ctx, "SELECT COUNT(*) FROM llm_calls WHERE lookup_id = ?", lookupID).Scan(&n)
	return n, err
}
