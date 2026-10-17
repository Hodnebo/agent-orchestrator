package litmus

import (
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func fixtureHomeBytes(t *testing.T, home string) map[string][32]byte {
	t.Helper()
	files := map[string][32]byte{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, home)] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestLegHomeCursorInstallIsPrivateAndIdempotent(t *testing.T) {
	owner, run := t.TempDir(), t.TempDir()
	t.Setenv("HOME", owner)
	writeFile(t, filepath.Join(owner, ".zshrc"), "export OWNER_RC=yes\n")
	writeFile(t, filepath.Join(owner, ".local", "bin", "grok"), "Grok\n")
	is(t, "")(nil, os.Symlink("grok", filepath.Join(owner, ".local", "bin", "agent")))
	writeFile(t, filepath.Join(owner, ".codex", "auth.json"), "refresh-token-secret")
	before := fixtureHomeBytes(t, owner)
	head, base := filepath.Join(run, "head"), filepath.Join(run, "base")
	env, err := prepareLegEnv([]string{"HOME=" + owner, "PATH=/usr/bin:/bin"}, head, owner)
	is(t, "")(nil, err)
	_, err = prepareLegEnv([]string{"HOME=" + owner, "PATH=/usr/bin:/bin"}, base, owner)
	is(t, "")(nil, err)
	cmd := exec.Command("/bin/sh", "-c", `mkdir -p "$HOME/.local/share/x"; printf Cursor > "$HOME/.local/share/x/cursor-agent"; ln -sf ../share/x/cursor-agent "$HOME/.local/bin/agent"; printf '\nCursor startup\n' >> "$HOME/.zshrc"`)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Cursor fixture: %s: %v", out, err)
	}
	_, err = prepareLegEnv(env, head, owner)
	is(t, "")(nil, err)
	if after := fixtureHomeBytes(t, owner); !reflect.DeepEqual(before, after) {
		t.Fatalf("owner changed: before %v after %v", before, after)
	}
	for path, want := range map[string]string{
		filepath.Join(owner, ".local/bin/agent"):     "Grok\n",
		filepath.Join(head, "home/.local/bin/agent"): "Cursor",
		filepath.Join(base, "home/.local/bin/agent"): "Grok\n",
		filepath.Join(base, "home/.zshrc"):           "export OWNER_RC=yes\n",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", path, data, err, want)
		}
	}
	if pathExists(filepath.Join(base, "home/.local/share/x")) {
		t.Fatal("Cursor leaked into base")
	}
	if info, err := os.Lstat(filepath.Join(head, "home/.codex")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("credentials were copied: %v %v", info, err)
	}
}

func TestLegHomeRefusesOwnerWriteTargets(t *testing.T) {
	for _, target := range []string{"", "home", "home/.local", "home/.local/bin", "home/.local/share", "home/.local/lib", "home/.local/state", "bin", "lib", "lib/node_modules", "uv/tools", "pipx", "shell"} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			owner, run := t.TempDir(), t.TempDir()
			writeFile(t, filepath.Join(owner, "keep"), "owner bytes")
			leg := filepath.Join(run, "head")
			path := filepath.Join(leg, filepath.FromSlash(target))
			is(t, "")(nil, os.MkdirAll(filepath.Dir(path), 0o700))
			is(t, "")(nil, os.Symlink(owner, path))
			before := fixtureHomeBytes(t, owner)
			if err := buildLegHome(owner, leg); err == nil {
				t.Fatal("accepted owner symlink")
			}
			if !reflect.DeepEqual(before, fixtureHomeBytes(t, owner)) {
				t.Fatal("wrote into owner home")
			}
		})
	}
	owner := t.TempDir()
	if err := buildLegHome(owner, filepath.Join(owner, ".local", "head")); err == nil {
		t.Fatal("accepted leg inside owner's install directory")
	}
}

func TestLegHomeAncestorLoop(t *testing.T) {
	owner := t.TempDir()
	leg := filepath.Join(owner, ".ao", "data", "litmus", "runs", "run_loop", "head")
	is(t, "")(nil, os.MkdirAll(filepath.Join(owner, ".ao"), 0o700))
	writeFile(t, filepath.Join(owner, ".ao", "owner-state"), "owner bytes")
	is(t, "")(nil, buildLegHome(owner, leg))
	privateAO := filepath.Join(leg, "home", ".ao")
	info, err := os.Lstat(privateAO)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatalf("private leg AO directory = %v, %v; want real 0700 directory", info, err)
	}
	if pathExists(filepath.Join(privateAO, "owner-state")) {
		t.Fatal("owner AO entry was mirrored into the leg")
	}
	writeFile(t, filepath.Join(privateAO, "scratch"), "leg bytes")
	if pathExists(filepath.Join(owner, ".ao", "scratch")) {
		t.Fatal("leg AO scratch escaped into owner AO")
	}
	is(t, "")(nil, os.Chmod(privateAO, 0o755))
	is(t, "")(nil, buildLegHome(owner, leg))
	if info, err := os.Lstat(privateAO); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("relaunch changed private AO mode = %v, %v; want 0700", info, err)
	}
}

func TestLegHomePathSafetyAcceptsOnlyAuthorizedRunRoot(t *testing.T) {
	owner := t.TempDir()
	leg := filepath.Join(owner, ".ao", "data", "litmus", "runs", "run_path", "head")
	if err := buildLegHome(owner, leg); err != nil {
		t.Fatalf("authorized run scratch rejected: %v", err)
	}
	if err := buildLegHome(owner, filepath.Join(leg, "home", ".ao", "x")); err == nil {
		t.Fatal("accepted nested leg/home/.ao/x as a leg root")
	}
}

func TestLegHomeRefusesChangedMirrorAndLinkedRC(t *testing.T) {
	for _, entry := range []string{".codex", ".zshrc"} {
		t.Run(entry, func(t *testing.T) {
			owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
			writeFile(t, filepath.Join(owner, ".codex", "config.toml"), "owner")
			writeFile(t, filepath.Join(owner, ".zshrc"), "rc")
			is(t, "")(nil, buildLegHome(owner, leg))
			dest := filepath.Join(leg, "home", entry)
			is(t, "")(nil, os.Remove(dest))
			if entry == ".zshrc" {
				is(t, "")(nil, os.Link(filepath.Join(owner, entry), dest))
			} else {
				is(t, "")(nil, os.Symlink(owner, dest))
			}
			if err := buildLegHome(owner, leg); err == nil {
				t.Fatal("accepted redirected mirror or hard-linked rc")
			}
		})
	}
}

func TestRunWalkSkipsLegHomes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "run_loop", "keep.txt"), "keep")
	for _, leg := range []string{"head", "base"} {
		home := filepath.Join(root, "run_loop", leg, "home")
		writeFile(t, filepath.Join(home, "receipts", "secret.txt"), "must not collect")
		is(t, "")(nil, os.Symlink(root, filepath.Join(home, ".ao")))
	}
	var visited []string
	is(t, "")(nil, walkRunDir(root, func(path string, _ fs.DirEntry, err error) error {
		visited = append(visited, path)
		return err
	}))
	for _, path := range visited {
		if strings.Contains(path, string(filepath.Separator)+"home") {
			t.Fatalf("visited mirrored home: %s", path)
		}
	}
	if size := dirSize(root); size != 4 {
		t.Fatalf("counted mirrored home bytes: %d", size)
	}
	inside := filepath.Join(root, "run_loop", "head", "home", "receipts")
	is(t, "")(nil, walkRunDir(inside, func(path string, _ fs.DirEntry, _ error) error { t.Errorf("visited inside home: %s", path); return nil }))
	runDir, artifacts := filepath.Join(root, "run_loop"), t.TempDir()
	writeFile(t, filepath.Join(runDir, "receipts", "public.txt"), "public")
	is(t, "")(nil, publishArtifacts(artifacts, runDir, "verdict", "index", nil))
	entries, err := os.ReadDir(filepath.Join(artifacts, "receipts"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "public.txt" {
		t.Fatalf("artifact collection entered mirrored home: %v %v", entries, err)
	}
}

func TestLegHomeFailureRefusesTargetStart(t *testing.T) {
	owner := t.TempDir()
	t.Setenv("HOME", owner)
	svc, _ := newSvc(t)
	work := t.TempDir()
	is(t, "")(nil, writeManifest(work, Manifest{SHA: "fixture"}))
	leg := svc.store.legDir("run_s1", "head")
	is(t, "")(nil, os.MkdirAll(leg, 0o700))
	is(t, "")(nil, os.Symlink(owner, filepath.Join(leg, "home")))
	svc.sys.freePorts = func(int) ([]int, error) { return []int{3200, 9400}, nil }
	_, err := svc.startLeg(ctx, "run_s1", 1, Recipe{Run: Step{Cmd: "/nonexistent/litmus-target"}}, Leg{Name: "head", Worktree: work}, newLaps(), nil, false, "launch")
	if err == nil || !strings.Contains(err.Error(), "refusing redirected leg directory") {
		t.Fatalf("launch did not stop at HOME failure: %v", err)
	}
	if pathExists(filepath.Join(leg, "owned.json")) {
		t.Fatal("failed HOME setup reached target start")
	}
	entries, err := os.ReadDir(owner)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed launch wrote into owner: %v %v", entries, err)
	}
}

func TestPruneAndCleanupPreserveLinkedOwner(t *testing.T) {
	owner := t.TempDir()
	writeFile(t, filepath.Join(owner, "keep"), "owner bytes")
	svc, _, _ := pruneFixture(t)
	for _, run := range []string{"run_old1", "run_old2"} {
		home := svc.store.legDir(run, "head")
		is(t, "")(nil, os.MkdirAll(home, 0o700))
		is(t, "")(nil, os.Symlink(owner, filepath.Join(home, "home")))
	}
	is(t, "")(svc.Prune(ctx, 1, 1))
	data, err := os.ReadFile(filepath.Join(owner, "keep"))
	if err != nil || string(data) != "owner bytes" {
		t.Fatalf("prune changed owner: %q %v", data, err)
	}
	if pathExists(filepath.Join(svc.store.root, "run_old1")) {
		t.Fatal("prune kept old run")
	}

	run := Run{ID: "run_clean", SessionID: "clean", State: RunRunning}
	is(t, "")(nil, svc.store.Save(run))
	leg := svc.store.legDir(run.ID, "head")
	is(t, "")(nil, os.MkdirAll(leg, 0o700))
	is(t, "")(nil, os.Symlink(owner, filepath.Join(leg, "home")))
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(svc.Cleanup(ctx, "clean"))
	if !pathExists(filepath.Join(owner, "keep")) || pathExists(filepath.Join(leg, "home")) {
		t.Fatal("cleanup followed or retained mirrored home")
	}
}

func TestStaleSweepSkipsMirroredHomeAndSymlinkedLeg(t *testing.T) {
	owner, data := t.TempDir(), t.TempDir()
	store := NewStore(data)
	writeFile(t, filepath.Join(owner, "keep"), "owner bytes")
	is(t, "")(nil, writeOwned(owner, Owned{RunID: "run_loop", Leg: "head", PGID: Proc{PID: 99}, State: "running"}))
	is(t, "")(nil, store.Save(Run{ID: "run_loop"}))
	head := store.legDir("run_loop", "head")
	is(t, "")(nil, os.MkdirAll(head, 0o700))
	writeFile(t, filepath.Join(head, "home", "nested", "owned.json"), "bait")
	is(t, "")(nil, os.Symlink(filepath.Dir(store.root), filepath.Join(head, "home", ".ao")))
	is(t, "")(nil, os.Symlink(owner, store.legDir("run_loop", "base")))
	f := &fakeSys{}
	sweepStale(data, f.ops(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(f.log) != 0 || !pathExists(filepath.Join(owner, "keep")) || !pathExists(filepath.Join(head, "home", "nested", "owned.json")) {
		t.Fatalf("sweep entered mirrored home or linked leg: %v", f.log)
	}
}

// Check links without following them, so a link cannot pull the walk out of the fixture.
func assertLegHomeUsesOwner(t *testing.T, legDir, owner string) {
	t.Helper()
	owner, err := filepath.EvalSymlinks(owner)
	is(t, "")(nil, err)
	links := 0
	err = filepath.WalkDir(filepath.Join(legDir, "home"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		links++
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		if !pathWithin(owner, target) {
			t.Errorf("leg home link %s points outside owner fixture: %s", path, target)
		}
		return nil
	})
	is(t, "")(nil, err)
	if links == 0 {
		t.Fatal("fixture did not exercise home links")
	}
	data, err := os.ReadFile(filepath.Join(legDir, "home", ".zshrc"))
	if err != nil || string(data) != "fixture owner rc\n" {
		t.Fatalf("leg copied rc from another home: %q %v", data, err)
	}
}

func TestLegHomeUsesExplicitOwnerWithDifferentProcessHome(t *testing.T) {
	owner, processHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", processHome)
	writeFile(t, filepath.Join(owner, ".zshrc"), "fixture owner rc\n")
	writeFile(t, filepath.Join(owner, ".codex", "auth.json"), "fixture credentials")
	writeFile(t, filepath.Join(owner, ".local", "share", "tool", "config"), "fixture tool")
	writeFile(t, filepath.Join(processHome, ".zshrc"), "wrong rc\n")
	writeFile(t, filepath.Join(processHome, "wrong-home"), "wrong home")
	leg := filepath.Join(t.TempDir(), "head")
	_, err := prepareLegEnv([]string{"HOME=" + processHome, "PATH=/usr/bin:/bin"}, leg, owner)
	is(t, "")(nil, err)
	assertLegHomeUsesOwner(t, leg, owner)
}

type fixtureHomeEntry struct {
	info fs.FileInfo
	data string
	link string
}

func fixtureHomeState(t *testing.T, home string) map[string]fixtureHomeEntry {
	t.Helper()
	entries := map[string]fixtureHomeEntry{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := fixtureHomeEntry{info: info}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.data = string(data)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		entries[path] = entry
		return nil
	})
	is(t, "")(nil, err)
	return entries
}

func assertFixtureHomeUnchanged(t *testing.T, home string, before map[string]fixtureHomeEntry) {
	t.Helper()
	after := fixtureHomeState(t, home)
	if len(after) != len(before) {
		t.Fatalf("owner entry count changed: %d to %d", len(before), len(after))
	}
	for path, old := range before {
		entry, ok := after[path]
		if !ok || !os.SameFile(old.info, entry.info) || old.info.Mode() != entry.info.Mode() || old.data != entry.data || old.link != entry.link {
			t.Errorf("owner bytes, inode, mode or link changed: %s", path)
		}
	}
}

func TestLegHomeAgentInstallsUsePrivateClones(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("successful install clones require macOS APFS")
	}
	owner, err := filepath.EvalSymlinks(t.TempDir())
	is(t, "")(nil, err)
	run := t.TempDir()
	for _, path := range []string{
		".grok/bin/agent", ".grok/downloads/archive", ".grok/completions/grok.bash",
		".grok/config.toml", ".grok/managed_config.toml", ".grok/requirements.toml", ".grok/auth.json",
		".kimi-code/bin/kimi", ".kimi-code/updates/archive", ".kimi-code/config/device_id",
		".opencode/plugins/plugin", ".opencode/skills/skill", ".opencode/openwork.json",
		".local/share/claude/versions/v1", ".local/share/claude/versions/v2", ".local/share/claude/ClaudeCode.app/Contents/Info.plist",
		".grok/bin-other/agent", "go/bin/owner-tool", "go/pkg/mod/shared", ".gitconfig",
	} {
		writeFile(t, filepath.Join(owner, filepath.FromSlash(path)), "owner "+path)
	}
	is(t, "")(nil, os.MkdirAll(filepath.Join(owner, ".local", "bin"), 0o700))
	for name, target := range map[string]string{
		"claude": filepath.Join(owner, ".local/share/claude/versions/v2"),
		"grok":   "../../.grok/bin/agent",
		"agent":  "grok",
		"other":  filepath.Join(owner, ".grok/bin-other/agent"),
	} {
		is(t, "")(nil, os.Symlink(target, filepath.Join(owner, ".local", "bin", name)))
	}
	before := fixtureHomeState(t, owner)
	head, base := filepath.Join(run, "head"), filepath.Join(run, "base")
	env, err := prepareLegEnv([]string{"PATH=/usr/bin:/bin"}, head, owner)
	is(t, "")(nil, err)
	is(t, "")(nil, buildLegHome(owner, base))
	for _, root := range legInstallRoots {
		path := filepath.Join(head, "home", filepath.FromSlash(root.path))
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("install root not private: %s: %v %v", path, info, err)
		}
		for _, cloned := range root.clonedPaths() {
			original, err := os.Stat(filepath.Join(owner, cloned))
			is(t, "")(nil, err)
			private, err := os.Lstat(filepath.Join(head, "home", cloned))
			is(t, "")(nil, err)
			if private.Mode()&os.ModeSymlink != 0 || os.SameFile(original, private) {
				t.Fatalf("clone shares the owner inode: %s", cloned)
			}
		}
	}
	for name, want := range map[string]string{
		"claude": filepath.Join(head, "home/.local/share/claude/versions/v2"),
		"grok":   filepath.Join(head, "home/.grok/bin/agent"),
		"agent":  filepath.Join(head, "home/.grok/bin/agent"),
		"other":  filepath.Join(owner, ".local/bin/other"),
	} {
		got, err := os.Readlink(filepath.Join(head, "home/.local/bin", name))
		if err != nil || got != want {
			t.Fatalf("leg %s link = %q, %v; want %q", name, got, err, want)
		}
	}
	cmd := exec.Command("/bin/sh", "-ec", `
printf replacement > "$HOME/.grok/downloads/new-agent"
mv "$HOME/.grok/downloads/new-agent" "$HOME/.grok/bin/agent"
ln -sf agent "$HOME/.grok/bin/grok"
printf completion > "$HOME/.grok/completions/grok.bash"
printf config > "$HOME/.grok/config.toml"
printf managed > "$HOME/.grok/managed_config.toml"
printf requirements > "$HOME/.grok/requirements.toml"
printf kimi > "$HOME/.kimi-code/bin/kimi"
printf update > "$HOME/.kimi-code/updates/archive"
printf v3 > "$HOME/.local/share/claude/versions/v3"
rm "$HOME/.local/share/claude/versions/v1"
ln -sf ../share/claude/versions/v3 "$HOME/.local/bin/claude"
mkdir -p "$HOME/.opencode/bin"
printf opencode > "$HOME/.opencode/bin/opencode"
printf go-tool > "$GOBIN/tool"
`)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install fixtures: %s: %v", out, err)
	}
	_, err = prepareLegEnv(env, head, owner)
	is(t, "")(nil, err)
	for path, want := range map[string]string{
		".grok/bin/agent": "replacement", ".grok/bin/grok": "replacement", ".grok/completions/grok.bash": "completion",
		".grok/config.toml": "config", ".grok/managed_config.toml": "managed", ".grok/requirements.toml": "requirements",
		".kimi-code/bin/kimi": "kimi", ".kimi-code/updates/archive": "update",
		".local/bin/claude": "v3", ".opencode/bin/opencode": "opencode",
	} {
		data, err := os.ReadFile(filepath.Join(head, "home", filepath.FromSlash(path)))
		if err != nil || string(data) != want {
			t.Errorf("leg install %s = %q, %v; want %q", path, data, err, want)
		}
	}
	if pathExists(filepath.Join(head, "home/.local/share/claude/versions/v1")) || pathExists(filepath.Join(base, "home/.opencode/bin")) {
		t.Fatal("relaunch restored a pruned clone or install reached the base leg")
	}
	for _, path := range []string{".grok/auth.json", ".kimi-code/config", ".opencode/plugins", ".opencode/skills", ".opencode/openwork.json", "go"} {
		got, err := os.Readlink(filepath.Join(head, "home", filepath.FromSlash(path)))
		if err != nil || got != filepath.Join(owner, filepath.FromSlash(path)) {
			t.Errorf("shared entry %s = %q, %v", path, got, err)
		}
	}
	assertFixtureHomeUnchanged(t, owner, before)
}

func TestProductionTeardownPreservesFixtureOwner(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("successful install clones require macOS APFS")
	}
	owner, err := filepath.EvalSymlinks(t.TempDir())
	is(t, "")(nil, err)
	for _, path := range []string{
		".grok/bin/agent", ".grok/downloads/A", ".grok/downloads/archive", ".grok/completions/grok.bash",
		".grok/auth.json", ".grok/requirements.toml", ".kimi-code/bin/kimi", ".kimi-code/updates/archive",
		".opencode/bin/opencode", ".opencode/plugins/plugin", ".opencode/skills/skill", ".opencode/openwork.json",
		".local/share/claude/versions/v1", "go/bin/owner-tool", ".gitconfig",
	} {
		writeFile(t, filepath.Join(owner, filepath.FromSlash(path)), "owner "+path)
	}
	config := filepath.Join(owner, ".grok", "config.toml")
	writeFile(t, config, "shared config")
	is(t, "")(nil, os.Link(config, filepath.Join(owner, ".grok", "managed_config.toml")))
	is(t, "")(nil, os.Symlink("../downloads/A", filepath.Join(owner, ".grok", "bin", "grok")))
	is(t, "")(nil, os.MkdirAll(filepath.Join(owner, ".local", "bin"), 0o700))
	is(t, "")(nil, os.Symlink("../../.grok/bin/grok", filepath.Join(owner, ".local", "bin", "grok")))
	before := fixtureHomeState(t, owner)

	svc, _ := newSvc(t)
	svc.WithOwnerHome(owner, nil)
	run := Run{ID: "run_prod_teardown", State: RunRunning, Legs: []Leg{{Name: "head"}}}
	is(t, "")(nil, svc.store.Save(run))
	leg := svc.store.legDir(run.ID, "head")
	is(t, "")(nil, buildLegHome(owner, leg))
	legHome := filepath.Join(leg, "home")
	for _, root := range legInstallRoots {
		path := filepath.Join(legHome, filepath.FromSlash(root.path))
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Fatalf("install root %s missing from leg: %v", root.path, err)
		}
		for _, cloned := range root.clonedPaths() {
			if _, err := os.Lstat(filepath.Join(legHome, cloned)); err != nil {
				t.Fatalf("cloned install path %s missing from leg: %v", cloned, err)
			}
		}
	}
	if got, err := os.Readlink(filepath.Join(legHome, ".local", "bin", "grok")); err != nil || got != filepath.Join(legHome, ".grok", "bin", "grok") {
		t.Fatalf("leg grok link = %q, %v; want clone bin/grok", got, err)
	}
	writeFile(t, filepath.Join(legHome, ".grok", "downloads", "B"), "B")
	is(t, "")(nil, os.Remove(filepath.Join(legHome, ".grok", "bin", "grok")))
	is(t, "")(nil, os.Symlink("../downloads/B", filepath.Join(legHome, ".grok", "bin", "grok")))
	if data, err := os.ReadFile(filepath.Join(legHome, ".local", "bin", "grok")); err != nil || string(data) != "B" {
		t.Fatalf("relinked leg grok = %q, %v; want B", data, err)
	}
	is(t, "")(nil, writeOwned(leg, Owned{RunID: run.ID, Leg: "head", State: "running"}))
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(nil, svc.teardown(ctx, run))
	if pathExists(legHome) {
		t.Fatal("production teardown retained leg home")
	}
	assertFixtureHomeUnchanged(t, owner, before)
}

func TestProductionTeardownUnlinksLegacyGrokRoot(t *testing.T) {
	owner := t.TempDir()
	writeFile(t, filepath.Join(owner, ".grok", "owner.txt"), "owner")
	before := fixtureHomeState(t, owner)

	svc, _ := newSvc(t)
	svc.WithOwnerHome(owner, nil)
	run := Run{ID: "run_legacy_grok", State: RunRunning, Legs: []Leg{{Name: "head"}}}
	is(t, "")(nil, svc.store.Save(run))
	leg := svc.store.legDir(run.ID, "head")
	is(t, "")(nil, os.MkdirAll(filepath.Join(leg, "home"), 0o700))
	is(t, "")(nil, os.Symlink(filepath.Join(owner, ".grok"), filepath.Join(leg, "home", ".grok")))
	is(t, "")(nil, writeOwned(leg, Owned{RunID: run.ID, Leg: "head", State: "running"}))
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(nil, svc.teardown(ctx, run))
	if pathExists(filepath.Join(leg, "home")) {
		t.Fatal("production teardown retained legacy leg home")
	}
	if !pathExists(filepath.Join(owner, ".grok", "owner.txt")) {
		t.Fatal("teardown followed legacy .grok symlink into owner")
	}
	assertFixtureHomeUnchanged(t, owner, before)
	forks, err := os.ReadFile(filepath.Join(leg, legHomeForksFile))
	if err != nil || !strings.Contains(string(forks), "linked, not inspected") {
		t.Fatalf("legacy symlink report = %q, %v", forks, err)
	}
}

func TestLegHomeCreatesOnlyOwnerInstallRoots(t *testing.T) {
	owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
	is(t, "")(nil, buildLegHome(owner, leg))
	for _, root := range legInstallRoots {
		if _, err := os.Lstat(filepath.Join(leg, "home", filepath.FromSlash(root.path))); !os.IsNotExist(err) {
			t.Errorf("created absent owner root %s: %v", root.path, err)
		}
	}
}

func TestLegHomeRefusesRedirectedInstallRoots(t *testing.T) {
	for _, root := range legInstallRoots {
		t.Run(root.path, func(t *testing.T) {
			owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
			source := filepath.Join(owner, filepath.FromSlash(root.path))
			writeFile(t, filepath.Join(source, "keep"), "owner bytes")
			dest := filepath.Join(leg, "home", filepath.FromSlash(root.path))
			is(t, "")(nil, os.MkdirAll(filepath.Dir(dest), 0o700))
			is(t, "")(nil, os.Symlink(source, dest))
			before := fixtureHomeState(t, owner)
			if err := buildLegHome(owner, leg); err == nil || !strings.Contains(err.Error(), "refusing redirected leg directory") {
				t.Fatalf("accepted redirected install root: %v", err)
			}
			assertFixtureHomeUnchanged(t, owner, before)
		})
	}
}

func TestLegHomeRefusesSymlinkCloneSources(t *testing.T) {
	for _, path := range []string{".grok/bin", ".local/share/claude"} {
		t.Run(path, func(t *testing.T) {
			owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
			target := filepath.Join(owner, "install-target")
			writeFile(t, filepath.Join(target, "keep"), "owner bytes")
			is(t, "")(nil, os.Chmod(target, 0o755))
			source := filepath.Join(owner, filepath.FromSlash(path))
			is(t, "")(nil, os.MkdirAll(filepath.Dir(source), 0o700))
			is(t, "")(nil, os.Symlink(target, source))
			before := fixtureHomeState(t, owner)
			if err := buildLegHome(owner, leg); err == nil || !strings.Contains(err.Error(), "refusing symlink install clone source") {
				t.Fatalf("accepted owner symlink as a private clone: %v", err)
			}
			assertFixtureHomeUnchanged(t, owner, before)
		})
	}
}

func TestLegHomeRefusesExistingCloneLinks(t *testing.T) {
	for _, path := range []string{".grok/bin", ".grok/config.toml", ".kimi-code/bin"} {
		t.Run(path, func(t *testing.T) {
			owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
			source := filepath.Join(owner, filepath.FromSlash(path))
			if strings.HasSuffix(path, ".toml") {
				writeFile(t, source, "owner config")
			} else {
				writeFile(t, filepath.Join(source, "keep"), "owner bytes")
			}
			dest := filepath.Join(leg, "home", filepath.FromSlash(path))
			is(t, "")(nil, os.MkdirAll(filepath.Dir(dest), 0o700))
			is(t, "")(nil, os.Symlink(source, dest))
			before := fixtureHomeState(t, owner)
			if err := buildLegHome(owner, leg); err == nil || !strings.Contains(err.Error(), "refusing symlink install clone destination") {
				t.Fatalf("accepted an existing clone link: %v", err)
			}
			assertFixtureHomeUnchanged(t, owner, before)
		})
	}
}

func TestLegHomeRefusesHardLinkedInstallFiles(t *testing.T) {
	for _, path := range []string{".grok/config.toml", ".opencode/openwork.json"} {
		t.Run(path, func(t *testing.T) {
			owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
			source := filepath.Join(owner, filepath.FromSlash(path))
			writeFile(t, source, "owner config")
			dest := filepath.Join(leg, "home", filepath.FromSlash(path))
			is(t, "")(nil, os.MkdirAll(filepath.Dir(dest), 0o700))
			is(t, "")(nil, os.Link(source, dest))
			before := fixtureHomeState(t, owner)
			if err := buildLegHome(owner, leg); err == nil || !strings.Contains(err.Error(), "refusing owner-linked") {
				t.Fatalf("accepted an owner hard link: %v", err)
			}
			assertFixtureHomeUnchanged(t, owner, before)
		})
	}
}

func TestLegHomeRefusesRedirectedAbsentInstallRoots(t *testing.T) {
	for _, root := range legInstallRoots {
		t.Run(root.path, func(t *testing.T) {
			owner, leg, target := t.TempDir(), filepath.Join(t.TempDir(), "head"), t.TempDir()
			writeFile(t, filepath.Join(target, "keep"), "outside bytes")
			dest := filepath.Join(leg, "home", filepath.FromSlash(root.path))
			is(t, "")(nil, os.MkdirAll(filepath.Dir(dest), 0o700))
			is(t, "")(nil, os.Symlink(target, dest))
			before := fixtureHomeState(t, target)
			if err := buildLegHome(owner, leg); err == nil || !strings.Contains(err.Error(), "refusing redirected leg directory") {
				t.Fatalf("accepted redirected absent root: %v", err)
			}
			assertFixtureHomeUnchanged(t, target, before)
		})
	}
}

func TestLegHomeGitGlobalConfigIsPrivate(t *testing.T) {
	git, err := exec.LookPath("git")
	is(t, "")(nil, err)
	owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
	writeFile(t, filepath.Join(owner, ".gitconfig"), "[user]\n\tname = Owner\n")
	before := fixtureHomeState(t, owner)
	env, err := prepareLegEnv([]string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1"}, leg, owner)
	is(t, "")(nil, err)
	cmd := exec.Command(git, "config", "--global", "user.name", "Leg User")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git global config: %s: %v", out, err)
	}
	data, err := os.ReadFile(filepath.Join(leg, "home", ".gitconfig"))
	if err != nil || !strings.Contains(string(data), "Leg User") {
		t.Fatalf("leg global config not changed: %q %v", data, err)
	}
	assertFixtureHomeUnchanged(t, owner, before)
}

func TestLegHomeCloneFailureRefusesTargetStart(t *testing.T) {
	owner, work := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(owner, ".grok/bin/agent"), "owner")
	before := fixtureHomeState(t, owner)
	svc, _ := newSvc(t)
	svc.WithOwnerHome(owner, []string{"PATH=/usr/bin:/bin"})
	is(t, "")(nil, writeManifest(work, Manifest{SHA: "fixture"}))
	svc.sys.freePorts = func(int) ([]int, error) { return []int{3200, 9400}, nil }
	original := cloneLegInstall
	t.Cleanup(func() { cloneLegInstall = original })
	failure := errors.New("injected clone failure")
	cloneLegInstall = func(string, string) error { return failure }
	_, err := svc.startLeg(ctx, "run_s1", 1, Recipe{Run: Step{Cmd: "/nonexistent/litmus-target"}}, Leg{Name: "head", Worktree: work}, newLaps(), nil, false, "launch")
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "refusing to start leg") {
		t.Fatalf("clone failure did not refuse launch: %v", err)
	}
	leg := svc.store.legDir("run_s1", "head")
	for _, path := range []string{"owned.json", "logs", "home/.grok/bin"} {
		if _, err := os.Lstat(filepath.Join(leg, filepath.FromSlash(path))); !os.IsNotExist(err) {
			t.Errorf("clone failure reached target launch or installed a fallback at %s: %v", path, err)
		}
	}
	assertFixtureHomeUnchanged(t, owner, before)
}

func TestLegHomeCloneRefusesNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin refusal")
	}
	owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
	writeFile(t, filepath.Join(owner, ".kimi-code/bin/kimi"), "owner")
	if err := buildLegHome(owner, leg); err == nil || !strings.Contains(err.Error(), "require APFS clonefile on macOS") {
		t.Fatalf("accepted unsupported clone: %v", err)
	}
}

func TestHomeGuardAgentInstallRootsAndForks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("successful install clones require macOS APFS")
	}
	owner := t.TempDir()
	svc, _ := newSvc(t)
	svc.WithOwnerHome(owner, nil)
	for _, path := range []string{".grok/downloads/archive", ".grok/auth.json", ".opencode/openwork.json"} {
		writeFile(t, filepath.Join(owner, path), "before")
	}
	roots, err := ownerHomeRoots(owner, nil)
	is(t, "")(nil, err)
	wantDepth := map[string]int{filepath.Join(owner, "go/bin"): 1}
	for _, root := range legInstallRoots {
		for _, cloned := range root.clonedPaths() {
			wantDepth[filepath.Join(owner, cloned)] = 1
		}
	}
	var fixtureRoots []homeScanRoot
	for _, root := range roots {
		if depth, ok := wantDepth[root.Path]; ok {
			if root.Depth != depth {
				t.Errorf("guard root %+v; want depth %d", root, depth)
			}
			delete(wantDepth, root.Path)
		}
		if pathWithin(owner, root.Path) {
			fixtureRoots = append(fixtureRoots, root)
		}
	}
	if len(wantDepth) != 0 {
		t.Fatalf("missing guard roots: %v", wantDepth)
	}
	svc.snapshotHome = func() (homeSnapshot, error) { return scanHomeRoots(fixtureRoots, svc.store.root) }
	run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	is(t, "")(nil, svc.snapshotIntakeHome(run.ID))
	leg := svc.store.legDir(run.ID, "head")
	is(t, "")(nil, buildLegHome(owner, leg))
	// A fork in another run must not be attributed to this run.
	other := svc.store.legDir("run_other", "head")
	is(t, "")(nil, buildLegHome(owner, other))
	for _, path := range []string{filepath.Join(leg, "home/.grok/auth.json"), filepath.Join(leg, "home/.opencode/openwork.json"), filepath.Join(other, "home/.grok/auth.json")} {
		is(t, "")(nil, os.Remove(path))
		writeFile(t, path, "fork")
	}
	archive := filepath.Join(owner, ".grok/downloads/archive")
	writeFile(t, archive, "owner install changed")
	is(t, "")(nil, writeOwned(leg, Owned{RunID: run.ID, Leg: "head", State: "running"}))
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(nil, svc.teardown(ctx, run))
	if pathExists(filepath.Join(leg, "home")) {
		t.Fatal("teardown did not exercise home deletion")
	}
	report, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	for _, want := range []string{
		"MODIFIED " + strconv.Quote(archive),
		"FORKED " + strconv.Quote(filepath.Join(leg, "home/.grok/auth.json")),
		"FORKED " + strconv.Quote(filepath.Join(leg, "home/.opencode/openwork.json")),
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %s: %s", want, report)
		}
	}
	if strings.Contains(report, other) {
		t.Fatalf("reported another run's fork: %s", report)
	}
}

func TestHomeGuardForksSurviveStaleCleanup(t *testing.T) {
	for _, wiped := range []bool{false, true} {
		t.Run(strconv.FormatBool(wiped), func(t *testing.T) {
			owner := t.TempDir()
			svc, _ := newSvc(t)
			svc.WithOwnerHome(owner, nil)
			writeFile(t, filepath.Join(owner, ".opencode/openwork.json"), "owner")
			svc.snapshotHome = func() (homeSnapshot, error) {
				return scanHomeRoots([]homeScanRoot{{Path: owner, Depth: 1, Required: true}}, svc.store.root)
			}
			run, err := svc.claim("run_stale", "stale", "o/r", 1, CommentOff, EvidenceOff)
			is(t, "")(nil, err)
			is(t, "")(nil, svc.snapshotIntakeHome(run.ID))
			leg := svc.store.legDir(run.ID, "head")
			is(t, "")(nil, buildLegHome(owner, leg))
			fork := filepath.Join(leg, "home/.opencode/openwork.json")
			is(t, "")(nil, os.Remove(fork))
			writeFile(t, fork, "leg fork")
			is(t, "")(nil, writeOwned(leg, Owned{RunID: run.ID, Leg: "head", State: "running"}))
			if wiped {
				// Simulate a daemon restart between deleting HOME and finalizing the report.
				is(t, "")(nil, cleanLeg(ctx, (&fakeSys{}).ops(), leg, func() error {
					_, err := svc.legHomeForksBeforeWipe(run.ID, "head")
					return err
				}))
			}
			sweepStale(filepath.Dir(filepath.Dir(svc.store.root)), (&fakeSys{}).ops(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if pathExists(filepath.Join(leg, "home")) {
				t.Fatal("stale cleanup did not exercise home deletion")
			}
			report, err := svc.store.HomeChanges(run.ID)
			is(t, "")(nil, err)
			if !strings.Contains(report, "FORKED "+strconv.Quote(fork)) {
				t.Fatalf("stale cleanup lost fork: %s", report)
			}
		})
	}
}

func TestHomeGuardRemovedInstallRootFinishes(t *testing.T) {
	owner := t.TempDir()
	svc, _ := newSvc(t)
	svc.WithOwnerHome(owner, nil)
	root := filepath.Join(owner, ".opencode")
	writeFile(t, filepath.Join(root, "openwork.json"), "owner")
	svc.snapshotHome = func() (homeSnapshot, error) {
		return scanHomeRoots([]homeScanRoot{{Path: owner, Depth: 1, Required: true}}, svc.store.root)
	}
	run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	is(t, "")(nil, svc.snapshotIntakeHome(run.ID))
	is(t, "")(nil, buildLegHome(owner, svc.store.legDir(run.ID, "head")))
	is(t, "")(nil, os.RemoveAll(root))
	is(t, "")(nil, svc.finishHomeGuard(run.ID))
	report, err := svc.store.HomeChanges(run.ID)
	is(t, "")(nil, err)
	if !strings.Contains(report, "REMOVED "+strconv.Quote(root)) {
		t.Fatalf("removed install root not reported: %s", report)
	}
}
