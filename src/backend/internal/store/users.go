package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type User struct {
	ID            string
	DiscordUserID string
	DisplayName   string
	CreatedAt     time.Time
}

// UpsertUser は Discord の利用者を登録し、表示名を最新にする。
func (t *Tx) UpsertUser(ctx context.Context, discordUserID, displayName string, now time.Time) (User, error) {
	u, err := t.UserByDiscordID(ctx, discordUserID)
	switch {
	case err == nil:
		if u.DisplayName != displayName {
			if err := t.exec(ctx, "UPDATE users SET display_name = ? WHERE id = ?", displayName, u.ID); err != nil {
				return User{}, err
			}
			u.DisplayName = displayName
		}
		return u, nil
	case errors.Is(err, ErrNotFound):
		u = User{ID: NewID("usr"), DiscordUserID: discordUserID, DisplayName: displayName, CreatedAt: now}
		err := t.exec(ctx, "INSERT INTO users (id, discord_user_id, display_name, created_at) VALUES (?, ?, ?, ?)",
			u.ID, u.DiscordUserID, u.DisplayName, ts(now))
		return u, err
	default:
		return User{}, err
	}
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created string
	if err := row.Scan(&u.ID, &u.DiscordUserID, &u.DisplayName, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.CreatedAt = parseTS(created)
	return u, nil
}

func (t *Tx) UserByDiscordID(ctx context.Context, discordUserID string) (User, error) {
	return scanUser(t.row(ctx, "SELECT id, discord_user_id, display_name, created_at FROM users WHERE discord_user_id = ?", discordUserID))
}

func (t *Tx) User(ctx context.Context, id string) (User, error) {
	return scanUser(t.row(ctx, "SELECT id, discord_user_id, display_name, created_at FROM users WHERE id = ?", id))
}

// ActivateMemberships は招待中の所属を本人のログインで有効にし、表示名を本人のものにする。
func (t *Tx) ActivateMemberships(ctx context.Context, u User) error {
	return t.exec(ctx, "UPDATE members SET user_id = ?, display_name = ? WHERE discord_user_id = ? AND left_at IS NULL AND EXISTS (SELECT 1 FROM groups g WHERE g.id = members.group_id AND g.deleted_at IS NULL)", u.ID, u.DisplayName, u.DiscordUserID)
}

type AuthSession struct {
	TokenHash string
	UserID    string
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (t *Tx) CreateAuthSession(ctx context.Context, s AuthSession) error {
	return t.exec(ctx, "INSERT INTO auth_sessions (token_hash, user_id, csrf_token, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		s.TokenHash, s.UserID, s.CSRFToken, ts(s.CreatedAt), ts(s.ExpiresAt))
}

func (t *Tx) AuthSession(ctx context.Context, tokenHash string) (AuthSession, error) {
	var s AuthSession
	var created, expires string
	err := t.row(ctx, "SELECT token_hash, user_id, csrf_token, created_at, expires_at FROM auth_sessions WHERE token_hash = ?", tokenHash).
		Scan(&s.TokenHash, &s.UserID, &s.CSRFToken, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	s.CreatedAt, s.ExpiresAt = parseTS(created), parseTS(expires)
	return s, nil
}

func (t *Tx) DeleteAuthSession(ctx context.Context, tokenHash string) error {
	return t.exec(ctx, "DELETE FROM auth_sessions WHERE token_hash = ?", tokenHash)
}

func (t *Tx) CreateOAuthState(ctx context.Context, state, returnTo string, expires time.Time) error {
	return t.exec(ctx, "INSERT INTO oauth_states (state, return_to, expires_at) VALUES (?, ?, ?)", state, returnTo, ts(expires))
}

// ConsumeOAuthState は state を1回だけ使えるように削除して戻り先を返す。期限切れなら ErrNotFound。
func (t *Tx) ConsumeOAuthState(ctx context.Context, state string, now time.Time) (string, error) {
	var returnTo, expires string
	err := t.row(ctx, "SELECT return_to, expires_at FROM oauth_states WHERE state = ?", state).Scan(&returnTo, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if err := t.exec(ctx, "DELETE FROM oauth_states WHERE state = ? OR expires_at < ?", state, ts(now)); err != nil {
		return "", err
	}
	if !now.Before(parseTS(expires)) {
		return "", ErrNotFound
	}
	return returnTo, nil
}
