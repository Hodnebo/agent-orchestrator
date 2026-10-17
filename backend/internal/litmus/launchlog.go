package litmus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxLaunchLogBytes = 1 << 20

func openLaunchLog(logDir string, now time.Time, reason string) (*os.File, error) {
	logPath := filepath.Join(logDir, "dev.log")
	launch, err := incrementLaunchCount(logDir)
	if err != nil {
		return nil, err
	}
	if err := rotateLaunchLog(logPath); err != nil {
		return nil, fmt.Errorf("litmus: rotate launch log: %w", err)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("litmus: open launch log: %w", err)
	}
	if err := logf.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("litmus: set launch log permissions: %w", err), logf.Close())
	}
	reason = strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(reason)
	if _, err := fmt.Fprintf(logf, "\n--- launch %d at %s (%s) ---\n", launch, now.Format(time.RFC3339), reason); err != nil {
		return nil, errors.Join(fmt.Errorf("litmus: write launch separator: %w", err), logf.Close())
	}
	return logf, nil
}

func rotateLaunchLog(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("launch log is not a regular file: %s", path)
	}
	if info.Size() <= maxLaunchLogBytes {
		return nil
	}
	previous := path + ".previous"
	if _, err := os.Stat(previous); err == nil {
		if err := os.Remove(previous); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(path, previous)
}

func incrementLaunchCount(logDir string) (uint64, error) {
	counterPath := filepath.Join(logDir, "launch-count")
	data, err := os.ReadFile(counterPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("litmus: read launch count: %w", err)
	}
	var current uint64
	if err == nil {
		text := string(data)
		text = strings.TrimSuffix(text, "\n")
		if text == "" || strings.ContainsAny(text, "\r\n") {
			return 0, fmt.Errorf("litmus: malformed launch count in %s", counterPath)
		}
		for _, char := range text {
			if char < '0' || char > '9' {
				return 0, fmt.Errorf("litmus: malformed launch count in %s", counterPath)
			}
		}
		current, err = strconv.ParseUint(text, 10, 64)
		if err != nil || current == 0 {
			return 0, fmt.Errorf("litmus: malformed launch count in %s", counterPath)
		}
	}
	if current == ^uint64(0) {
		return 0, fmt.Errorf("litmus: launch count overflow in %s", counterPath)
	}
	next := current + 1
	if err := writeAtomic(counterPath, []byte(strconv.FormatUint(next, 10)+"\n")); err != nil {
		return 0, fmt.Errorf("litmus: replace launch count: %w", err)
	}
	return next, nil
}
