package litmus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPointerTimeline(t *testing.T) {
	start := time.Unix(1000, 0).UTC()
	action := func(leg, verb string, ms int, point *clickAt) actionRec {
		return actionRec{Leg: leg, Action: verb, SentAt: start.Add(time.Duration(ms) * time.Millisecond), At: start.Add(10 * time.Second), Click: point}
	}
	a := &clickAt{X: 125, Y: 125, W: 1000, H: 500}
	b := &clickAt{X: 1750, Y: 750, W: 2000, H: 1000}
	actions := []actionRec{
		action("head", "click", 1000, a),
		action("base", "hover", 1500, b),
		action("head", "fill", 1700, b),
		action("head", "hover", 2000, b),
		action("head", "dblclick", 3000, a),
		action("head", "hover", 3500, nil),
		action("head", "hover", 4000, b),
	}
	before, _ := json.Marshal(actions)
	for _, tc := range []struct {
		name    string
		ms      int
		visible bool
		x, y    float64
		rings   int
	}{
		{"before first action", 999, false, 0, 0, 0},
		{"click arrives at dispatch", 1000, true, 64, 64, 1},
		{"ring lasts through 399ms", 1399, true, 64, 64, 1},
		{"ring ends at 400ms", 1400, true, 64, 64, 0},
		{"other leg and keyboard leave position", 1700, true, 64, 64, 0},
		{"glide starts 200ms before target", 1800, true, 64, 64, 0},
		{"glide midpoint uses viewport fractions", 1900, true, 256, 128, 0},
		{"hover arrives without ring", 2000, true, 448, 192, 0},
		{"double click has two rings", 3000, true, 64, 64, 2},
		{"missing point hides", 3500, false, 0, 0, 0},
		{"hidden until next known target", 3999, false, 0, 0, 0},
		{"known target restores", 4000, true, 448, 192, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := pointerAt(pointerEvents(actions, "head", start), time.Duration(tc.ms)*time.Millisecond, image.Pt(512, 256))
			rings := 0
			for _, r := range state.rings {
				rings += r.count
			}
			if state.visible != tc.visible || state.x != tc.x || state.y != tc.y || rings != tc.rings {
				t.Fatalf("state %+v, want visible=%t at (%v,%v), %d rings", state, tc.visible, tc.x, tc.y, tc.rings)
			}
		})
	}
	state := pointerAt(pointerEvents(actions, "base", start), 1900*time.Millisecond, image.Pt(100, 100))
	if !state.visible || state.x != 87.5 || state.y != 75 || len(state.rings) != 0 {
		t.Fatalf("base state %+v", state)
	}
	after, _ := json.Marshal(actions)
	if !bytes.Equal(before, after) {
		t.Fatal("timeline changed actions")
	}
}

func TestPointerTimelineShortGlidesAndRefusals(t *testing.T) {
	start := time.Unix(1000, 0)
	a := &clickAt{X: 10, Y: 20, W: 100, H: 100}
	b := &clickAt{X: 90, Y: 80, W: 100, H: 100}
	actions := []actionRec{
		{Action: "hover", SentAt: start.Add(time.Second), Click: a},
		{Action: "hover", SentAt: start.Add(1100 * time.Millisecond), Click: b},
		{Action: "click", At: start.Add(2 * time.Second), Click: a, Reason: "tab drift"},
	}
	events := pointerEvents(actions, "head", start)
	if state := pointerAt(events, 1050*time.Millisecond, image.Pt(100, 100)); state.x != 50 || state.y != 50 {
		t.Fatalf("short glide %+v", state)
	}
	if state := pointerAt(events, 2*time.Second, image.Pt(100, 100)); state.visible {
		t.Fatalf("refusal guessed a point: %+v", state)
	}
	for _, p := range []*clickAt{nil, {}, {X: -1, W: 100, H: 100}, {X: 100, W: 100, H: 100}} {
		if validPointer(p) {
			t.Fatalf("invalid point accepted: %+v", p)
		}
	}
}

func TestPointerTimelineIgnoresActionsBeforeRecording(t *testing.T) {
	start := time.Unix(1000, 0)
	point := &clickAt{X: 25, Y: 25, W: 100, H: 100}
	actions := []actionRec{
		{Action: "click", SentAt: start.Add(-time.Second), Click: point},
		{Action: "hover", SentAt: start.Add(-time.Nanosecond), Click: point},
		{Action: "hover", SentAt: start.Add(time.Second), Click: point},
	}
	events := pointerEvents(actions, "head", start)
	if state := pointerAt(events, 0, image.Pt(100, 100)); state.visible {
		t.Fatalf("pre-recording action made the pointer visible: %+v", state)
	}
	if len(events) != 1 || !pointerAt(events, time.Second, image.Pt(100, 100)).visible {
		t.Fatalf("recorded action missing: %+v", events)
	}
}

func TestPointerTimelineUsesRecordingClockAfterWallClockJump(t *testing.T) {
	start := time.Unix(1000, 0)
	first, second, refused := int64(1000), int64(2000), int64(3000)
	actions := []actionRec{
		{Leg: "head", Action: "hover", SentAt: start.Add(time.Second), RecordOffsetMS: &first, Click: &clickAt{X: 25, Y: 25, W: 100, H: 100}},
		{Leg: "head", Action: "click", SentAt: start.Add(-time.Minute), RecordOffsetMS: &second, Click: &clickAt{X: 75, Y: 75, W: 100, H: 100}},
		{Leg: "head", Action: "hover", At: start.Add(-time.Minute), RecordOffsetMS: &refused, Reason: "tab drift"},
	}
	events := pointerEvents(actions, "head", start)
	if state := pointerAt(events, 1900*time.Millisecond, image.Pt(100, 100)); state.x != 50 || state.y != 50 {
		t.Fatalf("wall clock jump moved glide: %+v", state)
	}
	if state := pointerAt(events, 2*time.Second, image.Pt(100, 100)); state.x != 75 || len(state.rings) != 1 {
		t.Fatalf("wall clock jump moved click: %+v", state)
	}
	if state := pointerAt(events, 3*time.Second, image.Pt(100, 100)); state.visible {
		t.Fatalf("wall clock jump lost refusal: %+v", state)
	}
}

func TestPointerActionsDoNotMoveForKeyboardOrScroll(t *testing.T) {
	for _, verb := range []string{"fill", "type", "focus", "select", "press", "keyboard", "scroll", "scrollintoview"} {
		if moves, _ := pointerAction(verb, nil); moves {
			t.Errorf("%s moves the pointer", verb)
		}
	}
	for _, tc := range []struct {
		verb  string
		args  []string
		moves bool
		rings int
	}{
		{"click", nil, true, 1}, {"tap", nil, true, 1}, {"check", nil, true, 1}, {"uncheck", nil, true, 1},
		{"hover", nil, true, 0}, {"dblclick", nil, true, 2},
		{"cdp", []string{"Input.dispatchMouseEvent", `{"type":"mouseMoved","x":3,"y":4}`}, true, 0},
		{"cdp", []string{"Input.dispatchMouseEvent", `{"type":"mousePressed",`, `"clickCount":2}`}, true, 2},
		{"cdp", []string{"Input.dispatchMouseEvent", `{"type":"mousePressed","clickCount":2}`}, true, 2},
		{"cdp", []string{"Input.dispatchMouseEvent", `{"type":"mouseReleased","clickCount":2}`}, true, 0},
		{"cdp", []string{"Input.dispatchMouseEvent", `{"type":"mouseWheel","x":3,"y":4}`}, false, 0},
		{"cdp", []string{"Input.dispatchKeyEvent", `{}`}, false, 0},
	} {
		if moves, rings := pointerAction(tc.verb, tc.args); moves != tc.moves || rings != tc.rings {
			t.Errorf("%s %v = %t, %d", tc.verb, tc.args, moves, rings)
		}
	}
}

func cursorJPEG(t *testing.T, w, h int, background color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPointerJPEGScalesPerFrameAndPreservesEvidence(t *testing.T) {
	for name, background := range map[string]color.Color{"light": color.White, "dark": color.Black} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			start := time.Unix(1000, 0)
			meta := screencastManifest{StartedWall: start, StoppedWall: start.Add(2 * time.Second), Frames: 2}
			frames := []screencastFrame{{N: 1, WallMS: start.UnixMilli()}, {N: 2, WallMS: start.Add(time.Second).UnixMilli()}}
			raw := [][]byte{cursorJPEG(t, 200, 100, background), cursorJPEG(t, 400, 200, background)}
			for i, f := range frames {
				path := filepath.Join(dir, "frames", fmtFrame(f.N))
				writeFile(t, path, string(raw[i]))
			}
			writeFile(t, filepath.Join(dir, "receipts", "1-after.png"), "unchanged after-shot")
			writeFile(t, filepath.Join(dir, "checks.json"), "unchanged receipts")
			actions := []actionRec{{Action: "hover", SentAt: start.Add(500 * time.Millisecond), Click: &clickAt{X: 400, Y: 200, W: 1000, H: 500}}}
			copies, drawn, err := drawPointerFrames(context.Background(), dir, "head", frames, meta, actions)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(copies) }()
			for _, tc := range []struct {
				ms, x, y int
			}{
				{500, 80, 40}, {1000, 160, 80},
			} {
				var frame screencastFrame
				for _, f := range drawn {
					if frameOffset(f, meta) == int64(tc.ms) {
						frame = f
					}
				}
				data, err := os.ReadFile(filepath.Join(copies, "frames", fmtFrame(frame.N)))
				if err != nil {
					t.Fatal(err)
				}
				img, err := jpeg.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				// Interior is dark; the left edge has a light outline on either theme.
				inside, _, _, _ := img.At(tc.x+4, tc.y+10).RGBA()
				outline, _, _, _ := img.At(tc.x, tc.y+10).RGBA()
				if inside > 100*257 || outline < 180*257 {
					t.Fatalf("pointer missing at (%d,%d): inside=%d outline=%d", tc.x, tc.y, inside/257, outline/257)
				}
			}
			first, err := os.ReadFile(filepath.Join(copies, "frames", "000001.jpg"))
			if err != nil || !bytes.Equal(first, raw[0]) {
				t.Fatalf("hidden frame altered: %v", err)
			}
			for i, frame := range frames {
				data, err := os.ReadFile(filepath.Join(dir, "frames", fmtFrame(frame.N)))
				if err != nil || !bytes.Equal(data, raw[i]) {
					t.Fatalf("raw frame changed: %v", err)
				}
			}
			for path, want := range map[string]string{"checks.json": "unchanged receipts", "receipts/1-after.png": "unchanged after-shot"} {
				got, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil || string(got) != want {
					t.Fatalf("evidence changed: %s %q %v", path, got, err)
				}
			}
		})
	}
}

func fmtFrame(n int) string { return fmt.Sprintf("%06d.jpg", n) }

func TestPointerAnimationDuringHeldFrames(t *testing.T) {
	start := time.Unix(1000, 0)
	meta := screencastManifest{StartedWall: start, StoppedWall: start.Add(5 * time.Second)}
	frames := []screencastFrame{{N: 1, WallMS: start.UnixMilli()}}
	events := []pointerEvent{
		{time.Second, &clickAt{X: 10, Y: 10, W: 100, H: 100}, 0},
		{3 * time.Second, &clickAt{X: 90, Y: 90, W: 100, H: 100}, 1},
	}
	times := pointerFrameTimes(frames, meta, events)
	var moving, ring int
	for _, at := range times {
		state := pointerAt(events, at, image.Pt(100, 100))
		if at > 2800*time.Millisecond && at < 3*time.Second && state.x > 10 && state.x < 90 {
			moving++
		}
		if len(state.rings) == 1 {
			ring++
		}
	}
	if moving != 4 || ring != 10 || pointerAt(events, 3400*time.Millisecond, image.Pt(100, 100)).rings != nil {
		t.Fatalf("held frame animation: %d moving, %d ring frames; times %v", moving, ring, times)
	}
}

type pointerMedia struct {
	inspect func(context.Context, []string) error
}

func (m pointerMedia) Run(ctx context.Context, args ...string) error {
	if err := m.inspect(ctx, args); err != nil {
		return err
	}
	return os.WriteFile(args[len(args)-1], []byte("playable"), 0o600)
}

func TestPointerEncodeCopiesAndFailurePath(t *testing.T) {
	for _, failure := range []string{"", "draw", "log", "encode"} {
		t.Run(failure, func(t *testing.T) {
			svc, _, _, runDir := driveSvc(t)
			meta := recordingFixture(t, svc, "run_s1", true)
			dir := filepath.Join(runDir, "head")
			raw := cursorJPEG(t, 200, 100, color.White)
			for _, name := range []string{"000001.jpg", "000002.jpg"} {
				writeFile(t, filepath.Join(dir, "frames", name), string(raw))
			}
			if failure == "draw" {
				writeFile(t, filepath.Join(dir, "frames", "000002.jpg"), "broken JPEG")
			}
			actions, err := json.Marshal(actionRec{Action: "click", Leg: "head", SentAt: meta.StartedWall.Add(time.Second), Click: &clickAt{X: 400, Y: 200, W: 1000, H: 500}})
			if err != nil {
				t.Fatal(err)
			}
			if failure == "log" {
				actions = append(actions, []byte("\ninvalid JSON")...)
			}
			writeFile(t, filepath.Join(runDir, "actions.jsonl"), string(actions))
			writeFile(t, filepath.Join(runDir, "checks.json"), "receipts unchanged")
			writeFile(t, filepath.Join(runDir, "receipts", "1-after.png"), "shot unchanged")
			paths := []string{"head/frames/000001.jpg", "head/frames/000002.jpg", "head/frames.jsonl", "actions.jsonl", "checks.json", "receipts/1-after.png"}
			before := make(map[string][]byte)
			for _, path := range paths {
				before[path], err = os.ReadFile(filepath.Join(runDir, path))
				if err != nil {
					t.Fatal(err)
				}
			}
			svc.runner = &videoProbeRunner{duration: "70"}
			var called bool
			svc.media = pointerMedia{inspect: func(_ context.Context, args []string) error {
				called = true
				list, err := os.ReadFile(args[6])
				if err != nil {
					return err
				}
				drawn := strings.Contains(string(list), ".cursor-")
				if drawn != (failure != "draw" && failure != "log") {
					t.Fatalf("wrong ffmpeg input: %s", list)
				}
				if drawn {
					path := strings.Split(string(list), "'")[1]
					if !fileExists(path) {
						t.Fatal("drawn copy missing before encode")
					}
				}
				if failure == "encode" {
					return errors.New("ffmpeg failed")
				}
				return nil
			}}
			err = svc.buildRecording(context.Background(), "run_s1", "head")
			if !called || (err != nil) != (failure == "encode") {
				t.Fatalf("encode called=%t err=%v", called, err)
			}
			for path, want := range before {
				if strings.HasPrefix(path, "head/frames/") && failure != "encode" {
					if _, err := os.Stat(filepath.Join(runDir, path)); !os.IsNotExist(err) {
						t.Fatalf("successful encode retained %s: %v", path, err)
					}
					continue
				}
				got, err := os.ReadFile(filepath.Join(runDir, path))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("%s changed: %v", path, err)
				}
			}
			copies, err := filepath.Glob(filepath.Join(dir, ".cursor-*"))
			if err != nil || len(copies) != 0 {
				t.Fatalf("drawn copies remain: %v, %v", copies, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got screencastManifest
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if failure == "draw" || failure == "log" {
				log, err := os.ReadFile(filepath.Join(dir, "engine.log"))
				if err != nil || !strings.Contains(string(log), "pointer overlay:") || len(got.Problems) != 2 || !strings.Contains(got.Problems[1], "pointer overlay:") {
					t.Fatalf("missing drawing problem: %s %+v %v", log, got, err)
				}
			} else if !reflect.DeepEqual(got.Problems, meta.Problems) {
				t.Fatalf("unexpected problems: %v", got.Problems)
			}
			run, _ := svc.store.Get("run_s1")
			if len(run.Gaps) != 0 || run.State != RunRunning {
				t.Fatalf("drawing affected verdict state: %+v", run)
			}
		})
	}
}

func TestPointerDrawingFailureFallsBackWithFullEncodeBudget(t *testing.T) {
	for _, failure := range []string{"deadline", "concat"} {
		t.Run(failure, func(t *testing.T) {
			svc, _, _, runDir := driveSvc(t)
			meta := recordingFixture(t, svc, "run_s1", true)
			dir := filepath.Join(runDir, "head")
			raw := cursorJPEG(t, 200, 100, color.White)
			for _, name := range []string{"000001.jpg", "000002.jpg"} {
				writeFile(t, filepath.Join(dir, "frames", name), string(raw))
			}
			action, err := json.Marshal(actionRec{Action: "click", Leg: "head", SentAt: meta.StartedWall.Add(time.Second), Click: &clickAt{X: 40, Y: 30, W: 100, H: 50}})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(runDir, "actions.jsonl"), string(action))
			svc.runner = &videoProbeRunner{duration: "70"}
			var copiedConcatFailed bool
			options := recordingBuildOptions{}
			if failure == "deadline" {
				options.drawTimeout = func(int) time.Duration { return 0 }
			} else {
				options.concat = func(dir string, frames []screencastFrame, manifest screencastManifest) (string, error) {
					if strings.HasPrefix(filepath.Base(dir), ".cursor-") {
						copiedConcatFailed = true
						return "", errors.New("drawn frame list failed")
					}
					return frameConcatList(dir, frames, manifest)
				}
			}
			svc.media = pointerMedia{inspect: func(ctx context.Context, args []string) error {
				if ctx.Err() != nil {
					return fmt.Errorf("encode context expired: %w", ctx.Err())
				}
				deadline, ok := ctx.Deadline()
				wantBudget := recordingEncodeTimeout(1750) // 70 seconds at 25 fps, despite two captured frames.
				if !ok || time.Until(deadline) < wantBudget-time.Second {
					return fmt.Errorf("fallback lost its full encode budget: deadline %v, want %s", deadline, wantBudget)
				}
				list, err := os.ReadFile(args[6])
				if err != nil {
					return err
				}
				if strings.Contains(string(list), ".cursor-") {
					return fmt.Errorf("fallback used drawn frames: %s", list)
				}
				return nil
			}}
			if err := svc.buildRecordingWithOptions(context.Background(), "run_s1", "head", options); err != nil {
				t.Fatalf("build recording after %s: %v", failure, err)
			}
			if failure == "concat" && !copiedConcatFailed {
				t.Fatal("copied frame list did not hit its forced failure")
			}
			if !fileExists(recordingPath(runDir, "head")) || pathExists(filepath.Join(dir, "frames")) {
				t.Fatal("fallback did not produce video and remove captured frames")
			}
			data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got screencastManifest
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Problems) != 2 || !strings.Contains(got.Problems[1], "pointer overlay:") {
				t.Fatalf("drawing problem not recorded: %+v", got.Problems)
			}
			copies, err := filepath.Glob(filepath.Join(dir, ".cursor-*"))
			if err != nil || len(copies) != 0 {
				t.Fatalf("drawn copies remain: %v, %v", copies, err)
			}
		})
	}
}

func TestPointerEncodeRejectsInvalidCaptureTimes(t *testing.T) {
	svc, _, _, runDir := driveSvc(t)
	meta := recordingFixture(t, svc, "run_s1", true)
	dir := filepath.Join(runDir, "head")
	writeFile(t, filepath.Join(dir, "frames.jsonl"), "{\"n\":1,\"wall_ms\":1001000}\n{\"n\":2,\"wall_ms\":999000}\n")
	action, err := json.Marshal(actionRec{Action: "hover", SentAt: meta.StartedWall.Add(time.Second), Click: &clickAt{X: 10, Y: 10, W: 100, H: 100}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(runDir, "actions.jsonl"), string(action))
	svc.media = pointerMedia{inspect: func(_ context.Context, _ []string) error {
		t.Fatal("invalid capture timeline reached ffmpeg")
		return nil
	}}
	if err := svc.buildRecording(context.Background(), "run_s1", "head"); err == nil || !strings.Contains(err.Error(), "negative duration") {
		t.Fatalf("invalid capture times were hidden: %v", err)
	}
}
