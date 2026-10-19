package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/aoagents/agent-orchestrator/backend/internal/preview"
)

// openRetainedContextFile uses a pinned AO root and rejects aliases at every
// component. Callers supply fixed known filenames, never model-supplied paths.
func openRetainedContextFile(ctx context.Context, dataDir, relative string) (*os.File, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	root, err := pinDiagnosticRoot(dataDir, "")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()
	before, _, err := contextPath(root, relative, false)
	if err != nil {
		return nil, nil, errors.New("the retained diagnostic file is unavailable")
	}
	file, opened, _, err := preview.OpenRootFile(root, relative)
	if err != nil {
		return nil, nil, errors.New("the retained diagnostic file could not be opened")
	}
	after, _, err := contextPath(root, relative, false)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		_ = file.Close()
		return nil, nil, errors.New("the retained diagnostic file changed while it was opened")
	}
	return file, opened, nil
}

func readRetainedLog(ctx context.Context, dataDir, relative, source string, request ContextRead) (Evidence, error) {
	item := privateContextItem(source)
	if request.Path != "" || request.List || request.Offset < 0 || request.Offset > 16<<20 {
		return item, errors.New("retained logs accept only a bounded byte offset from the end")
	}
	file, info, err := openRetainedContextFile(ctx, dataDir, relative)
	if err != nil {
		item.Text = "No readable retained log was found at the known AO-owned location. This does not establish that the affected process is healthy."
		return item, nil
	}
	defer func() { _ = file.Close() }()
	end := max(int64(0), info.Size()-request.Offset)
	start := max(int64(0), end-collectMaxBytes)
	nextStart := start
	data, err := io.ReadAll(io.NewSectionReader(file, start, end-start))
	if err != nil {
		item.Text = "The retained log could not be read."
		return item, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		item.Text = "The retained log contains binary data and was omitted."
		return item, nil
	}
	// Do not send a partial line with a credential's field name cut off. A
	// single oversized line is omitted honestly instead of read without a bound.
	if start > 0 {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			nextStart += int64(newline + 1)
			data = data[newline+1:]
		} else {
			data = nil
		}
	}
	if len(data) == 0 {
		nextStart = start
	}
	if request.Offset > 0 && len(data) > 0 {
		if newline := bytes.LastIndexByte(data, '\n'); newline >= 0 {
			data = data[:newline+1]
		} else {
			data = nil
		}
	}
	lines := strings.Split(string(bytes.ToValidUTF8(data, []byte("?"))), "\n")
	lineCount := 0
	for n, line := range lines {
		if strings.TrimSpace(line) != "" {
			lineCount++
		}
		// Structured payloads use the recursive credential-key filter too.
		if json.Valid([]byte(line)) {
			if encoded, err := json.Marshal(redactValue(json.RawMessage(line))); err == nil {
				lines[n] = string(encoded)
			}
		}
	}
	item.Text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, Redact(strings.Join(lines, "\n")))
	item.Status = "collected"
	if start > 0 || len(item.Text) > collectMaxBytes {
		item.Status = "truncated"
	}
	if len(item.Text) > collectMaxBytes {
		item.Text, _ = limitText(item.Text)
	}
	item.Values = map[string]any{"privateContext": true, "offset": request.Offset, "nextOffset": info.Size() - nextStart,
		"offsetKind": "bytes-from-end", "hasMore": start > 0, "readBytes": end - start,
		"modifiedAt": info.ModTime().UTC(), "retainedOnly": true, "lines": lineCount}
	if len(data) == 0 {
		item.Text = "No complete retained log lines were available in this bounded chunk. This does not establish process health."
	}
	return item, ctx.Err()
}

func readProviderLog(ctx context.Context, dataDir string, origin Origin, request ContextRead) (Evidence, error) {
	if request.Path != "" && request.Path != "archive" && request.Path != "journal" {
		return privateContextItem("provider-log"), errors.New("provider logs accept only the journal or archive selector")
	}
	identity := request
	identity.Path = ""
	identity.Offset = 0
	if err := validateRetainedRead(origin, identity); err != nil {
		return privateContextItem("provider-log"), err
	}
	selector := request.Path
	request.Path = ""
	if selector == "archive" {
		item, err := readProviderEvents(ctx, dataDir, origin, request)
		return providerLogCursor(item, "archive"), err
	}
	// ACP hosts retain only this active-prompt journal. They discard stderr.
	// Other drivers can still have the daemon's durable provider-event archive.
	item, err := readRetainedLog(ctx, dataDir, filepath.Join("chat-hosts", origin.SessionID, "acp-prompt.journal"), "provider-log", request)
	// An ACP completion truncates its journal. Try the durable archive only
	// during initial source selection, never reinterpret a later byte cursor.
	if err == nil && selector == "" && request.Offset == 0 && (item.Status == "unavailable" || item.Values["lines"] == 0) {
		item, err = readProviderEvents(ctx, dataDir, origin, request)
		return providerLogCursor(item, "archive"), err
	}
	if item.Values != nil {
		item.Values["storage"] = "selected-session ACP provider journal"
	}
	return providerLogCursor(item, "journal"), err
}

func providerLogCursor(item Evidence, selector string) Evidence {
	if item.Values == nil {
		item.Values = map[string]any{"privateContext": true}
	}
	item.Values["cursor"] = selector
	if selector == "archive" {
		item.Values["offsetKind"] = "records"
	} else {
		item.Values["offsetKind"] = "bytes-from-end"
	}
	if offset, ok := item.Values["nextOffset"].(int64); ok {
		// Path is an exact source selector here, not a filesystem path. It
		// keeps record offsets bound to SQLite even if the journal reappears.
		item.Values["nextRead"] = ContextRead{Source: "provider-log", Path: selector, Offset: offset}
	}
	return item
}
