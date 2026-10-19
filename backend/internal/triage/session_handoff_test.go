package triage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
)

func handoffForTest(t *testing.T, c *hostClient, origin Origin, authorized bool) Response {
	t.Helper()
	c.send(Command{ID: "prepare", Type: "prepare", Origin: &origin})
	prepared := c.response("prepare")
	if prepared.Run == nil {
		t.Fatalf("prepare = %#v", prepared)
	}
	request := startCommand("handoff", authorized).Start
	request.PreparationID, request.Origin = prepared.Run.ID, origin
	c.send(Command{ID: "handoff", Type: "handoff", Start: request})
	response := c.response("handoff")
	if response.Type != "session-launch" || response.Run == nil || response.Launch == nil {
		t.Fatalf("handoff = %#v", response)
	}
	return response
}

func TestSessionHandoffWaitsForCollectionStagesPrivateContextAndNeverStartsProvider(t *testing.T) {
	dir := t.TempDir()
	started, finish := make(chan struct{}), make(chan struct{})
	var providers atomic.Int32
	deps := configuredDeps()
	deps.Collect = func(ctx context.Context, _ Origin) ([]Evidence, error) {
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []Evidence{{ID: "snapshot", Status: "collected", Text: "observed collector fact", Values: map[string]any{"sources": []string{"session-history"}, "api_key": "private-credential-value"}}}, nil
	}
	deps.ReadContext = func(_ context.Context, origin Origin, read ContextRead) (Evidence, error) {
		if origin.SessionID != "affected" || read.Source != "session-history" {
			t.Errorf("automatic private read = %#v, %#v", origin, read)
		}
		return Evidence{ID: "private-history", Status: "collected", Text: "relevant private turn; token=private-token-value"}, nil
	}
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) {
		providers.Add(1)
		return nil, errors.New("handoff must not launch")
	}
	c := serveForTest(t, dir, deps)
	origin := Origin{SessionID: "affected", Surface: "session", AppVersion: "affected-build"}
	c.send(Command{ID: "prepare", Type: "prepare", Origin: &origin})
	prepared := c.response("prepare").Run
	awaitSignal(t, started)
	request := startCommand("handoff", true).Start
	request.PreparationID, request.Origin = prepared.ID, origin
	c.send(Command{ID: "handoff", Type: "handoff", Start: request})
	c.wait(func(response Response) bool { return response.Run != nil && response.Run.Execution == "session" })
	c.send(Command{ID: "pending", Type: "get", RunID: prepared.ID})
	if pending := c.response("pending"); pending.Run.State != "collecting" || providers.Load() != 0 {
		t.Fatalf("handoff did not wait for collection: %#v", pending)
	}
	close(finish)
	response := c.response("handoff")
	if response.Type != "session-launch" || response.Run.ID != prepared.ID || response.Run.Preparation || response.Run.Execution != "session" || providers.Load() != 0 {
		t.Fatalf("handoff response = %#v, providers=%d", response, providers.Load())
	}
	launch := response.Launch
	if len(launch.Prompt) >= 16384 || launch.ClientRequestID != "triage-"+prepared.ID || len(launch.Attachments) != 2 {
		t.Fatalf("ordinary launch payload = %#v", launch)
	}
	var decodedBytes int
	for n, attachment := range launch.Attachments {
		decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			t.Fatal(err)
		}
		decodedBytes += len(decoded)
		if n == 0 {
			if attachment.MimeType != "application/json" || !json.Valid(decoded) || !strings.Contains(string(decoded), "observed collector fact") || !strings.Contains(string(decoded), "relevant private turn") || strings.Contains(string(decoded), "private-credential-value") || strings.Contains(string(decoded), "private-token-value") {
				t.Fatalf("staged diagnostic attachment = %s", decoded)
			}
			var snapshot struct {
				SupportTool struct {
					MCPTool string   `json:"mcpTool"`
					Command string   `json:"command"`
					Args    []string `json:"args"`
				} `json:"supportTool"`
			}
			if err := json.Unmarshal(decoded, &snapshot); err != nil {
				t.Fatal(err)
			}
			executable, _ := os.Executable()
			if snapshot.SupportTool.MCPTool != "report_problem" || snapshot.SupportTool.Command != executable || !reflect.DeepEqual(snapshot.SupportTool.Args, []string{"support-host", "--tool", "--run", prepared.ID, "--data-dir", dir}) {
				t.Fatalf("tool must use this executable and data directory rather than shell PATH: %#v", snapshot.SupportTool)
			}
		} else if attachment.MimeType != "text/markdown" || !strings.HasPrefix(string(decoded), skillassets.BugTriage()) || !strings.Contains(string(decoded), "supportTool.command") || !strings.Contains(string(decoded), "report_problem MCP tool") {
			t.Fatal("launch did not use the bundled triage playbook")
		}
	}
	if strings.Contains(launch.Prompt, "support-host") || strings.Contains(launch.Prompt, "\"operation\"") || len(launch.Prompt) > 1024 {
		t.Fatal("internal tool instructions must stay in the attached playbook, not fill the ordinary chat")
	}
	if decodedBytes > 1<<20 {
		t.Fatalf("decoded attachments exceed ordinary API limit: %d", decodedBytes)
	}
	store, err := openRunStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	release, _, err := store.claimRun(prepared.ID)
	if err != nil {
		t.Fatalf("handoff returned before releasing preparation ownership: %v", err)
	}
	release()
	manifest, _ := os.ReadFile(filepath.Join(dir, "reports", "triage", "runs", prepared.ID, "manifest.json"))
	if strings.Contains(string(manifest), "relevant private turn") {
		t.Fatal("private context leaked into the public run projection")
	}
	for _, name := range []string{"manifest.json", "session-launch.json"} {
		info, err := os.Stat(filepath.Join(dir, "reports", "triage", "runs", prepared.ID, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private %s mode: %v, %v", name, info, err)
		}
	}
}

func TestSessionHandoffRetryIsImmutableAndExternalSessionIsNeverRecovered(t *testing.T) {
	dir := t.TempDir()
	deps := configuredDeps()
	var cleanupCalls, providers atomic.Int32
	deps.RecoverConversation = func(context.Context, Run) error { cleanupCalls.Add(1); return nil }
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) {
		providers.Add(1)
		return nil, errors.New("unexpected provider")
	}
	c := serveForTest(t, dir, deps)
	origin := Origin{SessionID: "affected", Surface: "session"}
	first := handoffForTest(t, c, origin, true)
	request := first.Run.Request
	c.send(Command{ID: "retry", Type: "handoff", Start: &request})
	retry := c.response("retry")
	if !reflect.DeepEqual(retry.Launch, first.Launch) {
		t.Fatal("retry changed ordinary POST body or client request ID")
	}
	for name, mutate := range map[string]func(*StartRequest){
		"authorization": func(r *StartRequest) { r.PublishAuthorized = false },
		"selection":     func(r *StartRequest) { r.Model = "different" },
		"origin":        func(r *StartRequest) { r.Origin.SessionID = "unrelated" },
	} {
		changed := request
		mutate(&changed)
		c.send(Command{ID: name, Type: "handoff", Start: &changed})
		if response := c.response(name); response.Type != "error" {
			t.Fatalf("changed %s reused a frozen launch: %#v", name, response)
		}
	}
	c.send(Command{ID: "intent", Type: "session-create-intent", RunID: first.Run.ID})
	if response := c.response("intent"); response.Run == nil || !response.Run.SessionLaunchStarted {
		t.Fatalf("POST intent not durable: %#v", response)
	}
	c.send(Command{ID: "link", Type: "session-linked", RunID: first.Run.ID, SessionID: "ordinary-session"})
	linked := c.response("link")
	if linked.Run == nil || linked.Run.SessionID != "ordinary-session" {
		t.Fatalf("normal session link = %#v", linked)
	}
	c.send(Command{ID: "same-link", Type: "session-linked", RunID: first.Run.ID, SessionID: "ordinary-session"})
	if same := c.response("same-link"); same.Type != "run" || !same.Run.UpdatedAt.Equal(linked.Run.UpdatedAt) {
		t.Fatalf("same session link is not idempotent: %#v", same)
	}
	c.send(Command{ID: "other-link", Type: "session-linked", RunID: first.Run.ID, SessionID: "another-session"})
	if response := c.response("other-link"); response.Type != "error" {
		t.Fatal("linked one report to two ordinary sessions")
	}
	_ = c.input.Close()
	store, _ := openRunStore(dir)
	run, _ := store.get(first.Run.ID)
	run.State, run.ProviderConversationID = "running", "externally-owned-native-conversation"
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	restarted := serveForTest(t, dir, deps)
	restarted.send(Command{ID: "restart-retry", Type: "handoff", Start: &request})
	after := restarted.response("restart-retry")
	if after.Run == nil || after.Run.State != "running" || !reflect.DeepEqual(after.Launch, first.Launch) || cleanupCalls.Load() != 0 || providers.Load() != 0 {
		t.Fatalf("restart recovered an external session or changed launch: %#v, cleanup=%d, providers=%d", after, cleanupCalls.Load(), providers.Load())
	}
	restarted.send(Command{ID: "old-publish", Type: "publish", RunID: run.ID})
	if response := restarted.response("old-publish"); response.Type != "error" {
		t.Fatal("old recovery path accepted an external session publication")
	}
}

func TestSessionHandoffCancelRemainsResponsiveAndNeverLaunches(t *testing.T) {
	deps := configuredDeps()
	started := make(chan struct{})
	deps.Collect = func(ctx context.Context, _ Origin) ([]Evidence, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var providers atomic.Int32
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) {
		providers.Add(1)
		return nil, errors.New("unexpected provider")
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(Command{ID: "prepare", Type: "prepare"})
	run := c.response("prepare").Run
	awaitSignal(t, started)
	request := startCommand("handoff", true).Start
	request.PreparationID = run.ID
	c.send(Command{ID: "handoff", Type: "handoff", Start: request})
	c.wait(func(response Response) bool { return response.Run != nil && response.Run.Execution == "session" })
	c.send(Command{ID: "cancel", Type: "cancel", RunID: run.ID})
	var cancelled, handoffFailed, acknowledged bool
	c.wait(func(response Response) bool {
		cancelled = cancelled || response.Run != nil && response.Run.State == "cancelled"
		handoffFailed = handoffFailed || response.ID == "handoff" && response.Type == "error"
		acknowledged = acknowledged || response.ID == "cancel"
		return cancelled && handoffFailed && acknowledged
	})
	if providers.Load() != 0 {
		t.Fatal("cancelling a handoff started a provider")
	}
}

func TestSessionHandoffFallbackOnlyBeforeDurablePOSTIntent(t *testing.T) {
	for _, intent := range []bool{false, true} {
		t.Run(map[bool]string{false: "known-unsent", true: "possibly-sent"}[intent], func(t *testing.T) {
			dir := t.TempDir()
			deps := configuredDeps()
			var collections, providers atomic.Int32
			deps.Collect = func(context.Context, Origin) ([]Evidence, error) { collections.Add(1); return nil, nil }
			conv := fakeConversation()
			deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { providers.Add(1); return conv, nil }
			c := serveForTest(t, dir, deps)
			response := handoffForTest(t, c, Origin{Surface: "help"}, true)
			if intent {
				c.send(Command{ID: "intent", Type: "session-create-intent", RunID: response.Run.ID})
				c.response("intent")
			}
			request := response.Run.Request
			c.send(Command{ID: "fallback", Type: "start", Start: &request})
			fallback := c.response("fallback")
			if intent {
				if fallback.Type != "error" || providers.Load() != 0 || collections.Load() != 1 {
					t.Fatalf("uncertain POST started independent fallback: %#v", fallback)
				}
				return
			}
			if fallback.Run == nil || fallback.Run.ID != response.Run.ID || fallback.Run.Execution != "" || !reflect.DeepEqual(fallback.Run.Request, response.Run.Request) {
				t.Fatalf("known-unsent adoption changed frozen request: %#v", fallback)
			}
			c.state("running")
			contextTurn(t, conv)
			c.send(Command{ID: "duplicate", Type: "start", Start: &request})
			if duplicate := c.response("duplicate"); duplicate.Run.ID != response.Run.ID || providers.Load() != 1 || collections.Load() != 2 {
				t.Fatalf("fallback provider/collection counts = %d/%d, response=%#v", providers.Load(), collections.Load(), duplicate)
			}
		})
	}
}

func TestSessionHandoffExplicitPermissionsDoNotChangeConfiguredDefaults(t *testing.T) {
	deps := configuredDeps()
	deps.Catalog = func(context.Context) ([]Choice, error) {
		return []Choice{{Harness: "test", Available: true, Permissions: domain.PermissionModeDefault, Mode: "chat", Models: []ModelChoice{{ID: "chosen-model", Efforts: []string{"xhigh"}}}, Accounts: []Account{{ID: "chosen-account"}}}}, nil
	}
	var configured atomic.Int32
	deps.Configure = func(context.Context, []Choice) error {
		configured.Add(1)
		return errors.New("must not persist a per-start override")
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(Command{ID: "prepare", Type: "prepare"})
	prepared := c.response("prepare").Run
	request := startCommand("handoff", true).Start
	request.PreparationID, request.Permissions, request.Mode = prepared.ID, domain.PermissionModeBypassPermissions, "chat"
	c.send(Command{ID: "handoff", Type: "handoff", Start: request})
	response := c.response("handoff")
	if response.Run == nil || response.Run.Request.Permissions != domain.PermissionModeBypassPermissions || configured.Load() != 0 {
		t.Fatalf("per-start permission override = %#v, configure calls=%d", response, configured.Load())
	}
	c.send(Command{ID: "catalog", Type: "catalog"})
	if catalog := c.response("catalog"); len(catalog.Choices) != 1 || catalog.Choices[0].Permissions != domain.PermissionModeDefault {
		t.Fatalf("handoff changed normal configured permissions: %#v", catalog)
	}
}

func TestSessionHandoffRecoversPartialKnownUnsentRunWithoutLosingFrozenRequest(t *testing.T) {
	for _, savedLaunch := range []bool{false, true} {
		for _, fallback := range []bool{false, true} {
			t.Run(map[bool]string{false: "missing-launch", true: "saved-launch"}[savedLaunch]+"/"+map[bool]string{false: "handoff", true: "fallback"}[fallback], func(t *testing.T) {
				opts, run := sessionToolFixture(t, true, Origin{SessionID: "affected", Surface: "session"})
				run.Preparation, run.SessionLaunchStarted, run.SessionID, run.State = true, false, "", "collecting"
				store, _ := openRunStore(opts.DataDir)
				if err := store.save(run); err != nil {
					t.Fatal(err)
				}
				original, err := store.sessionLaunch(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !savedLaunch {
					dir, _ := store.runDir(run.ID)
					if err := os.Remove(filepath.Join(dir, "session-launch.json")); err != nil {
						t.Fatal(err)
					}
				}
				var collections, providers, recovery atomic.Int32
				opts.Deps.Collect = func(context.Context, Origin) ([]Evidence, error) {
					collections.Add(1)
					return []Evidence{{ID: "recollected", Status: "collected", Text: "recollected private diagnostic facts"}}, nil
				}
				conv := fakeConversation()
				opts.Deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { providers.Add(1); return conv, nil }
				opts.Deps.RecoverConversation = func(context.Context, Run) error { recovery.Add(1); return nil }
				c := serveForTest(t, opts.DataDir, opts.Deps)
				kind := "handoff"
				if fallback {
					kind = "start"
				}
				c.send(Command{ID: "resume", Type: kind, Start: &run.Request})
				response := c.response("resume")
				if response.Run == nil || response.Run.ID != run.ID || !reflect.DeepEqual(response.Run.Request, run.Request) || recovery.Load() != 0 {
					t.Fatalf("partial recovery changed ownership or request: %#v, native recovery=%d", response, recovery.Load())
				}
				if fallback {
					c.state("running")
					contextTurn(t, conv)
					if providers.Load() != 1 || collections.Load() != 1 || response.Run.Execution != "" {
						t.Fatalf("partial fallback provider/collector=%d/%d run=%#v", providers.Load(), collections.Load(), response.Run)
					}
				} else {
					if response.Launch == nil || providers.Load() != 0 {
						t.Fatalf("partial handoff launched provider: %#v", response)
					}
					if savedLaunch && (!reflect.DeepEqual(response.Launch, &original.Launch) || collections.Load() != 0) {
						t.Fatal("saved immutable payload was rebuilt")
					}
					if !savedLaunch && collections.Load() != 1 {
						t.Fatal("missing private context was not recollected")
					}
				}
			})
		}
	}
}

func sessionToolFixture(t *testing.T, authorized bool, origin Origin) (Options, Run) {
	t.Helper()
	opts := Options{DataDir: t.TempDir(), Deps: configuredDeps()}
	now := time.Now().UTC()
	run := Run{ID: "tool-run", Version: ProtocolVersion, CreatedAt: now, UpdatedAt: now,
		Execution: "session", SessionID: "ordinary-investigation", SessionLaunchStarted: true,
		State: "session_linked", Messages: []Message{},
		Request: *startCommand("start", authorized).Start}
	run.Request.PreparationID, run.Request.Origin = run.ID, origin
	store, err := openRunStore(opts.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	launch, err := buildSessionLaunch(run, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveSessionLaunch(run.ID, sessionLaunchRecord{Request: run.Request, Launch: launch}); err != nil {
		t.Fatal(err)
	}
	return opts, run
}

func callSessionTool(t *testing.T, opts Options, id string, cmd ToolCommand) ToolResponse {
	t.Helper()
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ServeTool(t.Context(), bytes.NewReader(raw), &out, id, opts); err != nil {
		t.Fatal(err)
	}
	var response ToolResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Version != ProtocolVersion {
		t.Fatalf("tool response = %s, error=%v", out.String(), err)
	}
	return response
}

func TestSessionToolsAreExplicitBoundedAndNeverRecoverOrLaunch(t *testing.T) {
	opts, run := sessionToolFixture(t, true, Origin{SessionID: "affected"})
	var providers, recovery atomic.Int32
	opts.Deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { providers.Add(1); return nil, nil }
	opts.Deps.RecoverConversation = func(context.Context, Run) error { recovery.Add(1); return nil }
	for _, input := range []string{"", "ordinary agent prose", `{"operation":"get","origin":{"sessionId":"other"}}`, `{"operation":"get"} {"operation":"publish"}`, strings.Repeat(" ", maxCommandBytes+1)} {
		var out bytes.Buffer
		if err := ServeTool(t.Context(), strings.NewReader(input), &out, run.ID, opts); err != nil {
			t.Fatal(err)
		}
		var response ToolResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Status != "invalid_request" {
			t.Fatalf("invalid tool input response = %s, error=%v", out.String(), err)
		}
	}
	store, _ := openRunStore(opts.DataDir)
	releaseGlobal, _, err := claim(filepath.Join(store.root, "active.lock"), "unrelated-run")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseGlobal()
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "get"}); response.Status != "ok" || response.Origin.SessionID != "affected" || response.Run.SessionID != "ordinary-investigation" {
		t.Fatalf("one-shot get = %#v", response)
	}
	if providers.Load() != 0 || recovery.Load() != 0 {
		t.Fatalf("tool launched/recovered providers: %d/%d", providers.Load(), recovery.Load())
	}
	run.Execution = ""
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "get"}); response.Status != "unavailable" {
		t.Fatal("tool accepted a provider-owned legacy run")
	}
}

func TestSessionToolsReadFrozenOriginKeepPrivateChunksOutOfManifestAndSerialize(t *testing.T) {
	origin := Origin{SessionID: "affected", Surface: "session"}
	opts, run := sessionToolFixture(t, true, origin)
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	opts.Deps.ReadContext = func(ctx context.Context, got Origin, request ContextRead) (Evidence, error) {
		if !reflect.DeepEqual(got, origin) || request.Source != "session-history" {
			t.Errorf("context binding changed: %#v, %#v", got, request)
		}
		reads.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return Evidence{}, ctx.Err()
		}
		return Evidence{ID: "private", Status: "collected", Text: "private retained excerpt token=synthetic-private-token"}, nil
	}
	request, _ := json.Marshal(ToolCommand{Operation: "context-read", Request: &ContextRead{Source: "session-history"}})
	done := make(chan error, 1)
	var output bytes.Buffer
	go func() { done <- ServeTool(context.Background(), bytes.NewReader(request), &output, run.ID, opts) }()
	awaitSignal(t, started)
	if concurrent := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "get"}); concurrent.Status != "unavailable" {
		t.Fatalf("two helpers owned one run: %#v", concurrent)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var response ToolResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Status != "ok" || !strings.Contains(response.Evidence.Text, "private retained excerpt") || strings.Contains(output.String(), "synthetic-private-token") {
		t.Fatalf("bounded private read = %s, error=%v", output.String(), err)
	}
	manifest, _ := os.ReadFile(filepath.Join(opts.DataDir, "reports", "triage", "runs", run.ID, "manifest.json"))
	if strings.Contains(string(manifest), "private retained excerpt") || !strings.Contains(string(manifest), "contents are not attached automatically") || reads.Load() != 1 {
		t.Fatalf("saved raw private context: %s", manifest)
	}
}

func TestSessionToolsBindOnlyHumanSelectedInitialCandidateWithoutChangingFrozenLaunch(t *testing.T) {
	opts, run := sessionToolFixture(t, true, Origin{Surface: "help"})
	store, _ := openRunStore(opts.DataDir)
	run.Evidence = []Evidence{{ID: "session-candidates", Status: "collected", Values: map[string]any{"sessions": []DiagnosticSession{{ID: "offered-session", Label: "My affected session"}}}}}
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	var validations atomic.Int32
	opts.Deps.ValidateSession = func(_ context.Context, id string) error {
		validations.Add(1)
		if id != "offered-session" {
			return errors.New("unexpected session")
		}
		return nil
	}
	opts.Deps.Collect = nil
	for _, cmd := range []ToolCommand{
		{Operation: "bind-session", SessionID: "offered-session"},
		{Operation: "bind-session", SessionID: "forged-session", UserSelected: true},
	} {
		if response := callSessionTool(t, opts, run.ID, cmd); response.Status == "ok" {
			t.Fatal("bound an unselected or undiscovered session")
		}
	}
	bound := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "bind-session", SessionID: "offered-session", UserSelected: true})
	if bound.Status != "ok" || bound.Origin.SessionID != "offered-session" || bound.Run.Request.Origin.SessionID != "" || validations.Load() != 1 {
		t.Fatalf("explicit binding = %#v, validations=%d", bound, validations.Load())
	}
	opts.Deps.ReadContext = func(_ context.Context, origin Origin, _ ContextRead) (Evidence, error) {
		if origin.SessionID != "offered-session" {
			t.Errorf("read bound to %q", origin.SessionID)
		}
		return Evidence{ID: "read", Status: "collected"}, nil
	}
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "context-read", Request: &ContextRead{Source: "session-history"}}); response.Status != "ok" {
		t.Fatalf("bound context unavailable: %#v", response)
	}
	record, err := store.sessionLaunch(run.ID)
	if err != nil || record.Request.Origin.SessionID != "" || !sameHandoffRequest(run.Request, record.Request) {
		t.Fatalf("binding changed the frozen launch request: %#v, %v", record.Request, err)
	}
}

func TestSessionToolsRequireAuthorizationAndSuccessfulSearchBeforePublication(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-authorized", true: "authorized"}[authorized], func(t *testing.T) {
			opts, run := sessionToolFixture(t, authorized, Origin{})
			var publications atomic.Int32
			opts.Deps.Publish = func(_ context.Context, run Run, save func(Publication) error) (Publication, error) {
				publications.Add(1)
				pub, err := preparePublication(run)
				if err != nil {
					return pub, err
				}
				pub.Repository, pub.State, pub.IssueURL = canonicalRepository, PublicationPublished, "https://github.com/"+canonicalRepository+"/issues/12"
				return pub, save(pub)
			}
			draft := &Draft{Title: "Human symptom", Body: "The human observed a blank screen.", Evidence: "Selected fact only."}
			if saved := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "save", Draft: draft}); saved.Status != "ok" {
				t.Fatalf("explicit save = %#v", saved)
			}
			if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "publish"}); response.Status == "ok" || publications.Load() != 0 {
				t.Fatalf("publication without search/authorization = %#v", response)
			}
			opts.Deps.SearchReports = func(context.Context, string) (ReportSearch, error) {
				return ReportSearch{}, errors.New("GitHub sign-in is required")
			}
			if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "search", Query: "blank screen"}); response.Status != PublicationAuthRequired {
				t.Fatalf("auth recovery was not structured: %#v", response)
			}
			if saved := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "get"}); saved.Run.SearchCompleted || !saved.Run.SearchAuthRequired {
				t.Fatalf("search sign-in requirement was not durable: %#v", saved.Run)
			}
			if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "publish"}); response.Status == "ok" || publications.Load() != 0 {
				t.Fatal("failed search unlocked publication")
			}
			opts.Deps.SearchReports = func(context.Context, string) (ReportSearch, error) {
				return ReportSearch{Repository: canonicalRepository, Query: "blank screen", Candidates: []ReportCandidate{}}, nil
			}
			if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "search", Query: "blank screen"}); response.Status != "ok" {
				t.Fatalf("successful search = %#v", response)
			}
			if saved := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "get"}); !saved.Run.SearchCompleted || saved.Run.SearchAuthRequired {
				t.Fatalf("successful search did not clear sign-in requirement: %#v", saved.Run)
			}
			response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "publish"})
			if authorized && (response.Status != "ok" || publications.Load() != 1 || response.Run.Publication.IssueURL == "") {
				t.Fatalf("authorized searched publication = %#v", response)
			}
			if !authorized && (response.Status != "not_authorized" || publications.Load() != 0) {
				t.Fatalf("search replaced initial authorization = %#v", response)
			}
		})
	}
}

func TestSessionToolsContextBudgetSurvivesSeparateProcesses(t *testing.T) {
	opts, run := sessionToolFixture(t, false, Origin{SessionID: "affected"})
	var calls atomic.Int32
	opts.Deps.ReadContext = func(context.Context, Origin, ContextRead) (Evidence, error) {
		calls.Add(1)
		return Evidence{ID: "bounded", Status: "collected", Text: "small chunk"}, nil
	}
	for n := 0; n < maxContextReads; n++ {
		if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "context-read", Request: &ContextRead{Source: "session-history"}}); response.Status != "ok" {
			t.Fatalf("bounded read %d failed: %#v", n+1, response)
		}
	}
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "context-read", Request: &ContextRead{Source: "session-history"}}); response.Status == "ok" || calls.Load() != maxContextReads {
		t.Fatalf("process restart reset context limit: %#v, calls=%d", response, calls.Load())
	}
}

func TestSessionHostPublicationRetryAfterSignInNeverStartsAnotherProvider(t *testing.T) {
	opts, run := sessionToolFixture(t, true, Origin{})
	opts.Deps.SearchReports = func(context.Context, string) (ReportSearch, error) {
		return ReportSearch{Repository: canonicalRepository, Candidates: []ReportCandidate{}}, nil
	}
	callSessionTool(t, opts, run.ID, ToolCommand{Operation: "search", Query: "blank screen"})
	callSessionTool(t, opts, run.ID, ToolCommand{Operation: "save", Draft: &Draft{Title: "Blank screen", Body: "The human saw a blank screen."}})
	var calls, providers, recovery atomic.Int32
	opts.Deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) {
		providers.Add(1)
		return nil, errors.New("unexpected provider")
	}
	opts.Deps.RecoverConversation = func(context.Context, Run) error { recovery.Add(1); return nil }
	var frozen Publication
	opts.Deps.Publish = func(_ context.Context, run Run, save func(Publication) error) (Publication, error) {
		if calls.Add(1) == 1 {
			pub, err := preparePublication(run)
			if err != nil {
				return pub, err
			}
			pub.Repository, pub.State = canonicalRepository, PublicationAuthRequired
			frozen = pub
			if err := save(pub); err != nil {
				return Publication{}, err
			}
			return pub, errors.New("GitHub sign-in is required")
		}
		if !reflect.DeepEqual(run.Publication, frozen) {
			t.Errorf("sign-in retry changed frozen publication: %#v", run.Publication)
		}
		pub := frozen
		pub.State, pub.IssueURL = PublicationPublished, "https://github.com/"+canonicalRepository+"/issues/18"
		return pub, save(pub)
	}
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "publish"}); response.Status != PublicationAuthRequired {
		t.Fatalf("initial sign-in failure = %#v", response)
	}
	c := serveForTest(t, opts.DataDir, opts.Deps)
	c.send(Command{ID: "retry", Type: "publish", RunID: run.ID})
	_, published := c.responseAndState("retry", "published")
	if calls.Load() != 2 || providers.Load() != 0 || recovery.Load() != 0 || published.SessionID != run.SessionID || published.Publication.IssueURL == "" {
		t.Fatalf("session retry launched/recovered a provider: publications=%d providers=%d recovery=%d run=%#v", calls.Load(), providers.Load(), recovery.Load(), published)
	}
}

func TestSessionHostPublicationRetryUsesActiveInboxAndReady(t *testing.T) {
	deps := configuredDeps()
	conv := fakeConversation()
	var providers, publications atomic.Int32
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { providers.Add(1); return conv, nil }
	deps.Publish = func(_ context.Context, _ Run, save func(Publication) error) (Publication, error) {
		pub := Publication{State: PublicationAuthRequired}
		if publications.Add(1) == 2 {
			pub.State, pub.IssueURL = PublicationPublished, "https://github.com/"+canonicalRepository+"/issues/19"
		}
		return pub, save(pub)
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", true))
	run := c.response("start").Run
	c.state("running")
	contextTurn(t, conv)
	finishWith(conv, `{"title":"Blank screen","body":"The human observed a blank screen."}`)
	c.wait(func(response Response) bool {
		return response.Run != nil && response.Run.Publication.State == PublicationAuthRequired
	})
	c.send(Command{ID: "retry", Type: "publish", RunID: run.ID})
	if response := c.response("retry"); response.Type != "run" || publications.Load() != 1 {
		t.Fatalf("active retry missed its inbox or ignored Ready: %#v, publications=%d", response, publications.Load())
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady, ProviderConversationID: conv.ProviderConversationID(), ProviderTurnID: "turn-1"}
	c.wait(func(response Response) bool {
		return response.Run != nil && response.Run.Publication.State == PublicationPublished
	})
	if publications.Load() != 2 || providers.Load() != 1 || conv.turns.Load() != 1 {
		t.Fatalf("retry replaced the live provider: publications=%d providers=%d turns=%d", publications.Load(), providers.Load(), conv.turns.Load())
	}
	c.send(Command{ID: "cancel", Type: "cancel", RunID: run.ID})
	c.responseAndState("cancel", "interrupted")
	awaitSignal(t, conv.terminated)
}

func TestSessionHostPublicationLockFailureStaysOnItsRunAndKeepsHelperAlive(t *testing.T) {
	opts, run := sessionToolFixture(t, true, Origin{})
	opts.Deps.SearchReports = func(context.Context, string) (ReportSearch, error) {
		return ReportSearch{Repository: canonicalRepository, Candidates: []ReportCandidate{}}, nil
	}
	callSessionTool(t, opts, run.ID, ToolCommand{Operation: "search", Query: "blank screen"})
	callSessionTool(t, opts, run.ID, ToolCommand{Operation: "save", Draft: &Draft{Title: "Blank screen", Body: "The human saw a blank screen."}})
	store, _ := openRunStore(opts.DataDir)
	_, release, err := store.claimSessionRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	c := serveForTest(t, opts.DataDir, opts.Deps)
	c.send(Command{ID: "retry", Type: "publish", RunID: run.ID})
	var acknowledged, runError bool
	c.wait(func(response Response) bool {
		if response.Type == "error" && response.ID == "" {
			t.Fatal("publication error became a fatal uncorrelated protocol error")
		}
		acknowledged = acknowledged || response.ID == "retry"
		runError = runError || response.Run != nil && response.Run.ID == run.ID && response.Run.Error != ""
		return acknowledged && runError
	})
	c.send(Command{ID: "still-alive", Type: "catalog"})
	if response := c.response("still-alive"); response.Type != "catalog" {
		t.Fatalf("publication lock failure killed helper: %#v", response)
	}
}

func TestSessionLinkedWaitsForToolOwnershipWithoutBlockingOtherCommands(t *testing.T) {
	opts, run := sessionToolFixture(t, true, Origin{SessionID: "affected"})
	store, _ := openRunStore(opts.DataDir)
	run.SessionID = ""
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	opts.Deps.ReadContext = func(ctx context.Context, _ Origin, _ ContextRead) (Evidence, error) {
		close(started)
		select {
		case <-release:
			return Evidence{ID: "read", Status: "collected"}, nil
		case <-ctx.Done():
			return Evidence{}, ctx.Err()
		}
	}
	request, _ := json.Marshal(ToolCommand{Operation: "context-read", Request: &ContextRead{Source: "session-history"}})
	done := make(chan error, 1)
	var output bytes.Buffer
	go func() { done <- ServeTool(context.Background(), bytes.NewReader(request), &output, run.ID, opts) }()
	awaitSignal(t, started)
	c := serveForTest(t, opts.DataDir, opts.Deps)
	c.send(Command{ID: "linked", Type: "session-linked", RunID: run.ID, SessionID: "new-ordinary-session"})
	c.send(Command{ID: "responsive", Type: "catalog"})
	if response := c.response("responsive"); response.Type != "catalog" {
		t.Fatal("session link blocked command processing")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if response := c.response("linked"); response.Run == nil || response.Run.SessionID != "new-ordinary-session" {
		t.Fatalf("tool/POST race lost the created session link: %#v", response)
	}
}

func TestSessionToolsVerifyCandidateAndKeepUncertainPublicationFrozenOnRetry(t *testing.T) {
	opts, run := sessionToolFixture(t, true, Origin{})
	url := "https://github.com/" + canonicalRepository + "/pull/42"
	opts.Deps.SearchReports = func(context.Context, string) (ReportSearch, error) {
		return ReportSearch{Repository: canonicalRepository, Query: "blank screen", Candidates: []ReportCandidate{{URL: url, IsPullRequest: true, Merged: true}}}, nil
	}
	callSessionTool(t, opts, run.ID, ToolCommand{Operation: "search", Query: "blank screen"})
	draft := Draft{Title: "Human symptom", Body: "The human observed a blank screen.", ExistingReportURL: "https://github.com/" + canonicalRepository + "/issues/99", ExistingReportReason: "This report describes the same observed blank-screen symptom and affected build."}
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "save", Draft: &draft}); response.Status == "ok" {
		t.Fatal("an invented related-report URL was saved")
	}
	draft.ExistingReportURL = url
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "save", Draft: &draft}); response.Status != "ok" {
		t.Fatalf("verified candidate was rejected: %#v", response)
	}
	var calls int
	var frozen Publication
	opts.Deps.Publish = func(_ context.Context, run Run, save func(Publication) error) (Publication, error) {
		calls++
		if calls == 1 {
			pub, err := preparePublication(run)
			if err != nil {
				return pub, err
			}
			pub.Repository, pub.State = canonicalRepository, PublicationIssueUnknown
			frozen = pub
			if err := save(pub); err != nil {
				return Publication{}, err
			}
			return Publication{}, errors.New("POST outcome is unknown")
		}
		if !reflect.DeepEqual(run.Publication, frozen) {
			t.Errorf("retry replaced frozen publication: %#v", run.Publication)
		}
		pub := frozen
		pub.State, pub.IssueURL = PublicationPublished, "https://github.com/"+canonicalRepository+"/issues/15"
		return pub, save(pub)
	}
	uncertain := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "publish"})
	if uncertain.Status != "unavailable" || uncertain.Run.Publication.State != PublicationIssueUnknown {
		t.Fatalf("uncertain outcome was erased: %#v", uncertain)
	}
	changed := draft
	changed.Body = "A changed report that must not replace an uncertain request."
	if response := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "save", Draft: &changed}); response.Status == "ok" {
		t.Fatal("uncertain publication body was replaced")
	}
	if retried := callSessionTool(t, opts, run.ID, ToolCommand{Operation: "publish"}); retried.Status != "ok" || calls != 2 || retried.Run.Publication.IssueURL == "" {
		t.Fatalf("frozen retry = %#v, calls=%d", retried, calls)
	}
}
