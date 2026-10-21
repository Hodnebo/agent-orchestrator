package ports

import (
	"context"
	"errors"
)

// ErrComputerUseDriverNotFound is returned when Cua Driver is not installed
// in any location its official installers use.
var ErrComputerUseDriverNotFound = errors.New("computer use: cua-driver not found")

// ComputerUseDriver locates and runs the locally installed Cua Driver CLI.
// Callers build every argument themselves; the adapter only finds the binary
// and executes it.
type ComputerUseDriver interface {
	// Locate returns the cua-driver executable, or ErrComputerUseDriverNotFound.
	Locate(ctx context.Context) (string, error)
	// Run executes binary with args and returns its captured output. A non-zero
	// exit is returned as err alongside whatever the command printed.
	Run(ctx context.Context, binary string, args ...string) (stdout, stderr []byte, err error)
	// StartDaemon starts Cua Driver's background service the way its own docs
	// prescribe for this platform and returns without waiting for it to exit.
	StartDaemon(ctx context.Context, binary string) error
}
