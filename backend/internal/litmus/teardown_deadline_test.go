package litmus

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type teardownRunner func(context.Context, string, []string, string, ...string) ([]byte, error)

func (f teardownRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	return f(ctx, dir, env, name, args...)
}

func TestTeardownAfterSlowFinalizeOwnsDeadline(t *testing.T) {
	for _, deadlineClose := range []bool{false, true} {
		name := "submit"
		if deadlineClose {
			name = "deadline close"
		}
		t.Run(name, func(t *testing.T) {
			svc, _, runDir, artifacts := submitSvc(t)
			f, git, _ := leaveLeg(t, svc)
			svc.kill = nil
			legDir := svc.store.legDir("run_s1", "head")
			writeFile(t, filepath.Join(legDir, "home", "private"), "leg state")
			owned, err := readOwned(legDir)
			is(t, "")(nil, err)
			owned.DaemonPort, owned.DebugPort = 3200, 9400
			is(t, "")(nil, writeOwned(legDir, owned))
			// Death confirmation must poll again after the request expires.
			f.busy = 1
			_, err = svc.update("run_s1", func(run *Run) {
				run.Comment = CommentPost
				run.Legs[0].RecordStartedAt = svc.clock()
			})
			is(t, "")(nil, err)
			writeFile(t, recordingPath(runDir, "head"), "playable")
			svc.gh = &commentGH{t: t, login: "tester"}
			type requestKey struct{}
			requestCtx, cancel := context.WithTimeout(context.WithValue(ctx, requestKey{}, "trace"), 100*time.Millisecond)
			defer cancel()
			finalized := false
			changed := filepath.Join(svc.ownerHome, ".zshrc")
			svc.media = pointerMedia{inspect: func(stepCtx context.Context, _ []string) error {
				if !finalized {
					<-requestCtx.Done() // A slow finalize consumes the request's deadline.
					writeFile(t, changed, "changed during the run")
					finalized = true
				}
				return stepCtx.Err()
			}}
			svc.runner = teardownRunner(func(stepCtx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
				if strings.HasSuffix(name, "ffprobe") {
					return []byte("60\n"), nil
				}
				if stepCtx.Err() != nil || stepCtx.Value(requestKey{}) != "trace" {
					t.Fatalf("prune inherited the spent request or lost its values: %v", stepCtx.Err())
				}
				until, ok := stepCtx.Deadline()
				if !ok || time.Until(until) < PruneBudget-time.Second {
					t.Fatalf("prune lacks its own budget: %v", until)
				}
				return git.Run(stepCtx, dir, env, name, args...)
			})
			reported := false
			svc.report = func(reportCtx context.Context, _, _ string) error {
				until, ok := reportCtx.Deadline()
				if reportCtx.Err() != nil || reportCtx.Value(requestKey{}) != "trace" || !ok || time.Until(until) < doneReportBudget-time.Second {
					t.Fatalf("done report lacks a fresh context: %v, %v", reportCtx.Err(), until)
				}
				reported = true
				return nil
			}
			var v Verdict
			if deadlineClose {
				run, err := svc.store.Get("run_s1")
				is(t, "")(nil, err)
				checks, err := svc.store.Checks(run.ID)
				is(t, "")(nil, err)
				v, err = svc.closeRunAtDeadline(requestCtx, run, checks, "", "", "")
				is(t, "")(nil, err)
			} else {
				v, err = svc.Submit(requestCtx, "s1", "", "")
				is(t, "")(nil, err)
			}
			if !finalized || !reported || v.CommentState != "posted" || len(f.started) != 0 || git.count("worktree prune") != 1 {
				t.Fatalf("post-finalize work incomplete: finalized=%v reported=%v comment=%s live=%v git=%v", finalized, reported, v.CommentState, f.started, git.calls)
			}
			if pathExists(filepath.Join(legDir, "home")) || pathExists(filepath.Join(legDir, "data")) {
				t.Fatal("teardown left private state behind")
			}
			run, err := svc.store.Get("run_s1")
			is(t, "")(nil, err)
			if run.Legs[0].State != "cleaned" {
				t.Fatalf("leg state = %s", run.Legs[0].State)
			}
			for _, dir := range []string{runDir, artifacts} {
				md, err := os.ReadFile(filepath.Join(dir, "verdict.md"))
				is(t, "")(nil, err)
				if !strings.Contains(string(md), changed) || strings.Contains(string(md), "teardown has not completed") || strings.Contains(string(md), "Cleanup after submit failed") {
					t.Fatalf("verdict lacks actual teardown results: %s", md)
				}
			}
		})
	}
}

func TestHomeGuardSweepRewritesPendingVerdictAndLegStates(t *testing.T) {
	for _, wiped := range []bool{false, true} {
		t.Run(map[bool]string{false: "running ownership", true: "cleaned ownership"}[wiped], func(t *testing.T) {
			svc, _, runDir, artifacts := submitSvc(t)
			f, _, _ := leaveLeg(t, svc)
			svc.kill = nil
			legDir := svc.store.legDir("run_s1", "head")
			writeFile(t, filepath.Join(legDir, "home", "private"), "leg state")
			run, err := svc.store.Get("run_s1")
			is(t, "")(nil, err)
			clone := run.Legs[0].Worktree
			runGit(t, clone, "init", "--quiet")
			runGit(t, clone, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--quiet", "--allow-empty", "-m", "fixture")
			privateWorktree := filepath.Join(legDir, "data", "worktrees", "private")
			runGit(t, clone, "worktree", "add", "--quiet", "-b", "ao/litmus-fixture", privateWorktree)
			changed := filepath.Join(svc.ownerHome, ".zshrc")
			writeFile(t, changed, "changed during the run")
			f.procsErr = errors.New("fixture process listing unavailable")
			is(t, "")(svc.Submit(ctx, "s1", "", ""))
			for _, dir := range []string{runDir, artifacts} {
				path := filepath.Join(dir, "verdict.md")
				md, err := os.ReadFile(path)
				is(t, "")(nil, err)
				if !strings.Contains(string(md), "Unverified: teardown has not completed.") {
					t.Fatalf("fixture lacks pending home section: %s", md)
				}
				writeFile(t, path, string(md)+"\nReviewer note retained.\n")
			}
			f.procsErr = nil
			if wiped {
				// A daemon can exit between wiping owned state and saving run.json.
				is(t, "")(nil, svc.cleanLeg(ctx, "run_s1", "head"))
			}
			sweepStale(svc.store.dataDir(), f.ops(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			run, err = svc.store.Get("run_s1")
			is(t, "")(nil, err)
			if run.Legs[0].State != "cleaned" || pathExists(filepath.Join(legDir, "home")) {
				t.Fatalf("stale cleanup incomplete: %+v", run.Legs)
			}
			if listed := runGit(t, clone, "worktree", "list", "--porcelain"); strings.Contains(listed, privateWorktree) {
				t.Fatalf("stale cleanup left a worktree registration: %s", listed)
			}
			if branches := runGit(t, clone, "for-each-ref", "--format=%(refname:short)", "refs/heads/ao/litmus-*"); branches != "" {
				t.Fatalf("stale cleanup left a private branch: %s", branches)
			}
			for _, dir := range []string{runDir, artifacts} {
				md, err := os.ReadFile(filepath.Join(dir, "verdict.md"))
				is(t, "")(nil, err)
				if !strings.Contains(string(md), changed) || strings.Contains(string(md), "teardown has not completed") || strings.Contains(string(md), "Reviewer note retained.") || strings.Count(string(md), "## Owner home changes") != 1 {
					t.Fatalf("stale cleanup did not regenerate the report: %s", md)
				}
			}
		})
	}
}

func TestTeardownPersistsEachLegCleanupResult(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	run, err := svc.update("run_s1", func(run *Run) {
		run.Legs = []Leg{{Name: "head", State: "running"}, {Name: "base", State: "running"}}
	})
	is(t, "")(nil, err)
	for _, leg := range run.Legs {
		dir := svc.store.legDir(run.ID, leg.Name)
		writeFile(t, filepath.Join(dir, "home", "private"), "leg state")
		port := 3200
		if leg.Name == "base" {
			port++
		}
		is(t, "")(nil, writeOwned(dir, Owned{RunID: run.ID, Leg: leg.Name, State: "running", DaemonPort: port}))
	}
	svc.sys = (&fakeSys{}).ops()
	svc.sys.portBusy = func(port int) (bool, error) {
		if port == 3201 {
			return false, errors.New("fixture base port probe failed")
		}
		return false, nil
	}
	if err := svc.teardown(ctx, run); err == nil || !strings.Contains(err.Error(), "base port probe failed") {
		t.Fatalf("teardown error = %v", err)
	}
	run, err = svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if run.Legs[0].State != "cleaned" || run.Legs[1].State != "running" {
		t.Fatalf("leg states do not reflect cleanup results: %+v", run.Legs)
	}
	if pathExists(filepath.Join(svc.store.legDir(run.ID, "head"), "home")) || !pathExists(filepath.Join(svc.store.legDir(run.ID, "base"), "home")) {
		t.Fatal("private homes do not match leg cleanup results")
	}
	changes, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if !strings.Contains(changes, "teardown has not completed") {
		t.Fatalf("incomplete teardown finalized home report: %s", changes)
	}
}

func TestHomeGuardSweepFinishesClosedRunWithoutLaunchedLegs(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	// The required fixture root disappears during the first after-snapshot.
	home := svc.ownerHome
	aside := home + "-aside"
	is(t, "")(nil, os.Rename(home, aside))
	t.Cleanup(func() {
		if pathExists(aside) {
			if err := os.Rename(aside, home); err != nil {
				t.Error(err)
			}
		}
	})
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(svc.SubmitBlocked(ctx, "s1", "build unavailable"))
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	md := readRunFile(t, svc, run, "verdict.md")
	if len(run.Legs) != 0 || !strings.Contains(md, "Unverified: teardown has not completed.") {
		t.Fatalf("fixture lacks the closed run's pending report: %+v, %s", run.Legs, md)
	}
	is(t, "")(nil, os.Rename(aside, home))
	changed := filepath.Join(home, ".zshrc")
	writeFile(t, changed, "owner change")
	sweepStale(svc.store.dataDir(), (&fakeSys{}).ops(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	md = readRunFile(t, svc, run, "verdict.md")
	if strings.Contains(md, "teardown has not completed") || !strings.Contains(md, changed) {
		t.Fatalf("closed run without ownership stayed pending: %s", md)
	}
}

func TestHomeGuardSweepKeepsReportPendingForMissingRunningOwnership(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	run, err := svc.update("run_s1", func(run *Run) {
		run.State = RunSubmitted
		run.Legs = []Leg{{Name: "head", State: "running"}, {Name: "base", State: "running", PID: 42}}
	})
	is(t, "")(nil, err)
	for _, leg := range run.Legs {
		writeFile(t, filepath.Join(svc.store.legDir(run.ID, leg.Name), "home", "private"), "leg state")
	}
	is(t, "")(nil, writeOwned(svc.store.legDir(run.ID, "head"), Owned{RunID: run.ID, Leg: "head", State: "running"}))
	sweepStale(svc.store.dataDir(), (&fakeSys{}).ops(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	run, err = svc.store.Get(run.ID)
	is(t, "")(nil, err)
	changes, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if run.Legs[0].State != "cleaned" || run.Legs[1].State != "running" || !strings.Contains(changes, "teardown has not completed") || !pathExists(filepath.Join(svc.store.legDir(run.ID, "base"), "home")) {
		t.Fatalf("missing ownership finalized cleanup: %+v, %s", run.Legs, changes)
	}
}
