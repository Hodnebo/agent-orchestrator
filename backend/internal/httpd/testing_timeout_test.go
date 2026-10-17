package httpd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

type testingTimeoutProbe struct {
	controllers.TestingService
	cleanup, replay, drive, relaunch chan time.Duration
}

func (p *testingTimeoutProbe) Cleanup(ctx context.Context, _ string) (litmus.Run, error) {
	p.cleanup <- remainingRequestBudget(ctx)
	return litmus.Run{}, nil
}

func (p *testingTimeoutProbe) Replay(ctx context.Context, _, _, _ string) (map[string]any, error) {
	p.replay <- remainingRequestBudget(ctx)
	return map[string]any{}, nil
}

func (p *testingTimeoutProbe) Drive(ctx context.Context, _, _ string, _ []string) (map[string]any, error) {
	p.drive <- remainingRequestBudget(ctx)
	return map[string]any{}, nil
}

func (p *testingTimeoutProbe) Relaunch(ctx context.Context, _, _ string) (litmus.Run, error) {
	p.relaunch <- remainingRequestBudget(ctx)
	return litmus.Run{}, nil
}

func TestTestingLongRoutesUseLongTimeoutAndOthersKeepTheOrdinaryOne(t *testing.T) {
	const configuredTimeout = 250 * time.Millisecond
	probe := &testingTimeoutProbe{
		cleanup: make(chan time.Duration, 1), replay: make(chan time.Duration, 1), drive: make(chan time.Duration, 1), relaunch: make(chan time.Duration, 1),
	}
	router := NewRouterWithControl(config.Config{RequestTimeout: configuredTimeout}, discardLogger(), nil, APIDeps{Testing: probe}, ControlDeps{})

	for path, budgets := range map[string]chan time.Duration{
		"/api/v1/testing/cleanup":          probe.cleanup,
		"/api/v1/testing/checks/c1/replay": probe.replay,
		"/api/v1/testing/drive":            probe.drive,
		"/api/v1/testing/relaunch":         probe.relaunch,
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"sessionId":"s1"}`))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("POST %s = %d: %s", path, res.Code, res.Body.String())
		}
		budget := <-budgets
		if path == "/api/v1/testing/drive" {
			if budget < litmus.DriveBudget-time.Second || budget > litmus.DriveBudget {
				t.Fatalf("drive budget = %s, want %s", budget, litmus.DriveBudget)
			}
			continue
		}
		if path == "/api/v1/testing/relaunch" {
			want := litmus.DefaultLaunchBudget + 10*time.Second
			if budget < want-time.Second || budget > want {
				t.Fatalf("relaunch budget = %s, want %s", budget, want)
			}
			continue
		}
		if path == "/api/v1/testing/checks/c1/replay" {
			if budget < litmus.RunBudget+time.Minute-10*time.Second || budget > litmus.RunBudget+time.Minute {
				t.Fatalf("replay budget = %s, want 26 minutes", budget)
			}
			continue
		}
		if budget <= 0 || budget > configuredTimeout {
			t.Fatalf("ordinary POST budget = %s, want at most %s", budget, configuredTimeout)
		}
	}
}
