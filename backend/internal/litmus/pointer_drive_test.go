package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func pointerTestPage(t *testing.T, width, height float64, methods *[]string) func(context.Context) (*pageCDP, error) {
	t.Helper()
	return func(context.Context) (*pageCDP, error) {
		return &pageCDP{request: func(_ context.Context, method string, params, out any) error {
			*methods = append(*methods, method)
			var reply any
			switch method {
			case "Runtime.evaluate":
				if params.(map[string]any)["expression"] != "[innerWidth, innerHeight]" {
					t.Fatalf("unexpected runtime expression: %v", params)
				}
				reply = map[string]any{"result": map[string]any{"value": []float64{width, height}}}
			case "Input.dispatchMouseEvent":
				reply = map[string]any{"dispatched": true}
			default:
				return errors.New("unexpected CDP method: " + method)
			}
			data, _ := json.Marshal(reply)
			return json.Unmarshal(data, out)
		}}, nil
	}
}

func TestPointerDriveRecordsPointForAllElementActions(t *testing.T) {
	for _, action := range []string{"click", "dblclick", "hover", "tap", "check", "uncheck"} {
		t.Run(action, func(t *testing.T) {
			svc, f, _, _ := driveSvc(t)
			f.out["get"] = `{"x":10,"y":20,"width":100,"height":40}`
			run, err := svc.store.Get("run_s1")
			if err != nil {
				t.Fatal(err)
			}
			e := svc.engineFor(run.ID, run.Legs[0])
			e.last = "before.png"
			e.refs = map[string]refInfo{"@e1": {role: "button", name: "Continue"}}
			var methods []string
			e.connectPage = pointerTestPage(t, 1280, 720, &methods)

			res, err := svc.Drive(context.Background(), "s1", action, []string{"e1"})
			if err != nil {
				t.Fatal(err)
			}
			rec, ok := res["receipt"].(actionRec)
			if !ok {
				t.Fatalf("receipt type = %T", res["receipt"])
			}
			want := &clickAt{X: 60, Y: 40, W: 1280, H: 720}
			if !reflect.DeepEqual(rec.Click, want) {
				t.Fatalf("click point = %+v, want %+v", rec.Click, want)
			}
			if rec.SentAt.IsZero() {
				t.Fatal("SentAt is zero after dispatch")
			}
			if !slices.Contains(methods, "Runtime.evaluate") {
				t.Fatalf("viewport lookup missing: %v", methods)
			}
			if f.find(action) == nil {
				t.Fatalf("%s was not dispatched: %v", action, f.calls)
			}
		})
	}
}

func TestPointerDriveDoesNotMeasureKeyboardOrScroll(t *testing.T) {
	for _, tc := range []struct {
		action string
		args   []string
	}{
		{action: "press", args: []string{"Enter"}},
		{action: "scroll", args: []string{"400"}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			svc, f, _, _ := driveSvc(t)
			run, err := svc.store.Get("run_s1")
			if err != nil {
				t.Fatal(err)
			}
			e := svc.engineFor(run.ID, run.Legs[0])
			e.last = "before.png"
			res, err := svc.Drive(context.Background(), "s1", tc.action, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			rec := res["receipt"].(actionRec)
			if rec.Click != nil {
				t.Fatalf("%s moved the cursor: %+v", tc.action, rec.Click)
			}
			if f.find("get") != nil {
				t.Fatalf("%s measured an element box: %v", tc.action, f.calls)
			}
		})
	}
}

func TestEngineRunBeforeSendIsAfterProbes(t *testing.T) {
	f := &fakeEngine{fail: map[string]error{}, out: map[string]string{}}
	var events []string
	f.hook = func(args []string) { events = append(events, "invoke:"+args[0]) }
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)
	if _, err := e.runBeforeSend(context.Background(), time.Second, func() { events = append(events, "before-send") }, "click", "@e1"); err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[len(events)-2] != "before-send" || events[len(events)-1] != "invoke:click" {
		t.Fatalf("dispatch boundary events = %v", events)
	}
}

func TestDriveRecordsSentAtBeforeReceipt(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	base := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC).Add(time.Nanosecond)
	var tick int
	svc.clock = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Nanosecond)
	}
	var actionSeen bool
	f.hook = func(args []string) {
		if len(args) > 0 && args[0] == "press" {
			actionSeen = true
		}
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	e.last = "before.png"
	res, err := svc.Drive(context.Background(), "s1", "press", []string{"Enter"})
	if err != nil {
		t.Fatal(err)
	}
	rec := res["receipt"].(actionRec)
	if !actionSeen || rec.SentAt.IsZero() || !rec.SentAt.Before(rec.At) {
		t.Fatalf("dispatch and receipt times = actionSeen %t sentAt %s at %s", actionSeen, rec.SentAt, rec.At)
	}
	if got := readActions(e.RunDir); len(got) != 1 || got[0].SentAt.IsZero() {
		t.Fatalf("actions log lost SentAt: %+v", got)
	}
}

func TestActionSentCapturesRecordingOffsetPerLegAcrossWallClockJump(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	base := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	wall := []time.Time{base.Add(10 * time.Second), base.Add(5 * time.Second)}
	var clockCalls int
	svc.clock = func() time.Time {
		at := wall[min(clockCalls, len(wall)-1)]
		clockCalls++
		return at
	}
	svc.recorders = map[string]*screencast{
		"run_s1/head": {elapsed: func() time.Duration { return 1500 * time.Millisecond }},
		"run_s1/base": {elapsed: func() time.Duration { return 2300 * time.Millisecond }},
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	var headStamp actionTime
	if _, err := e.runBeforeSend(context.Background(), time.Second, func() { headStamp = svc.actionSent(run.ID, "head") }, "press", "Enter"); err != nil {
		t.Fatal(err)
	}
	baseStamp := svc.actionSent(run.ID, "base")
	if headStamp.recordOffsetMS == nil || *headStamp.recordOffsetMS != 1500 || baseStamp.recordOffsetMS == nil || *baseStamp.recordOffsetMS != 2300 {
		t.Fatalf("recording offsets head=%v base=%v", headStamp.recordOffsetMS, baseStamp.recordOffsetMS)
	}
	if !baseStamp.sentAt.Before(headStamp.sentAt) {
		t.Fatalf("wall clock did not move backward: head=%s base=%s", headStamp.sentAt, baseStamp.sentAt)
	}
	rec := actionRec{Leg: "head", Action: "press", SentAt: headStamp.sentAt, RecordOffsetMS: headStamp.recordOffsetMS}
	data, err := json.Marshal(rec)
	if err != nil || !strings.Contains(string(data), `"recordOffsetMs":1500`) {
		t.Fatalf("action record did not retain offset: %s, %v", data, err)
	}
	if f.find("press") == nil {
		t.Fatalf("dispatch did not run: %v", f.calls)
	}
}

func TestActionSentDoesNotWaitForFrameDiskIO(t *testing.T) {
	svc, _, _, _ := driveSvc(t)
	r := &screencast{elapsed: func() time.Duration { return 1500 * time.Millisecond }}
	svc.recorders = map[string]*screencast{"run_s1/head": r}
	r.mu.Lock()
	defer r.mu.Unlock()
	done := make(chan actionTime, 1)
	go func() { done <- svc.actionSent("run_s1", "head") }()
	select {
	case stamp := <-done:
		if stamp.recordOffsetMS == nil || *stamp.recordOffsetMS != 1500 {
			t.Fatalf("lost recording offset: %+v", stamp)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch clock blocked on the frame writer")
	}
}

func TestDrivePointerPointFailureKeepsSentAtWithoutClick(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	f.fail["get"] = errors.New("box unavailable")
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	e.last = "before.png"
	e.refs = map[string]refInfo{"@e1": {role: "button", name: "Continue"}}
	res, err := svc.Drive(context.Background(), "s1", "click", []string{"e1"})
	if err != nil {
		t.Fatal(err)
	}
	rec := res["receipt"].(actionRec)
	if rec.Click != nil || rec.SentAt.IsZero() {
		t.Fatalf("point failure receipt = %+v, want click nil and SentAt set", rec)
	}
}

func TestDriveTabDriftKeepsPointNilAndSentAtZero(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	svc.recorders = map[string]*screencast{"run_s1/head": {elapsed: func() time.Duration { return 1500 * time.Millisecond }}}
	f.out["tab"] = "→ [t1] App - http://app.test/\n  [t2] about:blank - about:blank\n"
	var tabLists int
	f.hook = func(args []string) {
		if len(args) == 2 && args[0] == "tab" && args[1] == "list" {
			tabLists++
			if tabLists == 2 {
				f.out["tab"] = "  [t1] App - http://app.test/\n→ [t2] about:blank - about:blank\n"
			}
		}
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	e.last = "before.png"
	e.refs = map[string]refInfo{"@e1": {role: "button", name: "Continue"}}
	res, err := svc.Drive(context.Background(), "s1", "click", []string{"e1"})
	if err == nil || !strings.Contains(err.Error(), errTabDrift.Error()) {
		t.Fatalf("tab drift error = %v", err)
	}
	rec := res["receipt"].(actionRec)
	if rec.Click != nil || !rec.SentAt.IsZero() {
		t.Fatalf("tab drift receipt = %+v, want click nil and SentAt zero", rec)
	}
	if rec.RecordOffsetMS == nil || *rec.RecordOffsetMS != 1500 {
		t.Fatalf("tab drift lost recording offset: %+v", rec)
	}
}

func TestRawCDPMouseDispatchRecordsPointButWheelDoesNotMove(t *testing.T) {
	svc, f, _, _ := driveSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	e.last = "before.png"
	var methods []string
	e.connectPage = pointerTestPage(t, 1440, 900, &methods)

	res, err := svc.Drive(context.Background(), "s1", "cdp", []string{"Input.dispatchMouseEvent", `{"type":"mousePressed","x":321.5,"y":123.25}`})
	if err != nil {
		t.Fatal(err)
	}
	rec := res["receipt"].(actionRec)
	if want := (&clickAt{X: 321.5, Y: 123.25, W: 1440, H: 900}); !reflect.DeepEqual(rec.Click, want) || rec.SentAt.IsZero() {
		t.Fatalf("mouse point receipt = %+v, want %+v with SentAt", rec, want)
	}
	if !reflect.DeepEqual(methods, []string{"Runtime.evaluate", "Input.dispatchMouseEvent"}) {
		t.Fatalf("mouse dispatch methods = %v", methods)
	}

	methods = nil
	res, err = svc.Drive(context.Background(), "s1", "cdp", []string{"Input.dispatchMouseEvent", `{"type":"mouseWheel","x":321.5,"y":123.25,"deltaY":400}`})
	if err != nil {
		t.Fatal(err)
	}
	rec = res["receipt"].(actionRec)
	if rec.Click != nil || rec.SentAt.IsZero() {
		t.Fatalf("mouse wheel receipt = %+v, want click nil and SentAt set", rec)
	}
	if !reflect.DeepEqual(methods, []string{"Input.dispatchMouseEvent"}) {
		t.Fatalf("mouse wheel methods = %v", methods)
	}
	if f.find("cdp") != nil {
		t.Fatalf("raw CDP unexpectedly went through agent-browser: %v", f.calls)
	}
}

func TestRawCDPViewportLegDownRefusesDispatchWithoutPoint(t *testing.T) {
	svc, _, _, _ := driveSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	e.last = "before.png"
	var methods []string
	e.connectPage = func(context.Context) (*pageCDP, error) {
		return &pageCDP{request: func(_ context.Context, method string, _ any, _ any) error {
			methods = append(methods, method)
			if method == "Runtime.evaluate" {
				return errLegDown
			}
			return errors.New("dispatch should not run after viewport leg down")
		}}, nil
	}

	res, err := svc.Drive(context.Background(), "s1", "cdp", []string{"Input.dispatchMouseEvent", `{"type":"mousePressed","x":3,"y":4}`})
	if err == nil || !strings.Contains(err.Error(), errLegDown.Error()) {
		t.Fatalf("viewport leg-down error = %v", err)
	}
	rec := res["receipt"].(actionRec)
	if rec.Click != nil || !rec.SentAt.IsZero() {
		t.Fatalf("viewport leg-down receipt = %+v, want no point and no send timestamp", rec)
	}
	if !reflect.DeepEqual(methods, []string{"Runtime.evaluate"}) {
		t.Fatalf("viewport leg-down methods = %v", methods)
	}
}
