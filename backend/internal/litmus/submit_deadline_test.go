package litmus

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionartifacts"
)

type submitDeadlineMedia func(context.Context, ...string) error

func (f submitDeadlineMedia) Run(ctx context.Context, args ...string) error { return f(ctx, args...) }

func TestAcceptedSubmitFinalizesAcrossDeadline(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	svc.kill = nil
	acceptedAt := svc.clock()
	var nanos atomic.Int64
	nanos.Store(acceptedAt.UnixNano())
	svc.clock = func() time.Time { return time.Unix(0, nanos.Load()).UTC() }
	run, err := svc.update("run_s1", func(r *Run) {
		r.Deadline = acceptedAt.Add(94 * time.Second)
		r.Legs[0].RecordStartedAt = acceptedAt.Add(-time.Minute)
	})
	is(t, "")(nil, err)
	is(t, "")(nil, os.WriteFile(recordingPath(runDir, "head"), []byte("mp4"), 0o600))
	svc.runner = &videoProbeRunner{duration: "120"}
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	svc.media = submitDeadlineMedia(func(_ context.Context, args ...string) error {
		first.Do(func() { close(entered); <-release })
		return os.WriteFile(args[len(args)-1], []byte("mp4"), 0o600)
	})
	type result struct {
		verdict Verdict
		err     error
	}
	finished := make(chan result, 1)
	go func() { v, err := svc.Submit(ctx, "s1", "The tester's full verdict.", ""); finished <- result{v, err} }()
	<-entered
	nanos.Store(run.Deadline.Add(time.Minute).UnixNano())
	status, statusErr := svc.Status(ctx, "s1")
	detail, detailErr := svc.RunDetail(ctx, run.ID)
	killed := sweep(svc, nil)
	close(release)
	got := <-finished
	is(t, "")(nil, statusErr)
	is(t, "")(nil, detailErr)
	is(t, "")(nil, got.err)
	for _, reading := range []Run{status.Run, detail.Run} {
		if reading.State.Closed() || !reading.TestingEndedAt.Equal(acceptedAt) {
			t.Fatalf("accepted submit did not report finalizing: %+v", reading)
		}
	}
	if len(status.InFlight) != 1 || status.InFlight[0].Verb != "submit" || len(killed) != 0 {
		t.Fatalf("submit in flight=%+v, killed=%v", status.InFlight, killed)
	}
	saved, err := svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if saved.State != RunSubmitted || !saved.TestingEndedAt.Equal(acceptedAt) || got.verdict.Summary != "The tester's full verdict." || got.verdict.Overall != CheckPass {
		t.Fatalf("final run=%+v, verdict=%+v", saved, got.verdict)
	}
}

func TestDeadlineReadLeavesClosureToSweep(t *testing.T) {
	svc, now := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(nil, err)
	*now = run.Deadline.Add(time.Second)
	is(t, "TESTING_RUN_CLOSED")(svc.Status(ctx, "s1"))
	before, err := svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if before.State != run.State {
		t.Fatalf("read changed run state to %s", before.State)
	}
	sweep(svc, nil)
	after, err := svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if after.State != RunExpired {
		t.Fatalf("deadline sweep state=%s, want expired", after.State)
	}
}

func TestRestartClosesExpiredRunWithDurableTestingEnd(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	run, err := svc.update("run_s1", func(r *Run) {
		r.TestingEndedAt = r.CreatedAt.Add(time.Minute)
		r.Deadline = r.CreatedAt.Add(2 * time.Minute)
		r.State = RunRunning
	})
	is(t, "")(nil, err)
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(checks, err)

	fresh := newTestService(t, NewStore(svc.store.dataDir()), func() time.Time { return run.Deadline.Add(time.Second) }).WithGH(fixtureGH(t))
	fresh.runner, fresh.retryPause = gitFixture(t), 0
	killed := make(chan string, 1)
	fresh.kill = func(_ context.Context, sessionID string) error {
		killed <- sessionID
		return nil
	}
	oldEndGrace := endGrace
	endGrace = 0
	t.Cleanup(func() { endGrace = oldEndGrace })
	if got := sweep(fresh, nil); got != nil {
		t.Fatalf("deadline sweep killed %v before finalizing the durable run", got)
	}
	after, err := fresh.store.Get(run.ID)
	is(t, "")(after, err)
	if after.State != RunSubmitted || !after.TestingEndedAt.Equal(run.TestingEndedAt) {
		t.Fatalf("restarted run = %+v, want submitted with end %v", after, run.TestingEndedAt)
	}
	afterChecks, err := fresh.store.Checks(run.ID)
	is(t, "")(afterChecks, err)
	if !reflect.DeepEqual(afterChecks, checks) {
		t.Fatalf("deadline close changed the recorded checks:\nbefore=%+v\nafter=%+v", checks, afterChecks)
	}
	select {
	case sessionID := <-killed:
		if sessionID != run.SessionID {
			t.Fatalf("killed session %q, want %q", sessionID, run.SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline recovery did not kill the tester")
	}
}

func TestSubmitFinalizeFailuresRecoverAtTheNextDeadlineTick(t *testing.T) {
	tests := []struct {
		name      string
		ask       bool
		prepare   func(t *testing.T, runDir, artifacts string)
		clear     func(t *testing.T, runDir, artifacts string)
		wantErr   string
		wantLog   string
		wantCause string
		wantState RunState
	}{
		{
			name: "write verdict",
			prepare: func(t *testing.T, runDir, _ string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(runDir, "verdict.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, runDir, _ string) {
				t.Helper()
				if err := os.Remove(filepath.Join(runDir, "verdict.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr:   "rename ",
			wantLog:   "could not finalize verdict",
			wantCause: "rename ",
			wantState: RunSubmitted,
		},
		{
			name: "publish artifacts",
			prepare: func(t *testing.T, _, artifacts string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(artifacts), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(artifacts, []byte("artifact blocker"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, _, artifacts string) {
				t.Helper()
				if err := os.Remove(artifacts); err != nil {
					t.Fatal(err)
				}
			},
			wantErr:   "copy evidence to the session artifact dir",
			wantLog:   "could not finalize verdict",
			wantCause: "copy evidence to the session artifact dir",
			wantState: RunSubmitted,
		},
		{
			name: "compose comment",
			ask:  true,
			prepare: func(t *testing.T, runDir, _ string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(runDir, "comment.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, runDir, _ string) {
				t.Helper()
				if err := os.Remove(filepath.Join(runDir, "comment.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr:   "write comment.md",
			wantLog:   "could not compose comment",
			wantCause: "rename ",
			wantState: RunAwaitingApproval,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, runDir, _ := askSvc(t, tc.ask)
			artifacts := sessionartifacts.Dir(svc.store.dataDir(), domain.SessionID("s1"))
			// askSvc already built the complete ledger; retrieve the same run's artifact path without creating another run.
			beforeRun, err := svc.store.Get("run_s1")
			is(t, "")(beforeRun, err)
			beforeChecks, err := svc.store.Checks(beforeRun.ID)
			is(t, "")(beforeChecks, err)
			tc.prepare(t, runDir, artifacts)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			_, err = svc.Submit(ctx, "s1", "looks right on head", "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Submit error = %v, want %q", err, tc.wantErr)
			}
			failed, readErr := svc.store.Get(beforeRun.ID)
			is(t, "")(failed, readErr)
			if failed.TestingEndedAt.IsZero() {
				t.Fatal("failed finalize did not persist TestingEndedAt")
			}
			if !strings.Contains(logs.String(), tc.wantLog) || !strings.Contains(logs.String(), tc.wantCause) {
				t.Fatalf("finalize failure was not logged with its cause: %s", logs.String())
			}
			tc.clear(t, runDir, artifacts)
			end := failed.Deadline.Add(time.Second)
			svc.clock = func() time.Time { return end }
			svc.kill = nil
			if got := sweep(svc, nil); got != nil {
				t.Fatalf("recovery sweep killed %v", got)
			}
			recovered, readErr := svc.store.Get(beforeRun.ID)
			is(t, "")(recovered, readErr)
			if recovered.State != tc.wantState || !recovered.TestingEndedAt.Equal(failed.TestingEndedAt) {
				t.Fatalf("recovered run = %+v, want state %s and end %v", recovered, tc.wantState, failed.TestingEndedAt)
			}
			afterChecks, readErr := svc.store.Checks(beforeRun.ID)
			is(t, "")(afterChecks, readErr)
			if !reflect.DeepEqual(afterChecks, beforeChecks) {
				t.Fatalf("recovery changed recorded checks:\nbefore=%+v\nafter=%+v", beforeChecks, afterChecks)
			}
		})
	}
}
