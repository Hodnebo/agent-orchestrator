package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

type testingCapture struct {
	method, path string
	body         map[string]any
}

// testingDaemon answers every request with status and body, records the last
// one, and points the CLI's run file at itself.
func testingDaemon(t *testing.T, status int, respBody string) *testingCapture {
	t.Helper()
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	got := &testingCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		// Check mutations fetch status to render their countdown. Keep the
		// mutation as the captured request so the wire assertions remain useful.
		followupStatus := r.URL.Path == "/api/v1/testing/status" && got.path != "" && got.path != "/internal/telemetry/cli-invoked" && got.path != "/api/v1/testing/status"
		if !followupStatus {
			*got = testingCapture{method: r.Method, path: r.URL.RequestURI()}
			_ = json.Unmarshal(raw, &got.body)
		}
		w.WriteHeader(status)
		if followupStatus {
			_, _ = io.WriteString(w, fmt.Sprintf(`{"run":{"deadline":%q}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)))
			return
		}
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	return got
}

func runTesting(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, append([]string{"testing"}, args...)...)
	return out, err
}

func TestTestingCheckEndSendsFlags(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		same bool
		shot int
	}{{false, 0}, {true, 0}, {false, 11}, {true, 11}} {
		var body map[string]any
		calls := 0
		deps := Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/v1/testing/status" {
				return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
			}
			if req.URL.Path != "/api/v1/testing/checks/c1/end" {
				return jsonResponse(http.StatusNoContent, ""), nil
			}
			calls++
			if req.Method != http.MethodPost {
				t.Errorf("method %s", req.Method)
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			return jsonResponse(http.StatusOK, `{"id":"c1","state":"fail"}`), nil
		})}}
		args := []string{"testing", "check", "end", "c1", "--result", "fail", "--note", "same row"}
		if tc.same {
			args = append(args, "--same-fault")
		}
		if tc.shot != 0 {
			args = append(args, "--shot", fmt.Sprint(tc.shot))
		}
		_, _, err := executeCLI(t, deps, args...)
		if err != nil || calls != 1 || body["sessionId"] != "w1" || body["result"] != "fail" || body["note"] != "same row" || (body["sameFault"] == true) != tc.same {
			t.Fatalf("flags=%+v, calls=%d, body=%v, err=%v", tc, calls, body, err)
		}
		if (tc.shot == 0 && body["shot"] != nil) || (tc.shot != 0 && body["shot"] != float64(tc.shot)) {
			t.Fatalf("shot=%d, body=%v", tc.shot, body)
		}
	}
}

func TestTestingCheckEndRejectsNonpositiveShot(t *testing.T) {
	for _, shot := range []string{"0", "-1"} {
		_, _, err := executeCLI(t, Deps{}, "testing", "check", "end", "c1", "--result", "fail", "--shot", shot)
		if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--shot must be a positive receipt number") {
			t.Fatalf("shot=%s: %v", shot, err)
		}
	}
}

func TestTestingCheckEndRejectsEmptySameBug(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "testing", "check", "end", "c1", "--result", "fail", "--same-bug", " ")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--same-bug requires a check id") {
		t.Fatalf("empty same-bug: %v", err)
	}
}

func TestTestingCheckAddReplacementFlags(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	statusCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/internal/telemetry/cli-invoked":
			return jsonResponse(http.StatusNoContent, ""), nil
		case "/api/v1/testing/checks":
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			return jsonResponse(http.StatusOK, `{"id":"c2","name":"menu-v2","state":"pending"}`), nil
		case "/api/v1/testing/status":
			statusCalls++
			return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	deps := Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client, Now: func() time.Time { return time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC) }}
	out, _, err := executeCLI(t, deps, "testing", "check", "add", "menu-v2", "--expect", "shown", "--explore", "--replaces", "c1", "--reason", "The menu remains open after the update")
	if err != nil {
		t.Fatal(err)
	}
	if body["sessionId"] != "w1" || body["name"] != "menu-v2" || body["expect"] != "shown" || body["purpose"] != "explore" || body["replaces"] != "c1" || body["reason"] != "The menu remains open after the update" {
		t.Fatalf("replacement body = %v", body)
	}
	if statusCalls != 1 || !strings.Contains(out, "10 min left") {
		t.Fatalf("out=%q status calls=%d", out, statusCalls)
	}
}

func TestTestingCheckAddReplacementFlagsMustBePaired(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing reason", args: []string{"--replaces", "c1"}, want: "must be provided together"},
		{name: "missing replaces", args: []string{"--reason", "the PR sentence"}, want: "must be provided together"},
		{name: "empty replaces", args: []string{"--replaces", "", "--reason", "the PR sentence"}, want: "must be non-empty"},
		{name: "empty reason", args: []string{"--replaces", "c1", "--reason", " "}, want: "must be non-empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"testing", "check", "add", "menu", "--expect", "shown", "--explore"}, tc.args...)
			_, _, err := executeCLI(t, Deps{}, args...)
			if ExitCode(err) != 2 || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("args=%v err=%v", args, err)
			}
		})
	}
}

func TestTestingCheckMutationsShowFixedClockTimeLeft(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "add", args: []string{"testing", "check", "add", "menu", "--expect", "shown", "--explore"}},
		{name: "start", args: []string{"testing", "check", "start", "c1", "--explore"}},
		{name: "end", args: []string{"testing", "check", "end", "c1", "--result", "skipped"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "w1")
			cfg := setConfigEnv(t)
			if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
				t.Fatal(err)
			}
			statusCalls := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/internal/telemetry/cli-invoked":
					return jsonResponse(http.StatusNoContent, ""), nil
				case "/api/v1/testing/status":
					statusCalls++
					return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
				case "/api/v1/testing/checks", "/api/v1/testing/checks/c1/start", "/api/v1/testing/checks/c1/end":
					return jsonResponse(http.StatusOK, `{"id":"c1","name":"menu","state":"pending"}`), nil
				default:
					return jsonResponse(http.StatusNotFound, `{}`), nil
				}
			})}
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client, Now: func() time.Time { return now }}, tc.args...)
			if err != nil || statusCalls != 1 || !strings.Contains(out, "10 min left") {
				t.Fatalf("out=%q status calls=%d err=%v", out, statusCalls, err)
			}
		})
	}
}

func TestTestingCheckJSONOmitsTextCountdown(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	statusCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/internal/telemetry/cli-invoked":
			return jsonResponse(http.StatusNoContent, ""), nil
		case "/api/v1/testing/checks":
			return jsonResponse(http.StatusOK, `{"id":"c1","name":"menu","state":"pending","replacedBy":"c2","replaceReason":"The PR changed the expected wording."}`), nil
		case "/api/v1/testing/status":
			statusCalls++
			return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client, Now: func() time.Time { return time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC) }}, "testing", "--json", "check", "add", "menu", "--expect", "shown", "--explore")
	if err != nil || statusCalls != 0 || strings.Contains(out, "min left") || !strings.Contains(out, `"replacedBy": "c2"`) || !strings.Contains(out, `"replaceReason": "The PR changed the expected wording."`) {
		t.Fatalf("out=%q status calls=%d err=%v", out, statusCalls, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil || decoded["id"] != "c1" {
		t.Fatalf("JSON output=%q decoded=%v err=%v", out, decoded, err)
	}
}

func TestTestingCheckMutationReportsMissingDeadlineAfterSuccess(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/internal/telemetry/cli-invoked":
			return jsonResponse(http.StatusNoContent, ""), nil
		case "/api/v1/testing/checks":
			return jsonResponse(http.StatusOK, `{"id":"c1","name":"menu","state":"pending"}`), nil
		case "/api/v1/testing/status":
			return jsonResponse(http.StatusOK, `{"run":{"id":"run_w1"}}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, "testing", "check", "add", "menu", "--expect", "shown", "--explore")
	if err != nil || !strings.Contains(stderr, "warning: minutes-left unavailable:") || !strings.Contains(stderr, "run deadline") || !strings.Contains(out, "c1 pending menu") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestTestingCheckMutationReportsStatusFailureAfterSuccess(t *testing.T) {
	for _, args := range [][]string{
		{"testing", "check", "add", "menu", "--expect", "shown", "--explore"},
		{"testing", "check", "start", "c1", "--explore"},
		{"testing", "check", "end", "c1", "--result", "skipped"},
	} {
		t.Run(args[2], func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "w1")
			cfg := setConfigEnv(t)
			if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
				t.Fatal(err)
			}
			mutations := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/internal/telemetry/cli-invoked":
					return jsonResponse(http.StatusNoContent, ""), nil
				case "/api/v1/testing/checks", "/api/v1/testing/checks/c1/start", "/api/v1/testing/checks/c1/end":
					mutations++
					return jsonResponse(http.StatusOK, `{"id":"c1","name":"menu","state":"pending"}`), nil
				case "/api/v1/testing/status":
					return nil, fmt.Errorf("status unavailable")
				default:
					return jsonResponse(http.StatusNotFound, `{}`), nil
				}
			})}
			out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, args...)
			if err != nil || mutations != 1 || !strings.Contains(stderr, "warning: minutes-left unavailable:") || !strings.Contains(stderr, "status unavailable") || !strings.Contains(out, "c1 pending menu") {
				t.Fatalf("out=%q stderr=%q mutations=%d err=%v", out, stderr, mutations, err)
			}
		})
	}
}

func TestCheckTimeLeftRoundsUpAndClampsAtZero(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		deadline time.Time
		want     string
	}{
		{name: "just over nine minutes", deadline: now.Add(9*time.Minute + time.Second), want: "10 min left"},
		{name: "exactly nine minutes", deadline: now.Add(9 * time.Minute), want: "9 min left"},
		{name: "at deadline", deadline: now, want: "0 min left"},
		{name: "past deadline", deadline: now.Add(-time.Second), want: "0 min left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkTimeLeft(litmus.Run{Deadline: tc.deadline}, now); got != tc.want {
				t.Fatalf("checkTimeLeft = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTestingAgentVersionForwardsLegAgentAndVersion(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/internal/telemetry/cli-invoked" {
			return jsonResponse(http.StatusNoContent, ""), nil
		}
		calls++
		if req.Method != http.MethodPost || req.URL.Path != "/api/v1/testing/agent-version" {
			t.Fatalf("request = %s %s", req.Method, req.URL.Path)
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, `{"id":"run_1","state":"running","repo":"o/r","prNumber":7}`), nil
	})}
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, "testing", "agent-version", "claude-code", "2.1", "--leg", "head")
	if err != nil || calls != 1 || body["sessionId"] != "w1" || body["leg"] != "head" || body["agent"] != "claude-code" || body["version"] != "2.1" {
		t.Fatalf("calls=%d body=%v out=%q err=%v", calls, body, out, err)
	}
}

func TestTestingAgentVersionRequiresValidLegAndValues(t *testing.T) {
	for _, args := range [][]string{
		{"agent-version", "claude-code", "2.1"},
		{"agent-version", "claude-code", "2.1", "--leg", "main"},
		{"agent-version", " ", "2.1", "--leg", "head"},
		{"agent-version", "claude-code", " ", "--leg", "base"},
	} {
		_, _, err := executeCLI(t, Deps{}, append([]string{"testing"}, args...)...)
		if ExitCode(err) != 2 {
			t.Fatalf("args=%v err=%v exit=%d", args, err, ExitCode(err))
		}
	}
}

func TestTestingRelaunchWireAndErrors(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	var got testingCapture
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/internal/telemetry/cli-invoked" {
			return jsonResponse(http.StatusNoContent, ""), nil
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &got.body); err != nil {
			return nil, err
		}
		got.method, got.path = req.Method, req.URL.RequestURI()
		return jsonResponse(http.StatusOK, `{"id":"run_w1","state":"running","repo":"o/r","prNumber":7}`), nil
	})}
	deps := Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}
	out, _, err := executeCLI(t, deps, "testing", "relaunch", "head")
	if err != nil || !strings.Contains(out, "run run_w1 running o/r#7") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got.method != http.MethodPost || got.path != "/api/v1/testing/relaunch" || got.body["sessionId"] != "w1" || got.body["leg"] != "head" {
		t.Fatalf("request = %s %s %v", got.method, got.path, got.body)
	}

	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/internal/telemetry/cli-invoked" {
			return jsonResponse(http.StatusNoContent, ""), nil
		}
		return jsonResponse(http.StatusConflict, `{"code":"TESTING_RUN_CLOSED","message":"testing run is closed","requestId":"req-relaunch"}`), nil
	})
	_, _, err = executeCLI(t, deps, "testing", "relaunch", "base")
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "TESTING_RUN_CLOSED") || !strings.Contains(err.Error(), "req-relaunch") {
		t.Fatalf("relaunch error = %v", err)
	}

	_, _, err = executeCLI(t, deps, "testing", "relaunch", "main")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "requires head or base") {
		t.Fatalf("invalid leg error = %v", err)
	}
}

func TestTestingCheckStartSendsClassificationAndSupportsBeginAlias(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		verb, purpose, sentence string
	}{
		{"start", "claim", `Clicking "Other models" in the chat model menu no longer closes the menu`},
		{"begin", "explore", ""},
	} {
		var body map[string]any
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/internal/telemetry/cli-invoked" {
				return jsonResponse(http.StatusNoContent, ""), nil
			}
			if req.URL.Path == "/api/v1/testing/status" {
				return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
			}
			if req.URL.Path != "/api/v1/testing/checks/c1/start" {
				t.Fatalf("path = %s", req.URL.Path)
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			return jsonResponse(http.StatusOK, `{"id":"c1","state":"running","purpose":"`+tc.purpose+`"}`), nil
		})}
		args := []string{"testing", "check", tc.verb, "c1", "--explore"}
		if tc.purpose == "claim" {
			args = []string{"testing", "check", tc.verb, "c1", "--claim", tc.sentence}
		}
		_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, args...)
		if err != nil || body["sessionId"] != "w1" || body["purpose"] != tc.purpose || (tc.sentence != "" && body["claimSentence"] != tc.sentence) {
			t.Fatalf("verb=%s purpose=%s body=%v err=%v", tc.verb, tc.purpose, body, err)
		}
	}
}

func TestTestingCheckClassificationRequiresOneChoice(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	for _, args := range [][]string{
		{"check", "add", "menu", "--expect", "shown"},
		{"check", "add", "menu", "--expect", "shown", "--claim", ""},
		{"check", "add", "menu", "--expect", "shown", "--claim", "sentence", "--explore"},
		{"check", "add", "menu", "--expect", "shown", "--risk", " "},
		{"check", "add", "menu", "--expect", "shown", "--risk", "git-push", "--claim", "sentence"},
		{"check", "add", "menu", "--expect", "shown", "--risk", "git-push", "--explore"},
		{"check", "start", "c1", "--risk", "git-push", "--claim", "sentence"},
		{"check", "reclassify", "c1", "--risk", "git-push"},
		{"check", "start", "c1", "--claim", "sentence", "--explore"},
		{"check", "reclassify", "c1", "--explore"},
		{"check", "reclassify", "c1", "--claim", "sentence", "--reason", ""},
	} {
		_, _, err := executeCLI(t, Deps{}, append([]string{"testing"}, args...)...)
		if ExitCode(err) != 2 {
			t.Fatalf("args=%v err=%v exit=%d", args, err, ExitCode(err))
		}
	}
}

func TestTestingCheckEndSendsAttribution(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/internal/telemetry/cli-invoked" {
			return jsonResponse(http.StatusNoContent, ""), nil
		}
		if req.URL.Path == "/api/v1/testing/status" {
			return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, `{"id":"c1","state":"absent"}`), nil
	})}
	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, "testing", "check", "end", "c1", "--result", "absent", "--attribution", "environment", "--cause", "outage", "--receipt", "8", "--same-bug", "c0")
	if err != nil {
		t.Fatal(err)
	}
	if body["attribution"] != "environment" || body["environmentCause"] != "outage" || body["attributionReceipt"] != float64(8) || body["sameBug"] != "c0" {
		t.Fatalf("attribution body = %v", body)
	}
}

func TestTestingCheckEndSendsSameBugWithoutAttribution(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/internal/telemetry/cli-invoked" {
			return jsonResponse(http.StatusNoContent, ""), nil
		}
		if req.URL.Path == "/api/v1/testing/status" {
			return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, `{"id":"c2","state":"fail","sameBug":"c1"}`), nil
	})}
	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, "testing", "check", "end", "c2", "--result", "fail", "--same-bug", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if body["sessionId"] != "w1" || body["sameBug"] != "c1" || body["attribution"] != nil {
		t.Fatalf("same-bug body = %v", body)
	}
}

func TestTestingVerbsSendOneRequestEach(t *testing.T) {
	verdict := filepath.Join(t.TempDir(), "verdict.txt")
	if err := os.WriteFile(verdict, []byte("head only"), 0o600); err != nil {
		t.Fatal(err)
	}
	recipe, _ := filepath.Abs("recipe.yaml")
	type M = map[string]any
	type row struct {
		args []string
		path string
		body M // sessionId is added to every expected body
	}
	const pr = "https://github.com/aoagents/agent-orchestrator/pull/6394"
	tests := []row{
		{[]string{"intake", pr}, "POST /testing/intake", M{"prUrl": pr}},
		{[]string{"prepare"}, "POST /testing/prepare", M{}},
		{[]string{"launch", "head"}, "POST /testing/launch", M{"leg": "head"}},
		{[]string{"launch", "base"}, "POST /testing/launch", M{"leg": "base"}},
		{[]string{"status"}, "GET /testing/status?sessionId=w1", nil},
		{[]string{"check", "add", "menu", "--expect", "shown", "--explore"}, "POST /testing/checks", M{"name": "menu", "expect": "shown", "purpose": "explore"}},
		{[]string{"check", "add", "menu", "--expect", "shown", "--claim", `Clicking "Other models" in the chat model menu no longer closes the menu`, "--leg", "base"}, "POST /testing/checks", M{"name": "menu", "expect": "shown", "purpose": "claim", "claimSentence": `Clicking "Other models" in the chat model menu no longer closes the menu`, "leg": "base"}},
		{[]string{"check", "start", "c1", "--claim", `Clicking "Other models" in the chat model menu no longer closes the menu`}, "POST /testing/checks/c1/start", M{"purpose": "claim", "claimSentence": `Clicking "Other models" in the chat model menu no longer closes the menu`}},
		{[]string{"check", "reclassify", "c1", "--explore", "--reason", "changed"}, "POST /testing/checks/c1/reclassify", M{"purpose": "explore", "reason": "changed"}},
		{[]string{"check", "add", "git-push", "--expect", "pushed", "--risk", "credentials-moved"}, "POST /testing/checks", M{"name": "git-push", "expect": "pushed", "purpose": "risk", "risk": "credentials-moved"}},
		{[]string{"check", "start", "c1", "--risk", "credentials-moved"}, "POST /testing/checks/c1/start", M{"purpose": "risk", "risk": "credentials-moved"}},
		{[]string{"check", "reclassify", "c1", "--risk", "credentials-moved", "--reason", "diff"}, "POST /testing/checks/c1/reclassify", M{"purpose": "risk", "risk": "credentials-moved", "reason": "diff"}},
		{[]string{"compare", "c1"}, "POST /testing/checks/c1/compare", M{}},
		{[]string{"replay", "c1", "--leg", "base"}, "POST /testing/checks/c1/replay", M{"leg": "base"}},
		{[]string{"check", "end", "c1", "--result", "fail", "--note", "clipped"}, "POST /testing/checks/c1/end", M{"result": "fail", "note": "clipped"}},
		{[]string{"check", "end", "c1", "--result", "fail", "--note", "same", "--same-fault"}, "POST /testing/checks/c1/end", M{"result": "fail", "note": "same", "sameFault": true}},
		{[]string{"check", "end", "c3", "--result", "fail", "--same-bug", "c1"}, "POST /testing/checks/c3/end", M{"result": "fail", "note": "", "sameBug": "c1"}},
		{[]string{"check", "end", "c2", "--result", "absent", "--note", "nopicker", "--attribution", "environment", "--cause", "outage", "--receipt", "8"}, "POST /testing/checks/c2/end", M{"result": "absent", "note": "nopicker", "attribution": "environment", "environmentCause": "outage", "attributionReceipt": 8}},
		{[]string{"submit"}, "POST /testing/submit", M{}},
		{[]string{"submit", "--verdict-file", verdict}, "POST /testing/submit", M{"summary": "head only"}},
		{[]string{"submit", "--blocked", "--reason", "prepare-refused"}, "POST /testing/submit", M{"blocked": true, "reason": "prepare-refused"}},
		{[]string{"submit", "--skip-base", "--reason", "time-ran-out"}, "POST /testing/submit", M{"skipBase": "time-ran-out"}},
		{[]string{"submit", "--skip-base", "--reason", "time-ran-out", "--verdict-file", verdict}, "POST /testing/submit", M{"summary": "head only", "skipBase": "time-ran-out"}},
		{[]string{"cleanup"}, "POST /testing/cleanup", M{}},
		{[]string{"recipe", "validate", "recipe.yaml"}, "POST /testing/recipe/validate", M{"path": recipe}},
	}
	// Driving verbs all post {action: first word, args: the rest} to /testing/drive.
	for _, d := range []string{"snapshot", "click e12", "fill e3 hello", "type e3 hi", "press Enter", "wait 500", "wait --text Saved", "screenshot", "screenshot a.png", "console", "errors", "network", "record start run.mp4", "record stop"} {
		w := strings.Fields(d)
		tests = append(tests, row{w, "POST /testing/drive", M{"action": w[0], "args": w[1:]}})
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got := testingDaemon(t, http.StatusOK, `{}`)
			if _, err := runTesting(t, tc.args...); err != nil {
				t.Fatal(err)
			}
			method, path, _ := strings.Cut(tc.path, " ")
			if got.method != method || got.path != "/api/v1"+path {
				t.Fatalf("request = %s %s, want %s", got.method, got.path, tc.path)
			}
			if tc.body != nil {
				tc.body["sessionId"] = "w1"
				if fmt.Sprint(got.body) != fmt.Sprint(tc.body) { // Sprint sorts map keys and prints []string like []any
					t.Fatalf("body = %v, want %v", got.body, tc.body)
				}
			}
		})
	}
}

func TestTestingLongCallsOutlastServerBudgets(t *testing.T) {
	recipe, err := litmus.NewStore(t.TempDir()).LoadRecipe(litmus.DefaultRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipe.Seed) != 2 {
		t.Fatalf("default recipe seed count = %d, want two for DefaultLaunchBudget", len(recipe.Seed))
	}
	for _, tc := range []struct {
		name, path string
		args       []string
		budget     time.Duration
	}{
		{"launch head", "/testing/launch", []string{"launch", "head"}, litmus.DefaultLaunchBudget},
		{"launch base", "/testing/launch", []string{"launch", "base"}, litmus.DefaultLaunchBudget},
		{"prepare", "/testing/prepare", []string{"prepare"}, litmus.PrepareBudget},
		{"intake", "/testing/intake", []string{"intake", "o/r#7"}, litmus.IntakeBudget},
		{"recipe validate with SHA", "/testing/recipe/validate", []string{"recipe", "validate", "recipe.yaml", "--sha", strings.Repeat("a", 40)}, litmus.RecipeValidateBudget},
		{"post", "/testing/runs/run_w1/post", []string{"post", "run_w1"}, litmus.PostBudget},
		{"submit", "/testing/submit", []string{"submit"}, litmus.SubmitBudget},
		{"drive wait", "/testing/drive", []string{"drive", "wait", strconv.FormatInt(litmus.MaxDriveWait.Milliseconds(), 10)}, litmus.DriveBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "w1")
			cfg := setConfigEnv(t)
			if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/api/v1"+tc.path {
					return jsonResponse(http.StatusOK, `{}`), nil
				}
				calls++
				deadline, ok := req.Context().Deadline()
				if !ok {
					t.Error("testing request has no client deadline")
				} else if remaining := time.Until(deadline); remaining < tc.budget+time.Minute-time.Second {
					t.Errorf("client deadline in %s, want server budget %s plus at least 60s", remaining, tc.budget)
				}
				return jsonResponse(http.StatusOK, `{}`), nil
			})}
			_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, append([]string{"testing"}, tc.args...)...)
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if client.Timeout != time.Second {
				t.Fatalf("shared client timeout changed to %s", client.Timeout)
			}
		})
	}
}

func TestTestingCheckEndNamesThemeFiles(t *testing.T) {
	for _, tc := range []struct {
		name, receipt string
		want          []string
	}{
		{"pair", `{"themeShots":[{"theme":"light","path":"/run/receipts/c1-light.png"},{"theme":"dark","path":"/run/receipts/c1-dark.png"}]}`, []string{"after (light): /run/receipts/c1-light.png", "after (dark): /run/receipts/c1-dark.png"}},
		{"pinned", `{"themeShots":[{"theme":"light","path":"/run/receipts/c1-light.png"}],"themeNote":"theme pair not taken: the app ignored the system theme"}`, []string{"after (light): /run/receipts/c1-light.png", "theme pair not taken: the app ignored the system theme"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "w1")
			cfg := setConfigEnv(t)
			if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001}); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/v1/testing/status" {
					return jsonResponse(http.StatusOK, `{"run":{"deadline":"2030-01-01T10:09:01Z"}}`), nil
				}
				if req.URL.Path != "/api/v1/testing/checks/c1/end" {
					return jsonResponse(http.StatusOK, `{}`), nil
				}
				return jsonResponse(http.StatusOK, fmt.Sprintf(`{"id":"c1","name":"accounts","state":"pass","receipts":[%s]}`, tc.receipt)), nil
			})}
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, "testing", "check", "end", "c1", "--result", "pass")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range append([]string{"c1 pass accounts"}, tc.want...) {
				if !strings.Contains(out, want) {
					t.Errorf("check end lacks %q: %s", want, out)
				}
			}
			if tc.name == "pinned" && strings.Contains(out, "after (dark)") {
				t.Errorf("pinned page printed a second copy: %s", out)
			}
		})
	}
}

func TestTestingDrivePrintsCompactText(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 49301, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	want := "receipt 7: receipts/7-after.png 12ms page errors: none\n- button \"Save\" [ref=e1]"
	finishHint := "Finish now: open claim checks c1 \"Save button works\". Main twins are optional from here."
	deadline := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	for _, action := range []string{"click", "replay"} {
		t.Run(action, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/v1/testing/status" {
					return jsonResponse(http.StatusOK, fmt.Sprintf(`{"run":{"deadline":%q},"hints":[{"text":%q},{"checkId":"c2","text":"check-specific hint"}]}`, deadline, finishHint)), nil
				}
				if !strings.HasPrefix(req.URL.Path, "/api/v1/testing/") {
					return jsonResponse(http.StatusNoContent, ""), nil
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				path := "/api/v1/testing/drive"
				if action == "replay" {
					path = "/api/v1/testing/checks/c1/replay"
					if body["leg"] != "base" {
						t.Errorf("replay body %v", body)
					}
				} else if body["action"] != "click" {
					t.Errorf("drive body %v", body)
				}
				if req.URL.Path != path || body["sessionId"] != "w1" {
					t.Errorf("request %s body %v", req.URL.Path, body)
				}
				data, _ := json.Marshal(map[string]any{"text": want, "output": "large engine output"})
				return jsonResponse(http.StatusOK, string(data)), nil
			})}
			args := []string{"testing", "click", "e1"}
			if action == "replay" {
				args = []string{"testing", "replay", "c1", "--leg", "base"}
			}
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, args...)
			wantOutput := want
			if action == "click" {
				wantOutput += "\n60 min left\n" + finishHint
			}
			if err != nil || strings.TrimSpace(out) != wantOutput {
				t.Fatalf("output %q, error %v; want %q", out, err, wantOutput)
			}
		})
	}
}

func TestTestingUsageErrors(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	for _, args := range []string{
		"intake", "launch", "check add x", "check add --expect y", "check end c1", "check end c1 --result maybe",
		"replay", "replay c1", "replay c1 --leg head", "replay c1 --leg main", "check start", "check add x --expect y --leg main", "compare", "start", "start --pr x --harness codex", "submit --blocked", "submit --reason x", "submit --skip-base", "submit --skip-base --reason", "submit --skip-base --blocked --reason x", "submit --blocked --reason x --verdict-file f", "click", "fill e1", "type e1", "press", "wait", "wait 5 --text x", "record start", "recipe validate", "status extra",
	} {
		t.Run(args, func(t *testing.T) {
			if _, err := runTesting(t, strings.Fields(args)...); ExitCode(err) != 2 {
				t.Fatalf("err=%v exit=%d", err, ExitCode(err))
			}
		})
	}
	t.Setenv("AO_SESSION_ID", "")
	if _, err := runTesting(t, "status"); ExitCode(err) != 2 || !strings.Contains(err.Error(), "AO_SESSION_ID") {
		t.Fatalf("err=%v", err)
	}
}

func TestTestingOutputAndDaemonErrors(t *testing.T) {
	testingDaemon(t, http.StatusOK, `{"run":{"id":"run_1","state":"intake","repo":"o/r","prNumber":7},"ownerLogins":"owner logins: unchanged","checks":[{"id":"c1","name":"menu","state":"pass","receipts":[{"seq":1}]}]}`)
	out, err := runTesting(t, "status")
	if err != nil || !strings.Contains(out, "run run_1 intake o/r#7") || !strings.Contains(out, "c1 pass menu (1 receipts)") || !strings.Contains(out, "owner logins: unchanged") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	testingDaemon(t, http.StatusOK, `{"run":{"id":"run_1","state":"intake","repo":"o/r","prNumber":7,"error":"blocked at prepare: no registered AO project matches o/r"},"checks":[]}`)
	if out, err = runTesting(t, "status"); err != nil || !strings.Contains(out, "\nblocked at prepare: no registered AO project matches o/r") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	testingDaemon(t, http.StatusOK, `{"run":{"id":"run_1","state":"intake","repo":"o/r","prNumber":7},"checks":[]}`)
	var parsed struct{ Run struct{ ID string } }
	if out, err = runTesting(t, "status", "--json"); err != nil || json.Unmarshal([]byte(out), &parsed) != nil || parsed.Run.ID != "run_1" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	testingDaemon(t, http.StatusNotImplemented, `{"message":"not implemented in T1: prepare","code":"TESTING_NOT_IMPLEMENTED"}`)
	if _, err = runTesting(t, "prepare"); ExitCode(err) != 1 || !strings.Contains(err.Error(), "not implemented in T1: prepare (TESTING_NOT_IMPLEMENTED)") {
		t.Fatalf("err=%v", err)
	}
}

func TestTestingStatusJSONKeepsItsExistingShape(t *testing.T) {
	response := `{"ownerLogins":"owner logins: unchanged","run":{"id":"run_1","state":"running","repo":"o/r","prNumber":7},"checks":[{"id":"c1","name":"menu","state":"pending"}],"inFlight":[{"verb":"prepare","seconds":4}],"openClaims":[{"id":"c1","name":"menu","state":"pending"}],"hints":[{"text":"Finish now: open claim checks c1 \"menu\". Main twins are optional from here."}]}`
	for _, args := range [][]string{{"status", "--json"}, {"check", "list", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "w1")
			cfg := setConfigEnv(t)
			if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/v1/testing/status" {
					return jsonResponse(http.StatusOK, response), nil
				}
				return jsonResponse(http.StatusNoContent, ""), nil
			})}
			deps := Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}
			out, _, err := executeCLI(t, deps, append([]string{"testing"}, args...)...)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &fields); err != nil {
				t.Fatalf("output %q is not JSON: %v", out, err)
			}
			if _, ok := fields["hints"]; ok {
				t.Fatalf("structured output exposed text hints: %s", out)
			}
			if _, ok := fields["openClaims"]; ok {
				t.Fatalf("structured output exposed open claims: %s", out)
			}
			for _, key := range []string{"run", "checks", "ownerLogins", "inFlight"} {
				if _, ok := fields[key]; !ok {
					t.Errorf("structured output lost %q: %s", key, out)
				}
			}
		})
	}
}

func TestTestingCheckEndPrintsOnlyItsHints(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/internal/telemetry/cli-invoked":
			return jsonResponse(http.StatusNoContent, ""), nil
		case "/api/v1/testing/checks/c1/end":
			return jsonResponse(http.StatusOK, `{"id":"c1","name":"menu","state":"fail"}`), nil
		case "/api/v1/testing/status":
			return jsonResponse(http.StatusOK, fmt.Sprintf(`{"run":{"deadline":%q},"hints":[{"text":"general finish guidance"},{"checkId":"c1","text":"replay this check on main"},{"checkId":"c2","text":"unrelated check guidance"}]}`, deadline.Format(time.RFC3339Nano))), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	deps := Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client, Now: func() time.Time { return time.Date(2030, 1, 1, 9, 1, 0, 0, time.UTC) }}
	out, _, err := executeCLI(t, deps, "testing", "check", "end", "c1", "--result", "fail", "--note", "menu closed")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"c1 fail menu", "59 min left", "general finish guidance", "replay this check on main"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, "unrelated check guidance") {
		t.Fatalf("end printed another check's hint: %q", out)
	}
}

func TestTestingStartSpawnsThroughTheDaemon(t *testing.T) {
	const pr = "https://github.com/aoagents/agent-orchestrator/pull/6394"
	// One canned answer serves the project lookup and the start call.
	reply := func(agent string) string {
		return `{"project":{"id":"p1","config":{"worker":{"agent":"` + agent + `"}}},"sessionId":"ao-7","name":"litmus: PR #6394"}`
	}

	got := testingDaemon(t, http.StatusOK, reply("claude-code"))
	out, err := runTesting(t, "start", "--pr", pr, "--project", "p1")
	if err != nil || !strings.Contains(out, `spawned session ao-7 "litmus: PR #6394"`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got.method != "POST" || got.path != "/api/v1/testing/start" ||
		fmt.Sprint(got.body) != fmt.Sprint(map[string]any{"projectId": "p1", "prUrl": pr, "sessionId": "w1"}) { // no harness: the daemon picks it; the caller lets the daemon refuse a tester
		t.Fatalf("request = %s %s %v", got.method, got.path, got.body)
	}

	got = testingDaemon(t, http.StatusOK, reply("claude-code"))
	if _, err = runTesting(t, "start", "--pr", pr, "--project", "p1", "--no-comment"); err != nil || got.body["noComment"] != true {
		t.Fatalf("--no-comment: err=%v body=%v", err, got.body)
	}

	got = testingDaemon(t, http.StatusOK, reply("claude-code"))
	if _, err = runTesting(t, "start", "--pr", pr, "--project", "p1", "--no-evidence"); err != nil || got.body["noEvidence"] != true || got.body["noComment"] != nil {
		t.Fatalf("--no-evidence: err=%v body=%v", err, got.body)
	}

	got = testingDaemon(t, http.StatusOK, reply("codex"))
	if _, err := runTesting(t, "start", "--pr", pr, "--project", "p1", "--harness", "codex"); err != nil || got.path != "/api/v1/testing/start" || got.body["harness"] != "codex" {
		t.Fatalf("--harness codex: err=%v request %s %v", err, got.path, got.body)
	}
	got = testingDaemon(t, http.StatusOK, reply("codex"))
	if _, err := runTesting(t, "start", "--pr", pr, "--project", "p1", "--harness", "aider"); ExitCode(err) != 2 || !strings.Contains(err.Error(), "claude-code or codex") || got.path == "/api/v1/testing/start" {
		t.Fatalf("--harness aider: err=%v last request %s", err, got.path)
	}
}

func TestTestingSubmitPrintsWhatBecameOfTheComment(t *testing.T) {
	for reply, want := range map[string]string{
		`{"overall":"pass","commentState":"posted","commentUrl":"https://github.com/o/r/pull/7#issuecomment-9"}`: "verdict: pass\ncomment posted: https://github.com/o/r/pull/7#issuecomment-9",
		`{"overall":"fail","commentState":"failed:HTTP 403: Resource not accessible"}`:                           "verdict: fail\ncomment failed:HTTP 403: Resource not accessible",
		`{"overall":"blocked","commentState":"off"}`:                                                             "verdict: blocked\ncomment off",
		`{"overall":"pass"}`: "verdict: pass",
		`{"overall":"pass","intakeClaims":["Title claim","Body claim"]}`: "verdict: pass\nPR claims from intake:\n- Title claim\n- Body claim",
	} {
		testingDaemon(t, http.StatusOK, reply)
		if out, err := runTesting(t, "submit"); err != nil || strings.TrimSpace(out) != want {
			t.Errorf("%s: out=%q err=%v", reply, out, err)
		}
	}
}

func TestTestingPruneSendsFlagsAndPrintsLines(t *testing.T) {
	got := testingDaemon(t, http.StatusOK, `{"dryRun":true,"lines":["would remove checkout repos/o__r/abc (1.0 KB)","would prune: 1 checkouts, 0 runs, 0.0 GB freed"]}`)
	out, err := runTesting(t, "prune", "--keep-runs", "5", "--keep-checkouts", "0", "--dry-run")
	if err != nil || got.path != "/api/v1/testing/prune" || fmt.Sprint(got.body) != "map[dryRun:true keepCheckouts:0 keepRuns:5 sessionId:w1]" {
		t.Fatalf("request = %s %v err=%v", got.path, got.body, err)
	}
	if !strings.Contains(out, "would remove checkout repos/o__r/abc") || !strings.HasSuffix(strings.TrimSpace(out), "0.0 GB freed") {
		t.Fatalf("out = %q", out)
	}
	if _, err := runTesting(t, "prune"); err != nil || fmt.Sprint(got.body) != "map[sessionId:w1]" { // only the caller; the flags stay unset
		t.Fatalf("defaults must be left to the daemon: body=%v err=%v", got.body, err)
	}
}

func TestTestingPanelVerbs(t *testing.T) {
	t.Setenv("AO_PROJECT_ID", "")
	type M = map[string]any
	for _, tc := range []struct {
		args, reply   string
		method, path  string
		body          M
		want, wantNot string
	}{
		{args: "runs --project p1 --limit 5", reply: `{"runs":[{"id":"run_ao-25","verdict":"pass","state":"submitted","repo":"o/r","pr":{"number":7},"durationSeconds":420,"commentState":"posted","commentUrl":"https://github.com/o/r/pull/7#issuecomment-9"}]}`,
			method: "GET", path: "/testing/runs?limit=5&projectId=p1", want: "run_ao-25 pass submitted o/r#7 7m0s | comment posted: https://github.com/o/r/pull/7#issuecomment-9"},
		{args: "runs", reply: `{"runs":[]}`, method: "GET", path: "/testing/runs", want: "no runs"},
		{args: "rerun run_ao-25", reply: `{"sessionId":"ao-26","name":"litmus: PR #7","runId":"run_ao-26"}`,
			method: "POST", path: "/testing/rerun", body: M{"runId": "run_ao-25", "sessionId": "w1"}, want: `spawned session ao-26 "litmus: PR #7", run run_ao-26`},
		{args: "rerun run_ao-25 --project p2", reply: `{}`, method: "POST", path: "/testing/rerun", body: M{"runId": "run_ao-25", "projectId": "p2", "sessionId": "w1"}},
		{args: "post run_ao-25", reply: `{"id":"run_ao-25","state":"submitted","repo":"o/r","prNumber":7,"commentState":"posted","commentUrl":"https://github.com/o/r/pull/7#issuecomment-9"}`,
			method: "POST", path: "/testing/runs/run_ao-25/post", body: M{"sessionId": "w1"}, want: "comment posted: https://github.com/o/r/pull/7#issuecomment-9"},
		{args: "discard run_ao-25", reply: `{"id":"run_ao-25","state":"cleaned","repo":"o/r","prNumber":7,"commentState":"discarded"}`,
			method: "POST", path: "/testing/runs/run_ao-25/discard", body: M{"sessionId": "w1"}, want: "comment discarded"},
		{args: "settings --project p1", reply: `{"askBeforePosting":true}`, method: "GET", path: "/testing/projects/p1/settings", want: "ask-before-posting on"},
		{args: "settings --project p1 --ask-before-posting off", reply: `{"askBeforePosting":false}`, method: "PUT", path: "/testing/projects/p1/settings", body: M{"askBeforePosting": false, "review": false}, want: "ask-before-posting off"},
		{args: "settings --project p1 --ask-before-posting on", reply: `{"askBeforePosting":true}`, method: "PUT", path: "/testing/projects/p1/settings", body: M{"askBeforePosting": true, "review": false}, want: "ask-before-posting on"},
		// A set reads the stored settings first and sends them back with only the given flags changed.
		{args: "settings --project p1 --model opus --effort max", reply: `{"askBeforePosting":true}`, method: "PUT", path: "/testing/projects/p1/settings",
			body: M{"askBeforePosting": true, "model": "opus", "effort": "max", "review": false}, want: "ask-before-posting on"},
		{args: "settings --project p1 --model=", reply: `{"askBeforePosting":true,"model":"opus","effort":"max"}`, method: "PUT", path: "/testing/projects/p1/settings",
			body: M{"askBeforePosting": true, "effort": "max", "review": false}},
		{args: "settings --project p1 --harness codex --model gpt-6.1-sol --effort high", reply: `{"askBeforePosting":true,"harness":"codex","model":"gpt-6.1-sol","effort":"high"}`, method: "PUT", path: "/testing/projects/p1/settings",
			body: M{"askBeforePosting": true, "harness": "codex", "model": "gpt-6.1-sol", "effort": "high", "review": false}, want: "harness codex | model gpt-6.1-sol | effort high"},
		{args: "settings --project p1", reply: `{"askBeforePosting":false,"model":"opus"}`, method: "GET", path: "/testing/projects/p1/settings", want: "harness (default) | model opus | effort (default)"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			got := testingDaemon(t, http.StatusOK, tc.reply)
			out, err := runTesting(t, strings.Fields(tc.args)...)
			if err != nil || got.method != tc.method || got.path != "/api/v1"+tc.path || (tc.body != nil && fmt.Sprint(got.body) != fmt.Sprint(tc.body)) || !strings.Contains(out, tc.want) {
				t.Fatalf("request %s %s %v, out %q, err %v", got.method, got.path, got.body, out, err)
			}
		})
	}
	for _, args := range []string{"rerun", "post", "discard", "settings", "settings --project p1 --ask-before-posting maybe", "runs extra"} {
		t.Run("usage "+args, func(t *testing.T) {
			testingDaemon(t, http.StatusOK, `{}`)
			if _, err := runTesting(t, strings.Fields(args)...); ExitCode(err) != 2 {
				t.Fatalf("err=%v exit=%d", err, ExitCode(err))
			}
		})
	}
	got := testingDaemon(t, http.StatusOK, `{"runs":[]}`)
	t.Setenv("AO_PROJECT_ID", "envproj") // the environment names the project when no flag does
	if _, err := runTesting(t, "runs"); err != nil || got.path != "/api/v1/testing/runs?projectId=envproj" {
		t.Fatalf("path %s err %v", got.path, err)
	}
}

// A stored model belongs to the old harness, so a harness change must restate the model (or clear it).
func TestTestingSettingsHarnessChangeNeedsTheModel(t *testing.T) {
	t.Setenv("AO_PROJECT_ID", "")
	got := testingDaemon(t, http.StatusOK, `{"askBeforePosting":true,"harness":"claude-code","model":"opus"}`)
	_, err := runTesting(t, "settings", "--project", "p1", "--harness", "codex")
	if ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), "--model") || got.method == "PUT" {
		t.Fatalf("harness change kept the stored model: err=%v method=%s", err, got.method)
	}
	if _, err := runTesting(t, "settings", "--project", "p1", "--harness", "codex", "--model", ""); err != nil || got.method != "PUT" {
		t.Fatalf("clearing the model with the harness change: err=%v method=%s", err, got.method)
	}
	got = testingDaemon(t, http.StatusOK, `{"askBeforePosting":true}`)
	if _, err := runTesting(t, "settings", "--project", "p1", "--harness", "codex"); err != nil || got.method != "PUT" {
		t.Fatalf("harness change with no stored model: err=%v method=%s", err, got.method)
	}
}

func TestTestingSettingsReviewFlag(t *testing.T) {
	t.Setenv("AO_PROJECT_ID", "")
	got := testingDaemon(t, http.StatusOK, `{"askBeforePosting":true}`)
	if _, err := runTesting(t, "settings", "--project", "p1", "--review", "on"); err != nil || got.method != "PUT" || got.body["review"] != true {
		t.Fatalf("--review on: err=%v method=%s body=%v", err, got.method, got.body)
	}
	if _, err := runTesting(t, "settings", "--project", "p1", "--review", "maybe"); ExitCode(err) != 2 {
		t.Fatalf("--review maybe: err=%v", err)
	}
}

func TestTestingLaunchPrintsAFailedSeedStep(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	prGap := "The PR build started without its sample content (seed step 2 failed: no project)."
	mainGap := "The main build started without its sample content (seed step 2 failed: no project)."
	for _, c := range []struct{ leg, gap, other string }{{"head", prGap, mainGap}, {"base", mainGap, prGap}} {
		t.Run(c.leg, func(t *testing.T) {
			testingDaemon(t, http.StatusOK, `{"id":"run_w1","state":"running","gaps":["I could not read the linked issue #1 (not found).","`+prGap+`","`+mainGap+`"]}`)
			out, err := runTesting(t, "launch", c.leg)
			if err != nil || !strings.Contains(out, "\n"+c.gap) || strings.Contains(out, c.other) || strings.Contains(out, "linked issue") {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestRunRowLineShowsStoppedReason(t *testing.T) {
	out := runRowLine(litmus.RunSummary{ID: "run_w1", State: litmus.RunExpired, Verdict: "expired", Error: litmus.ReasonStopped})
	if !strings.Contains(out, "expired expired") || !strings.HasSuffix(out, " | "+litmus.ReasonStopped) {
		t.Fatalf("stopped run: %q", out)
	}
}

func TestLaunchLinePrintsOnlyItsBuildSeedGap(t *testing.T) {
	prGap := "The PR build started without its sample content (seed step 2 failed: no project)."
	mainGap := "The main build started without its sample content (seed step 2 failed: no project)."
	run := litmus.Run{ID: "run_w1", Gaps: []string{prGap, mainGap, "I could not read the linked issue #1 (not found)."}}
	for _, c := range []struct{ leg, gap, other string }{{"head", prGap, mainGap}, {"base", mainGap, prGap}} {
		out := launchLine(run, c.leg)
		if !strings.Contains(out, "\n"+c.gap) || strings.Contains(out, c.other) || strings.Contains(out, "linked issue") {
			t.Errorf("launch %s: %q", c.leg, out)
		}
	}
}

// testingShowTransport serves run detail without binding a local socket.
func testingShowTransport(t *testing.T, status int, body string) (*testingCapture, Deps) {
	t.Helper()
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	got := &testingCapture{}
	deps := Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(req.URL.Path, "/api/v1/testing/") {
			return jsonResponse(http.StatusNoContent, ""), nil
		}
		*got = testingCapture{method: req.Method, path: req.URL.RequestURI()}
		return jsonResponse(status, body), nil
	})}}
	return got, deps
}

func TestTestingShow(t *testing.T) {
	const table = "| check | head |\n|---|---|\n| menu | fail |"
	detail := litmus.RunDetail{
		RunSummary: litmus.RunSummary{ID: "run_w1", Repo: "o/r", PR: litmus.RunPR{Number: 7}, State: litmus.RunAwaitingApproval, Verdict: "fail"},
		VerdictMD:  "# Verdict: fail\n\nprivate summary\n\n## Checks\n\n" + table + "\n\n## Notes\n\nprivate notes",
		CommentMD:  "legacy summary",
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	got, deps := testingShowTransport(t, http.StatusOK, string(raw))
	t.Setenv("AO_SESSION_ID", "")
	dir := filepath.Join(os.Getenv("AO_DATA_DIR"), "litmus", "runs", "run_w1", "comments")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "home-changes.txt"), []byte("MODIFIED \"/owner/.codex/config.toml\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"1.md":     "first bug\n\n![comparison](bug-7.png)\n\n[Watch the check](../clips/menu.mp4)",
		"2.md":     "second bug",
		"10.md":    "tenth bug",
		"notes.md": "ignore this draft",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := executeCLI(t, deps, "testing", "show", "run_w1")
	if err != nil || got.method != "GET" || got.path != "/api/v1/testing/runs/run_w1" {
		t.Fatalf("request %s %s, err %v", got.method, got.path, err)
	}
	for _, want := range []string{"run run_w1 o/r#7 awaiting-approval verdict fail", table, "first bug", "second bug", "tenth bug", "Owner home changes:\nAny process on this Mac can cause these; Litmus does not attribute them.\nMODIFIED \"/owner/.codex/config.toml\"", filepath.Join(dir, "1.md"), "media: " + filepath.Join(dir, "bug-7.png"), "media: " + filepath.Join(filepath.Dir(dir), "clips", "menu.mp4")} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %s", want, out)
		}
	}
	for _, absent := range []string{"private summary", "private notes", "legacy summary", "ignore this draft"} {
		if strings.Contains(out, absent) {
			t.Errorf("output includes %q: %s", absent, out)
		}
	}
	if strings.Index(out, "first bug") > strings.Index(out, "second bug") || strings.Index(out, "second bug") > strings.Index(out, "tenth bug") {
		t.Fatalf("drafts out of numeric order: %s", out)
	}
	// JSON returns only RunDetail, without reading or appending local drafts.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err = executeCLI(t, deps, "testing", "show", "run_w1", "--json")
	var jsonDetail litmus.RunDetail
	if err != nil || json.Unmarshal([]byte(out), &jsonDetail) != nil || jsonDetail.VerdictMD != detail.VerdictMD || strings.Contains(out, "first bug") {
		t.Fatalf("JSON = %s, err %v", out, err)
	}
	if _, _, err := executeCLI(t, deps, "testing", "show", "run_w1"); err == nil {
		t.Fatal("expected draft read error")
	}
}

func TestTestingShowFallbackAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		status, exit      int
		args              []string
	}{
		{name: "legacy comment", reply: `{"id":"run_w1","commentMd":"legacy comment"}`, want: "legacy comment", status: http.StatusOK, args: []string{"show", "run_w1"}},
		{name: "unfinished run", reply: `{"id":"run_w1","state":"running","verdict":"none"}`, want: "running verdict none", status: http.StatusOK, args: []string{"show", "run_w1"}},
		{name: "stopped run", reply: `{"id":"run_w1","state":"expired","verdict":"expired","error":"stopped before it finished"}`, want: "expired verdict expired\n\nstopped before it finished", status: http.StatusOK, args: []string{"show", "run_w1"}},
		{name: "daemon error", reply: `{"code":"TESTING_RUN_NOT_FOUND","message":"no testing run run_missing","requestId":"req-show"}`, want: "no testing run run_missing (TESTING_RUN_NOT_FOUND) [request req-show]", status: http.StatusNotFound, exit: 1, args: []string{"show", "run_missing"}},
		{name: "missing id", reply: `{}`, status: http.StatusOK, exit: 2, args: []string{"show"}},
		{name: "extra id", reply: `{}`, status: http.StatusOK, exit: 2, args: []string{"show", "run_w1", "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, deps := testingShowTransport(t, tc.status, tc.reply)
			t.Setenv("AO_SESSION_ID", "")
			out, _, err := executeCLI(t, deps, append([]string{"testing"}, tc.args...)...)
			if ExitCode(err) != tc.exit {
				t.Fatalf("out %q, err %v", out, err)
			}
			if err != nil {
				out = err.Error()
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output %q lacks %q", out, tc.want)
			}
		})
	}
}

func TestTestingDrivePassesBrowserFlagsAndLeg(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		args, want []string
		action     string
	}{
		{[]string{"drive", "hover", "e1", "--leg", "head"}, []string{"e1", "--leg", "head"}, "hover"},
		{[]string{"drive", "scroll", "down", "500", "--leg=base"}, []string{"down", "500", "--leg=base"}, "scroll"},
		{[]string{"drive", "snapshot", "-i", "--json", "--leg", "head"}, []string{"-i", "--json", "--leg", "head"}, "snapshot"},
		{[]string{"drive", "eval", "document.title"}, []string{"document.title"}, "eval"},
		{[]string{"drive", "cdp", "Page.reload", `{"ignoreCache":true}`}, []string{"Page.reload", `{"ignoreCache":true}`}, "cdp"},
		{[]string{"--leg", "head", "drive", "dblclick", "e1"}, []string{"e1", "--leg", "head"}, "dblclick"},
	} {
		calls := 0
		deps := Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/api/v1/testing/drive" {
				return jsonResponse(http.StatusNoContent, ""), nil
			}
			calls++
			var body struct {
				Action    string
				Args      []string
				SessionID string
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Action != row.action || strings.Join(body.Args, "|") != strings.Join(row.want, "|") || body.SessionID != "w1" {
				t.Errorf("request %+v want action=%s args=%v", body, row.action, row.want)
			}
			return jsonResponse(http.StatusOK, `{"text":"browser result"}`), nil
		})}}
		out, _, err := executeCLI(t, deps, append([]string{"testing"}, row.args...)...)
		if err != nil || calls != 1 || !strings.Contains(out, "browser result") {
			t.Fatalf("%v: calls=%d out=%q err=%v", row.args, calls, out, err)
		}
	}
}

func TestTestingDriveHelpListsNewActions(t *testing.T) {
	out, _, err := executeCLI(t, Deps{}, "testing", "drive", "--help")
	if err != nil || !strings.Contains(out, "cdp") || !strings.Contains(out, "Page-changing") {
		t.Fatalf("drive help %q %v", out, err)
	}
}

func TestTestingCheckLineShowsRisk(t *testing.T) {
	line := checkLine(litmus.Check{ID: "c1", Name: "git push", State: litmus.CheckPending, Purpose: "risk", Risk: "HOME moves credentials"})
	if !strings.Contains(line, "[risk]: HOME moves credentials") {
		t.Fatalf("status check line = %q", line)
	}
}

func TestTestingCheckLineShowsSameBug(t *testing.T) {
	line := checkLine(litmus.Check{ID: "c2", Name: "settings", State: litmus.CheckFail, SameBug: "c1"})
	if !strings.Contains(line, "[same-bug: c1]") {
		t.Fatalf("status check line = %q", line)
	}
}

func TestTestingCheckEndHelpExplainsSameBugLifecycle(t *testing.T) {
	out, _, err := executeCLI(t, Deps{}, "testing", "check", "end", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "an ended failed or partial head check for the same bug; set when ending and change only by re-ending") {
		t.Fatalf("check end help = %q", out)
	}
}

func TestTestingCheckListShowsSameBug(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/internal/telemetry/cli-invoked" {
			return jsonResponse(http.StatusNoContent, ""), nil
		}
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/testing/status" || req.URL.Query().Get("sessionId") != "w1" {
			t.Fatalf("request = %s %s", req.Method, req.URL.RequestURI())
		}
		return jsonResponse(http.StatusOK, `{"checks":[{"id":"c2","name":"settings","expect":"opens","state":"fail","sameBug":"c1"}]}`), nil
	})}
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, HTTPClient: client}, "testing", "check", "list")
	if err != nil {
		t.Fatal(err)
	}
	if want := "c2 fail settings [same-bug: c1] (0 receipts): opens"; !strings.Contains(out, want) {
		t.Fatalf("check list output = %q, want %q", out, want)
	}
}
