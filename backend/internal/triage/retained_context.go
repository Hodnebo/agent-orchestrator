package triage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type retainedContext struct {
	Evidence []Evidence
	Roots    map[string]string `json:"-"`
}

// readRetainedContext is optional evidence enrichment after the independent
// snapshot. No normal service, provider, migration or writable open is used.
func readRetainedContext(ctx context.Context, dataDir string, origin Origin) (retainedContext, error) {
	result := retainedContext{Roots: map[string]string{}}
	at := time.Now().UTC()
	unavailable := func(id, text string) {
		item := Evidence{ID: id, Kind: "retained-session", Source: "selected local session's retained SQLite facts", CollectedAt: at, Status: "unavailable", Text: text}
		if validateRetainedRead(origin, ContextRead{}) == nil {
			item.Values = map[string]any{"sources": []string{"daemon-log", "provider-log", "session-history", "session-hook-errors"}}
		}
		result.Evidence = append(result.Evidence, item)
	}
	if origin.SessionID == "" {
		if localContextAllowed(origin) {
			result.Evidence = append(result.Evidence, Evidence{ID: "retained-context-sources", Kind: "retained-session", Source: "local diagnostic source registry", CollectedAt: at, Status: "collected", Values: map[string]any{"sources": []string{"daemon-log"}}})
		}
		return result, nil
	}
	if len(origin.Session) > collectMaxBytes {
		unavailable("retained-session", "The initiating session identity could not be established. Local retained data was not read.")
		return result, errors.New("retained session identity snapshot exceeded its limit")
	}
	if !localContextAllowed(origin) {
		unavailable("retained-session", "This report concerns a remote or cloud session. Local retained session data was not read.")
		return result, nil
	}
	var snapshot struct {
		ID string `json:"id"`
	}
	if len(origin.Session) != 0 {
		if err := json.Unmarshal(origin.Session, &snapshot); err != nil || snapshot.ID != "" && snapshot.ID != origin.SessionID {
			unavailable("retained-session", "The initiating session snapshot belongs to a different session. Local retained data was not read.")
			return result, errors.New("retained session identity snapshot is mismatched")
		}
	}
	if len(origin.SessionID) > 255 || strings.ContainsRune(origin.SessionID, 0) {
		unavailable("retained-session", "The selected session identity is invalid. Local retained data was not read.")
		return result, errors.New("invalid retained session identity")
	}
	if err := ctx.Err(); err != nil {
		unavailable("retained-session", "The retained session read was cancelled or timed out.")
		return result, err
	}
	db, err := sqlite.OpenReadOnly(ctx, dataDir)
	if err != nil {
		unavailable("retained-session", "The existing local session database could not be read. No database was created or repaired.")
		return result, errors.New("retained session database unavailable")
	}
	defer func() { _ = db.Close() }()
	id := domain.SessionID(origin.SessionID)
	facts, found, err := db.ReadTriageSessionFacts(ctx, id)
	if err != nil {
		unavailable("retained-session", "The selected session's retained facts could not be read. The database may be unavailable or use an older schema.")
		return result, errors.New("retained session facts unavailable")
	}
	if !found {
		unavailable("retained-session", "No retained local row exists for the selected session.")
		return result, nil
	}
	paths := []string{facts.WorkspacePath.Value}
	if !facts.WorkspacePath.Truncated && usableRetainedRoot(facts.WorkspacePath.Value) {
		result.Roots["workspace"] = filepath.Clean(facts.WorkspacePath.Value)
	}
	var failures []error
	worktrees, omitted, rootsErr := db.ReadTriageWorkspaceRoots(ctx, id)
	if rootsErr != nil {
		unavailable("retained-workspaces", "The selected session's registered workspace roots could not be read.")
		failures = append(failures, errors.New("retained workspace roots unavailable"))
	}
	for _, root := range worktrees {
		paths = append(paths, root.Path.Value)
		if root.Name.Truncated || root.Path.Truncated || !usableRetainedRoot(root.Path.Value) {
			omitted = true
			continue
		}
		name := root.Name.Value
		if name == domain.RootWorkspaceRepoName {
			if result.Roots["workspace"] == "" {
				result.Roots["workspace"] = filepath.Clean(root.Path.Value)
			}
		} else if usableRetainedRepoName(name) {
			result.Roots["workspace:"+name] = filepath.Clean(root.Path.Value)
		} else {
			omitted = true
		}
	}
	if facts.WorkspacePath.Truncated || facts.WorkspacePath.Value != "" && !usableRetainedRoot(facts.WorkspacePath.Value) {
		omitted = true
	}
	sources := []string{"daemon-log", "provider-log", "session-history", "session-hook-errors"}
	for source := range result.Roots {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	values := map[string]any{"sessionId": origin.SessionID, "terminated": facts.IsTerminated, "sources": sources, "workspaceRootsOmitted": omitted}
	truncated := retainedFields(values, map[string]sqlitestore.TriageDiagnosticText{
		"agent": facts.Harness, "mode": facts.Mode, "activityState": facts.Activity,
		"provisionState": facts.ProvisionState, "provisionError": facts.ProvisionError,
		"createdAt": facts.CreatedAt, "updatedAt": facts.UpdatedAt, "activityLastAt": facts.ActivityLastAt,
	}, paths)
	status := "collected"
	if truncated {
		status = "truncated"
	}
	result.Evidence = append(result.Evidence, Evidence{ID: "retained-session", Kind: "retained-session", Source: "selected local session's retained SQLite row", CollectedAt: at, Status: status,
		Text: "Persisted session facts only. These do not establish current process health. Workspace paths remain private file-source references.", Values: values})
	turn, found, turnErr := db.ReadTriageLatestTurn(ctx, id)
	if turnErr != nil {
		unavailable("retained-turn", "The selected session's latest owned turn could not be read.")
		failures = append(failures, errors.New("retained owned turn unavailable"))
	} else if !found {
		unavailable("retained-turn", "No retained turn owned by the selected session was found in its currently linked conversation.")
	} else {
		values := map[string]any{}
		truncated := retainedFields(values, map[string]sqlitestore.TriageDiagnosticText{
			"state": turn.State, "error": turn.Error, "requestedAt": turn.RequestedAt,
			"startedAt": turn.StartedAt, "completedAt": turn.CompletedAt,
		}, paths)
		status := "collected"
		if truncated {
			status = "truncated"
		}
		result.Evidence = append(result.Evidence, Evidence{ID: "retained-turn", Kind: "retained-turn", Source: "latest retained turn owned by the selected local session", CollectedAt: at, Status: status,
			Text: "Selected-session ownership was checked. Other sessions in a shared project conversation were excluded.", Values: values})
	}
	result.Evidence = RedactEvidence(result.Evidence)
	return result, errors.Join(failures...)
}

func usableRetainedRoot(path string) bool {
	return path != "" && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Dir(filepath.Clean(path)) != filepath.Clean(path)
}

func usableRetainedRepoName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if r != '.' && r != '-' && r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func retainedFields(values map[string]any, fields map[string]sqlitestore.TriageDiagnosticText, paths []string) bool {
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	var truncated []string
	for key, field := range fields {
		text := field.Value
		for _, path := range paths {
			if path != "" {
				text = strings.ReplaceAll(text, path, "[workspace]")
			}
		}
		values[key] = Redact(text)
		if field.Truncated {
			truncated = append(truncated, key)
		}
	}
	if len(truncated) > 0 {
		sort.Strings(truncated)
		values["truncatedFields"] = truncated
	}
	return len(truncated) > 0
}
