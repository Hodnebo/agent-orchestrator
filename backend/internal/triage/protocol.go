// Package triage owns support investigations independently of the AO daemon.
package triage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ProtocolVersion identifies the private support-host wire contract.
const ProtocolVersion = 1

// Choice is an explicitly configured harness/model selection, not a public
// provider catalog. Credential values must never appear in this projection.
type Choice struct {
	Harness           string                `json:"harness"`
	Label             string                `json:"label"`
	Models            []ModelChoice         `json:"models"`
	ModelsMissing     bool                  `json:"modelsMissing,omitempty"`
	Accounts          []Account             `json:"accounts,omitempty"`
	Available         bool                  `json:"available"`
	UnavailableReason string                `json:"unavailableReason,omitempty"`
	Source            string                `json:"source,omitempty"`
	UpdatedAt         time.Time             `json:"updatedAt"`
	Permissions       domain.PermissionMode `json:"permissions,omitempty"`
	Mode              string                `json:"mode,omitempty"`
}

// ModelChoice is a model explicitly present in the user's configuration.
type ModelChoice struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Efforts []string `json:"efforts,omitempty"`
	Default bool     `json:"default,omitempty"`
}

// Account references a configured login without carrying its credentials.
type Account struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
}

// Selection identifies the exact agent, model and account to launch.
type Selection struct {
	Harness     string                `json:"harness"`
	Model       string                `json:"model,omitempty"`
	Effort      string                `json:"effort,omitempty"`
	AccountID   string                `json:"accountId,omitempty"`
	Permissions domain.PermissionMode `json:"permissions,omitempty"`
	Mode        string                `json:"mode,omitempty"`
}

// Origin contains the context available at the initiating UI action.
type Origin struct {
	AppVersion   string          `json:"appVersion,omitempty"`
	BuildMode    string          `json:"buildMode,omitempty"`
	HostID       string          `json:"hostId,omitempty"`
	Surface      string          `json:"surface,omitempty"`
	SessionID    string          `json:"sessionId,omitempty"`
	DaemonState  string          `json:"daemonState,omitempty"`
	DaemonCode   string          `json:"daemonCode,omitempty"`
	DaemonOutput string          `json:"daemonOutput,omitempty"`
	Machine      json.RawMessage `json:"machine,omitempty"`
	Session      json.RawMessage `json:"session,omitempty"`
}

// StartRequest captures the initiating context and publication authorization.
type StartRequest struct {
	Selection
	Observation       string `json:"observation,omitempty"`
	Origin            Origin `json:"origin"`
	PublishAuthorized bool   `json:"publishAuthorized"`
	PreparationID     string `json:"preparationId,omitempty"`
}

// Command and Response are newline-delimited JSON on private process pipes.
type Command struct {
	ID               string              `json:"id"`
	Type             string              `json:"type"`
	RunID            string              `json:"runId,omitempty"`
	SessionID        string              `json:"sessionId,omitempty"`
	Start            *StartRequest       `json:"start,omitempty"`
	Choices          []Choice            `json:"choices,omitempty"`
	Selection        *Selection          `json:"selection,omitempty"`
	Text             string              `json:"text,omitempty"`
	RequestID        string              `json:"requestId,omitempty"`
	Answers          map[string][]string `json:"answers,omitempty"`
	Origin           *Origin             `json:"origin,omitempty"`
	GitHubCredential *GitHubCredential   `json:"githubCredential,omitempty"`
}

// Response is a command result or unsolicited committed run update.
type Response struct {
	Version        int             `json:"version"`
	ID             string          `json:"id,omitempty"`
	Type           string          `json:"type"`
	Run            *Run            `json:"run,omitempty"`
	Runs           []Run           `json:"runs,omitempty"`
	Choices        []Choice        `json:"choices,omitempty"`
	Selection      *Selection      `json:"selection,omitempty"`
	Error          string          `json:"error,omitempty"`
	GitHubIdentity *GitHubIdentity `json:"githubIdentity,omitempty"`
	Launch         *SessionLaunch  `json:"launch,omitempty"`
}

// SessionLaunch is private desktop-main input to the ordinary session API.
// Its immutable bytes, including attachment order, are reused on POST retries.
type SessionLaunch struct {
	ClientRequestID string             `json:"clientRequestId"`
	Prompt          string             `json:"prompt"`
	Attachments     []LaunchAttachment `json:"attachments"`
}

// LaunchAttachment matches the ordinary session API's base64 file input.
type LaunchAttachment struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// Message is a persisted, scrubbed conversation entry.
type Message struct {
	ID   string    `json:"id"`
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Question is a provider question projected into the desktop reply controls.
type Question struct {
	ID        string   `json:"id"`
	Prompt    string   `json:"prompt"`
	Options   []string `json:"options,omitempty"`
	Multiple  bool     `json:"multiple,omitempty"`
	AllowText bool     `json:"allowText,omitempty"`
}

// PendingInput groups questions belonging to one provider input request.
type PendingInput struct {
	ID        string     `json:"id"`
	Questions []Question `json:"questions"`
	Origin    string     `json:"origin,omitempty"`
}

// Evidence records a diagnostic fact, its source, time and availability.
type Evidence struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Source      string         `json:"source"`
	CollectedAt time.Time      `json:"collectedAt"`
	Status      string         `json:"status"`
	Text        string         `json:"text,omitempty"`
	Values      map[string]any `json:"values,omitempty"`
}

// Draft keeps the human report separate from agent-collected evidence.
type Draft struct {
	Title                string `json:"title"`
	Body                 string `json:"body"`
	Evidence             string `json:"evidence,omitempty"`
	ExistingReportURL    string `json:"existingReportUrl,omitempty"`
	ExistingReportReason string `json:"existingReportReason,omitempty"`
}

// ContextRead requests a bounded chunk from a source owned by the affected session.
// Source is an opaque root ID, never a model-supplied absolute directory.
type ContextRead struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	List   bool   `json:"list,omitempty"`
}

// Publication stores frozen GitHub intent and its known or uncertain outcome.
type Publication struct {
	State                string `json:"state"`
	Repository           string `json:"repository,omitempty"`
	Marker               string `json:"marker,omitempty"`
	Identity             string `json:"identity,omitempty"`
	IssueURL             string `json:"issueUrl,omitempty"`
	EvidenceURL          string `json:"evidenceUrl,omitempty"`
	Title                string `json:"title,omitempty"`
	Body                 string `json:"body,omitempty"`
	Evidence             string `json:"evidence,omitempty"`
	Error                string `json:"error,omitempty"`
	ExistingReportURL    string `json:"existingReportUrl,omitempty"`
	ExistingReportReason string `json:"existingReportReason,omitempty"`
}

// Run is the persisted support investigation and its desktop projection.
type Run struct {
	ID                      string        `json:"id"`
	Version                 int           `json:"version"`
	CreatedAt               time.Time     `json:"createdAt"`
	UpdatedAt               time.Time     `json:"updatedAt"`
	Request                 StartRequest  `json:"request"`
	State                   string        `json:"state"`
	Activity                string        `json:"activity"`
	Messages                []Message     `json:"messages"`
	Evidence                []Evidence    `json:"evidence,omitempty"`
	PendingInput            *PendingInput `json:"pendingInput,omitempty"`
	Draft                   *Draft        `json:"draft,omitempty"`
	Publication             Publication   `json:"publication"`
	ProviderConversationID  string        `json:"providerConversationId,omitempty"`
	ProviderTurnID          string        `json:"providerTurnId,omitempty"`
	Error                   string        `json:"error,omitempty"`
	Preparation             bool          `json:"preparation,omitempty"`
	SearchCompleted         bool          `json:"searchCompleted,omitempty"`
	NativeControlsAvailable bool          `json:"nativeControlsAvailable,omitempty"`
	Execution               string        `json:"execution,omitempty"`
	SessionID               string        `json:"sessionId,omitempty"`
	SessionLaunchStarted    bool          `json:"sessionLaunchStarted,omitempty"`
	SearchAuthRequired      bool          `json:"searchAuthRequired,omitempty"`
}

// Dependencies keeps the host testable without a daemon or real provider.
type Dependencies struct {
	Catalog               func(context.Context) ([]Choice, error)
	Configure             func(context.Context, []Choice) error
	Collect               func(context.Context, Origin) ([]Evidence, error)
	ReadContext           func(context.Context, Origin, ContextRead) (Evidence, error)
	StartConversation     func(context.Context, Run) (ports.ChatConversation, error)
	RecoverConversation   func(context.Context, Run) error
	Publish               func(context.Context, Run, func(Publication) error) (Publication, error)
	SearchReports         func(context.Context, string) (ReportSearch, error)
	ListSessions          func(context.Context) ([]DiagnosticSession, error)
	ValidateSession       func(context.Context, string) error
	StoreGitHubCredential func(context.Context, GitHubCredential) (GitHubIdentity, error)
	Now                   func() time.Time
}

// Options configures a standalone support process and its dependencies.
type Options struct {
	DataDir         string
	CallerSessionID string
	Deps            Dependencies
}
