package triage

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"time"
)

// retainedEvidence exposes only the selected session's small host descriptor.
// Actual history and provider journals use the separate private context reader.
func retainedEvidence(ctx context.Context, dataDir, sessionID string) []Evidence {
	if sessionID == "" || ctx.Err() != nil {
		return nil
	}
	evidence := Evidence{ID: "retained-session-host", Kind: "session-host", Source: "affected session's retained provider-host descriptor", CollectedAt: time.Now().UTC(), Status: "unavailable"}
	if !validRunID(sessionID) {
		evidence.Text = "The session ID cannot be used to locate a retained provider-host descriptor."
		return []Evidence{evidence}
	}
	path := filepath.Join("chat-hosts", sessionID, "host.json")
	file, info, err := openRetainedContextFile(ctx, dataDir, path)
	if err != nil {
		evidence.Text = "No readable retained provider-host descriptor was found."
		return []Evidence{evidence}
	}
	defer func() { _ = file.Close() }()
	if info.Size() > collectMaxBytes {
		evidence.Text = "The retained provider-host descriptor exceeded its size limit."
		return []Evidence{evidence}
	}
	// Decode an allowlist. In particular, the descriptor's connection token,
	// address and opaque ownership fingerprint never enter the report.
	var descriptor struct {
		Version   int       `json:"version"`
		SessionID string    `json:"sessionId"`
		Protocol  string    `json:"protocol"`
		PID       int       `json:"pid"`
		StartedAt time.Time `json:"startedAt"`
	}
	if err := json.NewDecoder(io.LimitReader(file, collectMaxBytes+1)).Decode(&descriptor); err != nil || descriptor.SessionID != sessionID {
		evidence.Text = "The retained provider-host descriptor was invalid or belonged to another session."
		return []Evidence{evidence}
	}
	evidence.Status = "collected"
	evidence.Text = "Persisted provider-host metadata only. This does not establish that the process is running or healthy."
	evidence.Values = map[string]any{"version": descriptor.Version, "sessionId": descriptor.SessionID, "protocol": descriptor.Protocol, "pid": descriptor.PID, "startedAt": descriptor.StartedAt}
	return RedactEvidence([]Evidence{evidence})
}
