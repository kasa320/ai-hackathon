// Package fault は開発モードの障害注入の設定を保持する。
// 値は /api/dev/faults からだけ変更でき、開発モードでなければ常に空（障害なし）。
package fault

import (
	"fmt"
	"sort"
	"sync"
)

// Registry は障害注入の設定。nil でも安全に使え、その場合は常に障害なし。
type Registry struct {
	mu      sync.RWMutex
	allowed map[string][]string
	values  map[string]string
}

func New() *Registry {
	return &Registry{allowed: map[string][]string{}, values: map[string]string{}}
}

// Define は設定できるキーと値を登録する（例：llm → error, invalid_output）。
func (r *Registry) Define(key string, values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowed[key] = values
}

// Get は現在の設定値を返す。未設定なら空文字。
func (r *Registry) Get(key string) string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.values[key]
}

// Replace はすべての設定を置き換える。指定のないキーは null（障害なし）になる。
func (r *Registry) Replace(values map[string]*string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := map[string]string{}
	for k, v := range values {
		allowed, ok := r.allowed[k]
		if !ok {
			return fmt.Errorf("未知のキーです: %s", k)
		}
		if v == nil {
			continue
		}
		valid := false
		for _, a := range allowed {
			valid = valid || a == *v
		}
		if !valid {
			return fmt.Errorf("%s に指定できる値は %v です", k, allowed)
		}
		next[k] = *v
	}
	r.values = next
	return nil
}

// Snapshot は登録済みの全キーの現在値を返す（未設定は nil）。
func (r *Registry) Snapshot() map[string]*string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.allowed))
	for k := range r.allowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]*string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			v := v
			out[k] = &v
		} else {
			out[k] = nil
		}
	}
	return out
}
