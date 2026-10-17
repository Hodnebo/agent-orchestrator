package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestKillLegWaitsForGroupAndKeepsDataAndOwnership(t *testing.T) {
	_, _, _, dir := driveSvc(t)
	started := time.Unix(100, 0)
	owned := Owned{RunID: "run_s1", Leg: "head", State: "running", PGID: Proc{101, started}, Daemon: Proc{102, started}, Electron: Proc{103, started}, DaemonPort: 3200, DebugPort: 9400}
	legDir := filepath.Join(dir, "head")
	for _, sub := range []string{"electron", "data"} {
		is(t, "")(nil, os.MkdirAll(filepath.Join(legDir, sub), 0o750))
		is(t, "")(nil, os.WriteFile(filepath.Join(legDir, sub, "keep"), []byte(sub), 0o600))
	}
	is(t, "")(nil, writeOwned(legDir, owned))
	f := &fakeSys{started: map[int]time.Time{101: started, 102: started, 103: started, 104: started}, cmds: map[int]string{101: "wrapper", 102: "daemon", 103: "Electron", 104: "group child without leg argv"}, groups: map[int][]int{101: {104}}, busy: 2}
	sys := f.ops()
	killGroup := sys.killGroup
	sys.killGroup = func(pid int) {
		killGroup(pid)
		// SIGKILL was sent, but a group child has not exited yet.
		f.started[104], f.cmds[104] = started, "group child without leg argv"
	}
	polls := 0
	sys.procs = func() (map[int]string, error) {
		polls++
		got, err := readOwned(legDir)
		if err != nil || !reflect.DeepEqual(got, owned) {
			t.Fatalf("ownership changed during confirmation: %+v %v", got, err)
		}
		if polls >= 3 {
			delete(f.started, 104)
			delete(f.cmds, 104)
		}
		return f.cmds, nil
	}
	if err := killLeg(ctx, sys, legDir); err != nil {
		t.Fatal(err)
	}
	if polls < 3 {
		t.Fatal("kill returned before group child exited")
	}
	if got, want := strings.Join(f.log, "|"), "killGroup 101|kill 102|rm 102|kill 103|rm 103"; got != want {
		t.Fatalf("kill order=%s want %s", got, want)
	}
	if got, err := readOwned(legDir); err != nil || !reflect.DeepEqual(got, owned) {
		t.Fatalf("ownership changed before relaunch: %+v %v", got, err)
	}
	for _, sub := range []string{"electron", "data"} {
		if data, err := os.ReadFile(filepath.Join(legDir, sub, "keep")); err != nil || string(data) != sub {
			t.Fatalf("%s was wiped: %s %v", sub, data, err)
		}
	}
}

func TestKillLegUnconfirmedProcessAndPortAbort(t *testing.T) {
	for _, what := range []string{"process", "port"} {
		t.Run(what, func(t *testing.T) {
			dir := t.TempDir()
			start := time.Unix(100, 0)
			owned := Owned{State: "running", PGID: Proc{101, start}, DaemonPort: 3200}
			is(t, "")(nil, writeOwned(dir, owned))
			is(t, "")(nil, os.Mkdir(filepath.Join(dir, "electron"), 0o750))
			f := &fakeSys{started: map[int]time.Time{101: start}, cmds: map[int]string{101: "wrapper"}}
			sys := f.ops()
			if what == "process" {
				sys.killGroup = func(int) {}
			} else {
				sys.portBusy = func(int) (bool, error) { return true, nil }
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			err := killLeg(canceled, sys, dir)
			if err == nil || !strings.Contains(err.Error(), "leg death unconfirmed") {
				t.Fatalf("kill=%v", err)
			}
			got, _ := readOwned(dir)
			if !reflect.DeepEqual(got, owned) {
				t.Fatalf("ownership changed: %+v", got)
			}
			if _, err := os.Stat(filepath.Join(dir, "electron")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Relaunch uses a stopped shell fixture, never Electron or the AO app.
func relaunchFixture(t *testing.T) (*Service, *fakeSys, string, *Engine) {
	t.Helper()
	svc, engine, _, dir := driveSvc(t)
	engine.out["tab"] = "→ [t1] App - http://litmus.test\n"
	oldClient := httpClient
	httpClient = &http.Client{Transport: seedReadyTransport{}}
	t.Cleanup(func() { httpClient = oldClient })
	root := t.TempDir()
	is(t, "")(nil, writeManifest(root, Manifest{SHA: "test-sha"}))
	script := filepath.Join(root, "fixture.sh")
	is(t, "")(nil, os.WriteFile(script, []byte("kill -STOP $$\n"), 0o600))
	recipe := Recipe{Kind: "cli", Isolation: true, Run: Step{Cmd: "sh", Args: []string{script}}, Ready: Ready{PageTargets: 1}}
	data, err := yaml.Marshal(recipe)
	is(t, "")(nil, err)
	path := svc.store.recipePath("o/r")
	is(t, "")(nil, os.MkdirAll(filepath.Dir(path), 0o750))
	is(t, "")(nil, os.WriteFile(path, data, 0o600))
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	run.Repo = "o/r"
	run.Legs[0].Worktree = root
	run.Legs[0].PID = 101
	run.Legs[0].DaemonPort = 3200
	run.Legs[0].Control = &LegControl{State: "lost", Since: 1, Cause: "app_unresponsive", Detail: "original loss"}
	is(t, "")(nil, svc.store.Save(run))
	old := svc.engineFor(run.ID, run.Legs[0])
	old.once.Do(func() {})
	old.ierr = errors.New("sticky failure")
	legDir := filepath.Join(dir, "head")
	start := time.Unix(100, 0)
	is(t, "")(nil, writeOwned(legDir, Owned{RunID: run.ID, Leg: "head", State: "running", PGID: Proc{101, start}, Electron: Proc{103, start}, DaemonPort: 3200, DebugPort: 9400}))
	for _, sub := range []string{"electron", "data"} {
		is(t, "")(nil, os.MkdirAll(filepath.Join(legDir, sub), 0o750))
		is(t, "")(nil, os.WriteFile(filepath.Join(legDir, sub, "keep"), []byte(sub), 0o600))
	}
	is(t, "")(nil, os.WriteFile(filepath.Join(legDir, "native-dialogs.jsonl"), []byte("old dialog\n"), 0o600))
	f := &fakeSys{started: map[int]time.Time{101: start, 103: start}, cmds: map[int]string{101: "old wrapper", 103: "old Electron"}}
	sys := f.ops()
	killOld := sys.killGroup
	sys.killGroup = func(pid int) {
		if old.life.Err() == nil {
			t.Error("process killed before engine closed")
		}
		if pid == 101 {
			killOld(pid)
		} else {
			sigkillGroup(pid)
		}
	}
	fakeStart := sys.startTime
	sys.startTime = func(pid int) (time.Time, bool) {
		if pid == 101 || pid == 103 {
			return fakeStart(pid)
		}
		return time.Unix(200, 0), pid > 0
	}
	sys.procs = func() (map[int]string, error) {
		owned, err := readOwned(legDir)
		if err != nil {
			return nil, err
		}
		if len(f.started) > 0 && owned.PGID.PID != 101 {
			t.Errorf("owned.json overwritten before death: %+v", owned)
		}
		out := map[int]string{}
		for pid, cmd := range f.cmds {
			out[pid] = cmd
		}
		if owned.PGID.PID != 101 {
			out[owned.PGID.PID] = "fixture Electron --remote-debugging-port=" + strconv.Itoa(owned.DebugPort)
		}
		return out, nil
	}
	svc.sys = sys
	svc.sys.freePorts = func(n int) ([]int, error) {
		if n != 2 {
			t.Fatalf("requested %d ports", n)
		}
		if len(f.started) != 0 {
			t.Fatal("allocating ports before old process death")
		}
		return []int{3201, 9401}, nil
	}
	t.Cleanup(func() {
		o, _ := readOwned(legDir)
		if o.PGID.PID != 101 && o.PGID.PID > 0 {
			sigkillGroup(o.PGID.PID)
		}
	})
	return svc, f, legDir, old
}

func TestRelaunchKeepsStateReplacesEngineAndLogs(t *testing.T) {
	svc, f, dir, old := relaunchFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(svc.ownerHome, ".zshrc"), "fixture owner rc\n")
	writeFile(t, filepath.Join(svc.ownerHome, ".codex", "auth.json"), "fixture credentials")
	run, err := svc.Relaunch(ctx, "s1", "head")
	is(t, "")(nil, err)
	assertLegHomeUsesOwner(t, dir, svc.ownerHome)
	if len(f.started) != 0 || old.life.Err() == nil {
		t.Fatalf("old leg survived: %+v", f.started)
	}
	got, err := readOwned(dir)
	is(t, "")(nil, err)
	if got.PGID.PID == 101 || got.State != "running" || run.Legs[0].DebugPort != got.DebugPort || run.Legs[0].DaemonPort != got.DaemonPort {
		t.Fatalf("new ownership=%+v leg=%+v", got, run.Legs[0])
	}
	fresh := svc.engineFor(run.ID, run.Legs[0])
	if fresh == old || fresh.ierr != nil || fresh.life.Err() != nil || fresh.Namespace == old.Namespace {
		t.Fatalf("fresh engine=%+v", fresh)
	}
	if run.Legs[0].Control.State != "lost" {
		t.Fatal("readiness alone declared control recovered")
	}
	for _, sub := range []string{"electron", "data"} {
		if data, err := os.ReadFile(filepath.Join(dir, sub, "keep")); err != nil || string(data) != sub {
			t.Fatalf("lost %s: %s %v", sub, data, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "native-dialogs.jsonl.previous")); err != nil || string(data) != "old dialog\n" {
		t.Fatalf("rotation=%q %v", data, err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "actions.jsonl"))
	is(t, "")(nil, err)
	var rec actionRec
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	is(t, "")(nil, json.Unmarshal([]byte(lines[len(lines)-1]), &rec))
	if rec.Action != "relaunch" || rec.Leg != "head" || rec.Reason != "original loss" || rec.Error != "" {
		t.Fatalf("relaunch log=%+v", rec)
	}
	fresh.identity = fakeDaemonIdentity
	_, err = svc.Drive(ctx, "s1", "snapshot", nil)
	is(t, "")(nil, err)
	run, err = svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if run.Legs[0].Control.State != "ok" {
		t.Fatalf("control unrecovered: %+v", run.Legs[0].Control)
	}
}

func TestRelaunchWhileControlIsOKKeepsControlAndAuditsReason(t *testing.T) {
	svc, _, dir, _ := relaunchFixture(t)
	if _, err := svc.update("run_s1", func(run *Run) {
		run.Legs[0].Control = &LegControl{State: "ok"}
	}); err != nil {
		t.Fatal(err)
	}

	run, err := svc.Relaunch(ctx, "s1", "head")
	if err != nil {
		t.Fatal(err)
	}
	if control := run.Legs[0].Control; control == nil || control.State != "ok" || control.Cause != "" {
		t.Fatalf("planned relaunch changed control: %+v", control)
	}
	if len(run.Gaps) != 0 {
		t.Fatalf("planned relaunch added gaps: %q", run.Gaps)
	}

	data, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var relaunch actionRec
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec actionRec
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Action == "relaunch" {
			relaunch = rec
		}
		if rec.Action == "control" && rec.Control != nil && rec.Control.Cause == "browser_control_unusable" {
			t.Fatalf("planned relaunch recorded control loss: %+v", rec.Control)
		}
	}
	if relaunch.Action != "relaunch" || !relaunch.Planned || relaunch.Reason != plannedRelaunchReason || relaunch.Error != "" {
		t.Fatalf("planned relaunch audit=%+v", relaunch)
	}
}

func TestResumedTargetResetsStaleTeardownBeforeStartingTarget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		relaunch bool
	}{
		{name: "relaunch", relaunch: true},
		{name: "launch", relaunch: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, dir, _ := relaunchFixture(t)
			run, err := svc.store.Get("run_s1")
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(svc.ownerHome, ".zshrc"), "before resumed target\n")
			if err := svc.snapshotIntakeHome(run.ID); err != nil {
				t.Fatal(err)
			}
			if err := svc.store.WriteFile(run.ID, homeChangesFile, []byte(homeChangesReport("No owner home changes detected.\n"))); err != nil {
				t.Fatal(err)
			}
			_, err = svc.update(run.ID, func(run *Run) {
				run.TeardownComplete = true
				if !tc.relaunch {
					run.Legs[0].State = "prepared"
					run.Legs[0].PID = 0
					run.Legs[0].PIDStart = time.Time{}
					run.Legs[0].DaemonPort = 0
					run.Legs[0].Engine = nil
					run.Legs[0].Control = nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.relaunch {
				for _, pid := range []int{101, 103} {
					delete(fake.started, pid)
					delete(fake.cmds, pid)
				}
				if err := writeOwned(dir, Owned{RunID: run.ID, Leg: "head", State: "cleaned"}); err != nil {
					t.Fatal(err)
				}
			}

			if tc.relaunch {
				run, err = svc.Relaunch(ctx, "s1", "head")
			} else {
				run, err = svc.Launch(ctx, "s1", "head")
			}
			if err != nil {
				t.Fatal(err)
			}
			stored, err := svc.store.Get(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.TeardownComplete {
				t.Fatal("resumed target left stale teardown marker complete")
			}
			report, err := svc.store.HomeChanges(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(report, pendingHomeReportSuffix) {
				t.Fatalf("resumed target did not mark home report pending: %q", report)
			}

			changed := filepath.Join(svc.ownerHome, ".zshrc")
			writeFile(t, changed, "after resumed target\n")
			// The fixture keeps the replacement shell in its fake process table. Use
			// the teardown boundary directly once the resumed target is running.
			svc.sys = (&fakeSys{}).ops()
			closeCalls := 0
			svc.closeEngine = func(string) { closeCalls++ }
			if err := svc.teardown(ctx, run); err != nil {
				t.Fatal(err)
			}
			if closeCalls != 1 {
				t.Fatalf("later teardown close calls=%d, want 1", closeCalls)
			}
			stored, err = svc.store.Get(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !stored.TeardownComplete {
				t.Fatal("later teardown did not complete")
			}
			report, err = svc.store.HomeChanges(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(report, "MODIFIED") || !strings.Contains(report, changed) {
				t.Fatalf("later teardown omitted owner-home mutation: %q", report)
			}
		})
	}
}

func TestPlannedRelaunchFailureMarksControlLost(t *testing.T) {
	svc, _, _, _ := relaunchFixture(t)
	if _, err := svc.update("run_s1", func(run *Run) {
		run.Legs[0].Control = &LegControl{State: "ok"}
	}); err != nil {
		t.Fatal(err)
	}
	svc.sys.freePorts = func(int) ([]int, error) {
		return nil, errors.New("planned relaunch launch failure")
	}

	_, err := svc.Relaunch(ctx, "s1", "head")
	if err == nil || !strings.Contains(err.Error(), "planned relaunch launch failure") {
		t.Fatalf("planned relaunch error=%v", err)
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	control := run.Legs[0].Control
	if control == nil || control.State != "lost" || control.Cause != "browser_control_unusable" || !strings.Contains(control.Detail, "after relaunch") {
		t.Fatalf("failed planned relaunch control=%+v", control)
	}
}

func TestPlannedRelaunchVideoLineNamesJumpActions(t *testing.T) {
	svc, run, runDir, start := recordedRelaunchFixture(t)
	if _, err := svc.update(run.ID, func(cur *Run) {
		cur.Legs[0].Control = &LegControl{State: "ok"}
	}); err != nil {
		t.Fatal(err)
	}
	fakeRelaunchRecording(t, svc, 5*time.Second)
	svc.clock = func() time.Time { return start.Add(100 * time.Second) }

	run, err := svc.Relaunch(ctx, "s1", "head")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Gaps) != 0 {
		t.Fatalf("planned relaunch added gaps: %q", run.Gaps)
	}
	run = svc.stopRecording(ctx, run)
	line := videoLine(svc.finishVideo(ctx, runDir, run))
	if !strings.Contains(line, "video jumps at actions 1") || strings.Count(line, "video jumps at actions") != 1 {
		t.Fatalf("planned relaunch video line=%q", line)
	}
}

func TestVideoLineListsMultiplePlannedRelaunchJumpsOnce(t *testing.T) {
	got := videoLine([]legVideo{{leg: "head", raw: true, jumpsAt: []int{53, 91}}})
	want := "head: recorded (head.mp4); video jumps at actions 53, 91"
	if got != want {
		t.Fatalf("video line=%q, want %q", got, want)
	}
}

func TestRelaunchFailureIsAudited(t *testing.T) {
	svc, _, dir, _ := relaunchFixture(t)
	svc.sys.portBusy = func(int) (bool, error) { return false, errors.New("port probe failed") }
	_, relaunchErr := svc.Relaunch(ctx, "s1", "head")
	is(t, "TESTING_RELAUNCH_FAILED")(nil, relaunchErr)
	if !strings.Contains(relaunchErr.Error(), "port probe failed") {
		t.Fatalf("failure=%v", relaunchErr)
	}
	data, readErr := os.ReadFile(filepath.Join(filepath.Dir(dir), "actions.jsonl"))
	is(t, "")(nil, readErr)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var rec actionRec
	is(t, "")(nil, json.Unmarshal([]byte(lines[len(lines)-1]), &rec))
	if rec.Action != "relaunch" || rec.Leg != "head" || rec.Reason != "kill leg" || rec.Error != relaunchErr.Error() {
		t.Fatalf("failed relaunch audit=%+v", rec)
	}
}

func TestRelaunchSyntheticControlDetailHasNoCLIHint(t *testing.T) {
	svc, _, _, _ := relaunchFixture(t)
	_, err := svc.update("run_s1", func(run *Run) { run.Legs[0].Control = nil })
	is(t, "")(nil, err)
	run, err := svc.Relaunch(ctx, "s1", "head")
	is(t, "")(nil, err)
	if detail := run.Legs[0].Control.Detail; strings.Contains(detail, "ao testing") {
		t.Fatalf("synthetic control detail contains CLI hint: %q", detail)
	}
}

func TestRelaunchActionLogFailureIsReturned(t *testing.T) {
	svc, _, dir, _ := relaunchFixture(t)
	actions := filepath.Join(filepath.Dir(dir), "actions.jsonl")
	is(t, "")(nil, os.Mkdir(actions, 0o700))
	_, err := svc.Relaunch(ctx, "s1", "head")
	if err == nil || !strings.Contains(err.Error(), "record relaunch") || !strings.Contains(err.Error(), "actions.jsonl") {
		t.Fatalf("append failure was hidden: %v", err)
	}
}

func TestRelaunchRotatesNativeDialogLogsWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native-dialogs.jsonl")
	is(t, "")(nil, os.WriteFile(path, []byte("old dialog\n"), 0o600))
	is(t, "")(nil, rotateNativeDialogLog(path))
	is(t, "")(nil, os.WriteFile(path, []byte("new dialog\n"), 0o600))
	is(t, "")(nil, rotateNativeDialogLog(path))
	if data, err := os.ReadFile(path + ".previous"); err != nil || string(data) != "new dialog\n" {
		t.Fatalf("latest rotation=%q %v", data, err)
	}
	if data, err := os.ReadFile(path + ".previous-1"); err != nil || string(data) != "old dialog\n" {
		t.Fatalf("earlier rotation=%q %v", data, err)
	}
	is(t, "")(nil, os.WriteFile(path, []byte("third dialog\n"), 0o600))
	is(t, "")(nil, rotateNativeDialogLog(path))
	for suffix, want := range map[string]string{".previous": "third dialog\n", ".previous-1": "old dialog\n", ".previous-2": "new dialog\n"} {
		if data, err := os.ReadFile(path + suffix); err != nil || string(data) != want {
			t.Fatalf("rotation %s = %q, %v; want %q", suffix, data, err, want)
		}
	}
}

func TestRelaunchUnconfirmedKillStartsNothing(t *testing.T) {
	svc, _, dir, old := relaunchFixture(t)
	svc.sys.killGroup = func(int) {}
	// A canceled wait ends confirmation immediately without changing the bound.
	prior := portWait
	portWait = 0
	t.Cleanup(func() { portWait = prior })
	before, err := os.ReadFile(filepath.Join(dir, "owned.json"))
	is(t, "")(nil, err)
	_, err = svc.Relaunch(ctx, "s1", "head")
	is(t, "TESTING_RELAUNCH_FAILED")(nil, err)
	if !strings.Contains(err.Error(), "leg death unconfirmed") {
		t.Fatalf("failure=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "owned.json"))
	is(t, "")(nil, err)
	if string(before) != string(after) {
		t.Fatal("unconfirmed kill replaced ownership")
	}
	if svc.engines["run_s1/head"] != old {
		t.Fatal("new engine started")
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "dev.log")); !os.IsNotExist(err) {
		t.Fatalf("launch started: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "native-dialogs.jsonl.previous")); !os.IsNotExist(err) {
		t.Fatalf("log rotated before death: %v", err)
	}
}

func TestRelaunchRefusesNeverLaunchedClosedAndInvalidLeg(t *testing.T) {
	svc, _ := newSvc(t)
	run := Run{ID: "run_s1", SessionID: "s1", State: RunPrepared, Deadline: time.Now().Add(RunBudget), Legs: []Leg{{Name: "head", State: "prepared"}}}
	is(t, "")(nil, svc.store.Save(run))
	is(t, "TESTING_LEG_INVALID")(svc.Relaunch(ctx, "s1", "other"))
	is(t, "TESTING_LEG_NOT_LAUNCHED")(svc.Relaunch(ctx, "s1", "head"))
	run.State = RunSubmitted
	is(t, "")(nil, svc.store.Save(run))
	is(t, "TESTING_RUN_CLOSED")(svc.Relaunch(ctx, "s1", "head"))
}

func TestKillLegRefusesUnknownGroupIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listed  bool
		members []int
	}{
		{name: "listed leader with unreadable start", listed: true},
		{name: "leaderless group without ownership evidence", members: []int{104}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			start := time.Unix(100, 0)
			is(t, "")(nil, writeOwned(dir, Owned{State: "running", PGID: Proc{101, start}}))
			f := &fakeSys{started: map[int]time.Time{}, cmds: map[int]string{}, groups: map[int][]int{101: tc.members}}
			if tc.listed {
				f.cmds[101] = "unverified process"
			}
			for _, pid := range tc.members {
				f.started[pid] = start
				f.cmds[pid] = "unverified child"
			}
			err := killLeg(ctx, f.ops(), dir)
			if err == nil || !strings.Contains(err.Error(), "cannot verify") || len(f.log) != 0 {
				t.Fatalf("unverified group signaled: %v %v", err, f.log)
			}
		})
	}
}

func TestRelaunchKillFailureKeepsRecordingGap(t *testing.T) {
	svc, _, _, _ := relaunchFixture(t)
	_, err := svc.update("run_s1", func(run *Run) { run.Legs[0].RecordStartedAt = time.Unix(1, 0) })
	is(t, "")(nil, err)
	svc.sys.killGroup = func(int) {}
	oldWait := portWait
	portWait = 0
	t.Cleanup(func() { portWait = oldWait })
	is(t, "TESTING_RELAUNCH_FAILED")(svc.Relaunch(ctx, "s1", "head"))
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	if !strings.Contains(strings.Join(run.Gaps, "\n"), "recording stopped at relaunch") {
		t.Fatalf("lost recording was hidden: %q", run.Gaps)
	}
}

func TestRelaunchUnconfirmedEngineCloseStartsNothing(t *testing.T) {
	svc, f, dir, old := relaunchFixture(t)
	runtimeDir := filepath.Join(old.SocketDir, "namespaces", old.Namespace, "run")
	is(t, "")(nil, os.MkdirAll(runtimeDir, 0o700))
	pidFile := filepath.Join(runtimeDir, old.Session+".pid")
	is(t, "")(nil, os.WriteFile(pidFile, []byte("77"), 0o600))
	f.cmds[77] = "/bin/unrelated"
	before, err := os.ReadFile(filepath.Join(dir, "owned.json"))
	is(t, "")(nil, err)
	_, err = svc.Relaunch(ctx, "s1", "head")
	is(t, "TESTING_RELAUNCH_FAILED")(nil, err)
	if !strings.Contains(err.Error(), "close engine") || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("unconfirmed engine close=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "owned.json"))
	is(t, "")(nil, err)
	if string(after) != string(before) || svc.engines["run_s1/head"] != old || len(f.log) != 0 {
		t.Fatalf("unconfirmed engine was replaced: %v", f.log)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("old engine ownership lost: %v", err)
	}
}

func TestRelaunchWaitsForLatestControlBeforeReplacement(t *testing.T) {
	svc, _, dir, old := relaunchFixture(t)
	old.invocations.Add(1)
	go func() {
		<-old.life.Done()
		err := svc.setControl("run_s1", "head", LegControl{State: "ok"}, old)
		if err != nil {
			t.Error(err)
		}
		old.invocations.Done()
	}()
	run, err := svc.Relaunch(ctx, "s1", "head")
	is(t, "")(nil, err)
	if control := run.Legs[0].Control; control == nil || control.State != "lost" {
		t.Fatalf("fresh engine inherited recovered control: %+v", control)
	}
	actions, err := setupLog(filepath.Dir(dir))
	is(t, "")(nil, err)
	found := false
	for _, action := range actions {
		if action.Action == "relaunch" {
			found = true
			if action.Reason != "original loss" {
				t.Fatalf("real-loss relaunch was logged as planned: %+v", action)
			}
		}
	}
	if !found {
		t.Fatal("relaunch action missing")
	}
}

func TestRelaunchDoesNotReopenExpiredRun(t *testing.T) {
	svc, _, _, _ := relaunchFixture(t)
	allocate := svc.sys.freePorts
	svc.sys.freePorts = func(n int) ([]int, error) {
		_, err := svc.update("run_s1", func(run *Run) { run.State = RunExpired })
		if err != nil {
			return nil, err
		}
		return allocate(n)
	}
	run, err := svc.Relaunch(ctx, "s1", "head")
	is(t, "")(nil, err)
	if run.State != RunExpired {
		t.Fatalf("relaunch reopened run as %s", run.State)
	}
}

func TestKillLegKeepsUnknownProcessesBrowserFiles(t *testing.T) {
	dir := t.TempDir()
	is(t, "")(nil, writeOwned(dir, Owned{State: "running", Daemon: Proc{PID: 102, Start: time.Unix(1, 0)}}))
	f := &fakeSys{cmds: map[int]string{102: "unverified process"}}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err := killLeg(canceled, f.ops(), dir)
	if err == nil || !strings.Contains(err.Error(), "death unconfirmed") || len(f.log) != 0 {
		t.Fatalf("unverified process files removed: %v %v", err, f.log)
	}
}

func TestLaunchCannotBypassFailedRelaunchOwnership(t *testing.T) {
	svc, f, dir, old := relaunchFixture(t)
	_, err := svc.update("run_s1", func(run *Run) { run.Legs[0].State = "failed" })
	is(t, "")(nil, err)
	before, err := os.ReadFile(filepath.Join(dir, "owned.json"))
	is(t, "")(nil, err)
	_, err = svc.Launch(ctx, "s1", "head")
	is(t, "TESTING_LEG_RUNNING")(nil, err)
	if !strings.Contains(err.Error(), "use: ao testing relaunch head") {
		t.Fatalf("launch guidance=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "owned.json"))
	is(t, "")(nil, err)
	if string(after) != string(before) || old.life.Err() != nil || len(f.log) != 0 {
		t.Fatalf("launch replaced uncleaned leg: %v", f.log)
	}
	is(t, "")(nil, os.WriteFile(filepath.Join(dir, "owned.json"), []byte("{invalid"), 0o600))
	_, err = svc.Launch(ctx, "s1", "head")
	is(t, "TESTING_LAUNCH_FAILED")(nil, err)
	if !strings.Contains(err.Error(), "cannot verify prior leg ownership") {
		t.Fatalf("corrupt ownership=%v", err)
	}
}

func TestKillLegPortProbeErrorAborts(t *testing.T) {
	dir := t.TempDir()
	owned := Owned{State: "running", DaemonPort: 3200}
	is(t, "")(nil, writeOwned(dir, owned))
	sys := (&fakeSys{}).ops()
	sys.portBusy = func(int) (bool, error) { return false, errors.New("port probe failed") }
	err := killLeg(ctx, sys, dir)
	if err == nil || !strings.Contains(err.Error(), "confirm leg port 3200 free: port probe failed") {
		t.Fatalf("unverified port treated as free: %v", err)
	}
	got, err := readOwned(dir)
	is(t, "")(nil, err)
	if !reflect.DeepEqual(got, owned) {
		t.Fatalf("ownership replaced: %+v", got)
	}
}
