package domain

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSessionInterfaceOppositeIsTwoWay(t *testing.T) {
	if got := SessionInterfaceTUI.Opposite(); got != SessionInterfaceChat {
		t.Fatalf("TUI opposite = %q, want chat", got)
	}
	if got := SessionInterfaceChat.Opposite(); got != SessionInterfaceTUI {
		t.Fatalf("Chat opposite = %q, want tui", got)
	}
}

// The precedence is the local daemon's, case for case.
func TestStartInterfaceMatchesTheSharedCases(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/session-start-interface.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Kind      string `json:"kind"`
			Requested string `json:"requested"`
			Default   string `json:"default"`
			Want      string `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, tc := range fixture.Cases {
		// A harness that starts in Chat isolates the precedence.
		got := StartInterface(tc.Kind, SessionInterface(tc.Requested), SessionInterface(tc.Default), "claude-code")
		if string(got) != tc.Want {
			t.Errorf("StartInterface(%s, requested %q, default %q) = %q, want %q", tc.Kind, tc.Requested, tc.Default, got, tc.Want)
		}
	}
}

func TestStartInterfaceGatesChatByHarness(t *testing.T) {
	for _, kind := range []string{"worker", "orchestrator"} {
		for harness, want := range map[string]SessionInterface{
			"claude-code": SessionInterfaceChat,
			"codex":       SessionInterfaceChat,
			// Cursor can switch to Chat but does not start in it.
			"cursor":   SessionInterfaceTUI,
			"opencode": SessionInterfaceTUI,
		} {
			if got := StartInterface(kind, "", SessionInterfaceChat, harness); got != want {
				t.Errorf("StartInterface(%s, %s) = %q, want %q", kind, harness, got, want)
			}
		}
	}
}
