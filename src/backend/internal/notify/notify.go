// Package notify は Discord への通知を扱う。送信キューは DB（notifications）に置き、
// 計画の確定と通知の成否を分けて記録する。成否が分からない送信は unknown とし、無条件に再送しない。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var (
	// ErrDeliveryFailed は送信されなかったことが確実な失敗。
	ErrDeliveryFailed = errors.New("notify: delivery failed")
	// ErrDeliveryUnknown は送信されたかどうか分からない失敗（タイムアウト・5xx など）。
	ErrDeliveryUnknown = errors.New("notify: delivery unknown")
	// ErrRetryLater は送信前に拒否された（レート制限など）ため、後で送り直せる。
	ErrRetryLater = errors.New("notify: retry later")
)

type Message struct {
	Content string
	// MentionUserIDs はメンションを許可する Discord ユーザーID。
	MentionUserIDs []string
	// Kind は通知の種類（task_requested / reminder / plan_confirmed / needs_owner）。
	Kind string
	// DMUserIDs は DM を試す相手。本人だけが知ればよい依頼に使う。
	// DM が使えないことが確実なときだけチャンネルへ退避する。
	DMUserIDs []string
}

// DMKinds は本人宛てに DM を試す通知の種類。確定の連絡や管理者への差し戻しは全員が知るべき情報なので
// チャンネルへ送る。
var DMKinds = map[string]bool{"task_requested": true, "reminder": true}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

// maxContentLen は Discord のメッセージ長の上限。
const maxContentLen = 2000

// DiscordSender は Bot で指定チャンネルにメッセージを送る。
type DiscordSender struct {
	BaseURL   string
	Token     string
	ChannelID string
	HTTP      *http.Client
}

func NewDiscordSender(token, channelID string) *DiscordSender {
	return &DiscordSender{BaseURL: "https://discord.com/api/v10", Token: token, ChannelID: channelID, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Send は通知を送る。本人宛ての依頼はまず DM を試し、DM が使えないことが確実なときだけ
// チャンネルへ退避する。成否が分からない送信は退避せず、到達済みの DM と二重に送らない。
func (d *DiscordSender) Send(ctx context.Context, m Message) error {
	content := m.Content
	if utf8.RuneCountInString(content) > maxContentLen {
		content = string([]rune(content)[:maxContentLen])
	}
	users := m.MentionUserIDs
	if users == nil {
		users = []string{}
	}
	if len(m.DMUserIDs) == 1 && DMKinds[m.Kind] {
		err := d.sendDM(ctx, m.DMUserIDs[0], content, users)
		if err == nil || !errors.Is(err, ErrDeliveryFailed) {
			return err
		}
		// DM 拒否・共通サーバーなしなど、送信されていないことが確実な失敗だけ退避する。
	}
	return d.sendChannel(ctx, d.ChannelID, content, users)
}

// sendDM は相手との DM チャンネルを開いて送る。開けなければその失敗をそのまま返す。
func (d *DiscordSender) sendDM(ctx context.Context, userID, content string, mentions []string) error {
	body, _ := json.Marshal(map[string]any{"recipient_id": userID})
	res, err := d.post(ctx, "/users/@me/channels", body)
	if err != nil {
		return err
	}
	var ch struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res, &ch); err != nil || ch.ID == "" {
		return fmt.Errorf("%w: DM チャンネルを開けません", ErrDeliveryFailed)
	}
	return d.sendChannel(ctx, ch.ID, content, mentions)
}

func (d *DiscordSender) sendChannel(ctx context.Context, channelID, content string, mentions []string) error {
	body, _ := json.Marshal(map[string]any{
		"content":          content,
		"allowed_mentions": map[string]any{"parse": []string{}, "users": mentions},
	})
	_, err := d.post(ctx, "/channels/"+channelID+"/messages", body)
	return err
}

// post は Discord API を呼び、成否の分かる失敗と分からない失敗を区別する。
func (d *DiscordSender) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDeliveryFailed, err)
	}
	req.Header.Set("Authorization", "Bot "+d.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := d.HTTP.Do(req)
	if err != nil {
		// 接続できなかった場合は送信されていない。それ以外（タイムアウト等）は成否不明。
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return nil, fmt.Errorf("%w: %v", ErrDeliveryFailed, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrDeliveryUnknown, err)
	}
	defer res.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return payload, nil
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, ErrRetryLater
	case res.StatusCode >= 500:
		return nil, fmt.Errorf("%w: status %d", ErrDeliveryUnknown, res.StatusCode)
	default:
		return nil, fmt.Errorf("%w: status %d", ErrDeliveryFailed, res.StatusCode)
	}
}

// LogSender は Discord が未設定のときにログへ出すだけの送信先（開発用）。
type LogSender struct {
	Log *slog.Logger
}

func (l LogSender) Send(_ context.Context, m Message) error {
	l.Log.Info("通知（Discord 未設定のためログ出力）", "content", m.Content)
	return nil
}

// WithFaults は開発モードの障害注入（notify: fail / unknown）を反映する送信先を返す。
func WithFaults(next Sender, faults *fault.Registry) Sender {
	return faultSender{next: next, faults: faults}
}

type faultSender struct {
	next   Sender
	faults *fault.Registry
}

func (f faultSender) Send(ctx context.Context, m Message) error {
	switch f.faults.Get("notify") {
	case "fail":
		return fmt.Errorf("%w: 障害注入", ErrDeliveryFailed)
	case "unknown":
		return fmt.Errorf("%w: 障害注入", ErrDeliveryUnknown)
	}
	return f.next.Send(ctx, m)
}

// Dispatcher は通知待ちを1件ずつ送る。送信前に sending にし、再起動時に sending のものは unknown にする。
type Dispatcher struct {
	st     *store.Store
	clock  clock.Clock
	sender Sender
	log    *slog.Logger
}

func NewDispatcher(st *store.Store, clk clock.Clock, sender Sender, log *slog.Logger) *Dispatcher {
	return &Dispatcher{st: st, clock: clk, sender: sender, log: log}
}

func (d *Dispatcher) now() time.Time { return d.clock.Now().UTC().Truncate(time.Millisecond) }

// Recover は送信途中で停止した通知を成否不明にする。起動時に1回呼ぶ。二重送信を避けるため再送しない。
func (d *Dispatcher) Recover(ctx context.Context) error {
	return d.st.Tx(ctx, func(tx *store.Tx) error {
		now := d.now()
		list, err := tx.MarkInterruptedNotificationsUnknown(ctx, now)
		if err != nil {
			return err
		}
		for _, n := range list {
			if err := tx.AddActivity(ctx, store.Activity{SessionID: n.SessionID, CaseID: n.CaseID, Kind: "notification_updated",
				Summary: "送信中に停止したため、通知の成否は不明です。", OccurredAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
}

// DispatchPending は送信待ちの通知をすべて送る。送った件数を返す。
func (d *Dispatcher) DispatchPending(ctx context.Context) (int, error) {
	n := 0
	for {
		var ntf store.Notification
		err := d.st.Tx(ctx, func(tx *store.Tx) error {
			var err error
			ntf, err = tx.ClaimNotification(ctx, d.now())
			return err
		})
		if errors.Is(err, store.ErrNotFound) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		msg := Message{Content: ntf.Content, MentionUserIDs: ntf.Mentions, Kind: ntf.Kind}
		if DMKinds[ntf.Kind] {
			// 本人宛ての依頼は DM を試す。宛先は通知が名指しした本人だけ。
			msg.DMUserIDs = ntf.Mentions
		}
		sendErr := d.sender.Send(ctx, msg)
		status, code, summary := store.NotifySent, "", "通知を送信しました（Discord の成功応答）。"
		switch {
		case errors.Is(sendErr, ErrRetryLater):
			status, summary = store.NotifyPending, ""
		case errors.Is(sendErr, ErrDeliveryFailed):
			status, code, summary = store.NotifyFailed, "delivery_failed", "通知の送信に失敗しました。"
		case sendErr != nil:
			status, code, summary = store.NotifyUnknown, "delivery_unknown", "通知の成否を確認できませんでした。"
		}
		if sendErr != nil {
			d.log.Warn("通知の送信に失敗", "notification", ntf.ID, "status", status, "err", sendErr)
		}
		err = d.st.Tx(ctx, func(tx *store.Tx) error {
			now := d.now()
			if err := tx.SetNotificationStatus(ctx, ntf.ID, status, code, now); err != nil {
				return err
			}
			if summary == "" {
				return nil
			}
			return tx.AddActivity(ctx, store.Activity{SessionID: ntf.SessionID, CaseID: ntf.CaseID, Kind: "notification_updated", Summary: summary, OccurredAt: now})
		})
		if err != nil {
			return n, err
		}
		if status == store.NotifyPending {
			// レート制限中は次の周期まで待つ。
			return n, nil
		}
		n++
	}
}

// Run は通知待ちを送り続ける。
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := d.DispatchPending(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("通知処理に失敗", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
