package persistenthost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type codexDiscardWriter struct{}

func (codexDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (codexDiscardWriter) Close() error                { return nil }

func codexTestHost(t *testing.T) *host {
	t.Helper()
	relay, err := newCodexRelay(context.Background(), filepath.Join(t.TempDir(), "events.journal"))
	if err != nil {
		t.Fatal(err)
	}
	h := &host{ctx: context.Background(), codex: relay, token: "private-test-token", stdin: codexDiscardWriter{}, pendingRequests: make(map[string]*pendingRequest)}
	h.cond = sync.NewCond(&h.mu)
	t.Cleanup(func() { h.stopCodex(); _ = h.codex.journal.close(context.Background()) })
	return h
}

func codexTestAttach(t *testing.T, h *host) (net.Conn, *bufio.Reader, helloResponse) {
	t.Helper()
	server, client := net.Pipe()
	go h.handle(server)
	t.Cleanup(func() { _ = client.Close() })
	if err := json.NewEncoder(client).Encode(hello{Version: ProtocolVersion, Token: h.token, Action: "attach", CodexReplay: true}); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response helloResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || !response.CodexReplay {
		t.Fatalf("handshake=%s", line)
	}
	return client, reader, response
}

func codexTestControl(t *testing.T, conn net.Conn, method, id string) {
	t.Helper()
	if err := json.NewEncoder(conn).Encode(map[string]any{"method": method, "params": map[string]string{"eventId": id}}); err != nil {
		t.Fatal(err)
	}
}

func codexTestRead(t *testing.T, reader *bufio.Reader) CodexEvent {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Method string     `json:"method"`
		Params CodexEvent `json:"params"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Method != CodexEventMethod || envelope.Params.EventID == "" {
		t.Fatalf("not retained event: %s", line)
	}
	return envelope.Params
}

func codexTestFrame(t *testing.T, h *host, frame string) *codexEntry {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.codexProviderFrameLocked([]byte(frame + "\n")); err != nil {
		t.Fatal(err)
	}
	if len(h.codex.entries) == 0 {
		return nil
	}
	return h.codex.entries[len(h.codex.entries)-1]
}

func codexAwait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("host did not reach expected state")
}

func TestCodexHostRetainsDeliveredCompletionUntilCommitReceipt(t *testing.T) {
	h := codexTestHost(t)
	first, reader, _ := codexTestAttach(t, h)
	codexTestControl(t, first, CodexReplayMethod, "")
	frames := []string{
		`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"one"}}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"threadId":"root","turnId":"one","delta":"once"}}`,
		`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"one","status":"completed"}}}`,
	}
	var original []CodexEvent
	for _, frame := range frames {
		codexTestFrame(t, h, frame)
		original = append(original, codexTestRead(t, reader))
	}
	// Socket delivery is not a receipt. Simulate daemon death before commit.
	_ = first.Close()
	codexAwait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.client == nil })
	second, replay, response := codexTestAttach(t, h)
	if len(response.CodexState.ActiveTurns) != 0 {
		t.Fatalf("completed turn remained active: %+v", response.CodexState)
	}
	_ = second.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := replay.ReadByte(); err == nil {
		t.Fatal("replay escaped activation gate")
	}
	_ = second.SetReadDeadline(time.Time{})
	codexTestControl(t, second, CodexReplayMethod, "")
	for i := range original {
		event := codexTestRead(t, replay)
		if event.EventID != original[i].EventID || !event.Replayed || !bytes.Equal(event.Frame, original[i].Frame) || event.ObservedAt.IsZero() || !event.ObservedAt.Equal(original[i].ObservedAt) {
			t.Fatalf("replay changed event: %+v vs %+v", event, original[i])
		}
		codexTestControl(t, second, CodexAckMethod, event.EventID)
	}
	codexAwait(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.codex.byID) == 0 && h.codex.journal.size == 0
	})
}

func TestCodexJournalAcknowledgmentKeepsNormalizerPrefixAndChildOwnership(t *testing.T) {
	h := codexTestHost(t)
	root := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"root","turn":{"id":"same"}}}`)
	child := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"same"}}}`)
	delta := codexTestFrame(t, h, `{"method":"item/agentMessage/delta","params":{"threadId":"root","turnId":"same","delta":"partial"}}`)
	terminal := codexTestFrame(t, h, `{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"same"}}}`)
	r := h.codex
	if err := r.acknowledge(h.ctx, "another-host:1"); err != nil {
		t.Fatal(err)
	}
	if err := r.acknowledge(h.ctx, terminal.id); err != nil {
		t.Fatal(err)
	}
	if len(r.byID) != 4 {
		t.Fatal("terminal ACK discarded uncommitted prefix")
	}
	for _, entry := range []*codexEntry{root, child} {
		if err := r.acknowledge(h.ctx, entry.id); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.byID) != 4 {
		t.Fatal("ACKed prefix removed before whole turn committed")
	}
	if err := r.acknowledge(h.ctx, delta.id); err != nil {
		t.Fatal(err)
	}
	if len(r.byID) != 1 || r.byID[child.id] == nil || r.active["child"] != "same" {
		t.Fatalf("root receipt consumed child: %+v %+v", r.byID, r.active)
	}
	if err := r.acknowledge(h.ctx, terminal.id); err != nil {
		t.Fatal(err)
	}
	if len(r.byID) != 1 {
		t.Fatal("duplicate old ACK consumed successor")
	}
}

func TestCodexSnapshotKeepsRetainedTurnScopesAfterNativeCompletion(t *testing.T) {
	h := codexTestHost(t)
	rootStart := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"root:α","turn":{"id":"root-turn"}}}`)
	childStart := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"child-turn"}}}`)
	rootDone := codexTestFrame(t, h, `{"method":"turn/completed","params":{"threadId":"root:α","turn":{"id":"root-turn"}}}`)
	childDone := codexTestFrame(t, h, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"child-turn"}}}`)
	snapshot := h.codex.snapshot()
	want := map[string][]string{"root:α": {"root-turn"}, "child": {"child-turn"}}
	if len(snapshot.ActiveTurns) != 0 || !reflect.DeepEqual(snapshot.RetainedTurns, want) {
		t.Fatalf("completed native scopes lost: %+v", snapshot)
	}
	// The attachment snapshot is independent of subsequent receipts and callers.
	snapshot.RetainedTurns["root:α"][0] = "mutated"
	for _, entry := range []*codexEntry{rootStart, rootDone} {
		if err := h.codex.acknowledge(h.ctx, entry.id); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.codex.snapshot(); !reflect.DeepEqual(got.RetainedTurns, map[string][]string{"child": {"child-turn"}}) {
		t.Fatalf("root receipt affected child scope: %+v", got)
	}
	for _, entry := range []*codexEntry{childStart, childDone} {
		if err := h.codex.acknowledge(h.ctx, entry.id); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.codex.snapshot(); len(got.RetainedTurns) != 0 {
		t.Fatalf("ACKed historical scopes retained: %+v", got)
	}
}

func TestCodexLateOutputAfterTerminalReceiptDoesNotRetainAnotherPrefix(t *testing.T) {
	h := codexTestHost(t)
	r := h.codex
	start := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"root","turn":{"id":"old"}}}`)
	terminal := codexTestFrame(t, h, `{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"old"}}}`)
	for _, entry := range []*codexEntry{start, terminal} {
		if err := r.acknowledge(h.ctx, entry.id); err != nil {
			t.Fatal(err)
		}
	}
	successor := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"root","turn":{"id":"current"}}}`)
	if err := r.acknowledge(h.ctx, successor.id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		late := codexTestFrame(t, h, fmt.Sprintf(`{"method":"item/commandExecution/outputDelta","params":{"threadId":"root","turnId":"old-%d","delta":"late"}}`, i))
		if late.turn.threadID != "" {
			t.Fatalf("late output created an unterminated prefix: %+v", late)
		}
		if err := r.acknowledge(h.ctx, late.id); err != nil {
			t.Fatal(err)
		}
		if len(r.turns) != 1 || len(r.byID) != 1 || r.active["root"] != "current" {
			t.Fatal("late receipt retained output or affected the successor")
		}
	}
	// Also exercise the exact formerly retained turn after its terminal ACK.
	late := codexTestFrame(t, h, `{"method":"item/agentMessage/delta","params":{"threadId":"root","turnId":"old","delta":"late"}}`)
	if late.turn.threadID != "" {
		t.Fatal("settled turn reopened its host journal")
	}
	if err := r.acknowledge(h.ctx, late.id); err != nil {
		t.Fatal(err)
	}
	done := codexTestFrame(t, h, `{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"current"}}}`)
	if err := r.acknowledge(h.ctx, done.id); err != nil {
		t.Fatal(err)
	}
	if len(r.turns) != 0 || len(r.entries) != 0 || len(r.byID) != 0 || r.journal.size != 0 {
		t.Fatal("late output prevented complete journal reclamation")
	}
}

func TestCodexJournalCompactsCompletedTurnsAndRejectsQuotaWithoutEviction(t *testing.T) {
	h := codexTestHost(t)
	r := h.codex
	r.limit = 2048
	childStart := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"background"}}}`)
	childOutput := codexTestFrame(t, h, fmt.Sprintf(`{"method":"item/commandExecution/outputDelta","params":{"threadId":"child","turnId":"background","delta":%q}}`, strings.Repeat("c", 400)))
	for _, entry := range []*codexEntry{childStart, childOutput} {
		if err := r.acknowledge(h.ctx, entry.id); err != nil {
			t.Fatal(err)
		}
	}
	childSize := r.journal.size
	childBytes, err := os.ReadFile(r.journal.path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		start := codexTestFrame(t, h, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"turn-%d"}}}`, i))
		output := codexTestFrame(t, h, fmt.Sprintf(`{"method":"item/commandExecution/outputDelta","params":{"threadId":"root","turnId":"turn-%d","delta":%q}}`, i, strings.Repeat("x", 800)))
		terminal := codexTestFrame(t, h, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"turn-%d"}}}`, i))
		for _, entry := range []*codexEntry{start, output, terminal} {
			if err := r.acknowledge(h.ctx, entry.id); err != nil {
				t.Fatal(err)
			}
		}
		if r.journal.size != childSize || len(r.entries) != 2 {
			t.Fatalf("completed stream filled journal after turn %d", i)
		}
		retained, err := os.ReadFile(r.journal.path)
		if err != nil || !bytes.Equal(retained, childBytes) {
			t.Fatalf("compaction changed child prefix: %v", err)
		}
	}
	childTerminal := codexTestFrame(t, h, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"background"}}}`)
	if err := r.acknowledge(h.ctx, childTerminal.id); err != nil {
		t.Fatal(err)
	}
	if r.journal.size != 0 || len(r.byID) != 0 {
		t.Fatal("settled child journal was not reset")
	}
	entry := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"root","turn":{"id":"too-large"}}}`)
	before, err := os.ReadFile(r.journal.path)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	err = h.codexProviderFrameLocked([]byte(fmt.Sprintf(`{"method":"item/agentMessage/delta","params":{"threadId":"root","turnId":"too-large","delta":%q}}`, strings.Repeat("x", 2200))))
	h.mu.Unlock()
	if !errors.Is(err, errJournalFull) {
		t.Fatalf("quota error=%v", err)
	}
	after, err := os.ReadFile(r.journal.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(r.byID) != 1 || r.byID[entry.id].acked {
		t.Fatal("failed append evicted or acknowledged retained output")
	}
}

func TestCodexJournalExcludesPrivateAccountTraffic(t *testing.T) {
	h := codexTestHost(t)
	for _, method := range []string{"account/updated", "account/rateLimits/updated", "account/login/completed", "auth/refresh", "mcpServerStatus/updated"} {
		codexTestFrame(t, h, fmt.Sprintf(`{"method":%q,"params":{"token":"secret","email":"private@example.com"}}`, method))
	}
	// Auth-refresh requests remain only in the existing in-memory pending ledger.
	codexTestFrame(t, h, `{"id":8,"method":"account/chatgptAuthTokens/refresh","params":{"accessToken":"secret"}}`)
	bytes, err := os.ReadFile(h.codex.journal.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) != 0 || len(h.codex.byID) != 0 {
		t.Fatalf("private traffic was journaled: %s", bytes)
	}
	if h.pendingRequests["8"] == nil {
		t.Fatal("pending request lost")
	}
}

func TestCodexLargeReplayDoesNotBlockControlReader(t *testing.T) {
	h := codexTestHost(t)
	start := codexTestFrame(t, h, `{"method":"turn/started","params":{"threadId":"root","turn":{"id":"large"}}}`)
	for i := 0; i < 128; i++ {
		codexTestFrame(t, h, fmt.Sprintf(`{"method":"item/commandExecution/outputDelta","params":{"threadId":"root","turnId":"large","delta":%q}}`, strings.Repeat("x", 8192)))
	}
	conn, _, response := codexTestAttach(t, h)
	if response.CodexState.ActiveTurns["root"] != "large" {
		t.Fatal("active snapshot lost")
	}
	codexTestControl(t, conn, CodexReplayMethod, "")
	// The attachment writer is now blocked on net.Pipe's unread replay. The
	// independent reader must still accept a receipt instead of deadlocking.
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	codexTestControl(t, conn, CodexAckMethod, start.id)
	codexAwait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return start.acked })
}

func TestCodexCapabilityRejectsOldClientAndAdoptsOnlyLegacyRaw(t *testing.T) {
	h := codexTestHost(t)
	server, client := net.Pipe()
	defer client.Close()
	go h.handle(server)
	_ = json.NewEncoder(client).Encode(hello{Version: ProtocolVersion, Token: h.token, Action: "attach"})
	var response helloResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error != ErrIncompatible.Error() {
		t.Fatalf("old client accepted: %+v", response)
	}
	for _, tc := range []struct {
		cfg      Protocol
		existing Protocol
		ok       bool
	}{{ProtocolCodex, ProtocolRaw, true}, {ProtocolRaw, ProtocolCodex, false}, {ProtocolCodex, ProtocolACP, false}, {ProtocolCodex, ProtocolCodex, true}} {
		err := validateDescriptor(Config{Protocol: tc.cfg, OwnershipFingerprint: "stable"}, Descriptor{Protocol: tc.existing, OwnershipFingerprint: "stable"})
		if (err == nil) != tc.ok {
			t.Fatalf("profile %q -> %q: %v", tc.cfg, tc.existing, err)
		}
	}
	if err := validateDescriptor(Config{Protocol: ProtocolCodex, OwnershipFingerprint: "new"}, Descriptor{Protocol: ProtocolCodex, OwnershipFingerprint: "old"}); !errors.Is(err, ErrIncompatible) {
		t.Fatal("changed ownership accepted")
	}
}

func TestCodexConnectOrStartAdoptsLiveLegacyProviderWithoutReplay(t *testing.T) {
	cfg := Config{
		SessionID: "codex-legacy-migration", DataDir: t.TempDir(), Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	descriptor := awaitDescriptor(t, cfg.DataDir, cfg.SessionID)
	t.Cleanup(func() { _ = Shutdown(context.Background(), cfg.DataDir, cfg.SessionID); <-done })
	first := awaitAttach(t, descriptor)
	sendFrame(t, first, `{"id":5,"method":"pid"}`)
	before := readFrame(t, bufio.NewReader(first.Stdout))
	_ = first.Stdin.Close()
	cfg.Protocol = ProtocolCodex
	cfg.OwnershipFingerprint = "new-profile-fingerprint"
	cfg.ReconnectOnly = true
	after, err := ConnectOrStart(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Stdin.Close()
	if !after.Reconnected || after.CodexReplay || after.CodexState != nil {
		t.Fatalf("legacy capability misrepresented: %+v", after)
	}
	sendFrame(t, after, `{"id":6,"method":"pid"}`)
	response := readFrame(t, bufio.NewReader(after.Stdout))
	var oldReply, newReply struct {
		Result struct {
			PID int `json:"pid"`
		} `json:"result"`
	}
	if json.Unmarshal(before, &oldReply) != nil || json.Unmarshal(response, &newReply) != nil || oldReply.Result.PID == 0 || oldReply.Result.PID != newReply.Result.PID {
		t.Fatalf("provider was replaced: %s -> %s", before, response)
	}
}

var _ io.WriteCloser = codexDiscardWriter{}
