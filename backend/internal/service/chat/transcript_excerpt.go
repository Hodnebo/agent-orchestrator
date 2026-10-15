package chat

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// transcriptExcerptPageLimit bounds the read behind one excerpt. The byte budget
// is always reached first for a conversation with real answers in it.
const transcriptExcerptPageLimit = 500

const transcriptExcerptOmitted = "[... earlier conversation omitted ...]"

// TranscriptExcerpt is the newest part of a session's conversation as plain
// text, oldest first, within maxBytes. An agent switch hands it to the next
// agent, so it reads AO's own record: every agent has one, whether or not its
// provider keeps a transcript AO can open. truncated reports that older
// messages, or the start of the oldest one shown, were left out.
func (s *Service) TranscriptExcerpt(ctx context.Context, id domain.SessionID, maxBytes int) (excerpt string, truncated bool, err error) {
	snapshot, err := s.SnapshotPage(ctx, id, 0, transcriptExcerptPageLimit)
	if err != nil {
		return "", false, err
	}
	excerpt, truncated = transcriptExcerpt(snapshot.Messages, maxBytes)
	return excerpt, truncated || snapshot.HasMoreBefore, nil
}

func transcriptExcerpt(messages []domain.ConversationMessage, maxBytes int) (string, bool) {
	var blocks []string
	size := 0
	truncated := false
	for i := len(messages) - 1; i >= 0; i-- {
		text := strings.TrimSpace(messages[i].Text)
		if text == "" {
			continue
		}
		block := transcriptSpeaker(messages[i]) + ":\n" + text
		if size+len(block)+2 > maxBytes {
			truncated = true
			// Keep the end of the newest message even when it alone is too long:
			// its conclusion is what the next agent most needs.
			if len(blocks) == 0 && maxBytes > 0 {
				blocks = append(blocks, transcriptExcerptOmitted+"\n"+strings.ToValidUTF8(block[len(block)-min(len(block), maxBytes):], ""))
			}
			break
		}
		blocks = append(blocks, block)
		size += len(block) + 2
	}
	for left, right := 0, len(blocks)-1; left < right; left, right = left+1, right-1 {
		blocks[left], blocks[right] = blocks[right], blocks[left]
	}
	if truncated && len(blocks) > 0 && !strings.HasPrefix(blocks[0], transcriptExcerptOmitted) {
		blocks = append([]string{transcriptExcerptOmitted}, blocks...)
	}
	return strings.Join(blocks, "\n\n"), truncated
}

func transcriptSpeaker(message domain.ConversationMessage) string {
	if message.Role == domain.MessageRoleAssistant {
		return "Assistant"
	}
	if message.Origin == domain.MessageOriginHuman || message.Origin == "" {
		return "User"
	}
	return "AO"
}
