package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/aoagents/agent-orchestrator/backend/internal/preview"
)

const maxContextEntries = 128

func readDiagnosticContext(ctx context.Context, dataDir string, origin Origin, request ContextRead) (Evidence, error) {
	if err := ctx.Err(); err != nil {
		return Evidence{}, err
	}
	if request.Source == "" || len(request.Source) > 128 || len(request.Path) > 1024 {
		return Evidence{}, errors.New("invalid diagnostic context source or path")
	}
	if !localContextAllowed(origin) {
		return Evidence{}, errors.New("local files cannot establish evidence for a remote host")
	}
	switch request.Source {
	case "session-history":
		return readSessionHistory(ctx, dataDir, origin, request)
	case "daemon-log":
		return readRetainedLog(ctx, dataDir, "daemon.log", request.Source, request)
	case "provider-log":
		return readProviderLog(ctx, dataDir, origin, request)
	}
	if request.Source == "session-hook-errors" {
		if request.Path != "" || request.Offset != 0 || request.List {
			return Evidence{}, errors.New("hook errors do not accept a file path, offset or directory request")
		}
		if err := validateRetainedRead(origin, request); err != nil {
			return Evidence{}, err
		}
		return readHookErrors(ctx, dataDir, origin.SessionID), nil
	}
	retained, err := readRetainedContext(ctx, dataDir, origin)
	root := retained.Roots[request.Source]
	if root == "" {
		if err != nil {
			return Evidence{}, err
		}
		return Evidence{}, errors.New("the requested context source is unavailable for this session")
	}
	pinned, pinErr := pinDiagnosticRoot(root, dataDir)
	if pinErr != nil {
		return Evidence{}, pinErr
	}
	defer func() { _ = pinned.Close() }()
	return readWorkspaceContextRoot(ctx, pinned, request)
}

func localContextAllowed(origin Origin) bool {
	if origin.HostID != "" {
		return false
	}
	var session map[string]json.RawMessage
	if len(origin.Session) != 0 && json.Unmarshal(origin.Session, &session) != nil {
		return false
	}
	if host := session["hostId"]; len(host) != 0 && string(host) != "null" {
		var id string
		if json.Unmarshal(host, &id) != nil || id != "" {
			return false
		}
	}
	return len(session["cloud"]) == 0 || string(session["cloud"]) == "null"
}

func pinDiagnosticRoot(path, dataDir string) (*os.Root, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("the diagnostic source directory is unavailable or is an alias")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, errors.New("the diagnostic source directory is unavailable")
	}
	if dataDir != "" {
		dataRoot, err := filepath.EvalSymlinks(dataDir)
		if err != nil || contextContains(canonical, dataRoot) || contextContains(dataRoot, canonical) && !contextContains(filepath.Join(dataRoot, "worktrees"), canonical) {
			return nil, errors.New("app state or an ancestor directory cannot be used as project context")
		}
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, errors.New("the diagnostic source directory could not be opened")
	}
	opened, openErr := root.Stat(".")
	again, resolveErr := filepath.EvalSymlinks(path)
	latest, statErr := os.Lstat(path)
	if openErr != nil || resolveErr != nil || statErr != nil || again != canonical || latest.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, opened) || !os.SameFile(opened, latest) {
		_ = root.Close()
		return nil, errors.New("the diagnostic source directory changed while it was opened")
	}
	return root, nil
}

func contextContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func contextPath(confined *os.Root, relative string, directory bool) (os.FileInfo, string, error) {
	if filepath.IsAbs(relative) || strings.ContainsRune(relative, '\x00') || len(relative) > 1024 {
		return nil, "", errors.New("context paths must be relative")
	}
	if relative == "" && directory {
		relative = "."
	}
	clean, valid := preview.CleanWorkspacePath(relative)
	if !valid && relative != "." {
		return nil, "", errors.New("invalid context path")
	}
	if relative == "." {
		clean = "."
	}
	path := "."
	var info os.FileInfo
	var err error
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if !diagnosticFileName(part) {
			return nil, "", errors.New("this path is excluded from diagnostic context")
		}
		path = filepath.Join(path, part)
		info, err = confined.Lstat(path)
		if err != nil {
			return nil, "", err
		}
		// Reject internal aliases too. Confinement alone does not stop a
		// harmless-looking name pointing to a credential inside the root.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, "", errors.New("diagnostic context does not follow symbolic links")
		}
	}
	return info, clean, nil
}

func diagnosticFileName(name string) bool {
	lower := strings.ToLower(name)
	for _, excluded := range []string{".git", ".ao", ".ssh", ".aws", ".azure", ".config", ".codex", ".claude", "node_modules", ".npmrc", ".pypirc", ".netrc", "auth.json", "credentials", "credentials.json", ".credentials.json", "cookies", "local storage", "session storage", "login data", "web data"} {
		if lower == excluded {
			return false
		}
	}
	if strings.HasPrefix(lower, ".env") || strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") || strings.HasPrefix(lower, "token.") || strings.Contains(lower, "credential") || strings.Contains(lower, "private-key") {
		return false
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".sqlite", ".sqlite3", ".db"} {
		if strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	return true
}

func readWorkspaceContext(ctx context.Context, root string, request ContextRead) (Evidence, error) {
	pinned, err := pinDiagnosticRoot(root, "")
	if err != nil {
		return Evidence{}, err
	}
	defer func() { _ = pinned.Close() }()
	return readWorkspaceContextRoot(ctx, pinned, request)
}

func readWorkspaceContextRoot(ctx context.Context, root *os.Root, request ContextRead) (Evidence, error) {
	item := Evidence{ID: "context-file", Kind: "private-context", Source: request.Source + ":" + request.Path, CollectedAt: time.Now().UTC(), Status: "collected"}
	if request.Offset < 0 || request.Offset > 16<<20 || request.List && request.Offset != 0 {
		return item, errors.New("invalid context offset")
	}
	before, path, err := contextPath(root, request.Path, request.List)
	if err != nil {
		return item, err
	}
	if err := ctx.Err(); err != nil {
		return item, err
	}
	if request.List {
		if !before.IsDir() {
			return item, errors.New("the context path is not a directory")
		}
		return listWorkspaceContext(root, path, before, item)
	}
	file, opened, _, err := preview.OpenRootFile(root, path)
	if err != nil {
		return item, err
	}
	defer func() { _ = file.Close() }()
	after, _, err := contextPath(root, path, false)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return item, errors.New("the context file changed while it was opened")
	}
	if _, err := file.Seek(request.Offset, io.SeekStart); err != nil {
		return item, err
	}
	data, err := io.ReadAll(io.LimitReader(file, collectMaxBytes+1))
	if err != nil {
		return item, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return item, errors.New("binary files are excluded from diagnostic context")
	}
	if len(data) > collectMaxBytes {
		data = data[:collectMaxBytes]
		item.Status = "truncated"
	}
	item.Text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, string(bytes.ToValidUTF8(data, []byte("?"))))
	item.Values = map[string]any{"offset": request.Offset, "nextOffset": request.Offset + int64(len(data)), "bytes": len(data), "privateContext": true}
	return RedactEvidence([]Evidence{item})[0], ctx.Err()
}

func listWorkspaceContext(root *os.Root, path string, before os.FileInfo, item Evidence) (Evidence, error) {
	dir, err := root.OpenRoot(filepath.FromSlash(path))
	if err != nil {
		return item, err
	}
	defer func() { _ = dir.Close() }()
	file, err := dir.Open(".")
	if err != nil {
		return item, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return item, err
	}
	after, _, err := contextPath(root, path, true)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return item, errors.New("the context directory changed while it was opened")
	}
	entries, err := file.ReadDir(maxContextEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return item, err
	}
	if len(entries) > maxContextEntries {
		entries = entries[:maxContextEntries]
		item.Status = "truncated"
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !diagnosticFileName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	item.Values = map[string]any{"entries": names, "privateContext": true}
	return RedactEvidence([]Evidence{item})[0], nil
}

func readHookErrors(ctx context.Context, dataDir, sessionID string) Evidence {
	item := Evidence{ID: "session-hook-errors", Kind: "hook-errors", Source: "retained hooks.log for the affected session", CollectedAt: time.Now().UTC(), Status: "unavailable"}
	if !validRunID(sessionID) || ctx.Err() != nil {
		item.Text = "No usable local session reference was supplied."
		return item
	}
	root, err := pinDiagnosticRoot(dataDir, "")
	if err != nil {
		item.Text = "No readable retained hook error log was found."
		return item
	}
	defer func() { _ = root.Close() }()
	before, _, err := contextPath(root, "hooks.log", false)
	if err != nil {
		item.Text = "No readable retained hook error log was found."
		return item
	}
	file, info, _, err := preview.OpenRootFile(root, "hooks.log")
	if err != nil {
		item.Text = "No readable retained hook error log was found."
		return item
	}
	defer func() { _ = file.Close() }()
	after, _, err := contextPath(root, "hooks.log", false)
	if err != nil || !os.SameFile(before, info) || !os.SameFile(info, after) {
		item.Text = "The retained hook error log changed while it was opened."
		return item
	}
	start := max(int64(0), info.Size()-collectMaxBytes)
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		item.Text = "The retained hook error log could not be read."
		return item
	}
	data, err := io.ReadAll(io.LimitReader(file, collectMaxBytes))
	if err != nil {
		item.Text = "The retained hook error log could not be read."
		return item
	}
	lines := strings.Split(string(bytes.ToValidUTF8(data, []byte("?"))), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	var relevant []string
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) == 3 && parts[1] == "session="+sessionID {
			relevant = append(relevant, line)
		}
	}
	if len(relevant) > 20 {
		relevant = relevant[len(relevant)-20:]
	}
	item.Status = "collected"
	item.Text = strings.Join(relevant, "\n")
	item.Values = map[string]any{"lines": len(relevant), "tailBytes": len(data), "tailTruncated": start > 0}
	if len(relevant) == 0 {
		item.Text = "No matching lines in the retained tail. This does not establish that hooks succeeded."
	}
	return RedactEvidence([]Evidence{item})[0]
}

func contextReadError(request ContextRead, err error) Evidence {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		err = errors.New("the requested local context could not be read")
	}
	return Evidence{ID: "context-unavailable", Kind: "private-context", Source: Redact(fmt.Sprintf("%s:%s", request.Source, request.Path)), CollectedAt: time.Now().UTC(), Status: "unavailable", Text: Redact(err.Error())}
}
