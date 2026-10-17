package litmus

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	screencastInterval = 180 * time.Millisecond
	screencastFrameCap = 9000
)

type screencastFrame struct {
	N        int    `json:"n"`
	WallMS   int64  `json:"wall_ms"`
	OffsetMS *int64 `json:"offset_ms,omitempty"`
}

type screencastManifest struct {
	StartedWall time.Time `json:"startedWall"`
	StoppedWall time.Time `json:"stoppedWall"`
	DurationMS  *int64    `json:"duration_ms,omitempty"`
	Frames      int       `json:"frames"`
	Reconnects  int       `json:"reconnects"`
	Problems    []string  `json:"problems,omitempty"`
}

// screencast has no encoder in its capture loop. Only this loop writes frames
// and the index; stop waits for it before the manifest is consumed by ffmpeg.
type screencast struct {
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	dir             string
	file            string // recording path relative to the run directory
	clock           func() time.Time
	elapsed         func() time.Duration
	resolve         func(context.Context) (string, error)
	trouble         func(error)
	mu              sync.Mutex // connection and stop time, shared with stop
	conn            *websocket.Conn
	stopped         time.Time
	stoppedDuration int64
	once            sync.Once
	err             error // read only after done closes
	meta            screencastManifest
	last            time.Time
	lastOffset      time.Duration
}

func newScreencast(ctx context.Context, dir string, started time.Time, clock func() time.Time, elapsed func() time.Duration, resolve func(context.Context) (string, error), trouble func(error)) *screencast {
	ctx, cancel := context.WithCancel(ctx)
	if elapsed == nil {
		origin := time.Now()
		elapsed = func() time.Duration { return time.Since(origin) }
	}
	r := &screencast{ctx: ctx, cancel: cancel, done: make(chan struct{}), dir: dir, clock: clock, elapsed: elapsed, resolve: resolve, trouble: trouble,
		meta: screencastManifest{StartedWall: started}}
	go r.run()
	return r
}

func (r *screencast) problem(err error) {
	message := clip(cell(err.Error()), 400)
	if slices.Contains(r.meta.Problems, message) {
		return
	}
	if len(r.meta.Problems) >= 20 {
		return
	}
	r.meta.Problems = append(r.meta.Problems, message)
	if r.trouble != nil {
		r.trouble(errors.New(message))
	}
}

func (r *screencast) run() {
	defer close(r.done)
	defer func() {
		r.mu.Lock()
		r.meta.StoppedWall = r.stopped
		if !r.stopped.IsZero() {
			ms := r.stoppedDuration
			r.meta.DurationMS = &ms
		}
		r.mu.Unlock()
		if r.meta.StoppedWall.IsZero() {
			r.meta.StoppedWall = r.clock()
			ms := max(r.elapsed(), r.lastOffset).Milliseconds()
			r.meta.DurationMS = &ms
			if r.ctx.Err() != nil {
				r.problem(fmt.Errorf("capture ended before requested stop: %w", r.ctx.Err()))
			}
		}
		data, err := json.MarshalIndent(r.meta, "", "  ")
		if err == nil {
			err = writeAtomic(filepath.Join(r.dir, "manifest.json"), data)
		}
		if err != nil {
			r.err = err
			r.problem(fmt.Errorf("write recording manifest: %w", err))
		}
	}()
	if err := os.MkdirAll(filepath.Join(r.dir, "frames"), 0o750); err != nil {
		r.err = err
		r.problem(err)
		return
	}
	index, err := os.OpenFile(filepath.Join(r.dir, "frames.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		r.err = err
		r.problem(err)
		return
	}
	defer func() {
		if err := index.Close(); err != nil {
			r.err = err
			r.problem(err)
		}
	}()
	backoff := 250 * time.Millisecond
	for r.ctx.Err() == nil {
		kept := r.meta.Frames
		err := r.capture(index)
		if r.ctx.Err() != nil {
			return
		}
		r.problem(fmt.Errorf("screencast disconnected: %w", err))
		r.meta.Reconnects++
		if r.meta.Frames > kept {
			backoff = 250 * time.Millisecond
		}
		timer := time.NewTimer(backoff)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(2*backoff, 5*time.Second)
	}
}

func screencastWrite(ctx context.Context, conn *websocket.Conn, id int, method string, params any) error {
	data, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}

func (r *screencast) capture(index *os.File) error {
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	url, err := r.resolve(ctx)
	var conn *websocket.Conn
	if err == nil {
		var resp *http.Response
		conn, resp, err = websocket.Dial(ctx, url, nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	cancel()
	if err != nil {
		return err
	}
	conn.SetReadLimit(32 << 20)
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	defer func() {
		_ = conn.CloseNow()
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()
	}()
	if err := screencastWrite(r.ctx, conn, 1, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": 70, "maxWidth": 1320, "maxHeight": 780, "everyNthFrame": 6,
	}); err != nil {
		return err
	}
	id := 1
	for {
		_, data, err := conn.Read(r.ctx)
		if err != nil {
			return err
		}
		var event struct {
			ID     int
			Method string
			Error  *struct{ Message string }
			Params struct {
				Data      string
				SessionID int
			}
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		if event.Error != nil {
			return fmt.Errorf("CDP request %d: %s", event.ID, event.Error.Message)
		}
		if event.Method != "Page.screencastFrame" {
			continue
		}
		id++
		// Acknowledge even frames we discard, before decoding or disk I/O.
		if err := screencastWrite(r.ctx, conn, id, "Page.screencastFrameAck", map[string]int{"sessionId": event.Params.SessionID}); err != nil {
			return err
		}
		if err := r.keepFrame(index, event.Params.Data); err != nil {
			r.problem(fmt.Errorf("save screencast frame: %w", err))
		}
	}
}

// keepFrame and stop share the lock so disk writes finish before stop settles
// the manifest. Its elapsed duration covers the final frame even if wall time
// moves backward.
func (r *screencast) keepFrame(index *os.File, data string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped.IsZero() {
		return nil
	}
	at := r.clock()
	offset := r.elapsed()
	interval := screencastInterval
	if r.meta.Frames >= screencastFrameCap {
		interval = time.Second
	}
	if !r.last.IsZero() && offset-r.lastOffset < interval {
		return nil
	}
	jpeg, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.dir, "frames", fmt.Sprintf("%06d.jpg", r.meta.Frames+1)), jpeg, 0o600); err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return fmt.Errorf("write frame: %w", pathErr.Err)
		}
		return err
	}
	ms := offset.Milliseconds()
	line, _ := json.Marshal(screencastFrame{N: r.meta.Frames + 1, WallMS: at.UnixMilli(), OffsetMS: &ms})
	if _, err := index.Write(append(line, '\n')); err != nil {
		return err
	}
	r.meta.Frames++
	r.last = at
	r.lastOffset = offset
	return nil
}

func (r *screencast) stop(stopped time.Time) error {
	r.once.Do(func() {
		r.mu.Lock()
		r.stopped = stopped
		r.stoppedDuration = max(r.elapsed(), r.lastOffset).Milliseconds()
		conn := r.conn
		r.mu.Unlock()
		if conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = screencastWrite(ctx, conn, -1, "Page.stopScreencast", struct{}{})
			cancel()
		}
		r.cancel()
	})
	<-r.done
	return r.err
}

// frameConcatList holds the first available frame back to the recording origin,
// so a slow initial connection does not shift action offsets. Idle and reconnect
// gaps hold the preceding frame, including the interval through stop.
func frameConcatList(dir string, frames []screencastFrame, meta screencastManifest) (string, error) {
	if len(frames) == 0 {
		return "", fmt.Errorf("recording has no frames")
	}
	var list strings.Builder
	file := func(n int) {
		path := filepath.Join(dir, "frames", fmt.Sprintf("%06d.jpg", n))
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(path, "'", `'\''`))
	}
	for i, frame := range frames {
		from := frameOffset(frame, meta)
		if i == 0 {
			from = 0
		}
		to := recordingLength(meta).Milliseconds()
		if i+1 < len(frames) {
			to = frameOffset(frames[i+1], meta)
		}
		if to < from {
			return "", fmt.Errorf("recording frame %d has a negative duration", frame.N)
		}
		file(frame.N)
		fmt.Fprintf(&list, "duration %.3f\n", float64(to-from)/1000)
	}
	file(frames[len(frames)-1].N) // concat ignores the last duration without this repeat
	return list.String(), nil
}

func screencastArgs(list, out string, duration time.Duration) []string {
	return []string{"-y", "-f", "concat", "-safe", "0", "-i", list,
		"-vf", fmt.Sprintf("scale=1320:780:force_original_aspect_ratio=decrease,pad=1320:780:(ow-iw)/2:(oh-ih)/2,format=yuv420p,fps=%d", recordingFPS),
		"-r", fmt.Sprint(recordingFPS), "-fps_mode", "cfr", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-g", fmt.Sprint(recordingFPS), "-movflags", "+faststart",
		// fps fills sparse holds before output trim. JPEG EOF duration can be
		// guessed from the previous gap, so bound that tail to the wall interval.
		"-t", fmt.Sprintf("%.3f", duration.Seconds()), out}
}

// Existing wall-only indexes remain readable after a daemon upgrade.
func frameOffset(frame screencastFrame, meta screencastManifest) int64 {
	if frame.OffsetMS != nil {
		return *frame.OffsetMS
	}
	return frame.WallMS - meta.StartedWall.UnixMilli()
}

func recordingLength(meta screencastManifest) time.Duration {
	if meta.DurationMS != nil {
		return time.Duration(*meta.DurationMS) * time.Millisecond
	}
	return meta.StoppedWall.Sub(meta.StartedWall)
}

func readRecording(dir string, started time.Time) (screencastManifest, []screencastFrame, error) {
	var meta screencastManifest
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return meta, nil, err
	}
	if !missing {
		if err := json.Unmarshal(data, &meta); err != nil {
			return meta, nil, err
		}
	}
	index, err := os.Open(filepath.Join(dir, "frames.jsonl"))
	if err != nil {
		return meta, nil, err
	}
	defer func() { _ = index.Close() }()
	var frames []screencastFrame
	scan := bufio.NewScanner(index)
	for scan.Scan() {
		var frame screencastFrame
		if err := json.Unmarshal(scan.Bytes(), &frame); err != nil {
			return meta, nil, err
		}
		frames = append(frames, frame)
	}
	if err := scan.Err(); err != nil {
		return meta, nil, err
	}
	if len(frames) == 0 {
		return meta, nil, fmt.Errorf("recording has no frames")
	}
	if missing {
		first, last := frames[0], frames[len(frames)-1]
		meta.StartedWall = started
		if meta.StartedWall.IsZero() {
			meta.StartedWall = time.UnixMilli(first.WallMS)
			if first.OffsetMS != nil {
				meta.StartedWall = meta.StartedWall.Add(-time.Duration(*first.OffsetMS) * time.Millisecond)
			}
		}
		meta.StoppedWall = time.UnixMilli(last.WallMS)
		ms := frameOffset(last, meta)
		meta.DurationMS = &ms
		meta.Frames = len(frames)
	}
	if len(frames) != meta.Frames {
		return meta, nil, fmt.Errorf("recording index has %d frames, manifest has %d; keeping frames", len(frames), meta.Frames)
	}
	return meta, frames, nil
}

type frameConcatListFunc func(string, []screencastFrame, screencastManifest) (string, error)

type recordingBuildOptions struct {
	drawTimeout func(int) time.Duration
	concat      frameConcatListFunc
}

func (s *Service) buildRecording(ctx context.Context, runID, leg string) error {
	return s.buildRecordingWithOptions(ctx, runID, leg, recordingBuildOptions{})
}

// The options keep drawing, frame-list generation, and ffmpeg's deadlines
// independently testable without invoking ffmpeg.
func (s *Service) buildRecordingWithOptions(ctx context.Context, runID, leg string, options recordingBuildOptions) error {
	var started time.Time
	if run, err := s.store.Get(runID); err == nil {
		for _, l := range run.Legs {
			if l.Name == leg {
				started = l.RecordStartedAt
				break
			}
		}
	}
	return s.buildRecordingSegment(ctx, runID, leg, RecordSegment{File: filepath.Join(leg, leg+".mp4"), StartedAt: started}, options)
}

func (s *Service) buildRecordingSegment(ctx context.Context, runID, leg string, segment RecordSegment, options recordingBuildOptions) error {
	runDir := filepath.Join(s.store.root, runID)
	dir := segmentCaptureDir(runDir, leg, segment.File)
	if options.drawTimeout == nil {
		options.drawTimeout = recordingEncodeTimeout
	}
	if options.concat == nil {
		options.concat = frameConcatList
	}
	var meta screencastManifest
	var frames []screencastFrame
	var plainList string
	if err := videoStep(ctx, runID, leg, "frame-index", videoTimeout, func(context.Context) error {
		var err error
		meta, frames, err = readRecording(dir, segment.StartedAt)
		if err == nil {
			plainList, err = options.concat(dir, frames, meta)
		}
		return err
	}); err != nil {
		return err
	}
	if segment.StartedAt.IsZero() {
		segment.StartedAt = meta.StartedWall
	}
	outputFrames := recordingOutputFrameCount(recordingLength(meta))
	problem := func(err error) {
		message := "pointer overlay: " + clip(cell(err.Error()), 400)
		s.engineLog(runID, leg, "recording: "+message)
		if !slices.Contains(meta.Problems, message) {
			meta.Problems = append(meta.Problems, message)
			data, writeErr := json.MarshalIndent(meta, "", "  ")
			if writeErr == nil {
				writeErr = writeAtomic(filepath.Join(dir, "manifest.json"), data)
			}
			if writeErr != nil {
				s.engineLog(runID, leg, "recording: save pointer problem: "+writeErr.Error())
			}
		}
	}
	list := plainList
	var deferCopies string
	defer func() {
		if deferCopies != "" {
			if err := os.RemoveAll(deferCopies); err != nil {
				problem(fmt.Errorf("remove drawn frames: %w", err))
			}
		}
	}()
	drawErr := videoStep(ctx, runID, leg, "frame-draw", options.drawTimeout(outputFrames), func(drawCtx context.Context) error {
		actions, err := readPointerActions(runDir)
		if err != nil {
			return err
		}
		actions = segmentActions(actions, leg, segment, recordingLength(meta))
		var copies string
		var drawn []screencastFrame
		copies, drawn, err = drawPointerFrames(drawCtx, dir, leg, frames, meta, actions)
		if copies != "" {
			// These files must outlive this step, through the raw encode.
			deferCopies = copies
			if err == nil {
				err = drawCtx.Err()
			}
			if err == nil {
				list, err = options.concat(copies, drawn, meta)
			}
		}
		return err
	})
	if drawErr != nil {
		// Cursor rendering must not discard otherwise playable evidence. Its
		// failure remains in the recording manifest and engine log.
		list = plainList
		problem(drawErr)
	}
	listPath := filepath.Join(dir, "frames-concat.txt")
	out := filepath.Join(runDir, segment.File)
	length := recordingLength(meta)
	if err := videoStep(ctx, runID, leg, "raw-encode", recordingEncodeTimeout(outputFrames), func(ctx context.Context) error {
		if err := os.WriteFile(listPath, []byte(list), 0o600); err != nil {
			return err
		}
		return s.media.Run(ctx, screencastArgs(listPath, out, length)...)
	}); err != nil {
		_ = os.Remove(out)
		return err
	}
	if _, err := s.probeVideo(ctx, out, length); err != nil {
		_ = os.Remove(out)
		return err
	}
	return os.RemoveAll(filepath.Join(dir, "frames"))
}

func (s *Service) engineLog(runID, leg, line string) {
	dir := filepath.Join(s.store.root, runID, leg)
	if os.MkdirAll(dir, 0o750) != nil {
		return
	}
	if f, err := os.OpenFile(filepath.Join(dir, "engine.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(f, "%s %s\n", s.clock().Format(time.RFC3339), line)
		_ = f.Close()
	}
}

func (s *Service) startRecording(ctx context.Context, runID string, leg Leg) Leg {
	started, err := s.startRecordingSegment(ctx, runID, leg)
	if err != nil {
		s.recordingProblem(runID, leg.Name, err)
	}
	return started
}

func (s *Service) startRecordingSegment(ctx context.Context, runID string, leg Leg) (Leg, error) {
	e := s.engineFor(runID, leg)
	// Pin once; reconnect discovery does not hold the action lock.
	url, resolve, err := e.recordingPageResolver(ctx)
	if err != nil {
		return leg, err
	}
	first := true
	lookup := func(ctx context.Context) (string, error) {
		if first {
			first = false
			return url, nil
		}
		return resolve(ctx)
	}
	leg.RecordSegments = recordingSegments(leg)
	file := filepath.Join(leg.Name, leg.Name+".mp4")
	if len(leg.RecordSegments) > 0 {
		file = filepath.Join(leg.Name, fmt.Sprintf("%s.seg%d.mp4", leg.Name, len(leg.RecordSegments)+1))
	}
	started := s.clock()
	dir := segmentCaptureDir(filepath.Join(s.store.root, runID), leg.Name, file)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return leg, fmt.Errorf("create recording directory: %w", err)
	}
	if leg.RecordStartedAt.IsZero() {
		leg.RecordStartedAt = started
	}
	leg.RecordStoppedAt = time.Time{}
	leg.RecordSegments = append(leg.RecordSegments, RecordSegment{File: file, StartedAt: started})
	s.recorderMu.Lock()
	defer s.recorderMu.Unlock()
	if s.recorders == nil {
		s.recorders = map[string]*screencast{}
	}
	factory := s.newRecorder
	if factory == nil {
		factory = newScreencast
	}
	r := factory(e.life, dir, started, s.clock, nil, lookup,
		func(err error) { s.engineLog(runID, leg.Name, "recording: "+clip(cell(err.Error()), 400)) })
	r.file = file
	s.recorders[runID+"/"+leg.Name] = r
	return leg, nil
}

func (s *Service) recordingProblem(runID, leg string, err error) {
	s.engineLog(runID, leg, "recording: "+clip(cell(err.Error()), 400))
	build := map[string]string{"head": "PR", "base": "main"}[leg]
	note := "The " + build + " build's recording was not available."
	_, _ = s.update(runID, func(run *Run) {
		if !slices.Contains(run.Gaps, note) {
			run.Gaps = append(run.Gaps, note)
		}
	})
}

// stopRecording settles both captures before encoding. Capture trouble goes to
// engine.log; only an absent playable recording adds a verdict gap.
func (s *Service) stopRecording(ctx context.Context, run Run) Run {
	runDir := filepath.Join(s.store.root, run.ID)
	// Stop every capture before starting any encode.
	for i := range run.Legs {
		leg := &run.Legs[i]
		r := s.takeRecorder(run.ID, leg.Name)
		if r != nil {
			if err := r.stop(s.clock()); err != nil {
				s.engineLog(run.ID, leg.Name, "recording: "+err.Error())
			}
			if leg.RecordStartedAt.IsZero() {
				leg.RecordStartedAt = r.meta.StartedWall
			}
			leg.RecordStoppedAt = r.meta.StoppedWall
		}
		for _, segment := range recordingSegments(*leg) {
			dir := segmentCaptureDir(runDir, leg.Name, segment.File)
			if isRecordingFrameDir(filepath.Join(dir, "frames")) {
				if meta, _, err := readRecording(dir, segment.StartedAt); err == nil {
					if leg.RecordStartedAt.IsZero() {
						leg.RecordStartedAt = meta.StartedWall
					}
					leg.RecordStoppedAt = meta.StoppedWall
				}
			}
		}
		// Recover recordings made before segment metadata was introduced.
		if leg.RecordStartedAt.IsZero() {
			dir := s.store.legDir(run.ID, leg.Name)
			if meta, _, err := readRecording(dir, time.Time{}); err == nil {
				leg.RecordStartedAt, leg.RecordStoppedAt = meta.StartedWall, meta.StoppedWall
			}
		}
		if !leg.RecordStartedAt.IsZero() && leg.RecordStoppedAt.IsZero() {
			leg.RecordStoppedAt = s.clock()
		}
	}
	out, err := s.update(run.ID, func(cur *Run) {
		for i := range cur.Legs {
			for _, leg := range run.Legs {
				if cur.Legs[i].Name == leg.Name {
					cur.Legs[i].RecordStartedAt = leg.RecordStartedAt
					cur.Legs[i].RecordStoppedAt = leg.RecordStoppedAt
				}
			}
		}
	})
	if err != nil {
		s.recordingProblem(run.ID, "head", fmt.Errorf("save recording stop time: %w", err))
		return run
	}
	for _, leg := range run.Legs {
		if leg.RecordJoined {
			continue
		}
		for _, segment := range recordingSegments(leg) {
			path := segmentSourcePath(runDir, leg.Name, segment.File)
			dir := segmentCaptureDir(runDir, leg.Name, segment.File)
			if s.probeRecording(ctx, path) {
				if err := os.RemoveAll(filepath.Join(dir, "frames")); err != nil {
					s.recordingProblem(run.ID, leg.Name, fmt.Errorf("remove encoded frames: %w", err))
				}
				continue
			}
			if err := s.buildRecordingSegment(ctx, run.ID, leg.Name, segment, recordingBuildOptions{}); err != nil {
				s.recordingProblem(run.ID, leg.Name, fmt.Errorf("build recording: %w", err))
			}
		}
		if len(leg.RecordSegments) > 1 {
			if err := s.joinRecordingSegments(ctx, run.ID, leg); err != nil {
				s.engineLog(run.ID, leg.Name, "recording: join segments: "+clip(cell(err.Error()), 400))
				var note string
				if _, saveErr := s.update(run.ID, func(cur *Run) {
					build := map[string]string{"head": "PR", "base": "main"}[leg.Name]
					note = fmt.Sprintf("The %s build's recording segments could not be joined; the segment files were kept.", build)
					for _, saved := range cur.Legs {
						if saved.Name == leg.Name && saved.RecordJoined {
							note = fmt.Sprintf("The %s build's recording was joined, but its segment files could not be removed.", build)
						}
					}
					note = s.scrub(note)
					if !slices.Contains(cur.Gaps, note) {
						cur.Gaps = append(cur.Gaps, note)
					}
				}); saveErr != nil {
					s.engineLog(run.ID, leg.Name, "recording: save join failure: "+saveErr.Error())
				}
				s.engineLog(run.ID, leg.Name, note)
			}
		}
	}
	if latest, err := s.store.Get(run.ID); err == nil {
		return latest
	}
	return out
}

func (s *Service) takeRecorder(runID, leg string) *screencast {
	s.recorderMu.Lock()
	defer s.recorderMu.Unlock()
	key := runID + "/" + leg
	r := s.recorders[key]
	delete(s.recorders, key)
	return r
}

func isRecordingFrameDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
