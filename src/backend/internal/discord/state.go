// Package discord は DM での対話から参加条件を更新する常駐Bot
// （.agent/kasa/decisions/discord-interactive-agent.md）。
//
// ここが持つのは Discord の入出力と会話の進行だけで、保存・認可・検証は coord に任せる。
// 会話の状態はメモリにだけ置き、発言の原文・未保存の下書きは永続化しない。
package discord

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

const (
	// convTTL は会話を保つ時間（最終操作から）。過ぎたら最初から確認し直す。
	convTTL = 30 * time.Minute
	// missLimit と cooldownFor は空振りが続いたときの休止。どちらも初期値。
	missLimit   = 3
	cooldownFor = 10 * time.Minute
	// dedupeTTL は同じイベントを二度処理しないために覚えておく時間。
	dedupeTTL = 10 * time.Minute

	maxConversations = 500
	maxSeenEvents    = 5000
)

// confirmation は本人に表示した確認の不変スナップショット。保存するのはこの値で、
// 表示後に変わった下書きではない。
type confirmation struct {
	draftID   string
	state     coord.DialogState
	lines     []string
	channelID string
	messageID string
}

// conversation は1人の利用者の進行中の会話。対象の開催回を束縛する。
type conversation struct {
	sessionID string
	title     string
	startsAt  time.Time
	state     coord.DialogState
	// draftID は下書きの世代。値・確定状態・対象・読み込んだ版が変わるたびに振り直す。
	draftID string
	confirm *confirmation
	// targets は対象を選んでもらっている最中の候補。selectID はそのボタンの世代。
	targets  []coord.DialogTarget
	selectID string
}

// userSession は利用者ごとの状態。空振りの回数と休止は開催回の切替で戻さない。
type userSession struct {
	busy          bool
	conv          *conversation
	expiresAt     time.Time
	misses        int
	cooldownUntil time.Time
}

// memory は会話の置き場所。再起動で消えてよい値だけを持つ。
type memory struct {
	mu    sync.Mutex
	users map[string]*userSession
	seen  map[string]time.Time
}

func newMemory() *memory {
	return &memory{users: map[string]*userSession{}, seen: map[string]time.Time{}}
}

// begin は利用者の状態を取り出して排他を取る。処理中なら ok=false。
// DM とボタン操作は利用者ごとに直列化し、短い返答が別の質問に結びつくのを防ぐ。
func (m *memory) begin(userID string, now time.Time) (*userSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	us, ok := m.users[userID]
	if !ok {
		if len(m.users) >= maxConversations {
			m.evict(now)
		}
		us = &userSession{}
		m.users[userID] = us
	}
	if us.busy {
		return nil, false
	}
	if us.conv != nil && !now.Before(us.expiresAt) {
		// 期限切れの会話は捨てる。未保存の下書きも確認も残さない。
		us.conv = nil
	}
	us.busy = true
	return us, true
}

func (m *memory) end(userID string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	us, ok := m.users[userID]
	if !ok {
		return
	}
	us.busy = false
	if us.conv != nil {
		us.expiresAt = now.Add(convTTL)
	}
}

// evict は期限の切れた利用者の状態を捨てる。件数の上限を超えたときに呼ぶ。
func (m *memory) evict(now time.Time) {
	for id, us := range m.users {
		if !us.busy && us.conv == nil && now.After(us.cooldownUntil) {
			delete(m.users, id)
		}
	}
}

// firstSeen は初めて見るイベントIDなら true を返す。Gateway は同じイベントを再送しうる。
// メモリ上の重複排除は再起動を越えないため、保存の重複防止は冪等キーに任せる。
func (m *memory) firstSeen(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.seen[id]; ok {
		return false
	}
	if len(m.seen) >= maxSeenEvents {
		for k, t := range m.seen {
			if now.Sub(t) > dedupeTTL {
				delete(m.seen, k)
			}
		}
	}
	m.seen[id] = now
	return true
}

// newToken は推測困難な短い識別子。下書きとボタンの世代に使う。
func newToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 乱数が取れない環境では時刻で代用する（衝突しても所有者と期限で弾く）。
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))[:16]
	}
	return hex.EncodeToString(b[:])
}
