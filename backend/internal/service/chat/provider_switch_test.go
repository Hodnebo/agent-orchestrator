package chat_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func TestProviderSwitchPublishesApproximateBranch(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()

	// Give the source a complete durable exchange so the target assertion proves
	// the bootstrap came from AO's canonical rows, not provider scrollback.
	completeTurn(t, h, "keep this context", "provider-turn-1")
	h.awaitSnapshot(t, func(snapshot store.ConversationSnapshot) bool {
		return len(snapshot.Messages) == 2
	})

	// A turn this session had in the Terminal UI lives in the terminal turn
	// table. The note is built from both stores, merged by time.
	earlier := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC) // before the harness clock
	if err := h.st.RecordTerminalTurnRequest(ctx, domain.TerminalTurn{
		SessionID: testSession, Harness: domain.HarnessCodex, UserRequest: "terminal ask", RequestedAt: earlier,
	}); err != nil {
		t.Fatalf("record terminal request: %v", err)
	}
	if err := h.st.RecordTerminalTurnResponse(ctx, testSession, "terminal answer", earlier.Add(time.Minute)); err != nil {
		t.Fatalf("record terminal response: %v", err)
	}

	if source, err := h.st.ConversationForSession(ctx, testSession); err != nil {
		t.Fatalf("read conversation: %v", err)
	} else if err := h.st.RecordUsage(ctx, source.ID, domain.ConversationUsage{ContextUsed: 26956, ContextWindow: 475000}); err != nil {
		t.Fatalf("record source usage: %v", err)
	}
	before, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("read conversation before the switch: %v", err)
	}
	if source, found, err := h.st.GetSession(ctx, testSession); err != nil || !found || source.Harness != domain.HarnessCodex {
		t.Fatalf("source session = %+v found=%v err=%v, want a Codex session", source, found, err)
	}
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{
		TargetHarness:     domain.HarnessClaudeCode,
		IdempotencyKey:    "switch-once",
		AutoCompactWindow: "1m",
	}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}

	driver.mu.Lock()
	if len(driver.startConfigs) < 2 {
		driver.mu.Unlock()
		t.Fatalf("target start was not observed: %d starts", len(driver.startConfigs))
	}
	targetConfig := driver.startConfigs[len(driver.startConfigs)-1]
	driver.mu.Unlock()
	if after, err := h.st.ConversationForSession(ctx, testSession); err != nil || after.Usage != nil && after.Usage.ContextWindow != 0 {
		t.Fatalf("usage after the switch = %+v, %v; want the source's reading cleared", after.Usage, err)
	}
	if got := targetConfig.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; got != "1000000" {
		t.Fatalf("target compaction window = %q, want the switch's 1m", got)
	}
	if !strings.Contains(targetConfig.SystemPrompt, "keep this context") {
		t.Fatalf("target system prompt did not contain AO handoff context: %q", targetConfig.SystemPrompt)
	}
	for _, want := range []string{domain.HandoffNotePrefix, "terminal ask", "terminal answer", "## Latest turn"} {
		if !strings.Contains(targetConfig.SystemPrompt, want) {
			t.Fatalf("target handoff note is missing %q: %q", want, targetConfig.SystemPrompt)
		}
	}
	if strings.Index(targetConfig.SystemPrompt, "terminal ask") > strings.Index(targetConfig.SystemPrompt, "keep this context") {
		t.Fatalf("the older terminal turn must come before the latest chat turn: %q", targetConfig.SystemPrompt)
	}
	history, total, err := h.svc.SessionHistory(ctx, testSession, 0, 10)
	if err != nil || total != 2 || len(history) != 2 ||
		history[0].UserRequest != "keep this context" || history[1].UserRequest != "terminal ask" {
		t.Fatalf("session history = %+v (total %d), %v; want the chat turn then the terminal turn", history, total, err)
	}
	// A page past the end is empty, not nil: the HTTP layer serialises it as [].
	if past, total, err := h.svc.SessionHistory(ctx, testSession, 5, 10); err != nil || total != 2 || past == nil || len(past) != 0 {
		t.Fatalf("history past the end = %#v (total %d), %v; want an empty page", past, total, err)
	}

	// A switch is not replayed: asking again for the provider that now owns
	// the session is refused.
	err = h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{
		TargetHarness:  domain.HarnessClaudeCode,
		IdempotencyKey: "switch-once",
	})
	var refusal *domain.ProviderSwitchError
	if !errors.As(err, &refusal) || refusal.Kind != domain.ProviderSwitchRefusalUnsupported {
		t.Fatalf("repeated switch error = %v, want an unsupported refusal", err)
	}

	record, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("read switched session: found=%v err=%v", found, err)
	}
	if record.Harness != domain.HarnessClaudeCode || record.Mode != domain.SessionModeChat {
		t.Fatalf("durable session owner = %+v, want Claude Chat", record)
	}
	if record.Metadata.ProviderConversationID == "" {
		t.Fatalf("switched session has no target provider conversation: %+v", record.Metadata)
	}
	snapshot, err := h.st.LoadConversationSnapshot(ctx, before.ID)
	if err != nil {
		t.Fatalf("read switched conversation: %v", err)
	}
	if snapshot.ActiveBranch.ID == "" || snapshot.ActiveBranch.ID == before.ActiveBranchID {
		t.Fatalf("active branch = %q, want a new branch after %q", snapshot.ActiveBranch.ID, before.ActiveBranchID)
	}
	if snapshot.ActiveBranch.ProviderConversationID != record.Metadata.ProviderConversationID {
		t.Fatalf("active branch provider conversation = %q, want the session's %q",
			snapshot.ActiveBranch.ProviderConversationID, record.Metadata.ProviderConversationID)
	}
	if !strings.Contains(snapshot.ActiveBranch.HandoffNote, "keep this context") {
		t.Fatalf("the handoff note was not stored with the new branch: %q", snapshot.ActiveBranch.HandoffNote)
	}
	if snapshot.ActiveBranch.Strategy != domain.ConversationBranchStrategyApproximateContext {
		t.Fatalf("branch strategy = %q, want approximate_context", snapshot.ActiveBranch.Strategy)
	}
	foundMarker := false
	for _, activity := range snapshot.Activities {
		if activity.RequestID == "switch-once" && strings.Contains(activity.Summary, "Switched provider") {
			foundMarker = true
			break
		}
	}
	if !foundMarker {
		t.Fatalf("the switch marker was not committed with the branch")
	}
}

func TestProviderSwitchFailureRestoresSourceController(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	before, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{
		ApprovalMode: domain.PermissionModeAcceptEdits,
	})
	if err != nil {
		t.Fatalf("SetTurnSettings: %v", err)
	}
	driver.startErr = errProviderSwitchTarget

	err = h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{
		TargetHarness: domain.HarnessClaudeCode, Model: "claude-foreign-model", Effort: "max",
	})
	if err == nil || !strings.Contains(err.Error(), "start target provider") {
		t.Fatalf("SwitchProvider error = %v, want target-start failure", err)
	}
	if _, err := h.svc.Controller(testSession); err != nil {
		t.Fatalf("source controller was not restored: %v", err)
	}
	snapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := snapshot.Conversation.Settings
	if got.Model != before.Model || got.ReasoningEffort != before.ReasoningEffort || got.ApprovalMode != domain.PermissionModeAcceptEdits {
		t.Fatalf("settings after a failed switch = %+v, want the source's %+v", got, before)
	}
	// The restored source runs with the mode it had, not the one it was launched with.
	driver.mu.Lock()
	resumed := driver.resumeCalls[len(driver.resumeCalls)-1]
	driver.mu.Unlock()
	if resumed.Permissions != domain.PermissionModeAcceptEdits {
		t.Fatalf("restored source permissions = %q, want accept-edits", resumed.Permissions)
	}
	if controller, err := h.svc.Controller(testSession); err != nil || controller.Settings().ApprovalMode != domain.PermissionModeAcceptEdits {
		t.Fatalf("restored controller settings = %+v, %v; want accept-edits", controller.Settings(), err)
	}
}

func TestProviderSwitchCommitsTheTargetsSettingsAndKeepsItsLaunchWrites(t *testing.T) {
	ctx := context.Background()
	var h *harness
	var driver *editDriverState
	h, _, driver = newEditHarnessWithControllerEnv(t, false, ownerCheckedLaunch(&h))
	if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{ApprovalMode: domain.PermissionModeAcceptEdits}); err != nil {
		t.Fatalf("SetTurnSettings: %v", err)
	}
	var whileStarting domain.ConversationSettings
	driver.beforeStart = func(ports.ChatStartConfig) {
		conversation, _ := h.st.ConversationForSession(ctx, testSession)
		whileStarting = conversation.Settings
	}
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{
		TargetHarness: domain.HarnessClaudeCode, Model: "claude-target-model",
	}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	// A daemon that dies while the target starts resumes the source with these.
	if whileStarting.Model != "" || whileStarting.ApprovalMode != domain.PermissionModeAcceptEdits {
		t.Fatalf("settings while the target starts = %+v, want the source's", whileStarting)
	}
	conversation, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil || conversation.Settings.Model != "claude-target-model" {
		t.Fatalf("settings after the switch = %+v, %v; want the target's model", conversation.Settings, err)
	}
	record, _, err := h.st.GetSession(ctx, testSession)
	if err != nil || record.Metadata.BrowserCapabilityVerifier != launchVerifier(2) {
		t.Fatalf("browser verifier after the switch = %q, %v; want the target launch's", record.Metadata.BrowserCapabilityVerifier, err)
	}
}

func TestProviderSwitchQueuedBehindAnotherStartsFromItsResult(t *testing.T) {
	ctx := context.Background()
	var h *harness
	var driver *editDriverState
	h, _, driver = newEditHarnessWithControllerEnv(t, false, ownerCheckedLaunch(&h))
	second := make(chan error, 1)
	driver.beforeStart = func(ports.ChatStartConfig) {
		driver.mu.Lock()
		driver.beforeStart = nil
		driver.mu.Unlock()
		go func() {
			second <- h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessOpenCode})
		}()
		time.Sleep(50 * time.Millisecond) // the second switch reaches the lock
	}
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessClaudeCode}); err != nil {
		t.Fatalf("first SwitchProvider: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second SwitchProvider: %v", err)
	}
	record, _, err := h.st.GetSession(ctx, testSession)
	if err != nil || record.Harness != domain.HarnessOpenCode {
		t.Fatalf("session after both switches = %q, %v; want opencode", record.Harness, err)
	}
	if _, err := h.svc.Controller(testSession); err != nil {
		t.Fatalf("no controller after both switches: %v", err)
	}
}

func TestProviderSwitchRestoredTargetKeepsItsHandoffNote(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessClaudeCode}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	driver.mu.Lock()
	driver.startErr = errProviderSwitchTarget
	driver.resumed[driver.fresh.providerConversationID] = driver.fresh
	driver.mu.Unlock()
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessOpenCode}); err == nil {
		t.Fatal("second SwitchProvider succeeded, want the target to fail")
	}
	driver.mu.Lock()
	resumed := driver.resumeCalls[len(driver.resumeCalls)-1]
	driver.mu.Unlock()
	if !strings.Contains(resumed.SystemPrompt, domain.HandoffNotePrefix) {
		t.Fatalf("restored Claude Code system prompt has no handoff note: %q", resumed.SystemPrompt)
	}
}

// ownerCheckedLaunch stands in for the session manager: each launch writes its
// own browser verifier, and only while the session still has the owner the
// launch expects.
func ownerCheckedLaunch(h **harness) func(context.Context, domain.SessionControllerOwner) (map[string]string, error) {
	launches := 0
	return func(ctx context.Context, expected domain.SessionControllerOwner) (map[string]string, error) {
		launches++
		if *h == nil {
			return map[string]string{}, nil
		}
		applied, err := (*h).st.UpdateBrowserCapabilityVerifier(ctx, testSession, expected, launchVerifier(launches))
		if err != nil {
			return nil, err
		}
		if !applied {
			return nil, errors.New("session controller ownership changed")
		}
		return map[string]string{}, nil
	}
}

func launchVerifier(launch int) string { return fmt.Sprintf("verifier-%d", launch) }

func TestProviderSwitchPublicationFailureRestoresSourceController(t *testing.T) {
	h, _, _ := newEditHarnessWithStore(t, false, func(st *store.Store) chatsvc.Store {
		return failingProviderSwitchStore{Store: st, err: errors.New("commit unavailable")}
	})

	err := h.svc.SwitchProvider(context.Background(), testSession, domain.ProviderSwitchRequest{
		TargetHarness: domain.HarnessClaudeCode,
	})
	if err == nil || !strings.Contains(err.Error(), "start target provider") {
		t.Fatalf("SwitchProvider error = %v, want publication failure", err)
	}
	if _, err := h.svc.Controller(testSession); err != nil {
		t.Fatalf("source controller was not restored after publication failure: %v", err)
	}
}

type failingProviderSwitchStore struct {
	*store.Store
	err error
}

func (s failingProviderSwitchStore) CommitProviderSwitch(context.Context, domain.SessionRecord, domain.ConversationBranch, string, domain.ConversationSettings, domain.ConversationActivity) error {
	return s.err
}

var errProviderSwitchTarget = ports.ErrChatDriverUnavailable

func TestProviderSwitchStartsTheTargetUnderTheChatsPermissionMode(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{
		ApprovalMode: domain.PermissionModeBypassPermissions,
	}); err != nil {
		t.Fatalf("SetTurnSettings: %v", err)
	}

	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessClaudeCode}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	driver.mu.Lock()
	target := driver.startConfigs[len(driver.startConfigs)-1]
	driver.mu.Unlock()
	if target.Permissions != ports.PermissionModeBypassPermissions {
		t.Fatalf("target permissions = %q, want the chat's bypass mode", target.Permissions)
	}
}

func TestProviderSwitchStartsATargetThatRefusesTheChatsModeInDefault(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{
		ApprovalMode: domain.PermissionModeBypassPermissions,
	}); err != nil {
		t.Fatalf("SetTurnSettings: %v", err)
	}
	// Like Kimi, the target runs only in its default mode.
	driver.mu.Lock()
	driver.refusedMode = ports.PermissionModeBypassPermissions
	driver.mu.Unlock()

	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessKimi}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	driver.mu.Lock()
	target := driver.startConfigs[len(driver.startConfigs)-1]
	driver.mu.Unlock()
	if target.Permissions != ports.PermissionModeDefault {
		t.Fatalf("target permissions = %q, want default for an agent that refuses bypass", target.Permissions)
	}
}

func TestProviderSwitchStartsATargetThatRunsOnlyBypassedWithPermissionsBypassed(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{
		ApprovalMode: domain.PermissionModeAuto,
	}); err != nil {
		t.Fatalf("SetTurnSettings: %v", err)
	}
	// Like Pi, the target cannot ask before acting.
	driver.mu.Lock()
	driver.onlyMode = ports.PermissionModeBypassPermissions
	driver.mu.Unlock()
	modes, err := h.svc.PermissionModes(ctx, domain.HarnessPi)
	if err != nil || len(modes) != 1 || modes[0] != ports.PermissionModeBypassPermissions {
		t.Fatalf("PermissionModes = %v, %v; want only bypass-permissions", modes, err)
	}

	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessPi}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	driver.mu.Lock()
	target := driver.startConfigs[len(driver.startConfigs)-1]
	driver.mu.Unlock()
	if target.Permissions != ports.PermissionModeBypassPermissions {
		t.Fatalf("target permissions = %q, want the only mode the agent runs in", target.Permissions)
	}

	// Leaving it goes back to the chat's own mode, not the one Pi needed.
	driver.mu.Lock()
	driver.onlyMode = ""
	driver.mu.Unlock()
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessClaudeCode}); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	driver.mu.Lock()
	back := driver.startConfigs[len(driver.startConfigs)-1]
	driver.mu.Unlock()
	if back.Permissions != ports.PermissionModeAuto {
		t.Fatalf("permissions after leaving Pi = %q, want the chat's auto", back.Permissions)
	}
}

func TestProviderSwitchStartsTheTargetInTheModePickedForIt(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	ctx := context.Background()
	if _, err := h.svc.SetTurnSettings(ctx, testSession, domain.ConversationSettings{
		ApprovalMode: domain.PermissionModeBypassPermissions,
	}); err != nil {
		t.Fatalf("SetTurnSettings: %v", err)
	}
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{
		TargetHarness: domain.HarnessClaudeCode, PermissionMode: domain.PermissionModeAcceptEdits,
	}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	driver.mu.Lock()
	target := driver.startConfigs[len(driver.startConfigs)-1]
	driver.mu.Unlock()
	if target.Permissions != ports.PermissionModeAcceptEdits {
		t.Fatalf("target permissions = %q, want the mode picked for it", target.Permissions)
	}
}

func TestResumeStartsAnUnansweredSwitchBranchFresh(t *testing.T) {
	h, _, _ := newEditHarness(t, false)
	ctx := context.Background()
	if err := h.svc.SwitchProvider(ctx, testSession, domain.ProviderSwitchRequest{TargetHarness: domain.HarnessClaudeCode}); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	// The target never had a turn, so its provider never saved the conversation.
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	record, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("GetSession: found=%v err=%v", found, err)
	}
	var resumed ports.ChatResumeConfig
	driver := fakeDriver{conv: newFakeConversation()}
	driver.resume = func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		resumed = cfg
		if !cfg.FreshIfMissing {
			return nil, errors.New("provider conversation not found")
		}
		conv := newFakeConversation()
		conv.providerConversationID = "thread-started-fresh"
		return conv, nil
	}
	restarted := newRestartedEditService(t, h, driver, "unanswered-branch-restart")
	if _, err := restarted.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, WorkspacePath: t.TempDir(),
		ProviderConversationID: record.Metadata.ProviderConversationID,
	}); err != nil {
		t.Fatalf("Start after an unanswered switch: %v", err)
	}
	// The switch marker gives the conversation a sequence, but the target's
	// standing context has not been delivered yet.
	if resumed.HasPriorTurns {
		t.Fatal("resume of an unanswered switch target says it has prior turns, so its preamble is dropped")
	}
	if !resumed.FreshIfMissing || !strings.Contains(resumed.SystemPrompt, domain.HandoffNotePrefix) {
		t.Fatalf("resume = fresh %v, handoff in prompt %v; want a fresh start carrying the handoff",
			resumed.FreshIfMissing, strings.Contains(resumed.SystemPrompt, domain.HandoffNotePrefix))
	}
	conversation, err := h.st.ConversationForSession(ctx, testSession)
	if err != nil {
		t.Fatalf("ConversationForSession: %v", err)
	}
	branch, err := h.st.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil || branch.ProviderConversationID != "thread-started-fresh" {
		t.Fatalf("active branch provider = %q, %v; want it moved to the fresh conversation", branch.ProviderConversationID, err)
	}
}
