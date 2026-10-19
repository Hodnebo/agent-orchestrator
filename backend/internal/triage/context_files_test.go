package triage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceContextReadsBoundedChunksAndScrubsCredentials(t *testing.T) {
	root := t.TempDir()
	text := "api_key=private-test-value\n" + strings.Repeat("x", collectMaxBytes)
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := readWorkspaceContext(context.Background(), root, ContextRead{Source: "workspace", Path: "source.txt"})
	if err != nil || item.Status != "truncated" || strings.Contains(item.Text, "private-test-value") || item.Values["nextOffset"] != int64(collectMaxBytes) {
		t.Fatalf("chunk = %#v, %v", item, err)
	}
	item, err = readWorkspaceContext(context.Background(), root, ContextRead{Source: "workspace", Path: "source.txt", Offset: collectMaxBytes})
	if err != nil || item.Status != "collected" || item.Values["bytes"] != len(text)-collectMaxBytes {
		t.Fatalf("next chunk = %#v, %v", item, err)
	}
}

func TestWorkspaceContextRejectsTraversalSensitiveFilesAndAliases(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"source.txt", ".env", "auth.json", "private.pem", "binary.dat"} {
		data := []byte("content")
		if name == "binary.dat" {
			data = []byte{'a', 0, 'b'}
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"../source.txt", filepath.Join(root, "source.txt"), ".env", "auth.json", "private.pem", "binary.dat"} {
		if _, err := readWorkspaceContext(context.Background(), root, ContextRead{Source: "workspace", Path: path}); err == nil {
			t.Errorf("read excluded path %s", path)
		}
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "friendly.txt")); err != nil {
		t.Skip(err)
	}
	if _, err := readWorkspaceContext(context.Background(), root, ContextRead{Source: "workspace", Path: "friendly.txt"}); err == nil {
		t.Fatal("read sensitive-target alias")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkspaceContext(context.Background(), root, ContextRead{Source: "workspace", Path: "outside", List: true}); err == nil {
		t.Fatal("listed escaping alias")
	}
}

func TestWorkspaceContextDirectoryListOmitsSensitiveNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.ts", "b.md", ".env", "auth.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("text"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	item, err := readWorkspaceContext(context.Background(), root, ContextRead{Source: "workspace", List: true})
	if err != nil {
		t.Fatal(err)
	}
	names := item.Values["entries"].([]any)
	if len(names) != 2 || names[0] != "a.ts" || names[1] != "b.md" {
		t.Fatalf("directory names = %#v", names)
	}
}

func TestHookErrorsSelectExactSessionAndRetainLimits(t *testing.T) {
	root := t.TempDir()
	text := "2026-10-19T10:00:00Z session=unrelated error mentions session=affected private-other-data\n" +
		"2026-10-19T10:00:01Z session=affected token=private-token failed\n"
	if err := os.WriteFile(filepath.Join(root, "hooks.log"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	item := readHookErrors(context.Background(), root, "affected")
	if item.Status != "collected" || strings.Contains(item.Text, "private-other-data") || strings.Contains(item.Text, "private-token") || !strings.Contains(item.Text, "failed") {
		t.Fatalf("hook excerpt = %#v", item)
	}
	if err := os.Remove(filepath.Join(root, "hooks.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.log"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "other.log"), filepath.Join(root, "hooks.log")); err != nil {
		t.Skip(err)
	}
	if got := readHookErrors(context.Background(), root, "affected"); got.Status != "unavailable" {
		t.Fatal("read a log alias")
	}
}

func TestLocalContextDoesNotUseRemoteOrCloudSessionIDs(t *testing.T) {
	for _, origin := range []Origin{
		{HostID: "remote", SessionID: "same-id"},
		{SessionID: "same-id", Session: []byte(`{"hostId":"remote"}`)},
		{SessionID: "same-id", Session: []byte(`{"hostId":42}`)},
		{SessionID: "same-id", Session: []byte(`{"cloud":{"orgId":"other"}}`)},
	} {
		if _, err := readDiagnosticContext(context.Background(), t.TempDir(), origin, ContextRead{Source: "session-hook-errors"}); err == nil {
			t.Fatal("remote origin accepted local context")
		}
	}
}

func TestContextRootRejectsAppStateAliasesAndPinsDirectoryIdentity(t *testing.T) {
	data := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(data, link); err != nil {
		t.Skip(err)
	}
	if root, err := pinDiagnosticRoot(link, data); err == nil {
		root.Close()
		t.Fatal("workspace alias exposed app state")
	}
	if root, err := pinDiagnosticRoot(data, data); err == nil {
		root.Close()
		t.Fatal("app state used as project context")
	}
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("original project"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := pinDiagnosticRoot(workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(workspace, filepath.Join(parent, "preserved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("replacement private state"), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := readWorkspaceContextRoot(context.Background(), root, ContextRead{Source: "workspace", Path: "a.txt"})
	if err != nil || item.Text != "original project" {
		t.Fatalf("root was resolved again after pinning: %#v %v", item, err)
	}
}

func TestContextFileErrorsDoNotExposeCustomAbsoluteRoots(t *testing.T) {
	item := contextReadError(ContextRead{Source: "workspace", Path: "a.ts"}, &os.PathError{Op: "open", Path: "/opt/private-root/a.ts", Err: os.ErrPermission})
	if strings.Contains(item.Text, "/opt/private-root") || !strings.Contains(item.Text, "could not be read") {
		t.Fatalf("private root leaked in error: %#v", item)
	}
}
