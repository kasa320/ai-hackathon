package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

var jstZone = time.FixedZone("JST", 9*3600)

func jstDay(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, jstZone) }

// setNow は時計を指定時刻まで進める（戻さない）。デモ用の時計で自動進行を検証する。
func (s *server) setNow(at time.Time) {
	if d := at.Sub(s.clk.Now()); d > 0 {
		s.clk.Advance(d)
	}
}

func bookBody(title string, sections, count int) map[string]any {
	var secs []map[string]string
	for i := 1; i <= sections; i++ {
		secs = append(secs, map[string]string{"id": "sec_" + string(rune('0'+i)), "title": "第" + string(rune('0'+i)) + "章"})
	}
	return map[string]any{"title": title, "toc_source": map[string]any{}, "sections": secs, "period_start": "2026-10-01", "period_end": "2026-10-21",
		"planned_session_count": count, "duration_minutes": 60, "adjustment_lead_days": 7}
}

// readingGroup は A（管理者）・B・C・D の reading グループを API で作る。
func readingGroup(t *testing.T, s *server) (string, map[string]*client) {
	t.Helper()
	s.seed("initial_demo")
	people := s.personas()
	var invitees []map[string]string
	for _, n := range []string{"B", "C", "D"} {
		invitees = append(invitees, map[string]string{"discord_user_id": persona[n], "display_name": n})
	}
	var g apitypes.Group
	people["A"].send(http.MethodPost, "/api/groups", map[string]any{"name": "ブック輪読", "playbook_id": "reading", "invitees": invitees}).mustStatus(t, http.StatusCreated).decode(t, &g)
	for _, m := range g.Members {
		if !m.Joined {
			t.Fatalf("メンバーが参加済みでない: %s", dump(g))
		}
	}
	return g.ID, people
}

func (c *client) bookDetail(t *testing.T, groupID, bookID string) apitypes.ReadingBookDetail {
	t.Helper()
	var d apitypes.ReadingBookDetail
	c.get("/api/groups/"+groupID+"/reading/books/"+bookID).mustStatus(t, 200).decode(t, &d)
	return d
}

func (s *server) groupSessions(t *testing.T, c *client, groupID string) []apitypes.SessionSummary {
	t.Helper()
	var list apitypes.SessionList
	c.get("/api/groups/"+groupID+"/sessions").mustStatus(t, 200).decode(t, &list)
	return list.Items
}

func (s *server) dmKinds() map[string]int {
	out := map[string]int{}
	for _, m := range s.sender.all() {
		out[m.Kind]++
	}
	return out
}

func TestBookEndpointsContractAndPermissions(t *testing.T) {
	s := newServer(t)
	gid, p := readingGroup(t, s)
	base := "/api/groups/" + gid + "/reading/books"

	// 旧契約の項目は受け付けない（未知フィールドの拒否を維持する）。
	for _, extra := range []string{"initial_session", "session_creation_mode"} {
		body := bookBody("本", 6, 3)
		body[extra] = "x"
		if r := p["A"].send(http.MethodPost, base, body); r.status != 400 || r.errorCode(t) != "invalid_json" {
			t.Fatalf("%s = %d %s", extra, r.status, r.body)
		}
	}
	if r := p["B"].send(http.MethodPost, base, bookBody("本", 6, 3)); r.status != 403 {
		t.Fatalf("一般メンバーの登録 = %d", r.status)
	}
	if r := s.client().do(http.MethodPost, base, jsonBody(bookBody("本", 6, 3)), nil); r.status != 401 {
		t.Fatalf("未ログインの登録 = %d", r.status)
	}
	if r := p["A"].do(http.MethodPost, base, jsonBody(bookBody("本", 6, 3)), map[string]string{"X-CSRF-Token": p["A"].csrf}); r.status != 400 || r.errorCode(t) != "idempotency_key_required" {
		t.Fatalf("再送キーなし = %d %s", r.status, r.body)
	}
	if r := p["A"].send(http.MethodPost, base, bookBody("本", 2, 3)); r.status != 422 || !strings.Contains(string(r.body), `"path":"sections"`) {
		t.Fatalf("章が少ない = %d %s", r.status, r.body)
	}
	outsider := s.client()
	outsider.oauthLogin("999999999999999999", "外部", "/")
	if r := outsider.send(http.MethodPost, base, bookBody("本", 6, 3)); r.status != 404 {
		t.Fatalf("グループ外の登録 = %d", r.status)
	}

	// 登録は再送しても1冊だけ。応答は契約どおり {book} で、実セッションは作られない。
	key := newKey()
	var created struct {
		Book apitypes.ReadingBook `json:"book"`
	}
	res := p["A"].sendKey(http.MethodPost, base, key, bookBody("設計の本", 6, 3)).mustStatus(t, http.StatusCreated)
	res.decode(t, &created)
	again := p["A"].sendKey(http.MethodPost, base, key, bookBody("設計の本", 6, 3)).mustStatus(t, http.StatusCreated)
	var created2 struct {
		Book apitypes.ReadingBook `json:"book"`
	}
	again.decode(t, &created2)
	if created.Book.ID != created2.Book.ID || strings.Contains(string(res.body), "initial_session") {
		t.Fatalf("再送で別のブックができた/旧項目が残っている: %s", res.body)
	}
	var list apitypes.ReadingBookList
	p["B"].get(base).mustStatus(t, 200).decode(t, &list)
	if len(list.Items) != 1 || list.Items[0].PlanStatus != "planning" || list.Items[0].PeriodStart != "2026-10-01" || list.Items[0].AdjustmentLeadDays != 7 {
		t.Fatalf("一覧 = %s", dump(list))
	}
	if n := len(s.groupSessions(t, p["A"], gid)); n != 0 {
		t.Fatalf("登録直後にセッションがある: %d", n)
	}
	bookPath := base + "/" + created.Book.ID

	// 編集：管理者だけ。未知の項目は拒否。
	if r := p["B"].send(http.MethodPatch, bookPath, map[string]any{"title": "乗っ取り"}); r.status != 403 {
		t.Fatalf("一般メンバーの編集 = %d", r.status)
	}
	if r := p["A"].send(http.MethodPatch, bookPath, map[string]any{"title": "x", "plan_status": "approved"}); r.status != 400 {
		t.Fatalf("未知項目の編集 = %d %s", r.status, r.body)
	}
	var d apitypes.ReadingBookDetail
	p["A"].send(http.MethodPatch, bookPath, map[string]any{"title": "設計の本（改）", "isbn": ""}).mustStatus(t, 200).decode(t, &d)
	if d.Book.Title != "設計の本（改）" || !d.Permissions.CanEdit {
		t.Fatalf("編集結果 = %s", dump(d))
	}

	// 担当の回答：計画前は 409、他人の枠・不正な回答は拒否。
	if r := p["B"].send(http.MethodPut, bookPath+"/assignments/me", map[string]any{"decision": "accept"}); r.status != 409 {
		t.Fatalf("計画前の回答 = %d %s", r.status, r.body)
	}
	s.process()
	d = p["A"].bookDetail(t, gid, created.Book.ID)
	if d.Book.PlanStatus != "awaiting_approval" || len(d.Sessions) != 3 {
		t.Fatalf("計画後 = %s", dump(d.Book))
	}
	// 1枠目の担当者以外が、その枠を承認しようとすると 403（管理者でも他人の代理はできない）。
	first := d.Sessions[0]
	for _, name := range []string{"A", "B", "C", "D"} {
		me := memberIDOf(t, p[name], gid)
		if *first.AssigneeMemberID == me {
			continue
		}
		if r := p[name].send(http.MethodPut, bookPath+"/assignments/me", map[string]any{"decision": "accept", "slot_id": first.SlotID}); r.status != 403 {
			t.Fatalf("%s が他人の担当を承認 = %d %s", name, r.status, r.body)
		}
	}
	if r := p["A"].send(http.MethodPut, bookPath+"/assignments/me", map[string]any{"decision": "maybe"}); r.status != 422 {
		t.Fatalf("不正な回答 = %d", r.status)
	}
	if r := p["A"].send(http.MethodPut, bookPath+"/assignments/me", map[string]any{"decision": "accept", "member_id": "mem_x"}); r.status != 400 {
		t.Fatalf("メンバーIDの指定 = %d %s", r.status, r.body)
	}
	// 直前確認は担当者本人のみ・時期前は 409。
	if r := p["A"].send(http.MethodPut, bookPath+"/slots/"+first.SlotID+"/assignee-confirmation", map[string]any{"decision": "confirm"}); r.status != 403 && r.status != 409 {
		t.Fatalf("時期前の確認 = %d %s", r.status, r.body)
	}
	if r := p["A"].send(http.MethodPut, bookPath+"/slots/slot_none/assignee-confirmation", map[string]any{"decision": "confirm"}); r.status != 404 {
		t.Fatalf("存在しない枠 = %d", r.status)
	}
	// 空き時間の依頼は管理者だけ。
	if r := p["B"].send(http.MethodPost, bookPath+"/availability-requests", map[string]any{}); r.status != 403 {
		t.Fatalf("一般メンバーの空き時間依頼 = %d", r.status)
	}
	var ar apitypes.AvailabilityRequestResult
	p["A"].send(http.MethodPost, bookPath+"/availability-requests", map[string]any{}).mustStatus(t, 200).decode(t, &ar)
	if ar.RequestedCount != 4 {
		t.Fatalf("空き時間の依頼 = %+v", ar)
	}
}

func memberIDOf(t *testing.T, c *client, groupID string) string {
	t.Helper()
	var g apitypes.Group
	c.get("/api/groups/"+groupID).mustStatus(t, 200).decode(t, &g)
	return g.CurrentMemberID
}

// MVPデモ：登録 → 全体計画 → 各担当者が承認 → 調整開始日に自動開始 → 日時確定 → 3日前の確認 → 担当交代（候補本人の承認）。
func TestBookDemoFlowWithRestartsAndDemoClock(t *testing.T) {
	s := newServer(t)
	gid, p := readingGroup(t, s)
	base := "/api/groups/" + gid + "/reading/books"
	names := []string{"A", "B", "C", "D"}

	var created struct {
		Book apitypes.ReadingBook `json:"book"`
	}
	p["A"].send(http.MethodPost, base, bookBody("設計の本", 6, 3)).mustStatus(t, http.StatusCreated).decode(t, &created)
	var second struct {
		Book apitypes.ReadingBook `json:"book"`
	}
	p["A"].send(http.MethodPost, base, bookBody("別の本", 6, 3)).mustStatus(t, http.StatusCreated).decode(t, &second)
	bid, bid2 := created.Book.ID, second.Book.ID
	if n := len(s.groupSessions(t, p["A"], gid)); n != 0 {
		t.Fatalf("登録しただけで開催回がある: %d", n)
	}
	if k := s.dmKinds(); len(k) != 0 {
		t.Fatalf("登録だけで通知が作られた: %v", k)
	}

	// 全体計画（AIの提案）と、担当者への承認依頼。処理を重ねても、再起動しても重複しない。
	s.process()
	proposed := s.dmKinds()["book_plan_proposed"]
	if proposed < 4 || proposed > 8 {
		t.Fatalf("承認依頼のDM数が想定外: %d", proposed)
	}
	s.restart()
	p = s.reloginAll()
	s.process()
	s.process()
	if got := s.dmKinds()["book_plan_proposed"]; got != proposed {
		t.Fatalf("再起動・再処理で承認依頼のDMが重複した: %d → %d", proposed, got)
	}
	for _, m := range s.sender.all() {
		if m.Kind == "book_plan_proposed" && len(m.DMUserIDs) != 1 {
			t.Fatalf("承認依頼は本人宛ての個別DMのはず: %+v", m)
		}
	}
	for _, id := range []string{bid, bid2} {
		if d := p["A"].bookDetail(t, gid, id); d.Book.PlanStatus != "awaiting_approval" {
			t.Fatalf("計画待ち: %s", d.Book.PlanStatus)
		}
	}

	// 各担当者が自分の担当分だけを承認する。全員が揃うまで開始しない。
	accept := func(id string) {
		d := p["A"].bookDetail(t, gid, id)
		done := map[string]bool{}
		for _, sl := range d.Sessions {
			me := ""
			for _, n := range names {
				if memberIDOf(t, p[n], gid) == *sl.AssigneeMemberID {
					me = n
				}
			}
			if done[me] {
				continue
			}
			done[me] = true
			if got := p["A"].bookDetail(t, gid, id).Book.PlanStatus; got != "awaiting_approval" {
				t.Fatalf("承認が揃う前に成立した: %s", got)
			}
			p[me].send(http.MethodPut, base+"/"+id+"/assignments/me", map[string]any{"decision": "accept"}).mustStatus(t, 200)
		}
		if got := p["A"].bookDetail(t, gid, id).Book.PlanStatus; got != "approved" {
			t.Fatalf("全員承認後の状態: %s", got)
		}
	}
	accept(bid)
	accept(bid2)
	s.process()
	if n := len(s.groupSessions(t, p["A"], gid)); n != 0 {
		t.Fatalf("調整開始日の前にセッションができた: %d", n)
	}

	// 時計を進める：調整開始日（9/24）になると、各ブックの第1回だけが1回ずつ始まる。
	s.setNow(jstDay(2026, 9, 24))
	s.process()
	s.process()
	s.restart()
	p = s.reloginAll()
	s.process()
	sessions := s.groupSessions(t, p["A"], gid)
	if len(sessions) != 2 {
		t.Fatalf("2冊の第1回が同時進行していない: %d", len(sessions))
	}
	d := p["A"].bookDetail(t, gid, bid)
	if d.Sessions[0].Session == nil || d.Sessions[1].Session != nil || d.Sessions[0].SchedulingStatus != "scheduling" {
		t.Fatalf("第1回だけが調整中のはず: %s", dump(d.Sessions))
	}
	sid := d.Sessions[0].Session.ID
	assignee := ""
	for _, n := range names {
		if memberIDOf(t, p[n], gid) == *d.Sessions[0].AssigneeMemberID {
			assignee = n
		}
	}

	// 例外的な参加不可日を含む参加条件を集め、AIが日時候補を提案し、全員の同意で確定する（既存の日程・同意処理）。
	// 普段の空き時間は水曜の夜として登録済み。回では時間帯を答えず、今回だけの参加不可日を答える。
	for _, n := range names {
		p[n].send(http.MethodPut, "/api/me/weekly-availability", map[string]any{"timezone": "Asia/Tokyo", "windows": []map[string]any{
			{"weekday": 3, "start": "19:00", "end": "22:00"}, {"weekday": 5, "start": "19:00", "end": "22:00"}}}).mustStatus(t, 200)
	}
	for _, n := range names {
		if r := p[n].submitPreparation(sid, "attending", map[string]any{"declined_presentation": false, "unavailable_dates": []string{"2026-10-07"}}); r.status >= 300 {
			t.Fatalf("%s の参加条件 = %d %s", n, r.status, r.body)
		}
	}
	s.process()
	det := p["A"].detail(sid)
	if det.CurrentProposal == nil || det.CurrentProposal.Status != "pending" {
		t.Fatalf("案がない: %s", dump(det.ActiveCase))
	}
	if strings.Contains(string(det.CurrentProposal.Data), "2026-10-07") {
		t.Fatalf("今回だけ参加不可の日が候補になった: %s", det.CurrentProposal.Data)
	}
	// 同時進行中の別ブックがある。同じ日時の重複を提案しない（両方の案の日時が異なる）。
	p[assignee].respond(sid, "assignment", "accept").mustStatus(t, 202)
	for _, n := range names {
		p[n].respond(sid, "approval", "approve").mustStatus(t, 202)
	}
	d = p["A"].bookDetail(t, gid, bid)
	if d.Sessions[0].SchedulingStatus != "scheduled" {
		t.Fatalf("日時が確定していない: %s", dump(d.Sessions[0]))
	}
	starts := d.Sessions[0].Session.StartsAt

	// 開催3日前：担当者へ最終確認を一度だけ。再処理・再起動でも重複しない。
	s.setNow(starts.Add(-72 * time.Hour))
	s.process()
	s.process()
	s.restart()
	p = s.reloginAll()
	s.process()
	if s.dmKinds()["book_assignee_confirm"] != 1 {
		t.Fatalf("最終確認のDM = %v", s.dmKinds())
	}
	slotPath := base + "/" + bid + "/slots/" + d.Sessions[0].SlotID + "/assignee-confirmation"
	other := "A"
	if assignee == "A" {
		other = "B"
	}
	if r := p[other].send(http.MethodPut, slotPath, map[string]any{"decision": "confirm"}); r.status != 403 {
		t.Fatalf("担当者以外の確認 = %d %s", r.status, r.body)
	}

	// 担当者が変更を希望 → AIが候補を提案するが、候補本人の承認まで交代しない。
	p[assignee].send(http.MethodPut, slotPath, map[string]any{"decision": "request_change"}).mustStatus(t, 200)
	s.process()
	d = p["A"].bookDetail(t, gid, bid)
	slot := d.Sessions[0]
	if slot.AssignmentStatus != "change_proposed" || slot.ProposedAssigneeMemberID == nil || *slot.AssigneeMemberID != *d.Sessions[0].AssigneeMemberID {
		t.Fatalf("候補の提案 = %s", dump(slot))
	}
	oldAssignee := *slot.AssigneeMemberID
	s.advance(2 * time.Hour)
	if now := p["A"].bookDetail(t, gid, bid).Sessions[0]; *now.AssigneeMemberID != oldAssignee {
		t.Fatalf("無承認で担当が交代した: %s", dump(now))
	}
	candidate := ""
	for _, n := range names {
		if memberIDOf(t, p[n], gid) == *slot.ProposedAssigneeMemberID {
			candidate = n
		}
	}
	if candidate == assignee {
		t.Fatal("変更を希望した本人が候補になった")
	}
	for _, n := range names {
		if n != candidate {
			if r := p[n].send(http.MethodPut, base+"/"+bid+"/assignments/me", map[string]any{"decision": "accept", "slot_id": slot.SlotID}); r.status != 403 && r.status != 409 {
				t.Fatalf("%s が候補の代わりに承認 = %d %s", n, r.status, r.body)
			}
		}
	}
	p[candidate].send(http.MethodPut, base+"/"+bid+"/assignments/me", map[string]any{"decision": "accept", "slot_id": slot.SlotID}).mustStatus(t, 200)
	s.process()
	d = p["A"].bookDetail(t, gid, bid)
	if *d.Sessions[0].AssigneeMemberID == oldAssignee || d.Sessions[0].AssignmentStatus != "accepted" || d.Book.PlanStatus != "approved" {
		t.Fatalf("候補の承認後 = %s / %s", dump(d.Sessions[0]), d.Book.PlanStatus)
	}
	var data struct {
		Assignee string `json:"assignee_member_id"`
	}
	_ = json.Unmarshal(p["A"].detail(sid).Data, &data)
	if data.Assignee != *d.Sessions[0].AssigneeMemberID {
		t.Fatalf("開催回の担当が更新されていない: %s", p["A"].detail(sid).Data)
	}
}

func (s *server) reloginAll() map[string]*client {
	return s.personas()
}
