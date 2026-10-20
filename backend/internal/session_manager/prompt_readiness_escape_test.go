package sessionmanager

import "testing"

func TestPromptOutputContainsMatchesAStyledPrompt(t *testing.T) {
	// Kimi 2.2 draws its composer border and prompt in different colours.
	raw := "\x1b[34m│\x1b[39m > \x1b[7m \x1b[27m\r\n"
	if !promptOutputContains(raw, []string{"│ >"}) {
		t.Fatal("a prompt split by a colour code was not recognised")
	}
	if promptOutputContains("\x1b[34mloading…\x1b[39m", []string{"│ >"}) {
		t.Fatal("output without the prompt matched")
	}
}
