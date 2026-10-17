package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func segmentFrames(t *testing.T, dir string, started time.Time, length time.Duration) screencastManifest {
	t.Helper()
	ms := length.Milliseconds()
	meta := screencastManifest{StartedWall: started, StoppedWall: started.Add(length), DurationMS: &ms, Frames: 1}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "manifest.json"), string(data))
	writeFile(t, filepath.Join(dir, "frames.jsonl"), fmt.Sprintf("{\"n\":1,\"wall_ms\":%d,\"offset_ms\":0}\n", started.UnixMilli()))
	writeFile(t, filepath.Join(dir, "frames", "000001.jpg"), string(cursorJPEG(t, 160, 100, color.White)))
	return meta
}

// Resolve a real engine's recording page through fake HTTP and CLI responses.
// Capture itself writes deterministic frames without opening a socket.
func fakeRelaunchRecording(t *testing.T, svc *Service, length time.Duration) {
	t.Helper()
	oldClient := httpClient
	httpClient = &http.Client{Transport: relaunchRecordingTransport{}}
	t.Cleanup(func() { httpClient = oldClient })
	exec := svc.engExec
	svc.engExec = func(ctx context.Context, binary string, args, env []string) (string, error) {
		svc.engines["run_s1/head"].identity = fakeDaemonIdentity
		if len(args) > 4 && args[2] == "tab" && args[len(args)-1] == "--json" {
			return `{"data":{"tabs":[{"tabId":"t1","targetId":"app"}]}}`, nil
		}
		return exec(ctx, binary, args, env)
	}
	svc.newRecorder = func(_ context.Context, dir string, started time.Time, clock func() time.Time, _ func() time.Duration, _ func(context.Context) (string, error), _ func(error)) *screencast {
		done := make(chan struct{})
		close(done)
		return &screencast{dir: dir, done: done, cancel: func() {}, clock: clock,
			elapsed: func() time.Duration { return 0 }, meta: segmentFrames(t, dir, started, length)}
	}
}

type relaunchRecordingTransport struct{}

func (relaunchRecordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[{"id":"app","type":"page","url":"http://litmus.test/","webSocketDebuggerUrl":"ws://fixture.invalid/app"}]`)), Header: http.Header{}}, nil
}

type segmentProbeRunner struct {
	different bool
	joined    bool
}

func (r *segmentProbeRunner) Run(_ context.Context, _ string, _ []string, _ string, args ...string) ([]byte, error) {
	path := filepath.Base(args[len(args)-1])
	if strings.Contains(strings.Join(args, " "), "format=duration") {
		if path == "head.joined.mp4" {
			r.joined = true
		}
		duration := map[string]string{"head.mp4": "7", "head.seg2.mp4": "5", "head.seg1.mp4": "7", "head.joined.mp4": "12"}[path]
		if path == "head.mp4" && r.joined {
			duration = "12"
		}
		if duration == "" {
			return nil, fmt.Errorf("unexpected duration probe: %s", path)
		}
		return []byte(duration), nil
	}
	width := 1320
	if r.different && path == "head.seg2.mp4" {
		width = 1280
	}
	return []byte(fmt.Sprintf(`{"streams":[{"codec_type":"video","codec_name":"h264","width":%d,"height":780,"pix_fmt":"yuv420p","r_frame_rate":"25/1","time_base":"1/12800"}]}`, width)), nil
}

func recordedRelaunchFixture(t *testing.T) (*Service, Run, string, time.Time) {
	t.Helper()
	svc, _, dir, _ := relaunchFixture(t)
	start := time.Unix(1000, 0).UTC()
	run, err := svc.update("run_s1", func(run *Run) {
		run.Legs[0].RecordStartedAt = start
		run.Legs[0].RecordSegments = []RecordSegment{{File: filepath.Join("head", "head.mp4"), StartedAt: start}}
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := segmentFrames(t, dir, start, 7*time.Second)
	done := make(chan struct{})
	close(done)
	svc.recorders = map[string]*screencast{run.ID + "/head": {file: run.Legs[0].RecordSegments[0].File, dir: dir,
		done: done, cancel: func() {}, elapsed: func() time.Duration { return 7 * time.Second }, meta: meta}}
	svc.runner, svc.media = &segmentProbeRunner{}, &fakeMedia{}
	return svc, run, filepath.Dir(dir), start
}

func TestRelaunchRecordingSegmentsJoinAndRemapActions(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(fmt.Sprintf("different-streams-%t", different), func(t *testing.T) {
			svc, run, runDir, start := recordedRelaunchFixture(t)
			fakeRelaunchRecording(t, svc, 5*time.Second)
			svc.runner = &segmentProbeRunner{different: different}
			wall := start.Add(100 * time.Second)
			svc.clock = func() time.Time { return wall }
			beforeOffset := int64(2500)
			before := actionRec{Leg: "head", Action: "click", At: start.Add(3 * time.Second), SentAt: start.Add(2500 * time.Millisecond),
				RecordSegment: run.Legs[0].RecordSegments[0].File, SegmentOffsetMS: &beforeOffset, RecordOffsetMS: &beforeOffset}
			svc.logAction(run.ID, &before)
			run, err := svc.Relaunch(ctx, "s1", "head")
			if err != nil {
				t.Fatal(err)
			}
			if len(run.Legs[0].RecordSegments) != 2 || !run.Legs[0].RecordSegments[0].StartedAt.Equal(start) || !run.Legs[0].RecordSegments[1].StartedAt.Equal(wall) {
				t.Fatalf("recording segments: %+v", run.Legs[0])
			}
			stored, err := svc.store.Get(run.ID)
			if err != nil || len(stored.Legs[0].RecordSegments) != 2 {
				t.Fatalf("run.json segments: %+v, %v", stored.Legs, err)
			}
			if got := strings.Join(run.Gaps, "\n"); got != "The head app was relaunched at action 2; the video jumps there." {
				t.Fatalf("relaunch gap: %q", got)
			}
			r := svc.recorders[run.ID+"/head"]
			r.elapsed = func() time.Duration { return 1500 * time.Millisecond }
			wall = wall.Add(1500 * time.Millisecond)
			stamp := svc.actionSent(run.ID, "head")
			after := actionRec{Leg: "head", Action: "click", At: wall, SentAt: stamp.sentAt, RecordSegment: stamp.recordSegment,
				RecordOffsetMS: stamp.recordOffsetMS, SegmentOffsetMS: stamp.recordOffsetMS, Click: &clickAt{X: 40, Y: 40, W: 160, H: 100}}
			svc.logAction(run.ID, &after)
			if err := svc.store.SaveChecks(run.ID, []Check{{ID: "c1", Leg: "head", Receipts: []Receipt{{Seq: after.Seq, Leg: "head", At: after.At}}}}); err != nil {
				t.Fatal(err)
			}
			r.elapsed = func() time.Duration { return 5 * time.Second }
			run = svc.stopRecording(ctx, run)
			if !run.Legs[0].RecordJoined {
				t.Fatalf("join failed: %v", run.Gaps)
			}
			actions, err := readPointerActions(runDir)
			if err != nil || len(actions) != 3 || actions[2].RecordOffsetMS == nil || *actions[2].RecordOffsetMS != 8500 || *actions[2].SegmentOffsetMS != 1500 || *actions[0].RecordOffsetMS != 2500 {
				t.Fatalf("joined action offsets: %+v, %v", actions, err)
			}
			media := svc.media.(*fakeMedia)
			join := media.calls[len(media.calls)-1]
			joinedArgs := strings.Join(join, " ")
			if !strings.Contains(joinedArgs, "-f concat -safe 0") || strings.Contains(joinedArgs, "-c:v libx264") != different || strings.Contains(joinedArgs, "-c copy") == different {
				t.Fatalf("join codec selection: %s", joinedArgs)
			}
			if different {
				want := screencastArgs(filepath.Join(runDir, "head", "segments-concat.txt"), filepath.Join(runDir, "head", "head.joined.mp4"), 12*time.Second)
				if !reflect.DeepEqual(join, want) {
					t.Fatalf("join re-encode args = %v, recorder args = %v", join, want)
				}
			}
			if fileExists(filepath.Join(runDir, "head", "head.seg2.mp4")) || fileExists(filepath.Join(runDir, "head", "head.seg1.mp4")) || !fileExists(recordingPath(runDir, "head")) {
				t.Fatal("successful join did not replace and remove segments")
			}
			videos := svc.finishVideo(ctx, runDir, run)
			if len(videos) != 1 || !videos[0].raw || !videos[0].cut || len(videos[0].clips) != 1 || strings.Contains(videoLine(videos), "until relaunch") {
				t.Fatalf("joined derivatives: %+v, %s", videos, videoLine(videos))
			}
			clip := strings.Join(media.calls[len(media.calls)-2], " ")
			if !strings.Contains(clip, "-ss 7.500 -t 3.000") {
				t.Fatalf("clip did not use joined offset: %s", clip)
			}
			calls := len(media.calls)
			svc.stopRecording(ctx, run)
			if len(media.calls) != calls {
				t.Fatal("finalizing twice repeated the join")
			}
		})
	}
}

func TestJoinActionOffsetsClearsActionsOutsideAllSegments(t *testing.T) {
	svc, _, _, runDir := driveSvc(t)
	start := time.Unix(1000, 0).UTC()
	leg := Leg{Name: "head", RecordSegments: []RecordSegment{{File: filepath.Join("head", "head.mp4"), StartedAt: start}}}
	stale := int64(9000)
	svc.logAction("run_s1", &actionRec{Leg: "head", Action: "click", At: start.Add(20 * time.Second), RecordSegment: leg.RecordSegments[0].File, SegmentOffsetMS: &stale, RecordOffsetMS: &stale})
	if err := svc.joinActionOffsets(runDir, leg, []time.Duration{7 * time.Second}); err != nil {
		t.Fatal(err)
	}
	actions, err := readPointerActions(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].RecordSegment != "" || actions[0].SegmentOffsetMS != nil || actions[0].RecordOffsetMS != nil {
		t.Fatalf("unmatched action retained recording offsets: %+v", actions)
	}
}

func TestRelaunchRestartsRecordingWhenSegmentMarkerExists(t *testing.T) {
	svc, run, _, start := recordedRelaunchFixture(t)
	if _, err := svc.update(run.ID, func(cur *Run) {
		cur.Legs[0].RecordStartedAt = time.Time{}
	}); err != nil {
		t.Fatal(err)
	}
	fakeRelaunchRecording(t, svc, 5*time.Second)
	svc.clock = func() time.Time { return start.Add(100 * time.Second) }

	got, err := svc.Relaunch(ctx, "s1", "head")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Legs[0].RecordSegments) != 2 {
		t.Fatalf("recording segments after segment-only relaunch: %+v", got.Legs[0].RecordSegments)
	}
	if len(got.Gaps) != 1 || !strings.Contains(got.Gaps[0], "video jumps there") {
		t.Fatalf("relaunch gaps = %q, want one relaunch video gap", got.Gaps)
	}
}

func TestRelaunchRecorderRestartFailureKeepsCauseAndHonestVideoLine(t *testing.T) {
	svc, run, runDir, start := recordedRelaunchFixture(t)
	svc.clock = func() time.Time { return start.Add(100 * time.Second) }
	exec := svc.engExec
	svc.engExec = func(ctx context.Context, binary string, args, env []string) (string, error) {
		svc.engines["run_s1/head"].identity = fakeDaemonIdentity
		if len(args) > 4 && args[2] == "tab" && args[len(args)-1] == "--json" {
			return "", &engineInterruption{actionNotSent(errors.New("recorder target unavailable"))}
		}
		return exec(ctx, binary, args, env)
	}
	run, err := svc.Relaunch(ctx, "s1", "head")
	if err != nil {
		t.Fatal(err)
	}
	gap := strings.Join(run.Gaps, "\n")
	if gap != "Native dialog recorder unavailable for this leg" || len(run.Legs[0].RecordSegments) != 1 {
		t.Fatalf("restart failure: %+v, %q", run.Legs[0], gap)
	}
	log, err := os.ReadFile(filepath.Join(runDir, "head", "engine.log"))
	if err != nil || !strings.Contains(string(log), "resolve recording app target: recorder target unavailable") {
		t.Fatalf("recorder failure missing from private log: %s, %v", log, err)
	}
	later := actionRec{Leg: "head", Action: "click", At: svc.clock().Add(time.Second)}
	svc.logAction(run.ID, &later)
	run = svc.stopRecording(ctx, run)
	videos := svc.finishVideo(ctx, runDir, run)
	line := videoLine(videos)
	if !strings.Contains(line, "head: recorded until relaunch at action 1; later actions have screenshots only") {
		t.Fatalf("dishonest video line: %s", line)
	}
}

func TestRecordingJoinFailureKeepsSegmentsAndExplainsVideo(t *testing.T) {
	svc, run, runDir, start := recordedRelaunchFixture(t)
	fakeRelaunchRecording(t, svc, 5*time.Second)
	svc.clock = func() time.Time { return start.Add(100 * time.Second) }
	run, err := svc.Relaunch(ctx, "s1", "head")
	if err != nil {
		t.Fatal(err)
	}
	svc.media = pointerMedia{inspect: func(_ context.Context, args []string) error {
		if strings.HasSuffix(args[len(args)-1], ".joined.mp4") {
			return errors.New("concat encoder failed")
		}
		return nil
	}}
	run = svc.stopRecording(ctx, run)
	if !fileExists(recordingPath(runDir, "head")) || !fileExists(filepath.Join(runDir, "head", "head.seg2.mp4")) || run.Legs[0].RecordJoined || !strings.Contains(strings.Join(run.Gaps, "\n"), "The PR build's recording segments could not be joined; the segment files were kept.") || strings.Contains(strings.Join(run.Gaps, "\n"), "concat encoder failed") {
		t.Fatalf("join failure lost evidence or exposed its private cause: %+v", run)
	}
	log, err := os.ReadFile(filepath.Join(svc.store.legDir(run.ID, "head"), "engine.log"))
	if err != nil || !strings.Contains(string(log), "concat encoder failed") {
		t.Fatalf("join failure was not kept in the private engine log: %q, %v", log, err)
	}
	videos := svc.finishVideo(ctx, runDir, run)
	if line := videoLine(videos); !strings.Contains(line, "segments could not be joined; kept head.mp4, head.seg2.mp4") || strings.Contains(line, "head: recorded") {
		t.Fatalf("join failure video line: %s", line)
	}
}

func TestVerdictVideoLineWithAndWithoutRelaunch(t *testing.T) {
	for _, tc := range []struct {
		video legVideo
		want  string
	}{
		{legVideo{leg: "head", raw: true}, "head: recorded (head.mp4)"},
		{legVideo{leg: "head", raw: true, untilRelaunch: 12}, "head: recorded until relaunch at action 12; later actions have screenshots only"},
	} {
		md := renderVerdict(Run{ID: "run_video"}, Verdict{}, false, []legVideo{tc.video}, t.TempDir(), time.Now(), nil)
		if !strings.Contains(md, "Video: "+tc.want) {
			t.Fatalf("missing video line %q in verdict:\n%s", tc.want, md)
		}
	}
}

func TestFinishVideoReportsRecordingRecoveryAfterFailedRestart(t *testing.T) {
	svc, _, _, runDir := driveSvc(t)
	run, err := svc.update("run_s1", func(run *Run) {
		start := time.Unix(1000, 0).UTC()
		run.Legs[0].RecordStartedAt = start
		run.Legs[0].RecordJoined = true
		run.Legs[0].RecordSegments = []RecordSegment{
			{File: filepath.Join("head", "head.mp4"), StartedAt: start},
			{File: filepath.Join("head", "head.seg2.mp4"), StartedAt: start.Add(100 * time.Second)},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.runner, svc.media = &videoProbeRunner{duration: "12"}, &fakeMedia{}
	writeFile(t, recordingPath(runDir, "head"), "joined video")
	joined := int64(7000)
	for _, action := range []actionRec{
		{Leg: "head", Action: "relaunch", At: time.Unix(1010, 0)},
		{Leg: "head", Action: "click", At: time.Unix(1011, 0)},
		{Leg: "head", Action: "relaunch", At: time.Unix(1100, 0), RecordSegment: run.Legs[0].RecordSegments[1].File, RecordOffsetMS: &joined},
		{Leg: "head", Action: "click", At: time.Unix(1101, 0), RecordSegment: run.Legs[0].RecordSegments[1].File, RecordOffsetMS: &joined},
	} {
		svc.logAction(run.ID, &action)
	}
	videos := svc.finishVideo(ctx, runDir, run)
	line := videoLine(videos)
	if !strings.Contains(line, "recorded until relaunch at action 1") || !strings.Contains(line, "recording resumes at action 3") || strings.Contains(line, "later actions have screenshots only") {
		t.Fatalf("recording recovery was hidden: %s", line)
	}
	if len(videos) != 1 || !videos[0].cut {
		t.Fatalf("recovered actions were not cut: %+v", videos)
	}
}

func TestJoinOffsetsPreserveUnrecordedIntervalWhenWallTimeOverlaps(t *testing.T) {
	svc, _, _, runDir := driveSvc(t)
	start := time.Unix(1000, 0).UTC()
	leg := Leg{Name: "head", RecordSegments: []RecordSegment{
		{File: filepath.Join("head", "head.mp4"), StartedAt: start},
		{File: filepath.Join("head", "head.seg2.mp4"), StartedAt: start.Add(100 * time.Second)},
	}}
	local := int64(1500)
	for _, action := range []actionRec{
		{Leg: "head", Action: "relaunch", At: start.Add(7 * time.Second)},
		{Leg: "head", Action: "click", At: start.Add(2 * time.Second)},
		{Leg: "head", Action: "relaunch", At: start.Add(100 * time.Second), RecordSegment: leg.RecordSegments[1].File},
		{Leg: "head", Action: "click", At: start.Add(101500 * time.Millisecond), RecordSegment: leg.RecordSegments[1].File, RecordOffsetMS: &local, SegmentOffsetMS: &local},
	} {
		svc.logAction("run_s1", &action)
	}
	if err := svc.joinActionOffsets(runDir, leg, []time.Duration{7 * time.Second, 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	actions, err := readPointerActions(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if actions[0].RecordOffsetMS != nil || actions[1].RecordOffsetMS != nil || actions[0].RecordSegment != "" || *actions[3].RecordOffsetMS != 8500 {
		t.Fatalf("unrecorded interval gained fake offsets: %+v", actions)
	}
	if first := segmentActions(actions, "head", leg.RecordSegments[0], 7*time.Second); len(first) != 0 {
		t.Fatalf("unrecorded actions leaked into first segment: %+v", first)
	}
}
