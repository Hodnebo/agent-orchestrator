package domain

import (
	"slices"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/interfacehandoff"
)

// SessionInterface is the conversation controller currently committed for a
// session. Cloud sessions run a single controller at a time, and the interface
// can change only through the durable interface-transition coordinator.
//
//   - SessionInterfaceTUI: the provider's native interactive TUI inside the
//     sandbox agent PTY. This is the historical behavior and the default.
//   - SessionInterfaceChat: a durable, structured, event-projected headless
//     controller; the provider is invoked per-turn with headless flags.
type SessionInterface string

const (
	SessionInterfaceTUI  SessionInterface = "tui"
	SessionInterfaceChat SessionInterface = "chat"
)

func (i SessionInterface) Valid() bool {
	switch i {
	case SessionInterfaceTUI, SessionInterfaceChat:
		return true
	default:
		return false
	}
}

func (i SessionInterface) Normalized() SessionInterface {
	if i.Valid() {
		return i
	}
	return SessionInterfaceTUI
}

// chatStartHarnesses are the harnesses a new cloud session may start in Chat.
// Every other harness starts in the terminal TUI and may still switch to Chat
// later where interface handoff supports it (cursor). Adding a harness here is
// the whole change: every cloud session create resolves through
// StartInterface, and the desktop sends the user's default unfiltered.
var chatStartHarnesses = []string{"claude-code", "codex"}

// HarnessStartsInChat reports whether a new session of harness may start in
// Chat.
func HarnessStartsInChat(harness string) bool {
	return slices.Contains(chatStartHarnesses, harness)
}

// StartInterface is the interface a new session starts in, resolved as the
// local daemon resolves a spawn's mode (backend session_manager
// startSessionMode; both are tested against
// contracts/session-start-interface.json):
//
//  1. the interface the caller explicitly requested;
//  2. TUI when the caller sent no default interface (cloud only);
//  3. Chat for an orchestrator;
//  4. the user's default session interface (the desktop's setting);
//  5. TUI.
//
// A create without a default comes from a desktop that predates the field
// (v0.14.3 and older), so its orchestrators keep starting in the TUI as they
// did before: that desktop sends an explicit approval mode ("default") with
// every Chat turn, which would make a trusted orchestrator ask before each
// command. The local daemon always has a default, so rule 2 never applies
// there; a current desktop always sends its default, "tui" included.
//
// Chat then holds only for a harness that may start in Chat; any other
// harness starts in the TUI without an error, as the local daemon falls back
// when Chat is unavailable for a mode it did not explicitly request.
func StartInterface(kind string, requested, defaultInterface SessionInterface, harness string) SessionInterface {
	resolved := startPrecedence(kind, requested, defaultInterface)
	if resolved == SessionInterfaceChat && !HarnessStartsInChat(harness) {
		return SessionInterfaceTUI
	}
	return resolved
}

func startPrecedence(kind string, requested, defaultInterface SessionInterface) SessionInterface {
	if requested.Valid() {
		return requested
	}
	if defaultInterface == "" {
		return SessionInterfaceTUI
	}
	if kind == "orchestrator" {
		return SessionInterfaceChat
	}
	return defaultInterface.Normalized()
}

func (i SessionInterface) Opposite() SessionInterface {
	if i.Normalized() == SessionInterfaceChat {
		return SessionInterfaceTUI
	}
	return SessionInterfaceChat
}

// SessionInterfaceTransitionPolicy decides what AO does with work already in
// flight when moving a live session between its terminal and Chat controllers.
type SessionInterfaceTransitionPolicy = interfacehandoff.Policy

const (
	// The Cloud handoff admission policies.
	SessionInterfaceTransitionDrain     = interfacehandoff.PolicyDrain
	SessionInterfaceTransitionInterrupt = interfacehandoff.PolicyInterrupt
)

// SessionInterfaceTransitionPhase is the durable checkpoint of one controller
// handoff. Its state table lives in cloud/internal/interfacehandoff so the
// Cloud and local adapters use the same durable edges and terminal semantics.
type SessionInterfaceTransitionPhase = interfacehandoff.Phase

const (
	// The Cloud handoff durable checkpoints.
	SessionInterfaceTransitionRequested      = interfacehandoff.PhaseRequested
	SessionInterfaceTransitionPreflighting   = interfacehandoff.PhasePreflighting
	SessionInterfaceTransitionDraining       = interfacehandoff.PhaseDraining
	SessionInterfaceTransitionSourceStopping = interfacehandoff.PhaseSourceStopping
	SessionInterfaceTransitionSourceStopped  = interfacehandoff.PhaseSourceStopped
	SessionInterfaceTransitionTargetStarting = interfacehandoff.PhaseTargetStarting
	SessionInterfaceTransitionActivating     = interfacehandoff.PhaseActivating
	SessionInterfaceTransitionCompleted      = interfacehandoff.PhaseCompleted
	SessionInterfaceTransitionFailed         = interfacehandoff.PhaseFailed
	SessionInterfaceTransitionCancelled      = interfacehandoff.PhaseCancelled
	SessionInterfaceTransitionRecovery       = interfacehandoff.PhaseRecovery
)

// SessionInterfaceTransition is the durable controller-handoff record. The
// session row remains the authority for the currently committed interface; this
// row explains an in-progress gap where the old controller has stopped and the
// new one is not ready yet.
type SessionInterfaceTransition struct {
	SelectedModel        string
	SelectedEffort       string
	ID                   string
	OrgID                string
	SessionID            string
	SourceInterface      SessionInterface
	TargetInterface      SessionInterface
	Policy               SessionInterfaceTransitionPolicy
	Phase                SessionInterfaceTransitionPhase
	NativeConversationID string
	ErrorCode            string
	ErrorDetail          string
	NoticeAcknowledgedAt *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	CompletedAt          *time.Time
}

// SessionInterfaceTransitionMessage is an automation/lifecycle message held
// while neither controller is allowed to accept work.
type SessionInterfaceTransitionMessage struct {
	ID              int64
	TransitionID    string
	ClientMessageID string
	Message         string
	CreatedAt       time.Time
	DeliveredAt     *time.Time
}
