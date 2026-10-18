package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

// cleanupLookupStore answers ProjectCleanupCommands with fixed commands or an
// error.
type cleanupLookupStore struct {
	lifecycleStore
	commands []string
	err      error
}

func (s *cleanupLookupStore) ProjectCleanupCommands(context.Context, string, string) ([]string, error) {
	return s.commands, s.err
}

// Delete receives the project's current Cleanup commands, including an empty
// list for a project that no longer has Cleanup; when they can't be read, or
// the store can't read them, Delete gets no list and the provider falls back
// to the commands the worker installed.
func TestDeletionCarriesTheProjectsCurrentCleanup(t *testing.T) {
	record := domain.Sandbox{OrgID: "org-1", SessionID: "session-1"}
	reconciler := func(store Store) *Reconciler {
		return New(store, lifecycleResolver{}, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}

	ctx := reconciler(&cleanupLookupStore{commands: []string{"docker compose down"}}).withCleanupCommands(context.Background(), record)
	if commands, ok := sandbox.CleanupCommands(ctx); !ok || strings.Join(commands, "|") != "docker compose down" {
		t.Fatalf("current Cleanup = %v (%v), want the project's commands", commands, ok)
	}

	ctx = reconciler(&cleanupLookupStore{}).withCleanupCommands(context.Background(), record)
	if commands, ok := sandbox.CleanupCommands(ctx); !ok || len(commands) != 0 {
		t.Fatalf("a project without Cleanup = %v (%v), want a known empty list", commands, ok)
	}

	ctx = reconciler(&cleanupLookupStore{err: errors.New("project deleted")}).withCleanupCommands(context.Background(), record)
	if _, ok := sandbox.CleanupCommands(ctx); ok {
		t.Fatal("a failed lookup must leave the provider's installed commands in charge")
	}

	ctx = reconciler(&lifecycleStore{}).withCleanupCommands(context.Background(), record)
	if _, ok := sandbox.CleanupCommands(ctx); ok {
		t.Fatal("a store without the lookup must leave the provider's installed commands in charge")
	}
}

// deadlineDeleteProvider records the Deletes it receives and whether each
// asked to skip the project's Cleanup.
type deadlineDeleteProvider struct {
	lifecycleProvider
	deletes []bool
}

func (p *deadlineDeleteProvider) Delete(ctx context.Context, _ sandbox.ID) error {
	p.deletes = append(p.deletes, sandbox.CleanupSkipped(ctx))
	return nil
}

// Past the deletion deadline the reconciler gives up on the row, but first
// sends one Delete that skips Cleanup, so a Cleanup that outlived the deadline
// never leaves the workspace running.
func TestDeletionDeadlineDeletesWithoutCleanupBeforeGivingUp(t *testing.T) {
	requested := time.Now().Add(-time.Hour)
	record := domain.Sandbox{
		OrgID: "org-1", SessionID: "session-1", Provider: "coder",
		ProviderEnvironmentID: "workspace-1", DeletionRequestedAt: &requested,
	}
	provider := &deadlineDeleteProvider{lifecycleProvider: lifecycleProvider{
		environment: sandbox.Environment{ID: "workspace-1", State: sandbox.StateRunning},
	}}
	reconciler := New(&lifecycleStore{}, lifecycleResolver{provider: provider}, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := reconciler.reconcileDeletion(context.Background(), record, provider); err != nil {
		t.Fatal(err)
	}
	if len(provider.deletes) != 1 || !provider.deletes[0] {
		t.Fatalf("deletes = %v, want one that skips Cleanup", provider.deletes)
	}

	// Before the deadline Delete keeps running Cleanup.
	fresh := time.Now()
	record.DeletionRequestedAt = &fresh
	provider.deletes = nil
	if err := reconciler.reconcileDeletion(context.Background(), record, provider); err != nil {
		t.Fatal(err)
	}
	if len(provider.deletes) != 1 || provider.deletes[0] {
		t.Fatalf("deletes = %v, want one with Cleanup", provider.deletes)
	}
}

// sessionEventStore records the events appended to a session.
type sessionEventStore struct {
	lifecycleStore
	mu     sync.Mutex
	events []string
}

func (s *sessionEventStore) AppendSessionEvent(ctx context.Context, orgID, sessionID, eventType string, payload json.RawMessage) (domain.ClientEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.ClientEvent{}, err
	}
	s.events = append(s.events, orgID+"/"+sessionID+" "+eventType+" "+string(payload))
	return domain.ClientEvent{}, nil
}

// cleanupStepsProvider appends a Cleanup step from its Delete, after the
// reconcile pass that called it has ended, as the Coder provider does.
type cleanupStepsProvider struct {
	lifecycleProvider
	done chan struct{}
}

func (p *cleanupStepsProvider) Delete(ctx context.Context, _ sandbox.ID) error {
	go func() {
		defer close(p.done)
		background := context.WithoutCancel(ctx)
		time.Sleep(20 * time.Millisecond)
		sandbox.RecordSessionEvent(background, "workspace.script.step", map[string]any{"phase": "cleanup", "step": 1})
	}()
	return nil
}

// A provider's Delete appends events to the session whose sandbox it deletes,
// even once the reconcile pass that asked for the deletion is over.
func TestDeletionLetsTheProviderAppendSessionEvents(t *testing.T) {
	record := domain.Sandbox{OrgID: "org-1", SessionID: "session-1", Provider: "coder", ProviderEnvironmentID: "workspace-1"}
	provider := &cleanupStepsProvider{
		lifecycleProvider: lifecycleProvider{environment: sandbox.Environment{ID: "workspace-1", State: sandbox.StateRunning}},
		done:              make(chan struct{}),
	}
	store := &sessionEventStore{}
	reconciler := New(store, lifecycleResolver{provider: provider}, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	if err := reconciler.reconcileDeletion(ctx, record, provider); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-provider.done
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.events) != 1 || store.events[0] != `org-1/session-1 workspace.script.step {"phase":"cleanup","step":1}` {
		t.Fatalf("events = %q", store.events)
	}
}
