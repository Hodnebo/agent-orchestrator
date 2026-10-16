//go:build !windows

package gitworktree

import "syscall"

func moveTaskDirectory(source, reserved string) error {
	// reserved is the empty directory we exclusively created, never a user path.
	return syscall.Rename(source, reserved)
}
