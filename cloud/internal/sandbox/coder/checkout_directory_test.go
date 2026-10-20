package coder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

// An ao-worker session with a working folder probes the login user's home,
// checks the repository out in ~/work/<repo> instead of <durable
// root>/repository, and prepares that folder and its siblings in the
// bootstrap. The durable root itself is no longer made group-writable: the
// additional repositories live beside the working folder.
func TestAOWorkerBootstrapUsesTheWorkingFolderCheckout(t *testing.T) {
	t.Parallel()
	for _, durableRoot := range []string{sandbox.CoderHomeDurableRoot, "/home/ubuntu"} {
		t.Run(durableRoot, func(t *testing.T) {
			t.Parallel()
			fake := newDevEnvCoder(t, nil)
			server := httptest.NewServer(fake)
			t.Cleanup(server.Close)
			client := newTestClient(t, server.URL, nil)
			client.checkoutDirectory = "~/work/alice"
			client.checkoutSiblings = []string{"billing"}
			recorder := &stageRecorder{}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctx = sandbox.WithStageObserver(ctx, recorder.observe)
			if err := client.BootstrapWorker(ctx, testWorkspaceID, multiArchBootstrap(durableRoot)); err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			commands := fake.recordedCommands()
			if !strings.Contains(commands[0], workspaceProbe) || strings.Contains(commands[0], "id -un") {
				t.Fatalf("first command is not the home probe: %s", commands[0])
			}
			env := fake.lastArchive()["worker.env"]
			for _, want := range []string{
				"AO_WORKSPACE_DIR='/home/ubuntu/work/alice'\n",
				CheckoutDirectoryEnv + "='/home/ubuntu/work/alice'\n",
				UserHomeEnv + "='/home/ubuntu'\n",
			} {
				if !strings.Contains(env, want) {
					t.Errorf("worker.env missing %q:\n%s", want, env)
				}
			}
			if strings.Contains("\n"+env, "\nHOME='/home/ubuntu'\n") || strings.Contains(env, WorkingDirectoryEnv) {
				t.Errorf("an ao-worker got the login user's HOME or a working directory:\n%s", env)
			}
			script := unquoteBootstrap(t, commands[len(commands)-1])
			shellSyntaxCheck(t, script)
			root := durableRoot
			if root == sandbox.CoderHomeDurableRoot {
				root = "/home/ubuntu"
			}
			for _, want := range []string{
				"checkout_parent='/home/ubuntu/work'\n",
				"sudo -n -u 'ao-worker' test -x '/home/ubuntu' || { [ ! -L '/home/ubuntu' ] && sudo -n chmod o+x '/home/ubuntu'; } || ao_folder_unusable ",
				"sudo -n -u 'ao-worker' test -x '/home/ubuntu/work' || { [ ! -L '/home/ubuntu/work' ] && sudo -n chmod o+x '/home/ubuntu/work'; } || ao_folder_unusable ",
				"if [ ! -e '/home/ubuntu/work' ] && [ ! -L '/home/ubuntu/work' ]; then\n  { sudo -n mkdir '/home/ubuntu/work' &&\n" +
					"    sudo -n chown -h \"$(id -u):\"'ao-worker' '/home/ubuntu/work' &&\n    sudo -n chmod g+wx '/home/ubuntu/work'; } ||\n",
				"ao_checkout_folder '/home/ubuntu/work/alice' || true\n",
				"ao_checkout_folder '/home/ubuntu/work/billing' || true\n",
				"useradd -m 'ao-worker'",
			} {
				if !strings.Contains(script, want) {
					t.Errorf("bootstrap script missing %q", want)
				}
			}
			// Nothing outside the login user's home is ever made traversable.
			for _, unwanted := range []string{"'" + root + "/repository'", "chmod g+wx \"$durable_root\"", "chgrp 'ao-worker' \"$durable_root\"",
				"chmod o+x '/home'", "chmod o+x '/'", "mkdir -p '/home/ubuntu/work'"} {
				if strings.Contains(script, unwanted) {
					t.Errorf("bootstrap script still contains %q", unwanted)
				}
			}
			install, ok := recorder.stage(sandbox.StageWorkerInstall)
			if !ok || install.Attrs["checkout_directory"] != "/home/ubuntu/work/alice" || install.Attrs["worker_user"] != domain.CoderWorkerUserAOWorker {
				t.Fatalf("worker install stage = %+v", install)
			}
		})
	}
}

// A working folder outside the durable root would lose the agent's
// uncommitted work on a workspace restart, so the session stops with its own
// startup error before anything is installed.
func TestAOWorkerBootstrapRefusesAWorkingFolderOutsideTheDurableRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ durableRoot, checkout, shown string }{
		{sandbox.CoderHomeDurableRoot, "/workspace/alice", "your home folder (/home/ubuntu)"},
		{"/home/coder", "~/work/alice", "/home/coder"},
		{"/home/ubuntu/work", "/home/ubuntu/work", "/home/ubuntu/work"},
	} {
		t.Run(tc.durableRoot+" "+tc.checkout, func(t *testing.T) {
			t.Parallel()
			fake := newDevEnvCoder(t, nil)
			server := httptest.NewServer(fake)
			t.Cleanup(server.Close)
			client := newTestClient(t, server.URL, nil)
			client.checkoutDirectory = tc.checkout
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := client.BootstrapWorker(ctx, testWorkspaceID, multiArchBootstrap(tc.durableRoot))
			var startupErr *sandbox.StartupError
			if !errors.As(err, &startupErr) || startupErr.Code != sandbox.StartupErrorWorkingDirectoryNotDurable ||
				!strings.HasPrefix(startupErr.Message, "The working folder must be inside "+tc.shown+" so the agent's work survives restarts.") {
				t.Fatalf("bootstrap error = %#v", err)
			}
			if archive := fake.lastArchive(); archive != nil {
				t.Fatalf("the worker was installed: %v", archive)
			}
		})
	}
}

// Without a working folder the dedicated-user bootstrap is exactly the
// historical one: the durable root checkout, group-writable for its siblings.
func TestAOWorkerBootstrapWithoutWorkingFolderKeepsTheDurableRootCheckout(t *testing.T) {
	t.Parallel()
	script := unquoteBootstrap(t, bootstrapCommandForArchive(multiArchBootstrap("/home/coder"), sandbox.ArchAMD64, 100, false))
	for _, want := range []string{
		"sudo -n mkdir -p '/home/coder/repository' '/home/coder/.ao/worker' ",
		"sudo -n chown -R 'ao-worker:ao-worker' '/home/coder/repository' '/home/coder/.ao'\n",
		"sudo -n chgrp 'ao-worker' \"$durable_root\"\nsudo -n chmod g+wx \"$durable_root\"\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bootstrap script missing %q", want)
		}
	}
	if strings.Contains(script, "checkout_parent") || strings.Contains(script, "ao_checkout_folder") {
		t.Error("a session without a working folder prepares one")
	}
}

func TestExpandCheckoutDirectory(t *testing.T) {
	t.Parallel()
	for directory, want := range map[string]string{
		"~/work/alice":     "/home/ubuntu/work/alice",
		"~/work/alice/":    "/home/ubuntu/work/alice",
		"$HOME/work/alice": "/home/ubuntu/work/alice",
		"/srv/work/alice":  "/srv/work/alice",
	} {
		if got, err := ExpandCheckoutDirectory(directory, "/home/ubuntu"); err != nil || got != want {
			t.Errorf("ExpandCheckoutDirectory(%q) = %q, %v; want %q", directory, got, err, want)
		}
	}
	for _, directory := range []string{"/alice", "/", "work/alice", "~/", "/srv/./work"} {
		if got, err := ExpandCheckoutDirectory(directory, "/home/ubuntu"); err == nil {
			t.Errorf("ExpandCheckoutDirectory(%q) = %q, want an error", directory, got)
		}
	}
	if got, err := ExpandCheckoutDirectory("~/work/alice", ""); err == nil {
		t.Errorf("a home-relative folder resolved without a home: %q", got)
	}
}

// The folder preparation really runs: a missing parent is created for the
// login user (with group write for the worker), a missing checkout or
// sibling folder is created for the worker user, and a folder that already
// exists (the template's own checkout, or a symbolic link) is never chowned
// or changed. sudo is a stand-in that records ownership changes.
func TestCheckoutDirectoryScriptCreatesOnlyMissingFolders(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is required")
	}
	run := func(t *testing.T, home, directory string, siblings []string) []string {
		t.Helper()
		log := filepath.Join(t.TempDir(), "changes.log")
		script := "set -eu\n" +
			"sudo() {\n" +
			"  as=root\n" +
			"  while [ $# -gt 0 ]; do case \"$1\" in -n|-b) shift ;; -u) as=$2; shift 2 ;; *) break ;; esac; done\n" +
			"  case \"$1\" in chown|chmod|chgrp) echo \"$*\" >>" + shellQuote(log) + "; return 0 ;; esac\n" +
			"  if [ \"$1\" = mkdir ]; then echo \"mkdir as $as $2\" >>" + shellQuote(log) + "; fi\n" +
			"  \"$@\"\n}\n" +
			checkoutDirectoryScript("ao-worker", directory, siblings, home, home) + "echo __DONE__\n"
		output, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil || !strings.Contains(string(output), "__DONE__") {
			t.Fatalf("script failed: %v\n%s", err, output)
		}
		raw, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}

	t.Run("fresh workspace", func(t *testing.T) {
		home := t.TempDir()
		parent := filepath.Join(home, "work")
		changes := run(t, home, filepath.Join(parent, "alice"), []string{"billing"})
		for _, dir := range []string{parent, filepath.Join(parent, "alice"), filepath.Join(parent, "billing")} {
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Fatalf("%s was not created: %v", dir, err)
			}
		}
		for _, change := range changes {
			if strings.Contains(change, "chown ao-worker") {
				t.Errorf("root chowned a checkout folder instead of the worker creating it: %q", change)
			}
		}
		for _, want := range []string{
			"chmod g+wx " + parent,
			"mkdir as ao-worker " + filepath.Join(parent, "alice"),
			"mkdir as ao-worker " + filepath.Join(parent, "billing"),
		} {
			if !slices.Contains(changes, want) {
				t.Errorf("changes %q lack %q", changes, want)
			}
		}
	})

	t.Run("deeper parents are the login user's", func(t *testing.T) {
		home := t.TempDir()
		parent := filepath.Join(home, "src", "acme")
		changes := run(t, home, filepath.Join(parent, "alice"), nil)
		owner := fmt.Sprintf("chown -h %d:%d ", os.Getuid(), os.Getgid())
		for _, want := range []string{owner + filepath.Join(home, "src"), "chmod g+wx " + parent} {
			if !slices.Contains(changes, want) {
				t.Errorf("changes %q lack %q", changes, want)
			}
		}
		for _, change := range changes {
			if strings.HasPrefix(change, "chown root") {
				t.Errorf("a parent was left to root: %q", change)
			}
		}
	})

	t.Run("template already cloned the folder", func(t *testing.T) {
		home := t.TempDir()
		parent := filepath.Join(home, "work")
		occupied := filepath.Join(parent, "alice")
		if err := os.MkdirAll(filepath.Join(occupied, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(occupied, filepath.Join(parent, "billing")); err != nil {
			t.Fatal(err)
		}
		changes := run(t, home, occupied, []string{"billing", "api"})
		for _, change := range changes {
			if strings.Contains(change, occupied) || strings.Contains(change, filepath.Join(parent, "billing")) ||
				strings.HasSuffix(change, " "+parent) {
				t.Errorf("an existing folder was changed: %q", change)
			}
		}
		if !slices.Contains(changes, "mkdir as ao-worker "+filepath.Join(parent, "api")) {
			t.Errorf("the missing sibling was not prepared: %q", changes)
		}
	})
}

// Traverse permission is given only to the login user's home and the folders
// below it, never to "/", "/root" or a folder outside the home; missing
// parents are created only below the home or the durable root.
func TestCheckoutDirectoryScriptTouchesOnlyTheHome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, directory, home, root string
		chmod, mkdir                []string
		never                       []string
	}{
		{"home", "/home/ubuntu/work/alice", "/home/ubuntu", "/home/ubuntu",
			[]string{"/home/ubuntu", "/home/ubuntu/work"}, []string{"/home/ubuntu/work"}, []string{"/", "/home"}},
		{"root's home", "/root/work/alice", "/root", "/root",
			[]string{"/root/work"}, []string{"/root/work"}, []string{"/", "/root"}},
		{"durable root outside the home", "/srv/dev/work/alice", "/home/ubuntu", "/srv/dev",
			nil, []string{"/srv/dev/work"}, []string{"/", "/srv", "/srv/dev", "/srv/dev/work"}},
	} {
		script := checkoutDirectoryScript("ao-worker", tc.directory, nil, tc.home, tc.root)
		for _, dir := range tc.chmod {
			if !strings.Contains(script, "[ ! -L '"+dir+"' ] && sudo -n chmod o+x '"+dir+"'; }") {
				t.Errorf("%s: %s is not made traversable", tc.name, dir)
			}
		}
		for _, dir := range tc.mkdir {
			if !strings.Contains(script, "sudo -n mkdir '"+dir+"' ") {
				t.Errorf("%s: missing %s is not created", tc.name, dir)
			}
		}
		for _, dir := range tc.never {
			if strings.Contains(script, "chmod o+x '"+dir+"'") {
				t.Errorf("%s: %s is made traversable", tc.name, dir)
			}
		}
		for _, dir := range []string{"/srv", "/home", "/"} {
			if strings.Contains(script, "sudo -n mkdir '"+dir+"' ") {
				t.Errorf("%s: %s would be created", tc.name, dir)
			}
		}
	}
}

// A parent folder the bootstrap cannot prepare stops the bootstrap with the
// working-folder marker, which becomes the session's
// working_directory_unusable startup error (parked, not retried forever).
func TestCheckoutDirectoryScriptReportsAnUnusableWorkingFolder(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is required")
	}
	parent := filepath.Join(t.TempDir(), "work")
	script := "set -eu\n" +
		"sudo() {\n" +
		"  while [ $# -gt 0 ]; do case \"$1\" in -n|-b) shift ;; -u) shift 2 ;; *) break ;; esac; done\n" +
		"  case \"$1\" in mkdir) echo \"mkdir: permission denied\" >&2; return 1 ;; chown|chmod|chgrp) return 0 ;; esac\n" +
		"  \"$@\"\n}\n" +
		checkoutDirectoryScript("ao-worker", filepath.Join(parent, "alice"), nil, filepath.Dir(parent), filepath.Dir(parent)) + "echo __DONE__\n"
	output, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err == nil || strings.Contains(string(output), "__DONE__") || !strings.Contains(string(output), workingFolderUnusableMarker+" could not create "+parent) {
		t.Fatalf("script = %v\n%s", err, output)
	}
	classified := classifyBootstrapError(errors.New("coder: bootstrap failed: "+string(output)), "/home/ubuntu")
	var startupErr *sandbox.StartupError
	if !errors.As(classified, &startupErr) || startupErr.Code != sandbox.StartupErrorWorkingDirectoryUnusable ||
		!strings.Contains(startupErr.Message, "working folder in the workspace: could not create "+parent+". ") {
		t.Fatalf("classified = %#v", classified)
	}
}

// A session profile stamps the checkout on the session client only for the
// dedicated worker user.
func TestForSandboxCarriesTheWorkingFolderCheckout(t *testing.T) {
	t.Parallel()
	base := newTestClient(t, "https://coder.example.com", nil)
	profile := func(extra string) json.RawMessage {
		return json.RawMessage(`{"coder":{"baseUrl":"https://coder.example.com","owner":"o","templateId":"` + testTemplateID +
			`","durableRoot":"$HOME"` + extra + `}}`)
	}
	provider, err := base.ForSandbox(domain.Sandbox{SessionID: "session-1",
		ResourceProfile: profile(`,"checkoutDirectory":"~/work/alice","checkoutSiblings":["billing"]`)})
	if err != nil {
		t.Fatal(err)
	}
	if client := provider.(*Client); client.checkoutDirectory != "~/work/alice" || !slices.Equal(client.checkoutSiblings, []string{"billing"}) {
		t.Fatalf("session client checkout = %q %q", client.checkoutDirectory, client.checkoutSiblings)
	}
	legacy, err := base.ForSandbox(domain.Sandbox{SessionID: "session-1", ResourceProfile: profile(``)})
	if err != nil {
		t.Fatal(err)
	}
	if client := legacy.(*Client); client.checkoutDirectory != "" || client.checkoutSiblings != nil {
		t.Fatalf("legacy session client checkout = %q %q", client.checkoutDirectory, client.checkoutSiblings)
	}
}
