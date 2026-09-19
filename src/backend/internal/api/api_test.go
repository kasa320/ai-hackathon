package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/api"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

type healthyDB struct{}

func (healthyDB) Ping(context.Context) error { return nil }

func TestRegisteredPlaybooksReachAPI(t *testing.T) {
	coordinator, err := coord.NewService(reading.New())
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(healthyDB{}, clock.Real{}, slog.New(slog.NewTextHandler(io.Discard, nil)), coordinator)
	handler := srv.Handler(t.TempDir())
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/playbooks", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	var body struct {
		Playbooks []coord.Descriptor `json:"playbooks"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Playbooks) != 1 || body.Playbooks[0].ID != reading.ID {
		t.Fatalf("unexpected catalog: %+v", body.Playbooks)
	}

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("health regression: status = %d", res.Code)
	}
}
