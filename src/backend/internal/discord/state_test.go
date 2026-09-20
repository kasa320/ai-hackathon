package discord

import (
	"testing"
	"time"
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
