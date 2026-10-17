package litmus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type daemonIdentity struct {
	pid   int
	start time.Time
}

func (e *Engine) daemonIdentity() (daemonIdentity, error) {
	if e.identity != nil {
		return e.identity()
	}
	if e.SocketDir == "" { // in-process engines have no daemon
		return daemonIdentity{}, nil
	}
	return e.readDaemonIdentity(daemonStartTime)
}

func daemonStartTime(pid int) (time.Time, error) {
	if started, ok := realSys().startTime(pid); ok {
		return started, nil
	}
	// A failed ps probe alone does not prove the process exited. Signal 0
	// checks existence without sending a signal or changing the process.
	p, err := os.FindProcess(pid)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = p.Release() }()
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, errors.New("could not read the live agent-browser daemon's start time")
}

func (e *Engine) readDaemonIdentity(startTime func(int) (time.Time, error)) (daemonIdentity, error) {
	pidPath := filepath.Join(e.SocketDir, "namespaces", e.Namespace, "run", e.Session+".pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return daemonIdentity{}, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return daemonIdentity{}, errors.New("invalid agent-browser daemon PID")
	}
	info, err := os.Stat(pidPath)
	if err != nil {
		return daemonIdentity{}, err
	}
	started, err := startTime(pid)
	if err != nil {
		return daemonIdentity{}, fmt.Errorf("agent-browser daemon PID %d start time: %w", pid, err)
	}
	if started.After(info.ModTime()) {
		return daemonIdentity{}, fmt.Errorf("agent-browser daemon PID %d no longer identifies the recorded process: %w", pid, os.ErrProcessDone)
	}
	return daemonIdentity{pid: pid, start: started}, nil
}

func (e *Engine) checkDaemon() error {
	if e.daemon.pid == 0 {
		return nil
	}
	current, err := e.daemonIdentity()
	if err == nil && current.pid == e.daemon.pid && current.start.Equal(e.daemon.start) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrProcessDone) {
		return &engineInterruption{fmt.Errorf("cannot verify initialized agent-browser daemon: %w", err)}
	}
	if e.recovering {
		return errors.New("agent-browser daemon changed during initialization")
	}
	return &engineInterruption{e.restartDaemon(errors.New("initialized agent-browser daemon exited or was replaced"), "after the initialized daemon exited or was replaced")}
}

// setupLogBoundary and loggedDaemonState use the same drive log as receipts.
// Never print setting values: headers and credentials can contain secrets.
func setupLogBoundary(runDir string) (int, error) {
	actions, err := setupLog(runDir)
	seq := 0
	for _, a := range actions {
		seq = max(seq, a.Seq)
	}
	return seq, err
}

func setupLog(runDir string) ([]actionRec, error) {
	f, err := os.Open(filepath.Join(runDir, "actions.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var actions []actionRec
	for dec := json.NewDecoder(f); ; {
		var a actionRec
		if err := dec.Decode(&a); err != nil {
			if errors.Is(err, io.EOF) {
				return actions, nil
			}
			return nil, err
		}
		actions = append(actions, a)
	}
}

func loggedDaemonState(runDir, leg string, since int, generation uint64, boundaryErr error, pending ...*actionRec) string {
	actions, err := setupLog(runDir)
	if err = errors.Join(boundaryErr, err); err != nil {
		return "Could not read the tester setup drive log: " + err.Error() + ". " + lostDaemonState
	}
	// The current command has no receipt yet. Preserve its known send/result
	// boundary too, including setup that succeeded before an after-shot failed.
	if len(pending) > 0 && pending[0] != nil && !pending[0].SentAt.IsZero() {
		a := *pending[0]
		a.Seq = since + 1
		actions = append(actions, a)
	}
	var setup []string
	var uncertain []string
	routes := map[string]bool{}
	for _, a := range actions {
		if a.Seq <= since || a.Leg != leg || a.SentAt.IsZero() || a.EngineGeneration != generation {
			continue
		}
		name := a.Action
		args := a.Args
		switch name {
		case "set":
			if len(a.Args) == 0 {
				continue
			}
			name += " " + a.Args[0]
		case "route", "unroute", "record", "trace":
		case "network":
			if len(a.Args) == 0 || !slices.Contains([]string{"route", "unroute"}, a.Args[0]) {
				continue
			}
			name = a.Args[0]
			args = a.Args[1:]
		default:
			continue
		}
		if slices.Contains(a.Args, "--help") {
			continue
		}
		if a.Reason != "" {
			if !slices.Contains(uncertain, name) {
				uncertain = append(uncertain, name)
			}
			continue
		}
		if name == "record" || name == "trace" {
			if len(args) == 0 {
				continue
			}
			switch args[0] {
			case "stop":
				setup = slices.DeleteFunc(setup, func(s string) bool { return s == name })
				continue
			case "start", "restart":
			default:
				continue
			}
		}
		if name == "unroute" {
			if len(args) == 0 {
				clear(routes)
			} else {
				delete(routes, args[0])
			}
			if len(routes) == 0 {
				setup = slices.DeleteFunc(setup, func(s string) bool { return s == "route" })
			}
			continue
		}
		if name == "route" {
			if len(args) == 0 {
				continue
			}
			routes[args[0]] = true
		}
		if !slices.Contains(setup, name) {
			setup = append(setup, name)
		}
	}
	message := "Lost daemon-held state includes element refs, frame selection, dialog state, snapshot/console/errors/network history, and in-progress network HAR and profiler captures. Other daemon-held setup may also have been lost. "
	if len(setup) > 0 {
		message += "Tester setup recorded on this leg since initialization: " + strings.Join(setup, ", ") + ". Reapply that setup and restart any listed recording or trace. "
	} else {
		message += "No successful set, route, record, or trace setup remains in this leg's drive log since initialization. "
	}
	if len(uncertain) > 0 {
		message += "Tester setup that may have been lost after setup commands failed: " + strings.Join(uncertain, ", ") + ". Reapply that setup if needed. "
	}
	return message + "Take a new snapshot before continuing."
}
