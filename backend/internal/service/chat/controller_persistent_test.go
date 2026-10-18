package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	sqlite "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type receiptConversation struct {
	*terminatingConversation
	ack     func(string) error
	eventMu sync.Mutex
}

func (c *receiptConversation) Close() error {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	return c.fakeConversation.Close()
}

func (c *receiptConversation) emit(events ...ports.ChatEvent) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	c.fakeConversation.emit(events...)
}

func (c *receiptConversation) AcknowledgeProviderEvent(_ context.Context, id string) error {
	return c.ack(id)
}

type flakyReceiptStore struct {
	chatsvc.Store
	failures atomic.Int32 // -1 fails permanently; positive counts transient failures.
	attempts atomic.Int32
}

func (s *flakyReceiptStore) ProjectProviderEvent(
	ctx context.Context, conversationID string, session domain.SessionID,
	generation, providerEventID, method, payloadJSON string, now time.Time,
	project func(context.Context) error,
) (bool, error) {
	return s.Store.ProjectProviderEvent(ctx, conversationID, session, generation,
		providerEventID, method, payloadJSON, now, func(txCtx context.Context) error {
			if err := project(txCtx); err != nil {
				return err
			}
			if method == string(ports.ChatEventMessageDelta) {
				s.attempts.Add(1)
			}
			if method == string(ports.ChatEventMessageDelta) && s.failures.Load() != 0 {
				if s.failures.Load() > 0 {
					s.failures.Add(-1)
				}
				return errors.New("injected event transaction rollback")
			}
			return nil
		})
}

func TestPersistentProjectionNeverAcknowledgesPastFailedOutput(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failures      int32
		sourceReceipt bool
	}{
		{"legacy transient failure", 1, false},
		{"legacy permanent failure", -1, false},
		{"source receipt transient failure", 1, true},
		{"source receipt permanent failure", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputReceipt, completionReceipt := "", ""
			wantOutput, wantCompletion := "output", "completion"
			if tc.sourceReceipt {
				outputReceipt, completionReceipt = "output-frame", "completion-frame"
				wantOutput, wantCompletion = outputReceipt, completionReceipt
			}
			acknowledged := make(chan string, 4)
			conv := &receiptConversation{
				terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
				ack:                     func(id string) error { acknowledged <- id; return nil },
			}
			var flaky *flakyReceiptStore
			h := newHarnessWithConversationAndStore(t, conv, func(st *sqlite.Store) chatsvc.Store {
				flaky = &flakyReceiptStore{Store: st}
				flaky.failures.Store(tc.failures)
				return flaky
			})
			turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "first"})
			if err != nil {
				t.Fatal(err)
			}
			conv.emit(
				ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID},
				ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "answer", ProviderEventID: "output", ProviderReceiptID: outputReceipt, Delta: "retained answer"},
				ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, ProviderEventID: "completion", ProviderReceiptID: completionReceipt, TurnState: domain.TurnStateCompleted},
			)
			if tc.failures > 0 {
				for _, want := range []string{wantOutput, wantCompletion} {
					select {
					case got := <-acknowledged:
						if got != want {
							t.Fatalf("ack %q, want %q", got, want)
						}
					case <-time.After(3 * time.Second):
						t.Fatalf("missing ack %s", want)
					}
				}
				snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
					return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
				})
				if len(snapshot.Messages) != 2 || snapshot.Messages[1].Text != "retained answer" {
					t.Fatalf("acknowledged without exactly-once output: %+v", snapshot.Messages)
				}
				if flaky.attempts.Load() != 2 {
					t.Fatalf("transient projection attempts = %d, want 2", flaky.attempts.Load())
				}
				return
			}
			done := make(chan struct{})
			go func() { h.ctrl.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("failed projection did not detach")
			}
			select {
			case id := <-acknowledged:
				t.Fatalf("acknowledged past failed output: %s", id)
			default:
			}
			if flaky.attempts.Load() != 4 {
				t.Fatalf("permanent projection attempts = %d, want initial plus three retries", flaky.attempts.Load())
			}
			if conv.terminated.Load() {
				t.Fatal("projection failure destroyed replay owner")
			}
			snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
			if err != nil || len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateRunning {
				t.Fatalf("failed projection falsely settled turn: %+v, %v", snapshot.Turns, err)
			}
			if err := h.svc.Stop(context.Background(), testSession); err != nil || h.hostStops.Load() != 1 {
				t.Fatalf("explicit stop after failed attachment did not destroy host: calls=%d err=%v", h.hostStops.Load(), err)
			}
		})
	}
}

func TestPersistentCompletionIsAcknowledgedBeforeQueuedTurnDispatch(t *testing.T) {
	for _, ackFails := range []bool{false, true} {
		name := "acknowledged"
		if ackFails {
			name = "ack failure retains queue"
		}
		t.Run(name, func(t *testing.T) {
			var acknowledged, dispatchedBeforeAck atomic.Bool
			conv := &receiptConversation{
				terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
				ack: func(id string) error {
					if id == "completion" {
						if ackFails {
							return errors.New("connection lost before ack")
						}
						acknowledged.Store(true)
					}
					return nil
				},
			}
			conv.onSend = func(id string) {
				if id == "provider-turn-2" && !acknowledged.Load() {
					dispatchedBeforeAck.Store(true)
				}
			}
			h := newHarnessWithConversation(t, conv)
			ctx := context.Background()
			turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "first"})
			if err != nil {
				t.Fatal(err)
			}
			conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
			})
			queued, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "second"})
			if err != nil || queued.State != domain.TurnStateQueued {
				t.Fatalf("queue: %+v, %v", queued, err)
			}
			conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, ProviderEventID: "completion", TurnState: domain.TurnStateCompleted})
			if ackFails {
				done := make(chan struct{})
				go func() { h.ctrl.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("ack failure did not detach")
				}
				if len(conv.sentTexts()) != 1 || conv.terminated.Load() {
					t.Fatal("ack failure dispatched queued work or destroyed provider")
				}
			} else {
				h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
					return turnStateByText(t, s)["second"] == domain.TurnStateRunning
				})
			}
			if dispatchedBeforeAck.Load() {
				t.Fatal("queued turn dispatched before previous completion was acknowledged")
			}
		})
	}
}

func TestProviderPreservationReportRequiresLiveController(t *testing.T) {
	provider := &terminatingConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, provider)
	if !h.svc.PreservesProviderOnRestart(testSession) || h.svc.PreservesProviderOnRestart("missing") {
		t.Fatal("preservation report does not match established ownership")
	}
	h.svc.StopAll(context.Background())
	if h.hostStops.Load() != 0 {
		t.Fatal("daemon shutdown destroyed persistent ownership")
	}
	if h.svc.PreservesProviderOnRestart(testSession) {
		t.Fatal("missing controller advertised live ownership")
	}
	if err := h.svc.Stop(context.Background(), testSession); err != nil || h.hostStops.Load() != 1 {
		t.Fatalf("explicit stop with no registered controller: calls=%d err=%v", h.hostStops.Load(), err)
	}
	ordinary := newHarness(t)
	if ordinary.svc.PreservesProviderOnRestart(testSession) {
		t.Fatal("ordinary conversation advertised persistent ownership")
	}
}

func TestPersistentTransportFailureRetainsWorkForReconnect(t *testing.T) {
	provider := &terminatingConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, provider)
	ctx := context.Background()
	turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "running"})
	if err != nil {
		t.Fatal(err)
	}
	provider.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "queued"}); err != nil {
		t.Fatal(err)
	}
	provider.emit(ports.ChatEvent{
		Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerStopped,
		Err: ports.ErrChatRecoveryInconclusive,
	})
	_ = provider.Close()
	h.ctrl.Wait()
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	states := turnStateByText(t, snapshot)
	if states["running"] != domain.TurnStateRunning || states["queued"] != domain.TurnStateQueued {
		t.Fatalf("transport failure settled surviving provider work: %v", states)
	}
	if h.ctrl.State() != ports.ChatControllerStopped || provider.terminated.Load() {
		t.Fatal("lost controller remained live or terminated its persistent provider")
	}
}

func TestPersistentSourceReceiptFollowsAllDerivedProjections(t *testing.T) {
	ctx := context.Background()
	receipts := make(chan string, 4)
	var h *harness
	conv := &receiptConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
		ack: func(id string) error {
			if id != "source-frame" && id != "ignored-frame" {
				return nil // The provider recognizes source-frame receipts, not derived event IDs.
			}
			snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
			if err != nil {
				return err
			}
			if len(snapshot.Messages) != 2 || snapshot.Messages[1].Text != "one two" {
				return fmt.Errorf("source frame acknowledged before its output committed: %+v", snapshot.Messages)
			}
			receipts <- id
			return nil
		},
	}
	h = newHarnessWithConversation(t, conv)
	turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
	frame := []ports.ChatEvent{
		{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "answer", ProviderEventID: "source-frame:0", Delta: "one "},
		{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "answer", ProviderEventID: "source-frame:1", ProviderReceiptID: "source-frame", Delta: "two"},
	}
	for range 2 { // The second delivery models a commit followed by loss of the ACK.
		conv.emit(frame...)
		select {
		case id := <-receipts:
			if id != "source-frame" {
				t.Fatalf("receipt = %q, want source-frame", id)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("source frame was not acknowledged after all derived events committed")
		}
	}
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventDeliveryReceipt, ProviderEventID: "ignored-frame:0", ProviderReceiptID: "ignored-frame", Delta: "must not appear"})
	select {
	case id := <-receipts:
		if id != "ignored-frame" {
			t.Fatalf("ignored frame receipt = %q", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("frame without semantic events was not acknowledged")
	}
	archived, err := h.st.ProviderEventsSince(ctx, h.ctrl.ConversationID(), 0, 10)
	if err != nil || len(archived) != 4 {
		t.Fatalf("replayed frame archives = %+v, %v; want start, two output events and one receipt", archived, err)
	}
	var record struct {
		ProviderReceiptID string `json:"providerReceiptId"`
	}
	if err := json.Unmarshal([]byte(archived[2].PayloadJson), &record); err != nil || record.ProviderReceiptID != "source-frame" {
		t.Fatalf("archived source receipt = %q, %v", record.ProviderReceiptID, err)
	}
}

func TestFencedPersistentProjectionNeverAcknowledgesOrPromotesQueue(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		name := "new completion"
		if duplicate {
			name = "previously committed output"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			receipts := make(chan string, 4)
			conv := &receiptConversation{
				terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
				ack:                     func(id string) error { receipts <- id; return nil },
			}
			h := newHarnessWithConversation(t, conv)
			turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "first"})
			if err != nil {
				t.Fatal(err)
			}
			conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
			})
			queued, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "queued"})
			if err != nil || queued.State != domain.TurnStateQueued {
				t.Fatalf("queue: %+v, %v", queued, err)
			}
			event := ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "answer", ProviderEventID: "output", ProviderReceiptID: "output-frame", Delta: "saved answer"}
			conv.emit(event)
			select {
			case <-receipts:
			case <-time.After(3 * time.Second):
				t.Fatal("initial output did not commit")
			}
			if err := h.st.ClaimChatControllerGeneration(ctx, testSession, "replacement-generation"); err != nil {
				t.Fatal(err)
			}
			if !duplicate {
				event = ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, ProviderEventID: "completion", ProviderReceiptID: "completion-frame", TurnState: domain.TurnStateCompleted}
			}
			conv.emit(event)
			done := make(chan struct{})
			go func() { h.ctrl.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("fenced controller did not detach")
			}
			select {
			case id := <-receipts:
				t.Fatalf("fenced controller acknowledged %q", id)
			default:
			}
			snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
			if err != nil {
				t.Fatal(err)
			}
			states := turnStateByText(t, snapshot)
			if states["first"] != domain.TurnStateRunning || states["queued"] != domain.TurnStateQueued || len(conv.sentTexts()) != 1 || conv.terminated.Load() {
				t.Fatalf("fenced controller settled work, promoted queue, or destroyed provider: states=%v", states)
			}
		})
	}
}
