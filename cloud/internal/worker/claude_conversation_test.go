package worker

import (
	"testing"
	"time"
)

func TestRecordClaudeConversation(t *testing.T) {
	dataDir := t.TempDir()
	start := time.Unix(1_800_000_000, 0)
	if err := RecordClaudeConversation(dataDir, "100", "a", start); err != nil {
		t.Fatal(err)
	}
	// Later hooks in the same conversation keep when it began.
	if err := RecordClaudeConversation(dataDir, "100", "a", start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	want := ClaudeConversation{First: "a", Current: "a", Since: start.UnixNano(), Started: start.UnixNano()}
	if got := ReadClaudeConversations(dataDir)["100"]; got != want {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	// /clear: a new conversation in the same process.
	cleared := start.Add(2 * time.Minute)
	if err := RecordClaudeConversation(dataDir, "100", "b", cleared); err != nil {
		t.Fatal(err)
	}
	want = ClaudeConversation{First: "a", Current: "b", Since: cleared.UnixNano(), Started: start.UnixNano()}
	if got := ReadClaudeConversations(dataDir)["100"]; got != want {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	if err := RecordClaudeConversation(dataDir, "200", "c", cleared); err != nil {
		t.Fatal(err)
	}
	if got := len(ReadClaudeConversations(dataDir)); got != 2 {
		t.Fatalf("records = %d, want one per process", got)
	}
	for _, bad := range []struct{ pid, session string }{
		{"", "a"}, {"abc", "a"}, {"0", "a"}, {"../1", "a"}, {"100", ""}, {"100", "../x"},
	} {
		if err := RecordClaudeConversation(dataDir, bad.pid, bad.session, start); err == nil {
			t.Errorf("RecordClaudeConversation(%q, %q) accepted", bad.pid, bad.session)
		}
	}
}
