package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

type chatStartStore struct {
	reconnectStore
	moved     bool
	calls     int
	code      string
	detail    string
	workerID  string
	workerErr error
}

func (s *chatStartStore) FallBackChatStart(_ context.Context, _, _, code, detail string) (bool, error) {
	s.calls++
	s.code, s.detail = code, detail
	return s.moved, nil
}

func (s *chatStartStore) WorkerFallBackChatStart(_ context.Context, _, _, workerID string, _ int64, code, detail string) (bool, error) {
	s.calls++
	s.code, s.detail, s.workerID = code, detail, workerID
	return s.moved, s.workerErr
}

func reconnectLaunch(t *testing.T, srv *Server, capabilities string) worker.LaunchContext {
	t.Helper()
	req := workerRequest(t, http.MethodGet, "/worker/reconnect", "", "worker:connect")
	if capabilities != "" {
		req.Header.Set(worker.CapabilitiesHeader, capabilities)
	}
	w := httptest.NewRecorder()
	srv.workerReconnect(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body)
	}
	var resp worker.BootstrapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Launch
}

// A worker that does not report it can start in Chat gets a session created
// in Chat moved to the TUI first, so it never strands one waiting on a Chat
// controller it will not run. A worker that reports it gets Chat.
func TestWorkerLaunchMovesChatOffAWorkerThatCannotStartIt(t *testing.T) {
	chatLaunch := domain.WorkerLaunch{SessionID: testOrchestratorID, Kind: "worker", Harness: "codex", Interface: domain.SessionInterfaceChat}

	older := &chatStartStore{reconnectStore: reconnectStore{launch: chatLaunch}, moved: true}
	if launch := reconnectLaunch(t, testServer(older), worker.CapabilityMultiRepo); launch.Interface != "tui" {
		t.Fatalf("older worker launch interface = %q, want tui", launch.Interface)
	}
	if older.calls != 1 || older.code != worker.ChatStartUnsupported || older.detail == "" {
		t.Fatalf("fallback calls=%d code=%q detail=%q", older.calls, older.code, older.detail)
	}

	// A session the person switched to Chat does not move, and keeps Chat.
	switched := &chatStartStore{reconnectStore: reconnectStore{launch: chatLaunch}}
	if launch := reconnectLaunch(t, testServer(switched), ""); launch.Interface != "chat" {
		t.Fatalf("switched session launch interface = %q, want chat", launch.Interface)
	}

	current := &chatStartStore{reconnectStore: reconnectStore{launch: chatLaunch}, moved: true}
	if launch := reconnectLaunch(t, testServer(current), worker.CapabilityMultiRepo+","+worker.CapabilityChatStart); launch.Interface != "chat" {
		t.Fatalf("current worker launch interface = %q, want chat", launch.Interface)
	}
	if current.calls != 0 {
		t.Fatal("a worker that can start in Chat was moved off it")
	}

	tui := &chatStartStore{reconnectStore: reconnectStore{launch: domain.WorkerLaunch{SessionID: testOrchestratorID, Harness: "codex", Interface: domain.SessionInterfaceTUI}}}
	if reconnectLaunch(t, testServer(tui), ""); tui.calls != 0 {
		t.Fatal("a TUI session asked to move")
	}
}

func TestWorkerChatStartFallback(t *testing.T) {
	store := &chatStartStore{moved: true}
	w := httptest.NewRecorder()
	testServer(store).workerChatStartFallback(w, workerRequest(t, http.MethodPost, "/worker/interface/chat-start-fallback",
		`{"code":"`+worker.ChatStartAdapterUnavailable+`"}`, "worker:connect"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body)
	}
	var resp worker.ChatStartFallbackResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.MovedToTUI {
		t.Fatalf("response = %+v, %v; want moved", resp, err)
	}
	if store.workerID != "w1" || store.code != worker.ChatStartAdapterUnavailable ||
		store.detail != chatStartFallbackDetails[worker.ChatStartAdapterUnavailable] {
		t.Fatalf("store got worker=%q code=%q detail=%q", store.workerID, store.code, store.detail)
	}

	for name, req := range map[string]*http.Request{
		"unknown code": workerRequest(t, http.MethodPost, "/worker/interface/chat-start-fallback", `{"code":"WHATEVER"}`, "worker:connect"),
		"no scope":     workerRequest(t, http.MethodPost, "/worker/interface/chat-start-fallback", `{"code":"`+worker.ChatStartAdapterUnavailable+`"}`),
	} {
		store := &chatStartStore{}
		w := httptest.NewRecorder()
		testServer(store).workerChatStartFallback(w, req)
		if w.Code < 400 || store.calls != 0 {
			t.Fatalf("%s: status = %d calls = %d, want refused", name, w.Code, store.calls)
		}
	}
}

// The public contract names every code the endpoint accepts.
func TestChatStartFallbackCodesMatchTheContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "cloud", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	contract := string(raw)
	start := strings.Index(contract, "\n    WorkerChatStartFallbackInput:\n")
	if start < 0 {
		t.Fatal("WorkerChatStartFallbackInput is not in the contract")
	}
	schema := contract[start:]
	enumAt := strings.Index(schema, "enum: [")
	if enumAt < 0 {
		t.Fatal("WorkerChatStartFallbackInput.code has no enum")
	}
	line := schema[enumAt+len("enum: [") : enumAt+strings.Index(schema[enumAt:], "]")]
	var documented []string
	for _, code := range strings.Split(line, ",") {
		documented = append(documented, strings.TrimSpace(code))
	}
	var accepted []string
	for code := range chatStartFallbackDetails {
		accepted = append(accepted, code)
	}
	slices.Sort(documented)
	slices.Sort(accepted)
	if !slices.Equal(documented, accepted) {
		t.Fatalf("contract enum = %v, accepted codes = %v", documented, accepted)
	}
}
