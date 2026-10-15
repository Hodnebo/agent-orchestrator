package chat

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestTranscriptExcerptInterleavesActionsBetweenMessages(t *testing.T) {
	messages := []domain.ConversationMessage{
		{Sequence: 1, Role: domain.MessageRoleUser, Origin: domain.MessageOriginHuman, Text: "fix the tests"},
		{Sequence: 6, Role: domain.MessageRoleAssistant, Text: "Fixed auth.go."},
	}
	activities := []domain.ConversationActivity{
		{Sequence: 2, Kind: domain.ActivityKindReasoning, Summary: "thinking about it"},
		{Sequence: 3, Kind: domain.ActivityKindCommand, Status: domain.ActivityStatusFailed, Summary: "npm test",
			CommandOutput: "lots of noise\nFAIL auth.test.ts expected 200 got 401"},
		{Sequence: 4, Kind: domain.ActivityKindFileChange, Status: domain.ActivityStatusCompleted, Summary: "auth.go +12 -3"},
		{Sequence: 5, Kind: domain.ActivityKindUsage, Summary: "1200 tokens"},
	}
	excerpt, truncated := transcriptExcerpt(transcriptBlocks(messages, activities), 1<<20)
	if truncated {
		t.Fatal("excerpt truncated with room to spare")
	}
	want := "User:\nfix the tests\n\n" +
		"Actions:\n- ran npm test (failed)\n  output: lots of noise FAIL auth.test.ts expected 200 got 401\n- changed auth.go +12 -3\n\n" +
		"Assistant:\nFixed auth.go."
	if excerpt != want {
		t.Fatalf("excerpt =\n%s\n\nwant\n%s", excerpt, want)
	}
}

func TestTranscriptExcerptKeepsTheNewestWithinBudget(t *testing.T) {
	var messages []domain.ConversationMessage
	for i := 0; i < 50; i++ {
		messages = append(messages, domain.ConversationMessage{
			Sequence: int64(i), Role: domain.MessageRoleUser, Origin: domain.MessageOriginHuman,
			Text: strings.Repeat("x", 100) + " message " + string(rune('A'+i%26)),
		})
	}
	messages[49].Text = "the newest message"
	excerpt, truncated := transcriptExcerpt(transcriptBlocks(messages, nil), 1000)
	if !truncated || !strings.HasPrefix(excerpt, transcriptExcerptOmitted) {
		t.Fatalf("excerpt was not marked truncated: %q", excerpt[:min(len(excerpt), 80)])
	}
	if !strings.HasSuffix(excerpt, "the newest message") {
		t.Fatalf("excerpt lost the newest message: ...%q", excerpt[max(0, len(excerpt)-80):])
	}
	if len(excerpt) > 1000+len(transcriptExcerptOmitted)+2 {
		t.Fatalf("excerpt is %d bytes, over its budget", len(excerpt))
	}
}
