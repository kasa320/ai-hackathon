package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type ReadingBook struct {
	ID, GroupID, Title          string
	ISBN                        *string
	TocSource, Sections         json.RawMessage
	PlannedSessionCount         int
	SessionCreationMode, Status string
	CompletedSectionIDs         []string
	CreatedAt, UpdatedAt        time.Time
}
type ReadingBookSlot struct {
	ID, BookID        string
	SequenceNumber    int
	Status            string
	SessionID         *string
	CoveredSectionIDs []string
}

const bookCols = "id, group_id, title, isbn, toc_source, sections, planned_session_count, session_creation_mode, status, completed_section_ids, created_at, updated_at"

func scanBook(r interface{ Scan(...any) error }) (b ReadingBook, err error) {
	var isbn sql.NullString
	var toc, secs, done, created, updated string
	err = r.Scan(&b.ID, &b.GroupID, &b.Title, &isbn, &toc, &secs, &b.PlannedSessionCount, &b.SessionCreationMode, &b.Status, &done, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err == nil {
		if isbn.Valid {
			b.ISBN = &isbn.String
		}
		b.TocSource = json.RawMessage(toc)
		b.Sections = json.RawMessage(secs)
		_ = json.Unmarshal([]byte(done), &b.CompletedSectionIDs)
		b.CreatedAt, b.UpdatedAt = parseTS(created), parseTS(updated)
	}
	return
}
func (t *Tx) CreateReadingBook(ctx context.Context, b ReadingBook) error {
	done, _ := json.Marshal(nonNilStrings(b.CompletedSectionIDs))
	return t.exec(ctx, "INSERT INTO reading_books ("+bookCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", b.ID, b.GroupID, b.Title, nullStrPtr(b.ISBN), string(b.TocSource), string(b.Sections), b.PlannedSessionCount, b.SessionCreationMode, b.Status, string(done), ts(b.CreatedAt), ts(b.UpdatedAt))
}
func nullStrPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
func (t *Tx) ReadingBooks(ctx context.Context, groupID string) ([]ReadingBook, error) {
	rows, e := t.query(ctx, "SELECT "+bookCols+" FROM reading_books WHERE group_id=? ORDER BY created_at DESC", groupID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ReadingBook
	for rows.Next() {
		b, e := scanBook(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (t *Tx) ReadingBook(ctx context.Context, id string) (ReadingBook, error) {
	return scanBook(t.row(ctx, "SELECT "+bookCols+" FROM reading_books WHERE id=? AND EXISTS (SELECT 1 FROM groups g WHERE g.id=reading_books.group_id AND g.deleted_at IS NULL)", id))
}
func (t *Tx) UpdateReadingBook(ctx context.Context, b ReadingBook) error {
	done, _ := json.Marshal(nonNilStrings(b.CompletedSectionIDs))
	return t.exec(ctx, "UPDATE reading_books SET status=?, completed_section_ids=?, updated_at=? WHERE id=?", b.Status, string(done), ts(b.UpdatedAt), b.ID)
}
func (t *Tx) CreateReadingBookSlot(ctx context.Context, s ReadingBookSlot) error {
	done, _ := json.Marshal(nonNilStrings(s.CoveredSectionIDs))
	return t.exec(ctx, "INSERT INTO reading_book_slots (id,book_id,sequence_number,status,session_id,covered_section_ids) VALUES (?, ?, ?, ?, ?, ?)", s.ID, s.BookID, s.SequenceNumber, s.Status, nullStrPtr(s.SessionID), string(done))
}
func scanSlot(r interface{ Scan(...any) error }) (s ReadingBookSlot, e error) {
	var sid sql.NullString
	var covered string
	e = r.Scan(&s.ID, &s.BookID, &s.SequenceNumber, &s.Status, &sid, &covered)
	if errors.Is(e, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if e == nil {
		if sid.Valid {
			s.SessionID = &sid.String
		}
		_ = json.Unmarshal([]byte(covered), &s.CoveredSectionIDs)
	}
	return
}
func (t *Tx) ReadingBookSlots(ctx context.Context, bookID string) ([]ReadingBookSlot, error) {
	rows, e := t.query(ctx, "SELECT id,book_id,sequence_number,status,session_id,covered_section_ids FROM reading_book_slots WHERE book_id=? ORDER BY sequence_number", bookID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ReadingBookSlot
	for rows.Next() {
		s, e := scanSlot(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (t *Tx) ReadingBookSlot(ctx context.Context, id string) (ReadingBookSlot, error) {
	return scanSlot(t.row(ctx, "SELECT id,book_id,sequence_number,status,session_id,covered_section_ids FROM reading_book_slots WHERE id=?", id))
}
func (t *Tx) StartReadingBookSlot(ctx context.Context, id, sessionID string) (bool, error) {
	n, err := t.execN(ctx, "UPDATE reading_book_slots SET status='active', session_id=? WHERE id=? AND status='planned'", sessionID, id)
	return n == 1, err
}
func (t *Tx) CompleteReadingBookSlot(ctx context.Context, id string, covered []string) (bool, error) {
	v, _ := json.Marshal(nonNilStrings(covered))
	n, err := t.execN(ctx, "UPDATE reading_book_slots SET status='completed', covered_section_ids=? WHERE id=? AND status='active'", string(v), id)
	return n == 1, err
}
