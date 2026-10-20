package domain

import "testing"

func TestSessionInterfaceOppositeIsTwoWay(t *testing.T) {
	if got := SessionInterfaceTUI.Opposite(); got != SessionInterfaceChat {
		t.Fatalf("TUI opposite = %q, want chat", got)
	}
	if got := SessionInterfaceChat.Opposite(); got != SessionInterfaceTUI {
		t.Fatalf("Chat opposite = %q, want tui", got)
	}
}

func TestStartInterfaceGatesChatByHarness(t *testing.T) {
	cases := []struct {
		requested SessionInterface
		harness   string
		want      SessionInterface
	}{
		{SessionInterfaceChat, "claude-code", SessionInterfaceChat},
		{SessionInterfaceChat, "codex", SessionInterfaceChat},
		// Cursor can switch to Chat but does not start in it.
		{SessionInterfaceChat, "cursor", SessionInterfaceTUI},
		{SessionInterfaceChat, "opencode", SessionInterfaceTUI},
		{SessionInterfaceTUI, "claude-code", SessionInterfaceTUI},
		{"", "claude-code", SessionInterfaceTUI},
		{"bogus", "codex", SessionInterfaceTUI},
	}
	for _, tc := range cases {
		if got := StartInterface(tc.requested, tc.harness); got != tc.want {
			t.Errorf("StartInterface(%q, %q) = %q, want %q", tc.requested, tc.harness, got, tc.want)
		}
	}
}
