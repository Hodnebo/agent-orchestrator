package agentaccount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func linksTo(t *testing.T, link, target string) {
	t.Helper()
	got, err := os.Readlink(link)
	if err != nil || got != target {
		t.Fatalf("%s -> %q (%v), want %q", link, got, err, target)
	}
}

func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists, want it private to the device", path)
	}
}

// TestClaudePrepareSharesHistoryNotLogin pins what makes a native resume work
// across accounts without one account's login leaking into another: history is
// a link, the settings copy loses login-picking keys, and the global config
// carries MCP servers but never the device's own account.
func TestClaudePrepareSharesHistoryNotLogin(t *testing.T) {
	device, home := t.TempDir(), filepath.Join(t.TempDir(), "acct")
	write(t, filepath.Join(device, "projects", "-repo", "s1.jsonl"), "{}\n")
	write(t, filepath.Join(device, "CLAUDE.md"), "rules\n")
	write(t, filepath.Join(device, "settings.json"), `{"model":"opus","apiKeyHelper":"x","env":{"ANTHROPIC_API_KEY":"k","FOO":"1"}}`)
	write(t, filepath.Join(device, ".credentials.json"), `{"secret":true}`)
	config := filepath.Join(t.TempDir(), ".claude.json")
	write(t, config, `{"mcpServers":{"m":{"command":"x"}},"theme":"light","oauthAccount":{"emailAddress":"a@example.com"}}`)
	// The CLI made an empty folder before the link did.
	if err := os.MkdirAll(filepath.Join(home, "todos"), 0o700); err != nil {
		t.Fatal(err)
	}

	c := &Claude{DeviceHome: device, DeviceConfig: config}
	for range 2 { // idempotent: every launch runs it
		if err := c.Prepare(home); err != nil {
			t.Fatal(err)
		}
	}
	linksTo(t, filepath.Join(home, "projects"), filepath.Join(device, "projects"))
	linksTo(t, filepath.Join(home, "todos"), filepath.Join(device, "todos"))
	linksTo(t, filepath.Join(home, "CLAUDE.md"), filepath.Join(device, "CLAUDE.md"))
	linksTo(t, filepath.Join(home, "history.jsonl"), filepath.Join(device, "history.jsonl"))
	if _, err := os.Stat(filepath.Join(home, "projects", "-repo", "s1.jsonl")); err != nil {
		t.Fatalf("device transcript not visible: %v", err)
	}
	absent(t, filepath.Join(home, ".credentials.json"))

	var settings map[string]any
	data, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	env, _ := settings["env"].(map[string]any)
	if settings["model"] != "opus" || settings["apiKeyHelper"] != nil || env["ANTHROPIC_API_KEY"] != nil || env["FOO"] != "1" {
		t.Fatalf("settings copy = %s", data)
	}
	var global map[string]any
	data, _ = os.ReadFile(filepath.Join(home, ".claude.json"))
	if err := json.Unmarshal(data, &global); err != nil {
		t.Fatal(err)
	}
	if global["mcpServers"] == nil || global["theme"] != "light" || global["oauthAccount"] != nil || global["hasCompletedOnboarding"] != true {
		t.Fatalf("global config = %s", data)
	}
}

// TestCodexPrepareKeepsLoginAndDaemonPrivate pins the shadow home: everything
// is shared except the login and the app-server daemon's control files, so a
// terminal on one account cannot attach to a daemon running as another.
func TestCodexPrepareKeepsLoginAndDaemonPrivate(t *testing.T) {
	device, home := t.TempDir(), filepath.Join(t.TempDir(), "acct")
	write(t, filepath.Join(device, "auth.json"), `{"secret":true}`)
	write(t, filepath.Join(device, "config.toml"), "model = \"gpt-5\"\n")
	write(t, filepath.Join(device, "sessions", "2026", "r.jsonl"), "{}\n")
	write(t, filepath.Join(device, "app-server-daemon", "pid"), "1")
	write(t, filepath.Join(device, "state_5.sqlite"), "")
	// Codex made its own lock folder in the fresh home first.
	write(t, filepath.Join(home, "mcp-oauth-locks", "l"), "")

	c := &Codex{DeviceHome: device}
	for range 2 {
		if err := c.Prepare(home); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config.toml", "sessions", "state_5.sqlite", "thread-writer-locks", "mcp-oauth-locks", "archived_sessions"} {
		linksTo(t, filepath.Join(home, name), filepath.Join(device, name))
	}
	absent(t, filepath.Join(home, "auth.json"))
	absent(t, filepath.Join(home, "app-server-daemon"))
}

// TestLaunchEnvOverridesCredentialEnv pins the right-account guarantee: a key
// in the daemon's environment cannot outrank the account's own login.
func TestLaunchEnvOverridesCredentialEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "device-key")
	t.Setenv("OPENAI_API_KEY", "device-key")
	claude := (&Claude{}).LaunchEnv("/acct")
	if claude["CLAUDE_CONFIG_DIR"] != "/acct" || claude["CLAUDE_SECURESTORAGE_CONFIG_DIR"] != "/acct" {
		t.Fatalf("claude env = %v", claude)
	}
	if value, set := claude["ANTHROPIC_API_KEY"]; !set || value != "" {
		t.Fatalf("ANTHROPIC_API_KEY not cleared: %v", claude)
	}
	if _, set := claude["CLAUDE_CODE_OAUTH_TOKEN"]; set {
		t.Fatalf("unset variable added: %v", claude)
	}
	codex := (&Codex{}).LaunchEnv("/acct")
	if codex["CODEX_HOME"] != "/acct" || codex["OPENAI_API_KEY"] != "" {
		t.Fatalf("codex env = %v", codex)
	}
	if (&Claude{}).LaunchEnv("") != nil || (&Codex{}).LaunchEnv("") != nil {
		t.Fatal("the device account must launch with the environment untouched")
	}
}
