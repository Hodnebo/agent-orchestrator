package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// transcriptExcerptPageLimit bounds the read behind one excerpt. The byte budget
// is always reached first for a conversation with real answers in it.
const transcriptExcerptPageLimit = 500

const transcriptExcerptOmitted = "[... earlier conversation omitted ...]"

const (
	// excerptActionLineBytes caps one action line, so a long command or path
	// cannot crowd out the conversation around it.
	excerptActionLineBytes = 200
	// excerptActionsPerRun caps the actions kept between two messages; a turn
	// that ran hundreds of tools keeps its newest ones.
	excerptActionsPerRun = 40
	// excerptFailedOutputBytes is how much of a failed command's output is kept:
	// its tail, where the error usually is.
	excerptFailedOutputBytes = 300
)

// TranscriptExcerpt is the newest part of a session's conversation as plain
// text, oldest first, within maxBytes. An agent switch hands it to the next
// agent, so it reads AO's own record: every agent has one, whether or not its
// provider keeps a transcript AO can open. Messages come with the agent's
// actions between them (commands, file changes, tool calls), one line each.
// truncated reports that older entries, or the start of the oldest one shown,
// were left out.
func (s *Service) TranscriptExcerpt(ctx context.Context, id domain.SessionID, maxBytes int) (excerpt string, truncated bool, err error) {
	snapshot, err := s.SnapshotPage(ctx, id, 0, transcriptExcerptPageLimit)
	if err != nil {
		return "", false, err
	}
	excerpt, truncated = transcriptExcerpt(transcriptBlocks(snapshot.Messages, snapshot.Activities), maxBytes)
	return excerpt, truncated || snapshot.HasMoreBefore, nil
}

// transcriptBlocks renders messages and the actions between them in timeline
// order: one block per message, and one "Actions" block per run of actions.
func transcriptBlocks(messages []domain.ConversationMessage, activities []domain.ConversationActivity) []string {
	type entry struct {
		sequence int64
		message  *domain.ConversationMessage
		action   string
	}
	entries := make([]entry, 0, len(messages)+len(activities))
	for i := range messages {
		entries = append(entries, entry{sequence: messages[i].Sequence, message: &messages[i]})
	}
	for _, activity := range activities {
		if line := transcriptAction(activity); line != "" {
			entries = append(entries, entry{sequence: activity.Sequence, action: line})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].sequence < entries[j].sequence })

	var blocks []string
	var actions []string
	flush := func() {
		if len(actions) == 0 {
			return
		}
		header := "Actions:"
		if dropped := len(actions) - excerptActionsPerRun; dropped > 0 {
			header = fmt.Sprintf("Actions (%d earlier omitted):", dropped)
			actions = actions[dropped:]
		}
		blocks = append(blocks, header+"\n- "+strings.Join(actions, "\n- "))
		actions = nil
	}
	for _, entry := range entries {
		if entry.message == nil {
			actions = append(actions, entry.action)
			continue
		}
		text := strings.TrimSpace(entry.message.Text)
		if text == "" {
			continue
		}
		flush()
		blocks = append(blocks, transcriptSpeaker(*entry.message)+":\n"+text)
	}
	flush()
	return blocks
}

// transcriptAction is one line for an action worth handing over, or "" for
// noise the next agent does not need (reasoning, usage, system notices).
func transcriptAction(activity domain.ConversationActivity) string {
	summary := strings.Join(strings.Fields(activity.Summary), " ")
	if summary == "" {
		return ""
	}
	var line string
	switch activity.Kind {
	case domain.ActivityKindCommand:
		line = "ran " + summary
	case domain.ActivityKindFileChange:
		line = "changed " + summary
	case domain.ActivityKindMCPTool:
		line = "tool " + summary
	case domain.ActivityKindPlan:
		line = "plan " + summary
	case domain.ActivityKindError:
		line = "error " + summary
	case domain.ActivityKindApproval:
		line = "asked approval for " + summary
	case domain.ActivityKindUserInput:
		line = "asked the user " + summary
	default:
		return ""
	}
	line = clipText(line, excerptActionLineBytes)
	if status := activity.Status; status == domain.ActivityStatusFailed || status == domain.ActivityStatusCancelled {
		line += " (" + string(status) + ")"
		if activity.Kind == domain.ActivityKindCommand {
			if output := strings.TrimSpace(activity.CommandOutput); output != "" {
				tail := output[len(output)-min(len(output), excerptFailedOutputBytes):]
				line += "\n  output: " + strings.Join(strings.Fields(strings.ToValidUTF8(tail, "")), " ")
			}
		}
	}
	return line
}

// clipText cuts s to at most n bytes on a rune boundary, marking the cut.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

func transcriptExcerpt(blocks []string, maxBytes int) (string, bool) {
	var kept []string
	size := 0
	truncated := false
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if size+len(block)+2 > maxBytes {
			truncated = true
			// Keep the end of the newest block even when it alone is too long:
			// its conclusion is what the next agent most needs.
			if len(kept) == 0 && maxBytes > 0 {
				kept = append(kept, transcriptExcerptOmitted+"\n"+strings.ToValidUTF8(block[len(block)-min(len(block), maxBytes):], ""))
			}
			break
		}
		kept = append(kept, block)
		size += len(block) + 2
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	if truncated && len(kept) > 0 && !strings.HasPrefix(kept[0], transcriptExcerptOmitted) {
		kept = append([]string{transcriptExcerptOmitted}, kept...)
	}
	return strings.Join(kept, "\n\n"), truncated
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
