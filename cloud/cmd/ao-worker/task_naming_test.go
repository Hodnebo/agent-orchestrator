package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/tasknaming"
	"github.com/aoagents/agent-orchestrator/backend/pkg/taskworkspace"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workertransport"
)

func namingRepository(t *testing.T) (string, func(string, ...string) string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	var err error
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git(repo, "init", "-b", "main")
	git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "initial")
	return repo, git
}

func TestTaskWorkspaceFinalizesActualPathBeforeCompletion(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "configured-or-reused"}[fixed], func(t *testing.T) {
			repo, git := namingRepository(t)
			data := t.TempDir()
			launch := worker.BootstrapResponse{SessionID: "session-1", Launch: worker.LaunchContext{SessionID: "session-1", ProjectID: "project", Kind: "worker", Branch: "ao/session-1/root", RepositoryURL: "https://github.com/acme/app", DefaultBranch: "main", TaskNamingState: domain.TaskNamingPending}}
			if fixed {
				git(repo, "checkout", "-b", launch.Launch.Branch)
			}
			completed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/worker/task-name" {
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				var request domain.TaskNameRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				result := domain.TaskNameResult{State: domain.TaskNamingReserved, Title: "Manually edited title", Branch: "ao/login-redirect/root", Slug: "login-redirect"}
				if request.Complete {
					record, err := readTaskWorkspaceCheckpoint(data)
					if err != nil || record.Info.Branch != request.Branch {
						t.Errorf("completion before durable filesystem checkpoint: %+v / %v", record, err)
					}
					if got := git(record.Info.Path, "branch", "--show-current"); got != request.Branch {
						t.Errorf("actual branch = %s", got)
					}
					if !fixed && filepath.Base(record.Info.Path) != "login-redirect" {
						t.Errorf("actual path = %s", record.Info.Path)
					}
					result.State = domain.TaskNamingDone
					completed = true
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			c := &client{baseURL: server.URL, http: server.Client()}
			path, err := finalizeTaskWorkspace(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), c, &launch, repo, data, fixed, tasknaming.Name{Title: "Generated title", Slug: "login-redirect"})
			if err != nil {
				t.Fatal(err)
			}
			if !completed || launch.Launch.Branch != "ao/login-redirect/root" || launch.Launch.DisplayName != "Manually edited title" {
				t.Fatalf("launch = %+v, completed=%v", launch, completed)
			}
			if fixed && path != repo {
				t.Fatalf("configured path changed: %s", path)
			}
			if !fixed && path != filepath.Join(repo, ".ao-task-worktrees", "project", "login-redirect") {
				t.Fatalf("worktree = %s", path)
			}
			if err := os.WriteFile(filepath.Join(path, "dirty"), []byte("preserved"), 0o600); err != nil {
				t.Fatal(err)
			}
			resumed, err := finalizeTaskWorkspace(context.Background(), slog.Default(), c, &launch, repo, data, fixed, tasknaming.Name{})
			if err != nil || resumed != path {
				t.Fatalf("resume = %s, %v", resumed, err)
			}
			if body, err := os.ReadFile(filepath.Join(path, "dirty")); err != nil || string(body) != "preserved" {
				t.Fatalf("dirty resume = %q, %v", body, err)
			}
		})
	}
}

func TestTaskNameWaiterIgnoresLateProviderCleanup(t *testing.T) {
	release := make(chan struct{})
	job := startTaskNameCall(context.Background(), time.Millisecond, tasknaming.Fallback("fix login redirect", "session"), func(context.Context) (string, error) { <-release; return `{"slug":"too-late"}`, nil })
	started := time.Now()
	if name := job.wait(); name.Slug != "fix-login-redirect" {
		t.Fatalf("name = %+v", name)
	}
	if time.Since(started) > time.Second {
		t.Fatal("waited for provider cleanup")
	}
	close(release)
	if name := <-job.done; name.Slug != "fix-login-redirect" {
		t.Fatalf("accepted late result: %+v", name)
	}
}

func TestTaskNameCapacityFallsBackWithoutStartingProvider(t *testing.T) {
	for range cap(taskNamingSlots) {
		taskNamingSlots <- struct{}{}
	}
	defer func() {
		for range cap(taskNamingSlots) {
			<-taskNamingSlots
		}
	}()
	job := startTaskNameCall(context.Background(), time.Second, tasknaming.Name{Slug: "fallback"}, func(context.Context) (string, error) {
		t.Error("provider started above naming capacity")
		return "", nil
	})
	if got := job.wait(); got.Slug != "fallback" {
		t.Fatalf("name = %+v", got)
	}
}

func TestWorkerTaskNameRequiresControlPlaneCapacityBeforeCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/worker/task-name" {
			t.Errorf("credential/provider requested after admission denied: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var request domain.TaskNameRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Claim {
			t.Errorf("claim = %+v, %v", request, err)
		}
		_ = json.NewEncoder(w).Encode(domain.TaskNameResult{Generate: false})
	}))
	defer server.Close()
	job := startWorkerTaskName(context.Background(), &client{baseURL: server.URL, http: server.Client()}, worker.LaunchContext{SessionID: "session", Harness: "codex", Prompt: "fix login redirect"}, t.TempDir(), nil)
	if got := job.wait(); got.Slug != "fix-login-redirect" {
		t.Fatalf("name = %+v", got)
	}
}

func TestTaskNameCallUsesSharedParserAndFallback(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		err  error
		slug string
	}{
		{`{"title":"Login redirect","slug":"Login Redirect"}`, nil, "login-redirect"},
		{"", errors.New("provider unavailable"), "fix-login-redirect"},
		{"not json", nil, "fix-login-redirect"},
	} {
		job := startTaskNameCall(context.Background(), time.Second, tasknaming.Fallback("fix login redirect", "session"), func(context.Context) (string, error) { return tc.raw, tc.err })
		if name := job.wait(); name.Slug != tc.slug {
			t.Fatalf("name = %+v, want %s", name, tc.slug)
		}
	}
}

func TestTaskWorkspaceEligibilityLeavesLegacyAndNonworkersAlone(t *testing.T) {
	base := worker.LaunchContext{Kind: "worker", RepositoryURL: "https://github.com/acme/app", TaskNamingState: domain.TaskNamingPending}
	if !usesTaskWorkspace(base) {
		t.Fatal("new worker naming disabled")
	}
	for _, mutate := range []func(*worker.LaunchContext){
		func(l *worker.LaunchContext) { l.TaskNamingState = "" },
		func(l *worker.LaunchContext) { l.TaskNamingState = domain.TaskNamingDone },
		func(l *worker.LaunchContext) { l.Kind = "orchestrator" },
		func(l *worker.LaunchContext) { l.RepositoryURL = "https://scratch.ao.local/scratch/repository" },
	} {
		l := base
		mutate(&l)
		if usesTaskWorkspace(l) {
			t.Fatalf("unexpected naming for %+v", l)
		}
	}
}

func TestTaskWorkspaceKeepsMoveJournalWhenControlPlaneFailsAndResumes(t *testing.T) {
	repo, _ := namingRepository(t)
	data := t.TempDir()
	bootstrap := worker.BootstrapResponse{SessionID: "session-1", Launch: worker.LaunchContext{SessionID: "session-1", ProjectID: "project", Kind: "worker", Branch: "ao/session-1/root", DefaultBranch: "main", TaskNamingState: domain.TaskNamingPending}}
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request domain.TaskNameRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		result := domain.TaskNameResult{State: domain.TaskNamingReserved, Title: "Title", Slug: "login-redirect", Branch: "ao/login-redirect/root"}
		if request.Complete {
			if fail {
				http.Error(w, "database unavailable", http.StatusServiceUnavailable)
				return
			}
			result.State = domain.TaskNamingDone
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	if _, err := finalizeTaskWorkspace(context.Background(), slog.Default(), c, &bootstrap, repo, data, false, tasknaming.Name{Slug: "login-redirect"}); err == nil {
		t.Fatal("control-plane failure was hidden")
	}
	adapter, err := taskWorkspaceAdapter(repo, bootstrap.Launch, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	recovered, found, err := adapter.Recover(context.Background())
	if err != nil || !found {
		t.Fatalf("move journal lost: %+v, %v, %v", recovered, found, err)
	}
	if err := os.WriteFile(filepath.Join(recovered.Path, "dirty"), []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Model a worker with a stale private path: shared journal identity wins.
	if err := writeTaskWorkspaceCheckpoint(data, taskWorkspaceCheckpoint{Info: taskworkspace.Info{Path: filepath.Join(repo, ".ao-task-worktrees", "project", "session-1"), Branch: "ao/task-naming-session-1/root"}}); err != nil {
		t.Fatal(err)
	}
	fail = false
	bootstrap.Launch.TaskNamingState, bootstrap.Launch.TaskNameSlug, bootstrap.Launch.Branch = domain.TaskNamingReserved, "login-redirect", "ao/login-redirect/root"
	path, err := finalizeTaskWorkspace(context.Background(), slog.Default(), c, &bootstrap, repo, data, false, tasknaming.Name{})
	if err != nil || path != recovered.Path {
		t.Fatalf("resume = %s, %v", path, err)
	}
	if body, err := os.ReadFile(filepath.Join(path, "dirty")); err != nil || string(body) != "preserved" {
		t.Fatalf("dirty = %q, %v", body, err)
	}
	if _, found, err := adapter.Recover(context.Background()); err != nil || found {
		t.Fatalf("successful completion did not acknowledge journal: %v, %v", found, err)
	}
}

func TestTaskWorkspaceRetriesCloudCollisionAfterFilesystemSuffix(t *testing.T) {
	repo, git := namingRepository(t)
	data := t.TempDir()
	occupiedPath := filepath.Join(repo, ".ao-task-worktrees", "project", "login-redirect")
	if err := os.MkdirAll(occupiedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupiedPath, "keep"), []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "update-ref", "refs/remotes/origin/ao/login-redirect-2/root", "HEAD")
	bootstrap := worker.BootstrapResponse{SessionID: "session-1", Launch: worker.LaunchContext{SessionID: "session-1", ProjectID: "project", Kind: "worker", Branch: "ao/session-1/root", DefaultBranch: "main", TaskNamingState: domain.TaskNamingPending}}
	completions, retries := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request domain.TaskNameRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		result := domain.TaskNameResult{State: domain.TaskNamingReserved, Title: "Title", Slug: "login-redirect", Branch: "ao/login-redirect/root"}
		if request.Retry {
			retries++
			if request.Slug != "" {
				t.Error("collision retry changed the reserved base slug")
			}
			result.Slug, result.Branch = "login-redirect-4", "ao/login-redirect-4/root"
		} else if !request.Complete {
			refs, folder := false, false
			for _, ref := range request.Occupied {
				refs = refs || ref == "login-redirect-2"
				folder = folder || ref == "login-redirect"
			}
			if !refs || !folder {
				t.Errorf("missing compacted occupied candidates: %v", request.Occupied)
			}
		}
		if request.Complete {
			completions++
			if completions == 1 {
				if request.Branch != "ao/login-redirect-3/root" {
					t.Errorf("actual first branch = %s", request.Branch)
				}
				http.Error(w, "another session claimed this name", http.StatusConflict)
				return
			}
			result.State, result.Branch, result.Slug = domain.TaskNamingDone, request.Branch, "login-redirect-4"
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	path, err := finalizeTaskWorkspace(context.Background(), slog.Default(), &client{baseURL: server.URL, http: server.Client()}, &bootstrap, repo, data, false, tasknaming.Name{Slug: "login-redirect"})
	if err != nil {
		t.Fatal(err)
	}
	if retries != 1 || completions != 2 || filepath.Base(path) != "login-redirect-4" || bootstrap.Launch.Branch != "ao/login-redirect-4/root" {
		t.Fatalf("result path=%s, launch=%+v, retries=%d, completions=%d", path, bootstrap.Launch, retries, completions)
	}
	if body, err := os.ReadFile(filepath.Join(occupiedPath, "keep")); err != nil || string(body) != "preserved" {
		t.Fatalf("occupied folder changed: %q, %v", body, err)
	}
	if got := git(repo, "branch", "--show-current"); got != "main" {
		t.Fatalf("canonical branch changed: %s", got)
	}
}

func TestFixedTaskWorkspaceAvoidsExistingBranch(t *testing.T) {
	repo, git := namingRepository(t)
	data := t.TempDir()
	git(repo, "branch", "ao/login/root")
	original := git(repo, "rev-parse", "ao/login/root")
	git(repo, "checkout", "-b", "ao/task-naming-session-1/root")
	git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "session work")
	current := git(repo, "rev-parse", "HEAD")
	bootstrap := worker.BootstrapResponse{SessionID: "session-1", Launch: worker.LaunchContext{SessionID: "session-1", ProjectID: "project", Kind: "worker", Branch: "ao/session-1/root", DefaultBranch: "main", TaskNamingState: domain.TaskNamingPending}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request domain.TaskNameRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		result := domain.TaskNameResult{State: domain.TaskNamingReserved, Title: "Title", Slug: "login", Branch: "ao/login/root"}
		if request.Retry || request.Complete {
			result.Slug, result.Branch = "login-2", "ao/login-2/root"
		}
		if request.Complete {
			result.State = domain.TaskNamingDone
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	path, err := finalizeTaskWorkspace(context.Background(), slog.Default(), &client{baseURL: server.URL, http: server.Client()}, &bootstrap, repo, data, true, tasknaming.Name{Slug: "login"})
	if err != nil || path != repo {
		t.Fatalf("fixed path = %s, %v", path, err)
	}
	if got := git(repo, "rev-parse", "ao/login/root"); got != original {
		t.Fatal("overwrote another branch")
	}
	if got := git(repo, "rev-parse", "HEAD"); got != current {
		t.Fatal("checked out another branch's commits")
	}
}

func TestTaskWorkspaceUsesAuthoritativeDefaultWhenUnspecified(t *testing.T) {
	repo, git := namingRepository(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(repo, "clone", "--bare", repo, origin)
	git(repo, "remote", "add", "origin", origin)
	base := git(repo, "rev-parse", "HEAD")
	git(repo, "update-ref", "refs/remotes/origin/main", base)
	git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git(repo, "checkout", "-b", "feature")
	git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "feature")
	bootstrap := worker.BootstrapResponse{SessionID: "session-1", Launch: worker.LaunchContext{SessionID: "session-1", ProjectID: "project", Kind: "worker", TaskNamingState: domain.TaskNamingPending}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request domain.TaskNameRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		state := domain.TaskNamingReserved
		if request.Complete {
			state = domain.TaskNamingDone
		}
		_ = json.NewEncoder(w).Encode(domain.TaskNameResult{State: state, Title: "Title", Slug: "login", Branch: "ao/login/root"})
	}))
	defer server.Close()
	path, err := finalizeTaskWorkspace(context.Background(), slog.Default(), &client{baseURL: server.URL, http: server.Client()}, &bootstrap, repo, t.TempDir(), false, tasknaming.Name{Slug: "login"})
	if err != nil {
		t.Fatal(err)
	}
	if got := git(path, "rev-parse", "HEAD"); got != base {
		t.Fatalf("task started from checked-out feature branch: %s, want %s", got, base)
	}
}

func TestTaskWorkspaceRecoversCrashAfterReservationRetryBeforeJournalAcknowledgement(t *testing.T) {
	repo, _ := namingRepository(t)
	data := t.TempDir()
	bootstrap := worker.BootstrapResponse{SessionID: "session-1", Launch: worker.LaunchContext{SessionID: "session-1", ProjectID: "project", Kind: "worker", DefaultBranch: "main", TaskNamingState: domain.TaskNamingReserved, TaskNameSlug: "login-2", Branch: "ao/login-2/root"}}
	adapter, err := taskWorkspaceAdapter(repo, bootstrap.Launch, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	info, err := adapter.Create(context.Background(), "ao/task-naming-session-1/root", "main")
	if err != nil {
		t.Fatal(err)
	}
	info, err = adapter.Name(context.Background(), info, "ao/login/root", "login")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTaskWorkspaceCheckpoint(data, taskWorkspaceCheckpoint{Info: info}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "dirty"), []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request domain.TaskNameRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		state := domain.TaskNamingReserved
		if request.Complete {
			state = domain.TaskNamingDone
		}
		_ = json.NewEncoder(w).Encode(domain.TaskNameResult{State: state, Title: "Title", Slug: "login-2", Branch: "ao/login-2/root"})
	}))
	defer server.Close()
	path, err := finalizeTaskWorkspace(context.Background(), slog.Default(), &client{baseURL: server.URL, http: server.Client()}, &bootstrap, repo, data, false, tasknaming.Name{})
	if err != nil || filepath.Base(path) != "login-2" {
		t.Fatalf("retry recovery = %s, %v", path, err)
	}
	if body, err := os.ReadFile(filepath.Join(path, "dirty")); err != nil || string(body) != "preserved" {
		t.Fatalf("dirty = %q, %v", body, err)
	}
}

func TestTaskNamingFailureKeepsChatDiagnosticTransportAliveUntilReopen(t *testing.T) {
	var claimed, requestReopen atomic.Bool
	failed := make(chan string, 1)
	reported := make(chan worker.WorkspaceStartupEvent, 1)
	wake := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/worker/transport/claim":
			var request *worker.TransportRequest
			if requestReopen.CompareAndSwap(true, false) {
				request = &worker.TransportRequest{ID: "reopen", Attempt: 1, Kind: "workspace.checkout"}
			}
			_ = json.NewEncoder(w).Encode(worker.ClaimTransportResponse{Request: request})
		case "/worker/work/wait":
			select {
			case <-wake:
			case <-r.Context().Done():
			}
			_, _ = io.WriteString(w, `{}`)
		case "/worker/turns/claim":
			var turn *worker.Turn
			if claimed.CompareAndSwap(false, true) {
				turn = &worker.Turn{ID: "turn-1", Attempt: 1, Prompt: "task", Harness: "codex"}
			}
			_ = json.NewEncoder(w).Encode(worker.ClaimTurnResponse{Turn: turn})
		case "/worker/turns/turn-1/fail":
			var request worker.FailTurnRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			failed <- request.Error
			_, _ = io.WriteString(w, `{}`)
		case "/worker/events":
			var request struct {
				Type    string                       `json:"type"`
				Payload worker.WorkspaceStartupEvent `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Type == worker.WorkspaceStartupEventType {
				reported <- request.Payload
			}
			_, _ = io.WriteString(w, `{}`)
		case "/worker/transport/reopen/complete":
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected provider/agent request during naming failure: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	workspace := t.TempDir()
	cause := errors.New("task name database unavailable")
	ready := make(chan struct{})
	started := make(chan error, 1)
	reopened := make(chan struct{}, 1)
	supervisor := &workertransport.Supervisor{Control: c, Workspace: workspace, InitialInterface: workertransport.InterfaceChat, ChatRunner: workspaceFailureChatRunner(c, workspace, cause, slog.Default()), ChatWorkspaceReady: ready, Started: started, RequestCheckout: func() error { reopened <- struct{}{}; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic transport did not start")
	}
	reportWorkspaceStartup(ctx, slog.Default(), c, checkoutStartupFailure(cause))
	close(ready)
	select {
	case message := <-failed:
		if !strings.Contains(message, "task name database unavailable") {
			t.Fatalf("turn failure = %s", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued Chat turn did not receive startup failure")
	}
	if report := <-reported; report.Status != worker.WorkspaceStartupFailed {
		t.Fatalf("startup report = %+v", report)
	}
	select {
	case err := <-done:
		t.Fatalf("diagnostic transport stopped: %v", err)
	default:
	}
	requestReopen.Store(true)
	close(wake)
	select {
	case <-reopened:
	case <-time.After(2 * time.Second):
		t.Fatal("reopen could not request worker restart")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic transport did not stop")
	}
}

func TestTaskNameOccupiedMetadataIsBounded(t *testing.T) {
	occupied := make([]string, 0, 10000)
	for i := range 10000 {
		occupied = append(occupied, fmt.Sprintf("refs/heads/unrelated-%d", i))
	}
	occupied = append(occupied, "refs/remotes/upstream/custom/prefix/login/root", "login-2", "refs/heads/custom/prefix/login-3/child", "refs/heads/custom/prefix/login-4", "custom")
	got := taskNameOccupiedCandidates("custom/prefix/1234abcd", "", "login", occupied)
	if strings.Join(got, ",") != "login,login-2,login-3,login-4" {
		t.Fatalf("occupied candidates = %v", got)
	}
	if got := taskNameOccupiedCandidates("custom/prefix/login-5/root", "login-5", "ignored", append(occupied, "refs/heads/custom/prefix")); len(got) != 100 {
		t.Fatalf("ancestor blocks %d candidates, want 100", len(got))
	}
	payload, err := json.Marshal(domain.TaskNameRequest{Slug: "login", Occupied: got})
	if err != nil || len(payload) > 8192 {
		t.Fatalf("bounded payload = %d bytes %v", len(payload), err)
	}
	if !taskBranchOccupied("custom/prefix/login/root", occupied, "") {
		t.Fatal("compacting metadata mutated the local inventory")
	}
}
