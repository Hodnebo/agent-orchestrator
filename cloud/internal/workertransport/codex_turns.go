package workertransport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// Codex runs its Stop hook only after a turn whose model request succeeded. A
// turn that ends on an error (usage limit, an unsupported model, expired
// sign-in) fires no hook at all, so the terminal session stayed "working" and
// a switch to Chat waited forever for a Stop that never came. Codex does
// persist every turn end to its rollout, errors included, as a task_complete
// event carrying the error. The worker watches the TUI's rollout and reports
// that turn end as idle, the same proof the Stop hook writes.
const (
	codexRolloutScanEvery   = 5
	maxCodexRolloutReadSize = 4 << 20
)

// codexTurnPollInterval is a variable so tests can poll quickly.
var codexTurnPollInterval = 2 * time.Second

// activityPublisher is optional so alternate worker controls keep the
// existing transport contract. The Cloud client implements it.
type activityPublisher interface {
	PublishActivity(context.Context, worker.ActivityEvent) error
}

// codexHomeFromEnvironment returns the CODEX_HOME an exec.Cmd environment
// sets. Later entries win, as they do for the process.
func codexHomeFromEnvironment(environment []string) string {
	home := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			home = value
		}
	}
	return strings.TrimSpace(home)
}

// codexRolloutTail follows the newest rollout of the terminal's Codex under a
// CODEX_HOME and reports turns that ended on an error. Only bytes written
// after the watcher started count: history from an earlier process (a resumed
// conversation, or an adopted Codex that outlived its worker) must not mark
// the new turn idle.
type codexRolloutTail struct {
	root    string
	started time.Time
	// conversationID is the conversation the terminal resumed, if any. Its
	// rollout is the terminal's whatever surface started it.
	conversationID string
	path           string
	// offsets is how far each rollout has been read. It starts at every
	// rollout's size when the watcher started, so a resumed conversation is
	// read from where it was, and one created later from its start.
	offsets map[string]int64
	partial []byte
	scans   int
}

// newCodexRolloutTail records where every existing rollout ends. Call it
// before the agent starts, so nothing it writes counts as history.
func newCodexRolloutTail(codexHome, conversationID string, started time.Time) *codexRolloutTail {
	t := &codexRolloutTail{
		root: filepath.Join(codexHome, "sessions"), started: started,
		conversationID: strings.TrimSpace(conversationID),
		offsets:        make(map[string]int64),
	}
	_ = filepath.WalkDir(t.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			t.offsets[path] = info.Size()
		}
		return nil
	})
	return t
}

// codexTurnFailure is a turn that ended on an error, and when it ended.
type codexTurnFailure struct {
	message string
	at      time.Time
}

// poll returns each turn that ended unsuccessfully since the previous poll.
func (t *codexRolloutTail) poll() []codexTurnFailure {
	if t.path == "" || t.scans%codexRolloutScanEvery == 0 {
		t.follow(t.newestTerminalRollout())
	}
	t.scans++
	if t.path == "" {
		return nil
	}
	file, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	offset := t.offsets[t.path]
	if info.Size() < offset {
		// Truncated or replaced: start over from its current end.
		t.offsets[t.path], t.partial = info.Size(), nil
		return nil
	}
	if info.Size() == offset {
		return nil
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	chunk, err := io.ReadAll(io.LimitReader(file, maxCodexRolloutReadSize))
	if err != nil {
		return nil
	}
	t.offsets[t.path] = offset + int64(len(chunk))
	data := append(t.partial, chunk...)
	t.partial = nil
	if last := bytes.LastIndexByte(data, '\n'); last < len(data)-1 {
		// Keep an unfinished record for the next poll.
		t.partial = append([]byte(nil), data[last+1:]...)
		data = data[:last+1]
	}
	var failures []codexTurnFailure
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), maxCodexRolloutReadSize)
	for scanner.Scan() {
		if failure, failed := codexTurnFailedRecord(scanner.Bytes()); failed {
			failures = append(failures, failure)
		}
	}
	return failures
}

// follow switches to path, read from where the watcher left it: a rollout
// that existed when the watcher started from its size then, a new one from
// its start.
func (t *codexRolloutTail) follow(path string) {
	if path == "" || path == t.path {
		return
	}
	t.path, t.partial = path, nil
}

// newestTerminalRollout is the most recently written rollout of the terminal's
// Codex: the conversation it resumed, or any rollout an interactive Codex
// writes. A conversation started in Chat (source vscode) and resumed in the
// terminal is the terminal's now; Chat's app-server shares the CODEX_HOME but
// never runs beside the terminal agent. Exec, MCP and subagent rollouts are
// never the terminal's own turn.
func (t *codexRolloutTail) newestTerminalRollout() string {
	var newest string
	var newestMod time.Time
	_ = filepath.WalkDir(t.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(newestMod) {
			return nil
		}
		if path != t.path && !t.resumedRollout(entry.Name()) && !codexTerminalRollout(path) {
			return nil
		}
		newest, newestMod = path, info.ModTime()
		return nil
	})
	return newest
}

// resumedRollout reports whether a rollout file is the resumed conversation's:
// Codex names it rollout-<time>-<conversation id>.jsonl.
func (t *codexRolloutTail) resumedRollout(name string) bool {
	return t.conversationID != "" && strings.HasSuffix(strings.TrimSuffix(name, ".jsonl"), "-"+t.conversationID)
}

func codexTerminalRollout(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	line, err := reader.ReadSlice('\n')
	if err != nil && len(line) == 0 {
		return false
	}
	var record struct {
		Type    string `json:"type"`
		Payload struct {
			Source json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "session_meta" {
		// A meta line longer than the buffer still belongs to some rollout;
		// only a readable non-interactive source excludes it.
		return err == bufio.ErrBufferFull
	}
	var source string
	if json.Unmarshal(record.Payload.Source, &source) == nil {
		return source != "exec" && source != "mcp"
	}
	// Subagent and other structured sources are not the terminal's own turn.
	return len(record.Payload.Source) == 0
}

// codexTurnFailedRecord reports a rollout record for a turn that ended on an
// error. A successful turn end is skipped: its Stop hook already reported it.
// The turn ended at the record's own timestamp, not when the watcher read it
// up to a poll later; a record without one is taken to have just ended.
func codexTurnFailedRecord(line []byte) (codexTurnFailure, bool) {
	var record struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type  string           `json:"type"`
			Error *json.RawMessage `json:"error"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "event_msg" {
		return codexTurnFailure{}, false
	}
	if record.Payload.Type != "task_complete" && record.Payload.Type != "turn_complete" {
		return codexTurnFailure{}, false
	}
	if record.Payload.Error == nil || bytes.Equal(bytes.TrimSpace(*record.Payload.Error), []byte("null")) {
		return codexTurnFailure{}, false
	}
	var failure struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(*record.Payload.Error, &failure)
	at, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		at = time.Now()
	}
	return codexTurnFailure{message: failure.Message, at: at}, true
}

// watchCodexTurnFailures runs for the life of the terminal agent's process.
func (s *Supervisor) watchCodexTurnFailures(ctx context.Context, tail *codexRolloutTail) {
	ticker := time.NewTicker(codexTurnPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		failures := tail.poll()
		if len(failures) == 0 {
			continue
		}
		s.reportCodexTurnFailure(ctx, failures[len(failures)-1])
	}
}

func (s *Supervisor) reportCodexTurnFailure(ctx context.Context, failure codexTurnFailure) {
	if s.DataDir != "" {
		// Stamped when the turn ended: a prompt typed after that, before this
		// poll saw it, is newer than the proof and keeps the agent working.
		if err := worker.RecordTUIStop(s.DataDir, failure.at); err != nil {
			s.Logger.Warn("record Codex turn failure as terminal stop", "error", err)
		}
	}
	s.mu.Lock()
	inputAt := s.lastTUIInputAt
	s.mu.Unlock()
	if inputAt.After(failure.at) {
		s.Logger.Info("Codex turn ended on an error before the latest terminal input; leaving the agent working",
			"error_message", failure.message)
		return
	}
	s.Logger.Info("Codex turn ended on an error; marking the terminal agent idle", "error_message", failure.message)
	publisher, ok := s.Control.(activityPublisher)
	if !ok {
		return
	}
	if err := publisher.PublishActivity(ctx, worker.ActivityEvent{
		Harness:         "codex",
		Event:           "stop-failure",
		State:           contract.ActivityIdle,
		SourceInterface: "tui",
	}); err != nil && ctx.Err() == nil {
		s.Logger.Warn("publish Codex turn failure activity", "error", err)
	}
}
