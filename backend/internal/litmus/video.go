package litmus

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	homebrewFFmpeg  = "/opt/homebrew/bin/ffmpeg"
	homebrewFFprobe = "/opt/homebrew/bin/ffprobe"
	stopTimeout     = 30 * time.Second
	videoTimeout    = 2 * time.Minute
	noVideo         = "none (recording was not available)"
	recordingFPS    = 25
)

// Allow setup plus 100 ms per output frame for encoding and faststart.
func recordingEncodeTimeout(outputFrames int) time.Duration {
	return videoTimeout + time.Duration(outputFrames)*100*time.Millisecond
}

func recordingOutputFrameCount(length time.Duration) int {
	if length <= 0 {
		return 0
	}
	interval := time.Second / recordingFPS
	frames := length / interval
	if length%interval != 0 {
		frames++
	}
	return int(frames)
}

func cutEncodeTimeout(segs []seg) time.Duration {
	var length time.Duration
	for _, sg := range segs {
		length += sg.to - sg.from
	}
	// Each seeked input also needs a decoder, even for a short segment.
	return recordingEncodeTimeout(recordingOutputFrameCount(length)) + time.Duration(len(segs))*time.Second
}

func clipTimeout(length time.Duration) time.Duration {
	return 10*time.Second + min(length, time.Minute)/5
}

// Finalizing durable recordings owns its deadlines. A request timeout or a
// preceding step's timeout must not cancel another encode, probe, cut or clip.
func videoStep(ctx context.Context, run, leg, step string, budget time.Duration, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	started := time.Now()
	err := fn(ctx)
	if err == nil {
		err = ctx.Err()
	}
	level, message := slog.LevelInfo, ""
	if err != nil {
		err = fmt.Errorf("video run=%s leg=%s step=%s: %w", run, leg, step, err)
		level, message = slog.LevelWarn, err.Error()
	}
	slog.Log(ctx, level, "litmus: video step", "run", run, "leg", leg, "step", step, "duration", time.Since(started), "error", message)
	return err
}

// Media runs ffmpeg with the given arguments. Tests substitute a fake, so no unit test encodes video.
type Media interface {
	Run(ctx context.Context, args ...string) error
}

type execMedia struct{}

func (execMedia) Run(ctx context.Context, args ...string) error {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		bin = homebrewFFmpeg // the daemon's PATH may lack Homebrew
	}
	cmd := process.CommandContext(ctx, bin, append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
	errOut := &capWriter{max: 64 << 10}
	cmd.Stderr, cmd.WaitDelay = errOut, 2*time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, lastLines(errOut.buf, 5))
	}
	return nil
}

type playableCacheEntry struct {
	size     int64
	modified time.Time
	duration float64
}

var playableCache = struct {
	sync.Mutex
	m map[string]playableCacheEntry
}{m: map[string]playableCacheEntry{}}

// recordingPath is where a leg's screencast is encoded; cutPath is the captioned video built for the leg.
func recordingPath(runDir, leg string) string { return filepath.Join(runDir, leg, leg+".mp4") }
func cutPath(runDir, leg string) string       { return filepath.Join(runDir, leg+"-cut.mp4") }

func checkClipPath(runDir, checkID string) string {
	checkID = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, checkID)
	checkID = strings.Trim(checkID, "-")
	if checkID == "" {
		checkID = "check"
	}
	return filepath.Join(runDir, "clips", checkID+"-pr.mp4")
}

// recordingDuration asks ffprobe for the duration. A non-empty file is not
// enough: an interrupted MP4 often has bytes but no playable index.
func (s *Service) recordingDuration(ctx context.Context, path string) (float64, bool) {
	duration, err := s.probeVideo(ctx, path, 0)
	return duration, err == nil
}

func (s *Service) probeVideo(ctx context.Context, path string, want time.Duration) (float64, error) {
	duration := float64(0)
	err := videoStep(ctx, filepath.Base(filepath.Dir(filepath.Dir(path))), filepath.Base(filepath.Dir(path)), "probe", 15*time.Second, func(ctx context.Context) error {
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() == 0 {
			return fmt.Errorf("recording is not a non-empty regular file: %s", path)
		}
		playableCache.Lock()
		cached, ok := playableCache.m[path]
		playableCache.Unlock()
		if want == 0 && ok && cached.size == st.Size() && cached.modified.Equal(st.ModTime()) {
			duration = cached.duration
			return nil
		}
		if s.runner == nil {
			return fmt.Errorf("recording probe runner is unavailable")
		}
		probe := "ffprobe"
		if _, err := exec.LookPath(probe); err != nil {
			probe = homebrewFFprobe
		}
		out, err := s.runner.Run(ctx, "", nil, probe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
		if err != nil {
			return err
		}
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		d, parseErr := strconv.ParseFloat(line, 64)
		if parseErr != nil || d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
			return fmt.Errorf("invalid recording duration %q", line)
		}
		duration = d
		if want > 0 && (duration < want.Seconds()-2 || duration > want.Seconds()+2) {
			return fmt.Errorf("recording duration %.3fs, expected %.3fs within 2s; keeping frames", duration, want.Seconds())
		}
		playableCache.Lock()
		playableCache.m[path] = playableCacheEntry{size: st.Size(), modified: st.ModTime(), duration: duration}
		playableCache.Unlock()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return duration, nil
}

func (s *Service) probeRecording(ctx context.Context, path string) bool {
	_, ok := s.recordingDuration(ctx, path)
	return ok
}

// legVideo is what the video step left for one leg: raw is its recording (<leg>.mp4), cut is <leg>-cut.mp4.
type legVideo struct {
	leg           string
	raw, cut      bool
	untilRelaunch int
	resumedAt     []int
	jumpsAt       []int
	keptSegments  []string
	// source is the playable recording used for cuts.
	source string
	// clips maps check ids to per-check PR clips. It is filled by finishVideo;
	// callers that publish comments can use checkClipPath when they need the
	// on-disk path without depending on this internal result.
	clips map[string]string
}

func (v legVideo) files() (out []string) {
	if v.raw {
		out = append(out, v.leg+".mp4")
	}
	if v.cut {
		out = append(out, v.leg+"-cut.mp4")
	}
	return out
}

func (v legVideo) String() string {
	var text string
	switch {
	case v.raw && len(v.keptSegments) > 0:
		kept := v.keptSegments
		v.keptSegments = nil
		return v.String() + "; segment cleanup failed; kept " + strings.Join(kept, ", ")
	case len(v.keptSegments) > 0:
		text = fmt.Sprintf("%s: recording segments could not be joined; kept %s", v.leg, strings.Join(v.keptSegments, ", "))
	case v.raw && v.untilRelaunch > 0 && len(v.resumedAt) > 0:
		var resumes []string
		for _, seq := range v.resumedAt {
			resumes = append(resumes, strconv.Itoa(seq))
		}
		text = fmt.Sprintf("%s: recorded until relaunch at action %d; later actions without video have screenshots only; recording resumes at action %s (%s.mp4)", v.leg, v.untilRelaunch, strings.Join(resumes, ", "), v.leg)
	case v.raw && v.untilRelaunch > 0:
		text = fmt.Sprintf("%s: recorded until relaunch at action %d; later actions have screenshots only (%s.mp4)", v.leg, v.untilRelaunch, v.leg)
	case v.raw && v.cut:
		text = fmt.Sprintf("%s: recorded (%s.mp4; %s-cut.mp4 keeps a 2 s window around each action)", v.leg, v.leg, v.leg)
	case v.raw:
		text = fmt.Sprintf("%s: recorded (%s.mp4)", v.leg, v.leg)
	default:
		text = fmt.Sprintf("%s: recording was not available", v.leg)
	}
	if len(v.jumpsAt) > 0 {
		seqs := make([]string, len(v.jumpsAt))
		for i, seq := range v.jumpsAt {
			seqs[i] = strconv.Itoa(seq)
		}
		text += "; video jumps at actions " + strings.Join(seqs, ", ")
	}
	return text
}

// videoLine is the verdict's Video: text, one entry per leg that has video.
func videoLine(vs []legVideo) string {
	if len(vs) == 0 {
		return noVideo
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = v.String()
	}
	return strings.Join(parts, "; ")
}

// finishVideo builds each leg's cut from that leg's own recording and only that leg's actions: a 2 s window around
// each action, captioned. A leg without a recording is left out.
// Each operation has its own deadline. Cheap check clips precede captioned cuts.
func (s *Service) finishVideo(ctx context.Context, runDir string, run Run) (out []legVideo) {
	all := readActions(runDir)
	cuts := map[string][]seg{}
	for _, leg := range []string{"head", "base"} {
		acts := slices.DeleteFunc(slices.Clone(all), func(a actionRec) bool { return cmp.Or(a.Leg, "head") != leg })
		var l Leg
		if i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg }); i >= 0 {
			l = run.Legs[i]
		}
		started := l.RecordStartedAt
		v := legVideo{leg: leg}
		source := recordingPath(runDir, leg)
		if l.RecordJoined {
			for i, segment := range l.RecordSegments {
				path := filepath.Join(runDir, segment.File)
				if i == 0 {
					path = filepath.Join(runDir, leg, leg+".seg1.mp4")
				}
				if fileExists(path) {
					v.keptSegments = append(v.keptSegments, filepath.Base(path))
				}
			}
		}
		if len(l.RecordSegments) > 1 && !l.RecordJoined {
			for _, segment := range l.RecordSegments {
				path := segmentSourcePath(runDir, leg, segment.File)
				if fileExists(path) {
					v.keptSegments = append(v.keptSegments, filepath.Base(path))
				}
			}
			if len(v.keptSegments) > 0 {
				out = append(out, v)
			}
			continue // no derivative can claim to cover the joined timeline
		}
		duration, playable := s.recordingDuration(ctx, source)
		if !started.IsZero() && playable {
			v.raw, v.source = true, source
			v.untilRelaunch = unrecordedRelaunch(acts, leg)
			for _, a := range acts {
				if a.onLeg() == leg && a.Action == "relaunch" && a.Planned && a.Error == "" && a.RecordSegment != "" {
					v.jumpsAt = append(v.jumpsAt, a.Seq)
				}
			}
			if l.RecordJoined {
				for _, a := range acts {
					if v.untilRelaunch > 0 && a.Seq > v.untilRelaunch && a.Action == "relaunch" && a.RecordSegment != "" && a.RecordOffsetMS != nil {
						v.resumedAt = append(v.resumedAt, a.Seq)
					}
				}
				acts = slices.DeleteFunc(acts, func(a actionRec) bool { return a.RecordOffsetMS == nil })
			} else if v.untilRelaunch > 0 {
				acts = slices.DeleteFunc(acts, func(a actionRec) bool { return a.Seq >= v.untilRelaunch })
			}
			if segs := segments(acts, started); len(segs) > 0 {
				covered := segs[len(segs)-1].to <= time.Duration(duration*float64(time.Second))
				if covered {
					cuts[leg] = segs
				}
			}
		}
		if leg == "head" {
			v.clips = s.finishCheckClips(ctx, runDir, run, all, source, started)
		}
		if v.raw || v.cut {
			out = append(out, v)
		}
	}
	for i := range out {
		v := &out[i]
		if segs := cuts[v.leg]; len(segs) > 0 {
			v.cut = s.encodeCut(ctx, runDir, v.leg, v.source, segs) == nil
		}
	}
	return out
}

func clipArgs(in, out string, from, duration time.Duration) []string {
	duration = min(duration, time.Minute)
	return []string{"-y", "-ss", fmt.Sprintf("%.3f", from.Seconds()), "-t", fmt.Sprintf("%.3f", duration.Seconds()), "-i", in,
		"-c", "copy", "-movflags", "+faststart", out}
}

func actionForReceipt(r Receipt, all []actionRec) (actionRec, bool) {
	if i := slices.IndexFunc(all, func(a actionRec) bool { return a.Seq == r.Seq && a.onLeg() == r.onLeg() }); i >= 0 {
		a := all[i]
		if a.At.IsZero() {
			a.At = r.At
		}
		if a.After == "" {
			a.After = r.AfterPath
		}
		return a, true
	}
	if r.AfterPath == "" {
		return actionRec{}, false
	}
	return actionRec{Seq: r.Seq, Leg: r.Leg, Action: r.Action, Args: r.Args, Before: r.BeforePath, After: r.AfterPath, At: r.At,
		TargetRole: r.TargetRole, TargetName: r.TargetName}, true
}

func (a actionRec) onLeg() string { return cmp.Or(a.Leg, "head") }

func unrecordedRelaunch(actions []actionRec, leg string) int {
	for _, a := range actions {
		if a.onLeg() == leg && a.Action == "relaunch" && a.RecordSegment == "" {
			return a.Seq
		}
	}
	return 0
}

// finishCheckClips writes one clip for each head check when its recording covers
// the receipt window.
func (s *Service) finishCheckClips(ctx context.Context, runDir string, run Run, all []actionRec, source string, started time.Time) map[string]string {
	if s.store == nil {
		return nil
	}
	var checks []Check
	err := videoStep(ctx, run.ID, "head", "clip-index", 15*time.Second, func(context.Context) error {
		var err error
		checks, err = s.store.Checks(run.ID)
		return err
	})
	if err != nil {
		return nil
	}
	out := map[string]string{}
	cutoff := unrecordedRelaunch(all, "head")
	for _, check := range checks {
		if check.onLeg() != "head" {
			continue
		}
		var acts []actionRec
		uncovered := false
		for _, r := range check.legReceipts() {
			if a, ok := actionForReceipt(r, all); ok {
				if cutoff > 0 && a.Seq >= cutoff && a.RecordSegment == "" {
					uncovered = true
				}
				for _, leg := range run.Legs {
					if leg.Name == "head" && leg.RecordJoined && a.RecordOffsetMS == nil {
						uncovered = true
					}
				}
				acts = append(acts, a)
			}
		}
		if len(acts) == 0 {
			continue
		}
		slices.SortFunc(acts, func(a, b actionRec) int { return cmp.Compare(actionOffset(a, started), actionOffset(b, started)) })
		first, last := actionOffset(acts[0], started), actionOffset(acts[len(acts)-1], started)
		if acts[0].At.IsZero() || acts[len(acts)-1].At.IsZero() {
			continue
		}
		path := checkClipPath(runDir, check.ID)
		duration, playable := s.recordingDuration(ctx, source)
		covered := !uncovered && !started.IsZero() && first >= 0 && last+2*time.Second <= time.Duration(duration*float64(time.Second))
		from := max(first-time.Second, 0)
		to := max(last+2*time.Second, from+time.Millisecond)
		length := min(to-from, time.Minute)
		err := videoStep(ctx, run.ID, "head", "clip:"+check.ID, clipTimeout(length), func(ctx context.Context) error {
			if source == "" || !playable || !covered {
				return fmt.Errorf("recording does not cover the check's receipt window")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return err
			}
			if err := s.media.Run(ctx, clipArgs(source, path, from, length)...); err != nil {
				return err
			}
			if !fileExists(path) {
				return fmt.Errorf("clip output is missing")
			}
			return nil
		})
		if err == nil {
			out[check.ID] = path
			continue
		}
		_ = os.Remove(path)
	}
	return out
}

func (s *Service) encodeCut(ctx context.Context, runDir, leg, source string, segs []seg) error {
	out := cutPath(runDir, leg)
	err := videoStep(ctx, filepath.Base(runDir), leg, "cut", cutEncodeTimeout(segs), func(ctx context.Context) error {
		path := filepath.Join(runDir, leg+"-cut-captions.srt")
		if err := os.WriteFile(path, []byte(captions(segs)), 0o600); err != nil {
			return err
		}
		return s.media.Run(ctx, cutArgs(source, path, out, segs)...)
	})
	if err != nil {
		_ = os.Remove(out)
	}
	return err
}

// seg is a stretch of video and its caption.
type seg struct {
	from, to time.Duration
	text     string
}

func actionOffset(a actionRec, start time.Time) time.Duration {
	if a.RecordOffsetMS != nil {
		return time.Duration(*a.RecordOffsetMS) * time.Millisecond
	}
	return a.At.Sub(start)
}

// segments gives each action a 2 s window of the recording (1 s either side) and merges windows that touch.
func segments(acts []actionRec, start time.Time) (out []seg) {
	acts = slices.Clone(acts)
	slices.SortStableFunc(acts, func(a, b actionRec) int { return cmp.Compare(actionOffset(a, start), actionOffset(b, start)) })
	for _, a := range acts {
		if a.Action == "test" { // a package test run is not a moment on screen
			continue
		}
		at := actionOffset(a, start)
		if at < 0 {
			continue
		}
		from, to := max(at-time.Second, 0), at+time.Second
		if n := len(out); n > 0 && from <= out[n-1].to {
			out[n-1].to, out[n-1].text = to, out[n-1].text+" / "+caption(a)
		} else {
			out = append(out, seg{from, to, caption(a)})
		}
	}
	return out
}

// caption is the line under a step in the video: Step 3: click "Collapse sidebar".
func caption(a actionRec) string {
	return fmt.Sprintf("Step %d: %s", a.Seq, stepText(a.Action, a.Args, a.TargetRole, a.TargetName))
}

// refInfo is what a snapshot line says about an element, including whether an
// ancestor is an iframe.
type refInfo struct {
	role, name string
	inIframe   bool
}

var snapRef = regexp.MustCompile(`^\s*- ([A-Za-z]+)(?: ("(?:[^"\\]|\\.)*"))?[^\n]*?\bref=(e[0-9]+)`)
var snapNode = regexp.MustCompile(`^(\s*)- ([A-Za-z]+)\b`)

// parseRefs reads snapshot output into refs keyed the way actions spell them
// (@e7), carrying iframe ancestry down the indented tree.
func parseRefs(snap string) map[string]refInfo {
	out := map[string]refInfo{}
	type ancestor struct {
		indent   int
		inIframe bool
	}
	var ancestors []ancestor
	for _, l := range strings.Split(snap, "\n") {
		node := snapNode.FindStringSubmatch(l)
		if node == nil {
			continue
		}
		indent := len(node[1])
		for len(ancestors) > 0 && ancestors[len(ancestors)-1].indent >= indent {
			ancestors = ancestors[:len(ancestors)-1]
		}
		insideIframe := len(ancestors) > 0 && ancestors[len(ancestors)-1].inIframe
		role := node[2]
		if m := snapRef.FindStringSubmatch(l); m != nil {
			name, _ := strconv.Unquote(m[2])
			out["@"+m[3]] = refInfo{role: role, name: name, inIframe: insideIframe}
		}
		ancestors = append(ancestors, ancestor{indent: indent, inIframe: insideIframe || strings.EqualFold(role, "iframe")})
	}
	return out
}

var roleWords = map[string]string{"button": "a button", "link": "a link", "textbox": "a text field", "combobox": "a field", "checkbox": "a checkbox", "tab": "a tab", "menuitem": "a menu item"}

// plainName is an element's accessible name as a reader should see it: a project row's name is joined to its path
// and age, so only the first line is kept, cut at the first word that is a local path.
func plainName(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	isPath := func(w string) bool {
		return strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~/") || strings.Contains(w, "/Users/") || strings.Contains(w, "/home/")
	}
	words := strings.Fields(s)
	if i := slices.IndexFunc(words, isPath); i > 0 {
		words = words[:i]
	}
	return strings.Join(slices.DeleteFunc(words, isPath), " ")
}

// stepText says what an action did in the words a reader would use: click "Go back", type "hi" into "Message the
// agent", press Enter. An element the snapshot did not name is "a button" (or "an element" with no role either).
func stepText(action string, args []string, role, name string) string {
	quote := func(s string) string {
		return `"` + clip(strings.ReplaceAll(strings.Join(strings.Fields(s), " "), `"`, "'"), 40) + `"`
	}
	target := cmp.Or(roleWords[role], "an element")
	if name = plainName(name); name != "" {
		target = quote(name)
	}
	text := ""
	if len(args) > 1 {
		text = quote(args[1])
	}
	switch action {
	case "press":
		return "press " + strings.Join(args, " ")
	case "fill", "type":
		return "type " + cmp.Or(text, "text") + " into " + target
	case "select":
		return "select " + cmp.Or(text, "an option") + " in " + target
	}
	return action + " " + target
}

// cutArgs joins the segments of in into out, one seeked input per segment, with the captions as a soft track.
func cutArgs(in, srt, out string, segs []seg) []string {
	args, graph := []string{"-y"}, ""
	for i, sg := range segs {
		args = append(args, "-ss", fmt.Sprintf("%.3f", sg.from.Seconds()), "-t", fmt.Sprintf("%.3f", (sg.to-sg.from).Seconds()), "-i", in)
		graph += fmt.Sprintf("[%d:v]", i)
	}
	graph += fmt.Sprintf("concat=n=%d:v=1:a=0,format=yuv420p[v]", len(segs))
	return append(args, "-i", srt, "-filter_complex_threads", "1", "-filter_complex", graph, "-map", "[v]", "-map", fmt.Sprintf("%d:s", len(segs)),
		"-c:v", "libx264", "-preset", "veryfast", "-c:s", "mov_text", "-movflags", "+faststart", out)
}

// captions renders segs as SRT on the output timeline: each segment starts where the previous one ended.
func captions(segs []seg) string {
	var b strings.Builder
	var at time.Duration
	for i, sg := range segs {
		d := sg.to - sg.from
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(at), srtTime(at+d), sg.text)
		at += d
	}
	return b.String()
}

func srtTime(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, d.Milliseconds()%1000)
}
