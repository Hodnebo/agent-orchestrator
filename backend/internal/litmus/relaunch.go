package litmus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const plannedRelaunchReason = "tester requested relaunch"

// closeLegEngine retains the canceled entry until attachEngine replaces it, so
// concurrent commands cannot rebuild an engine against the old debug port.
func (s *Service) closeLegEngine(runID string, leg Leg) error {
	s.mu.Lock()
	e := s.engines[runID+"/"+leg.Name]
	s.mu.Unlock()
	if e == nil && leg.Engine != nil {
		e = s.engineFor(runID, leg)
	}
	if e != nil {
		return e.Close(s.sys)
	}
	return nil
}

// Relaunch replaces one launched app and engine, preserving its profile/data.
func (s *Service) Relaunch(ctx context.Context, sessionID, leg string) (Run, error) {
	var relaunchSeq int
	planned := false
	record := func(runID, reason string, cause error) error {
		stamp := s.actionSent(runID, leg)
		rec := actionRec{Leg: leg, Action: "relaunch", Planned: planned, At: s.clock(), Reason: s.scrub(reason), RecordSegment: stamp.recordSegment,
			RecordOffsetMS: stamp.recordOffsetMS, SegmentOffsetMS: stamp.recordOffsetMS}
		if cause != nil {
			rec.Error = s.scrub(cause.Error())
		}
		s.logAction(runID, &rec)
		relaunchSeq = rec.Seq
		if len(rec.Warnings) > 0 {
			return fmt.Errorf("record relaunch: %s", strings.Join(rec.Warnings, "; "))
		}
		return nil
	}
	auditStoredFailure := func(reason string, cause error) error {
		id, idErr := runID(sessionID)
		if idErr != nil {
			return cause
		}
		if _, readErr := s.store.Get(id); readErr != nil {
			if os.IsNotExist(readErr) {
				return cause
			}
			return errors.Join(cause, fmt.Errorf("audit relaunch: read run: %w", readErr))
		}
		if err := record(id, reason, cause); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	if leg != "head" && leg != "base" {
		return Run{}, auditStoredFailure("invalid leg", invalid("TESTING_LEG_INVALID", "leg must be base or head"))
	}
	run, done, err := s.begin(sessionID, "relaunch "+leg)
	if err != nil {
		return Run{}, auditStoredFailure("begin relaunch", err)
	}
	defer done()
	audit := func(reason string, cause error) error {
		return record(run.ID, reason, cause)
	}
	recordingGap := "The " + leg + " recording stopped at relaunch; later actions have screenshot receipts only."
	gapAdded := false
	fail := func(reason string, cause error) (Run, error) {
		auditErr := audit(reason, cause)
		return Run{}, errors.Join(cause, auditErr)
	}
	i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg })
	if i < 0 {
		return fail("leg was never launched", conflict("TESTING_LEG_NOT_LAUNCHED", "leg "+leg+" was never launched; run `ao testing launch "+leg+"`"))
	}
	requestedWhileHealthy := run.Legs[i].Control != nil && run.Legs[i].Control.State == "ok"
	dir := s.store.legDir(run.ID, leg)
	if _, err := readOwned(dir); err != nil {
		if os.IsNotExist(err) && run.Legs[i].PID == 0 && run.Legs[i].Engine == nil {
			return fail("leg was never launched", conflict("TESTING_LEG_NOT_LAUNCHED", "leg "+leg+" was never launched; run `ao testing launch "+leg+"`"))
		}
		return fail("read leg ownership", conflict("TESTING_RELAUNCH_FAILED", "relaunch "+leg+" refused: read leg ownership: "+err.Error()))
	}
	recipe, err := s.store.LoadRecipe(run.Repo)
	if err != nil {
		return fail("load recipe", err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launchBudget(len(recipe.Seed))+10*time.Second)
	defer cancel()
	closeErr := s.closeLegEngine(run.ID, run.Legs[i])
	if r := s.takeRecorder(run.ID, leg); r != nil {
		closeErr = errors.Join(closeErr, r.stop(s.clock()))
	}
	// Close waits for the prior command; use the control state it actually left,
	// rather than the snapshot from before that command completed.
	s.mu.Lock()
	current, readErr := s.store.Get(run.ID)
	s.mu.Unlock()
	if readErr != nil {
		return fail("read current leg state", errors.Join(readErr, closeErr))
	}
	control := current.Legs[i].Control
	planned = requestedWhileHealthy && control != nil && control.State == "ok"
	reason := plannedRelaunchReason
	if !planned {
		reason = "the " + leg + " app control stopped for relaunch"
		if control != nil && control.State == "lost" {
			reason = control.Detail
		} else if prior := run.Legs[i].Control; prior != nil && prior.State == "lost" {
			reason = prior.Detail
		}
	}
	// Either marker proves this leg had a capture before relaunch. A fresh leg
	// must not initialize agent-browser just to create a recorder.
	recordingBeforeRelaunch := !current.Legs[i].RecordStartedAt.IsZero() || len(current.Legs[i].RecordSegments) > 0
	if recordingBeforeRelaunch && !planned {
		current.Legs[i].RecordStoppedAt = s.clock()
		_, err := s.update(run.ID, func(cur *Run) {
			cur.Legs[i].RecordStoppedAt = current.Legs[i].RecordStoppedAt
			if !slices.Contains(cur.Gaps, recordingGap) {
				cur.Gaps = append(cur.Gaps, recordingGap)
				gapAdded = true
			}
		})
		if err != nil {
			return fail("save recording gap", err)
		}
	}
	if !planned && (control == nil || control.State != "lost") {
		if err := s.setControl(run.ID, leg, LegControl{State: "lost", Cause: "browser_control_unusable", Detail: "the " + leg + " app control stopped for relaunch"}, nil); err != nil {
			return fail("save control state", err)
		}
	}
	if closeErr != nil {
		return fail("close engine", conflict("TESTING_RELAUNCH_FAILED", "relaunch "+leg+" refused: close engine: "+closeErr.Error()))
	}
	if err := killLeg(ctx, s.sys, dir); err != nil {
		return fail("kill leg", conflict("TESTING_RELAUNCH_FAILED", "relaunch "+leg+" refused: "+err.Error()))
	}
	// Rotating only after death prevents old processes from writing into the new log.
	log := filepath.Join(dir, "native-dialogs.jsonl")
	if err := rotateNativeDialogLog(log); err != nil {
		return fail("rotate native dialog log", conflict("TESTING_RELAUNCH_FAILED", "relaunch "+leg+" failed: rotate native dialog log: "+err.Error()))
	}
	var seedGap string
	prior := current.Legs[i]
	prior.State = "failed"
	if err := s.resetTeardown(run.ID); err != nil {
		return fail("reset teardown", err)
	}
	started, launchErr := s.startLeg(ctx, run.ID, run.PRNumber, recipe, prior, newLaps(), &seedGap, true, reason)
	if launchErr == nil {
		started, launchErr = s.attachEngine(ctx, run.ID, started)
		if launchErr != nil {
			launchErr = errors.Join(launchErr, killLeg(ctx, s.sys, dir))
		}
	}
	var recordingErr error
	if launchErr == nil && recordingBeforeRelaunch {
		started, recordingErr = s.startRecordingSegment(ctx, run.ID, started)
		if recordingErr != nil {
			s.engineLog(run.ID, leg, "recording restart: "+s.scrub(recordingErr.Error()))
		}
	}
	if launchErr != nil && planned {
		loss := LegControl{
			State:  "lost",
			Cause:  "browser_control_unusable",
			Detail: "the " + leg + " app control was unavailable after relaunch: " + s.scrub(launchErr.Error()),
		}
		if err := s.setControl(run.ID, leg, loss, nil); err != nil {
			launchErr = errors.Join(launchErr, fmt.Errorf("save control state after relaunch: %w", err))
		}
	}
	out, saveErr := s.update(run.ID, func(cur *Run) {
		j := slices.IndexFunc(cur.Legs, func(l Leg) bool { return l.Name == leg })
		if j >= 0 {
			started.Control = cur.Legs[j].Control
			cur.Legs[j] = started
		}
		if launchErr == nil && !cur.State.Closed() {
			cur.State = RunRunning
		}
		if seedGap = s.scrub(seedGap); seedGap != "" && !slices.Contains(cur.Gaps, seedGap) {
			cur.Gaps = append(cur.Gaps, seedGap)
		}
	})
	if launchErr != nil {
		failure := conflict("TESTING_RELAUNCH_FAILED", "relaunch "+leg+" failed: "+launchErr.Error())
		_, err := fail("launch", failure)
		return Run{}, errors.Join(err, saveErr)
	}
	if saveErr != nil {
		return fail("save relaunched leg", saveErr)
	}
	if err := audit(s.scrub(reason), nil); err != nil {
		return out, err
	}
	return s.update(run.ID, func(cur *Run) {
		if gapAdded {
			cur.Gaps = slices.DeleteFunc(cur.Gaps, func(g string) bool { return g == recordingGap })
		}
		if recordingErr != nil {
			cur.Gaps = append(cur.Gaps, "Native dialog recorder unavailable for this leg")
		} else if !planned && len(started.RecordSegments) > 1 {
			cur.Gaps = append(cur.Gaps, fmt.Sprintf("The %s app was relaunched at action %d; the video jumps there.", leg, relaunchSeq))
		}
	})
}

// rotateNativeDialogLog moves each existing backup to the first available
// numbered slot before replacing .previous. This keeps every prior relaunch's
// native-dialog log when a run is relaunched repeatedly.
func rotateNativeDialogLog(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	previous := path + ".previous"
	if _, err := os.Stat(previous); err == nil {
		for n := 1; ; n++ {
			backup := fmt.Sprintf("%s.previous-%d", path, n)
			if _, err := os.Stat(backup); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := os.Rename(previous, backup); err != nil {
				return err
			}
			break
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(path, previous)
}
