package coord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// グループと開催回の制限（docs/data-structure.md）。
const (
	MaxNameLen     = 100
	MinGroupSize   = 2
	MaxGroupSize   = 10
	MinDuration    = 15
	MaxDuration    = 180
	RoleOwner      = "owner"
	RoleMember     = "member"
	sessionDraft   = "draft"
	sessionConfirm = "confirmed"
	sessionAttn    = "needs_attention"
)

var discordIDPattern = regexp.MustCompile(`^[0-9]{17,20}$`)

func validName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	return s, n >= 1 && n <= MaxNameLen
}

func groupView(members []store.Member, g store.Group, current store.Member) apitypes.Group {
	out := apitypes.Group{ID: g.ID, Name: g.Name, CurrentMemberID: current.ID, Members: []apitypes.Member{}}
	for _, m := range members {
		out.Members = append(out.Members, memberView(m))
	}
	return out
}

func memberView(m store.Member) apitypes.Member {
	return apitypes.Member{ID: m.ID, DisplayName: m.DisplayName, Role: m.Role, Joined: m.Joined(), Left: m.Left()}
}

// CreateGroup は固定メンバーでグループを作成する。作成者は owner になる。
func (c *Coordinator) CreateGroup(ctx context.Context, userID string, in apitypes.CreateGroupInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	return c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		user, err := tx.User(ctx, userID)
		if err != nil {
			return store.Response{}, err
		}
		var fields []apperr.Field
		name, ok := validName(in.Name)
		if !ok {
			fields = append(fields, apperr.Field{Path: "name", Message: fmt.Sprintf("1〜%d文字で入力してください", MaxNameLen)})
		}
		if n := len(in.Invitees); n < MinGroupSize-1 || n > MaxGroupSize-1 {
			fields = append(fields, apperr.Field{Path: "invitees", Message: fmt.Sprintf("招待するメンバーは%d〜%d人にしてください", MinGroupSize-1, MaxGroupSize-1)})
		}
		seen := map[string]bool{}
		for i := range in.Invitees {
			inv := &in.Invitees[i]
			p := fmt.Sprintf("invitees[%d]", i)
			switch {
			case !discordIDPattern.MatchString(inv.DiscordUserID):
				fields = append(fields, apperr.Field{Path: p + ".discord_user_id", Message: "DiscordユーザーIDは17〜20桁の数字で入力してください"})
			case inv.DiscordUserID == user.DiscordUserID:
				fields = append(fields, apperr.Field{Path: p + ".discord_user_id", Message: "自分自身は招待できません"})
			case seen[inv.DiscordUserID]:
				fields = append(fields, apperr.Field{Path: p + ".discord_user_id", Message: "同じIDが重複しています"})
			}
			seen[inv.DiscordUserID] = true
			var ok bool
			if inv.DisplayName, ok = validName(inv.DisplayName); !ok {
				fields = append(fields, apperr.Field{Path: p + ".display_name", Message: fmt.Sprintf("1〜%d文字で入力してください", MaxNameLen)})
			}
		}
		if len(fields) > 0 {
			return store.Response{}, apperr.Validation(fields...)
		}

		g := store.Group{ID: store.NewID("grp"), Name: name, OwnerUserID: user.ID, CreatedAt: now}
		if err := tx.CreateGroup(ctx, g); err != nil {
			return store.Response{}, err
		}
		owner := store.Member{ID: store.NewID("mem"), GroupID: g.ID, DiscordUserID: user.DiscordUserID, UserID: user.ID, DisplayName: user.DisplayName, Role: RoleOwner}
		if err := tx.AddMember(ctx, owner, 0); err != nil {
			return store.Response{}, err
		}
		for i, inv := range in.Invitees {
			m := store.Member{ID: store.NewID("mem"), GroupID: g.ID, DiscordUserID: inv.DiscordUserID, DisplayName: inv.DisplayName, Role: RoleMember}
			// 既にこのアプリで認証済みの利用者なら、作成時点で所属を有効にし本人の表示名を使う。
			if u, err := tx.UserByDiscordID(ctx, inv.DiscordUserID); err == nil {
				m.UserID, m.DisplayName = u.ID, u.DisplayName
			} else if !errors.Is(err, store.ErrNotFound) {
				return store.Response{}, err
			}
			if err := tx.AddMember(ctx, m, i+1); err != nil {
				return store.Response{}, err
			}
		}
		members, err := tx.Members(ctx, g.ID)
		if err != nil {
			return store.Response{}, err
		}
		return store.Response{Status: http.StatusCreated, Body: encode(groupView(members, g, owner)), Location: "/api/groups/" + g.ID}, nil
	})
}

// ListGroups は本人の所属グループを作成日時の降順で返す。
func (c *Coordinator) ListGroups(ctx context.Context, userID string) (apitypes.GroupList, error) {
	out := apitypes.GroupList{Items: []apitypes.GroupListItem{}}
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		groups, err := tx.GroupsForUser(ctx, userID)
		for _, g := range groups {
			out.Items = append(out.Items, apitypes.GroupListItem{ID: g.ID, Name: g.Name, CurrentMemberID: g.MemberID, Role: g.Role, MemberCount: g.MemberCount})
		}
		return err
	})
	return out, err
}

// GetGroup はメンバー一覧と自分の役割を返す。招待先の Discord ID は返さない。
func (c *Coordinator) GetGroup(ctx context.Context, userID, groupID string) (apitypes.Group, error) {
	var out apitypes.Group
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		g, m, err := c.groupAccess(ctx, tx, userID, groupID)
		if err != nil {
			return err
		}
		members, err := tx.Members(ctx, groupID)
		out = groupView(members, g, m)
		return err
	})
	return out, err
}

func summaryView(s store.Session) apitypes.SessionSummary {
	return apitypes.SessionSummary{
		ID: s.ID, GroupID: s.GroupID, PlaybookID: s.PlaybookID, Title: s.Title, StartsAt: s.StartsAt,
		DurationMinutes: s.DurationMinutes, Revision: s.Revision, Status: s.Status, UpdatedAt: s.UpdatedAt,
	}
}

// ListSessions は開催回を starts_at の降順で返す。
func (c *Coordinator) ListSessions(ctx context.Context, userID, groupID string) (apitypes.SessionList, error) {
	out := apitypes.SessionList{Items: []apitypes.SessionSummary{}}
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		if _, _, err := c.groupAccess(ctx, tx, userID, groupID); err != nil {
			return err
		}
		list, err := tx.SessionsByGroup(ctx, groupID)
		for _, s := range list {
			out.Items = append(out.Items, summaryView(s))
		}
		return err
	})
	return out, err
}

// CreateSession は開催回を登録し、全員への参加条件の確認を開始する。
func (c *Coordinator) CreateSession(ctx context.Context, userID, groupID string, in apitypes.CreateSessionInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		g, owner, err := c.groupAccess(ctx, tx, userID, groupID)
		if err != nil {
			return store.Response{}, err
		}
		if owner.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		members, err := tx.Members(ctx, g.ID)
		if err != nil {
			return store.Response{}, err
		}
		var notJoined []string
		for _, m := range members {
			if !m.Joined() {
				notJoined = append(notJoined, m.ID)
			}
		}
		if len(notJoined) > 0 {
			return store.Response{}, apperr.New(apperr.MembersNotJoined, "まだログインしていないメンバーがいます。").With("member_ids", notJoined)
		}
		pb, err := c.playbook(in.PlaybookID)
		if err != nil {
			return store.Response{}, apperr.New(apperr.UnsupportedPlaybook, "対応していない用途です。")
		}

		var fields []apperr.Field
		title, ok := validName(in.Title)
		if !ok {
			fields = append(fields, apperr.Field{Path: "title", Message: fmt.Sprintf("1〜%d文字で入力してください", MaxNameLen)})
		}
		startsAt, err := parseOffsetTime(in.StartsAt)
		if err != nil {
			fields = append(fields, apperr.Field{Path: "starts_at", Message: "タイムゾーン付きの日時（RFC 3339）で入力してください"})
		} else if _, secured := dueAt(now, startsAt); !startsAt.After(now.Add(SessionMinLead)) || !secured {
			fields = append(fields, apperr.Field{Path: "starts_at", Message: "回答期限を確保できるよう、1時間半以上先の日時を指定してください"})
		}
		if in.DurationMinutes < MinDuration || in.DurationMinutes > MaxDuration {
			fields = append(fields, apperr.Field{Path: "duration_minutes", Message: fmt.Sprintf("%d〜%d分で指定してください", MinDuration, MaxDuration)})
		}
		var data []byte
		if len(fields) == 0 {
			data, err = pb.ValidateSessionData(ctx, SessionParams{DurationMinutes: in.DurationMinutes}, in.Data)
			if err := validationErr(err, "data"); err != nil {
				return store.Response{}, err
			}
		} else if _, err := pb.ValidateSessionData(ctx, SessionParams{DurationMinutes: in.DurationMinutes}, in.Data); err != nil {
			if v := new(ValidationError); errors.As(err, &v) {
				for _, f := range v.Prefixed("data").Fields {
					fields = append(fields, apperr.Field{Path: f.Path, Message: f.Message})
				}
			}
		}
		if len(fields) > 0 {
			return store.Response{}, apperr.Validation(fields...)
		}

		sess := store.Session{
			ID: store.NewID("ses"), GroupID: g.ID, PlaybookID: in.PlaybookID, Title: title, StartsAt: startsAt.UTC(),
			DurationMinutes: in.DurationMinutes, Revision: 1, Status: sessionDraft, Data: data, CreatedAt: now, UpdatedAt: now,
		}
		var ids []string
		for _, m := range members {
			ids = append(ids, m.ID)
		}
		if err := tx.CreateSession(ctx, sess, ids); err != nil {
			return store.Response{}, err
		}
		cs := store.Case{ID: store.NewID("case"), SessionID: sess.ID, Status: store.CaseCollecting, Summary: "参加条件を確認しています。", CreatedAt: now, UpdatedAt: now}
		if err := tx.CreateCase(ctx, cs); err != nil {
			return store.Response{}, err
		}
		due, _ := dueAt(now, sess.StartsAt)
		for _, m := range members {
			if err := c.createTask(ctx, tx, sess, cs, m, store.TaskPreparation, "今回の準備状況を教えてください。", nil, "system", due, now); err != nil {
				return store.Response{}, err
			}
		}
		if err := c.activity(ctx, tx, sess, cs.ID, "input_received", "開催回が登録され、参加条件の確認を始めました。", "", now); err != nil {
			return store.Response{}, err
		}
		return store.Response{
			Status:   http.StatusCreated,
			Body:     encode(apitypes.SessionCreated{Session: summaryView(sess), CaseID: cs.ID}),
			Location: "/api/sessions/" + sess.ID,
		}, nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// parseOffsetTime は明示的なオフセット（Z を含む）付きの RFC 3339 日時だけを受け付ける。
func parseOffsetTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// validationErr は Playbook の検証エラーを本文内のパス付きの validation_failed に変換する。
func validationErr(err error, prefix string) error {
	if err == nil {
		return nil
	}
	var v *ValidationError
	if errors.As(err, &v) {
		var fields []apperr.Field
		for _, f := range v.Prefixed(prefix).Fields {
			fields = append(fields, apperr.Field{Path: f.Path, Message: f.Message})
		}
		return apperr.Validation(fields...)
	}
	return err
}
