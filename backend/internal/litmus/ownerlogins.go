package litmus

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/nativeconfig"
	"gopkg.in/yaml.v3"
)

const (
	ownerLoginsFile    = "owner-logins.json"
	ownerLoginsVersion = 3
	ownerLoginChanged  = "owner login changed: "
)

// Each value is a digest of account identifiers only. Credential bytes and
// their modification times do not belong in the baseline.
type ownerLogins struct {
	Version          int
	CodexIdentity    string
	ClaudeIdentity   string
	GitHubIdentity   string
	OpenCodeIdentity string
}

func readOwnerLogins(ctx context.Context) ownerLogins {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ownerLogins{Version: ownerLoginsVersion}
	}
	return readOwnerLoginsAt(ctx, home, opencodeAuthFile(home))
}

func readOwnerLoginsAt(ctx context.Context, home, opencodeAuthPath string) ownerLogins {
	codexHome, err := nativeconfig.Resolve(map[string]string{"HOME": home}, "CODEX_HOME", ".codex")
	if err != nil {
		codexHome = filepath.Join(home, ".codex")
	}
	return ownerLogins{
		Version:          ownerLoginsVersion,
		CodexIdentity:    codexIdentity(ctx, filepath.Join(codexHome, "auth.json")),
		ClaudeIdentity:   claudeIdentity(claudeConfigFile(home)),
		GitHubIdentity:   githubIdentity(ghHostsFile(home)),
		OpenCodeIdentity: openCodeIdentity(opencodeAuthPath),
	}
}

func opencodeAuthFile(home string) string {
	if dataDir := strings.TrimSpace(os.Getenv("OPENCODE_DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "auth.json")
	}
	if dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dataHome != "" {
		return filepath.Join(dataHome, "opencode", "auth.json")
	}
	return filepath.Join(home, ".local", "share", "opencode", "auth.json")
}

func claudeConfigFile(home string) string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

func ghHostsFile(home string) string {
	if dir := strings.TrimSpace(os.Getenv("GH_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, "hosts.yml")
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "gh", "hosts.yml")
	}
	return filepath.Join(home, ".config", "gh", "hosts.yml")
}

func identityFingerprint(identity any) string {
	data, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func codexIdentity(ctx context.Context, path string) string {
	if ctx.Err() != nil {
		return ""
	}
	data, err := os.ReadFile(path) //nolint:gosec // Codex's own auth file
	if err != nil {
		return ""
	}
	var auth struct {
		Email  string `json:"email"`
		Tokens struct {
			AccountID string `json:"account_id"`
			Email     string `json:"email"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil {
		return ""
	}
	id := strings.TrimSpace(auth.Tokens.AccountID)
	if id != "" {
		return identityFingerprint("id:" + id)
	}
	email := strings.TrimSpace(auth.Tokens.Email)
	if email == "" {
		email = strings.TrimSpace(auth.Email)
	}
	if email != "" {
		return identityFingerprint("email:" + strings.ToLower(email))
	}
	return ""
}

func claudeIdentity(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // Claude's own config file
	if err != nil {
		return ""
	}
	var config struct {
		OAuthAccount struct {
			ID    string `json:"accountUuid"`
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(data, &config) != nil {
		return ""
	}
	id := strings.TrimSpace(config.OAuthAccount.ID)
	if id != "" {
		return identityFingerprint("id:" + id)
	}
	if email := strings.TrimSpace(config.OAuthAccount.Email); email != "" {
		return identityFingerprint("email:" + strings.ToLower(email))
	}
	return ""
}

func githubIdentity(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // GitHub CLI's own config file
	if err != nil {
		return ""
	}
	var hosts map[string]struct {
		User string `yaml:"user"`
	}
	if yaml.Unmarshal(data, &hosts) != nil {
		return ""
	}
	active := make([]string, 0, len(hosts))
	for host, entry := range hosts {
		if user := strings.TrimSpace(entry.User); user != "" {
			active = append(active, strings.ToLower(strings.TrimSpace(host))+"\x00"+strings.ToLower(user))
		}
	}
	if len(active) == 0 {
		return ""
	}
	sort.Strings(active)
	return identityFingerprint(active)
}

func openCodeIdentity(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // OpenCode's own auth file
	if err != nil {
		return ""
	}
	var providers map[string]json.RawMessage
	if json.Unmarshal(data, &providers) != nil {
		return ""
	}
	active := make([]string, 0, len(providers))
	for provider, raw := range providers {
		provider = strings.TrimSpace(provider)
		if provider == "" || strings.HasPrefix(strings.ToLower(provider), "mcp:") {
			continue
		}
		var account struct {
			Type      string `json:"type"`
			AccountID string `json:"accountId"`
			Metadata  struct {
				AccountID string `json:"accountId"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &account) != nil {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(account.Type))
		if kind == "" {
			continue
		}
		id := strings.TrimSpace(account.AccountID)
		if id == "" {
			id = strings.TrimSpace(account.Metadata.AccountID)
		}
		active = append(active, provider+"\x00"+kind+"\x00"+id)
	}
	if len(active) == 0 {
		return ""
	}
	sort.Strings(active)
	return identityFingerprint(active)
}

func ownerLoginChangeReasons(before, after ownerLogins) []string {
	// Earlier baselines contained credential hashes. They cannot be compared
	// with account identifiers without treating a token refresh as a switch.
	if before.Version < ownerLoginsVersion {
		return nil
	}
	var changed []string
	add := func(which string) {
		changed = append(changed, ownerLoginChanged+which)
	}
	if before.CodexIdentity != after.CodexIdentity {
		add("Codex account")
	}
	if before.ClaudeIdentity != after.ClaudeIdentity {
		add("Claude account")
	}
	if before.GitHubIdentity != after.GitHubIdentity {
		add("GitHub account")
	}
	if before.OpenCodeIdentity != after.OpenCodeIdentity {
		add("OpenCode account")
	}
	return changed
}

const ownerLoginWarningPrefix = "Owner login changed during this run: "

// warnOwnerLogin records identity changes without changing the run or its verdict.
func (s *Service) warnOwnerLogin(ctx context.Context, sessionID string) string {
	id, err := runID(sessionID)
	if err != nil {
		return ""
	}
	run, err := s.store.Get(id)
	if err != nil {
		return ""
	}
	var before ownerLogins
	if err := s.store.read(id, ownerLoginsFile, &before); err != nil {
		return ""
	}
	changes := ownerLoginChangeReasons(before, readOwnerLogins(context.WithoutCancel(ctx)))
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.store.root, id, "owner-login-warning.txt")
	previous, _ := os.ReadFile(path)
	which := strings.TrimPrefix(strings.TrimSpace(string(previous)), ownerLoginWarningPrefix)
	var names []string
	if which != "" {
		names = strings.Split(which, ", ")
	}
	for _, change := range changes {
		name := strings.TrimPrefix(change, ownerLoginChanged)
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	warning := ownerLoginWarningPrefix + strings.Join(names, ", ")
	if warning == strings.TrimSpace(string(previous)) {
		return warning
	}
	if err := s.store.WriteFile(id, "owner-login-warning.txt", []byte(warning+"\n")); err != nil {
		slog.Warn("litmus: could not save login warning", "run", id, "error", err)
	}
	slog.Warn(warning, "run", run.ID)
	for _, file := range []string{"verdict.md", "comment.md", "run.log"} {
		body, _ := os.ReadFile(filepath.Join(s.store.root, id, file))
		var lines []string
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.HasPrefix(line, ownerLoginWarningPrefix) {
				lines = append(lines, line)
			}
		}
		text := strings.TrimRight(strings.Join(lines, "\n"), "\n")
		if text != "" {
			text += "\n\n"
		}
		if err := s.store.WriteFile(id, file, []byte(text+warning+"\n")); err != nil {
			slog.Warn("litmus: could not write login warning", "run", id, "file", file, "error", err)
		}
	}
	return warning
}

func (s *Service) ownerLoginStatus(ctx context.Context, run Run) string {
	var before ownerLogins
	if err := s.store.read(run.ID, ownerLoginsFile, &before); err != nil {
		if os.IsNotExist(err) {
			return "" // older runs did not record a baseline
		}
		return "owner logins: unverified"
	}
	if before.Version < ownerLoginsVersion {
		return "owner logins: unverified"
	}
	changes := ownerLoginChangeReasons(before, readOwnerLogins(ctx))
	if len(changes) > 0 {
		for i := range changes {
			changes[i] = strings.TrimPrefix(changes[i], ownerLoginChanged)
		}
		return "CHANGED: " + strings.Join(changes, ", ")
	}
	return "owner logins: unchanged"
}
