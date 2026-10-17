package controllers_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

func testingServer(t *testing.T, deps httpd.APIDeps) func(method, path, body string, status int, want ...string) {
	t.Helper()
	if svc, ok := deps.Testing.(*litmus.Service); ok {
		deps.Testing = svc.WithSpawnMarkerWait(time.Millisecond)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return func(method, path, body string, status int, want ...string) {
		t.Helper()
		got, code, _ := doRequest(t, srv, method, "/api/v1/testing"+path, body)
		if code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, status, got)
		}
		for _, w := range want {
			if !strings.Contains(string(got), w) {
				t.Fatalf("%s %s body %s lacks %q", method, path, got, w)
			}
		}
	}
}

func testLitmusService(t *testing.T, store *litmus.Store, clock func() time.Time) *litmus.Service {
	t.Helper()
	home := t.TempDir()
	patch, err := os.ReadFile(filepath.Join("..", "..", "litmus", "testdata", "pr6394.diff"))
	if err != nil {
		t.Fatal(err)
	}
	return litmus.New(store, clock).WithRunner(testingGitFixture{patch: patch}).WithOwnerHome(home, []string{
		"HOME=" + home,
		"PATH=" + filepath.Join(home, "bin"),
		"npm_config_prefix=" + filepath.Join(home, ".npm-global"),
		"HOMEBREW_PREFIX=" + filepath.Join(home, ".homebrew"),
	})
}

type testingGitFixture struct {
	patch []byte
}

func (f testingGitFixture) Run(ctx context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name != "git" {
		return nil, fmt.Errorf("unexpected testing command %q", name)
	}
	for len(args) >= 2 && (args[0] == "-C" || args[0] == "-c") {
		args = args[2:]
	}
	if len(args) == 0 {
		return nil, errors.New("testing git fixture received no subcommand")
	}
	last := args[len(args)-1]
	has := func(want string) bool {
		for _, arg := range args {
			if arg == want {
				return true
			}
		}
		return false
	}
	switch args[0] {
	case "clone":
		if err := os.MkdirAll(last, 0o750); err != nil {
			return nil, err
		}
		return []byte(""), os.WriteFile(filepath.Join(last, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	case "config", "fetch", "update-ref", "for-each-ref":
		return nil, nil
	case "merge-base":
		return []byte("0ca80cfc370b21443c4a2b9d22199c047b8c74e7\n"), nil
	case "rev-list":
		return []byte("0\n"), nil
	case "diff":
		switch {
		case has("--numstat"):
			return []byte("36\t16\tfrontend/src/renderer/components/chat/TurnSettingsBar.test.tsx\x007\t0\tfrontend/src/renderer/components/chat/TurnSettingsBar.tsx\x00"), nil
		case has("--name-status"):
			return []byte("M\x00frontend/src/renderer/components/chat/TurnSettingsBar.test.tsx\x00M\x00frontend/src/renderer/components/chat/TurnSettingsBar.tsx\x00"), nil
		case has("--patch"):
			return f.patch, nil
		}
	case "show":
		return []byte("export function keepModelMenuOpen(): boolean { return true }\n"), nil
	case "ls-tree":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected testing git command: %s", strings.Join(args, " "))
}

func savedTestPNG(t *testing.T, runDir ...string) string {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	var dir string
	if len(runDir) > 0 {
		dir = runDir[0]
	} else {
		dir = t.TempDir()
	}
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type agentVersionTestingService struct {
	controllers.TestingService
	sessionID, leg, agent, version string
}

func (s *agentVersionTestingService) AgentVersion(_ context.Context, sessionID, leg, agent, version string) (litmus.Run, error) {
	s.sessionID, s.leg, s.agent, s.version = sessionID, leg, agent, version
	return litmus.Run{ID: "run_1", State: litmus.RunRunning, Repo: "o/r", PRNumber: 7}, nil
}

type relaunchTestingService struct {
	controllers.TestingService
	sessionID, leg string
}

func (s *relaunchTestingService) Relaunch(_ context.Context, sessionID, leg string) (litmus.Run, error) {
	s.sessionID, s.leg = sessionID, leg
	return litmus.Run{ID: "run_1", State: litmus.RunRunning, Repo: "o/r", PRNumber: 7}, nil
}

func TestTestingAPIRelaunch(t *testing.T) {
	svc := &relaunchTestingService{}
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/relaunch", strings.NewReader(`{"sessionId":"ao-1","leg":"head"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"run_1"`) {
		t.Fatalf("POST /relaunch = %d %s", res.Code, res.Body.String())
	}
	if svc.sessionID != "ao-1" || svc.leg != "head" {
		t.Fatalf("Relaunch got session=%q leg=%q", svc.sessionID, svc.leg)
	}
}

type checkEndTestingService struct {
	controllers.TestingService
	sessionID, id string
	result        litmus.CheckState
	note          string
	attribution   []litmus.CheckAttribution
}

func (s *checkEndTestingService) CheckEnd(_ context.Context, sessionID, id string, result litmus.CheckState, note string, _ bool, _ int, attribution ...litmus.CheckAttribution) (litmus.Check, error) {
	s.sessionID, s.id, s.result, s.note, s.attribution = sessionID, id, result, note, attribution
	check := litmus.Check{ID: id, State: result}
	if len(attribution) == 1 {
		check.SameBug, check.Attribution = attribution[0].SameBug, attribution[0].Kind
	}
	return check, nil
}

func TestTestingAPIAgentVersion(t *testing.T) {
	svc := &agentVersionTestingService{}
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/agent-version", strings.NewReader(`{"sessionId":"ao-1","leg":"head","agent":"claude-code","version":"2.1"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"run_1"`) {
		t.Fatalf("POST /agent-version = %d %s", res.Code, res.Body.String())
	}
	if svc.sessionID != "ao-1" || svc.leg != "head" || svc.agent != "claude-code" || svc.version != "2.1" {
		t.Fatalf("AgentVersion got session=%q leg=%q agent=%q version=%q", svc.sessionID, svc.leg, svc.agent, svc.version)
	}
}

func TestTestingAPICheckEndForwardsSameBug(t *testing.T) {
	svc := &checkEndTestingService{}
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
	for _, tc := range []struct {
		body, wantBug, wantKind string
		wantResult              litmus.CheckState
	}{
		{`{"sessionId":"ao-1","result":"fail","note":"same row","sameBug":"c1"}`, "c1", "", litmus.CheckFail},
		{`{"sessionId":"ao-1","result":"partial","attribution":"unknown","sameBug":"c1"}`, "c1", "unknown", litmus.CheckPartial},
	} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/testing/checks/c2/end", strings.NewReader(tc.body)))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"sameBug":"c1"`) {
			t.Fatalf("POST /checks/c2/end = %d %s", res.Code, res.Body.String())
		}
		if svc.sessionID != "ao-1" || svc.id != "c2" || svc.result != tc.wantResult {
			t.Fatalf("CheckEnd got session=%q id=%q result=%q", svc.sessionID, svc.id, svc.result)
		}
		if len(svc.attribution) != 1 || svc.attribution[0].SameBug != tc.wantBug || svc.attribution[0].Kind != tc.wantKind {
			t.Fatalf("CheckEnd attribution = %+v", svc.attribution)
		}
	}
}

func TestTestingAPIStatusAndSubmit(t *testing.T) {
	dataDir := t.TempDir()
	store := litmus.NewStore(dataDir)
	do := testingServer(t, httpd.APIDeps{Testing: testLitmusService(t, store, nil).WithGH(recordedGH{t: t})})
	do("GET", "/status?sessionId=ao-1", "", http.StatusNotFound, "TESTING_RUN_NOT_FOUND")
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK, `"state":"intake"`, `"prNumber":6394`, `"comment":"off"`)
	do("GET", "/status?sessionId=ao-1", "", http.StatusOK, `"checks":[]`)
	do("POST", "/checks", `{"sessionId":"ao-1","name":"model menu","expect":"last model reachable","purpose":"claim","claimSentence":"Clicking \"Other models\" in the chat model menu no longer closes the menu"}`, http.StatusOK, `"id":"c1"`)

	do("POST", "/checks", `{"sessionId":"ao-1","name":"git push","expect":"push works","purpose":"risk","risk":"HOME moves credentials","claimSentence":"a claim"}`, http.StatusBadRequest, "TESTING_CHECK_CLAIM_SENTENCE")
	do("POST", "/checks", `{"sessionId":"ao-1","name":"git push","expect":"push works","purpose":"risk","risk":"HOME moves credentials"}`, http.StatusOK, `"id":"c2"`, `"purpose":"risk"`, `"risk":"HOME moves credentials"`)
	do("POST", "/checks", `{"sessionId":"ao-1","name":"old chat","expect":"opens","purpose":"risk","risk":"HOME hides transcripts"}`, http.StatusOK, `"id":"c3"`)
	for _, id := range []string{"c2", "c3"} {
		do("POST", "/checks/"+id+"/start", `{"sessionId":"ao-1","purpose":"risk","risk":"`+map[string]string{"c2": "HOME moves credentials", "c3": "HOME hides transcripts"}[id]+`"}`, http.StatusOK, `"state":"running"`)
		do("POST", "/checks/"+id+"/end", `{"sessionId":"ao-1","result":"skipped","note":"tried creating a fixture; no session available"}`, http.StatusOK, `"state":"skipped"`)
	}
	do("POST", "/checks/c1/start", `{"sessionId":"ao-1"}`, http.StatusOK, `"state":"running"`, `"claim":true`)
	do("POST", "/checks/c1/end", `{"sessionId":"ao-1","result":"maybe"}`, http.StatusBadRequest, "TESTING_CHECK_RESULT_INVALID")
	do("POST", "/checks/c1/end", `{"sessionId":"ao-1","result":"pass"}`, http.StatusConflict, "TESTING_CHECK_NO_RECEIPT") // nothing was driven
	do("POST", "/submit", `{"sessionId":"ao-1"}`, http.StatusBadRequest, "TESTING_CHECKS_INCOMPLETE", "c1 model menu")
	checks, err := store.Checks("run_ao-1") // receipts normally arrive from the drive stage
	if err != nil {
		t.Fatal(err)
	}
	shot := savedTestPNG(t, filepath.Join(dataDir, "litmus", "runs", "run_ao-1"))
	checks[0].Receipts = []litmus.Receipt{{Seq: 1, Action: "click", AfterPath: shot}}
	if err := store.SaveChecks("run_ao-1", checks); err != nil {
		t.Fatal(err)
	}
	do("POST", "/checks/c1/end", `{"sessionId":"ao-1","result":"pass"}`, http.StatusOK, `"state":"pass"`)
	do("POST", "/drive", `{"sessionId":"ao-1","action":"click","args":["e12"]}`, http.StatusConflict, "TESTING_RUN_NOT_RUNNING")
	do("POST", "/submit", `{"sessionId":"ao-1"}`, http.StatusOK, `"overall":"partial"`, "base-vs-head difference was not shown")
	do("POST", "/drive", `{"sessionId":"ao-1","action":"click","args":["e12"]}`, http.StatusConflict, "TESTING_RUN_CLOSED")
	do("POST", "/intake", `{"sessionId":"ao-1","unknown":true}`, http.StatusBadRequest, "INVALID_JSON")
}

type optionalClassificationTestingService struct {
	controllers.TestingService
	addClassificationCount, startClassificationCount int
}

func (s *optionalClassificationTestingService) CheckAdd(_ context.Context, _, name, expect, leg, _, _ string, classification ...litmus.CheckClassification) (litmus.Check, error) {
	s.addClassificationCount = len(classification)
	return litmus.Check{ID: "c1", Name: name, Expect: expect, Leg: leg, Purpose: "claim", Claim: true, ClaimSentence: "inherited claim"}, nil
}

func (s *optionalClassificationTestingService) CheckStart(_ context.Context, _, id string, classification ...litmus.CheckClassification) (litmus.Check, error) {
	s.startClassificationCount = len(classification)
	return litmus.Check{ID: id, State: litmus.CheckRunning, Purpose: "claim", Claim: true, ClaimSentence: "inherited claim"}, nil
}

func TestTestingAPIBaseCheckInheritsHeadClassification(t *testing.T) {
	svc := &optionalClassificationTestingService{}
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
		httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
	for _, tc := range []struct {
		path string
		body string
		want string
	}{
		{"checks", `{"sessionId":"ao-1","name":"other MODELS","expect":"the menu stays open","leg":"base"}`, `"id":"c1"`},
		{"checks/c1/start", `{"sessionId":"ao-1"}`, `"state":"running"`},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/"+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), tc.want) {
			t.Fatalf("POST %s = %d %s", tc.path, res.Code, res.Body.String())
		}
	}
	if svc.addClassificationCount != 0 || svc.startClassificationCount != 0 {
		t.Fatalf("omitted classifications reached service as explicit values: add=%d start=%d", svc.addClassificationCount, svc.startClassificationCount)
	}
}

func TestTestingAPISubmitBlocked(t *testing.T) {
	do := testingServer(t, httpd.APIDeps{Testing: testLitmusService(t, litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t})})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)
	do("POST", "/submit", `{"sessionId":"ao-1","blocked":true,"reason":"  "}`, http.StatusBadRequest, "TESTING_REASON_REQUIRED")
	do("POST", "/submit", `{"sessionId":"ao-1","blocked":true,"reason":"prepare refused"}`, http.StatusOK, `"overall":"blocked"`, `"reasons":["prepare refused"]`)
	do("POST", "/submit", `{"sessionId":"ao-1","blocked":true,"reason":"again"}`, http.StatusConflict, "TESTING_RUN_CLOSED")
}

func TestTestingAPICheckLegAndCompareRoute(t *testing.T) {
	do := testingServer(t, httpd.APIDeps{Testing: testLitmusService(t, litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t})})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)
	do("POST", "/checks", `{"sessionId":"ao-1","name":"menu","expect":"opens","leg":"base","purpose":"explore"}`, http.StatusOK, `"id":"c1"`, `"leg":"base"`, `"name":"menu"`)
	do("POST", "/checks", `{"sessionId":"ao-1","name":"menu","expect":"opens","leg":"main","purpose":"explore"}`, http.StatusBadRequest, "TESTING_LEG_INVALID")
	do("POST", "/checks/c1/reclassify", `{"sessionId":"ao-1","purpose":"claim","claimSentence":"Clicking \"Other models\" in the chat model menu no longer closes the menu","reason":"the PR now states this behavior"}`, http.StatusOK, `"purpose":"claim"`, `"reclassifications":[`)
	do("POST", "/checks/c9/compare", `{"sessionId":"ao-1"}`, http.StatusNotFound, "TESTING_CHECK_NOT_FOUND")
	do("POST", "/checks/c1/compare", `{"sessionId":"ao-1"}`, http.StatusConflict, "TESTING_COMPARE_INCOMPLETE") // no after-shots
}

func TestTestingAPIReplayValidationAndTesterAccess(t *testing.T) {
	store := litmus.NewStore(t.TempDir())
	run := litmus.Run{ID: "run_ao-1", SessionID: "ao-1", State: litmus.RunRunning, Deadline: time.Now().Add(time.Minute)}
	if err := store.Save(run); err != nil {
		t.Fatal(err)
	}
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: testLitmusService(t, store, nil)}, httpd.ControlDeps{})
	for _, tc := range []struct {
		leg  string
		code int
		want string
	}{
		{"head", http.StatusBadRequest, "TESTING_REPLAY_LEG"},
		{"base", http.StatusNotFound, "TESTING_CHECK_NOT_FOUND"},
	} {
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/testing/checks/c1/replay", strings.NewReader(`{"sessionId":"ao-1","leg":"`+tc.leg+`"}`)))
		if res.Code != tc.code || !strings.Contains(res.Body.String(), tc.want) {
			t.Fatalf("leg %s: %d %s", tc.leg, res.Code, res.Body.String())
		}
	}
}

func TestTestingAPICheckEndSameFaultAndDuplicate(t *testing.T) {
	store := litmus.NewStore(t.TempDir())
	svc := testLitmusService(t, store, nil)
	run := litmus.Run{ID: "run_ao-1", SessionID: "ao-1", State: litmus.RunRunning, Deadline: time.Now().Add(time.Minute)}
	if err := store.Save(run); err != nil {
		t.Fatal(err)
	}
	shot := savedTestPNG(t)
	checks := []litmus.Check{
		{ID: "c1", Name: "menu", State: litmus.CheckRunning, Receipts: []litmus.Receipt{{Action: "click", AfterPath: shot}}},
		{ID: "c2", Name: "menu", State: litmus.CheckRunning, Leg: "base", Receipts: []litmus.Receipt{{Action: "click", Leg: "base", AfterPath: shot}}},
	}
	if err := store.SaveChecks(run.ID, checks); err != nil {
		t.Fatal(err)
	}
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
	for _, row := range []struct {
		path, body, want string
		code             int
	}{
		{"/checks/c1/end", `{"sessionId":"ao-1","result":"fail","sameFault":true}`, "TESTING_CHECK_SAME_FAULT", http.StatusBadRequest},
		{"/checks/c1/end", `{"sessionId":"ao-1","result":"fail","sameBug":"c2"}`, "TESTING_CHECK_SAME_BUG", http.StatusBadRequest},
		{"/checks/c2/end", `{"sessionId":"ao-1","result":"fail","sameFault":true}`, `"sameFault":true`, http.StatusOK},
		{"/checks", `{"sessionId":"ao-1","name":"MENU","expect":"shown","leg":"head","purpose":"explore"}`, "TESTING_CHECK_DUPLICATE", http.StatusConflict},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/testing"+row.path, strings.NewReader(row.body)))
		if w.Code != row.code || !strings.Contains(w.Body.String(), row.want) {
			t.Fatalf("%s = %d %s", row.path, w.Code, w.Body.String())
		}
	}
}

func TestTestingAPICheckEndShot(t *testing.T) {
	for _, tc := range []struct {
		name string
		shot int
		leg  string
		code int
		want string
	}{
		{"unknown", 99, "head", http.StatusBadRequest, "TESTING_ARGS_INVALID"},
		{"base", 11, "base", http.StatusBadRequest, "TESTING_ARGS_INVALID"},
		{"valid", 11, "head", http.StatusOK, `"shot":11`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := litmus.NewStore(t.TempDir())
			run := litmus.Run{ID: "run_ao-1", SessionID: "ao-1", State: litmus.RunRunning, Deadline: time.Now().Add(time.Minute)}
			if err := store.Save(run); err != nil {
				t.Fatal(err)
			}
			shot := savedTestPNG(t)
			checks := []litmus.Check{
				{ID: "c2", Leg: tc.leg, State: litmus.CheckPass, Receipts: []litmus.Receipt{{Seq: 11, Leg: tc.leg, AfterPath: shot}}},
				{ID: "c9", State: litmus.CheckRunning, Receipts: []litmus.Receipt{{Seq: 42, AfterPath: shot}}},
			}
			if err := store.SaveChecks(run.ID, checks); err != nil {
				t.Fatal(err)
			}
			router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
				httpd.APIDeps{Testing: testLitmusService(t, store, nil)}, httpd.ControlDeps{})
			w := httptest.NewRecorder()
			body := fmt.Sprintf(`{"sessionId":"ao-1","result":"fail","shot":%d}`, tc.shot)
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/testing/checks/c9/end", strings.NewReader(body)))
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("shot=%d leg=%s: %d %s", tc.shot, tc.leg, w.Code, w.Body.String())
			}
		})
	}
}

func TestTestingAPITestVerbRemoved(t *testing.T) {
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
		httpd.APIDeps{Testing: testLitmusService(t, litmus.NewStore(t.TempDir()), nil)}, httpd.ControlDeps{})
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/testing/test", strings.NewReader(`{}`)))
	if res.Code != http.StatusNotFound {
		t.Fatalf("removed test route = %d: %s", res.Code, res.Body.String())
	}
}

func TestTestingAPIStartSpawnsWorker(t *testing.T) {
	var spawned []litmus.SpawnRequest
	svc := testLitmusService(t, litmus.NewStore(t.TempDir()), nil).WithSpawner(func(_ context.Context, r litmus.SpawnRequest) (string, error) {
		spawned = append(spawned, r)
		return "ao-7", nil
	})
	do := testingServer(t, httpd.APIDeps{Testing: svc})
	do("POST", "/start", `{"projectId":"p1","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusOK, `"sessionId":"ao-7"`, `"name":"litmus: PR #6394"`)
	do("POST", "/start", `{"prUrl":"aoagents/agent-orchestrator#6394","harness":"aider"}`, http.StatusBadRequest, "TESTING_HARNESS_UNSUPPORTED")
	do("POST", "/start", `{"prUrl":"nope"}`, http.StatusBadRequest, "TESTING_PR_URL_INVALID")
	do("POST", "/start", `{"projectId":"p2","prUrl":"aoagents/agent-orchestrator#6394","harness":"codex"}`, http.StatusOK, `"sessionId":"ao-7"`)
	if len(spawned) != 2 || spawned[0].ProjectID != "p1" || spawned[0].Harness != "claude-code" || spawned[1].Harness != "codex" {
		t.Fatalf("spawned = %+v", spawned)
	}
}

func TestTestingAPIStartNoCommentReachesTheRun(t *testing.T) {
	svc := testLitmusService(t, litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t}).WithSpawner(func(context.Context, litmus.SpawnRequest) (string, error) { return "ao-7", nil })
	do := testingServer(t, httpd.APIDeps{Testing: svc})
	do("POST", "/start", `{"prUrl":"aoagents/agent-orchestrator#6394","noComment":true}`, http.StatusOK, `"sessionId":"ao-7"`)
	do("POST", "/intake", `{"sessionId":"ao-7","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusOK, `"comment":"off"`)
	do("POST", "/submit", `{"sessionId":"ao-7","blocked":true,"reason":"x"}`, http.StatusOK, `"commentState":"off"`)
}

func TestTestingAPIStartNoEvidenceReachesTheRun(t *testing.T) {
	svc := testLitmusService(t, litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t}).WithSpawner(func(context.Context, litmus.SpawnRequest) (string, error) { return "ao-7", nil })
	do := testingServer(t, httpd.APIDeps{Testing: svc})
	do("POST", "/start", `{"prUrl":"aoagents/agent-orchestrator#6394","noEvidence":true}`, http.StatusOK, `"sessionId":"ao-7"`)
	do("POST", "/intake", `{"sessionId":"ao-7","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusOK, `"evidence":"off"`, `"comment":"post"`)
	do("POST", "/submit", `{"sessionId":"ao-7","blocked":true,"reason":"x"}`, http.StatusOK, `"evidenceState":"off"`)
}

func TestTestingAPIUnmountedWithoutService(t *testing.T) {
	testingServer(t, httpd.APIDeps{})("GET", "/status?sessionId=ao-1", "", http.StatusNotFound)
}

// recordedGH replays the gh output recorded for PR 6394 under litmus/testdata,
// so the controller test never reaches the network.
type recordedGH struct{ t *testing.T }

func (g recordedGH) Run(_ context.Context, args ...string) ([]byte, error) {
	g.t.Helper()
	var name string
	switch {
	case len(args) >= 2 && args[0] == "pr" && args[1] == "view":
		name = "pr6394.json"
	case len(args) >= 2 && args[0] == "pr" && args[1] == "diff":
		name = "pr6394.diff"
	default:
		return []byte("{}"), nil
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "litmus", "testdata", name))
	if err != nil {
		g.t.Fatalf("read recorded gh output: %v", err)
	}
	return b, nil
}

func TestTestingAPISubmitSkipBase(t *testing.T) {
	store := litmus.NewStore(t.TempDir())
	do := testingServer(t, httpd.APIDeps{Testing: testLitmusService(t, store, nil).WithGH(recordedGH{t: t})})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)
	do("POST", "/submit", `{"sessionId":"ao-1","skipBase":"  "}`, http.StatusBadRequest, "TESTING_REASON_REQUIRED")
	do("POST", "/submit", `{"sessionId":"ao-1","skipBase":"base build is broken"}`, http.StatusBadRequest, "TESTING_NO_CHECKS") // the field reaches the service
}

func TestTestingAPIPrune(t *testing.T) {
	dataDir := t.TempDir()
	checkout := filepath.Join(dataDir, "litmus", "repos", "o__r", strings.Repeat("a", 40))
	if err := os.MkdirAll(checkout, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "f"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour) // checkouts used in the last 10 minutes are kept
	if err := os.Chtimes(checkout, old, old); err != nil {
		t.Fatal(err)
	}
	do := testingServer(t, httpd.APIDeps{Testing: testLitmusService(t, litmus.NewStore(dataDir), nil)})
	do("POST", "/prune", `{"dryRun":true}`, http.StatusOK, `"dryRun":true`, "would remove checkout repos/o__r/", "would prune: 1 checkouts, 0 runs")
	if _, err := os.Stat(checkout); err != nil {
		t.Fatalf("dry run deleted the checkout: %v", err)
	}
	do("POST", "/prune", `{"keepRuns":-1}`, http.StatusBadRequest, "TESTING_PRUNE_INVALID")
	do("POST", "/prune", `{}`, http.StatusOK, `"dryRun":false`, "pruned: 1 checkouts, 0 runs")
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout survived: %v", err)
	}
}

// postingGH answers the comment endpoints a post needs and replays the recorded PR otherwise.
type postingGH struct {
	recordedGH
	posted *[]string
}

func (g postingGH) Run(ctx context.Context, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	switch {
	case line == "api user --jq .login":
		return []byte("litmus-bot\n"), nil
	case strings.HasSuffix(line, "/comments --paginate"):
		return []byte("[]"), nil
	case strings.HasPrefix(line, "api --method POST ") && strings.Contains(line, "/comments --input "):
		*g.posted = append(*g.posted, line)
		return []byte(`{"html_url":"https://github.com/o/r/pull/6394#issuecomment-1"}`), nil
	}
	return g.recordedGH.Run(ctx, args...)
}

func TestTestingAPIRunsAskBeforePostingAndRerun(t *testing.T) {
	const pr = "aoagents/agent-orchestrator#6394"
	dataDir := t.TempDir()
	store := litmus.NewStore(dataDir)
	var posted []string
	ids := []string{"ao-7", "ao-8", "ao-9"}
	svc := testLitmusService(t, store, nil).WithGH(postingGH{recordedGH{t: t}, &posted}).WithSpawner(func(context.Context, litmus.SpawnRequest) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	do := testingServer(t, httpd.APIDeps{Testing: svc})

	do("GET", "/projects/p1/settings", "", http.StatusOK, `"askBeforePosting":false`)
	do("PUT", "/projects/p1/settings", `{"askBeforePosting":true}`, http.StatusOK, `"askBeforePosting":true`)
	do("PUT", "/projects/p1/settings", `{"ask":true}`, http.StatusBadRequest, "INVALID_JSON")
	do("GET", "/projects/p1/settings", "", http.StatusOK, `"askBeforePosting":true`)
	do("GET", "/projects/p2/settings", "", http.StatusOK, `"askBeforePosting":false`)

	finish := func(session string) {
		t.Helper()
		do("POST", "/start", `{"projectId":"p1","prUrl":"`+pr+`","noEvidence":true}`, http.StatusOK, `"sessionId":"`+session+`"`)
		do("POST", "/intake", `{"sessionId":"`+session+`","prUrl":"`+pr+`"}`, http.StatusOK, `"projectId":"p1"`)
		do("POST", "/checks", `{"sessionId":"`+session+`","name":"model menu","expect":"last model reachable","purpose":"claim","claimSentence":"Clicking \"Other models\" in the chat model menu no longer closes the menu"}`, http.StatusOK)
		do("POST", "/checks/c1/start", `{"sessionId":"`+session+`","purpose":"claim","claimSentence":"Clicking \"Other models\" in the chat model menu no longer closes the menu"}`, http.StatusOK)
		id := "run_" + session
		checks, err := store.Checks(id)
		if err != nil {
			t.Fatal(err)
		}
		checks[0].Receipts = []litmus.Receipt{{Seq: 1, Action: "click", AfterPath: savedTestPNG(t, filepath.Join(dataDir, "litmus", "runs", id))}}
		if err := store.SaveChecks(id, checks); err != nil {
			t.Fatal(err)
		}
		do("POST", "/checks/c1/end", `{"sessionId":"`+session+`","result":"pass"}`, http.StatusOK)
		for _, id := range []string{"c2", "c3"} {
			do("POST", "/checks", `{"sessionId":"`+session+`","name":"risk `+id+`","expect":"still works","purpose":"risk","risk":"menu change breaks `+id+`"}`, http.StatusOK)
			do("POST", "/checks/"+id+"/start", `{"sessionId":"`+session+`","purpose":"risk","risk":"menu change breaks `+id+`"}`, http.StatusOK)
			do("POST", "/checks/"+id+"/end", `{"sessionId":"`+session+`","result":"skipped","note":"tried opening the menu; no session available"}`, http.StatusOK)
		}
		do("POST", "/submit", `{"sessionId":"`+session+`"}`, http.StatusOK, `"commentState":"awaiting-approval"`)
	}
	finish("ao-7")
	finish("ao-8")
	if len(posted) != 0 {
		t.Fatalf("submit posted %q although the project asks first", posted)
	}

	do("GET", "/runs?projectId=p1", "", http.StatusOK, `"id":"run_ao-8"`, `"id":"run_ao-7"`, `"state":"awaiting-approval"`, `"verdict":"partial"`, `"number":6394`, `"durationSeconds":`)
	do("GET", "/runs?projectId=p2", "", http.StatusOK, `"runs":[]`)
	do("GET", "/runs?limit=1", "", http.StatusOK, `"id":"run_ao-8"`)
	do("GET", "/runs?limit=0", "", http.StatusBadRequest, "TESTING_LIMIT_INVALID")
	do("GET", "/runs?limit=x", "", http.StatusBadRequest, "TESTING_LIMIT_INVALID")
	do("GET", "/runs/run_ao-7", "", http.StatusOK, `"verdictMd":"# Verdict: partial`, `"commentMd":"Litmus tried`, `"checks":[{`, `"run":{`)
	do("GET", "/runs/run_nope", "", http.StatusNotFound, "TESTING_RUN_NOT_FOUND")

	do("POST", "/runs/run_ao-7/post", `{}`, http.StatusOK, `"state":"submitted"`, `"commentState":"posted"`, `issuecomment-1`)
	if len(posted) != 1 {
		t.Fatalf("posted %q", posted)
	}
	do("POST", "/runs/run_ao-7/post", `{}`, http.StatusConflict, "TESTING_NOT_AWAITING") // a second post is refused
	do("POST", "/runs/run_ao-7/discard", `{}`, http.StatusConflict, "TESTING_NOT_AWAITING")
	do("POST", "/runs/run_nope/post", `{}`, http.StatusNotFound, "TESTING_RUN_NOT_FOUND")

	do("POST", "/runs/run_ao-8/discard", `{}`, http.StatusOK, `"state":"cleaned"`, `"commentState":"discarded"`)
	do("POST", "/runs/run_ao-8/post", `{}`, http.StatusConflict, "TESTING_NOT_AWAITING")
	if len(posted) != 1 {
		t.Fatalf("a discarded run posted: %q", posted)
	}

	do("POST", "/rerun", `{"runId":"run_ao-7"}`, http.StatusOK, `"sessionId":"ao-9"`, `"runId":"run_ao-9"`)
	do("POST", "/rerun", `{"runId":"run_nope"}`, http.StatusNotFound, "TESTING_RUN_NOT_FOUND")
	do("POST", "/rerun", `{}`, http.StatusBadRequest, "TESTING_ID_INVALID")
}

func TestTestingAPIProjectSettingsReviewPreservesOmittedValue(t *testing.T) {
	svc := testLitmusService(t, litmus.NewStore(t.TempDir()), nil)
	router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
	put := func(body string, want ...string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/testing/projects/p1/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("PUT settings = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
		}
		for _, text := range want {
			if !strings.Contains(rr.Body.String(), text) {
				t.Fatalf("PUT settings body %s lacks %q", rr.Body.String(), text)
			}
		}
	}
	put(`{"askBeforePosting":false,"review":true}`, `"review":true`)
	put(`{"askBeforePosting":true}`, `"askBeforePosting":true`, `"review":true`)
	put(`{"askBeforePosting":true,"review":false}`, `"review":false`)
}

// The daemon, not the prompt, keeps a tester out of start, rerun, post, discard and prune.
func TestTestingAPIRefusesTheTesterOnUserVerbs(t *testing.T) {
	svc := testLitmusService(t, litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t}).WithSpawner(func(context.Context, litmus.SpawnRequest) (string, error) { return "ao-9", nil })
	do := testingServer(t, httpd.APIDeps{Testing: svc})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)

	do("POST", "/prune", `{"sessionId":"ao-1","dryRun":true}`, http.StatusForbidden, "TESTING_TESTER_FORBIDDEN")
	do("POST", "/start", `{"sessionId":"ao-1","projectId":"p1","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusForbidden, "TESTING_TESTER_FORBIDDEN")
	do("POST", "/runs/run_ao-1/post", `{"sessionId":"ao-1"}`, http.StatusForbidden, "TESTING_TESTER_FORBIDDEN")
	do("POST", "/runs/run_ao-1/discard", `{"sessionId":"ao-1"}`, http.StatusForbidden, "TESTING_TESTER_FORBIDDEN")
	do("POST", "/rerun", `{"sessionId":"ao-1","runId":"run_ao-1"}`, http.StatusForbidden, "TESTING_TESTER_FORBIDDEN", "rerun")

	// The user's own session, or no session, is allowed through.
	do("POST", "/prune", `{"sessionId":"ao-orch","dryRun":true}`, http.StatusOK, `"dryRun":true`)
	do("POST", "/prune", `{"dryRun":true}`, http.StatusOK, `"dryRun":true`)
	do("POST", "/start", `{"sessionId":"ao-orch","projectId":"p1","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusOK, `"sessionId":"ao-9"`)
	do("POST", "/rerun", `{"sessionId":"ao-orch","runId":"run_ao-1","projectId":"p1"}`, http.StatusOK, `"sessionId":"ao-9"`, `"runId":"run_ao-9"`)
}

type riskTestingService struct {
	controllers.TestingService
	classification litmus.CheckClassification
	reason         string
}

func (s *riskTestingService) CheckAdd(_ context.Context, _, _, _, _, _, _ string, classification ...litmus.CheckClassification) (litmus.Check, error) {
	s.classification = classification[0]
	return litmus.Check{ID: "c1", Purpose: s.classification.Purpose, Risk: s.classification.Risk}, nil
}

func (s *riskTestingService) CheckStart(ctx context.Context, _, _ string, classification ...litmus.CheckClassification) (litmus.Check, error) {
	return s.CheckAdd(ctx, "", "", "", "", "", "", classification...)
}

func (s *riskTestingService) CheckReclassify(ctx context.Context, _, _ string, classification litmus.CheckClassification, reason string) (litmus.Check, error) {
	s.reason = reason
	return s.CheckAdd(ctx, "", "", "", "", "", "", classification)
}

func TestTestingAPIRiskClassification(t *testing.T) {
	for _, path := range []string{"checks", "checks/c1/start", "checks/c1/reclassify"} {
		t.Run(path, func(t *testing.T) {
			svc := &riskTestingService{}
			router := httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Testing: svc}, httpd.ControlDeps{})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/"+path, strings.NewReader(`{"sessionId":"s1","name":"git push","expect":"push succeeds","purpose":"risk","risk":"HOME moves credentials","reason":"the diff moves HOME"}`))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if res.Code != http.StatusOK || svc.classification != (litmus.CheckClassification{Purpose: "risk", Risk: "HOME moves credentials"}) || !strings.Contains(res.Body.String(), `"risk":"HOME moves credentials"`) {
				t.Fatalf("POST %s = %d %s, classification %+v", path, res.Code, res.Body.String(), svc.classification)
			}
			if strings.HasSuffix(path, "reclassify") && svc.reason != "the diff moves HOME" {
				t.Fatalf("reclassify reason = %q", svc.reason)
			}
		})
	}
}
