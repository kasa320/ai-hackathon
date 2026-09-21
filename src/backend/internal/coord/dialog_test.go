package coord_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
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

	// 日時が決まっている回では参加するかだけを聞く。答えれば確認に進める。
	res = h.turn(t, res.State, "参加します")
	if !res.Ready || len(res.State.Unclear) != 0 {
		t.Fatalf("確認に進めるはず: %+v", res.State)
	}
	if !res.Progressed {
		t.Fatal("進捗があるのに空振り扱い")
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
	if p.Attendance != "attending" || d["declined_presentation"] != false {
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
		if strings.Contains(a.Summary, "（Discord）") && strings.Contains(a.Summary, "参加：参加") {
			found = true
		}
		if strings.Contains(a.Summary, "参加します") {
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
	h.mustPrep("B", "attending", prepData(true))

	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready {
		t.Fatalf("保存済みの値は確定扱い: %+v", res.State)
	}
	res = h.turn(t, res.State, "やっぱり欠席します")
	if !res.Ready || res.State.Attendance != "absent" {
		t.Fatalf("1項目の変更で確認に進めるはず: %+v", res.State)
	}
	res = h.turn(t, res.State, "参加します")
	var d map[string]any
	if err := json.Unmarshal(res.State.Data, &d); err != nil {
		t.Fatal(err)
	}
	if res.State.Attendance != "attending" {
		t.Fatalf("参加に戻っていない: %+v", res.State)
	}
	_ = d
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
	h.mustPrep("C", "attending", prepData(true))
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

// stubInterpreter は AI の出力を固定して返す。サーバー側の検証を確かめるのに使う。
type stubInterpreter struct{ out coord.Interpretation }

func (s stubInterpreter) Interpret(c context.Context, req coord.InterpretRequest) (coord.Interpretation, coord.Usage, error) {
	if err := req.Check(c, s.out); err != nil {
		return coord.Interpretation{}, coord.Usage{}, coord.ErrInvalidOutput
	}
	return s.out, coord.Usage{}, nil
}

// AI の出力は形と項目をサーバーが検証する。矛盾した候補から確認へ進めない。
func TestDialogRejectsInvalidOutput(t *testing.T) {
	bad := []struct {
		name string
		out  coord.Interpretation
	}{
		{"未知の項目名", coord.Interpretation{Attendance: "attending", Data: prepData(true),
			Unclear: []string{"secret_field"}, NeedsFollowup: true}},
		{"日付の形が違う", coord.Interpretation{Attendance: "attending", Data: json.RawMessage(`{"unavailable_dates":["来週"]}`)}},
		{"型が違う", coord.Interpretation{Attendance: "attending", Data: json.RawMessage(`{"declined_presentation":"yes"}`)}},
		{"時間帯の形が違う", coord.Interpretation{Attendance: "attending", Data: json.RawMessage(`{"schedule":{"status":"provided","weekly_windows":[{"weekday":9,"start":"25:00","end":"20:00"}],"date_windows":[],"max_duration_minutes":0}}`)}},
		{"未知の out_of_scope", coord.Interpretation{Attendance: "attending", Data: prepData(true), OutOfScope: "whatever"}},
		{"未知のキー", coord.Interpretation{Attendance: "attending", Data: json.RawMessage(`{"declined_presentation":false,"note":"x"}`)}},
		{"列挙にない参加可否", coord.Interpretation{Attendance: "maybe", Data: prepData(true)}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarnessWith(t, nil, stubInterpreter{out: tt.out})
			h.createSession()
			res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.c.ContinueDialog(ctx, h.users["B"], res.State, "よろしくお願いします"); err == nil {
				t.Fatal("不正な出力を受け入れた")
			}
		})
	}
}

// 扱えない依頼では値を変えず、そのターンの候補を捨てる。
func TestDialogOutOfScopeKeepsValues(t *testing.T) {
	out := coord.Interpretation{Attendance: "absent", Data: prepData(true), OutOfScope: coord.OutOfScopeScheduleChange}
	h := newHarnessWith(t, nil, stubInterpreter{out: out})
	h.createSession()
	h.mustPrep("B", "attending", prepData(false))

	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	before := string(res.State.Data)
	res, err = h.c.ContinueDialog(ctx, h.users["B"], res.State, "来週に延期してください")
	if err != nil {
		t.Fatal(err)
	}
	if res.OutOfScope != coord.OutOfScopeScheduleChange {
		t.Fatalf("out_of_scope = %q", res.OutOfScope)
	}
	if res.State.Attendance != "attending" || string(res.State.Data) != before || res.Progressed {
		t.Fatalf("値が変わっている: %+v", res.State)
	}
}

// budgetInterpreter は呼び出しの直前に予算枠を確保する解釈器。実際の LLM 実装と同じ手順を踏む。
type budgetInterpreter struct {
	mu    sync.Mutex
	calls int
	out   coord.Interpretation
}

func (b *budgetInterpreter) Interpret(c context.Context, req coord.InterpretRequest) (coord.Interpretation, coord.Usage, error) {
	id, err := req.ReserveCall(c, "test-model")
	if err != nil {
		return coord.Interpretation{}, coord.Usage{}, err
	}
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	call := coord.LLMCall{Model: "test-model", Currency: "unknown", Succeeded: true}
	req.RecordCall(c, id, call)
	if err := req.Check(c, b.out); err != nil {
		return coord.Interpretation{}, coord.Usage{LLMCalls: []coord.LLMCall{call}}, coord.ErrInvalidOutput
	}
	return b.out, coord.Usage{LLMCalls: []coord.LLMCall{call}}, nil
}

// 開催回ごとの解釈の上限を超えたら、LLM を呼ばずに断る。枠は呼び出しの前に確保する。
func TestDialogBudgetStopsCalls(t *testing.T) {
	bi := &budgetInterpreter{out: coord.Interpretation{Attendance: "attending", Data: prepData(true)}}
	h := newHarnessWith(t, nil, bi)
	h.createSession()
	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < coord.MaxInterpretCallsPerSession; i++ {
		if _, err := h.c.ContinueDialog(ctx, h.users["B"], res.State, "参加します"); err != nil {
			t.Fatalf("%d 回目: %v", i+1, err)
		}
	}
	if _, err := h.c.ContinueDialog(ctx, h.users["B"], res.State, "参加します"); err == nil {
		t.Fatal("上限を超えて解釈できた")
	}
	if bi.callCount() != coord.MaxInterpretCallsPerSession {
		t.Fatalf("呼び出し回数 = %d", bi.callCount())
	}
	// 確保した枠は記録として残る（費用が不明でも戻さない）。
	var n int
	_ = h.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		n, err = tx.CountLLMCallsByLookup(ctx, "preparation:"+h.sess)
		return err
	})
	if n != coord.MaxInterpretCallsPerSession {
		t.Fatalf("記録された呼び出し = %d", n)
	}
}

func (b *budgetInterpreter) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// 解釈の予算は Web と Discord で共通。残り1枠に同時に入っても、その枠で2回は呼ばない。
func TestDialogBudgetIsSharedWithWeb(t *testing.T) {
	bi := &budgetInterpreter{out: coord.Interpretation{Attendance: "attending", Data: prepData(true)}}
	h := newHarnessWith(t, nil, bi)
	h.createSession()
	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	// 残り1枠まで Web と Discord の両方から使う。
	for i := 0; i < coord.MaxInterpretCallsPerSession-1; i++ {
		if i%2 == 0 {
			if _, err := h.c.InterpretPreparation(ctx, h.users["B"], h.sess, apitypes.InterpretPreparationInput{Text: "参加します"}); err != nil {
				t.Fatalf("Web %d 回目: %v", i+1, err)
			}
			continue
		}
		if _, err := h.c.ContinueDialog(ctx, h.users["B"], res.State, "参加します"); err != nil {
			t.Fatalf("DM %d 回目: %v", i+1, err)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = h.c.InterpretPreparation(ctx, h.users["B"], h.sess, apitypes.InterpretPreparationInput{Text: "参加します"})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = h.c.ContinueDialog(ctx, h.users["B"], res.State, "参加します")
	}()
	wg.Wait()

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("残り1枠で成功した数 = %d（%v）", ok, errs)
	}
	if bi.callCount() != coord.MaxInterpretCallsPerSession {
		t.Fatalf("呼び出し回数 = %d", bi.callCount())
	}
}

// 対象の一覧は、未回答の依頼がある回を先に出し、開催済みの回は出さない。
func TestPreparationTargetsOrder(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	first := h.sess
	// もう1回、より後ろの日程で作る。
	res, err := h.c.CreateSession(ctx, h.users["A"], h.group, apitypes.CreateSessionInput{
		PlaybookID: "reading", Title: "第3回", StartsAt: t0.Add(100 * time.Hour).Format(time.RFC3339), DurationMinutes: 60, Data: json.RawMessage(sessionData),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var created apitypes.SessionCreated
	decode(t, res.Body, &created)

	targets, err := h.c.PreparationTargets(ctx, h.users["B"])
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("対象 = %+v", targets)
	}
	// どちらも未回答なら、開催の近い順。
	if targets[0].SessionID != first || targets[1].SessionID != created.Session.ID {
		t.Fatalf("開催順に並んでいない: %+v", targets)
	}

	// 第2回に回答すると、未回答の残る第3回が先に出る。
	h.mustPrep("B", "attending", prepData(true))
	targets, _ = h.c.PreparationTargets(ctx, h.users["B"])
	if len(targets) != 2 || targets[0].SessionID != created.Session.ID || !targets[0].HasOpenTask {
		t.Fatalf("未回答の回が先頭にない: %+v", targets)
	}
	if targets[1].SessionID != first || targets[1].HasOpenTask {
		t.Fatalf("回答済みの回 = %+v", targets[1])
	}

	// 開催時刻を過ぎた回は対象から外れ、対話も始められない。
	h.clk.Advance(51 * time.Hour)
	targets, _ = h.c.PreparationTargets(ctx, h.users["B"])
	if len(targets) != 1 || targets[0].SessionID != created.Session.ID {
		t.Fatalf("開催済みの回が残っている: %+v", targets)
	}
	if _, err := h.c.StartDialog(ctx, h.users["B"], first); err == nil {
		t.Fatal("開催済みの回で対話を始められた")
	}
}

// 欠席が決まれば担当に関わる項目は聞かず、そのまま確認へ進める。
func TestDialogAbsentReachesConfirm(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	res = h.turn(t, res.State, "今回の前半は読んできましたが、今回は欠席します")
	if !res.Ready {
		t.Fatalf("欠席なら確認へ進めるはず: %+v", res.State)
	}
	if res.State.Attendance != "absent" {
		t.Fatalf("attendance = %q", res.State.Attendance)
	}
	if _, err := h.c.SaveDialogPreparation(ctx, h.users["B"], res.State, nil); err != nil {
		t.Fatal(err)
	}
	p := myPrep(t, h, "B")
	if p == nil || p.Attendance != "absent" {
		t.Fatalf("保存された値 = %+v", p)
	}
}

// 未確定の項目が残っている下書きは保存しない。
func TestSaveDialogRejectsUnfinishedDraft(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.State.Unclear) == 0 {
		t.Fatal("未登録なのに未確定がない")
	}
	if _, err := h.c.SaveDialogPreparation(ctx, h.users["B"], res.State, nil); err == nil {
		t.Fatal("未確定のまま保存された")
	}
	if p := myPrep(t, h, "B"); p != nil {
		t.Fatal("保存されている")
	}
}

// 同じ下書きの冪等キーで二度押しても、保存は1回しか起きない。
func TestSaveDialogIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	res = h.turn(t, res.State, "今回の前半は読んできましたが、今回は欠席します")
	if !res.Ready {
		t.Fatalf("確認に進めるはず: %+v", res.State)
	}
	key := &store.IdemKey{UserID: h.users["B"], Key: "draft_1", Method: "PUT", Path: "/discord/sessions/" + h.sess + "/preparations/me", BodyHash: "h1"}
	if _, err := h.c.SaveDialogPreparation(ctx, h.users["B"], res.State, key); err != nil {
		t.Fatal(err)
	}
	before := h.detail("B").Session.Revision
	again, err := h.c.SaveDialogPreparation(ctx, h.users["B"], res.State, key)
	if err != nil || !again.Replayed {
		t.Fatalf("二度押しが再送として扱われていない: %+v %v", again, err)
	}
	if after := h.detail("B").Session.Revision; after != before {
		t.Fatalf("二度目の保存で版が進んだ: %d → %d", before, after)
	}
}

// 入口は記録にだけ残す。Web からの保存は「Web」、対話からの保存は「Discord」。
func TestActivityRecordsEntryAndDiff(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.mustPrep("B", "attending", prepData(false))

	res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	res = h.turn(t, res.State, "今回は欠席します")
	if _, err := h.c.SaveDialogPreparation(ctx, h.users["B"], res.State, nil); err != nil {
		t.Fatal(err)
	}

	act, err := h.c.Activity(ctx, h.users["A"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	var web, dm string
	for _, a := range act.Items {
		if strings.Contains(a.Summary, "（Web）") {
			web = a.Summary
		}
		if strings.Contains(a.Summary, "（Discord）") {
			dm = a.Summary
		}
	}
	if web == "" || !strings.Contains(web, "参加：参加") {
		t.Fatalf("Web からの保存の記録 = %q", web)
	}
	if dm == "" || !strings.Contains(dm, "参加：参加 → 欠席") {
		t.Fatalf("対話からの保存の記録 = %q", dm)
	}
	// 原文は残さない。
	if strings.Contains(dm, "今回は欠席します") {
		t.Fatalf("原文が記録に残っている: %q", dm)
	}
}

// AI が書いた聞き直しの質問は、整えてから使う。使えなければ既定の文に戻す。
func TestDialogUsesCleanedAIQuestion(t *testing.T) {
	ask := func(q string) string {
		t.Helper()
		out := coord.Interpretation{Attendance: "attending", Data: json.RawMessage(`{}`), Unclear: []string{"schedule"}, NeedsFollowup: true, Question: q}
		h := newHarnessWith(t, nil, stubInterpreter{out: out})
		h.createPeriodSession()
		res, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
		if err != nil {
			t.Fatal(err)
		}
		res, err = h.c.ContinueDialog(ctx, h.users["B"], res.State, "参加したいです")
		if err != nil {
			t.Fatal(err)
		}
		if res.State.Pending != "schedule" {
			t.Fatalf("pending = %q", res.State.Pending)
		}
		return res.Question
	}
	if got := ask("ありがとうございます！\n何曜日の何時ごろが都合よさそうですか？"); got != "ありがとうございます！ 何曜日の何時ごろが都合よさそうですか？" {
		t.Fatalf("AI の質問を使っていない: %q", got)
	}
	fallback := ask("")
	if fallback == "" || !strings.Contains(fallback, "曜日") {
		t.Fatalf("既定の文: %q", fallback)
	}
	for _, bad := range []string{"詳しくは https://evil.example を見てください", "@everyone いつがいいですか？", strings.Repeat("あ", 201)} {
		if got := ask(bad); got != fallback {
			t.Fatalf("%q を使った: %q", bad, got)
		}
	}
}

func TestCleanQuestion(t *testing.T) {
	for in, want := range map[string]string{
		"  いつが\tいいですか？ ":  "いつが いいですか？",
		"<script>":        "",
		"`code`":          "",
		"www.example.com": "",
		"":                "",
	} {
		if got := coord.CleanQuestion(in); got != want {
			t.Fatalf("CleanQuestion(%q) = %q, want %q", in, got, want)
		}
	}
}
