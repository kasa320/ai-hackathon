package reading

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// decodeStrict は未知のフィールドを拒否してデコードする。
func decodeStrict(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("値がありません")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("形式が正しくありません: %v", err)
	}
	if dec.More() {
		return fmt.Errorf("余分なデータがあります")
	}
	return nil
}

func decodeError(err error) error {
	v := &coord.ValidationError{}
	v.Add("", "%v", err)
	return v
}

func textLen(s string) int { return utf8.RuneCountInString(s) }

func validID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && len(id) <= maxIDLen
}

type idSet map[string]struct{}

func newIDSet(ids []string) idSet {
	s := make(idSet, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

func (s idSet) has(id string) bool { _, ok := s[id]; return ok }

// checkIDList は重複と未登録のIDを検出する。
func checkIDList(v *coord.ValidationError, path string, ids []string, known idSet) {
	seen := idSet{}
	for i, id := range ids {
		p := fmt.Sprintf("%s[%d]", path, i)
		if seen.has(id) {
			v.Add(p, "同じ節が重複しています")
		}
		seen[id] = struct{}{}
		if known != nil && !known.has(id) {
			v.Add(p, "登録されていない節です")
		}
	}
}

func sameSet(a, b []string) bool {
	sa, sb := newIDSet(a), newIDSet(b)
	if len(sa) != len(sb) {
		return false
	}
	for id := range sa {
		if !sb.has(id) {
			return false
		}
	}
	return true
}

func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
