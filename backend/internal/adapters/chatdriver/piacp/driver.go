// Package piacp binds AO's Pi harness to a user-installed pi-acp adapter.
//
// AO does not package or download pi-acp. The adapter embeds Pi itself, while
// the existing Pi plugin remains the canonical auth probe. Both processes read
// the same PI_CODING_AGENT_DIR, provider credentials, and project resources.
package piacp

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	distributionName = "@victor-software-house/pi-acp"
	minimumVersion   = "0.17.1"
)

type piPlugin interface {
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

type binaryResolver func(context.Context) (string, error)

// New constructs Pi's Chat-only driver. Pi intentionally does not implement
// AgentInterfaceHandoff: pi-acp's durable id is not yet proven identical to the
// terminal adapter's continuation id across both implementations.
func New(plugin piPlugin, log *slog.Logger) ports.ChatDriver {
	return acpdriver.New(buildConfig(plugin, ResolveBinary, log), log)
}

func buildConfig(plugin piPlugin, resolve binaryResolver, log *slog.Logger) acpdriver.Config {
	totals := &sessionTotals{}
	return acpdriver.Config{
		Harness: domain.HarnessPi,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityTools:     true,
			// pi-acp does not request ACP permissions and Pi executes tools without
			// permission popups. Keep approvals false so AO's production floor
			// refuses mutating Chat admission instead of presenting a permission
			// selector whose value the provider would silently ignore.
			ports.ChatCapabilityApprovals: false,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    true,
			ports.ChatCapabilityUsage:     true,
			ports.ChatCapabilityDiffs:     true,
		},
		// pi-acp replays a user message as one chunk with no id, and a reply that
		// failed as nothing, so two prompts in a row must not merge into one.
		HistoryUserChunkIsMessage: true,
		InstallCheck: func(ctx context.Context) error {
			if _, err := resolve(ctx); err != nil {
				return fmt.Errorf("pi-acp is not installed; install it with npm i -g %s", distributionName)
			}
			return nil
		},
		Probe: func(ctx context.Context) error {
			if _, err := resolve(ctx); err != nil {
				return fmt.Errorf("%w: pi-acp is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			status, err := plugin.AuthStatus(ctx)
			if err == nil && status == ports.AgentAuthStatusUnauthorized {
				return ports.ErrChatAuthRequired
			}
			if err != nil && log != nil {
				log.Debug("Pi auth probe inconclusive; continuing", "error", err)
			}
			return nil
		},
		Launch: func(ctx context.Context, cfg acpdriver.LaunchConfig) (acpdriver.Launch, error) {
			binary, err := resolve(ctx)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: pi-acp is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			if !filepath.IsAbs(cfg.DataDir) {
				return acpdriver.Launch{}, fmt.Errorf("pi-acp requires an absolute AO data directory, got %q", cfg.DataDir)
			}
			env := cloneEnv(cfg.Env)
			// pi-acp 0.17 uses a long-running daemon. A session-private socket
			// prevents one project's first-start environment (notably
			// PI_CODING_AGENT_DIR and provider keys) leaking into another.
			env["PI_ACP_SOCKET_DIR"] = filepath.Join(cfg.DataDir, "pi-acp", string(cfg.SessionID), "run")
			if err := prepareStandingInstructions(ctx, cfg); err != nil {
				return acpdriver.Launch{}, err
			}
			if err := preloadCurrentModels(cfg, env); err != nil && log != nil {
				log.Warn("Pi chat keeps pi-acp's own model list", "session", cfg.SessionID, "error", err)
			}
			return acpdriver.Launch{Command: binary, Env: env}, nil
		},
		ValidateInitialize: validateInitialize,
		SessionMeta:        sessionMeta,
		SessionOptions:     sessionOptions,
		SessionPromptResponseFailure: func(sessionID string, response acpsdk.PromptResponse) error {
			return promptResponseFailure(response, totals.swap(sessionID, response), recordedModelError(piAgentDir(), sessionID))
		},
		// pi-acp can exit during its first handshake while its background
		// daemon starts; the next start finds the daemon up.
		RetryStartupExit: true,
	}
}

// sessionTotals is each Pi session's token total as of its last turn. pi-acp
// reports a session's totals, not a turn's, so a turn's own use is the change.
type sessionTotals struct {
	mu     sync.Mutex
	totals map[string]int
}

// swap records response's total for sessionID and returns the one before it.
func (s *sessionTotals) swap(sessionID string, response acpsdk.PromptResponse) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.totals[sessionID]
	if response.Usage != nil {
		if s.totals == nil {
			s.totals = map[string]int{}
		}
		s.totals[sessionID] = response.Usage.TotalTokens
	}
	return previous
}

// promptResponseFailure reports a turn Pi ended without an answer from its
// model. pi-acp answers such a prompt with end_turn, no message and no tokens
// used, so the chat would show the turn as done with nothing in it. previous is
// the session's token total before the turn. Pi keeps the model's error only
// in its own session record; recorded is that error, if any.
func promptResponseFailure(response acpsdk.PromptResponse, previous int, recorded string) error {
	usage := response.Usage
	if response.StopReason != acpsdk.StopReasonEndTurn || usage == nil || usage.TotalTokens != previous {
		return nil
	}
	if recorded == "" || strings.Contains(recorded, "No API key") {
		return ports.NewChatProviderFailure("Pi did not reach its model",
			"Pi ended the turn without calling its model, usually because its sign-in has expired. Run pi in a terminal and log in again.", nil)
	}
	return ports.NewChatProviderFailure("Pi's model refused the turn", recorded, nil)
}

// piAgentDir is where Pi keeps its settings and sessions.
func piAgentDir() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// recordedModelError reads the error of the latest failed reply in Pi's record
// of sessionID (sessions/<folder>/<time>_<id>.jsonl), with a provider's JSON
// error body reduced to its message. It reads only the end of the record.
func recordedModelError(agentDir, sessionID string) string {
	if agentDir == "" || sessionID == "" || strings.ContainsAny(sessionID, `/\*?[`) {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(agentDir, "sessions", "*", "*_"+sessionID+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	file, err := os.Open(matches[0])
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	const tail = 256 << 10
	if info, err := file.Stat(); err == nil && info.Size() > tail {
		_, _ = file.Seek(info.Size()-tail, io.SeekStart)
	}
	raw, _ := io.ReadAll(file)
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Message struct {
				Role         string `json:"role"`
				StopReason   string `json:"stopReason"`
				ErrorMessage string `json:"errorMessage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &entry) != nil || entry.Message.Role != "assistant" {
			continue
		}
		if entry.Message.StopReason != "error" {
			return ""
		}
		return readableModelError(entry.Message.ErrorMessage)
	}
	return ""
}

// readableModelError turns `Codex error: {"error":{"message":"..."}}` into
// its message, and caps any error at a few hundred characters.
func readableModelError(text string) string {
	text = strings.TrimSpace(text)
	if start := strings.Index(text, "{"); start >= 0 {
		var body struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(text[start:]), &body) == nil {
			if message := strings.TrimSpace(cmp.Or(body.Error.Message, body.Message)); message != "" {
				text = message
			}
		}
	}
	if len(text) > 400 {
		text = text[:399] + "…"
	}
	return text
}

func cloneEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

// modelsPreload is loaded into pi-acp to give its bundled Pi the models a newer
// Pi lists now. See the file for how.
//
//go:embed models_preload.mjs
var modelsPreload []byte

// preloadCurrentModels has pi-acp, and the daemon it starts, load
// modelsPreload. pi-acp pins a Pi release whose built-in models providers have
// since retired: a ChatGPT account refuses all of them. Without the preload
// pi-acp keeps that list.
func preloadCurrentModels(cfg acpdriver.LaunchConfig, env map[string]string) error {
	dir := filepath.Join(cfg.DataDir, "pi-acp", string(cfg.SessionID))
	script := filepath.Join(dir, "models-preload.mjs")
	// BUN_OPTIONS splits on whitespace and takes no quotes.
	if strings.ContainsAny(script, " \t\n") {
		return fmt.Errorf("the preload path %q has whitespace, which BUN_OPTIONS cannot carry", script)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(script, modelsPreload, 0o600); err != nil {
		return err
	}
	env["BUN_OPTIONS"] = strings.TrimSpace(env["BUN_OPTIONS"] + " --preload " + script)
	env["AO_PI_MODELS_JSON"] = filepath.Join(dir, "models.json")
	return nil
}

func instructionDir(cfg acpdriver.LaunchConfig) string {
	return filepath.Join(cfg.DataDir, "prompts", string(cfg.SessionID), "pi-acp")
}

func prepareStandingInstructions(ctx context.Context, cfg acpdriver.LaunchConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		return nil
	}
	dir := instructionDir(cfg)
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o700); err != nil {
		return fmt.Errorf("prepare Pi ACP instructions: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	content := strings.TrimRight(cfg.SystemPrompt, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o600); err != nil {
		return fmt.Errorf("write Pi ACP instructions: %w", err)
	}
	return nil
}

func sessionMeta(cfg acpdriver.LaunchConfig) map[string]any {
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		return nil
	}
	// An inline resource manifest composes the ordinary project/user Pi
	// resources with AO's standing instructions. The first root preserves the
	// exact project AGENTS.md, skills, prompts, extensions, and agent directory
	// Pi's TUI sees; the second adds only AO's generated AGENTS.md.
	manifest := map[string]any{
		"version":       1,
		"mode":          "local",
		"mergeStrategy": "append",
		"roots": []any{
			map[string]any{"id": "project", "kind": "local", "paths": map[string]any{"cwd": cfg.WorkspacePath}},
			map[string]any{"id": "ao", "kind": "local", "paths": map[string]any{
				"cwd": instructionDir(cfg), "agentDir": filepath.Join(instructionDir(cfg), "agent"),
			}},
		},
	}
	return map[string]any{"piAcp": map[string]any{"manifest": manifest}}
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	if settings.Effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "thought_level", Value: settings.Effort})
	}
	return options
}

var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$`)

func validateInitialize(init acpsdk.InitializeResponse) error {
	if init.AgentInfo == nil || init.AgentInfo.Name != distributionName {
		got := "missing agentInfo"
		if init.AgentInfo != nil {
			got = init.AgentInfo.Name
		}
		return fmt.Errorf("unsupported Pi ACP distribution %q; install %s", got, distributionName)
	}
	installed, ok := parseVersion(init.AgentInfo.Version)
	minimum, _ := parseVersion(minimumVersion)
	if !ok {
		return fmt.Errorf("unrecognized %s version %q", distributionName, init.AgentInfo.Version)
	}
	if installed.less(minimum) {
		return fmt.Errorf("%s %s is older than AO's tested minimum %s", distributionName, init.AgentInfo.Version, minimumVersion)
	}
	return nil
}

type version [3]int

func parseVersion(value string) (version, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 4 {
		return version{}, false
	}
	var out version
	for i := range out {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return version{}, false
		}
		out[i] = n
	}
	return out, true
}

func (v version) less(other version) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

var piACPBinarySpec = binaryutil.BinarySpec{
	Label:         "pi-acp",
	Names:         []string{"pi-acp"},
	WinNames:      []string{"pi-acp.cmd", "pi-acp.exe", "pi-acp"},
	UnixPaths:     []string{"/usr/local/bin/pi-acp", "/opt/homebrew/bin/pi-acp"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("pi-acp", []string{".local", "bin", "pi-acp"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "pi-acp.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "pi-acp.exe"}},
	},
}

// ResolveBinary finds a pre-installed pi-acp. It never invokes npm, npx, Bun,
// or any network installer.
func ResolveBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, piACPBinarySpec)
}
