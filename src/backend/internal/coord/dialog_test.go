package coord_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// 段階的な聞き取りで参加条件を作り、本人の確認（保存）まで進められる。
// 途中の仮値は保存せず、保存の経路・認可・整合性は Web と同じものを使う。
func TestDialogStepByStep(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()

	targets, err := h.c.PreparationTargets(ctx, h.users["B"])
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].SessionID != h.sess {
		t.Fatalf("対象の開催回 = %+v", targets)
	}

	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ready || res.State.Pending != "attendance" {
		t.Fatalf("未登録なら参加可否から聞く: pending=%q ready=%v", res.State.Pending, res.Ready)
	}

	// 1ターン目：参加可否だけが確定する。
	res = h.turn(t, res.State, "参加します")
	if res.State.Pending != "willing_to_present" || res.Ready {
		t.Fatalf("次は担当の可否: pending=%q ready=%v", res.State.Pending, res.Ready)
	}
	if !res.Progressed {
		t.Fatal("進捗があるのに空振り扱い")
	}

	// 2ターン目：読んできた範囲と担当の意思。時間はまだ聞いていない。
	res = h.turn(t, res.State, "今回の前半は読んできました。説明できます。")
	if res.State.Pending != "max_presentation_minutes" || res.Ready {
		t.Fatalf("次は時間: pending=%q ready=%v", res.State.Pending, res.Ready)
	}

	// 3ターン目：時間が決まれば全体検証を通り、確認に進める。
	res = h.turn(t, res.State, "15分です")
	if !res.Ready || len(res.State.Unclear) != 0 {
		t.Fatalf("確認に進めるはず: %+v", res.State)
	}
	if len(res.Confirm) == 0 {
		t.Fatal("確認表示に全項目が入っていない")
	}

	// 保存前は何も書き込まれていない。
	if p := myPrep(t, h, "B"); p != nil {
		t.Fatal("確認前に保存されている")
	}

	if _, err := h.c.SaveDialogPreparation(ctx, h.users["B"], res.State, nil); err != nil {
		t.Fatal(err)
	}
	p := myPrep(t, h, "B")
	if p == nil {
		t.Fatal("保存されていない")
	}
	var d map[string]any
	if err := json.Unmarshal(p.Data, &d); err != nil {
		t.Fatal(err)
	}
	if p.Attendance != "attending" || d["willing_to_present"] != true || d["max_presentation_minutes"].(float64) != 15 {
		t.Fatalf("保存された値 = %s %s", p.Attendance, p.Data)
	}
	if tk := h.openTask("B", "preparation"); tk != nil {
		t.Fatal("参加条件の確認タスクが残っている")
	}
	// 記録に残るのは差分と入口だけ（発言の原文は残さない）。
	act, err := h.c.Activity(ctx, h.users["A"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range act.Items {
		if strings.Contains(a.Summary, "（Discord）") && strings.Contains(a.Summary, "説明できる時間") {
			found = true
		}
		if strings.Contains(a.Summary, "15分です") {
			t.Fatalf("原文が記録に残っている: %q", a.Summary)
		}
	}
	if !found {
		t.Fatal("差分と入口が記録されていない")
	}
}

// 保存済みの値がある人は、触れなかった項目を保ったまま1項目だけ変えられる。
func TestDialogKeepsUntouchedValues(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.mustPrep("B", "attending", prepData(true, []string{"sec_2", "sec_3"}, []string{"sec_2"}, 40))

	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready {
		t.Fatalf("保存済みの値は確定扱い: %+v", res.State)
	}
	res = h.turn(t, res.State, "20分にしてください")
	if !res.Ready {
		t.Fatalf("1項目の変更で確認に進めるはず: %+v", res.State)
	}
	var d map[string]any
	if err := json.Unmarshal(res.State.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d["max_presentation_minutes"].(float64) != 20 {
		t.Fatalf("時間が変わっていない: %s", res.State.Data)
	}
	if ids, _ := d["prepared_section_ids"].([]any); len(ids) != 2 {
		t.Fatalf("触れていない項目が失われた: %s", res.State.Data)
	}
}

// 表示した下書きより保存済みの状態が新しければ、古い値で保存しない。
func TestDialogRejectsStaleRevision(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	res = h.turn(t, res.State, "参加します")
	res = h.turn(t, res.State, "今回の前半は読んできました。説明できます。")
	res = h.turn(t, res.State, "15分です")
	if !res.Ready {
		t.Fatalf("確認に進めるはず: %+v", res.State)
	}
	// 別の人の更新で版が進む。
	h.mustPrep("C", "attending", prepData(false, []string{"sec_2"}, []string{}, 0))
	if _, err := h.c.SaveDialogPreparation(ctx, h.users["B"], res.State, nil); err == nil {
		t.Fatal("古い版のまま保存された")
	}
}

func (h *harness) turn(t *testing.T, st coord.DialogState, text string) coord.DialogResult {
	t.Helper()
	res, err := h.c.ContinueDialog(ctx, h.users["B"], st, text)
	if err != nil {
		t.Fatalf("%q: %v", text, err)
	}
	return res
}

func myPrep(t *testing.T, h *harness, name string) *struct {
	Attendance string
	Data       json.RawMessage
} {
	t.Helper()
	id := h.memberID(name)
	for _, p := range h.detail(name).Preparations {
		if p.MemberID == id && p.Value != nil {
			return &struct {
				Attendance string
				Data       json.RawMessage
			}{p.Value.Attendance, p.Value.Data}
		}
	}
	return nil
}
