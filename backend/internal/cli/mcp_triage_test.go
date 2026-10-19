package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/triage"
)

func mcpReportFixture(t *testing.T) (string, triage.Run) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dataDir)
	t.Setenv("AO_RUN_FILE", filepath.Join(dataDir, "absent-running.json"))
	t.Setenv("AO_PORT", "invalid-daemon-config")
	t.Setenv("AO_SESSION_ID", "investigation-session")
	if err := os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte("broken: [daemon config"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := triage.Run{
		ID: "mcp-report", Version: triage.ProtocolVersion, Execution: "session", SessionLaunchStarted: true,
		SessionID: "investigation-session", State: "session_linked", Messages: []triage.Message{},
		Request: triage.StartRequest{PreparationID: "mcp-report", Origin: triage.Origin{SessionID: "affected-session"}},
	}
	dir := filepath.Join(dataDir, "reports", "triage", "runs", run.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMCPReportRun(t, dataDir, run)
	record, err := json.Marshal(map[string]any{
		"request": run.Request,
		"launch": triage.SessionLaunch{ClientRequestID: "triage-" + run.ID, Prompt: "frozen investigation",
			Attachments: []triage.LaunchAttachment{{MimeType: "application/json", Data: "e30="}, {MimeType: "text/markdown", Data: "eA=="}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-launch.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir, run
}

func writeMCPReportRun(t *testing.T, dataDir string, run triage.Run) {
	t.Helper()
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "reports", "triage", "runs", run.ID, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mcpSupportResult(t *testing.T, reply mcpReply) triage.ToolResponse {
	t.Helper()
	if reply.Error != nil || len(reply.Result.Content) != 1 || reply.Result.Content[0].Type != "text" {
		t.Fatalf("report_problem did not return an MCP text result: %#v", reply)
	}
	var response triage.ToolResponse
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &response); err != nil || response.Version != triage.ProtocolVersion {
		t.Fatalf("report_problem returned invalid support JSON: %q, %v", reply.Result.Content[0].Text, err)
	}
	if reply.Result.IsError != (response.Status != "ok") {
		t.Fatalf("support status %q mapped to isError=%t", response.Status, reply.Result.IsError)
	}
	return response
}

func TestMCPReportProblemSchemaDeclaresExplicitOperationsAndMutation(t *testing.T) {
	tools := runMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)[0].Result.Tools
	found := false
	for _, tool := range tools {
		if tool.Name != "report_problem" {
			continue
		}
		found = true
		if !slices.Equal(tool.InputSchema.Required, []string{"runId", "request"}) || tool.InputSchema.Properties["runId"]["maxLength"] != float64(128) {
			t.Fatalf("report_problem input schema = %#v", tool.InputSchema)
		}
		request := tool.InputSchema.Properties["request"]
		properties, ok := request["properties"].(map[string]any)
		if !ok || request["additionalProperties"] != false {
			t.Fatalf("request schema = %#v", request)
		}
		operation := properties["operation"].(map[string]any)
		if !reflect.DeepEqual(operation["enum"], []any{"get", "context-read", "search", "bind-session", "save", "publish"}) {
			t.Fatalf("report operations = %#v", operation)
		}
		for key, want := range map[string]bool{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true} {
			if tool.Annotations[key] != want {
				t.Errorf("%s = %v, want %t", key, tool.Annotations[key], want)
			}
		}
	}
	if !found {
		t.Fatal("existing AO MCP server omitted report_problem")
	}
}

func TestMCPReportProblemGetsBoundRunWithoutDaemonOrMutableDatabase(t *testing.T) {
	dataDir, run := mcpReportFixture(t)
	run.Evidence = []triage.Evidence{{ID: "fact", Status: "collected", Text: "retained symptom token=synthetic-private-value"}}
	writeMCPReportRun(t, dataDir, run)
	manifest := filepath.Join(dataDir, "reports", "triage", "runs", run.ID, "manifest.json")
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	input := mcpCall("report_problem", map[string]any{"runId": run.ID, "request": map[string]any{"operation": "get"}})
	stdout, stderr, err := executeCLI(t, Deps{
		In: strings.NewReader(input + "\n"),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("report_problem contacted the daemon or telemetry")
			return nil, nil
		})},
		SupportHost: func(context.Context, io.Reader, io.Writer, string) error {
			t.Fatal("report_problem entered Serve/provider recovery")
			return nil
		},
	}, "mcp")
	if err != nil || stderr != "" {
		t.Fatalf("ao mcp report_problem: err=%v stderr=%s", err, stderr)
	}
	var reply mcpReply
	if err := json.Unmarshal([]byte(stdout), &reply); err != nil {
		t.Fatal(err)
	}
	response := mcpSupportResult(t, reply)
	if response.Status != "ok" || response.Run == nil || response.Run.ID != run.ID || response.Run.SessionID != "investigation-session" || response.Origin.SessionID != "affected-session" || strings.Contains(stdout, "synthetic-private-value") {
		t.Fatalf("session-bound report result = %s", stdout)
	}
	after, _ := os.ReadFile(manifest)
	if !bytes.Equal(before, after) {
		t.Fatal("reading through MCP changed the report")
	}
	for _, name := range []string{"ao.db", "database.sqlite", "running.json", "agent-runtime", "session-tools.json"} {
		path := filepath.Join(dataDir, name)
		if name == "session-tools.json" {
			path = filepath.Join(filepath.Dir(manifest), name)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("get unexpectedly created %s: %v", name, err)
		}
	}
}

func TestMCPReportProblemRejectsForeignOrUnlinkedRunBeforeDependencies(t *testing.T) {
	for _, test := range []struct {
		name, savedSession string
		intent             bool
	}{
		{"foreign-session", "other-investigation", true},
		{"link-not-ready", "", true},
		{"no-create-intent", "investigation-session", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir, run := mcpReportFixture(t)
			run.SessionID, run.SessionLaunchStarted, run.Request.PublishAuthorized = test.savedSession, test.intent, true
			run.Draft = &triage.Draft{Title: "Saved symptom", Body: "The human observed a blank screen."}
			writeMCPReportRun(t, dataDir, run)
			var called int
			deps := triage.Dependencies{
				ReadContext: func(context.Context, triage.Origin, triage.ContextRead) (triage.Evidence, error) {
					called++
					return triage.Evidence{}, errors.New("must not read")
				},
				SearchReports: func(context.Context, string) (triage.ReportSearch, error) {
					called++
					return triage.ReportSearch{}, errors.New("must not search")
				},
				Publish: func(context.Context, triage.Run, func(triage.Publication) error) (triage.Publication, error) {
					called++
					return triage.Publication{}, errors.New("must not publish")
				},
				StartConversation: func(context.Context, triage.Run) (ports.ChatConversation, error) {
					called++
					return nil, errors.New("must not launch")
				},
				RecoverConversation: func(context.Context, triage.Run) error { called++; return errors.New("must not recover") },
			}
			manifest := filepath.Join(dataDir, "reports", "triage", "runs", run.ID, "manifest.json")
			before, _ := os.ReadFile(manifest)
			commands := []triage.ToolCommand{
				{Operation: "get"}, {Operation: "context-read", Request: &triage.ContextRead{Source: "session-history"}},
				{Operation: "search", Query: "blank screen"}, {Operation: "publish"},
				{Operation: "save", Draft: &triage.Draft{Title: "Changed", Body: "must not be stored"}},
				{Operation: "bind-session", SessionID: "affected-session", UserSelected: true},
			}
			for _, command := range commands {
				raw, _ := json.Marshal(command)
				var out bytes.Buffer
				if err := triage.ServeTool(t.Context(), bytes.NewReader(raw), &out, run.ID, triage.Options{DataDir: dataDir, CallerSessionID: "investigation-session", Deps: deps}); err != nil {
					t.Fatal(err)
				}
				var response triage.ToolResponse
				if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Status != "not_authorized" || response.Run != nil {
					t.Fatalf("%s crossed session binding: %s, %v", command.Operation, out.String(), err)
				}
			}
			if called != 0 {
				t.Fatalf("session mismatch reached %d dependencies", called)
			}
			after, _ := os.ReadFile(manifest)
			if !bytes.Equal(before, after) {
				t.Fatal("denied MCP operations changed the report")
			}
			response := mcpSupportResult(t, runMCP(t, mcpCall("report_problem", map[string]any{"runId": run.ID, "request": map[string]any{"operation": "get"}}))[0])
			if response.Status != "not_authorized" {
				t.Fatalf("MCP handler omitted trusted caller identity: %#v", response)
			}
		})
	}
}

func TestMCPReportProblemUnauthorizedPublishDoesNotSaveOrPublish(t *testing.T) {
	dataDir, run := mcpReportFixture(t)
	manifest := filepath.Join(dataDir, "reports", "triage", "runs", run.ID, "manifest.json")
	before, _ := os.ReadFile(manifest)
	request := triage.ToolCommand{Operation: "publish", Draft: &triage.Draft{Title: "Must not save", Body: "This publish operation has no initial authorization."}}
	var published int
	raw, _ := json.Marshal(request)
	var out bytes.Buffer
	if err := triage.ServeTool(t.Context(), bytes.NewReader(raw), &out, run.ID, triage.Options{
		DataDir: dataDir, CallerSessionID: "investigation-session",
		Deps: triage.Dependencies{Publish: func(context.Context, triage.Run, func(triage.Publication) error) (triage.Publication, error) {
			published++
			return triage.Publication{}, nil
		}},
	}); err != nil {
		t.Fatal(err)
	}
	var response triage.ToolResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Status != "not_authorized" || published != 0 {
		t.Fatalf("unauthorized publication = %s, calls=%d, error=%v", out.String(), published, err)
	}
	response = mcpSupportResult(t, runMCP(t, mcpCall("report_problem", map[string]any{"runId": run.ID, "request": request}))[0])
	if response.Status != "not_authorized" {
		t.Fatalf("MCP publication bypassed initial authorization: %#v", response)
	}
	after, _ := os.ReadFile(manifest)
	if !bytes.Equal(before, after) {
		t.Fatal("unauthorized publication changed saved draft or intent")
	}
}

func TestMCPReportProblemRejectsExtraMalformedAndOversizeArguments(t *testing.T) {
	_, run := mcpReportFixture(t)
	for _, args := range []map[string]any{
		nil,
		{"runId": run.ID},
		{"runId": run.ID, "request": nil},
		{"runId": run.ID, "request": []any{}},
		{"runId": "../escape", "request": map[string]any{"operation": "get"}},
		{"runId": run.ID, "dataDir": "/private/other-data", "request": map[string]any{"operation": "get"}},
		{"runId": run.ID, "callerSessionId": "forged", "request": map[string]any{"operation": "get"}},
		{"runId": run.ID, "request": map[string]any{"operation": "get", "origin": map[string]any{"sessionId": "other"}}},
		{"runId": run.ID, "request": map[string]any{"operation": "publish", "publishAuthorized": true}},
		{"runId": run.ID, "request": map[string]any{"operation": "search", "query": strings.Repeat("x", maxMCPReportBytes+1)}},
	} {
		reply := runMCP(t, mcpCall("report_problem", args))[0]
		if reply.Error != nil || !reply.Result.IsError || len(reply.Result.Content) != 1 || !strings.Contains(reply.Result.Content[0].Text, "report_problem") {
			t.Fatalf("bad report arguments did not produce a clear MCP tool error: %#v", reply)
		}
	}
	t.Setenv("AO_SESSION_ID", "")
	reply := runMCP(t, mcpCall("report_problem", map[string]any{"runId": run.ID, "request": map[string]any{"operation": "get"}}))[0]
	if reply.Error != nil || !reply.Result.IsError || !strings.Contains(reply.Result.Content[0].Text, "AO_SESSION_ID") {
		t.Fatalf("missing trusted caller was accepted: %#v", reply)
	}
}

func TestMCPReportProblemResponseRemainsBounded(t *testing.T) {
	dataDir, run := mcpReportFixture(t)
	run.Messages = []triage.Message{{ID: "large", Role: "user", Text: strings.Repeat("x", maxMCPReportBytes+1)}}
	writeMCPReportRun(t, dataDir, run)
	reply := runMCP(t, mcpCall("report_problem", map[string]any{"runId": run.ID, "request": map[string]any{"operation": "get"}}))[0]
	response := mcpSupportResult(t, reply)
	if response.Status != "unavailable" || len(reply.Result.Content[0].Text) > maxMCPReportBytes || !strings.Contains(response.Error, "exceeded its byte limit") {
		t.Fatalf("large support response escaped its cap: %#v", response)
	}
}

func TestMCPReportProblemUnknownRunDoesNotCreateStorage(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dataDir)
	t.Setenv("AO_RUN_FILE", filepath.Join(dataDir, "missing-running.json"))
	t.Setenv("AO_PORT", "invalid-daemon-config")
	t.Setenv("AO_SESSION_ID", "ordinary-coding-session")
	response := mcpSupportResult(t, runMCP(t, mcpCall("report_problem", map[string]any{"runId": "unknown-report", "request": map[string]any{"operation": "get"}}))[0])
	if response.Status != "not_authorized" {
		t.Fatalf("non-report session operation = %#v", response)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("non-report MCP call created AO state: %v, error=%v", entries, err)
	}
}
