package controllers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

func TestTestingAPICheckReplacement(t *testing.T) {
	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	store := litmus.NewStore(t.TempDir())
	run := litmus.Run{ID: "run_s1", SessionID: "s1", State: litmus.RunRunning, CreatedAt: now, Deadline: now.Add(25 * time.Minute)}
	for _, err := range []error{
		store.Save(run),
		store.SaveChecks(run.ID, []litmus.Check{{ID: "c1", Name: "menu", State: litmus.CheckFail, Expect: "old expect", Purpose: "claim", Claim: true, ClaimSentence: "The row now stops that click."}}),
		store.WriteFile(run.ID, "intake-claims.json", []byte(`["The row now stops that click."]`)),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := testLitmusService(t, store, func() time.Time { return now })
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
	call := func(body string, status int, want string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/checks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != status || !strings.Contains(res.Body.String(), want) {
			t.Fatalf("check add = %d %s; want %d %s", res.Code, res.Body.String(), status, want)
		}
	}
	call(`{"sessionId":"s1","name":"new menu","expect":"click stops","purpose":"explore","replaces":"c1"}`, http.StatusBadRequest, "TESTING_CHECK_REPLACEMENT")
	call(`{"sessionId":"s1","name":"new menu","expect":"click stops","purpose":"explore","replaces":"c1","reason":"The row now stops that click."}`, http.StatusConflict, "TESTING_CHECK_REPLACEMENT")
	call(`{"sessionId":"s1","name":"new menu","expect":"click stops","purpose":"claim","claimSentence":"The row now stops that click.","replaces":"c1","reason":"Invented promise"}`, http.StatusBadRequest, "TESTING_CHECK_REPLACE_REASON")
	call(`{"sessionId":"s1","name":"menu","expect":"click stops","purpose":"claim","claimSentence":"The row now stops that click.","replaces":"c1","reason":"The row now stops that click."}`, http.StatusOK, `"id":"c2"`)
	checks, err := store.Checks(run.ID)
	if err != nil || len(checks) != 2 || checks[0].ReplacedBy != "" || checks[0].ReplaceReason != "The row now stops that click." || checks[1].Replaces != "c1" {
		t.Fatalf("HTTP replacement not saved: %+v, %v", checks, err)
	}
	call(`{"sessionId":"s1","name":"third menu","expect":"click stops","purpose":"claim","claimSentence":"The row now stops that click.","replaces":"c1","reason":"The row now stops that click."}`, http.StatusConflict, "TESTING_CHECK_REPLACEMENT")
}
