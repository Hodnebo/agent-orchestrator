package litmus

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Tests shorten these.
var (
	settleDelay = 400 * time.Millisecond // let the UI react before the after-shot
	settleLoad  = 3 * time.Second        // cap on `wait --load`; a failure is recorded as a replay stop warning
	shotRetry   = time.Second            // pause before the one retry of an after-shot that timed out
)

const (
	actionOutputCap     = 2048
	actionOutputFileCap = 1 << 20
)

var pageContentBoundary = regexp.MustCompile(`(?m)^--- (?:END_)?AGENT_BROWSER_PAGE_CONTENT[^\r\n]*---\r?\n?`)

// replayVerbs describes the actions replay can safely retarget by accessible name.
// Drive itself accepts all commands except the escape paths in drivepolicy.go.
type verb struct {
	refs    int
	receipt bool
}

var replayVerbs = map[string]verb{
	"click": {refs: 1, receipt: true}, "dblclick": {refs: 1, receipt: true},
	"hover": {refs: 1, receipt: true}, "focus": {refs: 1, receipt: true},
	"fill": {refs: 1, receipt: true}, "type": {refs: 1, receipt: true},
	"press": {receipt: true}, "keyboard": {receipt: true}, "scroll": {receipt: true},
	"scrollintoview": {refs: 1, receipt: true}, "select": {refs: 1, receipt: true},
	"check": {refs: 1, receipt: true}, "uncheck": {refs: 1, receipt: true},
	"drag": {refs: 2, receipt: true},
}

var refPattern = regexp.MustCompile(`^@?(e\d+)$`)

// splitLeg pulls "--leg <name>" or "--leg=<name>" out of args; the CLI appends it when --leg is given.
func splitLeg(args []string) (leg string, rest []string) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--leg" && i+1 < len(args):
			leg, i = args[i+1], i+1
		case strings.HasPrefix(a, "--leg="):
			leg = strings.TrimPrefix(a, "--leg=")
		default:
			rest = append(rest, a)
		}
	}
	return leg, rest
}

// pickLeg names the launched leg an action applies to; without --leg it must be unambiguous.
func pickLeg(run Run, name string) (Leg, error) {
	var live []Leg
	for _, l := range run.Legs {
		if l.State == "running" && l.Engine != nil {
			live = append(live, l)
		}
	}
	switch {
	case name != "":
		if i := slices.IndexFunc(live, func(l Leg) bool { return l.Name == name }); i >= 0 {
			return live[i], nil
		}
		return Leg{}, conflict("TESTING_LEG_NOT_LAUNCHED", "leg "+name+" is not launched; run `ao testing launch "+name+"`")
	case len(live) == 1:
		return live[0], nil
	case len(live) == 0:
		return Leg{}, conflict("TESTING_LEG_NOT_LAUNCHED", "no leg is launched; run `ao testing launch head`")
	}
	return Leg{}, invalid("TESTING_LEG_REQUIRED", "both legs are launched; name one with --leg base|head")
}

// inRun resolves an output path the worker named: relative paths land in the run dir, and nothing may leave it.
func inRun(runDir, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(runDir, p)
	}
	rel, err := filepath.Rel(runDir, filepath.Clean(p))
	if err != nil || !filepath.IsLocal(rel) {
		return "", invalid("TESTING_PATH_INVALID", "output files must stay inside "+runDir)
	}
	if managedDrivePath(rel) {
		return "", invalid("TESTING_PATH_INVALID", "engine control files are managed by Litmus")
	}

	// Resolve existing ancestors as well as the final file: scratch symlinks must not reach host files.
	root, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return "", err
	}
	ancestor := filepath.Clean(p)
	for {
		resolved, e := filepath.EvalSymlinks(ancestor)
		if e == nil {
			rel, e := filepath.Rel(root, resolved)
			if e != nil || (!filepath.IsLocal(rel) && rel != ".") {
				return "", invalid("TESTING_PATH_INVALID", "file paths must stay inside "+runDir)
			}
			suffix, _ := filepath.Rel(ancestor, filepath.Clean(p))
			if managedDrivePath(filepath.Join(rel, suffix)) {
				return "", invalid("TESTING_PATH_INVALID", "run control files are managed by Litmus")
			}
			break
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", e
		}
		ancestor = parent
	}
	return filepath.Clean(p), os.MkdirAll(filepath.Dir(p), 0o750)
}

func managedDrivePath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if (parts[0] == "head" || parts[0] == "base") && len(parts) > 1 && slices.Contains([]string{"bin", "agents", "shell", "home"}, parts[1]) {
		return true
	}
	return slices.Contains([]string{"engine", "agents", "shell"}, parts[0]) || filepath.Base(rel) == "owned.json" || slices.Contains([]string{"run.json", "checks.json", "actions.jsonl", ownerLoginsFile, homeSnapshotFile, homeChangesFile, verdictFile, "verdict.md", "context-pack.md", "recipe.yaml", "comment.md", "intake-claims.json"}, rel)
}

// Drive runs one worker action on a launched leg. It is refused unless the run is
// running, inside its deadline and the named leg is launched.
func (s *Service) Drive(ctx context.Context, sessionID, action string, args []string) (res map[string]any, err error) {
	legName, args := splitLeg(args)
	s.mu.Lock()
	run, err := s.load(sessionID)
	s.mu.Unlock()
	if err != nil {
		if id, idErr := runID(sessionID); idErr == nil {
			if _, readErr := s.store.Get(id); readErr == nil {
				rec := actionRec{Leg: legName, Action: action, Args: args, At: s.clock(), Reason: err.Error()}
				s.logAction(id, &rec)
			}
		}
		return nil, err
	}
	runDir := filepath.Join(s.store.root, run.ID)
	t0 := time.Now()
	var out string
	var actionStamp actionTime
	recorded := false
	defer func() {
		if !recorded {
			rec := actionRec{Leg: legName, Action: action, Args: args, At: s.clock(), SentAt: actionStamp.sentAt, EngineGeneration: actionStamp.generation, RecordOffsetMS: actionStamp.recordOffsetMS, RecordSegment: actionStamp.recordSegment, SegmentOffsetMS: actionStamp.recordOffsetMS, MS: time.Since(t0).Milliseconds()}
			if outputAction(action) {
				rec.Output = out
			}
			if err != nil {
				rec.Reason = err.Error()
			}
			s.logAction(run.ID, &rec)
			if res != nil && outputAction(action) {
				if rec.Output != "" {
					res["output"] = rec.Output
				}
				if rec.OutputPath != "" {
					res["outputPath"] = relPath(runDir, rec.OutputPath)
				}
				addDriveText(res, runDir, "")
			}
		}
	}()
	if run.State != RunRunning {
		return nil, conflict("TESTING_RUN_NOT_RUNNING", fmt.Sprintf("run %s is %s; launch a leg first", run.ID, run.State))
	}
	leg, err := pickLeg(run, legName)
	if err != nil {
		return nil, err
	}
	legName = leg.Name
	cmd, file, err := argv(action, args, runDir, leg.Name)
	if err != nil {
		return nil, err
	}
	if action == "batch" {
		// Dispatch each child through Drive so it gets the same guards, binding and log.
		_, options, _ := browserOptions(args, runDir)
		var results []map[string]any
		for _, child := range cmd[1:] {
			if child == "--bail" || child == "--json" {
				continue
			}
			words, parseErr := commandWords(child)
			if parseErr != nil {
				return nil, parseErr
			}
			out, childErr := s.Drive(ctx, sessionID, words[0], append(append(words[1:], options...), "--leg", leg.Name))
			results = append(results, out)
			if childErr != nil {
				err = errors.Join(err, childErr)
				if captureBlocked(childErr) || slices.Contains(cmd, "--bail") {
					break
				}
			}
		}
		_, current, done, beginErr := s.beginLegCommand(sessionID, leg.Name)
		if beginErr != nil {
			return nil, errors.Join(err, beginErr)
		}
		defer done()
		leg = current
		e := s.engineFor(run.ID, leg)
		e.mu.Lock()
		defer e.mu.Unlock()
		defer reportEngineRestart(ctx, e, &err)
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		var rec actionRec
		if captureBlocked(err) {
			rec = s.noCaptureReceipt(run.ID, e, leg.Name, action, cmd[1:], refInfo{}, actionTime{}, time.Since(t0), reason, out)
		} else {
			var receiptErr error
			rec, receiptErr = s.receipt(ctx, run.ID, e, leg.Name, action, cmd[1:], refInfo{}, nil, nil, nil, actionTime{}, time.Since(t0), reason, out)
			err = errors.Join(err, receiptErr)
		}
		recorded = true
		res = map[string]any{"leg": leg.Name, "action": action, "results": results, "receipt": rec}
		addDriveText(res, runDir, "")
		return res, err
	}
	_, current, done, err := s.beginLegCommand(sessionID, leg.Name)
	if err != nil {
		return nil, err
	}
	defer done()
	leg = current
	e := s.engineFor(run.ID, leg)
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if e.life.Err() != nil {
			return
		}
		if err == nil {
			err = s.setControl(run.ID, leg.Name, LegControl{State: "ok"}, e)
		} else if isLegDown(err) {
			var diagnosed *diagnosedControlError
			if !errors.As(err, &diagnosed) {
				err = driveEngineError(e.unavailable(ctx, err))
			}
		}
	}()
	defer reportEngineRestart(ctx, e, &err)
	if e.restartNote != "" {
		return nil, driveEngineError(&engineInterruption{actionNotSent(errors.New(e.restartNote))})
	}
	pending := actionRec{Leg: leg.Name, Action: cmd[0], Args: cmd[1:], Reason: "command has not completed"}
	e.pendingAction = &pending
	defer func() { e.pendingAction = nil }()
	var target refInfo
	for _, i := range refArgs(action, cmd[1:]) {
		if i+1 < len(cmd) {
			target = e.refs[cmd[i+1]]
			break
		}
	}
	changing := !readOnly(action, cmd[1:])
	var click *clickAt
	var box *ElementBox
	var receiptBox *ElementBox
	var pointErr error
	if pointerTargetAction(action) && len(cmd) > 1 {
		click, box, receiptBox, pointErr = e.clickTarget(ctx, cmd[1])
		if pointErr != nil && !isTabDrift(pointErr) {
			slog.Default().Debug("litmus: click point unavailable", "action", action, "ref", cmd[1], "error", pointErr)
		}
	}
	if changing && e.last == "" && !captureBlocked(pointErr) {
		p := filepath.Join(runDir, "receipts", leg.Name+"-before.png")
		shotErr := s.shoot(ctx, e, p)
		if captureBlocked(shotErr) {
			pointErr = shotErr
		} else if shotErr == nil || fileExists(p) {
			e.last = p
			if shotErr != nil {
				slog.Default().Warn("litmus: before-shot", "error", shotErr)
			}
		}
	}
	if captureBlocked(pointErr) {
		err = pointErr
	} else if action == "cdp" {
		out, err = e.rawCDP(ctx, cmd[1:], func(point *clickAt) {
			click = point
			actionStamp = s.actionSent(run.ID, leg.Name)
			actionStamp.generation = e.generation
			pending.SentAt = actionStamp.sentAt
			pending.EngineGeneration = actionStamp.generation
		})
	} else {
		out, err = e.runBeforeSend(ctx, cmdTimeout, func() {
			actionStamp = s.actionSent(run.ID, leg.Name)
			actionStamp.generation = e.generation
			pending.SentAt = actionStamp.sentAt
			pending.EngineGeneration = actionStamp.generation
		}, cmd...)
		if err == nil && slices.Contains([]string{"close", "quit", "exit"}, action) {
			e.once = sync.Once{}
			e.ierr = nil
			e.tab = ""
			e.ready = false
			e.daemon = daemonIdentity{}
		}
	}
	pending.Reason = ""
	if err != nil {
		pending.Reason = err.Error()
	}
	res = map[string]any{"leg": leg.Name, "action": action, "output": s.shortActionOutput(action, out)}
	if captureBlocked(err) {
		if actionStamp.sentAt.IsZero() {
			err = actionNotSent(err)
		}
		rec := s.noCaptureReceipt(run.ID, e, leg.Name, action, cmd[1:], target, actionStamp, time.Since(t0), driveErrorMessage(err), out)
		recorded = true
		res["receipt"] = rec
		if rec.Output != "" {
			res["output"] = rec.Output
		}
		if rec.OutputPath != "" {
			res["outputPath"] = relPath(runDir, rec.OutputPath)
		}
		addDriveText(res, runDir, "")
		return res, driveEngineError(err)
	}
	if action == "snapshot" && err == nil {
		tree := snapshotTree(out)
		e.refs = parseRefs(tree)
		res["snapshot"] = compactSnapshot(tree)
	}
	if action == "screenshot" && err == nil {
		if flattenErr := e.flatten(ctx, file); flattenErr != nil {
			res["warning"] = "opaque screenshot: " + flattenErr.Error()
			if captureBlocked(flattenErr) {
				err = flattenErr
			}
		}
	}
	if file != "" {
		res["path"] = file
	}
	if actionStamp.sentAt.IsZero() {
		// A point measured before a health-probe refusal did not belong to a
		// dispatched action and must not be drawn as one.
		click = nil
	}
	if changing {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		rec, receiptErr := s.receipt(ctx, run.ID, e, leg.Name, action, cmd[1:], target, click, box, receiptBox, actionStamp, time.Since(t0), reason, out)
		recorded = true
		res["receipt"] = rec
		if rec.Output != "" {
			res["output"] = rec.Output
		}
		if rec.OutputPath != "" {
			res["outputPath"] = relPath(runDir, rec.OutputPath)
		}
		if receiptErr != nil {
			err = errors.Join(err, receiptErr)
			addDriveText(res, runDir, "")
			return res, driveEngineError(err)
		}
		if rec.After != "" {
			res["after"] = relPath(runDir, rec.After)
		}
		if snap, snapErr := e.Run(ctx, cmdTimeout, "snapshot", "-i"); snapErr == nil {
			e.refs = parseRefs(snap)
			res["snapshot"] = compactSnapshot(snap)
		} else if captureBlocked(snapErr) {
			err = errors.Join(err, snapErr)
		}
	}
	addDriveText(res, runDir, "")
	if err != nil {
		if action == "click" && !captureBlocked(err) && isCoveredClickError(err) {
			res["refused"], res["reason"] = true, err.Error()
			return res, apierr.Conflict("TESTING_ELEMENT_COVERED", "click refused because the element is covered: "+err.Error()+"\n"+res["text"].(string), nil)
		}
		return res, driveEngineError(err)
	}
	return res, nil
}

// driveErrorMessage includes recovery advice for tester errors and receipts.
func driveErrorMessage(err error) string {
	message := err.Error()
	var diagnosed *diagnosedControlError
	if errors.As(err, &diagnosed) && diagnosed.leg != "" {
		message = diagnosed.detail + ": " + message + "; relaunch with: ao testing relaunch " + diagnosed.leg
	}
	return message
}

// Preserve the interruption marker through the HTTP error boundary so a batch
// cannot continue with stale refs or missing tester setup after a child fails.
func driveEngineError(err error) error {
	failure := apierr.Unavailable("TESTING_ENGINE_FAILED", driveErrorMessage(err))
	var out error = failure
	if isLegDown(err) {
		out = &diagnosedControlError{error: failure, cause: errors.Join(failure, err)}
	}
	var interrupted *engineInterruption
	if errors.As(err, &interrupted) {
		return &engineInterruption{out}
	}
	return out
}

func reportEngineRestart(ctx context.Context, e *Engine, err *error) {
	if e.restartNote != "" {
		// Keep the notice for the next Drive if HTTP can no longer reply.
		if *err == nil || !strings.Contains((*err).Error(), e.restartNote) {
			*err = &engineInterruption{errors.Join(*err, errors.New(e.restartNote))}
		}
		if ctx.Err() == nil {
			e.restartNote = ""
		}
	}
	if captureBlocked(*err) {
		// The envelope renders the first API error's Message. Put the complete
		// aggregate there so an earlier batch error cannot hide state loss.
		*err = driveEngineError(*err)
	}
}

// logAction records read-only commands and refusals without attaching a receipt.
func (s *Service) logAction(runID string, rec *actionRec) {
	if rec.RecordSegment == "" && rec.RecordOffsetMS == nil {
		stamp := s.actionSent(runID, rec.onLeg())
		rec.RecordSegment, rec.RecordOffsetMS, rec.SegmentOffsetMS = stamp.recordSegment, stamp.recordOffsetMS, stamp.recordOffsetMS
	}
	s.mu.Lock()
	rec.Seq = s.nextSeq(runID)
	s.mu.Unlock()
	s.prepareActionOutput(runID, rec, rec.Output)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendAction(runID, rec)
}

// isCoveredClickError recognizes agent-browser's refusal when another painted
// element receives the click. The engine has used both "covered" and
// "intercepted" wording across releases.
func isCoveredClickError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, word := range []string{"covered", "intercept", "obscur", "overlap"} {
		if strings.Contains(msg, word) {
			return true
		}
	}
	return false
}

var interactiveSnapshotRoles = map[string]bool{
	"button": true, "checkbox": true, "combobox": true, "link": true,
	"listbox": true, "menuitem": true, "menuitemcheckbox": true, "menuitemradio": true, "option": true, "radio": true,
	"searchbox": true, "slider": true, "spinbutton": true, "switch": true,
	"tab": true, "textbox": true, "treeitem": true,
}

// compactSnapshot keeps only named interactive refs and caps the returned text
// so a single action does not consume the tester's context window.
func compactSnapshot(snapshot string) string {
	var b strings.Builder
	for _, line := range strings.Split(snapshot, "\n") {
		m := snapRef.FindStringSubmatch(line)
		if len(m) < 2 || !interactiveSnapshotRoles[strings.ToLower(m[1])] {
			continue
		}
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			continue
		}
		if b.Len() > 0 && b.Len()+1+len(line) > 2048 {
			break
		}
		if b.Len() == 0 && len(line) > 2048 {
			return line[:validUTF8Prefix(line, 2048)]
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// snapshotTree accepts agent-browser's optional JSON envelope as well as plain text.
func snapshotTree(output string) string {
	var reply struct{ Data struct{ Snapshot string } }
	if json.Unmarshal([]byte(output), &reply) == nil && reply.Data.Snapshot != "" {
		return reply.Data.Snapshot
	}
	return output
}

func validUTF8Prefix(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return n
}

// addDriveText is the compact human-readable output used by `ao testing`.
// JSON callers still receive the structured fields alongside it.
func addDriveText(res map[string]any, runDir, fallbackSnapshot string) {
	snapshot, _ := res["snapshot"].(string)
	if snapshot == "" {
		snapshot = fallbackSnapshot
	}
	var lines []string
	if rec, ok := res["receipt"].(actionRec); ok {
		after := ""
		if rec.After != "" {
			after = relPath(runDir, rec.After)
		}
		line := fmt.Sprintf("receipt %d: %s %dms", rec.Seq, cmp.Or(after, "(none)"), rec.MS)
		line += " page errors: " + cmp.Or(strings.ReplaceAll(rec.PageErrors, "\n", " | "), "none")
		if rec.Reason != "" {
			line += " reason: " + rec.Reason
		}
		lines = append(lines, line)
		if rec.ConsoleTail != "" {
			lines = append(lines, rec.ConsoleTail)
		}
	}
	if snapshot != "" {
		lines = append(lines, snapshot)
	}
	if action, _ := res["action"].(string); action != "snapshot" || snapshot == "" {
		if output, _ := res["output"].(string); output != "" {
			lines = append(lines, output)
		}
	}
	res["text"] = strings.Join(lines, "\n")
}

func stripPageContentBoundary(out string) string {
	return strings.TrimSpace(pageContentBoundary.ReplaceAllString(out, ""))
}

func outputAction(action string) bool {
	return slices.Contains([]string{"eval", "get", "cdp", "tab"}, action)
}

func pointerTargetAction(action string) bool {
	return slices.Contains([]string{"click", "dblclick", "hover", "tap", "check", "uncheck"}, action)
}

// prepareActionOutput keeps actions.jsonl useful without allowing a browser
// query to fill the run log. Oversized output gets a bounded copy under the run.
func (s *Service) prepareActionOutput(runID string, rec *actionRec, output string) {
	if !outputAction(rec.Action) || output == "" {
		return
	}
	output = s.scrub(stripPageContentBoundary(output))
	if output == "" {
		return
	}
	truncated := len([]byte(output)) > actionOutputCap
	if !truncated {
		rec.Output = output
		return
	}
	setBounded := func(marker string) {
		limit := actionOutputCap - len([]byte(marker)) - 1
		if limit < 0 {
			limit = 0
		}
		rec.Output = clipBytes(output, limit) + "\n" + marker
	}
	setBounded("[truncated; full output unavailable]")
	runDir := filepath.Join(s.store.root, runID)
	rel := filepath.ToSlash(filepath.Join("outputs", fmt.Sprintf("%d-%s.txt", rec.Seq, rec.Action)))
	path := filepath.Join(runDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		rec.Warnings = append(rec.Warnings, "output file: "+err.Error())
		return
	}
	fileOutput := output
	if len([]byte(fileOutput)) > actionOutputFileCap {
		marker := fmt.Sprintf("\n[full output truncated at %d bytes]", actionOutputFileCap)
		fileOutput = clipBytes(fileOutput, actionOutputFileCap-len([]byte(marker))) + marker
	}
	if err := writeAtomic(path, []byte(fileOutput)); err != nil {
		rec.Warnings = append(rec.Warnings, "output file: "+err.Error())
		return
	}
	rec.OutputPath = path
	if truncated {
		setBounded(fmt.Sprintf("[truncated; full output: %s]", rel))
	}
}

func (s *Service) shortActionOutput(action, output string) string {
	if !outputAction(action) {
		return output
	}
	output = s.scrub(stripPageContentBoundary(output))
	if len([]byte(output)) <= actionOutputCap {
		return output
	}
	marker := "[truncated; see outputPath]"
	limit := actionOutputCap - len([]byte(marker)) - 1
	if limit < 0 {
		limit = 0
	}
	return clipBytes(output, limit) + "\n" + marker
}

func clipBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	b := []byte(s)
	if len(b) <= n {
		return s
	}
	b = b[:n]
	return strings.ToValidUTF8(string(b), "")
}

// clickAt is where a click lands: the element's centre and the viewport it is in, both in CSS pixels, so the point
// can be placed on a screenshot of any size.
type clickAt struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// ElementBox is the CSS-pixel box returned by `get box` for a clicked element.
// It is kept separately from clickAt: clickAt also carries the viewport used to
// draw the pointer ring on an after-shot.
type ElementBox struct {
	X              float64 `json:"x"`
	Y              float64 `json:"y"`
	W              float64 `json:"w"`
	H              float64 `json:"h"`
	ViewportWidth  float64 `json:"viewportWidth,omitempty"`
	ViewportHeight float64 `json:"viewportHeight,omitempty"`
}

const pointTimeout = 5 * time.Second

// boxField reads one field (x, y, width, height) from `get box` output, whether it prints JSON or "x: 1 y: 2".
func boxField(out, name string) (float64, bool) {
	m := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `[^\w\s+-]{0,3}\s*([-+]?\d+(?:\.\d+)?)`).FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, err == nil
}

func isTabDrift(err error) bool {
	return errors.Is(err, errTabDrift)
}

// clickTarget reads the element box and viewport before a click. The box is
// returned even when the viewport lookup fails, so a refused click can still
// leave useful evidence.
func (e *Engine) clickTarget(ctx context.Context, ref string) (*clickAt, *ElementBox, *ElementBox, error) {
	box, err := e.Run(ctx, pointTimeout, "get", "box", ref)
	if err != nil {
		return nil, nil, nil, err
	}
	x, okx := boxField(box, "x")
	y, oky := boxField(box, "y")
	w, okw := boxField(box, "width")
	h, okh := boxField(box, "height")
	if !okx || !oky || !okw || !okh {
		return nil, nil, nil, errors.New("get box returned no usable coordinates")
	}
	element := &ElementBox{X: x, Y: y, W: w, H: h}
	target := e.refs[ref]
	iframe := target.inIframe || strings.EqualFold(strings.TrimSpace(target.role), "iframe")
	ctx, cancel := context.WithTimeout(ctx, pointTimeout)
	defer cancel()
	p, err := e.pageCDP(ctx)
	if err != nil {
		return nil, element, nil, err
	}
	if p.conn != nil {
		defer func() { _ = p.conn.CloseNow() }()
	}
	var size []float64
	if err := p.evaluate(ctx, "[innerWidth, innerHeight]", &size); err != nil {
		return nil, element, nil, err
	}
	if len(size) != 2 {
		return nil, element, nil, errors.New("viewport size was not returned")
	}
	vw, vh := size[0], size[1]
	if vw <= 0 || vh <= 0 {
		return nil, element, nil, errors.New("viewport size was invalid")
	}
	element.ViewportWidth, element.ViewportHeight = vw, vh
	point := element
	if iframe || w <= 0 || h <= 0 || x < 0 || y < 0 || x+w > vw || y+h > vh {
		if _, err := e.Run(ctx, pointTimeout, "scrollintoview", ref); err != nil {
			return nil, element, nil, err
		}
		moved, err := e.Run(ctx, pointTimeout, "get", "box", ref)
		if err != nil {
			return nil, element, nil, err
		}
		mx, mokx := boxField(moved, "x")
		my, moky := boxField(moved, "y")
		mw, mokw := boxField(moved, "width")
		mh, mokh := boxField(moved, "height")
		if !mokx || !moky || !mokw || !mokh {
			return nil, element, nil, errors.New("get box after scroll returned no usable coordinates")
		}
		point = &ElementBox{X: mx, Y: my, W: mw, H: mh, ViewportWidth: vw, ViewportHeight: vh}
	}
	return &clickAt{X: point.X + point.W/2, Y: point.Y + point.H/2, W: vw, H: vh}, element, point, nil
}

// shoot saves a screenshot and, on macOS, downsizes it with the built-in sips (no Go image dependency).
func (s *Service) shoot(ctx context.Context, e *Engine, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if _, err := e.Run(ctx, cmdTimeout, "screenshot", path); err != nil {
		return err
	}
	if err := e.flatten(ctx, path); err != nil {
		return err
	}
	_, _ = s.runner.Run(ctx, "", nil, "sips", "-Z", "1320", path)
	return nil
}

// actionRec is one line of actions.jsonl.
type actionRec struct {
	Seq              int         `json:"seq"`
	Leg              string      `json:"leg"`
	Action           string      `json:"action"`
	Planned          bool        `json:"planned,omitempty"`
	Args             []string    `json:"args,omitempty"`
	Output           string      `json:"output,omitempty"`
	OutputPath       string      `json:"outputPath,omitempty"`
	Before           string      `json:"before"`
	After            string      `json:"after"`
	ConsoleTail      string      `json:"consoleTail"`
	At               time.Time   `json:"at"`
	SentAt           time.Time   `json:"sentAt,omitzero"`
	EngineGeneration uint64      `json:"engineGeneration,omitempty"`
	RecordOffsetMS   *int64      `json:"recordOffsetMs,omitempty"`
	RecordSegment    string      `json:"recordSegment,omitempty"`
	SegmentOffsetMS  *int64      `json:"segmentOffsetMs,omitempty"`
	MS               int64       `json:"ms"`
	Click            *clickAt    `json:"click,omitempty"` // pointer action: where the pointer went
	Box              *ElementBox `json:"box,omitempty"`   // pointer action: the element's CSS-pixel box
	PageErrors       string      `json:"pageErrors,omitempty"`
	Reason           string      `json:"reason,omitempty"`
	Error            string      `json:"error,omitempty"`
	Unassigned       bool        `json:"unassigned,omitempty"` // no check on this leg was open when it happened
	Warnings         []string    `json:"warnings,omitempty"`
	Control          *LegControl `json:"control,omitempty"`
	TargetRole       string      `json:"targetRole,omitempty"` // from the last snapshot: the element acted on, for plain captions
	TargetName       string      `json:"targetName,omitempty"`
}

// receipt settles the UI, takes the after-shot and the console tail, appends the record to
// actions.jsonl and attaches it to the open check. Its failures are warnings: the action already happened.
func (s *Service) receipt(ctx context.Context, runID string, e *Engine, leg, action string, args []string, target refInfo, click *clickAt, box, receiptBox *ElementBox, stamp actionTime, took time.Duration, reason string, output ...string) (actionRec, error) {
	// The action or HTTP deadline may already have expired. Receipt probes have
	// their own bounded lifetime, and Engine.Run still observes engine closure.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	s.mu.Lock()
	seq := s.nextSeq(runID)
	s.mu.Unlock()
	rec := actionRec{Seq: seq, Leg: leg, Action: action, Args: args, Before: e.last, At: s.clock(), SentAt: stamp.sentAt, EngineGeneration: stamp.generation, RecordOffsetMS: stamp.recordOffsetMS, RecordSegment: stamp.recordSegment, SegmentOffsetMS: stamp.recordOffsetMS, MS: took.Milliseconds(), TargetRole: target.role, TargetName: target.name, Click: click, Box: box, Reason: reason}
	if outputAction(action) && len(output) > 0 {
		rec.Output = output[0]
	}
	warn := func(what string, err error) { rec.Warnings = append(rec.Warnings, what+": "+err.Error()) }
	finish := func() actionRec {
		s.prepareActionOutput(runID, &rec, rec.Output)
		if receiptBox == nil {
			receiptBox = rec.Box
		}
		s.attach(runID, &rec, Receipt{Seq: seq, Leg: leg, Action: action, Args: args, BeforePath: rec.Before, AfterPath: rec.After, ConsoleTail: rec.ConsoleTail, At: rec.At, MS: rec.MS, TargetRole: target.role, TargetName: target.name, Box: receiptBox, Reason: rec.Reason, PageErrors: rec.PageErrors})
		return rec
	}

	select {
	case <-time.After(settleDelay):
	case <-ctx.Done():
	}
	// Bound the operation in the daemon too: killing only its client leaves
	// the daemon's state lock held until the default load timeout expires.
	if _, err := e.Run(ctx, cmdTimeout, "wait", "--load", "load", "--timeout", strconv.FormatInt(settleLoad.Milliseconds(), 10)); err != nil {
		if captureBlocked(err) {
			warn("load", err)
			if rec.Reason == "" {
				rec.Reason = driveErrorMessage(err)
			}
			return finish(), err
		}
		if ctx.Err() == nil {
			warn("load", err)
		}
	}
	rec.After = filepath.Join(e.RunDir, "receipts", fmt.Sprintf("%d-after.png", seq))
	err := s.shoot(ctx, e, rec.After)
	if captureBlocked(err) {
		if !fileExists(rec.After) {
			rec.After = ""
		}
		warn("after-shot", err)
		if rec.Reason == "" {
			rec.Reason = driveErrorMessage(err)
		}
		return finish(), err
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil { // a window the OS hid often paints on the next frame
		select {
		case <-time.After(shotRetry):
			err = s.shoot(ctx, e, rec.After)
		case <-ctx.Done():
		}
	}
	if captureBlocked(err) {
		if !fileExists(rec.After) {
			rec.After = ""
		}
		warn("after-shot", err)
		if rec.Reason == "" {
			rec.Reason = driveErrorMessage(err)
		}
		return finish(), err
	}
	if err != nil {
		if !fileExists(rec.After) {
			rec.After = ""
		} else {
			e.last = rec.After
		}
		warn("after-shot", err)
	} else {
		e.last = rec.After
	}
	if out, err := e.Run(ctx, cmdTimeout, "errors"); err != nil {
		warn("page errors", err)
		if captureBlocked(err) {
			if rec.Reason == "" {
				rec.Reason = driveErrorMessage(err)
			}
			return finish(), err
		}
	} else {
		rec.PageErrors = compactErrors(s.scrub(stripPageContentBoundary(out)))
	}
	if out, err := e.Run(ctx, cmdTimeout, "console"); err != nil {
		warn("console", err)
		if captureBlocked(err) {
			if rec.Reason == "" {
				rec.Reason = driveErrorMessage(err)
			}
			return finish(), err
		}
	} else {
		rec.ConsoleTail = consoleErrorTail(s.scrub(stripPageContentBoundary(out)))
	}
	return finish(), nil
}

// noCaptureReceipt records a failed action without settling, capturing, or
// probing the browser again. The tester still gets a receipt it can cite.
func (s *Service) noCaptureReceipt(runID string, e *Engine, leg, action string, args []string, target refInfo, stamp actionTime, took time.Duration, reason string, output string) actionRec {
	if stamp.recordOffsetMS == nil {
		// A refusal has no send time, but its hide event still needs the
		// recording clock when wall time has moved.
		sample := s.actionSent(runID, leg)
		stamp.recordOffsetMS, stamp.recordSegment = sample.recordOffsetMS, sample.recordSegment
	}
	s.mu.Lock()
	seq := s.nextSeq(runID)
	s.mu.Unlock()
	rec := actionRec{Seq: seq, Leg: leg, Action: action, Args: args, Before: e.last, At: s.clock(), SentAt: stamp.sentAt, EngineGeneration: stamp.generation, RecordOffsetMS: stamp.recordOffsetMS, RecordSegment: stamp.recordSegment, SegmentOffsetMS: stamp.recordOffsetMS, MS: took.Milliseconds(), TargetRole: target.role, TargetName: target.name, Reason: reason}
	if outputAction(action) {
		rec.Output = output
	}
	s.prepareActionOutput(runID, &rec, output)
	s.attach(runID, &rec, Receipt{Seq: seq, Leg: leg, Action: action, Args: args, BeforePath: rec.Before, At: rec.At, MS: rec.MS, TargetRole: target.role, TargetName: target.name, Reason: reason})
	return rec
}

func compactErrors(out string) string {
	out = strings.TrimSpace(out)
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimRight(out, ".")))
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "", "no error", "no errors", "no page error", "no page errors", "no errors found", "no page errors found":
		return ""
	}
	return clip(out, 2000)
}

// consoleErrorTail keeps the useful part of the console without returning a
// normal log tail on every action. A page error is reported separately.
func consoleErrorTail(out string) string {
	tail := lastLines([]byte(out), 20)
	low := strings.ToLower(tail)
	if !strings.Contains(low, "error") && !strings.Contains(low, "exception") && !strings.Contains(low, "uncaught") && !strings.Contains(low, "failed") {
		return ""
	}
	return clip(tail, 4000)
}

// attach adds r to the open check on its leg (otherwise rec is marked unassigned) and appends rec to
// actions.jsonl. Failures are warnings on rec: the action already happened.
func (s *Service) attach(runID string, rec *actionRec, r Receipt) {
	warn := func(what string, err error) { rec.Warnings = append(rec.Warnings, what+": "+err.Error()) }
	s.mu.Lock()
	defer s.mu.Unlock()
	checks, err := s.store.Checks(runID)
	if i := slices.IndexFunc(checks, func(c Check) bool { return c.State == CheckRunning && c.onLeg() == r.onLeg() }); err == nil && i >= 0 {
		checks[i].Receipts = append(checks[i].Receipts, r)
		err = s.store.SaveChecks(runID, checks)
	} else {
		rec.Unassigned = true
	}
	if err != nil {
		warn("check", err)
	}
	s.appendAction(runID, rec)
}

// appendAction writes a command to the action log. The caller holds s.mu.
func (s *Service) appendAction(runID string, rec *actionRec) {
	warn := func(what string, err error) { rec.Warnings = append(rec.Warnings, what+": "+err.Error()) }
	if path, err := s.store.path(runID, "actions.jsonl"); err != nil {
		warn("actions.jsonl", err)
	} else if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		warn("actions.jsonl", err)
	} else {
		if err := json.NewEncoder(f).Encode(*rec); err != nil {
			warn("actions.jsonl", err)
		}
		_ = f.Close()
	}
}

// nextSeq numbers a run's receipts from 1, continuing after a daemon restart. Callers hold s.mu.
func (s *Service) nextSeq(runID string) int {
	n, ok := s.seqs[runID]
	if path, err := s.store.path(runID, "actions.jsonl"); !ok && err == nil {
		data, _ := os.ReadFile(path)
		n = bytes.Count(data, []byte("\n"))
	}
	s.seqs[runID] = n + 1
	return n + 1
}

// engineFor returns the leg's engine, rebuilding it from run.json after a daemon restart.
func (s *Service) engineFor(runID string, leg Leg) *Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := runID + "/" + leg.Name
	if s.engines[key] == nil {
		e := newEngine(*leg.Engine, s.engExec)
		s.initEngineCallbacks(runID, leg, e)
		s.engines[key] = e
	}
	return s.engines[key]
}

// initEngineCallbacks runs before the engine is shared. Lookups never rewrite
// callbacks that commands invoke while holding the engine's mutex.
func (s *Service) initEngineCallbacks(runID string, leg Leg, e *Engine) {
	e.leg = leg.Name
	e.diagnose = func(ctx context.Context) (LegControl, error) { return s.controlDiagnosis(ctx, runID, leg.Name) }
	e.onControl = func(control LegControl) error { return s.setControl(runID, leg.Name, control, e) }
	var since int
	var boundaryErr error
	e.onInit = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		since, boundaryErr = setupLogBoundary(e.RunDir)
	}
	e.lostState = func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return loggedDaemonState(e.RunDir, leg.Name, since, e.generation, boundaryErr, e.pendingAction)
	}
	e.onRestart = func(message string) {
		rec := actionRec{Leg: leg.Name, Action: "browser-restart", At: s.clock(), Reason: message}
		s.logAction(runID, &rec)
	}
	e.onReattach = func(oldTab, newTab, appURL string) {
		rec := actionRec{Leg: leg.Name, Action: "reattach", Args: []string{oldTab, newTab}, At: s.clock(), Reason: "app tab reattached at " + s.scrub(diagnosticURL(appURL))}
		s.logAction(runID, &rec)
	}
	e.abortAdvice = func() string {
		message := "The " + leg.Name + " leg's browser control is unusable."
		run, err := s.store.Get(runID)
		if err != nil {
			return message + " Could not read available legs: " + err.Error() + "."
		}
		for _, other := range run.Legs {
			if other.Name != leg.Name && other.State == "running" && other.Engine != nil {
				message += " Finish other checks on the " + other.Name + " leg."
			}
		}
		return message
	}
}

// attachEngine gives a freshly launched leg its engine. The caller owns failure
// cleanup, which preserves profile/data for relaunch and wipes it for launch.
func (s *Service) attachEngine(ctx context.Context, runID string, leg Leg) (Leg, error) {
	if err := s.closeLegEngine(runID, leg); err != nil {
		leg.State = "failed"
		return leg, err
	}
	appOrigin := ""
	if leg.Engine != nil {
		appOrigin = leg.Engine.AppOrigin
	}
	e, err := NewEngine(s.binary, filepath.Join(s.store.root, runID), leg.Name, leg.DebugPort, s.engExec)
	if err != nil {
		leg.State = "failed"
		return leg, err
	}
	e.AppOrigin = appOrigin
	leg.Engine = &e.EngineInfo
	s.initEngineCallbacks(runID, leg, e)
	s.mu.Lock()
	s.engines[runID+"/"+leg.Name] = e
	s.mu.Unlock()
	return leg, nil
}

// closeEngines closes every engine of a run, whether or not this daemon created it.
func (s *Service) closeEngines(runID string) {
	run, err := s.store.Get(runID)
	if err != nil {
		return
	}
	for _, l := range run.Legs {
		if l.Engine != nil {
			s.engineFor(runID, l).Close(s.sys)
		}
	}
	s.mu.Lock()
	for k := range s.engines {
		if strings.HasPrefix(k, runID+"/") {
			delete(s.engines, k)
		}
	}
	s.mu.Unlock()
}

// lastShot is the newest after-shot on leg among the receipts of every check called name.
func lastShot(checks []Check, name, leg string) (path string) {
	newest := 0
	for _, c := range checks {
		for _, r := range c.Receipts {
			if strings.EqualFold(c.Name, name) && r.onLeg() == leg && r.AfterPath != "" && r.Seq > newest {
				path, newest = r.AfterPath, r.Seq
			}
		}
	}
	return path
}

// Compare pairs a check's last after-shot on each leg and has the head engine pixel-diff the base shot against
// the head page (the engine can only diff a file against its live page). It reruns nothing; the diff image
// lands in receipts/ and its path on the check.
func (s *Service) Compare(ctx context.Context, sessionID, checkID string) (map[string]any, error) {
	checkID = strings.TrimSpace(checkID)
	if checkID == "" {
		return nil, invalid("TESTING_ARGS_INVALID", "wrong number of arguments; usage: compare <checkID>")
	}
	s.mu.Lock()
	run, err := s.load(sessionID)
	var checks []Check
	if err == nil {
		checks, err = s.store.Checks(run.ID)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(checks, func(c Check) bool { return c.ID == checkID })
	if i < 0 {
		return nil, apierr.NotFound("TESTING_CHECK_NOT_FOUND", "no check "+fmt.Sprintf("%q", checkID))
	}
	head, base := lastShot(checks, checks[i].Name, "head"), lastShot(checks, checks[i].Name, "base")
	if head == "" || base == "" {
		return nil, conflict("TESTING_COMPARE_INCOMPLETE", fmt.Sprintf("check %s needs an after-shot on both legs to compare (head %t, base %t); act on it with --leg head and --leg base", checkID, head != "", base != ""))
	}
	run, leg, done, err := s.beginLegCommand(sessionID, "head")
	if err != nil {
		return nil, err
	}
	defer done()
	out := filepath.Join(s.store.root, run.ID, "receipts", "diff-"+checkID+".png")
	_ = os.Remove(out) // an older image must not stand in for this one
	e := s.engineFor(run.ID, leg)
	e.mu.Lock()
	text, err := e.Run(ctx, cmdTimeout, "diff", "screenshot", "--baseline", base, "--output", out)
	if err == nil && e.life.Err() == nil {
		err = s.setControl(run.ID, leg.Name, LegControl{State: "ok"}, e)
	}
	e.mu.Unlock()
	if err != nil {
		return nil, driveEngineError(err)
	}
	res := map[string]any{"check": checkID, "base": base, "head": head, "output": text}
	if !fileExists(out) { // the engine may skip the image (nothing differs, or it could not diff); the check keeps no path to a file that is not there
		res["diff"], res["warning"] = "", "the engine wrote no diff image; the check keeps no diff"
		return res, nil
	}
	if _, err := s.updateCheck(sessionID, checkID, func(c *Check, _ []Check) error { c.Diff = out; return nil }); err != nil {
		return nil, err
	}
	res["diff"] = out
	return res, nil
}
