package coder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

const storedNameSessionID = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"

// workspaceNameServer is a Coder fake holding at most one workspace, served by
// name lookup, by id and on create, recording every name AO asked for.
type workspaceNameServer struct {
	mu        sync.Mutex
	existing  *workspace
	requested []string
}

func (f *workspaceNameServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		const lookup = "/api/v2/users/planned-owner/workspace/"
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v2/users/planned-owner/workspaces":
			var body createWorkspaceRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create: %v", err)
			}
			f.requested = append(f.requested, "create:"+body.Name)
			if f.existing != nil && f.existing.Name == body.Name {
				writer.WriteHeader(http.StatusConflict)
				_, _ = writer.Write([]byte(`{"message":"Workspace already exists."}`))
				return
			}
			view := healthyWorkspace()
			view.Name, view.OwnerName, view.TemplateID, view.CreatedAt = body.Name, "planned-owner", body.TemplateID, time.Now()
			f.existing = &view
			_ = json.NewEncoder(writer).Encode(view)
		case request.Method == http.MethodGet && len(request.URL.Path) > len(lookup) && request.URL.Path[:len(lookup)] == lookup:
			name := request.URL.Path[len(lookup):]
			f.requested = append(f.requested, "find:"+name)
			if f.existing == nil || f.existing.Name != name {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(writer).Encode(f.existing)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/workspaces/"+testWorkspaceID:
			if f.existing == nil {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(writer).Encode(f.existing)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func storedNameProvider(t *testing.T, baseURL, coderProfile string) sandbox.Provider {
	t.Helper()
	base := newTestClient(t, baseURL, nil)
	provider, err := base.ForSandbox(domain.Sandbox{
		SessionID: storedNameSessionID,
		ResourceProfile: json.RawMessage(fmt.Sprintf(
			`{"coder":{"baseUrl":%q,"owner":"planned-owner","templateId":%q,"durableRoot":"$HOME"%s}}`,
			baseURL, testTemplateID, coderProfile)),
	})
	if err != nil {
		t.Fatalf("scope client: %v", err)
	}
	return provider
}

// A session created with a stored name creates, finds and re-reads its
// workspace under that name, never the computed one.
func TestScopedClientUsesStoredWorkspaceName(t *testing.T) {
	t.Parallel()
	fake := &workspaceNameServer{}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()
	issuedAt := time.Now().UTC().Format(time.RFC3339Nano)
	provider := storedNameProvider(t, server.URL,
		`,"workspaceNamePrefix":"acme","workspaceName":"acme-w-fix-1a2b3c4d","workspaceNameIssuedAt":"`+issuedAt+`"`)

	ctx := context.Background()
	if _, found, err := provider.FindBySession(ctx, storedNameSessionID); err != nil || found {
		t.Fatalf("find before create = %v, %v; want absent", found, err)
	}
	environment, err := provider.Create(ctx, sandbox.Spec{SessionID: storedNameSessionID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, found, err := provider.FindBySession(ctx, storedNameSessionID); err != nil || !found {
		t.Fatalf("find after create = %v, %v; want the created workspace", found, err)
	}
	if _, err := provider.Get(ctx, environment.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := []string{"find:acme-w-fix-1a2b3c4d", "create:acme-w-fix-1a2b3c4d", "find:acme-w-fix-1a2b3c4d"}
	if fmt.Sprint(fake.requested) != fmt.Sprint(want) {
		t.Fatalf("requests = %v, want %v", fake.requested, want)
	}
	// The client stays bound to its own session: another id is a mismatch.
	if _, err := provider.Create(ctx, sandbox.Spec{SessionID: "9f8e7d6c-5b4a-4321-8fed-cba987654321"}); err == nil {
		t.Fatal("create for another session id succeeded")
	}
}

// A session created before names were stored keeps the computed name, so its
// existing workspace is still found and validated.
func TestScopedClientFallsBackToComputedWorkspaceName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, profile, want string }{
		{"prefixed", `,"workspaceNamePrefix":"elevenx"`, WorkspaceNameWithPrefix("elevenx", storedNameSessionID)},
		{"default", ``, WorkspaceName(storedNameSessionID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			legacy := healthyWorkspace()
			legacy.Name, legacy.OwnerName = tc.want, "planned-owner"
			legacy.CreatedAt = time.Now().Add(-90 * 24 * time.Hour)
			fake := &workspaceNameServer{existing: &legacy}
			server := httptest.NewServer(fake.handler(t))
			defer server.Close()
			provider := storedNameProvider(t, server.URL, tc.profile)

			ctx := context.Background()
			environment, found, err := provider.FindBySession(ctx, storedNameSessionID)
			if err != nil || !found {
				t.Fatalf("find legacy workspace = %v, %v; want found", found, err)
			}
			if _, err := provider.Get(ctx, environment.ID); err != nil {
				t.Fatalf("get legacy workspace: %v", err)
			}
			if fmt.Sprint(fake.requested) != fmt.Sprint([]string{"find:" + tc.want}) {
				t.Fatalf("requests = %v, want a lookup of %s", fake.requested, tc.want)
			}
		})
	}
}

// A workspace that held the stored name well before the session chose it is
// another session's (an id collision): it is reported absent so AO neither
// adopts nor deletes it, and the create then fails on Coder's conflict.
func TestStoredWorkspaceNameNeverAdoptsAnOlderWorkspace(t *testing.T) {
	t.Parallel()
	issuedAt := time.Now().UTC()
	for _, tc := range []struct {
		name      string
		createdAt time.Time
		adopt     bool
	}{
		{"created long before the name was chosen", issuedAt.Add(-time.Hour), false},
		{"created within clock skew", issuedAt.Add(-time.Minute), true},
		{"created after", issuedAt.Add(time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			other := healthyWorkspace()
			other.Name, other.OwnerName, other.CreatedAt = "acme-w-fix-1a2b3c4d", "planned-owner", tc.createdAt
			fake := &workspaceNameServer{existing: &other}
			server := httptest.NewServer(fake.handler(t))
			defer server.Close()
			provider := storedNameProvider(t, server.URL,
				`,"workspaceNamePrefix":"acme","workspaceName":"acme-w-fix-1a2b3c4d","workspaceNameIssuedAt":"`+issuedAt.Format(time.RFC3339Nano)+`"`)

			ctx := context.Background()
			_, found, err := provider.FindBySession(ctx, storedNameSessionID)
			if err != nil || found != tc.adopt {
				t.Fatalf("find = %v, %v; want found=%v", found, err, tc.adopt)
			}
			if !tc.adopt {
				if _, err := provider.Create(ctx, sandbox.Spec{SessionID: storedNameSessionID}); err == nil {
					t.Fatal("create over another session's workspace succeeded")
				}
			}
		})
	}
}

func TestSessionWorkspaceNameAndURL(t *testing.T) {
	t.Parallel()
	if got := SessionWorkspaceName(" acme-o-1a2b3c4d ", "acme", storedNameSessionID); got != "acme-o-1a2b3c4d" {
		t.Fatalf("stored name = %q", got)
	}
	if got := SessionWorkspaceName("", "acme", storedNameSessionID); got != WorkspaceNameWithPrefix("acme", storedNameSessionID) {
		t.Fatalf("legacy prefixed name = %q", got)
	}
	if got := SessionWorkspaceName("", "", storedNameSessionID); got != WorkspaceName(storedNameSessionID) {
		t.Fatalf("legacy default name = %q", got)
	}
	if got := WorkspaceURL("https://coder.example.com/", "dev", "acme-o-1a2b3c4d"); got != "https://coder.example.com/@dev/acme-o-1a2b3c4d" {
		t.Fatalf("workspace URL = %q", got)
	}
	for _, args := range [][3]string{
		{"https://coder.example.com", "", "acme-o-1a2b3c4d"},
		{"https://coder.example.com", "dev", ""},
		{"ftp://coder.example.com", "dev", "acme-o-1a2b3c4d"},
	} {
		if got := WorkspaceURL(args[0], args[1], args[2]); got != "" {
			t.Errorf("WorkspaceURL%v = %q, want empty", args, got)
		}
	}
}
