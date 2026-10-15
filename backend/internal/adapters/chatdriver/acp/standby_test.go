package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func standbyFixture(t *testing.T) (Config, ports.ChatStartConfig, string) {
	t.Helper()
	callsPath := filepath.Join(t.TempDir(), "calls.log")
	cfg, start := restartFixture(t, map[string]string{"AO_TEST_PERSISTENT_ACP_CALLS": callsPath})
	cfg.Standby = &StandbyConfig{Warm: func(ctx context.Context, request RawRequest) error {
		return request(ctx, "cursor/list_available_models", map[string]any{})
	}}
	start.Env = map[string]string{"AO_TEST_ENV": "1"}
	start.SystemPrompt = "standing"
	t.Cleanup(func() {
		_ = persistenthost.Shutdown(context.Background(), start.DataDir, string(start.SessionID))
		_ = persistenthost.Shutdown(context.Background(), start.DataDir, persistenthost.StandbyKey(string(start.SessionID)))
	})
	return cfg, start, callsPath
}

func providerCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read provider calls: %v", err)
	}
	return strings.Fields(string(data))
}

func countCalls(calls []string, method string) int {
	n := 0
	for _, call := range calls {
		if call == method {
			n++
		}
	}
	return n
}

func hostDescriptorExists(dataDir, key string) bool {
	_, err := os.Stat(filepath.Join(dataDir, "chat-hosts", key, "host.json"))
	return err == nil
}

func TestStartAdoptsMatchingStandby(t *testing.T) {
	cfg, start, callsPath := standbyFixture(t)
	driver := New(cfg, nil)
	prewarm := start
	prewarm.ProviderScopeID = ""
	standby, err := driver.Prewarm(context.Background(), prewarm)
	if err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	if !hostDescriptorExists(start.DataDir, persistenthost.StandbyKey(string(start.SessionID))) {
		t.Fatal("standby host is not under the standby key")
	}

	start.Standby = standby
	start.SystemPrompt = "standing\n<ao-continuation>handoff</ao-continuation>"
	conv, err := driver.Start(context.Background(), start)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = conv.(ports.ChatProviderTerminator).Terminate() }()

	calls := providerCalls(t, callsPath)
	if got := countCalls(calls, "initialize"); got != 1 {
		t.Fatalf("initialize calls = %d (%v), want the standby's only", got, calls)
	}
	if countCalls(calls, "cursor/list_available_models") != 1 || countCalls(calls, "session/new") != 1 {
		t.Fatalf("provider calls = %v, want one warm-up and one session/new", calls)
	}
	if hostDescriptorExists(start.DataDir, persistenthost.StandbyKey(string(start.SessionID))) {
		t.Fatal("standby key still published after adoption")
	}
	if !hostDescriptorExists(start.DataDir, string(start.SessionID)) {
		t.Fatal("adopted host is not under the session key")
	}
	if got := conv.(*conversation).providerScopeID; got != start.ProviderScopeID {
		t.Fatalf("provider scope = %q, want the adopting start's %q", got, start.ProviderScopeID)
	}
	// Adoption is final: a late Discard must not stop the adopted provider.
	standby.Discard()
	_ = nextEvent(t, conv.Events())
	completedProviderPID(t, conv)
}

func TestStartDiscardsStandbyWithChangedInputs(t *testing.T) {
	cfg, start, callsPath := standbyFixture(t)
	driver := New(cfg, nil)
	standby, err := driver.Prewarm(context.Background(), start)
	if err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	start.Standby = standby
	start.Model = "another-model"
	conv, err := driver.Start(context.Background(), start)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = conv.(ports.ChatProviderTerminator).Terminate() }()
	if got := countCalls(providerCalls(t, callsPath), "initialize"); got != 2 {
		t.Fatalf("initialize calls = %d, want a fresh provider after the mismatch", got)
	}
	waitHostGone(t, start.DataDir, persistenthost.StandbyKey(string(start.SessionID)))
}

func TestStandbyDiscardStopsItsHost(t *testing.T) {
	cfg, start, _ := standbyFixture(t)
	standby, err := New(cfg, nil).Prewarm(context.Background(), start)
	if err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	standby.Discard()
	standby.Discard()
	waitHostGone(t, start.DataDir, persistenthost.StandbyKey(string(start.SessionID)))
}

func TestPrewarmRequiresStandbySupport(t *testing.T) {
	cfg, start, _ := standbyFixture(t)
	cfg.Standby = nil
	if _, err := New(cfg, nil).Prewarm(context.Background(), start); !errors.Is(err, ports.ErrChatUnsupported) {
		t.Fatalf("Prewarm without standby support = %v, want ErrChatUnsupported", err)
	}
}

func TestContinuationPreambleExtractsHandoffBlock(t *testing.T) {
	blocks := ContinuationPreamble("standing\n<ao-continuation kind=\"switch\">facts</ao-continuation>\n")
	if len(blocks) != 1 || blocks[0].Text == nil || blocks[0].Text.Text != `<ao-continuation kind="switch">facts</ao-continuation>` {
		t.Fatalf("preamble = %#v", blocks)
	}
	if ContinuationPreamble("standing only") != nil {
		t.Fatal("a prompt without a handoff produced a preamble")
	}
}

func waitHostGone(t *testing.T, dataDir, key string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hostDescriptorExists(dataDir, key) {
		if time.Now().After(deadline) {
			t.Fatalf("host %s still running", key)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
