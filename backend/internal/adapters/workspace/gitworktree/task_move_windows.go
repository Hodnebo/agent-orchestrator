package gitworktree

import (
	"os"

	"golang.org/x/sys/windows"
)

func moveTaskDirectory(source, reserved string) error {
	if err := os.Remove(reserved); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(reserved)
	if err != nil {
		return err
	}
	// MoveFile refuses an occupied destination, including the race after Remove.
	return windows.MoveFile(from, to)
}
