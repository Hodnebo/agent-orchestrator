//go:build linux

package triage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
)

func readProcessIdentity(ctx context.Context, pid int) (processIdentity, error) {
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processIdentity{}, err
	}
	_, tail, ok := strings.Cut(string(raw), "(")
	if !ok {
		return processIdentity{}, errors.New("invalid process identity")
	}
	end := strings.LastIndex(tail, ")")
	if end < 0 {
		return processIdentity{}, errors.New("invalid process identity")
	}
	fields := strings.Fields(tail[end+1:])
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return processIdentity{}, errors.New("recorded process is not running")
	}
	executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return processIdentity{}, err
	}
	return processIdentity{PID: pid, Start: fields[19], Name: filepath.Base(executable), Executable: executable}, nil
}

func readMachine(ctx context.Context) (map[string]any, error) {
	values := unknownMachine()
	if err := ctx.Err(); err != nil {
		return values, err
	}
	system, err := procmem.ReadSystem()
	if err != nil {
		return values, err
	}
	values["memoryTotalBytes"] = system.TotalBytes
	if system.AvailableBytes > 0 {
		values["memoryAvailableBytes"] = system.AvailableBytes
	}
	// ReadSystem uses zero for an unreadable load average. Check the source
	// separately so an absent reading does not become a measured zero.
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			if load, err := strconv.ParseFloat(fields[0], 64); err == nil && load >= 0 {
				values["load1"] = load
			}
		}
	}
	return values, nil
}
