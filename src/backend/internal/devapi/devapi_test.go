package devapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/api"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/auth"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/devapi"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var t0 = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)

func handler(t *testing.T, dev bool) (http.Handler, *clock.Offset) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := clock.NewOffset(clock.Fixed{T: t0})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg, _ := coord.NewService(reading.New())
	lock := &sync.Mutex{}
	c := coord.NewCoordinator(reg, st, clk, coord.DraftOnlyPlanner{}, coord.Options{PublicBaseURL: "http://app.test", RunLock: lock})
	am := auth.NewManager(st, clk, nil, "s", false)
	faults := fault.New()
	faults.Define("llm", "error", "invalid_output")
	faults.Define("notify", "fail", "unknown")
	srv := api.New(api.Deps{DB: st, Clock: clk, Log: log, Coord: c, Auth: am, AllowedOrigins: []string{"http://app.test"}, DevMode: dev})
	var mounts []func(*http.ServeMux)
	if dev {
		mounts = append(mounts, devapi.Mount(devapi.Deps{Store: st, Clock: clk, Auth: am, Faults: faults, AllowedOrigins: []string{"http://app.test"}, Log: log,
			Seeder: devapi.NewSeeder(reg, st, clk, "http://app.test", lock), Wake: c.Wake, ProcessDue: c.ProcessDue}))
	}
	return srv.Handler(t.TempDir(), mounts...), clk
}

func call(h http.Handler, method, path, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "app.test"
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDevAPIIsAbsentWhenDisabled(t *testing.T) {
	h, _ := handler(t, false)
	for _, p := range []string{"/api/dev/status", "/api/dev/seed"} {
		if w := call(h, "GET", p, "", nil); w.Code != 404 {
			t.Fatalf("%s: 開発モードでなければ 404: %d", p, w.Code)
		}
	}
	if w := call(h, "GET", "/api/health", "", nil); !strings.Contains(w.Body.String(), `"dev_mode":false`) {
		t.Fatal(w.Body.String())
	}
}

func TestSeedLoginAndClock(t *testing.T) {
	h, clk := handler(t, true)
	if w := call(h, "GET", "/api/health", "", nil); !strings.Contains(w.Body.String(), `"dev_mode":true`) {
		t.Fatal("開発モードでは dev_mode=true")
	}
	w := call(h, "POST", "/api/dev/seed", `{"scenario":"replan_demo"}`, nil)
	if w.Code != 200 {
		t.Fatalf("seed: %d %s", w.Code, w.Body)
	}
	var seed devapi.SeedResult
	_ = json.Unmarshal(w.Body.Bytes(), &seed)

	w = call(h, "POST", "/api/dev/login", `{"discord_user_id":"100000000000000002"}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"csrf_token"`) {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	w = call(h, "GET", "/api/sessions/"+seed.SessionID, "", cookies)
	var d apitypes.SessionDetail
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if d.Session.Status != "confirmed" || d.ConfirmedPlan == nil || !d.Permissions.CanWithdrawAssignment {
		t.Fatalf("replan_demo の初期状態: %+v %+v", d.Session, d.Permissions)
	}
	if d.NotificationSummary != (apitypes.NotificationSummary{}) {
		t.Fatalf("投入時の通知は送らない: %+v", d.NotificationSummary)
	}
	if w := call(h, "POST", "/api/dev/login", `{"discord_user_id":"999999999999999999"}`, nil); w.Code != 404 {
		t.Fatalf("未登録の利用者: %d", w.Code)
	}

	w = call(h, "POST", "/api/dev/clock/advance", `{"seconds":86400}`, nil)
	if w.Code != 200 || !clk.Now().Equal(t0.Add(24*time.Hour)) || !strings.Contains(w.Body.String(), `"clock_offset_seconds":86400`) {
		t.Fatalf("advance: %d %s", w.Code, w.Body)
	}
	if w := call(h, "POST", "/api/dev/clock/advance", `{"seconds":-1}`, nil); w.Code != 422 {
		t.Fatalf("時計は戻せない: %d", w.Code)
	}
	if w := call(h, "POST", "/api/dev/seed", `{"scenario":"unknown"}`, nil); w.Code != 422 {
		t.Fatalf("未知のシナリオ: %d", w.Code)
	}
}

func TestFaultsAndOrigin(t *testing.T) {
	h, _ := handler(t, true)
	w := call(h, "PUT", "/api/dev/faults", `{"llm":"error","notify":null}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"llm":"error"`) || !strings.Contains(w.Body.String(), `"notify":null`) {
		t.Fatalf("faults: %d %s", w.Code, w.Body)
	}
	if w := call(h, "PUT", "/api/dev/faults", `{"llm":"explode"}`, nil); w.Code != 422 {
		t.Fatalf("未定義の値: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/dev/seed", strings.NewReader(`{"scenario":"initial_demo"}`))
	r.Host = "app.test"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://evil.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("別オリジンからの開発用 API は拒否: %d", rec.Code)
	}
}

func TestCurrentBookFlowScenariosAndSynchronousClock(t *testing.T) {
	for _, scenario := range []string{"book_plan_demo", "book_schedule_demo", "assignee_confirmation_demo"} {
		t.Run(scenario, func(t *testing.T) {
			h, clk := handler(t, true)
			w := call(h, "POST", "/api/dev/seed", `{"scenario":"`+scenario+`"}`, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("seed: %d %s", w.Code, w.Body.String())
			}
			var seed devapi.SeedResult
			if err := json.Unmarshal(w.Body.Bytes(), &seed); err != nil {
				t.Fatal(err)
			}
			if seed.GroupID == "" || len(seed.BookIDs) == 0 || len(seed.SlotIDs) == 0 {
				t.Fatalf("新しいフローのIDがない: %+v", seed)
			}
			login := call(h, "POST", "/api/dev/login", `{"discord_user_id":"100000000000000001"}`, nil)
			if login.Code != http.StatusOK {
				t.Fatalf("login: %d %s", login.Code, login.Body.String())
			}
			cookies := login.Result().Cookies()
			bookPath := "/api/groups/" + seed.GroupID + "/reading/books/" + seed.BookIDs[0]
			book := call(h, "GET", bookPath, "", cookies)
			var detail apitypes.ReadingBookDetail
			if book.Code != http.StatusOK {
				t.Fatalf("book: %d %s", book.Code, book.Body.String())
			}
			_ = json.Unmarshal(book.Body.Bytes(), &detail)

			switch scenario {
			case "book_plan_demo":
				if len(seed.BookIDs) != 2 || detail.Book.PlanStatus != "awaiting_approval" || detail.Sessions[0].AssignmentStatus != "pending" {
					t.Fatalf("担当計画待ちではない: %+v %+v", seed, detail.Book)
				}
			case "book_schedule_demo":
				if seed.NextTransitionAt == nil || detail.Book.PlanStatus != "approved" || detail.Sessions[0].Session != nil {
					t.Fatalf("調整開始待ちではない: %+v %+v", seed, detail.Book)
				}
				seconds := int64(seed.NextTransitionAt.Sub(clk.Now()).Seconds())
				advance := call(h, "POST", "/api/dev/clock/advance", fmt.Sprintf(`{"seconds":%d}`, seconds), nil)
				if advance.Code != http.StatusOK || !strings.Contains(advance.Body.String(), `"processed_count":2`) {
					t.Fatalf("同期処理: %d %s", advance.Code, advance.Body.String())
				}
				book = call(h, "GET", bookPath, "", cookies)
				_ = json.Unmarshal(book.Body.Bytes(), &detail)
				if detail.Sessions[0].Session == nil || detail.Sessions[0].SchedulingStatus != "scheduling" {
					t.Fatalf("時計を進めても調整が始まらない: %+v", detail.Sessions[0])
				}
			case "assignee_confirmation_demo":
				if seed.NextTransitionAt == nil || seed.SessionID == "" || detail.Sessions[0].SchedulingStatus != "scheduled" || detail.Sessions[0].AssigneeConfirmationStatus != nil {
					t.Fatalf("直前確認待ちではない: %+v %+v", seed, detail.Sessions[0])
				}
				seconds := int64(seed.NextTransitionAt.Sub(clk.Now()).Seconds())
				advance := call(h, "POST", "/api/dev/clock/advance", fmt.Sprintf(`{"seconds":%d}`, seconds), nil)
				if advance.Code != http.StatusOK {
					t.Fatalf("advance: %d %s", advance.Code, advance.Body.String())
				}
				book = call(h, "GET", bookPath, "", cookies)
				_ = json.Unmarshal(book.Body.Bytes(), &detail)
				if detail.Sessions[0].AssigneeConfirmationStatus == nil || *detail.Sessions[0].AssigneeConfirmationStatus != "open" {
					t.Fatalf("3日前の確認が始まらない: %+v", detail.Sessions[0])
				}
			}
		})
	}
}
