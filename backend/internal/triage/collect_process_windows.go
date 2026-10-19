//go:build windows

package triage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
)

func readProcessIdentity(ctx context.Context, pid int) (processIdentity, error) {
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	if pid <= 0 || int64(pid) > 1<<31-1 {
		return processIdentity{}, errors.New("invalid process ID")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return processIdentity{}, err
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		return processIdentity{}, errors.New("recorded process is not running")
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return processIdentity{}, err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return processIdentity{}, err
	}
	executable := windows.UTF16ToString(buffer[:size])
	return processIdentity{PID: pid, Start: fmt.Sprintf("%d.%d", created.HighDateTime, created.LowDateTime), Name: filepath.Base(executable), Executable: executable}, nil
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
	values["memoryAvailableBytes"] = system.AvailableBytes
	return values, nil
}
