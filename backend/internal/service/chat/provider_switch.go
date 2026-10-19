package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TerminalProviderSwitcher is late-bound during daemon construction.
type TerminalProviderSwitcher interface {
	SwitchTUIProvider(context.Context, domain.SessionID, domain.ProviderSwitchRequest) error
}

// SetTerminalProviderSwitcher gives the service the terminal side of a switch.
func (s *Service) SetTerminalProviderSwitcher(switcher TerminalProviderSwitcher) {
	s.terminalSwitcher = switcher
}

type providerSwitchActivityDetail struct {
	Event         string              `json:"event"`
	SourceHarness domain.AgentHarness `json:"sourceHarness"`
	TargetHarness domain.AgentHarness `json:"targetHarness"`
	// What AO knows of each side's settings. A side on its agent's default has none.
	SourceModel  string `json:"sourceModel,omitempty"`
	TargetModel  string `json:"targetModel,omitempty"`
	TargetEffort string `json:"targetEffort,omitempty"`
	// TargetAutoCompactWindow is the Claude Code compaction window the target started with.
	TargetAutoCompactWindow string `json:"targetAutoCompactWindow,omitempty"`
	BranchID                string `json:"branchId"`
}

// SwitchProvider performs an idle-only Chat-to-Chat handoff. It retains the
// canonical AO conversation and publishes a new provider branch only after the
// target has started successfully.
func (s *Service) SwitchProvider(ctx context.Context, id domain.SessionID, request domain.ProviderSwitchRequest) error {
	if request.TargetHarness == "" {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalUnsupported, Detail: "target provider is required"}
	}
	if record, found, err := s.sessions.GetSession(ctx, id); err != nil {
		return err
	} else if found && domain.NormalizeSessionMode(record.Mode) == domain.SessionModeTUI && s.terminalSwitcher != nil {
		// One builder writes the note for both UIs.
		started := time.Now()
		if request.HandoffNote, err = s.HandoffNote(ctx, record, request.TargetHarness, strings.TrimSpace(request.IdempotencyKey)); err != nil {
			return err
		}
		noteReady := time.Since(started)
		if err := s.terminalSwitcher.SwitchTUIProvider(ctx, id, request); err != nil {
			return err
		}
		s.log.Info("provider switch timing", "session", id, "mode", "terminal",
			"source", record.Harness, "target", request.TargetHarness,
			"handoff_note", noteReady, "total", time.Since(started))
		return nil
	}
	record, err := s.requireChatSession(ctx, id)
	if err != nil {
		return err
	}
	if record.IsTerminated {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalRecoveryRequired, Detail: "terminated sessions must be restored before switching"}
	}
	if !s.SupportsChat(request.TargetHarness) {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalUnsupported, Detail: fmt.Sprintf("%s does not support Chat", request.TargetHarness)}
	}
	owner := domain.SessionConversationOwner(id)
	gate := s.controllerGate(owner)
	if err := gate.lock(ctx); err != nil {
		return err
	}
	defer gate.unlock()

	source, err := s.Controller(id)
	if err != nil {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalRecoveryRequired, Detail: "the source Chat controller is not ready", Cause: err}
	}
	if source.harness == request.TargetHarness {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalUnsupported, Detail: "target provider is already active"}
	}
	if source.busy() {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalBusy, Detail: "the provider is still working"}
	}
	// The target runs under the chat's own permission mode, so an agent that only
	// runs with permissions bypassed (Pi) can take over a chat that has them.
	permissions := s.switchPermissions(source, request.TargetHarness)
	if err := s.PreflightChat(ctx, request.TargetHarness, permissions); err != nil {
		kind := domain.ProviderSwitchRefusalUnavailable
		if errors.Is(err, ports.ErrChatUnsupported) {
			kind = domain.ProviderSwitchRefusalUnsupported
		}
		return &domain.ProviderSwitchError{Kind: kind, Detail: err.Error(), Cause: err}
	}
	conversation, err := s.store.ConversationForSession(ctx, id)
	if err != nil {
		return err
	}
	if pending, pendingErr := s.store.HasPendingConversationInteractions(ctx, conversation.ID); pendingErr != nil {
		return pendingErr
	} else if pending {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalPendingInteraction, Detail: "answer the pending provider interaction before switching"}
	}
	started := time.Now()
	noteInput, err := s.handoffNoteInput(ctx, record, request.TargetHarness, strings.TrimSpace(request.IdempotencyKey))
	if err != nil {
		return err
	}
	if err := source.BeginIdleBranchHandoff(ctx); err != nil {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalBusy, Detail: err.Error(), Cause: err}
	}
	abortSource := true
	defer func() {
		if abortSource {
			source.AbortHandoff()
		}
	}()

	startCfg, sourceDriver, err := s.branchLaunchConfig(source)
	if err != nil {
		return &domain.ProviderSwitchError{Kind: domain.ProviderSwitchRefusalRecoveryRequired, Detail: "source launch configuration is unavailable", Cause: err}
	}
	// The saved settings can name the session's owner from before an earlier
	// switch. Starting the target, or restoring the source, checks that owner
	// against the session, so take it from the session as it is now.
	startCfg.ExpectedControllerOwner = record.ControllerOwner()
	activeBranch, err := s.store.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		return err
	}
	// The outgoing provider's model, for the handoff marker. Read before it is stopped.
	sourceModel := cmp.Or(source.Settings().Model, record.Metadata.Model)
	operationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nativeEditHandoffLimit)
	defer cancel()
	// The source is idle, so the workspace does not change while it stops: list
	// the changed files meanwhile instead of before.
	changedFiles := make(chan []string, 1)
	go func() {
		changedFiles <- handoffChangedFiles(operationCtx, record.Metadata.WorkspacePath, record.Metadata.DiffBaseSHA)
	}()
	stopStarted := time.Now()
	if err := source.closeForBranchHandoff(operationCtx); err != nil {
		closeErr := fmt.Errorf("stop source provider: %w", err)
		// A source that stopped anyway is brought back, or the session would sit
		// idle with no provider behind it.
		if source.State() == ports.ChatControllerStopped {
			if restoreErr := s.restoreClosedSourceController(operationCtx, id, source, activeBranch, startCfg, sourceDriver); restoreErr != nil {
				closeErr = errors.Join(closeErr, restoreErr)
			} else {
				abortSource = false
			}
		}
		return closeErr
	}
	stopped := time.Since(stopStarted)
	noteInput.ChangedFiles = <-changedFiles
	handoffNote := domain.BuildHandoffNote(noteInput)
	noteReady := time.Since(stopStarted)

	targetCfg := cloneStartConfig(startCfg)
	targetCfg.Harness = request.TargetHarness
	targetCfg.ProviderConversationID = ""
	// The source may have been rejoined after a daemon restart. The target is a
	// new provider with no host to rejoin, so it must be allowed to start one.
	targetCfg.ReconnectOnly = false
	targetCfg.ProviderSwitch = &domain.ChatProviderSwitch{
		ConversationID: conversation.ID, SourceBranchID: activeBranch.ID,
		SourceSequence: conversation.LatestSequence, Bootstrap: handoffNote,
	}
	if request.TargetHarness == domain.HarnessClaudeCode {
		targetCfg.ProviderSwitch.AutoCompactWindow = request.AutoCompactWindow
	}
	targetCfg.Model, targetCfg.Effort = request.Model, request.Effort
	targetCfg.Permissions = permissions
	targetCfg.ControllerReady = func(started StartResult) (ControllerCommit, error) {
		targetRecord := record
		targetRecord.Harness = request.TargetHarness
		targetRecord.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: s.now()}
		targetRecord.Metadata.ProviderConversationID = started.ProviderConversationID
		targetRecord.Metadata.ControllerGeneration = started.ControllerGeneration
		targetRecord.Metadata.Model, targetRecord.Metadata.Effort = request.Model, request.Effort
		targetRecord.UpdatedAt = s.now()
		detail, _ := json.Marshal(providerSwitchActivityDetail{
			Event: "provider_switch", SourceHarness: source.harness,
			TargetHarness: request.TargetHarness, BranchID: started.ProviderBoundary.ID,
			SourceModel: sourceModel, TargetModel: request.Model, TargetEffort: request.Effort,
			TargetAutoCompactWindow: targetCfg.ProviderSwitch.AutoCompactWindow,
		})
		switchActivity := domain.ConversationActivity{
			ID: started.ProviderBoundary.ID + ":provider-switch", Kind: domain.ActivityKindSystem,
			Status:  domain.ActivityStatusCompleted,
			Summary: "Switched provider from " + string(source.harness) + " to " + string(request.TargetHarness),
			Detail:  detail, RequestID: strings.TrimSpace(request.IdempotencyKey),
			ProviderItemID: started.ProviderBoundary.ID + ":provider-switch",
		}
		if err := s.store.CommitProviderSwitch(operationCtx, targetRecord, *started.ProviderBoundary, started.ControllerGeneration, switchActivity); err != nil {
			return ControllerCommit{}, err
		}
		committed := started.Conversation
		committed.ActiveBranchID = started.ProviderBoundary.ID
		committed.SessionID = id
		return ControllerCommit{Conversation: committed, ControllerOwner: domain.SessionControllerOwner{
			Harness: request.TargetHarness, Mode: domain.SessionModeChat,
			ProviderConversationID: started.ProviderConversationID,
			ControllerGeneration:   started.ControllerGeneration,
		}}, nil
	}

	// The context reading belongs to the outgoing agent's model. The target starts
	// without one, or the meter would show it until the target reports its own.
	if err := s.store.RecordUsage(operationCtx, conversation.ID, domain.ConversationUsage{}); err != nil {
		s.log.Warn("clear source usage before provider switch", "session", id, "error", err)
	}
	targetStarted := time.Now()
	target, err := s.startLocked(operationCtx, targetCfg)
	if err != nil {
		// Starting the target saved its model, effort and permission mode on the
		// conversation. The source keeps running on its own, so put them back.
		if settingsErr := s.store.SetConversationSettings(operationCtx, conversation.ID, conversation.Settings, s.now()); settingsErr != nil {
			err = errors.Join(err, fmt.Errorf("restore source settings: %w", settingsErr))
		}
		if restoreErr := s.restoreClosedSourceController(operationCtx, id, source, activeBranch, startCfg, sourceDriver); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		} else {
			abortSource = false
		}
		return fmt.Errorf("start target provider: %w", err)
	}
	source.completeBranchHandoff()
	abortSource = false
	s.log.Info("provider switch timing", "session", id, "mode", "chat",
		"source", source.harness, "target", request.TargetHarness,
		"stop_source", stopped, "stop_and_note", noteReady, "start_target", time.Since(targetStarted),
		"total", time.Since(started))
	if request.FastMode {
		// Fast mode is a setting of the running controller, so it is applied to
		// the target once it exists rather than passed through its launch.
		settings := target.Settings()
		settings.FastMode = true
		if err := target.SetSettings(operationCtx, settings); err != nil {
			s.log.Warn("apply fast mode after provider switch", "session", id, "error", err)
		}
	}
	return nil
}

// switchPermissions is the permission mode a switch target starts with: the
// chat's current one, else the one its agent was launched with, else Auto. A
// target that refuses that mode (Kimi runs only in its default) gets Default,
// which asks before acting.
func (s *Service) switchPermissions(source *Controller, target domain.AgentHarness) ports.PermissionMode {
	mode := s.sourcePermissions(source)
	if driver, err := s.drivers.Driver(target); err == nil {
		if checker, ok := driver.(ports.ChatPermissionModeChecker); ok && !checker.SupportsPermissionMode(mode) {
			return ports.PermissionModeDefault
		}
	}
	return mode
}

func (s *Service) sourcePermissions(source *Controller) ports.PermissionMode {
	if mode := source.Settings().ApprovalMode; mode != "" {
		return ports.NormalizePermissionMode(mode)
	}
	s.mu.RLock()
	launched := s.startConfigs[source.owner()].Permissions
	s.mu.RUnlock()
	if launched != "" {
		return ports.NormalizePermissionMode(launched)
	}
	return ports.PermissionModeAuto
}
