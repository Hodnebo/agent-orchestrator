package triage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedEvidenceOmitsConnectionCredentialsAndPromptJournal(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "chat-hosts", "affected")
	if err := os.MkdirAll(host, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, "host.json"), []byte(`{"version":1,"sessionId":"affected","pid":12,"token":"connection-secret","address":"private-address","ownershipFingerprint":"private-fingerprint"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, "acp-prompt.journal"), []byte("private conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := retainedEvidence(context.Background(), dir, "affected")
	raw, _ := json.Marshal(got)
	if len(got) != 1 || got[0].Status != "collected" {
		t.Fatalf("retained facts = %s", raw)
	}
	for _, forbidden := range []string{"connection-secret", "private-address", "private-fingerprint", "private conversation"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("retained facts leaked %s", forbidden)
		}
	}
}

func TestRetainedEvidenceRejectsOutsidePathsAndMismatchedSession(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../other", "a/b", "a\\b"} {
		got := retainedEvidence(context.Background(), dir, id)
		if len(got) != 1 || got[0].Status != "unavailable" {
			t.Fatalf("unsafe session ID accepted: %s", id)
		}
	}
	host := filepath.Join(dir, "chat-hosts", "affected")
	if err := os.MkdirAll(host, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, "host.json"), []byte(`{"sessionId":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := retainedEvidence(context.Background(), dir, "affected"); got[0].Status != "unavailable" {
		t.Fatal("mismatched descriptor accepted")
	}
}
