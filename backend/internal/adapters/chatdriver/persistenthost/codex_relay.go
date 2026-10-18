package persistenthost

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// codexRelay owns delivery, not Codex semantics. A whole acknowledged turn
// prefix remains replayable until its terminal receipt: the adapter needs it to
// rebuild citation and retry normalization state after a daemon replacement.
type codexRelay struct {
	identity   string
	next       uint64
	journal    *frameJournal
	limit      int64
	entries    []*codexEntry
	byID       map[string]*codexEntry
	turns      map[codexTurnKey]*codexTurn
	active     map[string]string
	attachment *codexAttachment
	stopped    bool
}

type codexTurn struct {
	pending              int
	terminalAcknowledged bool
}

type codexTurnKey struct{ threadID, turnID string }

type codexEntry struct {
	sequence   uint64
	id         string
	span       journalSpan
	turn       codexTurnKey
	terminal   bool
	acked      bool
	request    string
	observedAt time.Time
}

type codexOutbound struct {
	entry    *codexEntry
	frame    []byte
	replayed bool
}

type codexAttachment struct {
	conn        net.Conn
	cutoff      uint64
	open        bool
	queue       []codexOutbound
	directBytes int
}

func newCodexRelay(ctx context.Context, path string) (*codexRelay, error) {
	identity, err := randomToken()
	if err != nil {
		return nil, err
	}
	// Run holds the exclusive launch lock. A previous host crash can leave a
	// compaction file; no surviving host can own it when this relay is created.
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	for _, previous := range files {
		if !strings.HasPrefix(previous.Name(), "codex-events-") || !strings.HasSuffix(previous.Name(), ".journal") {
			continue
		}
		if err := os.Remove(filepath.Join(filepath.Dir(path), previous.Name())); err != nil {
			return nil, err
		}
	}
	journal, err := openFrameJournal(ctx, path)
	if err != nil {
		return nil, err
	}
	return &codexRelay{identity: identity, journal: journal, limit: maxJournalBytes, byID: make(map[string]*codexEntry), turns: make(map[codexTurnKey]*codexTurn), active: make(map[string]string)}, nil
}

func (r *codexRelay) snapshot() *CodexState {
	snapshot := &CodexState{ActiveTurns: make(map[string]string, len(r.active)), RetainedTurns: make(map[string][]string)}
	for thread, turn := range r.active {
		snapshot.ActiveTurns[thread] = turn
	}
	for key := range r.turns {
		snapshot.RetainedTurns[key.threadID] = append(snapshot.RetainedTurns[key.threadID], key.turnID)
	}
	for _, turns := range snapshot.RetainedTurns {
		sort.Strings(turns)
	}
	return snapshot
}

// Account, login, auth refresh, capacity and configuration traffic never enters
// this conversation journal. Unknown methods remain raw and unretained.
func codexConversationNotification(method string) bool {
	return strings.HasPrefix(method, "turn/") || strings.HasPrefix(method, "item/") ||
		strings.HasPrefix(method, "thread/") || method == "error" || method == "serverRequest/resolved" || method == "model/rerouted"
}

func (r *codexRelay) scope(method string, params json.RawMessage) (codexTurnKey, bool) {
	var scope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &scope)
	turn := scope.TurnID
	if turn == "" {
		turn = scope.Turn.ID
	}
	if method == "turn/started" && scope.ThreadID != "" && turn != "" {
		r.active[scope.ThreadID] = turn
	}
	if turn == "" {
		turn = r.active[scope.ThreadID]
	}
	terminal := method == "turn/completed"
	if terminal && r.active[scope.ThreadID] == turn {
		delete(r.active, scope.ThreadID)
	}
	if scope.ThreadID == "" || turn == "" {
		return codexTurnKey{}, terminal
	}
	key := codexTurnKey{threadID: scope.ThreadID, turnID: turn}
	// Native Codex starts a turn before its items. Once the terminal receipt
	// reclaimed that prefix, late output must not create a new prefix which can
	// never receive another terminal. Its individual receipt can reclaim it.
	if method != "turn/started" && !terminal && r.active[scope.ThreadID] != turn && r.turns[key] == nil {
		return codexTurnKey{}, false
	}
	return key, terminal
}

// compact atomically replaces the open journal only after copying every retained
// span. Its metadata is host-memory state, as with ACP: this protects daemon
// replacement while the host survives, not host crashes or power loss.
func (r *codexRelay) compact(ctx context.Context) error {
	file, err := os.CreateTemp(filepath.Dir(r.journal.path), "codex-events-*.journal")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	var spans []journalSpan
	var size int64
	for _, entry := range r.entries {
		if entry.request != "" {
			continue
		}
		span := journalSpan{offset: size, length: entry.span.length}
		if err := r.journal.replayTo(ctx, file, entry.span); err != nil {
			return err
		}
		spans = append(spans, span)
		size += span.length
	}
	index := 0
	for _, entry := range r.entries {
		if entry.request == "" {
			entry.span = spans[index]
			index++
		}
	}
	previous := r.journal
	r.journal = &frameJournal{path: file.Name(), file: file, size: size}
	keep = true
	return previous.close(context.WithoutCancel(ctx))
}

func (r *codexRelay) acknowledge(ctx context.Context, id string) error {
	entry := r.byID[id]
	if entry == nil || entry.acked {
		return nil
	}
	entry.acked = true
	if entry.turn.threadID != "" {
		turn := r.turns[entry.turn]
		turn.pending--
		turn.terminalAcknowledged = turn.terminalAcknowledged || entry.terminal
		// The common streamed-delta receipt needs no journal scan.
		if turn.pending != 0 || !turn.terminalAcknowledged {
			return nil
		}
		delete(r.turns, entry.turn)
	}
	kept := r.entries[:0]
	removed := false
	for _, candidate := range r.entries {
		if candidate.request == "" && candidate.acked && ((entry.turn.threadID == "" && candidate.id == id) || (entry.turn.threadID != "" && candidate.turn == entry.turn)) {
			delete(r.byID, candidate.id)
			removed = true
			continue
		}
		kept = append(kept, candidate)
	}
	clear(r.entries[len(kept):])
	r.entries = kept
	if removed && len(r.byID) == 0 {
		return r.journal.reset(ctx)
	}
	if removed && r.journal.size >= r.limit/2 {
		return r.compact(ctx)
	}
	return nil
}

func (h *host) writeCodex(attachment *codexAttachment) {
	defer h.detach(attachment.conn)
	for {
		h.mu.Lock()
		for h.codex.attachment == attachment && !h.codex.stopped && len(attachment.queue) == 0 {
			h.cond.Wait()
		}
		if h.codex.attachment != attachment || h.codex.stopped {
			h.mu.Unlock()
			return
		}
		next := attachment.queue[0]
		attachment.queue[0] = codexOutbound{}
		attachment.queue = attachment.queue[1:]
		attachment.directBytes -= len(next.frame)
		frame := next.frame
		if next.entry != nil {
			entry := next.entry
			if h.codex.byID[entry.id] == nil {
				h.cond.Broadcast()
				h.mu.Unlock()
				continue
			}
			frame = make([]byte, entry.span.length)
			if _, err := h.codex.journal.file.ReadAt(frame, entry.span.offset); err != nil {
				h.mu.Unlock()
				return
			}
			params, _ := json.Marshal(CodexEvent{EventID: entry.id, Frame: json.RawMessage(frame), Replayed: next.replayed, ObservedAt: entry.observedAt})
			frame = marshalFrame(map[string]json.RawMessage{"method": json.RawMessage(`"` + CodexEventMethod + `"`), "params": params}, nil)
		}
		h.cond.Broadcast()
		h.mu.Unlock()
		if _, err := attachment.conn.Write(frame); err != nil {
			return
		}
	}
}

func (h *host) queueCodexDirectLocked(frame []byte) {
	attachment := h.codex.attachment
	if attachment == nil {
		return
	}
	for attachment.directBytes > 0 && attachment.directBytes+len(frame) > maxDetachedBytes && h.codex.attachment == attachment && !h.codex.stopped {
		h.cond.Wait()
	}
	if h.codex.attachment != attachment || h.codex.stopped {
		return
	}
	attachment.queue = append(attachment.queue, codexOutbound{frame: append([]byte(nil), frame...)})
	attachment.directBytes += len(frame)
	h.cond.Broadcast()
}

func (h *host) codexProviderFrameLocked(frame []byte) error {
	var envelope struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
		Params json.RawMessage  `json:"params"`
	}
	if json.Unmarshal(frame, &envelope) != nil || envelope.ID != nil || !codexConversationNotification(envelope.Method) {
		if requestID, ok := serverRequestID(frame); ok {
			if _, exists := h.pendingRequests[requestID]; !exists {
				h.pendingRequests[requestID] = &pendingRequest{frame: append([]byte(nil), frame...)}
				h.pendingOrder = append(h.pendingOrder, requestID)
				h.codex.next++
				h.codex.entries = append(h.codex.entries, &codexEntry{sequence: h.codex.next, request: requestID})
			}
			if h.codex.attachment == nil || !h.codex.attachment.open {
				return nil
			}
		}
		h.queueCodexDirectLocked(frame)
		return nil
	}
	if int64(len(frame)) > h.codex.limit {
		return fmt.Errorf("persistent Codex conversation frame exceeds journal limit: %w", errJournalFull)
	}
	if h.codex.stopped {
		return context.Canceled
	}
	if h.codex.journal.size+int64(len(frame)) > h.codex.limit {
		if err := h.codex.compact(h.ctx); err != nil {
			return err
		}
		if h.codex.journal.size+int64(len(frame)) > h.codex.limit {
			return fmt.Errorf("persistent Codex active turn exceeds journal limit: %w", errJournalFull)
		}
	}
	span, err := h.codex.journal.append(h.ctx, frame)
	if err != nil {
		return err
	}
	h.codex.next++
	turn, terminal := h.codex.scope(envelope.Method, envelope.Params)
	entry := &codexEntry{sequence: h.codex.next, id: fmt.Sprintf("codex-host:%s:%d", h.codex.identity, h.codex.next), span: span, turn: turn, terminal: terminal, observedAt: time.Now().UTC()}
	if turn.threadID != "" {
		state := h.codex.turns[turn]
		if state == nil {
			state = &codexTurn{}
			h.codex.turns[turn] = state
		}
		state.pending++
	}
	h.codex.entries = append(h.codex.entries, entry)
	h.codex.byID[entry.id] = entry
	if attachment := h.codex.attachment; attachment != nil && attachment.open {
		attachment.queue = append(attachment.queue, codexOutbound{entry: entry})
		h.cond.Broadcast()
	}
	return nil
}

func (h *host) stopCodex() {
	if h.codex == nil {
		return
	}
	h.mu.Lock()
	h.codex.stopped = true
	attachment := h.codex.attachment
	h.codex.attachment = nil
	if h.cond != nil {
		h.cond.Broadcast()
	}
	h.mu.Unlock()
	if attachment != nil {
		_ = attachment.conn.Close()
	}
}
