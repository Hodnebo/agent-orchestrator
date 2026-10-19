package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/triage"
)

func TestSupportHostBootAndArgumentErrorsDoNotContactDaemon(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dir)
	t.Setenv("AO_PORT", "invalid-for-daemon")
	t.Setenv("AO_RUN_FILE", filepath.Join(dir, "missing-running.json"))
	for _, args := range [][]string{
		{"support-host"}, {"support-host", "--data-dir", dir},
		{"support-host", "extra"}, {"support-host", "--unknown-private-flag"},
		{"support-host", "--data-dir"}, {"support-host", "--data-dir", "relative"},
		{"support-host", "--tool"}, {"support-host", "--run", "report"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			called := false
			var out bytes.Buffer
			err := executeWithDeps(Deps{In: strings.NewReader(""), Out: &out, Err: io.Discard,
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("support invocation contacted the daemon or telemetry")
					return nil, nil
				})},
				SupportHost: func(_ context.Context, _ io.Reader, _ io.Writer, dataDir string) error {
					called = true
					if dataDir != dir {
						t.Fatalf("data dir=%s", dataDir)
					}
					return nil
				},
			}, args)
			valid := len(args) == 1 || len(args) == 3 && args[2] == dir
			if valid && (err != nil || !called) {
				t.Fatalf("independent boot failed: %v", err)
			}
			if !valid && (called || ExitCode(err) != 2) {
				t.Fatalf("invalid invocation = called %v, error %v", called, err)
			}
		})
	}
}

func TestSupportHostToolReadsOneExplicitOperationWithoutServeDaemonOrConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dir)
	t.Setenv("AO_PORT", "invalid-for-daemon")
	t.Setenv("AO_RUN_FILE", filepath.Join(dir, "missing-running.json"))
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("invalid: [configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(dir, "reports", "triage", "runs", "cli-tool")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"manifest.json":       triage.Run{ID: "cli-tool", Version: 1, Execution: "session", State: "running", SessionID: "ordinary-session", Request: triage.StartRequest{PreparationID: "cli-tool"}, Messages: []triage.Message{}},
		"session-launch.json": map[string]any{"request": triage.StartRequest{PreparationID: "cli-tool"}, "launch": triage.SessionLaunch{ClientRequestID: "triage-cli-tool", Prompt: "frozen prompt", Attachments: []triage.LaunchAttachment{{MimeType: "application/json", Data: "e30="}, {MimeType: "text/markdown", Data: "eA=="}}}},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ input, status string }{
		{`{"operation":"get"}`, "ok"},
		{`ordinary assistant prose`, "invalid_request"},
		{`{"operation":"get"} {"operation":"publish"}`, "invalid_request"},
	} {
		var out bytes.Buffer
		err := executeWithDeps(Deps{In: strings.NewReader(test.input), Out: &out, Err: io.Discard,
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("one-shot support tool contacted daemon or telemetry")
				return nil, nil
			})},
			SupportHost: func(context.Context, io.Reader, io.Writer, string) error {
				t.Fatal("one-shot tool entered Serve/recovery")
				return nil
			},
		}, []string{"support-host", "--tool", "--run", "cli-tool"})
		var response triage.ToolResponse
		if err != nil || json.Unmarshal(out.Bytes(), &response) != nil || response.Status != test.status {
			t.Fatalf("tool output=%s, error=%v, want %s", out.String(), err, test.status)
		}
	}
	raw, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil || !strings.Contains(string(raw), `"state":"running"`) {
		t.Fatalf("one-shot tool recovered an external ordinary session: %s, %v", raw, err)
	}
}
