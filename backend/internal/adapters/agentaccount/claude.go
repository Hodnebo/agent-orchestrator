package agentaccount

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// claudeShared are the parts of the computer's own Claude folder every account
// links to: conversation history, so `claude --resume` finds a session started
// under any account, and the user's own skills, plugins and commands. Anything
// else stays private to the account.
var claudeSharedDirs = []string{
	"projects", "sessions", "session-env", "file-history", "shell-snapshots", "todos",
	"paste-cache", "tasks", "plans", "transcripts",
	"skills", "plugins", "agents", "commands", "output-styles", "hooks",
}

// claudeSettingsDropped are settings.json keys that pick a login or a key, so an
// account's copy of the user's settings never carries them.
var claudeSettingsDropped = []string{"apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "forceLoginMethod", "forceLoginOrgUUID"}

// claudeCredentialEnv are variables that make Claude Code use something other
// than the folder's own login.
var claudeCredentialEnv = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

// Claude runs the `claude` CLI under an account folder (CLAUDE_CONFIG_DIR).
type Claude struct {
	// Binary resolves the claude executable.
	Binary func(context.Context) (string, error)
	// DeviceHome is the computer's own Claude folder (~/.claude).
	DeviceHome string
	// DeviceConfig is the computer's own global config (~/.claude.json).
	DeviceConfig string
}

var _ ports.AgentAccountProviderCLI = (*Claude)(nil)

// Provider names Claude.
func (*Claude) Provider() domain.AgentAccountProvider { return domain.AgentAccountProviderClaude }

// LaunchEnv points Claude Code at home. Claude keys the folder's Keychain login
// on the exact path text, so both variables carry the same string.
func (*Claude) LaunchEnv(home string) map[string]string {
	if home == "" {
		return nil
	}
	env := map[string]string{"CLAUDE_CONFIG_DIR": home, "CLAUDE_SECURESTORAGE_CONFIG_DIR": home}
	neutralize(env, claudeCredentialEnv...)
	return env
}

// LoginArgv is Claude Code's own sign-in.
func (c *Claude) LoginArgv(ctx context.Context) ([]string, error) {
	bin, err := c.Binary(ctx)
	if err != nil {
		return nil, err
	}
	return []string{bin, "auth", "login"}, nil
}

// Logout signs home out through Claude Code.
func (c *Claude) Logout(ctx context.Context, home string) error {
	bin, err := c.Binary(ctx)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, bin, home, nil, "auth", "logout")
	return err
}

// Identity asks `claude auth status --json` who home is signed in to.
func (c *Claude) Identity(ctx context.Context, home string) (domain.AgentAccountIdentityCheck, error) {
	bin, err := c.Binary(ctx)
	if err != nil {
		return domain.AgentAccountIdentityCheck{}, err
	}
	// The command exits 1 when signed out and still prints its report.
	out, runErr := c.run(ctx, bin, home, nil, "auth", "status", "--json")
	var report struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &report); err != nil {
		if runErr != nil {
			return domain.AgentAccountIdentityCheck{}, runErr
		}
		return domain.AgentAccountIdentityCheck{}, fmt.Errorf("read claude auth status: %w", err)
	}
	return domain.AgentAccountIdentityCheck{
		SignedIn: report.LoggedIn && report.AuthMethod == "claude.ai",
		Email:    report.Email,
		Plan:     report.SubscriptionType,
	}, nil
}

// Limits sends Claude Code's own `get_usage` control request over stream-json.
// The CLI reads the limits with its own login; AO only sees the answer.
func (c *Claude) Limits(ctx context.Context, home string) (domain.AgentAccountLimits, error) {
	bin, err := c.Binary(ctx)
	if err != nil {
		return domain.AgentAccountLimits{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request := `{"type":"control_request","request_id":"ao-usage","request":{"subtype":"get_usage","skip_behaviors":true}}` + "\n"
	cmd := c.command(ctx, bin, home, "--output-format", "stream-json", "--verbose", "--input-format", "stream-json")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return domain.AgentAccountLimits{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return domain.AgentAccountLimits{}, err
	}
	if err := cmd.Start(); err != nil {
		return domain.AgentAccountLimits{}, fmt.Errorf("start claude: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	if _, err := stdin.Write([]byte(request)); err != nil {
		return domain.AgentAccountLimits{}, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		var line struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Error     string          `json:"error"`
				Response  claudeUsageBody `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Type != "control_response" || line.Response.RequestID != "ao-usage" {
			continue
		}
		if line.Response.Subtype != "success" {
			return domain.AgentAccountLimits{}, fmt.Errorf("claude get_usage: %s", strings.TrimSpace(line.Response.Error))
		}
		return line.Response.Response.limits(time.Now().UTC()), nil
	}
	if err := ctx.Err(); err != nil {
		return domain.AgentAccountLimits{}, err
	}
	return domain.AgentAccountLimits{}, errors.New("claude ended without answering get_usage")
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudeUsageBody struct {
	RateLimits *struct {
		FiveHour    *claudeUsageWindow `json:"five_hour"`
		SevenDay    *claudeUsageWindow `json:"seven_day"`
		Opus        *claudeUsageWindow `json:"seven_day_opus"`
		Sonnet      *claudeUsageWindow `json:"seven_day_sonnet"`
		ModelScoped []struct {
			DisplayName string `json:"display_name"`
			claudeUsageWindow
		} `json:"model_scoped"`
		// Spend is paid usage beyond the plan, in minor units of its currency.
		Spend *struct {
			Enabled bool         `json:"enabled"`
			Used    *claudeMoney `json:"used"`
			Limit   *claudeMoney `json:"limit"`
		} `json:"spend"`
	} `json:"rate_limits"`
}

type claudeMoney struct {
	AmountMinor float64 `json:"amount_minor"`
	Currency    string  `json:"currency"`
	Exponent    int     `json:"exponent"`
}

func (m *claudeMoney) major() *float64 {
	if m == nil {
		return nil
	}
	v := m.AmountMinor / math.Pow10(m.Exponent)
	return &v
}

func (b claudeUsageBody) limits(now time.Time) domain.AgentAccountLimits {
	out := domain.AgentAccountLimits{Windows: []domain.AgentAccountLimitWindow{}, FetchedAt: now}
	r := b.RateLimits
	if r == nil {
		return out
	}
	add := func(id, label string, w *claudeUsageWindow) {
		if w == nil || w.Utilization == nil {
			return
		}
		window := domain.AgentAccountLimitWindow{ID: id, Label: label, UsedPercent: min(max(*w.Utilization, 0), 100)}
		if w.ResetsAt != nil {
			if at, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
				window.ResetsAt = &at
			}
		}
		out.Windows = append(out.Windows, window)
	}
	add("five_hour", "Session (5h)", r.FiveHour)
	add("seven_day", "Weekly", r.SevenDay)
	add("seven_day_opus", "Weekly · Opus", r.Opus)
	add("seven_day_sonnet", "Weekly · Sonnet", r.Sonnet)
	for _, scoped := range r.ModelScoped {
		w := scoped.claudeUsageWindow
		add("model:"+scoped.DisplayName, "Weekly · "+scoped.DisplayName, &w)
	}
	if e := r.Spend; e != nil {
		out.ExtraUsage = &domain.AgentAccountExtraUsage{Enabled: e.Enabled, UsedCredits: e.Used.major(), MonthlyLimit: e.Limit.major()}
		if e.Used != nil {
			out.ExtraUsage.Currency = e.Used.Currency
		}
	}
	return out
}

// Prepare links home's history and the user's own config to the computer's
// Claude folder, and copies the user's settings and MCP servers into it.
func (c *Claude) Prepare(home string) error {
	if home == "" {
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	var errs []error
	for _, dir := range claudeSharedDirs {
		errs = append(errs, linkShared(home, c.DeviceHome, dir, false, false))
	}
	errs = append(errs, linkShared(home, c.DeviceHome, "history.jsonl", true, false))
	// CLAUDE.md and the files it imports with @NAME.md.
	if matches, err := filepath.Glob(filepath.Join(c.DeviceHome, "*.md")); err == nil {
		for _, match := range matches {
			errs = append(errs, linkShared(home, c.DeviceHome, filepath.Base(match), true, false))
		}
	}
	errs = append(errs, c.copySettings(home), c.mergeGlobalConfig(home))
	return errors.Join(errs...)
}

// copySettings writes the user's settings.json into home without the keys that
// would pick another login.
func (c *Claude) copySettings(home string) error {
	data, err := os.ReadFile(filepath.Join(c.DeviceHome, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("read %s: %w", filepath.Join(c.DeviceHome, "settings.json"), err)
	}
	for _, key := range claudeSettingsDropped {
		delete(settings, key)
	}
	if env, ok := settings["env"].(map[string]any); ok {
		for _, key := range claudeCredentialEnv {
			delete(env, key)
		}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeIfChanged(filepath.Join(home, "settings.json"), append(out, '\n'))
}

// mergeGlobalConfig carries the user's MCP servers and theme into home's
// .claude.json, the file that also holds the account's own sign-in, so only
// those keys are touched.
func (c *Claude) mergeGlobalConfig(home string) error {
	var device map[string]json.RawMessage
	if data, err := os.ReadFile(c.DeviceConfig); err == nil {
		_ = json.Unmarshal(data, &device)
	}
	path := filepath.Join(home, ".claude.json")
	account := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		// A corrupt file is Claude's to repair; never overwrite it.
		if err := json.Unmarshal(data, &account); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed := false
	for _, key := range []string{"mcpServers", "theme"} {
		if value, ok := device[key]; ok && string(account[key]) != string(value) {
			account[key], changed = value, true
		}
	}
	if _, ok := account["hasCompletedOnboarding"]; !ok {
		account["hasCompletedOnboarding"], changed = json.RawMessage("true"), true
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(account, "", "  ")
	if err != nil {
		return err
	}
	return writeIfChanged(path, append(out, '\n'))
}

func (c *Claude) command(ctx context.Context, bin, home string, args ...string) *exec.Cmd {
	cmd := aoprocess.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ()
	for key, value := range c.LaunchEnv(home) {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	// A throwaway cwd keeps a project's own Claude settings out of the probe.
	cmd.Dir = os.TempDir()
	return cmd
}

func (c *Claude) run(ctx context.Context, bin, home string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := c.command(ctx, bin, home, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.Output()
}
