package litmus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in, real ffmpeg proof. The source supplies timing and actions only; all
// JPEGs and outputs are synthetic and live in the test's temporary directory.
// LITMUS_VIDEO_MEASURE_SOURCE=/path/to/run go test -run '^TestVideoSparseWorkloadMeasure$' -v ./internal/litmus/
func TestVideoSparseWorkloadMeasure(t *testing.T) {
	source := os.Getenv("LITMUS_VIDEO_MEASURE_SOURCE")
	if source == "" {
		t.Skip("set LITMUS_VIDEO_MEASURE_SOURCE to a recording's run directory")
	}
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	const runID = "run_measure"
	runDir := filepath.Join(svc.store.root, runID)
	actions, err := readPointerActions(source)
	if err != nil {
		t.Fatal(err)
	}
	// Full-size JPEGs with changing content, rather than tiny placeholder files.
	images := syntheticJPEGFrames(t)
	for _, leg := range []string{"head", "base"} {
		meta, frames, err := readRecording(filepath.Join(source, leg), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		dir := svc.store.legDir(runID, leg)
		for i, f := range frames {
			writeFile(t, filepath.Join(dir, "frames", fmt.Sprintf("%06d.jpg", f.N)), string(images[i%len(images)]))
		}
		var drawn []screencastFrame
		var copies string
		measureVideoStep(t, leg, "frame-draw", func(ctx context.Context) error {
			copies, drawn, err = drawPointerFrames(ctx, dir, leg, frames, meta, actions)
			return err
		})
		if copies != "" {
			t.Cleanup(func() { _ = os.RemoveAll(copies) })
			dir, frames = copies, drawn
		}
		list, err := frameConcatList(dir, frames, meta)
		if err != nil {
			t.Fatal(err)
		}
		listPath := filepath.Join(dir, "frames-concat.txt")
		writeFile(t, listPath, list)
		out := recordingPath(runDir, leg)
		rawArgs := screencastArgs(listPath, out, recordingLength(meta))
		measureVideoStep(t, leg, "raw-encode", func(ctx context.Context) error { return svc.media.Run(ctx, rawArgs...) })
		measureVideoStep(t, leg, "probe", func(context.Context) error {
			duration := liveFFProbeDuration(t, out)
			t.Logf("leg=%s duration=%.3fs source_frames=%d drawn_frames=%d", leg, duration, meta.Frames, len(frames))
			if math.Abs(duration-recordingLength(meta).Seconds()) > 0.1 {
				t.Fatalf("%s raw duration %.3fs, expected %s", leg, duration, recordingLength(meta))
			}
			return nil
		})
		var acts []actionRec
		for _, a := range actions {
			if a.onLeg() == leg {
				acts = append(acts, a)
			}
		}
		segs := segments(acts, meta.StartedWall)
		srt := filepath.Join(runDir, leg+"-captions.srt")
		writeFile(t, srt, captions(segs))
		measureVideoStep(t, leg, "cut", func(ctx context.Context) error {
			return svc.media.Run(ctx, cutArgs(out, srt, cutPath(runDir, leg), segs)...)
		})
		var cutLength time.Duration
		for _, sg := range segs {
			cutLength += sg.to - sg.from
		}
		if got := liveFFProbeDuration(t, cutPath(runDir, leg)); math.Abs(got-cutLength.Seconds()) > 2 {
			t.Fatalf("%s cut duration %.3fs, expected %.3fs", leg, got, cutLength.Seconds())
		}
		liveFFmpegDecode(t, cutPath(runDir, leg))
		if leg == "head" {
			var checks []Check
			data, err := os.ReadFile(filepath.Join(source, "checks.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &checks); err != nil {
				t.Fatal(err)
			}
			for _, check := range checks {
				receipts := check.legReceipts()
				if check.onLeg() != "head" || len(receipts) == 0 {
					continue
				}
				from := max(receipts[0].At.Sub(meta.StartedWall)-time.Second, 0)
				to := receipts[len(receipts)-1].At.Sub(meta.StartedWall) + 2*time.Second
				clip := filepath.Join(runDir, check.ID+".mp4")
				measureVideoStep(t, leg, "clip:"+check.ID, func(ctx context.Context) error {
					return svc.media.Run(ctx, clipArgs(out, clip, from, to-from)...)
				})
				liveFFmpegDecode(t, clip)
			}
		}
		liveFFmpegDecode(t, out)
	}
}

func measureVideoStep(t *testing.T, leg, step string, fn func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	err := fn(ctx)
	t.Logf("leg=%s step=%s wall=%s error=%v", leg, step, time.Since(start).Round(time.Millisecond), err)
	if err != nil {
		t.Fatal(err)
	}
}
