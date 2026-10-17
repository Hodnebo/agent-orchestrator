package litmus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const nativeDialogDetailPrefix = "blocked by a native dialog:"

// controlDiagnosis gives later dialog-record and native-window probes one place
// in the diagnosis order, between process identity and the unresponsive case.
// Those probes belong to 24b; 24a checks the owned Electron identity only.
func (s *Service) controlDiagnosis(_ context.Context, runID, leg string) (LegControl, error) {
	o, err := readOwned(s.store.legDir(runID, leg))
	if err != nil {
		return LegControl{}, fmt.Errorf("read leg ownership for diagnosis: %w", err)
	}
	if o.Electron.PID <= 0 || o.Electron.Start.IsZero() {
		return LegControl{}, errors.New("owned Electron PID or start time is missing")
	}
	if o.Electron.PID > 0 {
		procs, err := s.sys.procs()
		if err != nil {
			return LegControl{}, fmt.Errorf("inspect Electron for diagnosis: %w", err)
		}
		start, known := s.sys.startTime(o.Electron.PID)
		_, listed := procs[o.Electron.PID]
		if !known && !listed || known && !o.Electron.Start.IsZero() && !start.Equal(o.Electron.Start) {
			control := LegControl{State: "lost", Cause: "app_exited", Detail: fmt.Sprintf("the %s app exited (pid %d)", leg, o.Electron.PID)}
			control.PublicDetail = publicControlDetailFromCause(control.Cause, control.Detail)
			return control, nil
		}
		if !known {
			return LegControl{}, fmt.Errorf("cannot verify Electron pid %d start time", o.Electron.PID)
		}
	}
	// Insert open-dialog record lookup here, then helper `windows` layer-8 lookup.
	control := LegControl{State: "lost", Cause: "app_unresponsive", Detail: fmt.Sprintf("the %s app is not responding (main process alive, no native dialog open)", leg)}
	control.PublicDetail = publicControlDetailFromCause(control.Cause, control.Detail)
	return control, nil
}

// setControl persists transitions and logs their own sequence number. Repeated
// diagnoses retain the first loss boundary until an actual command succeeds.
func (s *Service) setControl(runID, leg string, control LegControl, engine *Engine) error {
	if control.State == "lost" {
		control.PublicDetail = publicControlDetailFromCause(control.Cause, control.Detail)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if engine != nil && s.engines[runID+"/"+leg] != engine {
		return nil
	}
	run, err := s.store.Get(runID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg })
	if i < 0 {
		return fmt.Errorf("leg %s missing from run %s", leg, runID)
	}
	old := run.Legs[i].Control
	if old != nil && old.State == control.State {
		if control.State == "ok" || old.Cause == control.Cause && old.Detail == control.Detail && old.PublicDetail == control.PublicDetail {
			return nil
		}
		control.Since = old.Since
	}
	if control.State == "ok" && old == nil {
		return nil
	}
	seq := s.nextSeq(runID)
	if control.State == "lost" && control.Since == 0 {
		control.Since = seq
	}
	run.Legs[i].Control = &control
	if err := s.store.Save(run); err != nil {
		return err
	}
	rec := actionRec{Seq: seq, Leg: leg, Action: "control", At: s.clock(), Control: &control}
	s.appendAction(runID, &rec)
	if len(rec.Warnings) > 0 {
		return errors.New(strings.Join(rec.Warnings, "; "))
	}
	return nil
}

// diagnosedControlError preserves the cause and carries the diagnosis to the
// tester error boundary. Internal callers can record the cause without advice.
type diagnosedControlError struct {
	error
	cause  error
	leg    string
	detail string
}

func (e *diagnosedControlError) Error() string {
	return strings.TrimPrefix(e.error.Error(), errLegDown.Error()+": ")
}
func (e *diagnosedControlError) Unwrap() error { return e.cause }

func (e *Engine) unavailable(ctx context.Context, cause error) error {
	if e.diagnose == nil {
		return cause
	}
	// A known daemon replacement interrupts setup before the app probe runs.
	// Preserve its dispatch/state-loss notice; it says nothing about app health.
	var unsent *unsentAction
	if e.ierr == nil && !isLegDown(cause) && (e.restartNote != "" || errors.As(cause, &unsent)) {
		return cause
	}
	control, err := e.diagnose(ctx)
	if err != nil {
		control = LegControl{State: "lost", Cause: "browser_control_unusable", Detail: fmt.Sprintf("cannot diagnose the %s app: %v", e.leg, err)}
	}
	if control.State == "lost" {
		control.PublicDetail = publicControlDetailFromCause(control.Cause, control.Detail)
	}
	if e.onControl != nil {
		if err := e.onControl(control); err != nil {
			return errors.Join(cause, fmt.Errorf("save leg control loss: %w", err))
		}
	}
	return &diagnosedControlError{error: cause, cause: errors.Join(errLegDown, cause), leg: e.leg, detail: control.Detail}
}

// publicControlDetail returns the persisted public sentence, or derives one
// for runs written before PublicDetail was added. Raw Detail stays diagnostic.
func publicControlDetail(control LegControl) string {
	if detail := strings.TrimSpace(control.PublicDetail); detail != "" {
		return detail
	}
	return publicControlDetailFromCause(control.Cause, control.Detail)
}

func publicControlDetailFromCause(cause, detail string) string {
	switch cause {
	case "native_dialog":
		title, buttons, ok := recordedNativeDialog(detail)
		if !ok {
			return "The app is blocked by a native dialog."
		}
		if title == "" && len(buttons) == 0 {
			return "The app is blocked by a native dialog."
		}
		var parts []string
		if title != "" {
			parts = append(parts, `title "`+title+`"`)
		}
		if len(buttons) > 0 {
			parts = append(parts, "buttons "+strings.Join(buttons, ", "))
		}
		return "The app is blocked by a native dialog with " + strings.Join(parts, " and ") + "."
	case "app_exited":
		return "The app exited."
	case "app_unresponsive":
		return "The app is not responding."
	case "browser_control_unusable":
		return "Browser control is unavailable."
	default:
		return ""
	}
}

func recordedNativeDialog(detail string) (string, []string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(detail), nativeDialogDetailPrefix)
	if !ok {
		return "", nil, false
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 || rest[0] != '"' {
		return "", nil, false
	}
	end := -1
	for i := 1; i < len(rest); i++ {
		if rest[i] == '\\' {
			i++
			continue
		}
		if rest[i] == '"' {
			end = i
			break
		}
	}
	if end < 0 {
		return "", nil, false
	}
	title, err := strconv.Unquote(rest[:end+1])
	if err != nil {
		return "", nil, false
	}
	rest = strings.TrimSpace(rest[end+1:])
	if rest == "" {
		return strings.TrimSpace(title), nil, true
	}
	buttonsText, ok := strings.CutPrefix(rest, "buttons ")
	if !ok {
		return "", nil, false
	}
	var buttons []string
	for buttonsText = strings.TrimSpace(buttonsText); buttonsText != ""; buttonsText = strings.TrimSpace(buttonsText) {
		if buttonsText[0] != '[' {
			return "", nil, false
		}
		end := strings.IndexByte(buttonsText, ']')
		if end < 0 {
			return "", nil, false
		}
		button := strings.TrimSpace(buttonsText[1:end])
		if button == "" {
			return "", nil, false
		}
		buttons = append(buttons, button)
		buttonsText = buttonsText[end+1:]
	}
	return strings.TrimSpace(title), buttons, true
}
