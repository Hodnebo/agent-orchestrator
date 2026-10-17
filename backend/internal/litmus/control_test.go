package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlDiagnosisOwnedElectron(t *testing.T) {
	for _, tc := range []struct {
		name        string
		started     map[int]time.Time
		cmds        map[int]string
		cause, text string
	}{
		{name: "exited", cause: "app_exited", text: "the head app exited (pid 41)"},
		{name: "recycled", started: map[int]time.Time{41: time.Unix(20, 0)}, cmds: map[int]string{41: "other"}, cause: "app_exited", text: "the head app exited (pid 41)"},
		{name: "unresponsive", started: map[int]time.Time{41: time.Unix(10, 0)}, cmds: map[int]string{41: "Electron"}, cause: "app_unresponsive", text: "the head app is not responding (main process alive, no native dialog open)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newSvc(t)
			svc.sys = (&fakeSys{started: tc.started, cmds: tc.cmds}).ops()
			dir := svc.store.legDir("run_s1", "head")
			is(t, "")(nil, os.MkdirAll(dir, 0o750))
			is(t, "")(nil, writeOwned(dir, Owned{Electron: Proc{PID: 41, Start: time.Unix(10, 0)}}))
			got, err := svc.controlDiagnosis(ctx, "run_s1", "head")
			if err != nil || got.State != "lost" || got.Cause != tc.cause || got.Detail != tc.text {
				t.Fatalf("diagnosis=%+v err=%v", got, err)
			}
		})
	}
}

func TestDriveControlLossAndFirstSuccessfulCommand(t *testing.T) {
	svc, f, _, dir := driveSvc(t)
	f.out["tab"] = ""
	_, err := svc.Drive(ctx, "s1", "snapshot", nil)
	if err == nil || !strings.Contains(err.Error(), "the head app is not responding") || strings.Contains(err.Error(), errLegDown.Error()) {
		t.Fatalf("diagnosis error=%v", err)
	}
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	control := run.Legs[0].Control
	if control == nil || control.State != "lost" || control.Cause != "app_unresponsive" || control.Since < 1 {
		t.Fatalf("lost=%+v", control)
	}
	_, _ = svc.Drive(ctx, "s1", "snapshot", nil)
	again, _ := svc.store.Get("run_s1")
	if again.Legs[0].Control.Since != control.Since {
		t.Fatal("repeated diagnosis moved loss boundary")
	}
	delete(f.out, "tab")
	_, err = svc.Drive(ctx, "s1", "snapshot", nil)
	is(t, "")(nil, err)
	run, err = svc.store.Get("run_s1")
	is(t, "")(nil, err)
	if run.Legs[0].Control.State != "ok" {
		t.Fatalf("recovery=%+v", run.Legs[0].Control)
	}
	data, err := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
	is(t, "")(nil, err)
	var events []actionRec
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec actionRec
		is(t, "")(nil, json.Unmarshal([]byte(line), &rec))
		if rec.Action == "control" {
			events = append(events, rec)
		}
	}
	if len(events) != 2 || events[0].Control.State != "lost" || events[0].Seq != control.Since || events[1].Control.State != "ok" {
		t.Fatalf("events=%+v", events)
	}
}

func TestEngineStickyErrorLosesControl(t *testing.T) {
	svc, _, _, _ := driveSvc(t)
	_, initErr := svc.Drive(ctx, "s1", "snapshot", nil)
	is(t, "")(nil, initErr)
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	e := svc.engineFor(run.ID, run.Legs[0])
	e.ierr = errors.New("owned daemon cannot be verified")
	_, err = svc.Drive(context.Background(), "s1", "snapshot", nil)
	if err == nil || !strings.Contains(err.Error(), "owned daemon cannot be verified") || !strings.Contains(err.Error(), "ao testing relaunch head") {
		t.Fatalf("sticky error=%v", err)
	}
	run, err = svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if c := run.Legs[0].Control; c == nil || c.State != "lost" || c.Cause != "browser_control_unusable" || strings.Contains(c.Detail, "ao testing") {
		t.Fatalf("sticky control=%+v", c)
	}
}

func TestDriveBatchStopsAfterControlLoss(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	f.out["tab"] = ""
	res, err := svc.Drive(ctx, "s1", "batch", []string{"snapshot", "press Enter"})
	if err == nil || !isLegDown(err) || len(res["results"].([]map[string]any)) != 1 || f.find("press") != nil {
		t.Fatalf("batch continued after control loss: %v %v %v", err, res, f.calls)
	}
}

func TestControlCommandAndCloseShareBusyBoundary(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	_, _, done, err := svc.beginLegCommand("s1", "head")
	is(t, "")(nil, err)
	is(t, "TESTING_RUN_BUSY")(svc.Submit(ctx, "s1", "", ""))
	is(t, "TESTING_RUN_BUSY")(svc.Cleanup(ctx, "s1"))
	_, recoverDone, err := svc.begin("s1", "relaunch head")
	is(t, "")(nil, err)
	recoverDone()
	done()
	_, closeDone, err := svc.begin("s1", "submit")
	is(t, "")(nil, err)
	_, err = svc.Drive(ctx, "s1", "snapshot", nil)
	is(t, "TESTING_RUN_BUSY")(nil, err)
	closeDone()
	if len(f.calls) != 0 {
		t.Fatalf("command ran during close: %v", f.calls)
	}
}

func TestControlLossKeepsCauseAndHintsOutOfPublicArtifacts(t *testing.T) {
	svc, engine, _, runDir := driveSvc(t)
	svc.media = &fakeMedia{}
	engine.fail["tab"] = errors.New("page unavailable")
	run, err := svc.update("run_s1", func(run *Run) {
		run.Repo, run.PRNumber, run.Evidence = "o/r", 7, EvidenceOff
	})
	is(t, "")(nil, err)
	leg := svc.startRecording(ctx, run.ID, run.Legs[0])
	if !leg.RecordStartedAt.IsZero() {
		t.Fatal("missing page started recording")
	}
	_, err = svc.Drive(ctx, "s1", "snapshot", nil)
	if err == nil || !errors.Is(err, engine.fail["tab"]) || !isLegDown(err) || !strings.Contains(err.Error(), "agent-browser find the leg's app tab: page unavailable") || !strings.Contains(err.Error(), "ao testing relaunch head") {
		t.Fatalf("tester error lost cause or relaunch hint: %v", err)
	}
	run, err = svc.store.Get(run.ID)
	is(t, "")(nil, err)
	checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: CheckSkipped, Note: "The app did not respond."}}
	v, err := svc.closeRun(ctx, run, checks, "", "", "")
	is(t, "")(nil, err)
	run, err = svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if len(run.Gaps) < 1 || run.Gaps[0] != "The PR build's recording was not available." {
		t.Fatalf("gap exposed a private recording error: %q", run.Gaps)
	}
	public := append(append([]string{run.Legs[0].Control.Detail}, run.Gaps...), v.Reasons...)
	for _, name := range []string{"verdict.md", "comment.md"} {
		data, err := os.ReadFile(filepath.Join(runDir, name))
		is(t, "")(nil, err)
		public = append(public, string(data))
	}
	for _, text := range public {
		if strings.Contains(text, "ao testing") {
			t.Fatalf("CLI hint reached public text: %s", text)
		}
	}
}

func TestDriveControlReceiptReasonsStayOutOfPublicArtifacts(t *testing.T) {
	for _, command := range []string{"press", "wait", "screenshot", "errors", "console"} {
		t.Run(command, func(t *testing.T) {
			svc, engine, _, runDir := driveSvc(t)
			svc.media = &fakeMedia{}
			run, err := svc.update("run_s1", func(run *Run) {
				run.Repo, run.PRNumber, run.Evidence = "o/r", 7, EvidenceOff
			})
			is(t, "")(nil, err)
			is(t, "")(svc.CheckAdd(ctx, "s1", "menu opens", "list shown", "", "", "", CheckClassification{Purpose: "explore"}))
			is(t, "")(svc.CheckStart(ctx, "s1", "c1", CheckClassification{Purpose: "explore"}))
			// Skip the before-shot so screenshot failure happens during receipt capture.
			svc.engineFor(run.ID, run.Legs[0]).last = filepath.Join(runDir, "before.png")
			cause := errors.Join(errLegDown, errors.New("receipt-only cause"))
			engine.fail[command] = cause
			res, err := svc.Drive(ctx, "s1", "press", []string{"Enter"})
			if err == nil || !errors.Is(err, cause) {
				t.Fatalf("drive lost the cause: %v", err)
			}
			rec, ok := res["receipt"].(actionRec)
			if !ok || rec.Reason != err.Error() || !strings.Contains(rec.Reason, "the head app is not responding") || !strings.Contains(rec.Reason, "receipt-only cause") || !strings.Contains(rec.Reason, "ao testing relaunch head") {
				t.Fatalf("receipt %+v differs from tester error %v", res, err)
			}
			if text, ok := res["text"].(string); !ok || !strings.Contains(text, rec.Reason) {
				t.Fatalf("tester text lost receipt reason: %v", res)
			}
			checks, err := svc.store.Checks(run.ID)
			is(t, "")(nil, err)
			if len(checks) != 1 || len(checks[0].Receipts) != 1 || checks[0].Receipts[0].Reason != rec.Reason {
				t.Fatalf("checks.json lost receipt reason: %+v", checks)
			}
			actions := readActions(runDir)
			if len(actions) == 0 || actions[len(actions)-1].Reason != rec.Reason {
				t.Fatalf("actions.jsonl lost receipt reason: %+v", actions)
			}
			is(t, "")(svc.CheckEnd(ctx, "s1", "c1", CheckSkipped, "The app did not respond.", false, 0))
			checks, err = svc.store.Checks(run.ID)
			is(t, "")(nil, err)
			run, err = svc.store.Get(run.ID)
			is(t, "")(nil, err)
			v, err := svc.closeRun(ctx, run, checks, "", "", "")
			is(t, "")(nil, err)
			run, err = svc.store.Get(run.ID)
			is(t, "")(nil, err)
			public := append(append([]string{}, run.Gaps...), v.Reasons...)
			for _, name := range []string{"verdict.md", "comment.md"} {
				data, err := os.ReadFile(filepath.Join(runDir, name))
				is(t, "")(nil, err)
				public = append(public, string(data))
			}
			for _, text := range public {
				if strings.Contains(text, "ao testing") || strings.Contains(text, "receipt-only cause") {
					t.Fatalf("receipt reason reached public text: %s", text)
				}
			}
		})
	}
}
