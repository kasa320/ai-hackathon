package coord

import (
	"fmt"
	"sort"
	"strings"
)

// Service は起動時に登録された用途（Playbook）の一覧。
// 状態遷移・同意検証・永続化は Coordinator が担い、用途の解決にこの一覧を使う。
type Service struct {
	playbooks   map[string]Playbook
	descriptors []Descriptor
}

func NewService(playbooks ...Playbook) (*Service, error) {
	s := &Service{playbooks: make(map[string]Playbook), descriptors: make([]Descriptor, 0, len(playbooks))}
	for _, p := range playbooks {
		if p == nil {
			return nil, fmt.Errorf("playbook must not be nil")
		}
		d := p.Descriptor()
		if strings.TrimSpace(d.ID) == "" || d.ID != strings.TrimSpace(d.ID) || strings.TrimSpace(d.Name) == "" {
			return nil, fmt.Errorf("invalid playbook descriptor: %+v", d)
		}
		if _, exists := s.playbooks[d.ID]; exists {
			return nil, fmt.Errorf("duplicate playbook ID: %s", d.ID)
		}
		s.playbooks[d.ID] = p
		s.descriptors = append(s.descriptors, d)
	}
	sort.Slice(s.descriptors, func(i, j int) bool { return s.descriptors[i].ID < s.descriptors[j].ID })
	return s, nil
}

// Playbooks は登録済み用途のコピーを返す。登録済みでも業務処理の完成は意味しない。
func (s *Service) Playbooks() []Descriptor {
	return append([]Descriptor{}, s.descriptors...)
}

// Playbook は用途IDから実装を解決する。未知の用途を輪読で代用しない。
func (s *Service) Playbook(id string) (Playbook, error) {
	p, ok := s.playbooks[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPlaybook, id)
	}
	return p, nil
}
