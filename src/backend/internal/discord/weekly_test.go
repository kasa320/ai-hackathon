package discord

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func weeklyBot(t *testing.T) (*Bot, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "weekly.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var u store.User
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		u, err = tx.UpsertUser(ctx, "222222222222222222", "B", now0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := coord.NewService(reading.New())
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.Fixed{T: now0}
	co := coord.NewCoordinator(svc, st, clk, coord.DraftOnlyPlanner{}, coord.Options{})
	bot, err := New(Deps{Token: "fake", Coord: co, Clock: clk, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PublicBaseURL: "http://localhost"})
	if err != nil {
		t.Fatal(err)
	}
	bot.dg.Client = &http.Client{Transport: &discordTransport{}}
	return bot, u.ID
}

func weeklyConfirmUS() *userSession {
	return &userSession{weekly: &weeklyState{confirm: &weeklyConfirmation{
		draftID: "draft1", timezone: "Asia/Tokyo",
		windows: []apitypes.WeeklyWindow{{Weekday: 3, Start: "19:00", End: "22:00"}},
		lines:   []string{"水曜 19:00〜22:00"}, channelID: "dm", messageID: "msg1",
	}}}
}

// 保存ボタンは「表示した下書き」に束縛される：下書きの世代・メッセージ・チャンネルの
// いずれかが一致しなければ保存しない。
func TestWeeklySaveIsBoundToDraftMessageAndChannel(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong_draft", "wrong_message", "wrong_channel", "expired_memory"} {
		t.Run(scenario, func(t *testing.T) {
			bot, userID := weeklyBot(t)
			us := weeklyConfirmUS()
			i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "int", ChannelID: "dm", Message: &discordgo.Message{ID: "msg1"}}}
			token := "draft1"
			switch scenario {
			case "wrong_draft":
				token = "other"
			case "wrong_message":
				i.Message.ID = "other"
			case "wrong_channel":
				i.ChannelID = "other"
			case "expired_memory":
				us.weekly = nil
			}
			bot.onWeeklySaveButton(context.Background(), i, userID, us, token)
			out, err := bot.co.WeeklyAvailability(context.Background(), userID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "valid" {
				if len(out.Windows) != 1 || out.Windows[0].Start != "19:00" {
					t.Fatalf("保存されるはず: %+v", out)
				}
				if us.weekly != nil {
					t.Fatal("保存後は会話を終えるはず")
				}
			} else if len(out.Windows) != 0 {
				t.Fatalf("不正な操作で保存されてしまった: %+v", out)
			}
		})
	}
}

// 取消ボタンは下書きを破棄するだけで、保存はしない。
func TestWeeklyCancelDoesNotSave(t *testing.T) {
	bot, userID := weeklyBot(t)
	us := weeklyConfirmUS()
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "int", ChannelID: "dm", Message: &discordgo.Message{ID: "msg1"}}}
	bot.onWeeklyCancelButton(context.Background(), i, us, "draft1")
	if us.weekly != nil {
		t.Fatal("取消後も対話が残っている")
	}
	out, err := bot.co.WeeklyAvailability(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Windows) != 0 {
		t.Fatalf("取消しても保存されてしまった: %+v", out)
	}
}

// 訂正（新しい発言）を受け取ると、表示済みの確認は無効になり、古いボタンでは保存できない。
func TestWeeklyCorrectionInvalidatesOldDraft(t *testing.T) {
	bot, userID := weeklyBot(t)
	us := weeklyConfirmUS()
	old := us.weekly.confirm.draftID
	bot.clearWeeklyDraft(context.Background(), us)
	if us.weekly.confirm != nil {
		t.Fatal("訂正後も古い確認が残っている")
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "int", ChannelID: "dm", Message: &discordgo.Message{ID: "msg1"}}}
	bot.onWeeklySaveButton(context.Background(), i, userID, us, old)
	out, err := bot.co.WeeklyAvailability(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Windows) != 0 {
		t.Fatal("無効化された古いボタンで保存できてしまった")
	}
}

// 空き時間の対話を始めると、参加条件の対話（対象の開催回）とは排他になる。
func TestBeginWeeklyDialogClearsSessionConversation(t *testing.T) {
	bot, _ := weeklyBot(t)
	us := &userSession{conv: &conversation{sessionID: "sess_1"}}
	bot.beginWeeklyDialog(context.Background(), "dm", us)
	if us.conv != nil {
		t.Fatal("参加条件の対話が残っている")
	}
	if us.weekly == nil {
		t.Fatal("週間空き時間の対話が始まっていない")
	}
}
