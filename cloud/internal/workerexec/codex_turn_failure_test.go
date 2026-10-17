package workerexec

import (
	"encoding/json"
	"strings"
	"testing"
)

// A failed Codex turn reports Codex's own reason, not only its status, so the
// timeline says why the turn failed and, for a lapsed sign-in, what to do.
func TestCodexTurnFailureSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		turnErr *codexTurnError
		want    []string
		signIn  bool
	}{
		{name: "no reason", want: []string{"Codex turn ended with failed"}},
		{name: "empty reason", turnErr: &codexTurnError{}, want: []string{"Codex turn ended with failed"}},
		{name: "reason and details", turnErr: &codexTurnError{Message: "model not supported", AdditionalDetails: "gpt-5.6-sol needs a newer Codex"},
			want: []string{"Codex turn failed: model not supported (gpt-5.6-sol needs a newer Codex)"}},
		{name: "unauthorized", turnErr: &codexTurnError{Message: "unexpected status 401", CodexErrorInfo: json.RawMessage(`"unauthorized"`)},
			want: []string{"Codex turn failed: unexpected status 401", "Sign in to Codex again for Cloud"}, signIn: true},
		{name: "refresh token reused", turnErr: &codexTurnError{Message: "Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again."},
			want: []string{"refresh token was already used", "Sign in to Codex again for Cloud"}, signIn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := codexTurnFailure("failed", tc.turnErr).Error()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("codexTurnFailure = %q, want it to contain %q", got, want)
				}
			}
			if strings.Contains(got, "Sign in to Codex again") != tc.signIn {
				t.Fatalf("sign-in guidance = %v in %q", !tc.signIn, got)
			}
		})
	}
	long := codexTurnFailure("failed", &codexTurnError{Message: strings.Repeat("x", 2000)}).Error()
	if len(long) > codexTurnErrorMaxLen+100 {
		t.Fatalf("a long provider error was not bounded: %d characters", len(long))
	}
}

// A turn/completed without an error of its own reports the last final error
// notification Codex sent for the thread.
func TestCodexSessionFallsBackToTheLastErrorNotification(t *testing.T) {
	session := &codexSession{lastError: &codexTurnError{Message: "stream disconnected before completion"}}
	if got := session.reason(nil); got == nil || got.Message != "stream disconnected before completion" {
		t.Fatalf("reason(nil) = %+v", got)
	}
	own := &codexTurnError{Message: "context window exceeded"}
	if got := session.reason(own); got != own {
		t.Fatalf("reason(own) = %+v, want the turn's own error", got)
	}
}
