package reading_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

func TestScaffoldCannotApproveAPlan(t *testing.T) {
	p := reading.New()
	ctx := context.Background()
	state := coord.Snapshot{}
	plan := coord.Proposal{}

	if _, err := p.BuildContext(ctx, state); !errors.Is(err, coord.ErrNotImplemented) {
		t.Fatalf("BuildContext must report not implemented: %v", err)
	}
	if err := p.ValidatePlan(ctx, state, plan); !errors.Is(err, coord.ErrNotImplemented) {
		t.Fatalf("ValidatePlan must not accept a plan: %v", err)
	}
	if _, err := p.ApprovalRequirements(ctx, state, plan); !errors.Is(err, coord.ErrNotImplemented) {
		t.Fatalf("ApprovalRequirements must not imply no approval is needed: %v", err)
	}
}
