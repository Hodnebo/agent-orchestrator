package litmus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	homeSnapshotFile       = "home-before.json"
	homeChangesFile        = "home-changes.txt"
	homeSnapshotTimeout    = 20 * time.Second
	homeSnapshotIncomplete = "owner home snapshot incomplete after 20s"
)

// Roots preserve the scan scope across changes to PATH and package-manager
// configuration. Entries contain metadata and hashes, never file contents.
type homeSnapshot struct {
	Roots          []homeScanRoot       `json:"roots"`
	Entries        map[string]homeEntry `json:"entries"`
	RunsRoot       string               `json:"runsRoot,omitempty"`
	Incomplete     bool                 `json:"incomplete,omitempty"`
	TimedOut       bool                 `json:"timedOut,omitempty"`
	CompletedRoots []string             `json:"completedRoots,omitempty"`
}

type homeScanRoot struct {
	Path           string `json:"path"`
	Depth          int    `json:"depth,omitempty"`    // root is depth 0
	Hash           bool   `json:"hash,omitempty"`     // only the explicitly named file
	Required       bool   `json:"required,omitempty"` // the owner home itself must be readable
	DiscoveryError string `json:"-"`                  // root discovery could not read this entry
}

type homeEntry struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Size       int64  `json:"size"`
	MTime      int64  `json:"mtime"`
	SHA256     string `json:"sha256,omitempty"`
	NotHashed  string `json:"notHashed,omitempty"`
	Unreadable string `json:"unreadable,omitempty"`
}

// The hooks let tests prove that dataless files are never opened, and that a
// blocked filesystem operation cannot hold up intake or teardown.
type homeScanner struct {
	dataless func(fs.FileInfo) bool
	open     func(string) (*os.File, error)
	lstat    func(string) (fs.FileInfo, error)
	readDir  func(string) ([]os.DirEntry, error)
	readlink func(string) (string, error)
}

func newHomeScanner() homeScanner {
	return homeScanner{dataless: homeFileDataless, open: os.Open, lstat: os.Lstat, readDir: os.ReadDir, readlink: os.Readlink}
}

func snapshotOwnerHome(home string, env []string, runsRoot string) (homeSnapshot, error) {
	home, err := absoluteHomePath(home)
	if err != nil {
		return homeSnapshot{}, err
	}
	runsRoot, err = optionalAbsolutePath(runsRoot)
	if err != nil {
		return homeSnapshot{}, fmt.Errorf("runs root: %w", err)
	}
	scanner := newHomeScanner()
	return scanner.scan(context.Background(), func(ctx context.Context) ([]homeScanRoot, error) {
		return scanner.ownerHomeRoots(ctx, home, env)
	}, runsRoot)
}

func diffOwnerHome(before homeSnapshot) (string, error) {
	after, err := scanHomeRoots(before.Roots, before.RunsRoot)
	if err != nil {
		return "", err
	}
	return diffHomeSnapshots(before, after), nil
}

// Only inspect this run's private roots, without walking linked credentials.
func legHomeForks(ownerHome, runDir string, legs ...string) (string, error) {
	var forked []string
	if len(legs) == 0 {
		legs = []string{"head", "base"}
	}
	for _, leg := range legs {
		legDir := filepath.Join(runDir, leg)
		for _, root := range legInstallRoots {
			if root.whole {
				continue
			}
			dest := filepath.Join(legDir, "home", filepath.FromSlash(root.path))
			info, err := os.Lstat(dest)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				forked = append(forked, "LINKED "+strconv.Quote(dest)+" linked, not inspected")
				continue
			}
			if err := checkLegWriteDir(legDir, dest); err != nil {
				return "", err
			}
			if ownerHome == "" {
				return "", errors.New("litmus: owner home missing from intake snapshot")
			}
			entries, err := os.ReadDir(filepath.Join(ownerHome, filepath.FromSlash(root.path)))
			if os.IsNotExist(err) {
				continue // The owner snapshot reports a removed install root.
			}
			if err != nil {
				return "", fmt.Errorf("litmus: inspect linked install children: %w", err)
			}
			for _, entry := range entries {
				if slices.Contains(root.children, entry.Name()) {
					continue
				}
				path := filepath.Join(dest, entry.Name())
				info, err := os.Lstat(path)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return "", err
				}
				if info.Mode()&os.ModeSymlink == 0 {
					forked = append(forked, "FORKED "+strconv.Quote(path))
				}
			}
		}
	}
	sort.Strings(forked)
	return strings.Join(forked, "\n"), nil
}

func diffHomeSnapshots(before, after homeSnapshot) string {
	added, modified, removed := []string{}, []string{}, []string{}
	for path, oldEntry := range before.Entries {
		if oldEntry.Unreadable != "" {
			continue
		}
		newEntry, ok := after.Entries[path]
		if !ok {
			if homeSnapshotCovers(after, path) {
				removed = append(removed, path)
			}
			continue
		}
		if newEntry.Unreadable != "" {
			continue
		}
		if oldEntry.Type == "missing" {
			if newEntry.Type != "missing" {
				added = append(added, path)
			}
			continue
		}
		if newEntry.Type == "missing" {
			removed = append(removed, path)
			continue
		}
		if !sameHomeEntry(oldEntry, newEntry) {
			modified = append(modified, path)
		}
	}
	for path, entry := range after.Entries {
		if _, ok := before.Entries[path]; !ok && entry.Unreadable == "" && homeSnapshotCovers(before, path) {
			added = append(added, path)
		}
	}
	sort.Strings(added)
	sort.Strings(modified)
	sort.Strings(removed)
	lines := make([]string, 0, len(added)+len(modified)+len(removed)+1)
	unreadable := map[string]string{}
	for _, snapshot := range []homeSnapshot{before, after} {
		count := 0
		for path, entry := range snapshot.Entries {
			if entry.Unreadable != "" {
				unreadable[path] = entry.Unreadable
				count++
			}
		}
		// Incomplete snapshots written before timedOut was added represent timeouts.
		if snapshot.TimedOut || (snapshot.Incomplete && count == 0) {
			if len(lines) == 0 {
				lines = append(lines, homeSnapshotIncomplete)
			}
		}
	}
	if len(unreadable) > 0 {
		lines = append(lines, fmt.Sprintf("incomplete: %d entries unreadable", len(unreadable)))
		paths := make([]string, 0, len(unreadable))
		for path := range unreadable {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			lines = append(lines, "UNREADABLE "+strconv.Quote(path)+" "+strconv.Quote(unreadable[path]))
		}
	}
	unhashed := map[string]bool{}
	for _, snapshot := range []homeSnapshot{before, after} {
		for path, entry := range snapshot.Entries {
			if entry.NotHashed != "" {
				unhashed[path] = true
			}
		}
	}
	paths := make([]string, 0, len(unhashed))
	for path := range unhashed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		lines = append(lines, strconv.Quote(path)+" not hashed: dataless")
	}
	for _, path := range added {
		lines = append(lines, "ADDED "+strconv.Quote(path))
	}
	for _, path := range modified {
		lines = append(lines, "MODIFIED "+strconv.Quote(path))
	}
	for _, path := range removed {
		lines = append(lines, "REMOVED "+strconv.Quote(path))
	}
	return strings.Join(lines, "\n")
}

// An unreadable subtree or timed-out scan cannot establish additions or removals.
func homeSnapshotCovers(snapshot homeSnapshot, path string) bool {
	if !snapshot.Incomplete {
		return true
	}
	for unreadable, entry := range snapshot.Entries {
		if entry.Unreadable != "" && pathWithin(unreadable, path) {
			return false
		}
	}
	for _, root := range snapshot.Roots {
		completed := false
		for _, done := range snapshot.CompletedRoots {
			if done == root.Path {
				completed = true
				break
			}
		}
		if !completed || !pathWithin(root.Path, path) {
			continue
		}
		rel, _ := filepath.Rel(root.Path, path)
		if rel == "." || strings.Count(rel, string(filepath.Separator))+1 <= root.Depth {
			return true
		}
	}
	return false
}

func sameHomeEntry(a, b homeEntry) bool {
	if a.Type != b.Type || a.LinkTarget != b.LinkTarget || a.Size != b.Size || a.MTime != b.MTime {
		return false
	}
	// A partial scan may have listed a named file before reaching its hash.
	// Dataless files also lack content evidence. Both cases are reported above.
	return a.SHA256 == "" || b.SHA256 == "" || a.SHA256 == b.SHA256
}

func ownerHomeRoots(home string, env []string) ([]homeScanRoot, error) {
	return newHomeScanner().ownerHomeRoots(context.Background(), home, env)
}

func (s homeScanner) ownerHomeRoots(ctx context.Context, home string, env []string) ([]homeScanRoot, error) {
	values := environmentValues(env)
	roots := make(map[string]homeScanRoot)
	addRoot := func(path string, depth int, hash bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := absoluteHomePath(expandHomePath(path, home))
		if err != nil {
			return err
		}
		root := roots[path]
		root.Path = path
		root.Depth = max(root.Depth, depth)
		root.Hash = root.Hash || hash
		root.Required = path == home
		roots[path] = root
		return nil
	}

	if err := addRoot(home, 1, false); err != nil {
		return nil, err
	}
	if err := addRoot(filepath.Join(home, ".local"), 1, false); err != nil {
		return nil, err
	}
	for _, name := range []string{"bin", "share", "lib", "state"} {
		depth := 2
		if name == "bin" {
			depth = 1
		}
		if err := addRoot(filepath.Join(home, ".local", name), depth, false); err != nil {
			return nil, err
		}
	}
	for _, root := range legInstallRoots {
		for _, path := range root.clonedPaths() {
			if err := addRoot(filepath.Join(home, path), 1, false); err != nil {
				return nil, err
			}
		}
	}
	if err := addRoot(filepath.Join(home, "go", "bin"), 1, false); err != nil {
		return nil, err
	}

	if path := values["PATH"]; path != "" {
		for _, dir := range filepath.SplitList(path) {
			if strings.TrimSpace(dir) == "" {
				dir = "."
			}
			if err := addRoot(dir, 1, false); err != nil {
				return nil, fmt.Errorf("PATH root %q: %w", dir, err)
			}
		}
	}

	npmPrefix, err := s.npmGlobalPrefix(ctx, home, values)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		path := filepath.Join(home, ".npmrc")
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			path = pathErr.Path
		}
		path, pathErrValue := absoluteHomePath(expandHomePath(path, home))
		if pathErrValue != nil {
			return nil, pathErrValue
		}
		if rootErr := addRoot(path, 0, false); rootErr != nil {
			return nil, rootErr
		}
		root := roots[path]
		root.DiscoveryError = err.Error()
		roots[path] = root
	}
	if npmPrefix != "" {
		if err := addRoot(filepath.Join(npmPrefix, "bin"), 1, false); err != nil {
			return nil, fmt.Errorf("npm bin: %w", err)
		}
		if err := addRoot(filepath.Join(npmPrefix, "lib", "node_modules"), 2, false); err != nil {
			return nil, fmt.Errorf("npm modules: %w", err)
		}
	}

	brewPrefixes := []string{"/opt/homebrew", "/usr/local"}
	if prefix := strings.TrimSpace(values["HOMEBREW_PREFIX"]); prefix != "" {
		brewPrefixes = append(brewPrefixes, prefix)
	}
	// Include custom Homebrew prefixes found on PATH as well as the standard
	// prefixes, even when the owner has not put brew on the current PATH.
	for _, dir := range filepath.SplitList(values["PATH"]) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(dir) == "" {
			continue
		}
		absDir, err := absoluteHomePath(dir)
		if err != nil {
			return nil, fmt.Errorf("homebrew PATH root %q: %w", dir, err)
		}
		if filepath.Base(absDir) != "bin" {
			continue
		}
		prefix := filepath.Dir(absDir)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if prefix == "/opt/homebrew" || prefix == "/usr/local" || homePathExists(filepath.Join(absDir, "brew")) {
			brewPrefixes = append(brewPrefixes, prefix)
		}
	}
	for _, prefix := range brewPrefixes {
		for _, name := range []string{"Cellar", "Caskroom"} {
			if err := addRoot(filepath.Join(prefix, name), 2, false); err != nil {
				return nil, fmt.Errorf("homebrew root %q: %w", filepath.Join(prefix, name), err)
			}
		}
	}

	// These files are deliberately explicit. Broadly scanning dotfiles would
	// record credentials and unrelated application state in a baseline.
	for _, name := range []string{
		".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
		".zshenv", ".zshrc", ".zprofile", ".zlogin", ".zlogout", ".kshrc",
		".cshrc", ".tcshrc", ".npmrc",
		".commandcode/config.json", ".commandcode/settings.json",
		".codex/config.toml", ".claude/settings.json",
		".config/opencode/opencode.json", ".gitconfig", ".cursor/cli-config.json",
	} {
		if err := addRoot(filepath.Join(home, name), 0, true); err != nil {
			return nil, err
		}
	}
	if err := addRoot(filepath.Join(home, ".claude", "plans"), 1, false); err != nil {
		return nil, err
	}

	out := make([]homeScanRoot, 0, len(roots))
	for _, root := range roots {
		out = append(out, root)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// The collector alone owns the snapshot. A worker may be stuck in a kernel
// filesystem call at the deadline; it stops at the next context check and
// cannot mutate the partial snapshot returned to the caller.
func (s homeScanner) scan(parent context.Context, roots func(context.Context) ([]homeScanRoot, error), runsRoot string) (homeSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, homeSnapshotTimeout)
	defer cancel()
	type event struct {
		roots     []homeScanRoot
		entry     *homeEntry
		completed string
		done      bool
		err       error
	}
	events := make(chan event)
	send := func(e event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- e:
			return nil
		}
	}
	go func() {
		planned, err := roots(ctx)
		if err == nil {
			err = send(event{roots: planned})
		}
		for _, root := range planned {
			if err != nil {
				break
			}
			if root.Path == "" || root.Depth < 0 {
				err = errors.New("home snapshot contains an invalid scan root")
				break
			}
			if shouldSkipHomePath(root.Path, runsRoot) {
				continue
			}
			err = s.scanRoot(ctx, root, false, runsRoot, func(entry homeEntry) error {
				return send(event{entry: &entry})
			})
			if err == nil {
				err = send(event{completed: root.Path})
			}
		}
		_ = send(event{done: true, err: err})
	}()
	snapshot := homeSnapshot{Entries: map[string]homeEntry{}, RunsRoot: runsRoot}
	for {
		if ctx.Err() != nil {
			snapshot.Incomplete = true
			snapshot.TimedOut = true
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			snapshot.Incomplete = true
			snapshot.TimedOut = true
			return snapshot, nil
		case e := <-events:
			if e.done {
				if ctx.Err() != nil {
					snapshot.Incomplete = true
					snapshot.TimedOut = true
					return snapshot, nil
				}
				return snapshot, e.err
			}
			if e.roots != nil {
				snapshot.Roots = e.roots
			}
			if e.entry != nil {
				entry := *e.entry
				previous := snapshot.Entries[entry.Path]
				if previous.Unreadable != "" && entry.Unreadable == "" {
					entry.Unreadable = previous.Unreadable
				}
				if previous.Type != "" && previous.Type != "missing" && entry.Type == "missing" && entry.Unreadable == "" {
					entry.Unreadable = fmt.Sprintf("home path disappeared while scanning: %q", entry.Path)
				}
				snapshot.Entries[entry.Path] = entry
				if entry.Unreadable != "" {
					snapshot.Incomplete = true
				}
			}
			if e.completed != "" {
				snapshot.CompletedRoots = append(snapshot.CompletedRoots, e.completed)
			}
		}
	}
}

func scanHomeRoots(roots []homeScanRoot, runsRoot string) (homeSnapshot, error) {
	return newHomeScanner().scan(context.Background(), func(context.Context) ([]homeScanRoot, error) { return roots, nil }, runsRoot)
}

func (s homeScanner) scanRoot(ctx context.Context, root homeScanRoot, listed bool, runsRoot string, record func(homeEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := root.Path
	if root.DiscoveryError != "" {
		if err := record(homeEntry{Path: path, Type: "unreadable", Unreadable: root.DiscoveryError}); err != nil {
			return err
		}
	}
	info, err := s.lstat(path)
	if errors.Is(err, fs.ErrNotExist) && !root.Required && !listed {
		return record(homeEntry{Path: path, Type: "missing"})
	}
	if err != nil {
		err = fmt.Errorf("stat home path %q: %w", path, err)
		if root.Required {
			return err
		}
		return record(homeEntry{Path: path, Type: "unreadable", Unreadable: err.Error()})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := s.entry(ctx, path, info, root.Hash)
	if err != nil {
		if root.Required {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry.Unreadable = err.Error()
		return record(entry)
	}
	if err := record(entry); err != nil {
		return err
	}
	if root.Depth == 0 || (!info.IsDir() && !root.Required) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	children, err := s.readDir(path)
	if err != nil {
		err = fmt.Errorf("read home directory %q: %w", path, err)
		if root.Required {
			return err
		}
		entry.Unreadable = err.Error()
		return record(entry)
	}
	for _, child := range children {
		childPath := filepath.Join(path, child.Name())
		if shouldSkipHomePath(childPath, runsRoot) {
			continue
		}
		if err := s.scanRoot(ctx, homeScanRoot{Path: childPath, Depth: root.Depth - 1}, true, runsRoot, record); err != nil {
			return err
		}
	}
	return nil
}

func (s homeScanner) entry(ctx context.Context, path string, info fs.FileInfo, hash bool) (homeEntry, error) {
	entry := homeEntry{Path: path, Size: info.Size(), MTime: info.ModTime().UnixNano()}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		entry.Type = "symlink"
		target, err := s.readlink(path)
		if err != nil {
			return entry, fmt.Errorf("read symlink %q: %w", path, err)
		}
		entry.LinkTarget = target
		if !hash {
			return entry, nil
		}
		// Hash named rc/config links by their regular target, while preserving
		// the link itself as evidence when the target does not resolve.
		targetInfo, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			current, linkErr := s.lstat(path)
			if linkErr != nil {
				return entry, fmt.Errorf("recheck home symlink %q: %w", path, linkErr)
			}
			if !os.SameFile(info, current) {
				return entry, fmt.Errorf("home symlink changed while resolving target: %q", path)
			}
			return entry, nil
		}
		if err != nil {
			return entry, fmt.Errorf("stat home symlink target %q: %w", path, err)
		}
		info = targetInfo
		if !info.Mode().IsRegular() {
			return entry, nil
		}
	case info.IsDir():
		entry.Type = "directory"
	case info.Mode().IsRegular():
		entry.Type = "file"
	default:
		entry.Type = "other"
	}
	if hash && info.Mode().IsRegular() {
		if s.dataless(info) {
			entry.NotHashed = "not hashed: dataless"
		} else {
			digest, dataless, err := s.hashFile(ctx, path, info)
			if err != nil {
				return entry, err
			}
			entry.SHA256 = digest
			if dataless {
				entry.NotHashed = "not hashed: dataless"
			}
		}
	}
	return entry, nil
}

func (s homeScanner) hashFile(ctx context.Context, path string, before fs.FileInfo) (digest string, dataless bool, err error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	f, err := s.open(path)
	if err != nil {
		return "", false, fmt.Errorf("open home file %q: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close home file %q: %w", path, closeErr))
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	// Recheck after open in case the named file was replaced since Lstat.
	info, err := f.Stat()
	if err != nil {
		return "", false, fmt.Errorf("stat open home file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return "", false, fmt.Errorf("home file changed before hashing: %q", path)
	}
	if s.dataless(info) {
		return "", true, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, homeContextReader{ctx, f}); err != nil {
		return "", false, fmt.Errorf("hash home file %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), false, nil
}

// Cancellation cannot interrupt a kernel read, but prevents further reads once
// it returns. scan's collector enforces the caller's deadline independently.
type homeContextReader struct {
	ctx  context.Context
	file *os.File
}

func (r homeContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}

func shouldSkipHomePath(path, runsRoot string) bool {
	path = filepath.Clean(path)
	if runsRoot != "" && pathWithin(runsRoot, path) {
		return true
	}
	return isLegHome(path)
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func environmentValues(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func npmGlobalPrefix(home string, values map[string]string) (string, error) {
	return newHomeScanner().npmGlobalPrefix(context.Background(), home, values)
}

func (s homeScanner) npmGlobalPrefix(ctx context.Context, home string, values map[string]string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for _, key := range []string{"npm_config_prefix", "NPM_CONFIG_PREFIX"} {
		if prefix := strings.TrimSpace(values[key]); prefix != "" {
			return expandHomePath(prefix, home), nil
		}
	}

	npmrc := filepath.Join(home, ".npmrc")
	// Prefix discovery must not hydrate a dataless .npmrc either.
	var data []byte
	info, err := os.Stat(npmrc)
	missing := errors.Is(err, fs.ErrNotExist)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("npmrc is not a regular file: %q", npmrc)
	}
	if err == nil && !s.dataless(info) {
		var f *os.File
		f, err = s.open(npmrc)
		if err == nil {
			var buffer bytes.Buffer
			_, err = io.Copy(&buffer, homeContextReader{ctx, f})
			data = buffer.Bytes()
			err = errors.Join(err, f.Close())
		}
	}
	switch {
	case err == nil:
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(key) != "prefix" {
				continue
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				value = value[1 : len(value)-1]
			}
			if value != "" {
				return expandHomePath(value, home), nil
			}
		}
	case missing:
		// Only absence at the initial stat is normal. A later ENOENT is a read race.
	default:
		return "", fmt.Errorf("read npmrc %q: %w", npmrc, err)
	}

	if prefix := values["PREFIX"]; prefix != "" {
		return prefix, nil
	}
	node, err := findNodeInPath(ctx, values["PATH"])
	if err != nil {
		return "", err
	}
	if node != "" {
		if runtime.GOOS == "windows" {
			return filepath.Dir(node), nil
		}
		return filepath.Join(values["DESTDIR"], filepath.Dir(filepath.Dir(node))), nil
	}
	return "", nil
}

func findNodeInPath(ctx context.Context, pathValue string) (string, error) {
	if pathValue == "" {
		return "", nil
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if dir == "" {
			dir = "."
		}
		candidate := dir
		if !filepath.IsAbs(candidate) {
			var err error
			candidate, err = filepath.Abs(candidate)
			if err != nil {
				return "", err
			}
		}
		candidate = filepath.Join(candidate, "node")
		if err := ctx.Err(); err != nil {
			return "", err
		}
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(candidate)
		}
	}
	return "", nil
}

func absoluteHomePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("home path is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("absolute path %q: %w", path, err)
	}
	return abs, nil
}

func optionalAbsolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	return absoluteHomePath(path)
}

func homePathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func expandHomePath(path, home string) string {
	if path == "~" {
		return home
	}
	prefix := "~" + string(filepath.Separator)
	if strings.HasPrefix(path, prefix) {
		return filepath.Join(home, strings.TrimPrefix(path, prefix))
	}
	return path
}
