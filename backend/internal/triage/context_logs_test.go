package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedDaemonLogHasBoundedTailChunksAndRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	line := "retained diagnostic event " + strings.Repeat("x", 200) + "\n"
	log := strings.Repeat(line, 800) + "preview connection refused api_key=daemon-secret\n" + `{"credentials":{"custom":"nested-log-secret"},"error":"save failed"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "daemon.log"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := readDiagnosticContext(context.Background(), dir, Origin{}, ContextRead{Source: "daemon-log"})
	if err != nil || item.Status != "truncated" || len(item.Text) > collectMaxBytes || item.Values["readBytes"] != int64(collectMaxBytes) || item.Values["offsetKind"] != "bytes-from-end" {
		t.Fatalf("daemon log chunk = %#v %v", item, err)
	}
	if !strings.Contains(item.Text, "connection refused") || !strings.Contains(item.Text, "save failed") || strings.Contains(item.Text, "daemon-secret") || strings.Contains(item.Text, "nested-log-secret") {
		t.Fatal("retained log lost useful error text or included credentials")
	}
	readLines := strings.Count(item.Text, "retained diagnostic event")
	item, err = readDiagnosticContext(context.Background(), dir, Origin{}, ContextRead{Source: "daemon-log", Offset: item.Values["nextOffset"].(int64)})
	if err != nil || len(item.Text) > collectMaxBytes || item.Values["nextOffset"].(int64) <= collectMaxBytes {
		t.Fatalf("older daemon log chunk = %#v %v", item, err)
	}
	readLines += strings.Count(item.Text, "retained diagnostic event")
	for chunk := 0; item.Values["hasMore"] == true && chunk < 5; chunk++ {
		item, err = readDiagnosticContext(context.Background(), dir, Origin{}, ContextRead{Source: "daemon-log", Offset: item.Values["nextOffset"].(int64)})
		if err != nil {
			t.Fatal(err)
		}
		readLines += strings.Count(item.Text, "retained diagnostic event")
	}
	if readLines != 800 || item.Values["hasMore"] != false {
		t.Fatalf("tail cursor skipped or repeated complete log lines: count=%d more=%v", readLines, item.Values["hasMore"])
	}
	for _, request := range []ContextRead{{Source: "daemon-log", Path: "credentials.json"}, {Source: "daemon-log", List: true}, {Source: "daemon-log", Offset: -1}, {Source: "daemon-log", Offset: 17 << 20}} {
		if _, err := readDiagnosticContext(context.Background(), dir, Origin{}, request); err == nil {
			t.Fatalf("invalid retained log request was accepted: %#v", request)
		}
	}
}

func TestProviderJournalUsesExactSelectedSessionAndFixedConfinedPath(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"selected", "other"} {
		host := filepath.Join(dir, "chat-hosts", id)
		if err := os.MkdirAll(host, 0o700); err != nil {
			t.Fatal(err)
		}
		text := `{"method":"tool.failed","params":{"message":"` + id + ` failure","token":"journal-secret"}}` + "\n"
		if err := os.WriteFile(filepath.Join(host, "acp-prompt.journal"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	item, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: "selected"}, ContextRead{Source: "provider-log"})
	if err != nil || item.Status != "collected" || !strings.Contains(item.Text, "selected failure") || strings.Contains(item.Text, "other failure") || strings.Contains(item.Text, "journal-secret") {
		t.Fatalf("provider journal = %#v %v", item, err)
	}
	encoded, _ := json.Marshal(item)
	if !strings.Contains(string(encoded), `"privateContext":true`) || !strings.Contains(string(encoded), "ACP provider journal") {
		t.Fatalf("provider provenance missing: %s", encoded)
	}
	for _, id := range []string{"../other", "other/../selected", "selected\\peer"} {
		if _, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: id}, ContextRead{Source: "provider-log"}); err == nil {
			t.Fatalf("provider journal accepted invalid identity %q", id)
		}
	}
}

func TestRetainedLogsRejectFileAndDirectoryAliasesAndBinaryData(t *testing.T) {
	for _, kind := range []string{"file", "directory", "root", "binary"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "daemon.log"), []byte("private outside log"), 0o600); err != nil {
				t.Fatal(err)
			}
			source := "daemon-log"
			origin := Origin{}
			switch kind {
			case "file":
				if err := os.Symlink(filepath.Join(outside, "daemon.log"), filepath.Join(dir, "daemon.log")); err != nil {
					t.Skip(err)
				}
			case "directory":
				if err := os.Symlink(outside, filepath.Join(dir, "chat-hosts")); err != nil {
					t.Skip(err)
				}
				source, origin = "provider-log", Origin{SessionID: "selected"}
			case "root":
				alias := filepath.Join(dir, "data-alias")
				if err := os.Symlink(outside, alias); err != nil {
					t.Skip(err)
				}
				dir = alias
			case "binary":
				if err := os.WriteFile(filepath.Join(dir, "daemon.log"), []byte{'x', 0, 'z'}, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			item, err := readDiagnosticContext(context.Background(), dir, origin, ContextRead{Source: source})
			if err != nil || item.Status != "unavailable" || strings.Contains(item.Text, "private outside log") {
				t.Fatalf("unsafe retained log read = %#v %v", item, err)
			}
		})
	}
}

func TestRetainedLogOmitsOversizedPartialCredentialLine(t *testing.T) {
	dir := t.TempDir()
	text := "api_key=" + strings.Repeat("secret", collectMaxBytes) + "\nconnection refused\n"
	if err := os.WriteFile(filepath.Join(dir, "daemon.log"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := readDiagnosticContext(context.Background(), dir, Origin{}, ContextRead{Source: "daemon-log"})
	if err != nil || item.Status != "truncated" || strings.Contains(item.Text, "secret") || !strings.Contains(item.Text, "connection refused") {
		t.Fatalf("partial credential line was exposed: %#v %v", item, err)
	}
}

func TestProviderLogEmptyJournalFallsBackToArchiveWithStableRecordCursor(t *testing.T) {
	dir, id, _, _, raw := retainedContextFixture(t)
	seedDiagnosticHistory(t, raw, id)
	for n := range 8 {
		if _, err := raw.Exec(`INSERT INTO conversation_provider_events
(conversation_id,session_id,method,payload_json,received_at)
VALUES ('private-history',?,'tool.failed',?,CURRENT_TIMESTAMP)`, id, fmt.Sprintf(`{"message":"archived failure %d"}`, n)); err != nil {
			t.Fatal(err)
		}
	}
	journal := filepath.Join(dir, "chat-hosts", string(id), "acp-prompt.journal")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	origin := Origin{SessionID: string(id)}
	item, err := readDiagnosticContext(context.Background(), dir, origin, ContextRead{Source: "provider-log"})
	if err != nil || item.Status != "truncated" || item.Values["cursor"] != "archive" || item.Values["offsetKind"] != "records" || item.Values["nextOffset"] != int64(6) {
		t.Fatalf("empty journal hid the archive: %#v %v", item, err)
	}
	encoded, _ := json.Marshal(item)
	if !strings.Contains(string(encoded), "archived failure 7") || strings.Contains(string(encoded), "provider-secret") {
		t.Fatalf("archive content missing or unredacted: %s", encoded)
	}
	request, ok := item.Values["nextRead"].(ContextRead)
	if !ok || request.Source != "provider-log" || request.Path != "archive" || request.Offset != 6 {
		t.Fatalf("archive cursor has no explicit identity: %#v", item.Values["nextRead"])
	}
	if err := os.WriteFile(journal, []byte("new journal entry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err = readDiagnosticContext(context.Background(), dir, origin, request)
	if err != nil || item.Status != "collected" || item.Values["cursor"] != "archive" || item.Values["offsetKind"] != "records" || item.Values["offset"] != int64(6) || item.Values["nextOffset"] != int64(9) {
		t.Fatalf("archive cursor jumped into the reappearing journal: %#v %v", item, err)
	}
	encoded, _ = json.Marshal(item)
	if !strings.Contains(string(encoded), "archived failure 1") || strings.Contains(string(encoded), "archived failure 7") || strings.Contains(string(encoded), "new journal entry") {
		t.Fatalf("archive cursor skipped or repeated records: %s", encoded)
	}
}

func TestProviderLogLaterJournalPagesDoNotSwitchToArchive(t *testing.T) {
	dir, id, _, _, raw := retainedContextFixture(t)
	seedDiagnosticHistory(t, raw, id)
	journal := filepath.Join(dir, "chat-hosts", string(id), "acp-prompt.journal")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, request := range []ContextRead{
		{Source: "provider-log", Offset: 1},
		{Source: "provider-log", Path: "journal"},
		{Source: "provider-log", Path: "journal", Offset: 1},
	} {
		item, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: string(id)}, request)
		if err != nil || item.Values["cursor"] != "journal" || item.Values["offsetKind"] != "bytes-from-end" || item.Values["entries"] != nil {
			t.Fatalf("later journal cursor switched to archive records: %#v %v", item, err)
		}
	}
}

func TestProviderLogFirstChunkWithoutCompleteJournalEntryUsesArchive(t *testing.T) {
	dir, id, _, _, raw := retainedContextFixture(t)
	seedDiagnosticHistory(t, raw, id)
	journal := filepath.Join(dir, "chat-hosts", string(id), "acp-prompt.journal")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte(strings.Repeat("x", collectMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: string(id)}, ContextRead{Source: "provider-log"})
	if err != nil || item.Status != "collected" || item.Values["cursor"] != "archive" || len(item.Values["entries"].([]any)) != 1 {
		t.Fatalf("empty first journal chunk hid the archive: %#v %v", item, err)
	}
}
