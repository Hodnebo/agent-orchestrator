package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

func TestClientUsesReachableDaemonWhenRunFilePIDIsInvisible(t *testing.T) {
	const daemonPID = 4242
	calls := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.Method + " " + r.URL.Path
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			_, _ = fmt.Fprintf(w, `{"status":"ok","service":%q,"pid":%d}`, daemonmeta.ServiceName, daemonPID)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/ao-1/activity" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: daemonPID, Port: serverPort(t, srv.URL), StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	c := &commandContext{deps: Deps{HTTPClient: &http.Client{}, ProcessAlive: func(int) bool { return false }}}

	if err := c.postJSON(context.Background(), "sessions/ao-1/activity", struct{}{}, nil); err != nil {
		t.Fatalf("postJSON with an invisible but reachable daemon: %v", err)
	}
	for _, want := range []string{"GET /healthz", "POST /api/v1/sessions/ao-1/activity"} {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("request = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("request %q was not made", want)
		}
	}
}

func TestClientRejectsInvisiblePIDWhenProbeCannotVerifyDaemon(t *testing.T) {
	tests := []struct {
		name   string
		server *httptest.Server
	}{
		{
			name: "wrong service",
			server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"status":"ok","service":"other-service","pid":4242}`)
			})),
		},
		{
			name:   "unreachable port",
			server: httptest.NewServer(http.NotFoundHandler()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			port := serverPort(t, tt.server.URL)
			if tt.name == "unreachable port" {
				tt.server.Close()
			}
			defer tt.server.Close()
			if err := runfile.Write(cfg.runFile, runfile.Info{PID: 4242, Port: port, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
				t.Fatal(err)
			}
			c := &commandContext{deps: Deps{HTTPClient: &http.Client{}, ProcessAlive: func(int) bool { return false }}}
			err := c.postJSON(context.Background(), "sessions/ao-1/activity", struct{}{}, nil)
			if !errors.Is(err, errDaemonNotRunning) {
				t.Fatalf("postJSON error = %v, want errDaemonNotRunning", err)
			}
		})
	}
}
