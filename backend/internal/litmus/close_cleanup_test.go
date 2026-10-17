package litmus

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConcurrentSubmitCleanupTearsDownOnce(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	svc.kill = nil
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	svc.closeEngine = func(string) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	finished := make(chan error, 1)
	go func() { _, err := svc.Submit(ctx, "s1", "", ""); finished <- err }()
	<-entered
	_, cleanupErr := svc.Cleanup(ctx, "s1")
	close(release)
	is(t, "")(nil, <-finished)
	is(t, "TESTING_RUN_BUSY")(nil, cleanupErr)
	// Sequential cleanup also reads the durable completion marker.
	is(t, "")(svc.Cleanup(ctx, "s1"))
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	if calls.Load() != 1 || !run.TeardownComplete {
		t.Fatalf("teardown calls=%d, complete=%v", calls.Load(), run.TeardownComplete)
	}
	restarted := newTestService(t, svc.store, svc.clock)
	restarted.closeEngine = func(string) { calls.Add(1) }
	is(t, "")(restarted.Cleanup(ctx, "s1"))
	if calls.Load() != 1 {
		t.Fatalf("daemon restart repeated teardown: %d calls", calls.Load())
	}
}

func TestTimedOutTeardownSnapshotRetriedWithoutTeardown(t *testing.T) {
	svc, _, runDir, artifacts := submitSvc(t)
	svc.kill = nil
	scans, teardowns := 0, 0
	svc.closeEngine = func(string) { teardowns++ }
	svc.snapshotHomeAfter = func(roots []homeScanRoot, runsRoot string) (homeSnapshot, error) {
		scans++
		if scans == 1 {
			return homeSnapshot{Roots: roots, Incomplete: true, TimedOut: true}, nil
		}
		return scanHomeRoots(roots, runsRoot)
	}
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	report, err := svc.store.HomeChanges("run_s1")
	is(t, "")(nil, err)
	if !strings.HasSuffix(report, incompleteHomeReportSuffix) || !strings.Contains(report, homeSnapshotIncomplete) {
		t.Fatalf("timed-out report was finalized: %q", report)
	}
	changed := filepath.Join(svc.ownerHome, ".zshrc")
	writeFile(t, changed, "owner change")
	svc.sweepStale(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if scans != 2 || teardowns != 1 {
		t.Fatalf("snapshot scans=%d, teardowns=%d", scans, teardowns)
	}
	for _, dir := range []string{runDir, artifacts} {
		md, err := os.ReadFile(filepath.Join(dir, "verdict.md"))
		is(t, "")(nil, err)
		if strings.Contains(string(md), "snapshot is incomplete") || strings.Contains(string(md), homeSnapshotIncomplete) || !strings.Contains(string(md), changed) {
			t.Fatalf("sweep did not replace incomplete report: %s", md)
		}
	}
}

func TestRunCloseSweepsOrphansAndKeepsLiveRuns(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	svc.kill = nil
	svc.sys = (&fakeSys{}).ops()
	orphan := Run{ID: "run_orphan", State: RunRunning, Legs: []Leg{{Name: "head", State: "running"}}}
	live := Run{ID: "run_live", State: RunRunning, Legs: []Leg{{Name: "head", State: "running"}}}
	for _, run := range []Run{orphan, live} {
		is(t, "")(nil, svc.store.Save(run))
		dir := svc.store.legDir(run.ID, "head")
		writeFile(t, filepath.Join(dir, "home", "private"), "private state")
		is(t, "")(nil, writeOwned(dir, Owned{RunID: run.ID, Leg: "head", State: "running"}))
	}
	svc.mu.Lock()
	done, err := svc.markBusy(live.ID, "launch head")
	svc.mu.Unlock()
	is(t, "")(nil, err)
	done() // Idle live targets are still owned by this daemon.
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	if pathExists(filepath.Join(svc.store.legDir(orphan.ID, "head"), "home")) {
		t.Fatal("run close left an earlier daemon's orphan running")
	}
	if !pathExists(filepath.Join(svc.store.legDir(live.ID, "head"), "home")) {
		t.Fatal("run close swept a live target owned by this daemon")
	}
}

func TestStaleSweepExcludesAdoptionAndValidation(t *testing.T) {
	svc, _ := newSvc(t)
	run := Run{ID: "run_old", SessionID: "old", State: RunIntake, Deadline: svc.clock().Add(RunBudget)}
	is(t, "")(nil, svc.store.Save(run))
	done, ok := svc.beginStaleSweep(run.ID)
	if !ok {
		t.Fatal("could not reserve stale cleanup")
	}
	svc.mu.Lock()
	_, claimErr := svc.claim(run.ID, run.SessionID, "o/r", 1, CommentOff, EvidenceOff)
	svc.mu.Unlock()
	is(t, "TESTING_RUN_BUSY")(nil, claimErr)
	_, _, launchErr := svc.begin("old", "launch head")
	is(t, "TESTING_RUN_BUSY")(nil, launchErr)
	done()
	svc.mu.Lock()
	validationDone, err := svc.markBusy("validate_fixture", "launch head")
	svc.mu.Unlock()
	is(t, "")(nil, err)
	if done, ok := svc.beginStaleSweep("validate_fixture"); ok {
		done()
		t.Fatal("sweep claimed an active recipe validation")
	}
	validationDone()
}

func TestStaleSweepDoesNotReserveCompletedRun(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	svc.kill = nil
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	if !run.TeardownComplete {
		t.Fatal("submit did not complete teardown")
	}
	for _, service := range []*Service{svc, newTestService(t, svc.store, svc.clock)} {
		if done, ok := service.beginStaleSweep(run.ID); ok {
			done()
			t.Fatal("sweep reserved a run with completed teardown and home report")
		}
	}
}
