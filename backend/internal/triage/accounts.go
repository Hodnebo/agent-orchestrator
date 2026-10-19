package triage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/modelcatalog"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agentaccount"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

// Native discovery uses local configuration parsers, never AuthStatus or model
// list commands. Some adapters treat installation alone as authentication.
func nativeConfiguredChoices(ctx context.Context) []Choice {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	choices := localConfiguredChoices(ctx, os.Getenv, home, true)
	return append(choices, additionalConfiguredChoices(ctx, os.Getenv, home)...)
}

func additionalConfiguredChoices(ctx context.Context, getenv func(string) string, home string) []Choice {
	env := map[string]string{"HOME": home}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != "HOME" {
			env[key] = getenv(key)
		}
	}
	var choices []Choice
	for _, item := range agentregistry.Harnessed() {
		if ctx.Err() != nil {
			break
		}
		harness := string(item.Harness)
		if harness == "claude-code" || harness == "codex" {
			continue
		}
		catalog := modelcatalog.LocalConfigured(ctx, ports.AgentModelDiscoveryRequest{AgentID: harness, Env: env})
		choice := Choice{Harness: harness}
		for _, model := range catalog.Models {
			choice.Models = append(choice.Models, ModelChoice{ID: model.ID, Label: model.Label, Default: model.IsDefault, Efforts: model.Efforts})
		}
		if len(choice.Models) != 0 {
			choices = append(choices, choice)
		}
	}
	return choices
}

func localConfiguredChoices(ctx context.Context, getenv func(string) string, home string, allowKeychain bool) []Choice {
	var choices []Choice
	add := func(harness string, provider domain.AgentAccountProvider) {
		choices = append(choices, Choice{
			Harness: harness, Models: []ModelChoice{{ID: "", Label: "Configured default", Default: true}},
			Accounts: []Account{{ID: domain.DeviceAgentAccountID(provider), Label: "This computer", Default: true}},
		})
	}
	codexHome := strings.TrimSpace(getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	configuredCodex := false
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
		configuredCodex = configuredCodex || strings.TrimSpace(getenv(key)) != ""
	}
	if configuredCodex || codexCredentialPresent(filepath.Join(codexHome, "auth.json"), false) {
		add(string(domain.HarnessCodex), domain.AgentAccountProviderCodex)
	}
	if ctx.Err() != nil {
		return choices
	}
	claudeHome := strings.TrimSpace(getenv("CLAUDE_CONFIG_DIR"))
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	opts := (agentcreds.ResolveOptions{Env: getenv, ConfigDir: claudeHome, AllowKeychain: allowKeychain}).WithClaudeSettings(probeCtx)
	if provider, ok := agentcreds.ResolveProvider("", opts); ok {
		if _, configured := agentcreds.ResolveLocal(probeCtx, provider, opts); configured {
			add(string(domain.HarnessClaudeCode), domain.AgentAccountProviderClaude)
		}
	}
	return choices
}

func validConfiguredAccount(harness, id string) bool {
	provider, ok := domain.AgentAccountProviderFor(domain.AgentHarness(harness))
	if !ok {
		return false
	}
	if id == domain.DeviceAgentAccountID(provider) {
		return true
	}
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id && parsed.Version() == 4
}

func configuredAccountEnv(ctx context.Context, dataDir, harness, id string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validConfiguredAccount(harness, id) {
		return nil, errors.New("the selected saved account is unavailable")
	}
	provider, _ := domain.AgentAccountProviderFor(domain.AgentHarness(harness))
	if id == domain.DeviceAgentAccountID(provider) {
		return map[string]string{}, nil
	}
	// The original accounts are SQLite-backed metadata around fixed UUID
	// folders. The snapshot supplies the explicit id; triage neither reads that
	// database nor recreates a missing profile from the native login.
	legacy := filepath.Join(dataDir, "agent-accounts", string(provider), id)
	home := legacy
	if provider == domain.AgentAccountProviderCodex {
		managed := filepath.Join(dataDir, "harnesses", "codex", "accounts", id)
		_, legacyErr := os.Lstat(legacy)
		_, managedErr := os.Lstat(managed)
		if legacyErr == nil && managedErr == nil {
			return nil, errors.New("the saved Codex account has conflicting profiles; choose it again in AO")
		}
		if managedErr == nil {
			if !safeAccountDirectory(dataDir, "harnesses", "codex", "accounts", id, "credential-home") || !validManagedCodexDescriptor(filepath.Join(managed, "account.json"), id) {
				return nil, errors.New("the saved Codex account profile is unavailable or unsafe")
			}
			home = filepath.Join(managed, "credential-home")
		} else if !safeAccountDirectory(dataDir, "agent-accounts", string(provider), id) {
			return nil, errors.New("the saved Codex account profile is unavailable; select or sign in to it in AO")
		}
		if !codexCredentialPresent(filepath.Join(home, "auth.json"), true) {
			return nil, errors.New("the saved Codex account credential is unavailable; sign in to that account in AO")
		}
		return (&agentaccount.Codex{}).LaunchEnv(home), nil
	}
	if !safeAccountDirectory(dataDir, "agent-accounts", string(provider), id) {
		return nil, errors.New("the saved Claude account profile is unavailable; select or sign in to it in AO")
	}
	// The shared local resolver's keychain source is the native login, not a
	// saved profile's path-specific keychain item. Never substitute it here.
	if !privateCredentialFile(filepath.Join(home, ".credentials.json")) {
		return nil, errors.New("the saved Claude account cannot be verified without its profile credential; select or sign in to it in AO")
	}
	_, configured := agentcreds.ResolveLocal(ctx, agentcreds.ProviderFirstParty, agentcreds.ResolveOptions{
		ConfigDir: home, Env: func(string) string { return "" },
	})
	if !configured {
		return nil, errors.New("the saved Claude account credential is unavailable; sign in to that account in AO")
	}
	return (&agentaccount.Claude{}).LaunchEnv(home), nil
}

func safeAccountDirectory(root string, parts ...string) bool {
	path := root
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			path = filepath.Join(path, parts[i])
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return false
		}
	}
	return true
}

func privateCredentialFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 && info.Size() > 0 && info.Size() <= 1<<20
}

func codexCredentialPresent(path string, private bool) bool {
	if private && !privateCredentialFile(path) {
		return false
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	return readSmallJSON(path, 1<<20, &auth) == nil && (strings.TrimSpace(auth.APIKey) != "" || strings.TrimSpace(auth.Tokens.AccessToken) != "")
}

func validManagedCodexDescriptor(path, id string) bool {
	if !privateCredentialFile(path) {
		return false
	}
	var descriptor struct {
		Version    int       `json:"version"`
		ID         string    `json:"id"`
		Source     string    `json:"source"`
		AuthMethod string    `json:"authMethod"`
		CreatedAt  time.Time `json:"createdAt"`
		VerifiedAt time.Time `json:"verifiedAt"`
	}
	if readSmallJSON(path, 16<<10, &descriptor) != nil || descriptor.ID != id || descriptor.Source != "managed" || (descriptor.Version != 1 && descriptor.Version != 2) || descriptor.CreatedAt.IsZero() || descriptor.VerifiedAt.IsZero() {
		return false
	}
	switch descriptor.AuthMethod {
	case "chatgpt", "api_key", "other", "unknown":
		return true
	default:
		return false
	}
}

func readSmallJSON(path string, limit int64, target any) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return errors.New("configuration file is unavailable")
	}
	file, err := os.Open(path) //nolint:gosec // Only fixed native credential files and validated UUID profiles reach this helper.
	if err != nil {
		return errors.New("configuration file is unavailable")
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	if decoder.Decode(target) != nil {
		return errors.New("configuration file is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("configuration file is invalid")
	}
	return nil
}
