package reading

import (
	"context"
	"encoding/json"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

const ID = "reading"

// Playbook は輪読用の実装。業務処理は未実装で、呼び出すと明示的に停止する。
type Playbook struct{}

var _ coord.Playbook = Playbook{}

func New() Playbook { return Playbook{} }

func (Playbook) Descriptor() coord.Descriptor {
	return coord.Descriptor{ID: ID, Name: "輪読"}
}

func (Playbook) BuildContext(context.Context, coord.Snapshot) (json.RawMessage, error) {
	// TODO: State を検証し、本人確認済みの共有可能な情報だけを組み立てる。
	return nil, coord.ErrNotImplemented
}
