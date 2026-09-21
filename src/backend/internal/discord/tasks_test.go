package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

type discordTransport struct{ bodies []string }

func (f *discordTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, string(b))
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":"msg%d","channel_id":"dm"}`, len(f.bodies))))}, nil
}

func taskBot(t *testing.T) (*Bot, *userSession, string, *discordTransport) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var a, b store.User
	err = st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		a, err = tx.UpsertUser(ctx, "111111111111111111", "A", now0)
		if err != nil {
			return err
		}
		b, err = tx.UpsertUser(ctx, "222222222222222222", "B", now0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := coord.NewService(reading.New())
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.Fixed{T: now0}
	co := coord.NewCoordinator(svc, st, clk, coord.DraftOnlyPlanner{}, coord.Options{Interpreter: coord.DraftOnlyInterpreter{}})
	res, err := co.CreateGroup(ctx, a.ID, apitypes.CreateGroupInput{Name: "輪読", Invitees: []apitypes.Invitee{{DiscordUserID: "222222222222222222", DisplayName: "B"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var group apitypes.Group
	json.Unmarshal(res.Body, &group)
	sessionData := json.RawMessage(`{"book_title":"本","isbn":null,"toc_source":{"kind":"manual","urls":[]},"sections":[{"id":"s1","title":"第一章"}],"completed_section_ids":[],"target_section_ids":["s1"]}`)
	res, err = co.CreateSession(ctx, a.ID, group.ID, apitypes.CreateSessionInput{PlaybookID: "reading", PeriodStart: "2026-09-21", PeriodEnd: "2026-09-30", DurationMinutes: 60, Data: sessionData}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var created apitypes.SessionCreated
	json.Unmarshal(res.Body, &created)
	for _, u := range []store.User{a, b} {
		d, err := co.SessionDetail(ctx, u.ID, created.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		data := json.RawMessage(`{"declined_presentation":false,"unavailable_dates":[],"schedule":{"status":"provided","weekly_windows":[{"weekday":3,"start":"20:00","end":"22:00"}],"date_windows":[],"max_duration_minutes":60}}`)
		_, err = co.PutPreparation(ctx, u.ID, created.Session.ID, apitypes.PutPreparationInput{ExpectedRevision: &d.Session.Revision, Preparation: &apitypes.Preparation{Attendance: "attending", Data: data}}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := co.ProcessDue(ctx); err != nil {
		t.Fatal(err)
	}
	bot, err := New(Deps{Token: "fake", Coord: co, Clock: clk, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PublicBaseURL: "http://localhost"})
	if err != nil {
		t.Fatal(err)
	}
	f := &discordTransport{}
	bot.dg.Client = &http.Client{Transport: f}
	us := &userSession{conv: &conversation{sessionID: created.Session.ID}}
	bot.replyTasks(ctx, "dm", b.ID, us)
	if len(us.conv.taskCards) == 0 {
		t.Fatal("no consent card sent")
	}
	return bot, us, b.ID, f
}

func TestDiscordConsentIsBoundToUserMessageAndProposal(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong_message", "wrong_channel", "wrong_user", "invalid_decision", "expired_memory", "stale_proposal"} {
		t.Run(scenario, func(t *testing.T) {
			b, us, userID, _ := taskBot(t)
			var token string
			var card taskCard
			for k, c := range us.conv.taskCards {
				if c.value.Task.Kind == "approval" {
					token, card = k, c
					break
				}
			}
			if token == "" {
				t.Fatal("no approval card")
			}
			i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "int", AppID: "app", Token: "fake", ChannelID: "dm", Message: &discordgo.Message{ID: card.messageID}}}
			decision := "approve"
			sessionID := us.conv.sessionID
			switch scenario {
			case "wrong_message":
				i.Message.ID = "other"
			case "wrong_channel":
				i.ChannelID = "other"
			case "wrong_user":
				userID = "other"
			case "invalid_decision":
				decision = "submit"
			case "expired_memory":
				us.conv = nil
			case "stale_proposal":
				d, err := b.co.SessionDetail(context.Background(), userID, sessionID)
				if err != nil {
					t.Fatal(err)
				}
				var p *apitypes.Preparation
				for _, prep := range d.Preparations {
					if prep.MemberID == d.CurrentMemberID {
						p = prep.Value
					}
				}
				var fields map[string]any
				json.Unmarshal(p.Data, &fields)
				fields["unavailable_dates"] = []string{"2026-09-23"}
				p.Data, _ = json.Marshal(fields)
				if _, err := b.co.PutPreparation(context.Background(), userID, sessionID, apitypes.PutPreparationInput{ExpectedRevision: &d.Session.Revision, Preparation: p}, nil); err != nil {
					t.Fatal(err)
				}
			}
			b.onTaskButton(context.Background(), i, userID, us, token, decision)
			// The original member's view is resolved from Discord identity, never the payload.
			original, err := b.co.UserByDiscordID(context.Background(), "222222222222222222")
			if err != nil {
				t.Fatal(err)
			}
			d, err := b.co.SessionDetail(context.Background(), original.ID, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			for _, tk := range d.MyTasks {
				if tk.ID == card.value.Task.ID {
					if scenario == "valid" && tk.Status != "answered" {
						t.Fatalf("valid explicit answer not saved: %s", tk.Status)
					}
					if scenario != "valid" && tk.Status == "answered" {
						t.Fatal("invalid control accepted")
					}
				}
			}
		})
	}
}

func TestDiscordDoesNotConfirmTruncatedConditions(t *testing.T) {
	b, us, _, transport := taskBot(t)
	res := coord.DialogResult{Ready: true, Confirm: []string{strings.Repeat("条件", 1200)}}
	b.reply(context.Background(), "dm", us, res)
	if us.conv.confirm != nil {
		t.Fatal("truncated conditions became confirmable")
	}
	if strings.Contains(transport.bodies[len(transport.bodies)-1], "save:") {
		t.Fatal("save button sent without full conditions")
	}
	us.conv.taskCards = map[string]taskCard{"old": {}}
	b.clearDraft(context.Background(), us)
	if len(us.conv.taskCards) != 0 {
		t.Fatal("old consent buttons remain after correction")
	}
}
