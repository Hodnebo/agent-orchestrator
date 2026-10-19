//go:build darwin

package triage

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func readProcessIdentity(ctx context.Context, pid int) (processIdentity, error) {
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return processIdentity{}, err
	}
	if int(proc.Proc.P_pid) != pid || proc.Proc.P_stat == 0 || proc.Proc.P_stat == 5 {
		return processIdentity{}, errors.New("recorded process is not running")
	}
	return processIdentity{PID: pid, Start: fmt.Sprintf("%d.%d", proc.Proc.P_starttime.Sec, proc.Proc.P_starttime.Usec), Name: strings.TrimRight(string(proc.Proc.P_comm[:]), "\x00")}, nil
}

func readMachine(ctx context.Context) (map[string]any, error) {
	values := unknownMachine()
	values["cpuCount"] = runtime.NumCPU()
	if err := ctx.Err(); err != nil {
		return values, err
	}
	// The existing full macOS reader starts vm_stat. Keep this collector
	// independent of subprocesses and leave unmeasured fields unknown.
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil && total > 0 {
		values["memoryTotalBytes"] = total
	}
	return values, nil
}
