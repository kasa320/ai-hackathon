package discord

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

var now0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

// 利用者ごとに処理は直列化する。処理中の追加入力は保持せず、その場で断る。
func TestBeginSerializesPerUser(t *testing.T) {
	m := newMemory()
	us, ok := m.begin("usr_1", now0)
	if !ok {
		t.Fatal("最初の入力が取れない")
	}
	if _, ok := m.begin("usr_1", now0); ok {
		t.Fatal("処理中の追加入力を受け付けた")
	}
	if _, ok := m.begin("usr_2", now0); !ok {
		t.Fatal("別の利用者まで待たされている")
	}
	us.conv = &conversation{sessionID: "ses_1"}
	m.end("usr_1", now0)
	if _, ok := m.begin("usr_1", now0); !ok {
		t.Fatal("処理が終わっても受け付けない")
	}
}

// 期限が切れた会話は捨てる。未保存の下書きと確認を持ち越さない。
func TestConversationExpires(t *testing.T) {
	m := newMemory()
	us, _ := m.begin("usr_1", now0)
	us.conv = &conversation{sessionID: "ses_1", confirm: &confirmation{draftID: "d1"}}
	us.misses = 2
	m.end("usr_1", now0)

	us, _ = m.begin("usr_1", now0.Add(convTTL+time.Minute))
	if us.conv != nil {
		t.Fatal("期限切れの会話が残っている")
	}
	if us.misses != 2 {
		t.Fatal("空振りの回数は会話の期限で戻さない")
	}
}

// Gateway の再送で同じイベントを二度処理しない。
func TestFirstSeen(t *testing.T) {
	m := newMemory()
	if !m.firstSeen("msg:1", now0) {
		t.Fatal("初回が重複扱い")
	}
	if m.firstSeen("msg:1", now0) {
		t.Fatal("再送を処理した")
	}
	if !m.firstSeen("int:1", now0) {
		t.Fatal("種類が違うIDまで重複扱い")
	}
}

func TestSplitCustomID(t *testing.T) {
	kind, token, arg := splitCustomID("target:abc:ses_1")
	if kind != "target" || token != "abc" || arg != "ses_1" {
		t.Fatalf("target = %q %q %q", kind, token, arg)
	}
	kind, token, arg = splitCustomID("save:abc")
	if kind != "save" || token != "abc" || arg != "" {
		t.Fatalf("save = %q %q %q", kind, token, arg)
	}
	if kind, _, _ := splitCustomID("save"); kind != "" {
		t.Fatalf("形式の違う custom_id を受け付けた: %q", kind)
	}
}

func TestIsCommand(t *testing.T) {
	if !isCommand("取消。", "取消", "キャンセル") {
		t.Fatal("句点付きの定型入力を認識しない")
	}
	if isCommand("取消したくないです", "取消") {
		t.Fatal("自由文を定型入力と誤認した")
	}
}

// 節名などの可変文字列は装飾記号を打ち消す。
func TestEscape(t *testing.T) {
	if got := escape("*強調*と`コード`"); got != "\\*強調\\*と\\`コード\\`" {
		t.Fatalf("escape = %q", got)
	}
}

// 会話の件数が上限に達したら、使われていない状態から捨てる。休止中の利用者は残す。
func TestEvictKeepsActiveAndCooldown(t *testing.T) {
	m := newMemory()
	us, _ := m.begin("usr_idle", now0)
	m.end("usr_idle", now0)
	us, _ = m.begin("usr_talking", now0)
	us.conv = &conversation{sessionID: "ses_1"}
	m.end("usr_talking", now0)
	us, _ = m.begin("usr_cool", now0)
	us.cooldownUntil = now0.Add(cooldownFor)
	m.end("usr_cool", now0)

	m.evict(now0)
	if _, ok := m.users["usr_idle"]; ok {
		t.Fatal("使っていない状態が残っている")
	}
	if _, ok := m.users["usr_talking"]; !ok {
		t.Fatal("会話中の状態を捨てた")
	}
	if _, ok := m.users["usr_cool"]; !ok {
		t.Fatal("休止中の状態を捨てた")
	}
}

// 冪等キーは表示した下書きごとに決まる。連打では同じキー、下書きが変われば別のキー。
func TestIdemKeyBindsToDraft(t *testing.T) {
	c := &confirmation{draftID: "d1", state: coordState("attending", 15)}
	k1, k2 := idemKey("usr_1", c), idemKey("usr_1", c)
	if k1 == nil || *k1 != *k2 {
		t.Fatalf("同じ下書きでキーが変わる: %+v %+v", k1, k2)
	}
	if k1.UserID != "usr_1" || k1.Key != "d1" || !strings.Contains(k1.Path, "ses_1") {
		t.Fatalf("キーの内容 = %+v", k1)
	}
	// 値が変われば本文ハッシュも変わる（表示と違う値を同じ保存として扱わない）。
	other := &confirmation{draftID: "d1", state: coordState("attending", 30)}
	if idemKey("usr_1", other).BodyHash == k1.BodyHash {
		t.Fatal("値が変わってもハッシュが同じ")
	}
	// 別の利用者のキーとは混ざらない。
	if idemKey("usr_2", c).UserID == k1.UserID {
		t.Fatal("利用者が混ざっている")
	}
}

func coordState(attendance string, minutes int) coord.DialogState {
	data := fmt.Sprintf(`{"willing_to_present":true,"prepared_section_ids":["sec_2"],"explainable_section_ids":["sec_2"],"max_presentation_minutes":%d}`, minutes)
	return coord.DialogState{SessionID: "ses_1", Revision: 3, Attendance: attendance, Data: []byte(data), Unclear: []string{}}
}

// 確認表示には開催回・日時・全項目と、操作と世代だけを入れたボタンを出す。
func TestConfirmTextAndButtons(t *testing.T) {
	conv := &conversation{sessionID: "ses_1", title: "第2回", startsAt: now0}
	text := confirmText(conv, []string{"参加：参加", "説明できる時間：15分"})
	for _, want := range []string{"第2回", "9/20 21:00", "参加：参加", "説明できる時間：15分", msgConfirmAsk} {
		if !strings.Contains(text, want) {
			t.Fatalf("確認表示に %q がない: %q", want, text)
		}
	}
	rows := confirmButtons("d1")
	buttons := rows[0].(discordgo.ActionsRow).Components
	if len(buttons) != 2 {
		t.Fatalf("ボタン = %d 個", len(buttons))
	}
	for _, c := range buttons {
		id := c.(discordgo.Button).CustomID
		if !strings.HasSuffix(id, ":d1") {
			t.Fatalf("custom_id に下書きの世代がない: %q", id)
		}
		// 値は custom_id に埋めない。
		if strings.Contains(id, "15") || strings.Contains(id, "attending") {
			t.Fatalf("custom_id に値が入っている: %q", id)
		}
	}
}

// 対象の選択ボタンは候補の世代に束縛し、1行5個までに分ける。
func TestTargetButtons(t *testing.T) {
	var targets []coord.DialogTarget
	for i := 0; i < 7; i++ {
		targets = append(targets, coord.DialogTarget{SessionID: fmt.Sprintf("ses_%d", i), Title: "第1回", StartsAt: now0, HasOpenTask: i == 0})
	}
	rows := targetButtons("sel_1", targets)
	if len(rows) != 2 {
		t.Fatalf("行数 = %d", len(rows))
	}
	first := rows[0].(discordgo.ActionsRow).Components
	if len(first) != 5 {
		t.Fatalf("1行目のボタン = %d 個", len(first))
	}
	b := first[0].(discordgo.Button)
	if b.CustomID != "target:sel_1:ses_0" {
		t.Fatalf("custom_id = %q", b.CustomID)
	}
	if !strings.HasPrefix(b.Label, "未回答：") {
		t.Fatalf("未回答の印がない: %q", b.Label)
	}
	if len(rows[1].(discordgo.ActionsRow).Components) != 2 {
		t.Fatal("2行目に残りが入っていない")
	}
	if got := trimLabel(strings.Repeat("あ", 100)); len([]rune(got)) != 80 {
		t.Fatalf("ボタンの表示名が上限を超える: %d 文字", len([]rune(got)))
	}
}

// 扱えない依頼は種類ごとに案内を変える。分類は案内にだけ使う。
func TestOutOfScopeHint(t *testing.T) {
	seen := map[string]bool{}
	for _, kind := range append(coord.OutOfScopeKinds, "") {
		h := outOfScopeHint(kind)
		if !strings.HasPrefix(h, msgOutOfScope) {
			t.Fatalf("%q の案内 = %q", kind, h)
		}
		seen[h] = true
	}
	if len(seen) < 4 {
		t.Fatalf("案内が種類ごとに分かれていない: %d 種", len(seen))
	}
}

// 送る本文は Discord の上限に収め、Webリンクは対象の開催回を指す。
func TestClipContentAndWebLink(t *testing.T) {
	if got := clipContent(strings.Repeat("あ", 2100)); len([]rune(got)) != 2000 {
		t.Fatalf("本文の長さ = %d", len([]rune(got)))
	}
	if got := webLink("http://x", "ses_1"); got != "\nhttp://x/session.html?id=ses_1" {
		t.Fatalf("リンク = %q", got)
	}
	if got := webLink("http://x", ""); got != "\nhttp://x" {
		t.Fatalf("リンク = %q", got)
	}
	if webLink("", "ses_1") != "" {
		t.Fatal("基点がないのにリンクを出した")
	}
}
