package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

// createSessionWithInterface posts a session create for harness, adding an
// interfaceMode field when interfaceMode is not empty.
func createSessionWithInterface(t *testing.T, store *stubAutolinkStore, harness, interfaceMode string) *httptest.ResponseRecorder {
	t.Helper()
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	req := createSessionRequestHTTP(t, "worker", "")
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(body), `"harness":"claude-code"`, `"harness":"`+harness+`"`, 1)
	if interfaceMode != "" {
		text = strings.TrimSuffix(text, "}") + `,"interfaceMode":"` + interfaceMode + `"}`
	}
	req.Body = io.NopCloser(strings.NewReader(text))
	recorder := httptest.NewRecorder()
	srv.createSession(recorder, req)
	return recorder
}

// The desktop sends its default session interface on create. Chat is honored
// for a harness that may start in Chat and silently becomes the TUI for any
// other; a request without the field (an older desktop) starts in the TUI.
func TestCreateSessionInterfaceMode(t *testing.T) {
	cases := []struct {
		name          string
		harness       string
		interfaceMode string
		want          domain.SessionInterface
	}{
		{"omitted", "claude-code", "", domain.SessionInterfaceTUI},
		{"tui", "claude-code", "tui", domain.SessionInterfaceTUI},
		{"claude chat", "claude-code", "chat", domain.SessionInterfaceChat},
		{"codex chat", "codex", "chat", domain.SessionInterfaceChat},
		{"cursor chat falls back", "cursor", "chat", domain.SessionInterfaceTUI},
		{"opencode chat falls back", "opencode", "chat", domain.SessionInterfaceTUI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubAutolinkStore{}
			recorder := createSessionWithInterface(t, store, tc.harness, tc.interfaceMode)
			if recorder.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", recorder.Code, recorder.Body)
			}
			if store.captured.Interface != tc.want {
				t.Fatalf("interface = %q, want %q", store.captured.Interface, tc.want)
			}
		})
	}
}

func TestCreateSessionRejectsUnknownInterfaceMode(t *testing.T) {
	store := &stubAutolinkStore{}
	recorder := createSessionWithInterface(t, store, "claude-code", "gui")
	if recorder.Code != http.StatusUnprocessableEntity || store.created {
		t.Fatalf("status = %d created = %v, want 422 and no session", recorder.Code, store.created)
	}
}
