// Package store は SQLite へのアクセスを担う。
// 業務更新は Store.Tx の中でまとめて行い、同じトランザクションで再送情報・イベント・通知待ちを保存する。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound は対象の行がないことを示す。
var ErrNotFound = errors.New("store: not found")

type Store struct {
	db *sql.DB
}

// Open は DB ファイルを開き、スキーマを適用する。
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("DB ディレクトリの作成: %w", err)
	}
	return open(ctx, "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
}

func open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("DB を開く: %w", err)
	}
	// SQLite への接続は 1 本に絞り、書き込みを直列化する（同じ再送キーの同時到着も1回分として処理される）。
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("スキーマの適用: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Tx は DB トランザクション。業務データのアクセスはすべてこの型のメソッドで行う。
type Tx struct {
	tx *sql.Tx
}

// Tx は fn を1つのトランザクションで実行する。fn がエラーを返すとロールバックする。
// 接続は1本なので、fn の中から Store.Tx を呼ぶと待ち続ける。外部呼び出し（LLM・通知）は fn の外で行う。
func (s *Store) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("トランザクション開始: %w", err)
	}
	if err := fn(&Tx{tx: sqlTx}); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("コミット: %w", err)
	}
	return nil
}

func (t *Tx) exec(ctx context.Context, q string, args ...any) error {
	_, err := t.tx.ExecContext(ctx, q, args...)
	return err
}

func (t *Tx) execN(ctx context.Context, q string, args ...any) (int64, error) {
	res, err := t.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (t *Tx) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, args...)
}

func (t *Tx) row(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, q, args...)
}

// nextSeq は表内の順序を固定するための連番を返す。
func (t *Tx) nextSeq(ctx context.Context, table string) (int64, error) {
	var n int64
	err := t.row(ctx, "SELECT COALESCE(MAX(seq), 0) + 1 FROM "+table).Scan(&n)
	return n, err
}

// Reset はすべての業務データを削除する（開発用の初期データ投入で使う）。
func (t *Tx) Reset(ctx context.Context) error {
	for _, table := range []string{
		"idempotency", "llm_calls", "activity", "notifications", "events", "tasks", "proposals", "cases",
		"preparations", "session_members", "sessions", "reading_toc_lookups", "members", "groups",
		"auth_sessions", "oauth_states", "users",
	} {
		if err := t.exec(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("%s の削除: %w", table, err)
		}
	}
	return nil
}

// NewID は接頭辞付きのランダムな ID を返す。形式はフロントで解釈しない。
func NewID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// RandomToken は秘密の値（セッション・CSRF・OAuth state）に使うランダムな文字列を返す。
func RandomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func ts(t time.Time) string { return t.UTC().Format(timeLayout) }

func nullTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(fmt.Sprintf("store: 日時を読めません %q: %v", s, err))
	}
	return t.UTC()
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
