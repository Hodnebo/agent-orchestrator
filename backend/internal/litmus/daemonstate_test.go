package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestDaemonIdentityReadsPIDAndStartTime(t *testing.T) {
	e := newEngine(EngineInfo{SocketDir: t.TempDir(), Namespace: "head", Session: "head"}, nil)
	pidPath := filepath.Join(e.SocketDir, "namespaces", e.Namespace, "run", "head.pid")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	fs := &fakeSys{started: map[int]time.Time{41: started, 42: started.Add(time.Minute)}}
	startTime := func(pid int) (time.Time, error) {
		if when, ok := fs.started[pid]; ok {
			return when, nil
		}
		return time.Time{}, os.ErrProcessDone
	}
	for _, tc := range []struct {
		name, pid string
		want      daemonIdentity
		wantErr   bool
	}{
		{"original", "41", daemonIdentity{41, started}, false},
		{"replacement PID", "42", daemonIdentity{42, started.Add(time.Minute)}, false},
		{"dead PID", "43", daemonIdentity{}, true},
		{"invalid PID", "bad", daemonIdentity{}, true},
		{"invalid zero", "0", daemonIdentity{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(pidPath, []byte(tc.pid), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := e.readDaemonIdentity(startTime)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("identity=%v error=%v, want %v error=%t", got, err, tc.want, tc.wantErr)
			}
		})
	}
	if err := os.Remove(pidPath); err != nil {
		t.Fatal(err)
	}
	if _, err := e.readDaemonIdentity(startTime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing PID file: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte("41"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.readDaemonIdentity(func(int) (time.Time, error) { return time.Time{}, errors.New("process inspection failed") })
	if err == nil || errors.Is(err, os.ErrProcessDone) || !strings.Contains(err.Error(), "process inspection failed") {
		t.Fatalf("unknown process probe reported as an exit: %v", err)
	}
}

func TestEngineReplacementDuringRestartWaitsForAnExplicitRequest(t *testing.T) {
	f := &fakeEngine{}
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)
	identity, _ := fakeDaemonIdentity()
	e.identity = func() (daemonIdentity, error) { return identity, nil }
	if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
		t.Fatal(err)
	}
	identity.pid++
	changed := false
	restarts := 0
	e.onRestart = func(string) { restarts++ }
	f.hook = func(cmd []string) {
		if !changed && slices.Equal(cmd, []string{"tab", "t1", "--pin-tab"}) {
			identity.pid++
			changed = true
		}
	}
	f.calls = nil
	_, err := e.Run(context.Background(), time.Second, "fill", "#name", "value")
	if err == nil || !strings.Contains(err.Error(), "restart failed") || restarts != 1 || f.find("fill") != nil {
		t.Fatalf("recovery recursed or sent action: error=%v restarts=%d calls=%v", err, restarts, f.calls)
	}
	if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil || restarts != 1 || !e.ready || e.daemon != identity {
		t.Fatalf("explicit request could not initialize the replacement: error=%v restarts=%d identity=%v", err, restarts, e.daemon)
	}
}

func TestEngineDetectsDaemonLossBeforeAndDuringCommands(t *testing.T) {
	for _, phase := range []string{"idle exit", "crash", "new PID", "reused PID", "tab probe", "command", "initial pin"} {
		t.Run(phase, func(t *testing.T) {
			f := &fakeEngine{}
			e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)
			original := daemonIdentity{pid: 41, start: time.Unix(1, 0)}
			current := original
			var identityErr error
			e.identity = func() (daemonIdentity, error) { return current, identityErr }
			restarts := 0
			e.onRestart = func(string) { restarts++ }
			f.hook = func(cmd []string) {
				if phase == "initial pin" && slices.Equal(cmd, []string{"tab", "t1", "--pin-tab"}) {
					current.pid = 42
				}
			}
			if _, err := e.Run(context.Background(), time.Second, "snapshot"); phase == "initial pin" {
				if err == nil || !strings.Contains(err.Error(), "browser session was restarted") || f.find("snapshot") != nil || restarts != 1 {
					t.Fatalf("initialization lost identity: error=%v calls=%v restarts=%d", err, f.calls, restarts)
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			// A stable daemon must retain its pin and tester setup.
			f.calls = nil
			if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil || hasEngineReviewCall(f.calls, "stream", "disable") {
				t.Fatalf("stable daemon reinitialized: error=%v calls=%v", err, f.calls)
			}
			f.calls = nil
			e.last, e.refs = "before.png", map[string]refInfo{"@e1": {name: "old"}}
			switch phase {
			case "idle exit":
				identityErr = os.ErrNotExist
			case "crash":
				identityErr = os.ErrProcessDone
			case "new PID":
				current.pid = 42
			case "reused PID":
				current.start = time.Unix(2, 0)
			}
			f.hook = func(cmd []string) {
				if slices.Equal(cmd, []string{"stream", "disable"}) {
					identityErr, current.pid = nil, 42
				}
				if phase == "tab probe" && slices.Equal(cmd, []string{"tab", "list"}) || phase == "command" && cmd[0] == "fill" {
					current.pid = 42
				}
			}
			sent := 0
			_, err := e.runBeforeSend(context.Background(), time.Second, func() { sent++ }, "fill", "#name", "value")
			wantSent := 0
			if phase == "command" {
				wantSent = 1
			}
			if err == nil || !strings.Contains(err.Error(), "browser session was restarted") || restarts != 1 || sent != wantSent || !e.ready || e.tab != "t1" || e.last != "" || e.refs != nil {
				t.Fatalf("error=%v restarts=%d sent=%d ready=%t tab=%s last=%s refs=%v calls=%v", err, restarts, sent, e.ready, e.tab, e.last, e.refs, f.calls)
			}
			if !hasEngineReviewCall(f.calls, "stream", "disable") || !hasEngineReviewCall(f.calls, "tab", "t1", "--pin-tab") {
				t.Fatalf("daemon setup not restored: %v", f.calls)
			}
			if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
				t.Fatalf("next explicit command: %v", err)
			}
		})
	}
}

func TestDriveDaemonLossListsOnlyThisLegsRecordedSetup(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	identity, _ := fakeDaemonIdentity()
	e.identity = func() (daemonIdentity, error) { return identity, nil }
	for _, cmd := range [][]string{{"set", "media", "dark"}, {"route", "**/api", "--body", "private"}, {"record", "start", "tester.webm"}, {"trace", "start"}, {"trace", "stop", "trace.zip"}} {
		if _, err := svc.Drive(context.Background(), "s1", cmd[0], cmd[1:]); err != nil {
			t.Fatalf("setup %v: %v", cmd, err)
		}
	}
	f.calls = nil
	identity.pid++
	res, err := svc.Drive(context.Background(), "s1", "press", []string{"Enter"})
	if err == nil {
		t.Fatal("state loss did not refuse new action")
	}
	for _, want := range []string{"was NOT sent", "set media, route, record", "element refs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q: %v", want, err)
		}
	}
	for _, unwanted := range []string{"private", "timezone", "credentials", "record, trace"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("unrecorded or secret setup %q in %v", unwanted, err)
		}
	}
	if f.find("press") != nil || !res["receipt"].(actionRec).SentAt.IsZero() {
		t.Fatalf("new action was sent: calls=%v result=%v", f.calls, res)
	}
}

func TestDriveRetainedRestartNoticeSaysNewActionWasNotSent(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	e.restartNote = "browser session was restarted. " + lostDaemonState
	f.calls = nil
	_, err = svc.Drive(context.Background(), "s1", "press", []string{"Enter"})
	if err == nil || !strings.Contains(err.Error(), "the new action was NOT sent") || len(f.calls) != 0 || e.restartNote != "" {
		t.Fatalf("notice=%v calls=%v retained=%s", err, f.calls, e.restartNote)
	}
}

func TestEngineUnverifiableAbortExplainsPermanentLossAndNextSteps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeEngine{}
		e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)
		e.abort = func() error { return errors.New("cannot verify owned agent-browser daemon PID and start time") }
		e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
			if slices.Equal(args[2:], []string{"stream", "status"}) {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return f.exec(ctx, bin, args, env)
		}
		_, first := e.Run(context.Background(), time.Second, "hang")
		f.calls = nil
		_, next := e.Run(context.Background(), time.Second, "snapshot")
		for _, err := range []error{first, next} {
			for _, want := range []string{"cannot verify", "The s leg", "unusable for the rest of this run", "skipped with a reason", "submit"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "other leg") {
				t.Errorf("single-leg abort mentions another leg: %v", err)
			}
		}
		if len(f.calls) != 0 {
			t.Fatalf("unverified daemon received more commands: %v", f.calls)
		}
	})
}

func TestEngineUnknownIdentityRefusesWithoutKillingOrSending(t *testing.T) {
	f := &fakeEngine{}
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)
	e.identity = fakeDaemonIdentity
	if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	e.identity = func() (daemonIdentity, error) { return daemonIdentity{}, errors.New("invalid daemon PID") }
	e.abort = func() error { t.Fatal("killed an unverified PID"); return nil }
	_, err := e.Run(context.Background(), time.Second, "fill", "#name", "value")
	if err == nil || !strings.Contains(err.Error(), "cannot verify initialized") || !strings.Contains(err.Error(), "was NOT sent") || len(f.calls) != 0 {
		t.Fatalf("error=%v calls=%v", err, f.calls)
	}
}

func TestDriveStateLossIncludesSetupAwaitingItsReceipt(t *testing.T) {
	for _, phase := range []string{"command interrupted", "after-shot interrupted"} {
		t.Run(phase, func(t *testing.T) {
			svc, f, _, _ := driveSvc(t)
			run, err := svc.store.Get("run_s1")
			if err != nil {
				t.Fatal(err)
			}
			e := svc.engineFor(run.ID, run.Legs[0])
			e.last = "before.png"
			identity, _ := fakeDaemonIdentity()
			e.identity = func() (daemonIdentity, error) { return identity, nil }
			changed := false
			f.hook = func(cmd []string) {
				if !changed && (phase == "command interrupted" && cmd[0] == "set" || phase == "after-shot interrupted" && cmd[0] == "screenshot") {
					identity.pid++
					changed = true
				}
			}
			_, err = svc.Drive(context.Background(), "s1", "set", []string{"media", "dark"})
			want := "since initialization: set media."
			if phase == "command interrupted" {
				want = "may have been lost after setup commands failed: set media."
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("current setup missing from state-loss notice: %v", err)
			}
			// The old command's receipt is written after recovery initialized the
			// replacement. It must not become setup owned by that replacement.
			identity.pid++
			_, err = svc.Drive(context.Background(), "s1", "snapshot", nil)
			if err == nil || strings.Contains(err.Error(), "set media") || !strings.Contains(err.Error(), "No successful set, route, record, or trace setup remains") {
				t.Fatalf("old setup leaked into the replacement's generation: %v", err)
			}
		})
	}
}

func TestLoggedDaemonStateHonorsInitializationAndLegAndReportsUnknowns(t *testing.T) {
	dir := t.TempDir()
	sent := time.Unix(1, 0)
	actions := []actionRec{
		{Seq: 1, Leg: "head", Action: "set", Args: []string{"timezone", "UTC"}, SentAt: sent, EngineGeneration: 1},
		{Seq: 2, Leg: "base", Action: "set", Args: []string{"locale", "fr"}, SentAt: sent, EngineGeneration: 1},
		{Seq: 3, Leg: "head", Action: "set", Args: []string{"headers", "secret"}, SentAt: sent, EngineGeneration: 1},
		{Seq: 4, Leg: "head", Action: "trace", Args: []string{"start"}, SentAt: sent, EngineGeneration: 1},
		{Seq: 5, Leg: "head", Action: "trace", Args: []string{"stop"}, SentAt: sent, EngineGeneration: 1},
	}
	write := func() {
		t.Helper()
		f, err := os.Create(filepath.Join(dir, "actions.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		for _, a := range actions {
			if err := json.NewEncoder(f).Encode(a); err != nil {
				t.Fatal(err)
			}
		}
	}
	write()
	got := loggedDaemonState(dir, "head", 1, 1, nil)
	if !strings.Contains(got, "since initialization: set headers.") || strings.Contains(got, "timezone") || strings.Contains(got, "locale") || strings.Contains(got, "secret") || strings.Contains(got, "headers, trace") {
		t.Fatalf("wrong setup inventory: %s", got)
	}
	actions = append(actions,
		actionRec{Seq: 6, Leg: "head", Action: "route", Args: []string{"secret-route-a"}, SentAt: sent, EngineGeneration: 1},
		actionRec{Seq: 7, Leg: "head", Action: "network", Args: []string{"route", "secret-route-b"}, SentAt: sent, EngineGeneration: 1},
		actionRec{Seq: 8, Leg: "head", Action: "unroute", Args: []string{"secret-route-a"}, SentAt: sent, EngineGeneration: 1},
	)
	write()
	if got = loggedDaemonState(dir, "head", 1, 1, nil); !strings.Contains(got, "set headers, route.") || strings.Contains(got, "secret-route") {
		t.Fatalf("remaining route not reported safely: %s", got)
	}
	actions = append(actions,
		actionRec{Seq: 9, Leg: "head", Action: "network", Args: []string{"unroute", "secret-route-b"}, SentAt: sent, EngineGeneration: 1},
		actionRec{Seq: 10, Leg: "head", Action: "set", Args: []string{"geolocation", "0", "0"}, Reason: "the action was NOT sent"},
	)
	write()
	if got = loggedDaemonState(dir, "head", 1, 1, nil); strings.Contains(got, "headers, route") || strings.Contains(got, "geolocation") {
		t.Fatalf("removed routes or refused setup reported as lost: %s", got)
	}
	actions = append(actions, actionRec{Seq: 11, Leg: "head", Action: "set", Args: []string{"media", "dark"}, Reason: "client failed", SentAt: sent, EngineGeneration: 1})
	write()
	if got = loggedDaemonState(dir, "head", 1, 1, nil); !strings.Contains(got, "may have been lost") {
		t.Fatalf("uncertain command reported as known: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "actions.jsonl"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got = loggedDaemonState(dir, "head", 1, 1, nil); !strings.Contains(got, "Could not read") || !strings.Contains(got, "may have been lost") {
		t.Fatalf("log failure hidden: %s", got)
	}
}
