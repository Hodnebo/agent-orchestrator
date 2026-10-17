package litmus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeadlineClosePersistsTheBudgetBoundaryBeforeFinalization(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	preparedAt := run.CreatedAt.Add(3 * time.Minute)
	run.Deadline = preparedAt.Add(RunBudget)
	run.TestingEndedAt = time.Time{}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	deadline := run.Deadline
	svc.clock = func() time.Time { return deadline.Add(9 * time.Minute) }
	svc.WithActivity(func(context.Context, string) (string, time.Time, bool) {
		return "active", time.Time{}, true
	})

	if !svc.closeAtDeadline(context.Background(), run, quiet) {
		t.Fatal("deadline close did not finish")
	}
	saved, err := svc.store.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.TestingEndedAt.Equal(deadline) {
		t.Fatalf("testing end = %v, want deadline %v", saved.TestingEndedAt, deadline)
	}
	md, err := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "testing 25m0s of a 25m budget") {
		t.Fatalf("deadline verdict used finalization time as testing time:\n%s", md)
	}
}

func TestTestingEndMarkerRoundTripsAndSeparatesFinalization(t *testing.T) {
	created := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	ended := created.Add(28 * time.Minute)
	serialized := Run{ID: "run_timing", CreatedAt: created, Deadline: created.Add(3*time.Minute + RunBudget), TestingEndedAt: ended}
	store := NewStore(t.TempDir())
	if err := store.Save(serialized); err != nil {
		t.Fatal(err)
	}
	run, err := store.Get(serialized.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.TestingEndedAt.Equal(ended) {
		t.Fatalf("testing end = %v, want %v", run.TestingEndedAt, ended)
	}
	if err := store.Save(Run{ID: "run_legacy"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.root, "run_legacy", "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "testingEndedAt") {
		t.Fatalf("zero testing end leaked into JSON: %s", raw)
	}
	builds, testingWindow := run.buildsAndTesting(ended.Add(4 * time.Second))
	if builds != 3*time.Minute || testingWindow != 25*time.Minute {
		t.Fatalf("timing = builds %s, testing %s; want 3m and 25m", builds, testingWindow)
	}
	if got := max(ended.Add(4*time.Second).Sub(run.testingEndedAt(ended.Add(4*time.Second))), time.Duration(0)); got != 4*time.Second {
		t.Fatalf("finalization = %s, want 4s", got)
	}
}

func TestOrdinarySubmitTimingExcludesLaterFinalization(t *testing.T) {
	svc, now := newSvc(t)
	run, err := svc.Intake(context.Background(), "s1", prURL)
	if err != nil {
		t.Fatal(err)
	}
	preparedAt := run.CreatedAt.Add(3 * time.Minute)
	*now = preparedAt
	run.State = RunRunning
	run.Deadline = preparedAt.Add(RunBudget)
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	v, err := svc.SubmitBlocked(context.Background(), "s1", "manual stop")
	if err != nil {
		t.Fatal(err)
	}
	*now = preparedAt.Add(8 * time.Minute)
	saved, err := svc.store.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	md := renderVerdict(saved, v, false, nil, filepath.Join(svc.store.root, run.ID), *now, nil)
	if !strings.Contains(md, "testing 0s of a 25m budget, finalizing 8m0s") {
		t.Fatalf("submit timing included finalization in testing window:\n%s", md)
	}
}

func TestRejectedSubmitDoesNotRecordTestingEnd(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	svc.runner = &videoProbeRunner{duration: "70"}
	if err := os.WriteFile(recordingPath(runDir, "head"), []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.update("run_s1", func(run *Run) { run.Legs[0].RecordStartedAt = svc.clock() }); err != nil {
		t.Fatal(err)
	}
	clock := svc.clock()
	svc.clock = func() time.Time { return clock }
	checks, err := svc.store.Checks("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	checks[0].Note = "![missing shot](receipts/missing.png)"
	if err := svc.store.SaveChecks("run_s1", checks); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, "s1", "[missing evidence](scratch/missing.txt#detail)", ""); err == nil {
		t.Fatal("submit with broken links unexpectedly succeeded")
	}
	rejected, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	if !rejected.TestingEndedAt.IsZero() {
		t.Fatalf("rejected submit recorded testing end %v", rejected.TestingEndedAt)
	}

	checks[0].Note = "![shot](receipts/1-after.png)"
	if err := svc.store.SaveChecks("run_s1", checks); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(3 * time.Minute)
	if _, err := svc.Submit(ctx, "s1", "[shot](receipts/1-after.png#detail)", ""); err != nil {
		t.Fatal(err)
	}
	accepted, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.TestingEndedAt.Equal(clock) {
		t.Fatalf("accepted submit testing end = %v, want later clock %v", accepted.TestingEndedAt, clock)
	}
}

func TestLegacyClosedRunDoesNotInventTestingEnd(t *testing.T) {
	created := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	run := Run{
		ID:        "run_legacy",
		State:     RunSubmitted,
		CreatedAt: created,
		Deadline:  created.Add(3*time.Minute + RunBudget),
	}
	md := renderVerdict(run, Verdict{Overall: CheckBlocked}, false, nil, t.TempDir(), created.Add(40*time.Minute), nil)
	if !strings.Contains(md, "testing time unavailable (end not recorded)") || strings.Contains(md, "testing 37m") {
		t.Fatalf("legacy timing invented a testing window:\n%s", md)
	}
}

func TestVerdictTimingGolden(t *testing.T) {
	created := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	run := Run{
		ID:             "run_timing",
		Repo:           "o/r",
		PRNumber:       7,
		CreatedAt:      created,
		Deadline:       created.Add(3*time.Minute + RunBudget),
		TestingEndedAt: created.Add(3*time.Minute + RunBudget),
	}
	md := renderVerdict(run, Verdict{Overall: CheckBlocked}, false, nil, t.TempDir(), created.Add(3*time.Minute+RunBudget+4*time.Second), nil)
	line := ""
	for _, candidate := range strings.Split(md, "\n") {
		if strings.HasPrefix(candidate, "Duration: ") {
			line = candidate
			break
		}
	}
	want, err := os.ReadFile(filepath.Join("testdata", "verdict-timing.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	if line != strings.TrimSpace(string(want)) {
		t.Fatalf("timing line = %q, want %q", line, strings.TrimSpace(string(want)))
	}
}
