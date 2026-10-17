package litmus

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSweepFinalizesInterruptedAcceptedSubmitWhenTesterExited(t *testing.T) {
	for _, tc := range []struct {
		name        string
		afterExpiry bool
	}{
		{name: "before deadline"},
		{name: "after deadline", afterExpiry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, runDir, _ := submitSvc(t)
			acceptedAt := svc.clock()
			svc.clock = func() time.Time { return acceptedAt }
			run, err := svc.update("run_s1", func(r *Run) {
				r.Deadline = acceptedAt.Add(2 * time.Minute)
			})
			is(t, "")(nil, err)
			beforeChecks, err := svc.store.Checks(run.ID)
			is(t, "")(beforeChecks, err)

			// Block the verdict write after Submit records the accepted testing end.
			// The fresh service below then resumes that unfinished run.
			verdictPath := filepath.Join(runDir, "verdict.md")
			if err := os.Mkdir(verdictPath, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Submit(ctx, "s1", "accepted summary", ""); err == nil {
				t.Fatal("Submit succeeded despite the verdict path blocker")
			}
			interrupted, err := svc.store.Get(run.ID)
			is(t, "")(interrupted, err)
			if !interrupted.TestingEndedAt.Equal(acceptedAt) || interrupted.State.Closed() {
				t.Fatalf("Submit did not leave an accepted, unfinished run: %+v", interrupted)
			}
			if err := os.Remove(verdictPath); err != nil {
				t.Fatal(err)
			}

			recoveryAt := acceptedAt.Add(time.Minute)
			if tc.afterExpiry {
				recoveryAt = interrupted.Deadline.Add(time.Second)
			}
			fresh := newTestService(t, NewStore(svc.store.dataDir()), func() time.Time { return recoveryAt }).WithGH(fixtureGH(t))
			fresh.runner, fresh.retryPause = gitFixture(t), 0
			fresh.WithActivity(func(_ context.Context, sessionID string) (string, time.Time, bool) {
				return "exited", acceptedAt, sessionID == interrupted.SessionID
			})

			if killed := sweep(fresh, nil); len(killed) != 0 {
				t.Fatalf("recovery killed %v while finalizing the accepted submit", killed)
			}
			recovered, err := fresh.store.Get(run.ID)
			is(t, "")(recovered, err)
			if recovered.State != RunSubmitted || recovered.Error == ReasonStopped || !recovered.TestingEndedAt.Equal(interrupted.TestingEndedAt) {
				t.Fatalf("recovered run = %+v, want submitted with the accepted testing end %v", recovered, interrupted.TestingEndedAt)
			}
			afterChecks, err := fresh.store.Checks(run.ID)
			is(t, "")(afterChecks, err)
			if !reflect.DeepEqual(afterChecks, beforeChecks) {
				t.Fatalf("recovery changed saved checks:\nbefore=%+v\nafter=%+v", beforeChecks, afterChecks)
			}
			exists(t, verdictPath)
		})
	}
}

func TestSweepRetriesAcceptedSubmitAfterStoppedCloseFailure(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	acceptedAt := svc.clock()
	svc.clock = func() time.Time { return acceptedAt }
	run, err := svc.update("run_s1", func(r *Run) {
		r.Deadline = acceptedAt.Add(2 * time.Minute)
	})
	is(t, "")(nil, err)
	beforeChecks, err := svc.store.Checks(run.ID)
	is(t, "")(beforeChecks, err)

	verdictPath := filepath.Join(runDir, "verdict.md")
	if err := os.Mkdir(verdictPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, "s1", "accepted summary", ""); err == nil {
		t.Fatal("Submit succeeded despite the verdict path blocker")
	}
	interrupted, err := svc.store.Get(run.ID)
	is(t, "")(interrupted, err)
	if !interrupted.TestingEndedAt.Equal(acceptedAt) || interrupted.State.Closed() {
		t.Fatalf("Submit did not leave an accepted, unfinished run: %+v", interrupted)
	}

	recoveryAt := interrupted.Deadline.Add(time.Second)
	fresh := newTestService(t, NewStore(svc.store.dataDir()), func() time.Time { return recoveryAt }).WithGH(fixtureGH(t))
	fresh.runner, fresh.retryPause = gitFixture(t), 0
	fresh.WithActivity(func(_ context.Context, sessionID string) (string, time.Time, bool) {
		return "exited", acceptedAt, sessionID == interrupted.SessionID
	})
	var killed []string
	fresh.SweepExpired(ctx, func(_ context.Context, sessionID string) error {
		killed = append(killed, sessionID)
		return nil
	}, quiet)
	if len(killed) != 0 {
		t.Fatalf("unfinished accepted submit entered expiry cleanup: %v", killed)
	}
	unfinished, err := fresh.store.Get(run.ID)
	is(t, "")(unfinished, err)
	if unfinished.State != interrupted.State || unfinished.Error == ReasonStopped || fresh.reaped(run.ID) {
		t.Fatalf("failed close expired or reaped an accepted submit: run=%+v reaped=%t", unfinished, fresh.reaped(run.ID))
	}

	if err := os.Remove(verdictPath); err != nil {
		t.Fatal(err)
	}
	fresh.SweepExpired(ctx, func(_ context.Context, sessionID string) error {
		t.Errorf("second sweep killed %q instead of resuming the accepted submit", sessionID)
		return nil
	}, quiet)
	recovered, err := fresh.store.Get(run.ID)
	is(t, "")(recovered, err)
	if recovered.State != RunSubmitted || recovered.Error == ReasonStopped || !recovered.TestingEndedAt.Equal(acceptedAt) {
		t.Fatalf("recovered run = %+v, want submitted with the accepted testing end %v", recovered, acceptedAt)
	}
	afterChecks, err := fresh.store.Checks(run.ID)
	is(t, "")(afterChecks, err)
	if !reflect.DeepEqual(afterChecks, beforeChecks) {
		t.Fatalf("recovery changed saved checks:\nbefore=%+v\nafter=%+v", beforeChecks, afterChecks)
	}
	exists(t, verdictPath)
}
