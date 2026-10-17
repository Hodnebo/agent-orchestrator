package litmus

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLegShellDoesNotShadowCLIs(t *testing.T) {
	runDir, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	env := map[string]string{"PATH": "/usr/bin:/bin", "HOME": home, "GH_TOKEN": "test-owner-token", "GH_CONFIG_DIR": filepath.Join(home, "owner-gh")}
	if env["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("tester PATH changed: %s", env["PATH"])
	}
	legDir := filepath.Join(runDir, "head")
	legEnv, err := prepareLegEnv([]string{"PATH=" + env["PATH"], "HOME=" + home, "GH_TOKEN=" + env["GH_TOKEN"], "GH_CONFIG_DIR=" + env["GH_CONFIG_DIR"]}, legDir, home)
	is(t, "")(nil, err)
	if envValue(legEnv, "GH_TOKEN") != env["GH_TOKEN"] || envValue(legEnv, "GH_CONFIG_DIR") != env["GH_CONFIG_DIR"] {
		t.Fatal("leg shell changed GitHub authentication")
	}
	entries, err := os.ReadDir(filepath.Join(legDir, "bin"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("leg bin contains generated commands: %v, %v", entries, err)
	}
	for _, dir := range []string{filepath.Join(runDir, "agents"), filepath.Join(legDir, "guard")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("unexpected generated directory: %s: %v", dir, err)
		}
	}
	if got := filepath.SplitList(envValue(legEnv, "PATH"))[0]; got != filepath.Join(legDir, "bin") {
		t.Fatalf("leg PATH starts with %s", got)
	}
	if got := envValue(legEnv, "GOBIN"); got != filepath.Join(legDir, "bin") {
		t.Fatalf("GOBIN = %q, want %q", got, filepath.Join(legDir, "bin"))
	}
}

func TestRetargetLegBinLinksKeepsMutableInstallLink(t *testing.T) {
	owner, err := filepath.EvalSymlinks(t.TempDir())
	is(t, "")(nil, err)
	leg := t.TempDir()
	ownerGrokBin := filepath.Join(owner, ".grok", "bin")
	ownerGrokDownloads := filepath.Join(owner, ".grok", "downloads")
	legHome := filepath.Join(leg, "home")
	legGrokBin := filepath.Join(legHome, ".grok", "bin")
	legGrokDownloads := filepath.Join(legHome, ".grok", "downloads")

	is(t, "")(nil, os.MkdirAll(ownerGrokBin, 0o700))
	is(t, "")(nil, os.MkdirAll(filepath.Join(owner, ".local", "bin"), 0o700))
	is(t, "")(nil, os.MkdirAll(legGrokBin, 0o700))
	writeFile(t, filepath.Join(ownerGrokDownloads, "A"), "A")
	writeFile(t, filepath.Join(ownerGrokDownloads, "B"), "B")
	is(t, "")(nil, os.Symlink("../downloads/A", filepath.Join(ownerGrokBin, "grok")))
	is(t, "")(nil, os.Symlink("../../.grok/bin/grok", filepath.Join(owner, ".local", "bin", "grok")))
	writeFile(t, filepath.Join(legGrokDownloads, "A"), "A")
	writeFile(t, filepath.Join(legGrokDownloads, "B"), "B")
	is(t, "")(nil, os.Symlink("../downloads/A", filepath.Join(legGrokBin, "grok")))
	is(t, "")(nil, os.MkdirAll(filepath.Join(legHome, ".local", "bin"), 0o700))
	is(t, "")(nil, os.Symlink(filepath.Join(owner, ".local", "bin", "grok"), filepath.Join(legHome, ".local", "bin", "grok")))

	is(t, "")(nil, retargetLegBinLinks(owner, legHome))
	got, err := os.Readlink(filepath.Join(legHome, ".local", "bin", "grok"))
	if err != nil || got != filepath.Join(legHome, ".grok", "bin", "grok") {
		t.Fatalf("leg grok link = %q, %v; want clone bin/grok", got, err)
	}
	if data, err := os.ReadFile(filepath.Join(legHome, ".local", "bin", "grok")); err != nil || string(data) != "A" {
		t.Fatalf("initial leg grok = %q, %v; want A", data, err)
	}

	is(t, "")(nil, os.Remove(filepath.Join(legGrokBin, "grok")))
	is(t, "")(nil, os.Symlink("../downloads/B", filepath.Join(legGrokBin, "grok")))
	if data, err := os.ReadFile(filepath.Join(legHome, ".local", "bin", "grok")); err != nil || string(data) != "B" {
		t.Fatalf("relinked leg grok = %q, %v; want B", data, err)
	}
}

func TestLegZshStartupKeepsInstalledBinFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zsh startup is Unix-only")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	home, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
	t.Setenv("HOME", home)
	is(t, "")(nil, os.WriteFile(filepath.Join(home, "extra.zsh"), []byte("export OWNER_STARTUP=loaded\n"), 0o600))
	for _, file := range []string{".zprofile", ".zshrc"} {
		is(t, "")(nil, os.WriteFile(filepath.Join(home, file), []byte(". \"$ZDOTDIR/extra.zsh\"\nexport PATH=:/usr/bin::/bin:\nexport NPM_CONFIG_PREFIX=/owner/prefix\nexport npm_config_prefix=/owner/lower\nexport HOMEBREW_FORBIDDEN_TAPS=\nexport HOME=/owner/home\n"), 0o600))
	}
	env, err := prepareLegEnv([]string{"PATH=/usr/bin:/bin", "HOME=" + home}, leg, home)
	is(t, "")(nil, err)
	cmd := exec.Command(zsh, "-ilc", `printf '%s\n' "$PATH" "$ZDOTDIR" "$OWNER_STARTUP" "$NPM_CONFIG_PREFIX" "$npm_config_prefix" "$HOME" "$HOMEBREW_FORBIDDEN_TAPS"`)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	is(t, "")(nil, err)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 7 || !strings.HasPrefix(lines[0], filepath.Join(leg, "bin")+":") || strings.Contains(lines[0], "::") || lines[1] != filepath.Join(leg, "shell") || lines[2] != "loaded" || lines[3] != leg || lines[4] != leg || lines[5] != filepath.Join(leg, "home") || lines[6] != "homebrew/core" {
		t.Fatalf("shell startup: %s", out)
	}
}

func TestLegShellBashLoginKeepsInstallEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash startup is Unix-only")
	}
	bash, err := exec.LookPath("bash")
	is(t, "")(nil, err)
	for _, hasGrok := range []bool{false, true} {
		for _, hasProfile := range []bool{false, true} {
			t.Run(fmt.Sprintf("grok_%t_profile_%t", hasGrok, hasProfile), func(t *testing.T) {
				owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
				if hasGrok {
					is(t, "")(nil, os.MkdirAll(filepath.Join(owner, ".grok"), 0o700))
				}
				if hasProfile {
					for _, name := range legBashFiles {
						writeFile(t, filepath.Join(owner, name), "export PATH="+shellQuote(filepath.Join(owner, ".local/bin")+":"+filepath.Join(owner, ".kimi-code/bin")+":/usr/bin:/bin")+"\nexport HOME=/owner/home GOBIN=/owner/bin GROK_HOME=/wrong/grok NPM_CONFIG_PREFIX=/owner/prefix\n")
					}
				}
				env, err := prepareLegEnv([]string{"PATH=/usr/bin:/bin", "GROK_HOME=/inherited/grok"}, leg, owner)
				is(t, "")(nil, err)
				wantGrok := ""
				if hasGrok {
					wantGrok = filepath.Join(owner, ".grok")
				}
				if got := envValue(env, "GROK_HOME"); got != wantGrok {
					t.Fatalf("GROK_HOME = %q, want %q", got, wantGrok)
				}
				if got := envValue(env, "GOBIN"); got != filepath.Join(leg, "bin") {
					t.Fatalf("GOBIN = %q", got)
				}
				cmd := exec.Command(bash, "-lc", `printf '%s\n' "$PATH" "$HOME" "$GOBIN" "${GROK_HOME-unset}" "$NPM_CONFIG_PREFIX"`)
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				is(t, "")(nil, err)
				if !hasGrok {
					wantGrok = "unset"
				}
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				prefix := strings.Join([]string{filepath.Join(leg, "bin"), filepath.Join(leg, "home/.local/bin"), filepath.Join(leg, "home/.kimi-code/bin"), filepath.Join(leg, "home/.grok/bin"), filepath.Join(leg, "home/.opencode/bin")}, ":")
				if len(lines) != 5 || !strings.HasPrefix(lines[0], prefix+":") || lines[1] != filepath.Join(leg, "home") || lines[2] != filepath.Join(leg, "bin") || lines[3] != wantGrok || lines[4] != leg {
					t.Fatalf("bash login lost leg PATH or install env: %s", out)
				}
			})
		}
	}
}

func TestLegShellBashLoginKeepsProfileFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash startup is Unix-only")
	}
	bash, err := exec.LookPath("bash")
	is(t, "")(nil, err)
	for _, hasLogin := range []bool{false, true} {
		t.Run(fmt.Sprintf("bash_login_%t", hasLogin), func(t *testing.T) {
			owner, leg := t.TempDir(), filepath.Join(t.TempDir(), "head")
			writeFile(t, filepath.Join(owner, ".profile"), "export OWNER_STARTUP=profile\n")
			want := "profile"
			if hasLogin {
				writeFile(t, filepath.Join(owner, ".bash_login"), "export OWNER_STARTUP=login\n")
				want = "login"
			}
			env, err := prepareLegEnv([]string{"PATH=/usr/bin:/bin"}, leg, owner)
			is(t, "")(nil, err)
			cmd := exec.Command(bash, "-lc", `printf '%s\n' "$OWNER_STARTUP" "$GOBIN"`)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			is(t, "")(nil, err)
			if string(out) != want+"\n"+filepath.Join(leg, "bin")+"\n" {
				t.Fatalf("bash login fallback: %s", out)
			}
		})
	}
}
