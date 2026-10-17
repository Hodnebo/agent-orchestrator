package litmus

import (
	"context"
	"encoding/json"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Real codec proof without launching an app or browser.
// LITMUS_VIDEO_ENCODE=1 go test -run '^TestVideoPointerSurvivesRawCutAndClip$' -v ./internal/litmus/
func TestVideoPointerSurvivesRawCutAndClip(t *testing.T) {
	if os.Getenv("LITMUS_VIDEO_ENCODE") != "1" {
		t.Skip("set LITMUS_VIDEO_ENCODE=1")
	}
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	const runID = "run_pointer_video"
	runDir := filepath.Join(svc.store.root, runID)
	dir := svc.store.legDir(runID, "head")
	start := time.Unix(1000, 0).UTC()
	run := Run{ID: runID, Legs: []Leg{{Name: "head", RecordStartedAt: start}}}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(screencastManifest{StartedWall: start, StoppedWall: start.Add(6 * time.Second), Frames: 2})
	writeFile(t, filepath.Join(dir, "manifest.json"), string(meta))
	writeFile(t, filepath.Join(dir, "frames.jsonl"), "{\"n\":1,\"wall_ms\":1000000}\n{\"n\":2,\"wall_ms\":1004000}\n")
	frame := cursorJPEG(t, 1320, 780, color.White)
	for _, name := range []string{"000001.jpg", "000002.jpg"} {
		writeFile(t, filepath.Join(dir, "frames", name), string(frame))
	}
	var actions []byte
	for _, a := range []actionRec{
		{Seq: 1, Leg: "head", Action: "hover", At: start.Add(time.Second), SentAt: start.Add(time.Second), Click: &clickAt{X: 40, Y: 40, W: 1320, H: 780}},
		{Seq: 2, Leg: "head", Action: "click", At: start.Add(4 * time.Second), SentAt: start.Add(4 * time.Second), Click: &clickAt{X: 100, Y: 100, W: 1320, H: 780}},
	} {
		line, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		actions = append(actions, append(line, '\n')...)
	}
	writeFile(t, filepath.Join(runDir, "actions.jsonl"), string(actions))
	if err := svc.store.SaveChecks(runID, []Check{{ID: "pointer", Leg: "head", Receipts: []Receipt{{Seq: 1, Leg: "head", At: start.Add(time.Second)}}}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.buildRecording(context.Background(), runID, "head"); err != nil {
		t.Fatal(err)
	}
	results := svc.finishVideo(context.Background(), runDir, run)
	if len(results) != 1 || !results[0].raw || !results[0].cut || len(results[0].clips) != 1 {
		t.Fatalf("missing pointer derivative: %+v", results)
	}
	for _, path := range []string{recordingPath(runDir, "head"), cutPath(runDir, "head"), checkClipPath(runDir, "pointer")} {
		liveFFmpegDecode(t, path)
		// A white source image has no dark pixels. Sample the arrow's bounds at
		// the same action time in all three independently decoded videos.
		pixels, err := exec.Command(homebrewFFmpeg, "-v", "error", "-ss", "1.5", "-i", path, "-frames:v", "1", "-vf", "crop=24:32:39:39", "-pix_fmt", "rgb24", "-f", "rawvideo", "-").Output()
		if err != nil || len(pixels) != 24*32*3 {
			t.Fatalf("decode cursor patch from %s: %d bytes, %v", path, len(pixels), err)
		}
		dark := 0
		for i := 0; i < len(pixels); i += 3 {
			if pixels[i] < 100 && pixels[i+1] < 100 && pixels[i+2] < 100 {
				dark++
			}
		}
		if dark < 50 {
			t.Fatalf("cursor is missing from %s: %d dark pixels", path, dark)
		}
		t.Logf("pointer visible in %s: %d dark pixels", filepath.Base(path), dark)
	}
}
