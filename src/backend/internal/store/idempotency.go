package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrIdempotencyKeyReused は同じキーが別の操作に使われたことを示す。
var ErrIdempotencyKeyReused = errors.New("store: idempotency key reused")

// IdemKey は業務更新の再送判定に使う情報。BodyHash は正規化した本文のハッシュ。
type IdemKey struct {
	UserID   string
	Key      string
	Method   string
	Path     string
	BodyHash string
}

// Response は保存した成功応答。
type Response struct {
	Status   int
	Body     []byte
	Location string
	// Replayed は保存済みの応答を返したとき true。
	Replayed bool
}

// Idempotent は再送判定と業務更新を1つのトランザクションで行う。
// 同じ利用者・キーで同じ操作の成功記録があれば fn を実行せずに保存済みの応答を返す。
// 別の操作なら ErrIdempotencyKeyReused。fn が成功したときだけ応答を保存する（検証で拒否した更新は記録しない）。
// key が nil なら再送判定をせずに fn だけを実行する。
func (s *Store) Idempotent(ctx context.Context, key *IdemKey, now time.Time, fn func(tx *Tx) (Response, error)) (Response, error) {
	var out Response
	err := s.Tx(ctx, func(tx *Tx) error {
		if key != nil {
			var method, path, hash, location string
			var status int
			var body []byte
			err := tx.row(ctx, "SELECT method, path, body_hash, status, body, location FROM idempotency WHERE user_id = ? AND key = ?", key.UserID, key.Key).
				Scan(&method, &path, &hash, &status, &body, &location)
			switch {
			case err == nil:
				if method != key.Method || path != key.Path || hash != key.BodyHash {
					return ErrIdempotencyKeyReused
				}
				out = Response{Status: status, Body: body, Location: location, Replayed: true}
				return nil
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		res, err := fn(tx)
		if err != nil {
			return err
		}
		if key != nil {
			if err := tx.exec(ctx, "INSERT INTO idempotency (user_id, key, method, path, body_hash, status, body, location, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
				key.UserID, key.Key, key.Method, key.Path, key.BodyHash, res.Status, res.Body, res.Location, ts(now)); err != nil {
				return err
			}
		}
		out = res
		return nil
	})
	return out, err
}

// PurgeIdempotency は保存期間（最低24時間）を過ぎた再送情報を削除する。
func (s *Store) PurgeIdempotency(ctx context.Context, before time.Time) error {
	return s.Tx(ctx, func(tx *Tx) error {
		return tx.exec(ctx, "DELETE FROM idempotency WHERE created_at < ?", ts(before))
	})
}
