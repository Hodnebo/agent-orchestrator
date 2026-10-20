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

// createSessionWithInterface posts a session create for harness, adding a
// defaultInterfaceMode field when interfaceMode is not empty.
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
		text = strings.TrimSuffix(text, "}") + `,"defaultInterfaceMode":"` + interfaceMode + `"}`
	}
	req.Body = io.NopCloser(strings.NewReader(text))
	recorder := httptest.NewRecorder()
	srv.createSession(recorder, req)
	return recorder
}

// The desktop sends its default session interface on create; the control
// plane stores it on the create, where the session's interface is resolved
// from it (postgres, domain.StartInterface). A request without the field (an
// older desktop) carries no default.
func TestCreateSessionDefaultInterfaceMode(t *testing.T) {
	for _, tc := range []struct {
		name, harness, interfaceMode string
		want                         domain.SessionInterface
	}{
		{"omitted", "claude-code", "", ""},
		{"tui", "claude-code", "tui", domain.SessionInterfaceTUI},
		{"chat", "codex", "chat", domain.SessionInterfaceChat},
		{"chat for a harness that cannot start in it", "cursor", "chat", domain.SessionInterfaceChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubAutolinkStore{}
			recorder := createSessionWithInterface(t, store, tc.harness, tc.interfaceMode)
			if recorder.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", recorder.Code, recorder.Body)
			}
			if store.captured.DefaultInterface != tc.want || store.captured.Interface != "" {
				t.Fatalf("default = %q interface = %q, want default %q and no explicit request",
					store.captured.DefaultInterface, store.captured.Interface, tc.want)
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
