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

// Real codec proof with generated frames only, without an app or browser.
func TestVideoRelaunchSegmentsKeepPointerCutAndClip(t *testing.T) {
	if os.Getenv("LITMUS_VIDEO_ENCODE") != "1" {
		t.Skip("set LITMUS_VIDEO_ENCODE=1")
	}
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	start := time.Unix(1000, 0).UTC()
	leg := Leg{Name: "head", RecordStartedAt: start, RecordSegments: []RecordSegment{
		{File: filepath.Join("head", "head.mp4"), StartedAt: start},
		{File: filepath.Join("head", "head.seg2.mp4"), StartedAt: start.Add(100 * time.Second)},
	}}
	run := Run{ID: "run_segments", Legs: []Leg{leg}}
	runDir := filepath.Join(svc.store.root, run.ID)
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	for i, segment := range leg.RecordSegments {
		dir := segmentCaptureDir(runDir, leg.Name, segment.File)
		segmentFrames(t, dir, segment.StartedAt, time.Duration(3+i)*time.Second)
		writeFile(t, filepath.Join(dir, "frames", "000001.jpg"), string(cursorJPEG(t, 1320, 780, color.White)))
	}
	local := int64(1000)
	action := actionRec{Seq: 1, Leg: "head", Action: "click", At: start.Add(101 * time.Second), SentAt: start.Add(101 * time.Second),
		RecordSegment: leg.RecordSegments[1].File, SegmentOffsetMS: &local, RecordOffsetMS: &local, Click: &clickAt{X: 40, Y: 40, W: 1320, H: 780}}
	data, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(runDir, "actions.jsonl"), string(data)+"\n")
	if err := svc.store.SaveChecks(run.ID, []Check{{ID: "pointer", Leg: "head", Receipts: []Receipt{{Seq: 1, Leg: "head", At: action.At}}}}); err != nil {
		t.Fatal(err)
	}
	run = svc.stopRecording(context.Background(), run)
	if !run.Legs[0].RecordJoined || len(run.Gaps) != 0 {
		t.Fatalf("join: %+v, %v", run.Legs[0], run.Gaps)
	}
	actions, err := readPointerActions(runDir)
	if err != nil || len(actions) != 1 || actions[0].RecordOffsetMS == nil || *actions[0].RecordOffsetMS != 4000 {
		t.Fatalf("joined offset: %+v, %v", actions, err)
	}
	videos := svc.finishVideo(context.Background(), runDir, run)
	if len(videos) != 1 || !videos[0].raw || !videos[0].cut || len(videos[0].clips) != 1 {
		t.Fatalf("joined derivatives: %+v", videos)
	}
	for i, path := range []string{recordingPath(runDir, "head"), cutPath(runDir, "head"), checkClipPath(runDir, "pointer")} {
		liveFFmpegDecode(t, path)
		at := "1.5"
		if i == 0 {
			at = "4.5"
		}
		pixels, err := exec.Command(homebrewFFmpeg, "-v", "error", "-ss", at, "-i", path, "-frames:v", "1", "-vf", "crop=24:32:39:39", "-pix_fmt", "rgb24", "-f", "rawvideo", "-").Output()
		if err != nil || len(pixels) != 24*32*3 {
			t.Fatalf("decode pointer in %s: %d bytes, %v", path, len(pixels), err)
		}
		dark := 0
		for i := 0; i < len(pixels); i += 3 {
			if pixels[i] < 100 && pixels[i+1] < 100 && pixels[i+2] < 100 {
				dark++
			}
		}
		if dark < 50 {
			t.Fatalf("pointer missing in %s: %d dark pixels", path, dark)
		}
		t.Logf("joined pointer visible in %s: %d dark pixels", filepath.Base(path), dark)
	}
}

func TestVideoJoinReencodesDifferentStreamParameters(t *testing.T) {
	if os.Getenv("LITMUS_VIDEO_ENCODE") != "1" {
		t.Skip("set LITMUS_VIDEO_ENCODE=1")
	}
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	start := time.Unix(1000, 0).UTC()
	leg := Leg{Name: "head", RecordStartedAt: start, RecordSegments: []RecordSegment{
		{File: filepath.Join("head", "head.mp4"), StartedAt: start},
		{File: filepath.Join("head", "head.seg2.mp4"), StartedAt: start.Add(100 * time.Second)},
	}}
	run := Run{ID: "run_streams", Legs: []Leg{leg}}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(svc.store.root, run.ID)
	writeFile(t, filepath.Join(runDir, "head", "keep-dir"), "")
	for i, size := range []string{"640x480", "1280x720"} {
		if err := svc.media.Run(context.Background(), "-y", "-f", "lavfi", "-i", "color=white:s="+size+":r=25", "-t", "3", "-c:v", "libx264", "-pix_fmt", "yuv420p", filepath.Join(runDir, leg.RecordSegments[i].File)); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.joinRecordingSegments(context.Background(), run.ID, leg); err != nil {
		t.Fatal(err)
	}
	liveFFmpegDecode(t, recordingPath(runDir, "head"))
}
