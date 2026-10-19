//go:build unix

package githubpat

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// The lock file is never replaced or removed. All processes must lock the same
// inode even while the credential file is replaced atomically.
func lockCredentialFile(ctx context.Context, path string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			_ = unix.Close(fd)
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func replaceCredentialFile(source, target string) error {
	return os.Rename(source, target)
}
