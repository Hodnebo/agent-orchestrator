package litmus

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenLaunchLogAppendsNumberedLaunches(t *testing.T) {
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "dev.log")
	if err := os.WriteFile(logPath, []byte("existing output\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	firstTime := time.Date(2026, time.October, 17, 10, 11, 12, 0, time.FixedZone("IST", 5*60*60+30*60))
	first, err := openLaunchLog(logDir, firstTime, "initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(first, "first output\n"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	secondTime := firstTime.Add(3 * time.Minute)
	second, err := openLaunchLog(logDir, secondTime, "relaunch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(second, "second output\n"); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "existing output\n\n--- launch 1 at 2026-10-17T10:11:12+05:30 (initial) ---\n" +
		"first output\n\n--- launch 2 at 2026-10-17T10:14:12+05:30 (relaunch) ---\nsecond output\n"
	if string(data) != want {
		t.Fatalf("dev.log = %q, want %q", data, want)
	}

	counter, err := os.ReadFile(filepath.Join(logDir, "launch-count"))
	if err != nil {
		t.Fatal(err)
	}
	if string(counter) != "2\n" {
		t.Fatalf("launch-count = %q, want %q", counter, "2\n")
	}
}

func TestOpenLaunchLogEscapesReasonNewlines(t *testing.T) {
	logDir := t.TempDir()
	logf, err := openLaunchLog(logDir, time.Date(2026, time.October, 17, 10, 11, 12, 0, time.UTC), "bad\n--- launch 999\rforged")
	if err != nil {
		t.Fatal(err)
	}
	if err := logf.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(logDir, "dev.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := "\n--- launch 1 at 2026-10-17T10:11:12Z (bad\\n--- launch 999\\rforged) ---\n"
	if string(data) != want {
		t.Fatalf("dev.log = %q, want %q", data, want)
	}
}

func TestOpenLaunchLogRotatesOversizedPreviousLaunchWithOneBackup(t *testing.T) {
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "dev.log")
	previous := strings.Repeat("old output\n", maxLaunchLogBytes/len("old output\n")+1)
	if err := os.WriteFile(logPath, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "launch-count"), []byte("7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	logf, err := openLaunchLog(logDir, time.Date(2026, time.October, 17, 10, 11, 12, 0, time.UTC), "relaunch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(logf, strings.Repeat("current output\n", maxLaunchLogBytes/len("current output\n")+1)); err != nil {
		t.Fatal(err)
	}
	if err := logf.Close(); err != nil {
		t.Fatal(err)
	}

	rotated, err := os.ReadFile(logPath + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) != previous {
		t.Fatalf("rotated log = %q, want prior launch output", rotated[:min(len(rotated), 80)])
	}
	current, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(current), "\n--- launch 8 at 2026-10-17T10:11:12Z (relaunch) ---\n") || !strings.Contains(string(current), "current output\n") {
		t.Fatalf("current launch log lost its section or output: prefix=%q", current[:min(len(current), 120)])
	}

	second, err := openLaunchLog(logDir, time.Date(2026, time.October, 17, 10, 12, 12, 0, time.UTC), "relaunch again")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(second, "second launch output\n"); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	rotated, err = os.ReadFile(logPath + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rotated), "current output\n") {
		t.Fatalf("second rotation lost the immediately previous launch: %q", rotated[:min(len(rotated), 120)])
	}
	if _, err := os.Stat(logPath + ".previous-1"); !os.IsNotExist(err) {
		t.Fatalf("unexpected unbounded launch-log backup: err=%v", err)
	}
}

func TestOpenLaunchLogRejectsMalformedCountWithoutChangingLog(t *testing.T) {
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "dev.log")
	before := []byte("existing output\n")
	if err := os.WriteFile(logPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(logDir, "launch-count")
	if err := os.WriteFile(countPath, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if logf, err := openLaunchLog(logDir, time.Now(), "relaunch"); err == nil {
		_ = logf.Close()
		t.Fatal("openLaunchLog succeeded with a malformed launch count")
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("dev.log changed after malformed count: got %q, want %q", after, before)
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "two\n" {
		t.Fatalf("launch-count changed after malformed value: %q", count)
	}
}
