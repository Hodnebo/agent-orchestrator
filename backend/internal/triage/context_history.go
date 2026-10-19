package triage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func privateContextItem(source string) Evidence {
	return Evidence{ID: source, Kind: "private-context", Source: source, CollectedAt: time.Now().UTC(), Status: "unavailable", Values: map[string]any{"privateContext": true}}
}

func validateRetainedRead(origin Origin, request ContextRead) error {
	if !localContextAllowed(origin) {
		return errors.New("local context cannot establish evidence for a remote or cloud session")
	}
	if !validRunID(origin.SessionID) || len(origin.Session) > collectMaxBytes {
		return errors.New("no usable local session reference was supplied")
	}
	var snapshot struct {
		ID string `json:"id"`
	}
	if len(origin.Session) != 0 {
		if err := json.Unmarshal(origin.Session, &snapshot); err != nil || snapshot.ID != "" && snapshot.ID != origin.SessionID {
			return errors.New("the initiating session snapshot does not match the selected session")
		}
	}
	if request.Path != "" || request.List || request.Offset < 0 || request.Offset > sqlitestore.TriageMaxHistoryOffset {
		return errors.New("this retained source accepts only a bounded record offset")
	}
	return nil
}

func readSessionHistory(ctx context.Context, dataDir string, origin Origin, request ContextRead) (Evidence, error) {
	item := privateContextItem("session-history")
	if err := validateRetainedRead(origin, request); err != nil {
		return item, err
	}
	db, err := sqlite.OpenReadOnly(ctx, dataDir)
	if err != nil {
		item.Text = "The retained local conversation database is unavailable. No database was created or repaired."
		return item, nil
	}
	defer func() { _ = db.Close() }()
	entries, more, err := db.ReadTriageSessionHistory(ctx, domain.SessionID(origin.SessionID), request.Offset)
	if err != nil {
		item.Text = "The selected session's retained conversation and tool history could not be read. Retained data may be unavailable or use an older schema."
		return item, nil
	}
	if len(entries) == 0 {
		item.Text = "No owned conversation or tool history was found at this offset. This does not establish that the provider had no history."
		return item, nil
	}
	values := make([]any, 0, len(entries))
	truncated := more
	bytesUsed := 0
	for _, entry := range entries {
		value := map[string]any{"sequence": entry.Sequence, "rolledBack": entry.RolledBack}
		truncated = retainedFields(value, map[string]sqlitestore.TriageDiagnosticText{
			"kind": entry.Kind, "role": entry.Role, "state": entry.State, "turnState": entry.TurnState,
			"text": entry.Text, "output": entry.Output, "streamedText": entry.Streamed, "createdAt": entry.CreatedAt,
		}, nil) || truncated
		if entry.Detail.Value != "" {
			if entry.Detail.Truncated || !json.Valid([]byte(entry.Detail.Value)) {
				value["detail"] = "Retained tool detail was truncated or invalid and was omitted."
				truncated = true
			} else {
				value["detail"] = json.RawMessage(entry.Detail.Value)
			}
		}
		var added bool
		values, added = appendPrivateContextEntry(values, value, &bytesUsed)
		if !added {
			more, truncated = true, true
			break
		}
	}
	item.Status = "collected"
	if truncated {
		item.Status = "truncated"
	}
	item.Text = "Actual retained user/assistant messages and tool activities owned by the selected local session, newest first. Rolled-back entries are marked. Other session owners and unowned timeline entries were excluded."
	item.Values = map[string]any{"privateContext": true, "entries": values, "offset": request.Offset, "nextOffset": request.Offset + int64(len(values)), "offsetKind": "records", "hasMore": more}
	return RedactEvidence([]Evidence{item})[0], ctx.Err()
}

func readProviderEvents(ctx context.Context, dataDir string, origin Origin, request ContextRead) (Evidence, error) {
	item := privateContextItem("provider-log")
	if err := validateRetainedRead(origin, request); err != nil {
		return item, err
	}
	db, err := sqlite.OpenReadOnly(ctx, dataDir)
	if err != nil {
		item.Text = "No readable retained provider journal or event archive was found. No database was created or repaired."
		return item, nil
	}
	defer func() { _ = db.Close() }()
	entries, more, err := db.ReadTriageProviderEvents(ctx, domain.SessionID(origin.SessionID), request.Offset)
	if err != nil {
		item.Text = "The selected session's retained provider archive could not be read. Retained data may be unavailable or use an older schema."
		return item, nil
	}
	if len(entries) == 0 {
		item.Text = "No retained provider events were found at this offset. Provider stderr is not retained by the local provider host; absence of an archive does not establish success."
		return item, nil
	}
	values := make([]any, 0, len(entries))
	truncated := more
	bytesUsed := 0
	for _, entry := range entries {
		value := map[string]any{}
		truncated = retainedFields(value, map[string]sqlitestore.TriageDiagnosticText{"method": entry.Method, "receivedAt": entry.ReceivedAt}, nil) || truncated
		if entry.Payload.Truncated || !json.Valid([]byte(entry.Payload.Value)) {
			value["payload"] = "Retained provider payload was truncated or invalid and was omitted."
			truncated = true
		} else {
			value["payload"] = json.RawMessage(entry.Payload.Value)
		}
		var added bool
		values, added = appendPrivateContextEntry(values, value, &bytesUsed)
		if !added {
			more, truncated = true, true
			break
		}
	}
	item.Status = "collected"
	if truncated {
		item.Status = "truncated"
	}
	item.Text = "Retained provider-event archive owned by the selected local session, newest first. These are historical records and do not establish current provider health."
	item.Values = map[string]any{"privateContext": true, "storage": "selected-session SQLite provider-event archive", "entries": values, "offset": request.Offset, "nextOffset": request.Offset + int64(len(values)), "offsetKind": "records", "hasMore": more}
	return RedactEvidence([]Evidence{item})[0], ctx.Err()
}

func appendPrivateContextEntry(entries []any, value map[string]any, bytesUsed *int) ([]any, bool) {
	value = redactMap(value)
	encoded, err := json.Marshal(value)
	// Reserve space for the evidence envelope and cursor metadata. JSON can
	// expand control characters, so a raw-text bound alone is insufficient.
	if err != nil || *bytesUsed+len(encoded)+1 > collectMaxBytes-4096 {
		return entries, false
	}
	*bytesUsed += len(encoded) + 1
	return append(entries, value), true
}
