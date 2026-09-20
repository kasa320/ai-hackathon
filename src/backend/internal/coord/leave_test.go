package coord_test

import (
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func (h *harness) leave(name string) (apitypes.LeaveGroupResult, error) {
	h.t.Helper()
	res, err := h.c.LeaveGroup(ctx, h.users[name], h.group, nil)
	var out apitypes.LeaveGroupResult
	if err == nil {
		decode(h.t, res.Body, &out)
	}
	return out, err
}

func (h *harness) mustLeave(name string) apitypes.LeaveGroupResult {
	h.t.Helper()
	out, err := h.leave(name)
	if err != nil {
		h.t.Fatalf("%s の脱退: %v", name, err)
	}
	return out
}

// errCode は公開用のエラーコードを返す。
func errCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	ae := httpx.ToAppError(err)
	if ae == nil {
		t.Fatalf("公開エラーではありません: %v", err)
	}
	return ae.Code
}

// 脱退すると、そのグループも開催回も本人からは見えなくなる。
func TestLeaveGroupRemovesAccess(t *testing.T) {
	h := newHarness(t, nil)
	h.confirmInitial()

	out := h.mustLeave("D")
	if len(out.AffectedSessionIDs) != 1 || out.AffectedSessionIDs[0] != h.sess {
		t.Errorf("再調整された開催回が %v（期待 [%s]）", out.AffectedSessionIDs, h.sess)
	}

	if _, err := h.c.GetGroup(ctx, h.users["D"], h.group); errCode(t, err) != apperr.NotFound {
		t.Errorf("脱退後もグループが見えています")
	}
	if _, err := h.c.SessionDetail(ctx, h.users["D"], h.sess); errCode(t, err) != apperr.NotFound {
		t.Errorf("脱退後も開催回が見えています")
	}
	list, err := h.c.ListGroups(ctx, h.users["D"])
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("脱退したグループが一覧に残っています: %+v", list.Items)
	}
}

// 在籍者の一覧からは消えるが、開催回に固定された顔ぶれと履歴は残る。
func TestLeaveGroupKeepsSessionHistory(t *testing.T) {
	h := newHarness(t, nil)
	h.confirmInitial()
	leftID := h.memberID("D")
	h.mustLeave("D")

	g, err := h.c.GetGroup(ctx, h.users["A"], h.group)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range g.Members {
		if m.ID == leftID {
			t.Errorf("脱退した D がグループのメンバー一覧に残っています")
		}
	}
	if len(g.Members) != 3 {
		t.Errorf("在籍者が %d 人（期待 3）", len(g.Members))
	}

	// 開催回は固定した時点の顔ぶれを保つ。
	d := h.detail("A")
	found := false
	for _, m := range d.Members {
		if m.ID == leftID {
			found = true
		}
	}
	if !found {
		t.Error("開催回の参加対象者から D が消えています。固定した顔ぶれは残す")
	}
	// 本人は欠席として記録され、以降の引き受け・投票の対象から外れる。
	for _, p := range d.Preparations {
		if p.MemberID == leftID {
			if p.Value == nil || p.Value.Attendance != "absent" {
				t.Errorf("D の参加条件が %+v（期待 absent）", p.Value)
			}
		}
	}
}

// 担当者が脱退すると、確定していた計画が崩れて再調整が始まる。
func TestLeaveGroupByPresenterStartsReplan(t *testing.T) {
	h := newHarness(t, nil)
	h.confirmInitial()
	if d := h.detail("A"); d.Session.Status != "confirmed" {
		t.Fatalf("前提が崩れています: %s", d.Session.Status)
	}

	h.mustLeave("B") // B は初回案の担当者

	d := h.detail("A")
	if d.Session.Status == "confirmed" {
		t.Error("担当者が脱退したのに確定のままです")
	}
	if d.ActiveCase == nil {
		t.Fatal("再調整の案件が開いていません")
	}
	// 脱退した本人は、この案件での確認依頼の対象にしない。
	var cs store.Case
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		cs, err = tx.LatestCase(ctx, h.sess)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	leftID := h.memberID("B")
	found := false
	for _, id := range cs.WithdrawnMemberIDs {
		if id == leftID {
			found = true
		}
	}
	if !found {
		t.Errorf("脱退した B が withdrawn_member_ids に入っていません: %v", cs.WithdrawnMemberIDs)
	}
}

// 未回答の依頼は、もう答えようがないので閉じる。
func TestLeaveGroupClosesOpenTasks(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession() // 全員に参加条件の確認依頼が出る

	if h.openTask("D", "preparation") == nil {
		t.Fatal("前提：D に未回答の依頼がない")
	}
	leftID := h.memberID("D")
	h.mustLeave("D")

	var open int
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		cs, err := tx.LatestCase(ctx, h.sess)
		if err != nil {
			return err
		}
		tasks, err := tx.TasksByCase(ctx, cs.ID)
		if err != nil {
			return err
		}
		for _, tk := range tasks {
			if tk.MemberID == leftID && tk.Status == store.TaskOpen {
				open++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("脱退した D に open な依頼が %d 件残っています", open)
	}
}

// 管理者は脱退できない。先に交代が必要。
func TestOwnerCannotLeaveGroup(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.leave("A")
	if code := errCode(t, err); code != apperr.InvalidState {
		t.Errorf("エラーコードが %s（期待 %s）", code, apperr.InvalidState)
	}
	g, err := h.c.GetGroup(ctx, h.users["A"], h.group)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Members) != 4 {
		t.Errorf("在籍者が %d 人（期待 4）。失敗した脱退が反映されています", len(g.Members))
	}
}

// 最低人数を下回る脱退は断る。
func TestLeaveGroupKeepsMinimumSize(t *testing.T) {
	h := newHarness(t, nil) // A（管理者）・B・C・D
	h.mustLeave("D")        // 3人
	h.mustLeave("C")        // 2人

	_, err := h.leave("B")
	if code := errCode(t, err); code != apperr.InvalidState {
		t.Errorf("エラーコードが %s（期待 %s）", code, apperr.InvalidState)
	}
	g, err := h.c.GetGroup(ctx, h.users["A"], h.group)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Members) != 2 {
		t.Errorf("在籍者が %d 人（期待 2）", len(g.Members))
	}
}

// 二重の脱退要求は、本人がもう所属していないので 404 になる。
func TestLeaveGroupTwiceIsNotFound(t *testing.T) {
	h := newHarness(t, nil)
	h.mustLeave("D")
	_, err := h.leave("D")
	if code := errCode(t, err); code != apperr.NotFound {
		t.Errorf("エラーコードが %s（期待 %s）", code, apperr.NotFound)
	}
}

// 脱退後にログインし直しても、元のグループには戻らない。
func TestLeaveGroupSurvivesRelogin(t *testing.T) {
	h := newHarness(t, nil)
	h.mustLeave("D")

	// ログインのたびに呼ばれる所属の有効化を、脱退後に実行する。
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		u, err := tx.User(ctx, h.users["D"])
		if err != nil {
			return err
		}
		return tx.ActivateMemberships(ctx, u)
	}); err != nil {
		t.Fatal(err)
	}

	list, err := h.c.ListGroups(ctx, h.users["D"])
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("ログインし直しただけで復帰しています: %+v", list.Items)
	}
}
