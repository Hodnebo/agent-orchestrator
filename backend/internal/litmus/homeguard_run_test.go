package litmus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHomeGuardIntakeTeardownAndLocalReports(t *testing.T) {
	svc, _ := newSvc(t)
	home := t.TempDir()
	config := filepath.Join(home, ".codex", "config.toml")
	writeFile(t, config, "model=before-secret")
	scans := 0
	svc.snapshotHome = func() (homeSnapshot, error) {
		scans++
		return scanHomeRoots([]homeScanRoot{{Path: config, Hash: true}}, svc.store.root)
	}
	run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	is(t, "")(nil, svc.snapshotIntakeHome(run.ID))
	snapshot := filepath.Join(svc.store.root, run.ID, homeSnapshotFile)
	before, err := os.ReadFile(snapshot)
	is(t, "")(nil, err)
	info, err := os.Stat(snapshot)
	if err != nil || info.Mode().Perm() != 0o400 || bytes.Contains(before, []byte("before-secret")) {
		t.Fatalf("snapshot not private metadata: %v %v", info, err)
	}
	writeFile(t, config, "model=after-secret")
	_, err = svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	is(t, "")(nil, svc.snapshotIntakeHome(run.ID))
	if scans != 1 {
		t.Fatalf("intake retry replaced baseline: %d scans", scans)
	}
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(nil, svc.teardown(ctx, run))
	changes, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if changes != homeChangesReport("MODIFIED "+strconv.Quote(config)) {
		t.Fatalf("home changes %q", changes)
	}
	raw, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, homeChangesFile))
	if err != nil || string(raw) != changes {
		t.Fatalf("stored report differs: %q %v", raw, err)
	}
	after, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("baseline changed: %v", err)
	}
	v := commentVerdict(nil)
	md := renderVerdict(run, v, false, nil, filepath.Join(svc.store.root, run.ID), run.CreatedAt, nil)
	if !strings.Contains(md, "## Owner home changes\n\n"+homeChangesCaveat+"\n") || !strings.Contains(md, config) {
		t.Fatalf("verdict omitted report: %s", md)
	}
	for _, body := range []string{renderComment(run, v, evidence{}, run.CreatedAt), summaryCommentBody(run, v, run.CreatedAt, false)} {
		if strings.Contains(body, config) || strings.Contains(body, "Owner home changes") || strings.Contains(body, "after-secret") {
			t.Fatalf("home report leaked into comment: %s", body)
		}
	}
	writeFile(t, config, "later owner activity")
	is(t, "")(nil, svc.teardown(ctx, run))
	if report, err := svc.store.HomeChanges(run.ID); err != nil || report != changes {
		t.Fatalf("repeat teardown changed report: %q %v", report, err)
	}
}

func TestHomeGuardSnapshotFailureRefusesIntake(t *testing.T) {
	svc, _ := newSvc(t)
	svc.snapshotHome = func() (homeSnapshot, error) { return homeSnapshot{}, errors.New("fixture denied") }
	_, err := svc.Intake(ctx, "s1", prURL)
	if err == nil || !strings.Contains(err.Error(), "fixture denied") {
		t.Fatalf("intake accepted missing guard: %v", err)
	}
	run, err := svc.store.Get("run_s1")
	if err != nil || run.HeadSHA != "" {
		t.Fatalf("failed snapshot pinned launchable run: %+v %v", run, err)
	}
}

func TestHomeGuardStaleSweepFinalizesAfterBothLegs(t *testing.T) {
	svc, _ := newSvc(t)
	home := t.TempDir()
	config := filepath.Join(home, "config")
	writeFile(t, config, "before")
	svc.snapshotHome = func() (homeSnapshot, error) {
		return scanHomeRoots([]homeScanRoot{{Path: config, Hash: true}}, svc.store.root)
	}
	run := Run{ID: "run_stale", State: RunRunning}
	is(t, "")(nil, svc.store.Save(run))
	is(t, "")(nil, svc.snapshotIntakeHome(run.ID))
	for _, leg := range []string{"head", "base"} {
		dir := svc.store.legDir(run.ID, leg)
		is(t, "")(nil, os.MkdirAll(dir, 0o700))
		is(t, "")(nil, writeOwned(dir, Owned{RunID: run.ID, Leg: leg, State: "running"}))
	}
	writeFile(t, config, "after")
	sweepStale(svc.store.dataDir(), (&fakeSys{}).ops(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	changes, err := svc.store.HomeChanges(run.ID)
	if err != nil || changes != homeChangesReport("MODIFIED "+strconv.Quote(config)) {
		t.Fatalf("stale report %q %v", changes, err)
	}
	// A restart can occur after cleanup marked ownership but before the diff.
	is(t, "")(nil, os.Remove(filepath.Join(svc.store.root, run.ID, homeChangesFile)))
	sweepStale(svc.store.dataDir(), (&fakeSys{}).ops(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	changes, err = svc.store.HomeChanges(run.ID)
	if err != nil || changes != homeChangesReport("MODIFIED "+strconv.Quote(config)) {
		t.Fatalf("cleaned stale report %q %v", changes, err)
	}
}

func TestHomeGuardIntakeScanDoesNotHoldServiceMutex(t *testing.T) {
	svc, _ := newSvc(t)
	snapshot := svc.snapshotHome
	entered, release := make(chan struct{}), make(chan struct{})
	svc.snapshotHome = func() (homeSnapshot, error) {
		close(entered)
		<-release
		return snapshot()
	}
	finished := make(chan error, 1)
	go func() { _, err := svc.Intake(ctx, "s1", prURL); finished <- err }()
	<-entered
	unlocked := svc.mu.TryLock()
	if unlocked {
		svc.mu.Unlock()
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !unlocked {
		t.Fatal("owner home scan held the service mutex")
	}
}

func TestHomeGuardDeadlineReportsIncompleteWithoutFailingTeardown(t *testing.T) {
	svc, _ := newSvc(t)
	home := t.TempDir()
	config := filepath.Join(home, ".zshrc")
	writeFile(t, config, "fixture")
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	scanner := newHomeScanner()
	scanner.open = func(path string) (*os.File, error) {
		close(entered)
		<-release
		defer close(returned)
		return os.Open(path)
	}
	defer func() { close(release); <-returned }()
	svc.snapshotHome = func() (homeSnapshot, error) {
		limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		return scanner.scan(limited, func(context.Context) ([]homeScanRoot, error) {
			return []homeScanRoot{{Path: home, Depth: 1}, {Path: config, Hash: true}}, nil
		}, svc.store.root)
	}
	run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	is(t, "")(nil, svc.snapshotIntakeHome(run.ID))
	select {
	case <-entered:
	default:
		t.Fatal("snapshot did not reach blocked open")
	}
	var before homeSnapshot
	is(t, "")(nil, svc.store.read(run.ID, homeSnapshotFile, &before))
	if !before.Incomplete || len(before.Entries) == 0 {
		t.Fatalf("partial baseline not saved: %+v", before)
	}
	assertReport := func() {
		t.Helper()
		changes, err := svc.store.HomeChanges(run.ID)
		if err != nil || !strings.Contains(changes, homeSnapshotIncomplete) {
			t.Fatalf("home changes omitted timeout: %q %v", changes, err)
		}
		md := renderVerdict(run, commentVerdict(nil), false, nil, filepath.Join(svc.store.root, run.ID), run.CreatedAt, nil)
		if !strings.Contains(md, homeSnapshotIncomplete) {
			t.Fatalf("verdict omitted timeout: %s", md)
		}
	}
	assertReport()
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(nil, svc.teardown(ctx, run))
	assertReport()
	changes, err := svc.store.HomeChanges(run.ID)
	if err != nil || changes != homeChangesReport(homeSnapshotIncomplete) {
		t.Fatalf("teardown did not finalize partial report: %q %v", changes, err)
	}
}

func TestHomeGuardIntakeProceedsAfterDepthTwoReadDirRace(t *testing.T) {
	svc, _ := newSvc(t)
	home := svc.ownerHome
	share := filepath.Join(home, ".local", "share")
	churn := filepath.Join(share, "churn")
	writeFile(t, filepath.Join(churn, "unread"), "churning entry")
	healthy := filepath.Join(share, "healthy", "keep")
	writeFile(t, healthy, "healthy entry")
	scanner := newHomeScanner()
	scanner.readDir = func(path string) ([]os.DirEntry, error) {
		if path == churn {
			return nil, &fs.PathError{Op: "readdir", Path: path, Err: fs.ErrNotExist}
		}
		return os.ReadDir(path)
	}
	svc.snapshotHome = func() (homeSnapshot, error) {
		return scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
			return []homeScanRoot{{Path: home, Depth: 1, Required: true}, {Path: share, Depth: 2}}, nil
		}, svc.store.root)
	}
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(nil, err)
	var before homeSnapshot
	is(t, "")(nil, svc.store.read(run.ID, homeSnapshotFile, &before))
	if run.HeadSHA == "" || !before.Incomplete || before.Entries[churn].Unreadable == "" || before.Entries[healthy].Type != "file" {
		t.Fatalf("intake failed to retain partial scan: run=%+v snapshot=%+v", run, before)
	}
	assertReport := func() {
		t.Helper()
		report, err := svc.store.HomeChanges(run.ID)
		is(t, "")(nil, err)
		md := renderVerdict(run, commentVerdict(nil), false, nil, filepath.Join(svc.store.root, run.ID), run.CreatedAt, nil)
		for _, text := range []string{report, md} {
			if !strings.Contains(text, "incomplete: 1 entries unreadable") || !strings.Contains(text, churn) || strings.Contains(text, homeSnapshotIncomplete) {
				t.Fatalf("local report omitted entry error: %s", text)
			}
		}
	}
	assertReport()
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(nil, svc.teardown(ctx, run))
	assertReport()
	report, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if strings.Contains(report, "ADDED") || strings.Contains(report, "REMOVED") {
		t.Fatalf("teardown invented changes beneath unreadable directory: %s", report)
	}
}

func TestHomeChangesCaveatForPendingAndMissingBaseline(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	runDir := filepath.Join(svc.store.root, run.ID)
	is(t, "")(nil, svc.store.put(run.ID, homeSnapshotFile, homeSnapshot{}))
	report, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	section, err := homeChangesSection(runDir)
	is(t, "")(nil, err)
	if !strings.HasPrefix(report, homeChangesCaveat+"\n") || !strings.HasPrefix(section, "## Owner home changes\n\n"+homeChangesCaveat+"\n") {
		t.Fatalf("pending report=%q section=%q", report, section)
	}
	is(t, "")(nil, os.Remove(filepath.Join(runDir, homeSnapshotFile)))
	is(t, "")(nil, svc.finishHomeGuard(run.ID))
	raw, err := os.ReadFile(filepath.Join(runDir, homeChangesFile))
	if err != nil || !strings.HasPrefix(string(raw), homeChangesCaveat+"\n") || !strings.Contains(string(raw), "no intake snapshot") {
		t.Fatalf("missing baseline report=%q err=%v", raw, err)
	}
}

func TestRefreshHomeChangesRendersReportsFromState(t *testing.T) {
	svc, _, runDir, artifacts := submitSvc(t)
	svc.kill = nil
	is(t, "")(svc.Submit(ctx, "s1", "Tester summary with two paragraphs.\n\nSecond paragraph.", ""))
	var inputs verdictRenderInputs
	is(t, "")(nil, svc.store.read("run_s1", verdictRenderFile, &inputs))
	report := homeChangesReport("MODIFIED \"/owner/## first\"\n\nADDED \"/owner/second\"\n")
	is(t, "")(nil, svc.store.WriteFile("run_s1", homeChangesFile, []byte(report)))
	// No heading or existing report body is needed to regenerate the files.
	writeFile(t, filepath.Join(runDir, "verdict.md"), "no heading here")
	writeFile(t, filepath.Join(runDir, "comment.md"), "outdated comment")
	for range 2 {
		is(t, "")(nil, svc.refreshHomeChangesVerdict("run_s1"))
	}
	want := inputs.render(runDir)
	for _, dir := range []string{runDir, artifacts} {
		md, err := os.ReadFile(filepath.Join(dir, "verdict.md"))
		is(t, "")(nil, err)
		if string(md) != want || !strings.Contains(string(md), "    MODIFIED \"/owner/## first\"\n    \n    ADDED \"/owner/second\"") {
			t.Fatalf("refreshed verdict differs from fresh render or lost blank lines: %s", md)
		}
	}
	comment, err := os.ReadFile(filepath.Join(runDir, "comment.md"))
	is(t, "")(nil, err)
	docs, err := svc.renderCommentDrafts(inputs.Run, inputs.Verdict, inputs.At)
	is(t, "")(nil, err)
	wantComment := docs[0].Body
	if string(comment) != wantComment || strings.Contains(string(comment), "/owner/") {
		t.Fatalf("refreshed comment differs from fresh render: %s", comment)
	}
}

func TestRefreshHomeChangesReportsMissingInputs(t *testing.T) {
	for _, missing := range []string{verdictRenderFile, homeChangesFile} {
		t.Run(missing, func(t *testing.T) {
			svc, _, runDir, _ := submitSvc(t)
			svc.kill = nil
			is(t, "")(svc.Submit(ctx, "s1", "", ""))
			before, readErr := os.ReadFile(filepath.Join(runDir, homeChangesFile))
			is(t, "")(nil, readErr)
			is(t, "")(nil, os.Remove(filepath.Join(runDir, missing)))
			err := svc.refreshHomeChangesVerdict("run_s1")
			if missing == verdictRenderFile {
				if err != nil {
					t.Fatalf("missing %s error=%v", missing, err)
				}
				after, readErr := os.ReadFile(filepath.Join(runDir, homeChangesFile))
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("missing %s changed home report: before=%q after=%q err=%v", missing, before, after, readErr)
				}
				return
			}
			if err == nil || !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing %s error=%v", missing, err)
			}
		})
	}
}

func TestPermanentUnreadableHomeReportIsFinal(t *testing.T) {
	svc, _ := newSvc(t)
	run := Run{ID: "run_unreadable", State: RunSubmitted, TeardownComplete: true}
	is(t, "")(nil, svc.store.Save(run))
	path := filepath.Join(svc.ownerHome, ".zshrc")
	before := homeSnapshot{Roots: []homeScanRoot{{Path: path, Hash: true}}, Entries: map[string]homeEntry{
		path: {Path: path, Type: "file", SHA256: "before"},
	}}
	is(t, "")(nil, svc.store.put(run.ID, homeSnapshotFile, before))
	is(t, "")(nil, svc.store.WriteFile(run.ID, homeChangesFile, []byte(homeChangesReport(pendingHomeReportSuffix))))
	scans := 0
	svc.snapshotHomeAfter = func(roots []homeScanRoot, runsRoot string) (homeSnapshot, error) {
		scans++
		return homeSnapshot{Roots: roots, Entries: map[string]homeEntry{
			path: {Path: path, Type: "file", Unreadable: "fixture denied"},
		}, Incomplete: true}, nil
	}
	is(t, "")(nil, svc.finishHomeGuard(run.ID))
	first, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if scans != 1 || !strings.Contains(first, "UNREADABLE "+strconv.Quote(path)) || strings.Contains(first, incompleteHomeReportSuffix) || strings.Contains(first, homeSnapshotIncomplete) {
		t.Fatalf("permanent unreadable report = %q, scans=%d", first, scans)
	}
	is(t, "")(nil, svc.finishHomeGuard(run.ID))
	second, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if scans != 1 || second != first {
		t.Fatalf("permanent unreadable report was retried or changed: first=%q second=%q scans=%d", first, second, scans)
	}
	if done, ok := svc.beginStaleSweep(run.ID); ok {
		done()
		t.Fatal("sweep reserved a run with a final unreadable report")
	}
}

func TestTimedOutBaselineWithPermanentUnreadableAfterIsFinal(t *testing.T) {
	svc, _ := newSvc(t)
	run := Run{ID: "run_timeout_then_unreadable", State: RunSubmitted, TeardownComplete: true}
	is(t, "")(nil, svc.store.Save(run))
	path := filepath.Join(svc.ownerHome, ".zshrc")
	before := homeSnapshot{Roots: []homeScanRoot{{Path: path, Hash: true}}, Incomplete: true, TimedOut: true, Entries: map[string]homeEntry{
		path: {Path: path, Type: "file", SHA256: "before"},
	}}
	is(t, "")(nil, svc.store.put(run.ID, homeSnapshotFile, before))
	is(t, "")(nil, svc.store.WriteFile(run.ID, homeChangesFile, []byte(homeChangesReport(diffHomeSnapshots(before, before)+"\n"+pendingHomeReportSuffix))))
	scans := 0
	svc.snapshotHomeAfter = func(roots []homeScanRoot, runsRoot string) (homeSnapshot, error) {
		scans++
		return homeSnapshot{Roots: roots, Entries: map[string]homeEntry{
			path: {Path: path, Type: "file", Unreadable: "fixture denied"},
		}, Incomplete: true}, nil
	}
	is(t, "")(nil, svc.finishHomeGuard(run.ID))
	report, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if scans != 1 || !strings.Contains(report, homeSnapshotIncomplete) || !strings.Contains(report, "UNREADABLE "+strconv.Quote(path)) || strings.Contains(report, incompleteHomeReportSuffix) {
		t.Fatalf("timed-out baseline was treated as retryable after a permanent error: report=%q scans=%d", report, scans)
	}
	is(t, "")(nil, svc.finishHomeGuard(run.ID))
	if scans != 1 {
		t.Fatalf("permanent unreadable after was retried: scans=%d", scans)
	}
}

type homeGuardLogCounter struct{ count atomic.Int32 }

func (h *homeGuardLogCounter) Enabled(context.Context, slog.Level) bool { return true }
func (h *homeGuardLogCounter) Handle(context.Context, slog.Record) error {
	h.count.Add(1)
	return nil
}
func (h *homeGuardLogCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *homeGuardLogCounter) WithGroup(string) slog.Handler      { return h }

func TestRefreshHomeChangesWarnsOnceForOlderRun(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	svc.kill = nil
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	is(t, "")(nil, os.Remove(filepath.Join(runDir, verdictRenderFile)))
	handler := &homeGuardLogCounter{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(previous)
	for range 2 {
		is(t, "")(nil, svc.refreshHomeChangesVerdict("run_s1"))
	}
	if got := handler.count.Load(); got != 1 {
		t.Fatalf("older-run warning count=%d, want 1", got)
	}
}

func TestRefreshHomeChangesDoesNotHideMissingVerdict(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	svc.kill = nil
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	is(t, "")(nil, os.Remove(filepath.Join(runDir, verdictRenderFile)))
	is(t, "")(nil, os.Remove(filepath.Join(runDir, verdictFile)))
	is(t, "")(nil, os.Remove(filepath.Join(runDir, "verdict.md")))
	if err := svc.refreshHomeChangesVerdict("run_s1"); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing verdict and render inputs error=%v", err)
	}
}

func TestRefreshHomeChangesKeepsOlderMarkdownOnlyRun(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	svc.kill = nil
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	is(t, "")(nil, os.Remove(filepath.Join(runDir, verdictRenderFile)))
	is(t, "")(nil, os.Remove(filepath.Join(runDir, verdictFile)))
	before, err := os.ReadFile(filepath.Join(runDir, homeChangesFile))
	is(t, "")(nil, err)
	is(t, "")(nil, svc.refreshHomeChangesVerdict("run_s1"))
	after, err := os.ReadFile(filepath.Join(runDir, homeChangesFile))
	is(t, "")(nil, err)
	if string(after) != string(before) {
		t.Fatalf("markdown-only older run changed home report: before=%q after=%q", before, after)
	}
}

func TestRefreshHomeChangesKeepsPublishedCommentMedia(t *testing.T) {
	svc, runDir, _ := askSvc(t, true)
	svc.kill = nil
	checks, err := svc.store.Checks("run_s1")
	is(t, "")(nil, err)
	checks[2].State, checks[2].Attribution, checks[2].Note = CheckFail, "pr", "The model menu crashes on the PR build."
	whitePNG(t, checks[2].Receipts[0].AfterPath, 80, 60)
	checks[2].Receipts[0].ThemeShots = []ThemeShot{{Theme: "light", Path: checks[2].Receipts[0].AfterPath}}
	baseShot := filepath.Join(runDir, "receipts", "base-model.png")
	whitePNG(t, baseShot, 80, 60)
	checks = append(checks, Check{ID: "base-model", Name: checks[2].Name, Leg: "base", State: CheckPass,
		Receipts: []Receipt{{Leg: "base", AfterPath: baseShot, ThemeShots: []ThemeShot{{Theme: "light", Path: baseShot}}}}})
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	v, err := svc.Submit(ctx, "s1", "The model menu failed.", "")
	is(t, "")(nil, err)
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	writeFile(t, checkClipPath(runDir, checks[2].ID), "mp4")
	_, err = svc.draftComments(run, v)
	is(t, "")(nil, err)
	imageURL, clipURL := "https://example.test/published/image.png", "https://example.test/published/check.mp4"
	_, state, err := svc.publishCommentSet(ctx, run, v, evidence{
		bugImages:  map[string]string{checks[2].Name: imageURL},
		checkClips: map[string]string{checks[2].Name: clipURL},
	}, "")
	is(t, "")(nil, err)
	if state != "posted" {
		t.Fatalf("comment state=%s", state)
	}
	_, err = svc.update(run.ID, func(r *Run) { r.State, r.CommentState = RunSubmitted, state })
	is(t, "")(nil, err)
	before, err := os.ReadFile(filepath.Join(runDir, "comment.md"))
	is(t, "")(nil, err)
	is(t, "")(nil, svc.refreshHomeChangesVerdict(run.ID))
	after, err := os.ReadFile(filepath.Join(runDir, "comment.md"))
	is(t, "")(nil, err)
	if string(after) != string(before) || !strings.Contains(string(after), imageURL) || !strings.Contains(string(after), clipURL) {
		t.Fatalf("refresh changed published comment media:\nbefore=%s\nafter=%s", before, after)
	}
	is(t, "")(nil, os.Remove(filepath.Join(runDir, commentMediaFile)))
	if err := svc.refreshHomeChangesVerdict(run.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing published media inputs error=%v", err)
	}
}
