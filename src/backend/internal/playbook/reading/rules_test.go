package reading_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

var ctx = context.Background()

func TestISBN13(t *testing.T) {
	if !reading.ValidISBN13("9784297127831") {
		t.Fatal("正しいISBNを拒否した")
	}
	for _, bad := range []string{"9784297127830", "978-4297127831", "978429712783", "abcdefghijklm"} {
		if reading.ValidISBN13(bad) {
			t.Fatalf("不正なISBN %q を受け付けた", bad)
		}
	}
}

func TestValidateSessionDataAcceptsExampleAndNormalizes(t *testing.T) {
	raw := strings.Replace(sessionJSON, `"サンプル技術書"`, `"  サンプル技術書  "`, 1)
	out, err := reading.New().ValidateSessionData(ctx, coord.SessionParams{DurationMinutes: 60}, json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	var d reading.SessionData
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatal(err)
	}
	if d.BookTitle != "サンプル技術書" {
		t.Fatalf("書名の前後の空白が除かれていない: %q", d.BookTitle)
	}
}

func TestValidateSessionDataRejectsInvalidInput(t *testing.T) {
	cases := map[string]struct {
		json string
		path string
	}{
		"未知のフィールド":   {`{"book_title":"x","isbn":null,"toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":["s"],"extra":1}`, ""},
		"節IDの重複":     {`{"book_title":"x","isbn":null,"toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s","title":"t"},{"id":"s","title":"u"}],"completed_section_ids":[],"target_section_ids":["s"]}`, "sections[1].id"},
		"範囲の重なり":     {`{"book_title":"x","isbn":null,"toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":["s"],"target_section_ids":["s"]}`, "target_section_ids[0]"},
		"対象範囲が空":     {`{"book_title":"x","isbn":null,"toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":[]}`, "target_section_ids"},
		"未登録の節":      {`{"book_title":"x","isbn":null,"toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":["zzz"]}`, "target_section_ids[0]"},
		"ISBNのチェック":  {`{"book_title":"x","isbn":"9784297127830","toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":["s"]}`, "isbn"},
		"webにURLなし":  {`{"book_title":"x","isbn":null,"toc_source":{"kind":"web","urls":[]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":["s"]}`, "toc_source.urls"},
		"httpのURL":   {`{"book_title":"x","isbn":null,"toc_source":{"kind":"web","urls":["http://example.com"]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":["s"]}`, "toc_source.urls[0]"},
		"manualにURL": {`{"book_title":"x","isbn":null,"toc_source":{"kind":"manual","urls":["https://example.com"]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":["s"]}`, "toc_source.urls"},
		"空の書名":       {`{"book_title":"   ","isbn":null,"toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s","title":"t"}],"completed_section_ids":[],"target_section_ids":["s"]}`, "book_title"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := reading.New().ValidateSessionData(ctx, coord.SessionParams{DurationMinutes: 60}, json.RawMessage(tc.json))
			paths := validationPaths(t, err)
			if !hasPath(paths, tc.path) {
				t.Fatalf("パス %q のエラーを期待したが %v", tc.path, paths)
			}
		})
	}
}

func TestValidatePreparation(t *testing.T) {
	pb := reading.New()
	s := baseSnapshot()
	for _, ok := range []string{`{}`, `{"declined_presentation":true}`, `{"unavailable_dates":["2026-09-30"]}`} {
		if _, err := pb.ValidatePreparation(ctx, s, "attending", json.RawMessage(ok)); err != nil {
			t.Fatalf("%s を拒否した: %v", ok, err)
		}
	}
	// 欠席なら担当の辞退の印は要らない
	out, err := pb.ValidatePreparation(ctx, s, "absent", json.RawMessage(`{"declined_presentation":true}`))
	if err != nil || strings.Contains(string(out), `"declined_presentation":true`) {
		t.Fatalf("欠席の回答: %s %v", out, err)
	}
	// 以前の準備状況の項目は無視して受け付ける（保存済みの値・古い画面との互換）
	legacy := `{"willing_to_present":true,"prepared_section_ids":["sec_2"],"explainable_section_ids":["sec_2"],"max_presentation_minutes":15}`
	out, err = pb.ValidatePreparation(ctx, s, "attending", json.RawMessage(legacy))
	if err != nil || strings.Contains(string(out), "willing_to_present") {
		t.Fatalf("以前の項目: %s %v", out, err)
	}

	cases := map[string]struct {
		attendance, json, path string
	}{
		"日付の形":         {"attending", `{"unavailable_dates":["9/30"]}`, "unavailable_dates[0]"},
		"本人以外のIDを含む本文": {"attending", `{"member_id":"mem_b"}`, ""},
		"未知のフィールド":     {"attending", `{"note":"x"}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := pb.ValidatePreparation(ctx, s, tc.attendance, json.RawMessage(tc.json))
			if paths := validationPaths(t, err); !hasPath(paths, tc.path) {
				t.Fatalf("パス %q のエラーを期待したが %v", tc.path, paths)
			}
		})
	}
}

func TestApplyWithdrawalMarksDeclined(t *testing.T) {
	pb := reading.New()
	s := baseSnapshot()
	withSchedule := json.RawMessage(`{"declined_presentation":false,"unavailable_dates":["2026-09-30"],"schedule":{"status":"unknown","weekly_windows":[],"date_windows":[],"max_duration_minutes":0}}`)
	out, err := pb.ApplyWithdrawal(ctx, s, coord.WithdrawAssignment, withSchedule)
	if err != nil {
		t.Fatal(err)
	}
	var d reading.PreparationData
	_ = json.Unmarshal(out, &d)
	if !d.DeclinedPresentation || !reflect.DeepEqual(d.UnavailableDates, []string{"2026-09-30"}) || d.Schedule == nil {
		t.Fatalf("辞退の印を付け、予定は残す: %+v", d)
	}
	// 変換結果は欠席・参加のどちらでも検証を通る。
	for _, att := range []string{"attending", "absent"} {
		if _, err := pb.ValidatePreparation(ctx, s, att, out); err != nil {
			t.Fatalf("%s: 変換結果が検証を通らない: %v", att, err)
		}
	}

	out, err = pb.ApplyWithdrawal(ctx, s, coord.WithdrawAttendance, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"declined_presentation":true,"unavailable_dates":[]}` {
		t.Fatalf("未回答からの既定値が違う: %s", out)
	}
}

func TestValidatePlanAcceptsExamples(t *testing.T) {
	pb := reading.New()
	s := baseSnapshot()
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)}); err != nil {
		t.Fatalf("初回案: %v", err)
	}
	s = withConfirmed(s, initialPlanJSON)
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(replanJSON)}); err != nil {
		t.Fatalf("R6 の再計画例: %v", err)
	}
}

func TestValidatePlanRejectsViolations(t *testing.T) {
	pb := reading.New()
	mutate := func(f func(p map[string]any)) json.RawMessage {
		var p map[string]any
		_ = json.Unmarshal([]byte(replanJSON), &p)
		f(p)
		b, _ := json.Marshal(p)
		return b
	}
	agenda := func(p map[string]any, i int) map[string]any { return p["agenda"].([]any)[i].(map[string]any) }

	cases := map[string]struct {
		plan json.RawMessage
		prep func(s *coord.Snapshot)
		path string
	}{
		"合計が持ち時間超過": {plan: mutate(func(p map[string]any) { agenda(p, 2)["minutes"] = 26 }), path: "agenda"},
		"発表に担当者なし":  {plan: mutate(func(p map[string]any) { agenda(p, 0)["presenter_member_id"] = nil }), path: "agenda[0].presenter_member_id"},
		"範囲の和が不一致":  {plan: mutate(func(p map[string]any) { p["deferred_section_ids"] = []any{} }), path: "covered_section_ids"},
		"扱う節が進行表にない": {plan: mutate(func(p map[string]any) {
			p["covered_section_ids"] = []any{"sec_2", "sec_3"}
			p["deferred_section_ids"] = []any{}
		}), path: "covered_section_ids[1]"},
		"範囲外の節":    {plan: mutate(func(p map[string]any) { agenda(p, 2)["section_ids"] = []any{"sec_3"} }), path: "agenda[2].section_ids[0]"},
		"分数が0":     {plan: mutate(func(p map[string]any) { agenda(p, 1)["minutes"] = 0 }), path: "agenda[1].minutes"},
		"進行項目ID重複": {plan: mutate(func(p map[string]any) { agenda(p, 1)["id"] = "item_1" }), path: "agenda[1].id"},
		"不明な活動":    {plan: mutate(func(p map[string]any) { agenda(p, 1)["activity"] = "party" }), path: "agenda[1].activity"},
		"欠席者を担当":   {plan: json.RawMessage(replanJSON), prep: func(s *coord.Snapshot) { setPrep(s, "mem_c", prep("absent", false)) }, path: "agenda[0].presenter_member_id"},
		"未回答者を担当":  {plan: json.RawMessage(replanJSON), prep: func(s *coord.Snapshot) { setPrep(s, "mem_c", nil) }, path: "agenda[0].presenter_member_id"},
		"担当を辞退した人": {plan: json.RawMessage(replanJSON), prep: func(s *coord.Snapshot) {
			setPrep(s, "mem_c", prep("attending", true))
		}, path: "agenda[0].presenter_member_id"},
		"この案件で引き受けを断った人": {plan: json.RawMessage(replanJSON), prep: func(s *coord.Snapshot) {
			s.Case.DeclinedMemberIDs = []string{"mem_c"}
		}, path: "agenda[0].presenter_member_id"},
		"未知のフィールド": {plan: mutate(func(p map[string]any) { p["note"] = "x" }), path: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := withConfirmed(baseSnapshot(), initialPlanJSON)
			if tc.prep != nil {
				tc.prep(&s)
			}
			err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: tc.plan})
			if paths := validationPaths(t, err); !hasPath(paths, tc.path) {
				t.Fatalf("パス %q のエラーを期待したが %v", tc.path, paths)
			}
		})
	}
}

func TestReviewOnlyPlanIsAllowed(t *testing.T) {
	plan := `{"covered_section_ids":[],"deferred_section_ids":["sec_2","sec_3"],"agenda":[{"id":"r","activity":"review","section_ids":["sec_1"],"presenter_member_id":null,"minutes":60}]}`
	s := withConfirmed(baseSnapshot(), initialPlanJSON)
	if err := reading.New().ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(plan)}); err != nil {
		t.Fatalf("復習回を拒否した: %v", err)
	}
}

// 同じ人を3回連続で代役にしない。履歴は確定済みの開催回ごとに数える。
func TestThirdConsecutiveSubstituteIsRejected(t *testing.T) {
	pb := reading.New()
	s := withConfirmed(baseSnapshot(), initialPlanJSON)
	cSub := coord.PastSession{ConfirmedPlans: []coord.PlanRecord{
		{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)},
		{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(replanJSON)},
	}}
	noSub := coord.PastSession{ConfirmedPlans: []coord.PlanRecord{{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)}}}
	prop := coord.Proposal{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(replanJSON)}

	s.History = []coord.PastSession{cSub}
	if err := pb.ValidatePlan(ctx, s, prop); err != nil {
		t.Fatalf("2回連続は許可される: %v", err)
	}
	s.History = []coord.PastSession{cSub, cSub}
	if err := pb.ValidatePlan(ctx, s, prop); err == nil || !strings.Contains(err.Error(), "3回連続") {
		t.Fatalf("3回連続の代役を拒否すべき: %v", err)
	}
	s.History = []coord.PastSession{cSub, noSub, cSub}
	if err := pb.ValidatePlan(ctx, s, prop); err != nil {
		t.Fatalf("連続が途切れていれば許可される: %v", err)
	}
	// 初回案は代役に当たらない。
	s.History = []coord.PastSession{cSub, cSub}
	s.ConfirmedPlans = nil
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(replanJSON)}); err != nil {
		t.Fatalf("初回案は代役制約の対象外: %v", err)
	}
}

func TestApprovalRequirements(t *testing.T) {
	pb := reading.New()

	t.Run("初回案は担当者の引き受けと参加予定者全員の同意", func(t *testing.T) {
		req, err := pb.ApprovalRequirements(ctx, baseSnapshot(), coord.Proposal{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(req.RequiredAcceptorIDs, []string{"mem_b"}) || len(req.Approvals) != 1 || req.Approvals[0].Kind != coord.ApprovalAll ||
			!reflect.DeepEqual(req.Approvals[0].EligibleMemberIDs, []string{"mem_a", "mem_b", "mem_c", "mem_d"}) {
			t.Fatalf("unexpected: %+v", req)
		}
	})

	t.Run("範囲の変更は参加予定者全員の同意", func(t *testing.T) {
		s := withConfirmed(baseSnapshot(), initialPlanJSON)
		setPrep(&s, "mem_d", prep("absent", false))
		req, err := pb.ApprovalRequirements(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(replanJSON)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(req.RequiredAcceptorIDs, []string{"mem_c"}) {
			t.Fatalf("acceptors: %v", req.RequiredAcceptorIDs)
		}
		if len(req.Approvals) != 1 || req.Approvals[0].Kind != coord.ApprovalAll ||
			!reflect.DeepEqual(req.Approvals[0].EligibleMemberIDs, []string{"mem_a", "mem_b", "mem_c"}) {
			t.Fatalf("approvals: %+v", req.Approvals)
		}
	})

	t.Run("担当だけの変更は承認不要で新担当者の引き受けのみ", func(t *testing.T) {
		s := withConfirmed(baseSnapshot(), initialPlanJSON)
		swapped := strings.Replace(initialPlanJSON, `"presenter_member_id": "mem_b"`, `"presenter_member_id": "mem_c"`, 1)
		req, err := pb.ApprovalRequirements(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(swapped)})
		if err != nil {
			t.Fatal(err)
		}
		if len(req.Approvals) != 0 || !reflect.DeepEqual(req.RequiredAcceptorIDs, []string{"mem_c"}) {
			t.Fatalf("unexpected: %+v", req)
		}
	})

	t.Run("担当が固定されたブック由来セッションでは担当引き受けを重複して求めない", func(t *testing.T) {
		fixed := strings.Replace(sessionJSON, `"target_section_ids": ["sec_2", "sec_3"]`, `"target_section_ids": ["sec_2", "sec_3"], "assignee_member_id": "mem_b"`, 1)
		s := baseSnapshot()
		s.SessionData = json.RawMessage(fixed)
		req, err := pb.ApprovalRequirements(ctx, s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)})
		if err != nil {
			t.Fatal(err)
		}
		if len(req.RequiredAcceptorIDs) != 0 {
			t.Fatalf("担当固定セッションで重複して引き受けを求めている: %+v", req.RequiredAcceptorIDs)
		}
		if len(req.Approvals) != 1 || req.Approvals[0].Kind != coord.ApprovalAll {
			t.Fatalf("同意の要件は変わらないはず: %+v", req.Approvals)
		}

		s = withConfirmed(s, initialPlanJSON)
		req, err = pb.ApprovalRequirements(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(initialPlanJSON)})
		if err != nil {
			t.Fatal(err)
		}
		if len(req.RequiredAcceptorIDs) != 0 || len(req.Approvals) != 0 {
			t.Fatalf("担当固定・内容据え置きの再計画は無条件で成立するはず: %+v", req)
		}
	})

	t.Run("担当が固定されていない旧方式・手動セッションでは従来どおり担当引き受けを求める", func(t *testing.T) {
		s := baseSnapshot() // sessionJSON に assignee_member_id は無い
		req, err := pb.ApprovalRequirements(ctx, s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(req.RequiredAcceptorIDs, []string{"mem_b"}) {
			t.Fatalf("旧方式では担当引き受けを維持すべき: %+v", req.RequiredAcceptorIDs)
		}
	})

	t.Run("時間配分の変更は参加予定者全員の同意", func(t *testing.T) {
		s := withConfirmed(baseSnapshot(), initialPlanJSON)
		changed := strings.Replace(strings.Replace(initialPlanJSON, `"minutes": 30`, `"minutes": 25`, 1), `"minutes": 15}
  ]`, `"minutes": 20}
  ]`, 1)
		req, err := pb.ApprovalRequirements(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: json.RawMessage(changed)})
		if err != nil {
			t.Fatal(err)
		}
		if len(req.Approvals) != 1 || req.Approvals[0].Kind != coord.ApprovalAll {
			t.Fatalf("unexpected: %+v", req)
		}
	})
}

func TestAssignees(t *testing.T) {
	ids, err := reading.New().Assignees(json.RawMessage(replanJSON))
	if err != nil || !reflect.DeepEqual(ids, []string{"mem_c"}) {
		t.Fatalf("got %v, %v", ids, err)
	}
}

func TestBuildContextHasNoUnsharedFields(t *testing.T) {
	raw, err := reading.New().BuildContext(ctx, withConfirmed(baseSnapshot(), initialPlanJSON))
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"book_title", "sections", "members", "current_confirmed_plan", "case"} {
		if _, ok := c[key]; !ok {
			t.Fatalf("判断材料に %s がない: %s", key, raw)
		}
	}
}

// 対話の途中の値は未確定を許す。聞くのは参加と（日時未定の回の）時間帯だけ。
func TestValidatePartialPreparation(t *testing.T) {
	pb := reading.New()
	s := baseSnapshot()
	all := []string{"attendance", "schedule", "unavailable_dates", "declined_presentation"}

	// 日時が決まっている回では、時間帯・出られない日・担当の辞退は聞かない
	_, unclear, err := pb.ValidatePartialPreparation(ctx, s, "attending", json.RawMessage(`{}`), all)
	if err != nil || !reflect.DeepEqual(unclear, []string{"attendance"}) {
		t.Fatalf("日時決定済みの回の未確定 = %v %v", unclear, err)
	}
	// 日時未定の回では時間帯を聞く。欠席と決まれば聞かない
	s.ScheduleStatus = coord.ScheduleProposed
	if _, unclear, _ = pb.ValidatePartialPreparation(ctx, s, "attending", json.RawMessage(`{}`), all); !reflect.DeepEqual(unclear, []string{"attendance", "schedule"}) {
		t.Fatalf("日時未定の回の未確定 = %v", unclear)
	}
	if _, unclear, _ = pb.ValidatePartialPreparation(ctx, s, "absent", json.RawMessage(`{}`), nil); len(unclear) != 0 {
		t.Fatalf("欠席なら時間帯は聞かない: %v", unclear)
	}

	cases := map[string]struct {
		json, path string
	}{
		"未知のキー":      {`{"note":"x"}`, ""},
		"本人以外のIDの混入": {`{"member_id":"mem_b"}`, ""},
		"日付の形":       {`{"unavailable_dates":["明日"]}`, "unavailable_dates[0]"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := pb.ValidatePartialPreparation(ctx, s, "attending", json.RawMessage(tc.json), all)
			if paths := validationPaths(t, err); !hasPath(paths, tc.path) {
				t.Fatalf("パス %q のエラーを期待したが %v", tc.path, paths)
			}
		})
	}
}

// 記録に残すのは項目の差分だけ。初回は全項目、変更時は変わった項目だけを出す。
func TestDiffPreparation(t *testing.T) {
	pb := reading.New()
	s := baseSnapshot()
	before := prep("attending", false)

	if first := pb.DiffPreparation(s, nil, *before); !reflect.DeepEqual(first, []string{"参加：参加"}) {
		t.Fatalf("初回: %v", first)
	}
	after := prep("attending", true)
	if got := pb.DiffPreparation(s, before, *after); !reflect.DeepEqual(got, []string{"説明の担当：割り振り可 → 今回は辞退"}) {
		t.Fatalf("差分 = %v", got)
	}
	if got := pb.DiffPreparation(s, before, *before); len(got) != 0 {
		t.Fatalf("変更がないのに差分が出た: %v", got)
	}
}
