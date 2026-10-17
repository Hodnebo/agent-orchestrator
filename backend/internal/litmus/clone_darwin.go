//go:build darwin

package litmus

import "golang.org/x/sys/unix"

func cloneInstallPath(source, dest string) error {
	return unix.Clonefile(source, dest, unix.CLONE_NOFOLLOW)
}
