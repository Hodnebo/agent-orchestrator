package litmus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHomeGuardDetectsSmallFileHashChangeWithStableStat(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zshrc")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"one":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := scanHomeRoots([]homeScanRoot{{Path: path, Hash: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	old := before.Entries[path]
	if old.SHA256 == "" {
		t.Fatal("small file was not hashed")
	}
	if err := os.WriteFile(path, []byte(`{"two":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldTime(old), oldTime(old)); err != nil {
		t.Fatal(err)
	}
	changes, err := diffOwnerHome(before)
	if err != nil {
		t.Fatal(err)
	}
	want := "MODIFIED " + strconv.Quote(path)
	if changes != want {
		t.Fatalf("changes = %q, want %q", changes, want)
	}
}

func TestHomeGuardDetectsAddedAndRemovedPaths(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, ".local", "bin", "before")
	if err := os.MkdirAll(filepath.Dir(existing), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, ".claude", "plans")
	before, err := scanHomeRoots([]homeScanRoot{
		{Path: home, Depth: 1},
		{Path: filepath.Join(home, ".local", "bin"), Depth: 1},
		{Path: filepath.Join(home, ".claude", "plans"), Depth: 1},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(existing); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(missing, "new-plan.md")
	if err := os.MkdirAll(missing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := diffOwnerHome(before)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"ADDED " + strconv.Quote(filepath.Join(home, ".claude")),
		"ADDED " + strconv.Quote(missing),
		"ADDED " + strconv.Quote(created),
		"MODIFIED " + strconv.Quote(home),
		"MODIFIED " + strconv.Quote(filepath.Dir(existing)),
		"REMOVED " + strconv.Quote(existing),
	}, "\n")
	if changes != want {
		t.Fatalf("changes = %q, want %q", changes, want)
	}
}

func TestHomeGuardDoesNotWalkNestedLegHome(t *testing.T) {
	home := t.TempDir()
	runsRoot := filepath.Join(home, ".local", "share", "runs")
	legHome := filepath.Join(runsRoot, "run-1", "head", "home")
	secret := filepath.Join(legHome, "secret.txt")
	if err := os.MkdirAll(legHome, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := scanHomeRoots([]homeScanRoot{{Path: home, Depth: 1}, {Path: filepath.Join(home, ".local", "share"), Depth: 2}}, runsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.Entries[secret]; ok {
		t.Fatalf("nested leg home file was snapshotted: %q", secret)
	}
	if err := os.WriteFile(secret, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := diffOwnerHome(before)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(changes, secret) {
		t.Fatalf("nested leg home leaked into changes: %q", changes)
	}
}

func TestHomeGuardSnapshotJSONIsMetadataOnly(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(path, []byte("secret-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := scanHomeRoots([]homeScanRoot{{Path: path, Hash: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-content") {
		t.Fatalf("snapshot contains file content: %s", data)
	}
	digest := sha256.Sum256([]byte("secret-content"))
	if !strings.Contains(string(data), hex.EncodeToString(digest[:])) {
		t.Fatalf("snapshot omitted small-file hash: %s", data)
	}
}

func oldTime(entry homeEntry) (t time.Time) {
	return time.Unix(0, entry.MTime)
}

func TestHomeGuardSnapshotScopeAndSymlinkMetadata(t *testing.T) {
	home, prefix, brew, bin := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, path := range []string{
		filepath.Join(home, ".local", "lib", "node_modules", "fixture", "index.js"),
		filepath.Join(home, ".claude", "plans", "plan.md"),
		filepath.Join(home, ".cursor", "cli-config.json"),
		filepath.Join(home, ".commandcode", "settings.json"),
		filepath.Join(prefix, "lib", "node_modules", "fixture", "index.js"),
		filepath.Join(brew, "Cellar", "fixture", "1", "binary"),
		filepath.Join(brew, "Caskroom", "fixture", "1", "app"),
		filepath.Join(bin, "agent"),
	} {
		writeFile(t, path, "secret bytes")
	}
	link := filepath.Join(home, "link")
	is(t, "")(nil, os.Symlink(prefix, link))
	roots, err := ownerHomeRoots(home, []string{"PATH=" + bin, "npm_config_prefix=" + prefix, "HOMEBREW_PREFIX=" + brew})
	is(t, "")(nil, err)
	var fixtureRoots []homeScanRoot
	standardBrewRoots := 0
	for _, root := range roots {
		if pathWithin(home, root.Path) || pathWithin(prefix, root.Path) || pathWithin(brew, root.Path) || pathWithin(bin, root.Path) {
			fixtureRoots = append(fixtureRoots, root)
		} else if root.Path == "/opt/homebrew/Cellar" || root.Path == "/opt/homebrew/Caskroom" || root.Path == "/usr/local/Cellar" || root.Path == "/usr/local/Caskroom" {
			standardBrewRoots++
		} else {
			t.Fatalf("unexpected scan root: %s", root.Path)
		}
	}
	if standardBrewRoots != 4 {
		t.Fatalf("standard Homebrew roots missing: %d", standardBrewRoots)
	}
	before, err := scanHomeRoots(fixtureRoots, filepath.Join(home, ".ao", "data", "litmus", "runs"))
	is(t, "")(nil, err)
	for _, path := range []string{filepath.Join(home, ".claude/plans/plan.md"), filepath.Join(home, ".cursor/cli-config.json"), filepath.Join(home, ".commandcode/settings.json"), filepath.Join(prefix, "lib/node_modules/fixture/index.js"), filepath.Join(bin, "agent")} {
		if before.Entries[path].Type != "file" {
			t.Errorf("scope missed %s", path)
		}
	}
	for _, path := range []string{filepath.Join(home, ".local/lib/node_modules/fixture"), filepath.Join(brew, "Cellar/fixture/1"), filepath.Join(brew, "Caskroom/fixture/1")} {
		if before.Entries[path].Type != "directory" {
			t.Errorf("scope missed directory %s", path)
		}
	}
	for _, path := range []string{filepath.Join(home, ".local/lib/node_modules/fixture/index.js"), filepath.Join(brew, "Cellar/fixture/1/binary"), filepath.Join(brew, "Caskroom/fixture/1/app")} {
		if _, ok := before.Entries[path]; ok {
			t.Errorf("walk exceeded depth at %s", path)
		}
	}
	for path, entry := range before.Entries {
		named := path == filepath.Join(home, ".cursor/cli-config.json") || path == filepath.Join(home, ".commandcode/settings.json")
		if (entry.SHA256 != "") != named {
			t.Errorf("unexpected hash policy for %s: %+v", path, entry)
		}
	}
	if before.Entries[link].Type != "symlink" || before.Entries[link].LinkTarget != prefix {
		t.Fatalf("link metadata %+v", before.Entries[link])
	}
	is(t, "")(nil, os.Remove(link))
	is(t, "")(nil, os.Symlink(brew, link))
	changes, err := diffOwnerHome(before)
	want := "MODIFIED " + strconv.Quote(home) + "\nMODIFIED " + strconv.Quote(link)
	if err != nil || changes != want {
		t.Fatalf("link diff %q %v", changes, err)
	}
}

func TestHomeGuardNPMDefaultResolvesNodeLink(t *testing.T) {
	home, prefix, bin := t.TempDir(), t.TempDir(), t.TempDir()
	node := filepath.Join(prefix, "bin", "node")
	writeFile(t, node, "fixture node")
	is(t, "")(nil, os.Chmod(node, 0o700))
	is(t, "")(nil, os.Symlink(node, filepath.Join(bin, "node")))
	got, err := npmGlobalPrefix(home, map[string]string{"PATH": bin})
	want, resolveErr := filepath.EvalSymlinks(prefix)
	if err != nil || resolveErr != nil || got != want {
		t.Fatalf("prefix %q, want %q: %v %v", got, want, err, resolveErr)
	}
}

func TestHomeGuardDepthRulesAndInstalls(t *testing.T) {
	home, prefix, brew := t.TempDir(), t.TempDir(), t.TempDir()
	roots, err := ownerHomeRoots(home, []string{"npm_config_prefix=" + prefix, "HOMEBREW_PREFIX=" + brew})
	if err != nil {
		t.Fatal(err)
	}
	wantDepth := map[string]int{
		filepath.Join(home, ".local/bin"):         1,
		filepath.Join(home, ".local/share"):       2,
		filepath.Join(home, ".local/lib"):         2,
		filepath.Join(home, ".local/state"):       2,
		filepath.Join(home, ".claude/plans"):      1,
		filepath.Join(prefix, "bin"):              1,
		filepath.Join(prefix, "lib/node_modules"): 2,
		filepath.Join(brew, "Cellar"):             2,
		filepath.Join(brew, "Caskroom"):           2,
	}
	var fixtureRoots []homeScanRoot
	for _, root := range roots {
		if depth, ok := wantDepth[root.Path]; ok {
			if root.Depth != depth || root.Hash {
				t.Errorf("root %+v, want depth %d without hashing", root, depth)
			}
			delete(wantDepth, root.Path)
		}
		if pathWithin(home, root.Path) || pathWithin(prefix, root.Path) || pathWithin(brew, root.Path) {
			fixtureRoots = append(fixtureRoots, root)
		}
	}
	if len(wantDepth) != 0 {
		t.Fatalf("missing roots: %v", wantDepth)
	}
	before, err := scanHomeRoots(fixtureRoots, "")
	is(t, "")(nil, err)
	paths := []string{
		filepath.Join(home, ".local/bin/agent"),
		filepath.Join(home, ".local/share/x/version/bin/agent"),
		filepath.Join(prefix, "lib/node_modules/@scope/pkg/index.js"),
		filepath.Join(brew, "Cellar/formula/1/bin/tool"),
		filepath.Join(brew, "Caskroom/app/1/app"),
		filepath.Join(home, ".claude/plans/plan.md"),
		filepath.Join(home, ".bashrc"),
	}
	for _, path := range paths {
		writeFile(t, path, "fixture contents")
	}
	scanner := newHomeScanner()
	scanner.open = func(path string) (*os.File, error) {
		if path != filepath.Join(home, ".bashrc") {
			t.Errorf("walk opened file contents: %s", path)
		}
		return os.Open(path)
	}
	after, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) { return fixtureRoots, nil }, "")
	is(t, "")(nil, err)
	changes := diffHomeSnapshots(before, after)
	for _, path := range []string{paths[0], filepath.Join(home, ".local/share/x"), filepath.Join(prefix, "lib/node_modules/@scope/pkg"), filepath.Join(brew, "Cellar/formula/1"), filepath.Join(brew, "Caskroom/app/1"), paths[5], paths[6]} {
		if !strings.Contains(changes, "ADDED "+strconv.Quote(path)) {
			t.Errorf("install not detected at %s: %s", path, changes)
		}
	}
	for _, path := range []string{paths[1], paths[2], paths[3], paths[4]} {
		if _, ok := after.Entries[path]; ok {
			t.Errorf("walk exceeded depth: %s", path)
		}
	}
	if after.Entries[paths[5]].SHA256 != "" || after.Entries[paths[6]].SHA256 == "" {
		t.Fatal("plan should be metadata only and rc file should be hashed")
	}
}

func TestHomeGuardDatalessFileIsNeverOpened(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zshrc")
	writeFile(t, path, "must not read")
	scanner := newHomeScanner()
	scanner.dataless = func(fs.FileInfo) bool { return true }
	opened := false
	scanner.open = func(string) (*os.File, error) { opened = true; return nil, errors.New("unexpected open") }
	before, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
		return []homeScanRoot{{Path: path, Hash: true}}, nil
	}, "")
	if err != nil || opened || before.Entries[path].NotHashed != "not hashed: dataless" || before.Entries[path].SHA256 != "" {
		t.Fatalf("dataless snapshot: %+v, opened=%v, err=%v", before, opened, err)
	}
	if report := diffHomeSnapshots(before, before); report != strconv.Quote(path)+" not hashed: dataless" {
		t.Fatalf("report omitted dataless note: %q", report)
	}
	// A named symlink must apply the same no-hydration rule to its target.
	link := filepath.Join(home, ".bashrc")
	is(t, "")(nil, os.Symlink(path, link))
	linked, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
		return []homeScanRoot{{Path: link, Hash: true}}, nil
	}, "")
	if err != nil || opened || linked.Entries[link].NotHashed != "not hashed: dataless" || linked.Entries[link].SHA256 != "" {
		t.Fatalf("dataless symlink target was read: %+v opened=%v err=%v", linked, opened, err)
	}
	// .npmrc is also read during root discovery, before hashing.
	writeFile(t, filepath.Join(home, ".npmrc"), "prefix=/must-not-read")
	prefix, err := scanner.npmGlobalPrefix(ctx, home, map[string]string{})
	if err != nil || opened || prefix != "" {
		t.Fatalf("dataless npmrc read: prefix=%q, opened=%v, err=%v", prefix, opened, err)
	}
}

func TestHomeGuardDeadlineReturnsPartialDuringBlockedOpen(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zshrc")
	writeFile(t, path, "fixture")
	scanner := newHomeScanner()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	scanner.open = func(path string) (*os.File, error) {
		close(entered)
		<-release
		defer close(returned)
		return os.Open(path)
	}
	defer func() { close(release); <-returned }()
	limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	before, err := scanner.scan(limited, func(context.Context) ([]homeScanRoot, error) {
		return []homeScanRoot{{Path: home, Depth: 1}, {Path: path, Hash: true}}, nil
	}, "")
	select {
	case <-entered:
	default:
		t.Fatal("scan never reached blocked open")
	}
	if err != nil || !before.Incomplete || len(before.Entries) != 2 || time.Since(start) > time.Second {
		t.Fatalf("deadline did not return partial snapshot promptly: %+v, %v, %s", before, err, time.Since(start))
	}
	if len(before.CompletedRoots) != 1 || before.CompletedRoots[0] != home {
		t.Fatalf("completed roots: %v", before.CompletedRoots)
	}
}

func TestHomeGuardDeadlineIncludesRootDiscovery(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".npmrc"), "prefix=/fixture")
	scanner := newHomeScanner()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	scanner.open = func(path string) (*os.File, error) {
		close(entered)
		<-release
		defer close(returned)
		return os.Open(path)
	}
	defer func() { close(release); <-returned }()
	limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	before, err := scanner.scan(limited, func(ctx context.Context) ([]homeScanRoot, error) {
		return scanner.ownerHomeRoots(ctx, home, nil)
	}, "")
	select {
	case <-entered:
	default:
		t.Fatal("root discovery never reached blocked open")
	}
	if err != nil || !before.Incomplete {
		t.Fatalf("root discovery was not bounded: %+v %v", before, err)
	}
}

func TestHomeGuardPartialDiffDoesNotInventChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unvisited")
	complete := homeSnapshot{Roots: []homeScanRoot{{Path: root, Depth: 1}}, Entries: map[string]homeEntry{path: {Path: path, Type: "file"}}}
	partial := homeSnapshot{Roots: complete.Roots, Entries: map[string]homeEntry{}, Incomplete: true}
	for _, changes := range []string{diffHomeSnapshots(complete, partial), diffHomeSnapshots(partial, complete)} {
		if changes != homeSnapshotIncomplete {
			t.Fatalf("partial diff invented a change: %s", changes)
		}
	}
	partial.CompletedRoots = []string{root}
	if changes := diffHomeSnapshots(complete, partial); !strings.Contains(changes, "REMOVED "+strconv.Quote(path)) {
		t.Fatalf("completed root did not establish removal: %s", changes)
	}
}

func TestHomeGuardHashesNamedSymlinkTargetsOnly(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "file")
	link := filepath.Join(home, ".zshrc")
	writeFile(t, file, "fixture")
	is(t, "")(nil, os.Symlink(file, link))
	scanner := newHomeScanner()
	scanner.open = func(path string) (*os.File, error) {
		if path != link {
			return nil, fmt.Errorf("unexpected content read: %s", path)
		}
		return os.Open(path)
	}
	before, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
		return []homeScanRoot{{Path: home, Depth: 1, Hash: true}, {Path: link, Hash: true}}, nil
	}, "")
	is(t, "")(nil, err)
	if before.Entries[link].Type != "symlink" || before.Entries[link].LinkTarget != file {
		t.Fatalf("symlink metadata: %+v", before.Entries[link])
	}
	for path, entry := range before.Entries {
		if (entry.SHA256 != "") != (path == link) {
			t.Errorf("unexpected content hash for %s: %+v", path, entry)
		}
	}
}

func TestHomeGuardSkipsLinkedInstallRootDuringWipeInspection(t *testing.T) {
	owner, run, linked := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(owner, ".grok", "keep"), "owner")
	writeFile(t, filepath.Join(linked, "keep"), "linked")
	legHome := filepath.Join(run, "head", "home")
	is(t, "")(nil, os.MkdirAll(legHome, 0o700))
	is(t, "")(nil, os.Symlink(linked, filepath.Join(legHome, ".grok")))

	report, err := legHomeForks(owner, run, "head")
	is(t, "")(report, err)
	want := "LINKED " + strconv.Quote(filepath.Join(legHome, ".grok")) + " linked, not inspected"
	if report != want {
		t.Fatalf("linked install report = %q, want %q", report, want)
	}
}

func TestHomeGuardRejectsReplacedNamedFileBeforeReading(t *testing.T) {
	home := t.TempDir()
	path, target := filepath.Join(home, ".zshrc"), filepath.Join(home, "unlisted")
	writeFile(t, path, "before")
	writeFile(t, target, "must not hash")
	scanner := newHomeScanner()
	scanner.open = func(path string) (*os.File, error) {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		if err := os.Symlink(target, path); err != nil {
			return nil, err
		}
		return os.Open(path)
	}
	snapshot, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
		return []homeScanRoot{{Path: path, Hash: true}}, nil
	}, "")
	entry := snapshot.Entries[path]
	if err != nil || !snapshot.Incomplete || entry.SHA256 != "" || !strings.Contains(entry.Unreadable, "home file changed before hashing") {
		t.Fatalf("replacement was not reported unreadable: %+v %v", snapshot, err)
	}
}

func TestHomeGuardNamedSymlinkDetectsTargetContentChanges(t *testing.T) {
	home := t.TempDir()
	target, link := filepath.Join(home, "rc-target"), filepath.Join(home, ".zshrc")
	writeFile(t, target, "before")
	is(t, "")(nil, os.Symlink("rc-target", link))
	before, err := scanHomeRoots([]homeScanRoot{{Path: link, Hash: true}}, "")
	is(t, "")(nil, err)
	writeFile(t, target, "after!")
	changes, err := diffOwnerHome(before)
	is(t, "")(nil, err)
	if changes != "MODIFIED "+strconv.Quote(link) {
		t.Fatalf("target content change was not detected: %s", changes)
	}
	is(t, "")(nil, os.Remove(target))
	broken, err := scanHomeRoots(before.Roots, "")
	is(t, "")(nil, err)
	entry := broken.Entries[link]
	if broken.Incomplete || entry.Type != "symlink" || entry.LinkTarget != "rc-target" || entry.SHA256 != "" {
		t.Fatalf("broken link did not retain its target: %+v", broken)
	}
}

func TestHomeGuardEntryErrorsKeepScanning(t *testing.T) {
	for _, operation := range []string{"lstat", "readlink", "hash", "readdir"} {
		t.Run(operation, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "entry")
			writeFile(t, path, "contents")
			if operation == "readlink" {
				is(t, "")(nil, os.Remove(path))
				is(t, "")(nil, os.Symlink("target", path))
			}
			if operation == "readdir" {
				is(t, "")(nil, os.Remove(path))
				writeFile(t, filepath.Join(path, "child"), "contents")
			}
			healthy := filepath.Join(home, "healthy")
			writeFile(t, healthy, "healthy")
			scanner := newHomeScanner()
			denied := &fs.PathError{Op: operation, Path: path, Err: fs.ErrPermission}
			switch operation {
			case "lstat":
				scanner.lstat = func(p string) (fs.FileInfo, error) {
					if p == path {
						return nil, denied
					}
					return os.Lstat(p)
				}
			case "readlink":
				scanner.readlink = func(string) (string, error) { return "", &fs.PathError{Op: operation, Path: path, Err: fs.ErrNotExist} }
			case "hash":
				scanner.open = func(string) (*os.File, error) { return nil, denied }
			case "readdir":
				scanner.readDir = func(p string) ([]os.DirEntry, error) {
					if p == path {
						return nil, denied
					}
					return os.ReadDir(p)
				}
			}
			snapshot, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
				return []homeScanRoot{{Path: home, Depth: 2, Required: true}, {Path: path, Hash: operation == "hash"}}, nil
			}, "")
			if err != nil || !snapshot.Incomplete || snapshot.TimedOut || snapshot.Entries[path].Unreadable == "" || snapshot.Entries[healthy].Type != "file" {
				t.Fatalf("entry error aborted or hid scan: %+v %v", snapshot, err)
			}
			report := diffHomeSnapshots(snapshot, snapshot)
			if !strings.Contains(report, "incomplete: 1 entries unreadable") || !strings.Contains(report, "UNREADABLE "+strconv.Quote(path)) || strings.Contains(report, homeSnapshotIncomplete) {
				t.Fatalf("entry error report: %s", report)
			}
		})
	}
}

func TestHomeGuardUnreadableSubtreeDoesNotInventChanges(t *testing.T) {
	root := t.TempDir()
	path, child := filepath.Join(root, "unreadable"), filepath.Join(root, "unreadable", "child")
	complete := homeSnapshot{Roots: []homeScanRoot{{Path: root, Depth: 2}}, Entries: map[string]homeEntry{
		path: {Path: path, Type: "directory"}, child: {Path: child, Type: "file"},
	}}
	partial := homeSnapshot{Roots: complete.Roots, CompletedRoots: []string{root}, Incomplete: true, Entries: map[string]homeEntry{
		path: {Path: path, Type: "directory", Unreadable: "fixture denied"},
	}}
	for _, report := range []string{diffHomeSnapshots(complete, partial), diffHomeSnapshots(partial, complete)} {
		if !strings.Contains(report, "incomplete: 1 entries unreadable") || strings.Contains(report, "ADDED") || strings.Contains(report, "MODIFIED") || strings.Contains(report, "REMOVED") {
			t.Fatalf("unreadable subtree invented changes: %s", report)
		}
	}
}

func TestHomeGuardUnreadableHomeRootFails(t *testing.T) {
	home := t.TempDir()
	scanner := newHomeScanner()
	scanner.readDir = func(path string) ([]os.DirEntry, error) {
		return nil, &fs.PathError{Op: "readdir", Path: path, Err: fs.ErrPermission}
	}
	_, err := scanner.scan(ctx, func(ctx context.Context) ([]homeScanRoot, error) {
		return scanner.ownerHomeRoots(ctx, home, nil)
	}, "")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("unreadable home root did not fail: %v", err)
	}
}

func TestHomeGuardNPMDiscoveryReadErrorIsReported(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".npmrc")
	writeFile(t, path, "prefix=/unknown")
	scanner := newHomeScanner()
	scanner.open = func(path string) (*os.File, error) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	snapshot, err := scanner.scan(ctx, func(ctx context.Context) ([]homeScanRoot, error) {
		return scanner.ownerHomeRoots(ctx, home, nil)
	}, "")
	if err != nil || !snapshot.Incomplete || snapshot.Entries[path].Unreadable == "" {
		t.Fatalf("npmrc read error aborted or hid scan: %+v %v", snapshot, err)
	}
}

func TestHomeGuardUnreadableEntryDisappearingDoesNotClaimRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unreadable")
	before := homeSnapshot{Entries: map[string]homeEntry{
		path: {Path: path, Type: "unreadable", Unreadable: "fixture denied"},
	}, Incomplete: true}
	after := homeSnapshot{Entries: map[string]homeEntry{}}
	if report := diffHomeSnapshots(before, after); strings.Contains(report, "REMOVED") || !strings.Contains(report, "incomplete: 1 entries unreadable") {
		t.Fatalf("unreadable entry was claimed removed: %s", report)
	}
}

func TestHomeGuardNamedSymlinkDisappearingIsUnreadable(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(home, ".zshrc")
	is(t, "")(nil, os.Symlink("missing-target", link))
	scanner := newHomeScanner()
	scanner.readlink = func(path string) (string, error) {
		target, err := os.Readlink(path)
		if err == nil {
			err = os.Remove(path)
		}
		return target, err
	}
	snapshot, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
		return []homeScanRoot{{Path: link, Hash: true}}, nil
	}, "")
	entry := snapshot.Entries[link]
	if err != nil || !snapshot.Incomplete || entry.LinkTarget != "missing-target" || !strings.Contains(entry.Unreadable, "no such file") {
		t.Fatalf("symlink removal was not reported unreadable: %+v %v", snapshot, err)
	}
}

func TestHomeGuardNPMDiscoveryOpenRaceIsUnreadable(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".npmrc")
	writeFile(t, path, "prefix=/unknown")
	scanner := newHomeScanner()
	scanner.open = func(path string) (*os.File, error) {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		return os.Open(path)
	}
	snapshot, err := scanner.scan(ctx, func(ctx context.Context) ([]homeScanRoot, error) {
		return scanner.ownerHomeRoots(ctx, home, nil)
	}, "")
	if err != nil || !snapshot.Incomplete || snapshot.Entries[path].Unreadable == "" {
		t.Fatalf("npmrc open race was not reported unreadable: %+v %v", snapshot, err)
	}
}

func TestHomeGuardTimeoutAndChangesAreBothReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "added")
	before := homeSnapshot{Entries: map[string]homeEntry{}, Incomplete: true, TimedOut: true,
		Roots: []homeScanRoot{{Path: path}}, CompletedRoots: []string{path}}
	after := homeSnapshot{Entries: map[string]homeEntry{path: {Path: path, Type: "file"}}}
	report := diffHomeSnapshots(before, after)
	if !strings.Contains(report, homeSnapshotIncomplete) || !strings.Contains(report, "ADDED "+strconv.Quote(path)) {
		t.Fatalf("report omitted timeout or observed change: %s", report)
	}
}

func TestHomeGuardObservedFileDisappearingBeforeHashIsUnreadable(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zshrc")
	writeFile(t, path, "rc contents")
	scanner := newHomeScanner()
	stats := 0
	scanner.lstat = func(p string) (fs.FileInfo, error) {
		if p == path {
			stats++
			if stats > 1 {
				return nil, &fs.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist}
			}
		}
		return os.Lstat(p)
	}
	snapshot, err := scanner.scan(ctx, func(context.Context) ([]homeScanRoot, error) {
		return []homeScanRoot{{Path: home, Depth: 1, Required: true}, {Path: path, Hash: true}}, nil
	}, "")
	if err != nil || !snapshot.Incomplete || snapshot.Entries[path].Unreadable == "" {
		t.Fatalf("observed file disappearance was not reported unreadable: %+v %v", snapshot, err)
	}
}
