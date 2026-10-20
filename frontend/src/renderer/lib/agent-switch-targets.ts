// Agents a Chat session can switch to: the ones with a structured Chat driver.
export const CHAT_SWITCH_TARGETS = [
	"claude-code", "codex", "cursor", "opencode", "opencode-v2", "droid", "kimi",
	"kimchi", "pi", "omp", "qwen", "deepseek-harness", "unreal-agent",
] as const;
// Agents a Terminal session can switch to: the Chat agents that also run in a
// terminal, plus the terminal agents that receive the handoff note on their
// command line. Agents that have it typed in after start stay out until each
// has passed a real switch. Unreal Agent is Chat-only; DeepSeek and Autohand
// answer one task and exit in a terminal, and Gemini CLI is no longer supported.
const NOT_IN_TERMINAL: readonly string[] = ["unreal-agent", "deepseek-harness"];
export const TERMINAL_SWITCH_TARGETS = [
	...CHAT_SWITCH_TARGETS.filter((agent) => !NOT_IN_TERMINAL.includes(agent)),
	"continue", "devin", "muse", "kilocode", "mimo-code", "prime-agent",
	"auggie", "copilot", "agy", "vibe",
] as const;
