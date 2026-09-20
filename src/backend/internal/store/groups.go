package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Group struct {
	ID          string
	Name        string
	OwnerUserID string
	CreatedAt   time.Time
}

type Member struct {
	ID            string
	GroupID       string
	DiscordUserID string
	UserID        string // 空なら本人が未ログイン
	DisplayName   string
	Role          string
	LeftAt        *time.Time // nil なら在籍中
}

func (m Member) Joined() bool { return m.UserID != "" }

// Left は脱退済みかを返す。脱退しても行は消さないので、過去の開催回の表示には出続ける。
func (m Member) Left() bool { return m.LeftAt != nil }

func (t *Tx) CreateGroup(ctx context.Context, g Group) error {
	return t.exec(ctx, "INSERT INTO groups (id, name, owner_user_id, created_at) VALUES (?, ?, ?, ?)", g.ID, g.Name, g.OwnerUserID, ts(g.CreatedAt))
}

// AddMember はメンバーを追加する。seq はグループ内の表示順。
func (t *Tx) AddMember(ctx context.Context, m Member, seq int) error {
	return t.exec(ctx, "INSERT INTO members (id, group_id, discord_user_id, user_id, display_name, role, seq) VALUES (?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.GroupID, m.DiscordUserID, nullStr(m.UserID), m.DisplayName, m.Role, seq)
}

func (t *Tx) Group(ctx context.Context, id string) (Group, error) {
	var g Group
	var created string
	err := t.row(ctx, "SELECT id, name, owner_user_id, created_at FROM groups WHERE id = ?", id).Scan(&g.ID, &g.Name, &g.OwnerUserID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	g.CreatedAt = parseTS(created)
	return g, nil
}

const memberCols = "id, group_id, discord_user_id, user_id, display_name, role, left_at"

func scanMember(row interface{ Scan(...any) error }) (Member, error) {
	var m Member
	var userID, leftAt sql.NullString
	if err := row.Scan(&m.ID, &m.GroupID, &m.DiscordUserID, &userID, &m.DisplayName, &m.Role, &leftAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, ErrNotFound
		}
		return m, err
	}
	m.UserID = userID.String
	m.LeftAt = parseNullTS(leftAt)
	return m, nil
}

func scanMembers(rows *sql.Rows, err error) ([]Member, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Members はグループの在籍中のメンバーを表示順で返す。脱退した人は含めない。
func (t *Tx) Members(ctx context.Context, groupID string) ([]Member, error) {
	return scanMembers(t.query(ctx, "SELECT "+memberCols+" FROM members WHERE group_id = ? AND left_at IS NULL ORDER BY seq", groupID))
}

// MemberByUser は本人のログイン済みの所属を返す。脱退していれば ErrNotFound。
func (t *Tx) MemberByUser(ctx context.Context, groupID, userID string) (Member, error) {
	return scanMember(t.row(ctx, "SELECT "+memberCols+" FROM members WHERE group_id = ? AND user_id = ? AND left_at IS NULL", groupID, userID))
}

// Member はメンバーIDで引く。過去の開催回の表示に使うため、脱退した人も返す。
func (t *Tx) Member(ctx context.Context, id string) (Member, error) {
	return scanMember(t.row(ctx, "SELECT "+memberCols+" FROM members WHERE id = ?", id))
}

// LeaveGroup は本人を脱退済みにする。既に脱退していれば false。
// 行は消さず left_at を入れるだけなので、開催回に固定された参照は壊れない。
func (t *Tx) LeaveGroup(ctx context.Context, memberID string, now time.Time) (bool, error) {
	n, err := t.execN(ctx, "UPDATE members SET left_at = ? WHERE id = ? AND left_at IS NULL", ts(now), memberID)
	return n == 1, err
}

// UserGroup は一覧表示用の所属グループ。
type UserGroup struct {
	Group
	MemberID    string
	Role        string
	MemberCount int
}

// GroupsForUser は本人が在籍するグループを作成日時の降順で返す。脱退したグループは含めない。
func (t *Tx) GroupsForUser(ctx context.Context, userID string) ([]UserGroup, error) {
	rows, err := t.query(ctx, `
		SELECT g.id, g.name, g.owner_user_id, g.created_at, m.id, m.role,
		       (SELECT COUNT(*) FROM members x WHERE x.group_id = g.id AND x.left_at IS NULL)
		FROM members m JOIN groups g ON g.id = m.group_id
		WHERE m.user_id = ? AND m.left_at IS NULL
		ORDER BY g.created_at DESC, g.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserGroup
	for rows.Next() {
		var g UserGroup
		var created string
		if err := rows.Scan(&g.ID, &g.Name, &g.OwnerUserID, &created, &g.MemberID, &g.Role, &g.MemberCount); err != nil {
			return nil, err
		}
		g.CreatedAt = parseTS(created)
		out = append(out, g)
	}
	return out, rows.Err()
}
