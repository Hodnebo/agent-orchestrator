package litmus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func videoLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &out
}

func TestVideoStepDeadlineAndLabels(t *testing.T) {
	logs := videoLogs(t)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	err := videoStep(parent, "run_budget", "base", "raw-encode", time.Minute, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if ctx.Err() != nil || !ok || time.Until(deadline) < 59*time.Second {
			t.Fatalf("step inherited an exhausted deadline: %v, %v", ctx.Err(), deadline)
		}
		return context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "leg=base step=raw-encode") {
		t.Fatalf("unscoped error: %v", err)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &line); err != nil {
		t.Fatal(err)
	}
	if line["run"] != "run_budget" || line["leg"] != "base" || line["step"] != "raw-encode" || line["duration"] == nil || line["error"] == "" {
		t.Fatalf("incomplete step log: %v", line)
	}
	if err := videoStep(parent, "run_budget", "base", "probe", 0, func(ctx context.Context) error { return ctx.Err() }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("step did not enforce its own deadline: %v", err)
	}
}

func TestFinishVideoCutFailureDoesNotStarveClipsOrBase(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	run.Legs = []Leg{{Name: "head", RecordStartedAt: run.CreatedAt}, {Name: "base", RecordStartedAt: run.CreatedAt}}
	for _, leg := range run.Legs {
		writeFile(t, recordingPath(runDir, leg.Name), "playable")
	}
	svc.runner = &videoProbeRunner{duration: "120"}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	svc.media = pointerMedia{inspect: func(ctx context.Context, args []string) error {
		if ctx.Err() != nil {
			t.Fatalf("later operation inherited the failed cut's context: %v", ctx.Err())
		}
		out := args[len(args)-1]
		calls = append(calls, out)
		if out == cutPath(runDir, "head") {
			for _, id := range []string{"c1", "c3"} {
				if !fileExists(checkClipPath(runDir, id)) {
					t.Fatalf("cut ran before cheap clip %s", id)
				}
			}
			cancel() // the request expires while this slow cut is running
			return context.DeadlineExceeded
		}
		return nil
	}}
	got := svc.finishVideo(parent, runDir, run)
	if len(got) != 2 || !got[0].raw || got[0].cut || !got[1].raw || !got[1].cut || len(got[0].clips) != 2 {
		t.Fatalf("video results after slow cut: %+v, calls %v", got, calls)
	}
	if fileExists(cutPath(runDir, "head")) {
		t.Fatal("failed cut left a publishable output")
	}
}

func TestRawEncodeFailureDoesNotStarveOtherLegAndLogsItsStep(t *testing.T) {
	logs := videoLogs(t)
	svc, _, _, runDir := driveSvc(t)
	meta := recordingFixture(t, svc, "run_s1", true)
	base := filepath.Join(runDir, "base")
	for _, name := range []string{"manifest.json", "frames.jsonl", "frames/000001.jpg", "frames/000002.jpg"} {
		data, err := os.ReadFile(filepath.Join(runDir, "head", name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(base, name), string(data))
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise base first so its failure must not cancel the head encode.
	run.Legs = []Leg{{Name: "base", RecordStartedAt: meta.StartedWall}, {Name: "head", RecordStartedAt: meta.StartedWall}}
	svc.runner = &videoProbeRunner{duration: "70"}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.media = pointerMedia{inspect: func(ctx context.Context, args []string) error {
		if ctx.Err() != nil {
			t.Fatalf("raw encode inherited an expired context: %v", ctx.Err())
		}
		if args[len(args)-1] == recordingPath(runDir, "base") {
			cancel()
			return context.DeadlineExceeded
		}
		return nil
	}}
	svc.stopRecording(parent, run)
	if !fileExists(recordingPath(runDir, "head")) || fileExists(recordingPath(runDir, "base")) || !isRecordingFrameDir(filepath.Join(base, "frames")) {
		t.Fatal("base failure lost another leg's video or removed the failed leg's source frames")
	}
	found := false
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry["run"] == run.ID && entry["leg"] == "base" && entry["step"] == "raw-encode" && strings.Contains(entry["error"].(string), "context deadline exceeded") && entry["duration"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("base raw failure was not logged with its step: %s", logs)
	}
}

func TestVideoBudgetsScaleWithWork(t *testing.T) {
	short := []seg{{from: 0, to: 2 * time.Second}}
	long := []seg{{from: 0, to: 100 * time.Second}}
	many := []seg{{from: 0, to: time.Second}, {from: time.Hour, to: time.Hour + time.Second}}
	if cutEncodeTimeout(long) <= cutEncodeTimeout(short) || cutEncodeTimeout(many) <= cutEncodeTimeout(short) {
		t.Fatal("cut budget must grow with encoded frames and input seeks")
	}
	if clipTimeout(time.Minute) <= clipTimeout(time.Second) || clipTimeout(2*time.Minute) != clipTimeout(time.Minute) {
		t.Fatal("clip budget must scale to its sixty-second work cap")
	}
}

func TestFailedClipDoesNotCancelNextClip(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	source := recordingPath(runDir, "head")
	writeFile(t, source, "playable")
	svc.runner = &videoProbeRunner{duration: "120"}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.media = pointerMedia{inspect: func(ctx context.Context, args []string) error {
		if args[len(args)-1] == checkClipPath(runDir, "c1") {
			cancel()
			return context.DeadlineExceeded
		}
		if ctx.Err() != nil {
			t.Fatalf("next clip was starved: %v", ctx.Err())
		}
		return nil
	}}
	got := svc.finishCheckClips(parent, runDir, run, readActions(runDir), source, run.CreatedAt)
	if len(got) != 1 || got["c3"] != checkClipPath(runDir, "c3") || !fileExists(got["c3"]) {
		t.Fatalf("clips after first clip failure: %v", got)
	}
}
