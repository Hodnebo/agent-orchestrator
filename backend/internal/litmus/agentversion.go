package litmus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var agentVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// AgentVersion installs a pinned package with npm's global layout in the leg.
// npm owns the links in <leg>/bin, which the leg shell puts first on PATH.
func (s *Service) AgentVersion(ctx context.Context, sessionID, leg, agent, version string) (Run, error) {
	if leg != "head" && leg != "base" {
		return Run{}, invalid("TESTING_LEG_INVALID", "leg must be base or head")
	}
	packages := map[string]string{
		"codex": "@openai/codex", "claude": "@anthropic-ai/claude-code",
		"claude-code": "@anthropic-ai/claude-code", "copilot": "@github/copilot",
		"opencode": "opencode-ai", "gemini": "@google/gemini-cli", "qwen": "@qwen-code/qwen-code",
	}
	pkg, ok := packages[agent]
	if !ok {
		return Run{}, invalid("TESTING_AGENT_INVALID", "agent-version requires an npm agent: codex, claude-code, copilot, opencode, gemini or qwen")
	}
	if !agentVersionPattern.MatchString(version) {
		return Run{}, invalid("TESTING_AGENT_VERSION_INVALID", "version must be an exact version such as 1.2.3")
	}
	run, done, err := s.begin(sessionID, "launch "+leg)
	if err != nil {
		return Run{}, err
	}
	defer done()
	i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg })
	if i < 0 {
		return Run{}, conflict("TESTING_NOT_PREPARED", "run `ao testing prepare` before installing an agent version")
	}
	if run.Legs[i].State == "running" {
		return Run{}, conflict("TESTING_LEG_RUNNING", "install agent versions before launching "+leg)
	}
	dir := s.store.legDir(run.ID, leg)
	prefix := dir
	env := os.Environ()
	if node := readManifest(run.Legs[i].Worktree).NodePath; node != "" {
		env = append(env, "PATH="+prependPath(filepath.Dir(node), os.Getenv("PATH")))
	}
	env, err = prepareLegEnv(env, dir, s.ownerHome)
	if err != nil {
		return Run{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := s.runner.Run(ctx, dir, env, "npm", "install", "-g", "--prefix", prefix, "--no-audit", "--no-fund", pkg+"@"+version)
	if err != nil {
		return Run{}, fmt.Errorf("litmus: install %s@%s: %w: %s", agent, version, err, s.scrub(string(out)))
	}
	binary := agent
	if binary == "claude-code" {
		binary = "claude"
	}
	if err := validateAgentVersion(prefix, binary); err != nil {
		return Run{}, err
	}
	return run, nil
}

func validateAgentVersion(prefix, binary string) error {
	target, err := filepath.EvalSymlinks(filepath.Join(prefix, "bin", binary))
	if err != nil {
		return fmt.Errorf("litmus: resolve installed %s: %w", binary, err)
	}
	modules, err := filepath.EvalSymlinks(filepath.Join(prefix, "lib", "node_modules"))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(modules, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !isExecutable(target) {
		return fmt.Errorf("litmus: %s binary must be installed inside the leg's lib/node_modules", binary)
	}
	return nil
}
