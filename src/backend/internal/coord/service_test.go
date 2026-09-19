package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// 共通側の変更なしに別パッケージから用途を追加できることを確認する。
// 会議機能を実装したことにはしない。
type examplePlaybook struct{ id string }

func (p examplePlaybook) Descriptor() coord.Descriptor {
	return coord.Descriptor{ID: p.id, Name: "検証用"}
}
func (examplePlaybook) BuildContext(context.Context, coord.Snapshot) (json.RawMessage, error) {
	return nil, coord.ErrNotImplemented
}
func (examplePlaybook) Instructions() string { return "検証用の指示" }
func (examplePlaybook) ValidatePlan(context.Context, coord.Snapshot, coord.Proposal) error {
	return coord.ErrNotImplemented
}
func (examplePlaybook) ApprovalRequirements(context.Context, coord.Snapshot, coord.Proposal) (coord.ApprovalRequirements, error) {
	return coord.ApprovalRequirements{}, coord.ErrNotImplemented
}

func TestExternalPlaybooksAreResolvedIndependently(t *testing.T) {
	s, err := coord.NewService(examplePlaybook{id: "first"}, examplePlaybook{id: "second"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		p, err := s.Playbook(id)
		if err != nil || p.Descriptor().ID != id {
			t.Fatalf("Playbook(%q) = %v, %v", id, p, err)
		}
	}
	if _, err := s.Playbook("unknown"); !errors.Is(err, coord.ErrUnknownPlaybook) {
		t.Fatalf("unknown ID must fail, got %v", err)
	}
	// 外部に渡した一覧の変更で登録情報が書き換わらない。
	s.Playbooks()[0].ID = "changed"
	if got := s.Playbooks()[0].ID; got != "first" {
		t.Fatalf("catalog was mutated: %s", got)
	}
}

func TestDuplicatePlaybookDoesNotOverrideRegistration(t *testing.T) {
	_, err := coord.NewService(examplePlaybook{id: "same"}, examplePlaybook{id: "same"})
	if err == nil {
		t.Fatal("duplicate ID must fail at startup")
	}
}
