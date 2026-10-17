package litmus

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type seedCallRecorder struct {
	calls  []string
	envs   [][]string
	output string
	err    error
}

func (r *seedCallRecorder) Run(_ context.Context, _ string, env []string, name string, args ...string) ([]byte, error) {
	if name != "sh" || len(args) != 2 || args[0] != "-c" {
		return nil, errors.New("unexpected command in seed recorder")
	}
	r.calls = append(r.calls, args[1])
	r.envs = append(r.envs, slices.Clone(env))
	return []byte(r.output), r.err
}

func useSeedReadyTransport(t *testing.T) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: seedReadyTransport{}}
	t.Cleanup(func() { httpClient = old })
}

func saveSeedRecipe(t *testing.T, svc *Service, steps []SeedStep) Recipe {
	t.Helper()
	recipe, err := svc.store.LoadRecipe("o/r")
	if err != nil {
		t.Fatal(err)
	}
	recipe.Seed = steps
	data, err := yaml.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	path := svc.store.recipePath("o/r")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return recipe
}

type seedFixtureProcesses struct {
	alive     map[int]bool
	pids      []int
	killed    []int
	startedAt time.Time
}

func seedFixtureSys(processes *seedFixtureProcesses) sysOps {
	// Process metadata and port probes stay deterministic; killGroup still signals the real fixture shell.
	startedAt := time.Unix(100, 0)
	processes.startedAt = startedAt
	processes.alive = map[int]bool{}
	return sysOps{
		startTime: func(pid int) (time.Time, bool) {
			if _, known := processes.alive[pid]; !known {
				processes.alive[pid] = true
				processes.pids = append(processes.pids, pid)
			}
			return processes.startedAt, processes.alive[pid]
		},
		procs: func() (map[int]string, error) { return map[int]string{}, nil },
		groupPIDs: func(pgid int) ([]int, error) {
			if processes.alive[pgid] {
				return []int{pgid}, nil
			}
			return nil, nil
		},
		killGroup: func(pgid int) {
			sigkillGroup(pgid)
			processes.alive[pgid] = false
			processes.killed = append(processes.killed, pgid)
		},
		kill:      sigkill,
		rmBrowser: func(int) {},
		portBusy:  func(int) (bool, error) { return false, nil },
		freePorts: func(n int) ([]int, error) {
			if n != 2 {
				return nil, errors.New("seed fixture expected two ports")
			}
			return []int{3201, 9401}, nil
		},
	}
}

func registerSeedFixtureCleanup(t *testing.T, svc *Service, runID string) func() error {
	t.Helper()
	dir := svc.store.legDir(runID, "head")
	cleaned := false
	stop := func() error {
		svc.closeEngines(runID)
		if err := cleanLeg(context.Background(), svc.sys, dir); err != nil {
			return err
		}
		cleaned = true
		return nil
	}
	t.Cleanup(func() {
		if !cleaned {
			if err := stop(); err != nil {
				t.Errorf("clean seed fixture leg: %v", err)
			}
		}
	})
	return stop
}

func TestRelaunchKeepsDataSkipsSeedAndAddsNoGap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a stopped POSIX shell")
	}
	svc, _, dir, _ := relaunchFixture(t)
	fakeRelaunchRecording(t, svc, 5*time.Second)
	steps := []SeedStep{{Cmd: "populate sample data"}}
	saveSeedRecipe(t, svc, steps)
	recorder := &seedCallRecorder{}
	svc.runner = recorder

	run, err := svc.Relaunch(ctx, "s1", "head")
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("relaunch ran seed commands: %q", recorder.calls)
	}
	if len(run.Gaps) != 0 {
		t.Fatalf("relaunch added a gap: %q", run.Gaps)
	}
	data, err := os.ReadFile(filepath.Join(dir, "logs", "seed.log"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "relaunch: data dir kept, seed not re-run\n"; got != want {
		t.Fatalf("seed log = %q, want %q", got, want)
	}
}

func newSeedStartFixture(t *testing.T, recorder *seedCallRecorder) (*Service, Recipe, Leg, string, *seedFixtureProcesses) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a stopped POSIX shell")
	}
	useSeedReadyTransport(t)
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	svc.runner = recorder
	processes := &seedFixtureProcesses{}
	svc.sys = seedFixtureSys(processes)
	engine := &fakeEngine{fail: map[string]error{}, out: map[string]string{}}
	svc.engExec, svc.binary = engine.exec, "fake-agent-browser"

	root := t.TempDir()
	if err := writeManifest(root, Manifest{SHA: "test-sha"}); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "fixture.sh")
	if err := os.WriteFile(script, []byte("kill -STOP $$\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recipe := Recipe{Kind: "cli", Isolation: true, Run: Step{Cmd: "sh", Args: []string{script}}, Ready: Ready{PageTargets: 1}, Seed: []SeedStep{{Cmd: "populate sample data"}}}
	path := svc.store.recipePath("o/r")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	encoded, err := yaml.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	leg := Leg{Name: "head", State: "prepared", Worktree: root}
	return svc, recipe, leg, "run_s1", processes
}

func TestInitialSeedGapRemainsSingleGapAfterRelaunch(t *testing.T) {
	recorder := &seedCallRecorder{output: "sample data could not be created", err: errors.New("exit status 1")}
	svc, recipe, leg, runID, processes := newSeedStartFixture(t, recorder)
	stop := registerSeedFixtureCleanup(t, svc, runID)
	dir := svc.store.legDir(runID, leg.Name)

	laps := newLaps()
	var seedGap string
	started, err := svc.startLeg(ctx, runID, 0, recipe, leg, laps, &seedGap, false, "launch")
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "populate sample data" {
		t.Fatalf("initial seed calls = %q", recorder.calls)
	}
	if !slices.ContainsFunc(laps.out, func(s string) bool { return strings.HasPrefix(s, "seed ") }) {
		t.Fatalf("initial launch omitted seed timing lap: %q", laps.out)
	}
	wantGap := "The PR build started without its sample content (seed step 1 failed)."
	if seedGap != wantGap {
		t.Fatalf("initial gap = %q, want %q", seedGap, wantGap)
	}

	run := Run{ID: runID, SessionID: "s1", Repo: "o/r", State: RunRunning, Deadline: time.Now().Add(RunBudget), Legs: []Leg{started}, Gaps: []string{seedGap}}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Relaunch(ctx, "s1", "head")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Gaps) != 1 || got.Gaps[0] != wantGap {
		t.Fatalf("gaps after relaunch = %q, want one original gap %q", got.Gaps, wantGap)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("relaunch re-ran seed commands: %q", recorder.calls)
	}
	data, err := os.ReadFile(filepath.Join(dir, "logs", "seed.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "relaunch: data dir kept, seed not re-run\n") {
		t.Fatalf("seed log did not record skipped seed: %q", data)
	}
	if len(processes.pids) != 2 {
		t.Fatalf("target processes = %v, want initial and relaunched targets", processes.pids)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(processes.killed, processes.pids[1]) {
		t.Fatalf("relaunch target pid %d was not killed during cleanup: %v", processes.pids[1], processes.killed)
	}
}

func TestFreshStartReturnsSeedFailure(t *testing.T) {
	recorder := &seedCallRecorder{output: "sample data could not be created", err: errors.New("exit status 1")}
	svc, recipe, leg, runID, processes := newSeedStartFixture(t, recorder)
	stop := registerSeedFixtureCleanup(t, svc, runID)
	dir := svc.store.legDir(runID, leg.Name)

	_, err := svc.startLeg(ctx, runID, 0, recipe, leg, newLaps(), nil, false, "launch")
	if err == nil || !strings.Contains(err.Error(), "seed step 1 (populate sample data): sample data could not be created") {
		t.Fatalf("fresh start hid seed failure: %v", err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "populate sample data" {
		t.Fatalf("fresh start seed calls = %q", recorder.calls)
	}
	if got := envValue(recorder.envs[0], "AO_DATA_DIR"); got != filepath.Join(dir, "data") {
		t.Fatalf("seed data directory = %q, want %q", got, filepath.Join(dir, "data"))
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if len(processes.killed) != 1 || processes.killed[0] != processes.pids[0] {
		t.Fatalf("failed target process was not killed during cleanup: pids=%v killed=%v", processes.pids, processes.killed)
	}
}

func TestKeptStartSkipsSeedAndKeepsSeedTimingLap(t *testing.T) {
	recorder := &seedCallRecorder{}
	svc, recipe, leg, runID, processes := newSeedStartFixture(t, recorder)
	stop := registerSeedFixtureCleanup(t, svc, runID)
	dir := svc.store.legDir(runID, leg.Name)

	var laps laps
	var seedGap string
	if _, err := svc.startLeg(ctx, runID, 0, recipe, leg, &laps, &seedGap, true, "relaunch"); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 0 || seedGap != "" {
		t.Fatalf("kept start called seed or made a gap: calls=%q gap=%q", recorder.calls, seedGap)
	}
	if !slices.ContainsFunc(laps.out, func(s string) bool { return strings.HasPrefix(s, "seed ") }) {
		t.Fatalf("kept start omitted seed timing lap: %q", laps.out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "logs", "seed.log"))
	if err != nil || string(data) != "relaunch: data dir kept, seed not re-run\n" {
		t.Fatalf("seed log = %q, %v", data, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if len(processes.killed) != 1 || processes.killed[0] != processes.pids[0] {
		t.Fatalf("kept target process was not killed during cleanup: pids=%v killed=%v", processes.pids, processes.killed)
	}
}
