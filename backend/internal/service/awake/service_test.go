package awake

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type memoryStore struct {
	document string
	fail     bool
	writes   int
}

func (m *memoryStore) LoadAwake(context.Context) (string, error) {
	if m.document == "" {
		return "{}", nil
	}
	return m.document, nil
}
func (m *memoryStore) SaveAwake(_ context.Context, d string) error {
	if m.fail {
		return errors.New("disk full")
	}
	m.document = d
	m.writes++
	return nil
}

type fakeHost struct {
	active, lid           bool
	observes              int
	setups, starts, stops int
	setupError            error
	observation           domain.AwakeObservation
	deadline              time.Time
	reason                string
	percent               int
	browserCalls          int
	browserError          error
	networkError          error
}

func (h *fakeHost) Supported() bool { return true }
func (h *fakeHost) Active() bool    { return h.active }
func (h *fakeHost) Start() error {
	if !h.active {
		h.starts++
	}
	h.active = true
	return nil
}
func (h *fakeHost) Stop() {
	if h.active {
		h.stops++
	}
	h.active = false
	h.lid = false
}
func (h *fakeHost) ConfigureLid(on, _ bool) error           { h.lid = on; return nil }
func (h *fakeHost) SetupLid() error                         { h.setups++; return h.setupError }
func (h *fakeHost) LidStatus() (bool, bool, string)         { return true, h.active && h.lid, h.reason }
func (h *fakeHost) ConfigureIdle(d time.Time, _ bool) error { h.deadline = d; return nil }
func (h *fakeHost) SetDeadline(d time.Time) error           { h.deadline = d; return nil }
func (h *fakeHost) SetProtection(p int) error               { h.percent = p; return nil }
func (h *fakeHost) Observe(context.Context) (domain.AwakeObservation, error) {
	h.observes++
	return h.observation, nil
}
func (h *fakeHost) Capabilities() []domain.AwakeCapability {
	return []domain.AwakeCapability{{ID: "idle", Available: true}, {ID: "lid", Available: true}, {ID: "native_guard", Available: true}, {ID: "agent_reconnect"}, {ID: "low_power", Reason: "authorization required"}}
}

type fakeBindings struct {
	alive          bool
	unknown        bool
	agent          bool
	recovery       bool
	targets        []domain.AwakeAgentTarget
	reconnectCalls int
	reconnectError error
}

func (b *fakeBindings) Alive(context.Context, string, string, string) (bool, string, error) {
	if b.unknown {
		return false, "", errors.New("probe failed")
	}
	return b.alive, "pid:start", nil
}
func (b *fakeBindings) AgentActive(context.Context) (bool, error) { return b.agent, nil }
func fixture(t *testing.T) (*Service, *memoryStore, *fakeHost, *fakeBindings, *time.Time) {
	t.Helper()
	store := &memoryStore{}
	ac := false
	percent := 80
	host := &fakeHost{observation: domain.AwakeObservation{OnBattery: &ac, BatteryPercent: &percent, Thermal: "nominal"}}
	bindings := &fakeBindings{alive: true}
	s, err := New(context.Background(), store, host, bindings)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, store, host, bindings, &now
}
func tick(t *testing.T, s *Service) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(context.Background())
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func start(t *testing.T, s *Service, r domain.StartAwakeRequest) {
	t.Helper()
	if err := s.Start(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestOverlappingScopesReleaseOnlyTheirOwnRequest(t *testing.T) {
	s, _, h, _, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Scope: "ao", Mode: "indefinite", LidClosed: true})
	first := s.Status().Sessions[0].ID
	start(t, s, domain.StartAwakeRequest{Scope: "session", SessionID: "work-1", Mode: "indefinite", LidClosed: true})
	if h.stops != 0 {
		t.Fatal("setup withdrew existing native ownership")
	}
	if err := s.End(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Active || h.starts != 1 {
		t.Fatal("ending one scope canceled another")
	}
	if err := s.End(context.Background(), s.Status().Sessions[1].ID); err != nil {
		t.Fatal(err)
	}
	if s.Status().Active {
		t.Fatal("final owner did not release")
	}
}
func TestDurationAndExtendUseNativeDeadline(t *testing.T) {
	s, _, h, _, now := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "duration", DurationSeconds: 60})
	id := s.Status().Sessions[0].ID
	if !h.deadline.Equal(now.Add(time.Minute)) {
		t.Fatal("native deadline missing")
	}
	if err := s.Extend(context.Background(), id, 30); err != nil {
		t.Fatal(err)
	}
	if !h.deadline.Equal(now.Add(90 * time.Second)) {
		t.Fatal("native extension missing")
	}
	*now = now.Add(91 * time.Second)
	tick(t, s)
	state := s.Status()
	if state.Active || state.Sessions[0].EndReason != "expired" {
		t.Fatalf("duration did not expire: %+v", state)
	}
}
func TestSetupFailurePreservesAllExistingIntent(t *testing.T) {
	s, store, h, _, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	before := store.document
	writes := store.writes
	h.setupError = errors.New("registered helper upgrade required")
	if err := s.Start(context.Background(), domain.StartAwakeRequest{Mode: "indefinite", LidClosed: true}); err == nil {
		t.Fatal("setup failure ignored")
	}
	if store.document != before || store.writes != writes || !h.active || h.stops != 0 {
		t.Fatal("failed setup changed active sources")
	}
}
func TestPersistenceFailureDoesNotWithdrawExistingOwner(t *testing.T) {
	s, store, h, _, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	store.fail = true
	if err := s.Start(context.Background(), domain.StartAwakeRequest{Mode: "duration", DurationSeconds: 60}); err == nil {
		t.Fatal("save failure ignored")
	}
	if !h.active || len(s.Status().Sessions) != 1 {
		t.Fatal("save failure corrupted ownership")
	}
}
func TestUnknownBindingIsNotTerminationOrPositiveWakeEvidence(t *testing.T) {
	s, _, h, b, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Scope: "session", SessionID: "work-1", Mode: "indefinite"})
	b.unknown = true
	tick(t, s)
	if h.active || s.Status().Sessions[0].EndedAt != nil {
		t.Fatal("unknown binding was treated as alive or dead")
	}
	b.unknown = false
	tick(t, s)
	if !h.active {
		t.Fatal("fresh live binding did not recover")
	}
	b.alive = false
	tick(t, s)
	if h.active || s.Status().Sessions[0].EndReason != "session_ended" {
		t.Fatal("known termination not released")
	}
}
func TestAppBindingFencesOriginalIdentity(t *testing.T) {
	s, _, _, b, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "app", BoundID: "com.example.editor"})
	if s.Status().Sessions[0].BoundIdentity != "pid:start" {
		t.Fatal("application identity not recorded")
	}
	b.alive = false
	tick(t, s)
	if s.Status().Sessions[0].EndReason != "app_ended" {
		t.Fatal("app lifetime not followed")
	}
}
func TestDownloadUsesFreshTypedEvents(t *testing.T) {
	s, _, h, _, _ := fixture(t)
	if err := s.Downloads([]string{"download-1"}); err != nil {
		t.Fatal(err)
	}
	start(t, s, domain.StartAwakeRequest{Mode: "download", BoundID: "download-1"})
	if !h.active {
		t.Fatal("download did not hold wake")
	}
	if err := s.Downloads([]string{}); err != nil {
		t.Fatal(err)
	}
	tick(t, s)
	if h.active || s.Status().Sessions[0].EndReason != "download_ended" {
		t.Fatal("download completion did not release")
	}
}
func TestSafetyLatchRequiresExplicitFreshRearm(t *testing.T) {
	s, _, h, _, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite", LidClosed: true})
	h.observation.Thermal = "critical"
	tick(t, s)
	if h.active || s.Status().Reason != "critical_heat" {
		t.Fatal("critical thermal state did not release")
	}
	h.observation.Thermal = "nominal"
	if err := s.Start(context.Background(), domain.StartAwakeRequest{Mode: "indefinite"}); err == nil {
		t.Fatal("new source cleared a safety latch")
	}
	h.observation.OnBattery = nil
	if err := s.Resume(context.Background()); err == nil {
		t.Fatal("unknown power cleared a safety latch")
	}
	ac := false
	h.observation.OnBattery = &ac
	if err := s.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.active || s.Status().Reason != "" {
		t.Fatal("explicit safe rearm did not recover")
	}
}
func TestStopAllSuppressesRulesButAllowsManualWake(t *testing.T) {
	s, _, h, b, now := fixture(t)
	b.agent = true
	rule := domain.AwakeRule{ID: "rule", Name: "Agent work", Enabled: true, Match: "all", Conditions: []domain.AwakeCondition{{Kind: "agent_activity"}}}
	if err := s.UpdateRules(context.Background(), []domain.AwakeRule{rule}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(5 * time.Second)
	tick(t, s)
	if !h.active {
		t.Fatal("eligible rule not active")
	}
	if err := s.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	if !h.active || len(s.Status().ActiveRules) != 0 || !s.Status().Paused {
		t.Fatal("manual wake silently resumed rules")
	}
	if err := s.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.Status().ActiveRules) != 1 {
		t.Fatal("explicit resume failed")
	}
}
func TestRestartDoesNotResurrectManualSessions(t *testing.T) {
	s, store, _, b, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	next, err := New(context.Background(), store, &fakeHost{}, b)
	if err != nil {
		t.Fatal(err)
	}
	state := next.Status()
	if state.Active || state.Sessions[0].EndReason != "interrupted" || state.Sessions[0].EndedAt == nil {
		t.Fatal("manual request resurrected")
	}
	var persisted domain.AwakeDocument
	if err := json.Unmarshal([]byte(store.document), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Sessions[0].EndedAt == nil {
		t.Fatal("interruption not durable")
	}
}
func TestUnknownConditionsCannotActivateOrClearSuppression(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	s.observation = domain.AwakeObservation{}
	rule := domain.AwakeRule{ID: "rule", Name: "AC", Enabled: true, Suppressed: true, Match: "all", Conditions: []domain.AwakeCondition{{Kind: "power", Value: "ac"}}}
	s.doc.Rules = []domain.AwakeRule{rule}
	tick(t, s)
	// fixture supplies known AC; switch it to unknown and check the raw three-valued evaluator.
	s.observation.OnBattery = nil
	if got := s.matchRule(context.Background(), rule); got != -1 {
		t.Fatalf("unknown became %d", got)
	}
}
func TestUnavailableActionsAreNotSavedAsEffective(t *testing.T) {
	s, store, _, _, _ := fixture(t)
	settings := s.Status().Settings
	settings.LowPowerBatteryPercent = 40
	if err := s.UpdateSettings(context.Background(), settings); !errors.Is(err, ErrValidation) {
		t.Fatalf("unavailable action accepted: %v", err)
	}
	if store.writes != 0 {
		t.Fatal("unavailable action persisted")
	}
	settings.LowPowerBatteryPercent = 0
	settings.StopBatteryPercent = 9
	if err := s.UpdateSettings(context.Background(), settings); !errors.Is(err, ErrValidation) {
		t.Fatal("hard battery floor weakened")
	}
}

func (h *fakeHost) ConfigureEffects(domain.AwakeSettings) error { return nil }
func (h *fakeHost) RequestRecovery() error                      { return nil }
func (h *fakeHost) RecoveryReason() string                      { return "" }

func (h *fakeHost) RelaunchBrowser(context.Context, string) error {
	h.browserCalls++
	return h.browserError
}

func (h *fakeHost) JoinNetwork(context.Context, string) error { return h.networkError }

func TestBoundRequestsExpireWithoutFreshPolicyRenewal(t *testing.T) {
	s, _, h, _, now := fixture(t)
	start(t, s, domain.StartAwakeRequest{Scope: "session", SessionID: "work", Mode: "indefinite"})
	if !h.deadline.Equal(now.Add(15 * time.Second)) {
		t.Fatal("session binding has unbounded native lease")
	}
	*now = now.Add(5 * time.Second)
	tick(t, s)
	if !h.deadline.Equal(now.Add(15 * time.Second)) {
		t.Fatal("fresh binding did not renew native deadline")
	}
}
func TestMissingDownloadHeartbeatIsNotCompletion(t *testing.T) {
	s, _, h, _, now := fixture(t)
	if err := s.Downloads([]string{"one"}); err != nil {
		t.Fatal(err)
	}
	start(t, s, domain.StartAwakeRequest{Mode: "download", BoundID: "one"})
	*now = now.Add(20 * time.Second)
	tick(t, s)
	if h.active || s.Status().Sessions[0].EndReason != "download_observation_expired" {
		t.Fatal("lost heartbeat reported as completion or kept awake")
	}
}
func TestRulesDebounceUnknownAndBatteryHysteresis(t *testing.T) {
	s, _, h, _, now := fixture(t)
	rule := domain.AwakeRule{ID: "battery", Name: "Battery", Enabled: true, Match: "all", Conditions: []domain.AwakeCondition{{Kind: "battery", Threshold: 40}}}
	if err := s.UpdateRules(context.Background(), []domain.AwakeRule{rule}); err != nil {
		t.Fatal(err)
	}
	if h.active {
		t.Fatal("one sample activated rule")
	}
	*now = now.Add(5 * time.Second)
	tick(t, s)
	if !h.active {
		t.Fatal("stable observations did not activate")
	}
	percent := 38
	h.observation.BatteryPercent = &percent
	tick(t, s)
	if !h.active {
		t.Fatal("battery threshold oscillation dropped ownership")
	}
	h.observation.BatteryPercent = nil
	tick(t, s)
	if h.active {
		t.Fatal("unknown battery kept automatic ownership")
	}
	percent = 45
	h.observation.BatteryPercent = &percent
	tick(t, s)
	if h.active {
		t.Fatal("unknown did not reset debounce")
	}
	*now = now.Add(5 * time.Second)
	tick(t, s)
	if !h.active {
		t.Fatal("fresh observations did not recover")
	}
	*now = now.Add(30 * time.Second)
	tick(t, s)
	if h.active {
		t.Fatal("stale observation window renewed ownership")
	}
}
func TestBrowserRetriesNeverRepeatUncertainSideEffect(t *testing.T) {
	s, store, h, _, _ := fixture(t)
	s.doc.Settings.BrowserActions = true
	h.observation.Browsers = []domain.AwakeApplication{{ID: "com.google.Chrome"}}
	id := "b5073e79-5813-452f-95ba-b5c841eb72ba"
	h.browserError = errors.New("launch unconfirmed")
	if err := s.RelaunchBrowser(context.Background(), id, "com.google.Chrome", true); err == nil {
		t.Fatal("action failure hidden")
	}
	if h.browserCalls != 1 {
		t.Fatal("action not attempted")
	}
	if err := s.RelaunchBrowser(context.Background(), id, "com.google.Chrome", true); err == nil || h.browserCalls != 1 {
		t.Fatal("uncertain action was repeated")
	}
	store.fail = true
	if err := s.RelaunchBrowser(context.Background(), "06678c05-840a-48a0-baca-4c9d762a05d8", "com.google.Chrome", true); err == nil || h.browserCalls != 1 {
		t.Fatal("side effect happened before journal persistence")
	}
}
func TestNetworkRetryBudgetCooldownAndCancellation(t *testing.T) {
	s, _, h, _, now := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	disconnected := false
	s.observation.NetworkConnected = &disconnected
	s.doc.Settings.HotspotRecovery = true
	s.doc.Settings.HotspotName = "saved"
	ctx := context.Background()
	if task := s.networkTask(ctx); task != nil {
		t.Fatal("immediate handoff")
	}
	*now = now.Add(10 * time.Second)
	task := s.networkTask(ctx)
	if task == nil {
		t.Fatal("sustained outage ignored")
	}
	h.networkError = errors.New("join failed")
	s.runNetwork(ctx, *task)
	if s.doc.Network.Outcome != "failed" {
		t.Fatal("failure misclassified as cancelled")
	}
	if s.networkTask(ctx) != nil {
		t.Fatal("cooldown ignored")
	}
	*now = now.Add(30 * time.Second)
	task = s.networkTask(ctx)
	if task == nil {
		t.Fatal("retry missing")
	}
	s.networkCancel()
	s.runNetwork(ctx, *task)
	if s.doc.Network.Outcome != "cancelled" {
		t.Fatal("cancellation not recorded")
	}
	*now = now.Add(30 * time.Second)
	task = s.networkTask(ctx)
	if task == nil {
		t.Fatal("third attempt missing")
	}
	s.runNetwork(ctx, *task)
	*now = now.Add(30 * time.Second)
	if s.networkTask(ctx) != nil {
		t.Fatal("retry budget exceeded")
	}
}
func TestShutdownPersistsEndedRequestsAndClosesAdmission(t *testing.T) {
	s, store, h, _, _ := fixture(t)
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
	if h.active || s.Status().Sessions[0].EndReason != "shutdown" {
		t.Fatal("shutdown kept ownership")
	}
	var doc domain.AwakeDocument
	if err := json.Unmarshal([]byte(store.document), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Sessions[0].EndReason != "shutdown" {
		t.Fatal("shutdown fact not saved")
	}
	if err := s.Start(context.Background(), domain.StartAwakeRequest{}); err == nil {
		t.Fatal("shutdown admitted new request")
	}
}

func (b *fakeBindings) AgentRecoverySupported() bool                 { return b.recovery }
func (b *fakeBindings) InterruptedAgents() []domain.AwakeAgentTarget { return b.targets }
func (b *fakeBindings) ReconnectAgent(context.Context, domain.AwakeAgentTarget) error {
	b.reconnectCalls++
	return b.reconnectError
}
func TestAwakeAgentRecoveryRequiresEvidenceAndNeverRepeatsGeneration(t *testing.T) {
	s, store, _, b, _ := fixture(t)
	b.recovery = true
	connected := true
	s.observation.NetworkConnected = &connected
	settings := s.Status().Settings
	settings.AgentReconnect = true
	if err := s.UpdateSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if s.agentTask(context.Background()) != nil {
		t.Fatal("reconnected without awake ownership")
	}
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	s.observation.NetworkConnected = &connected
	if s.agentTask(context.Background()) != nil {
		t.Fatal("invented interrupted transport")
	}
	b.targets = []domain.AwakeAgentTarget{{SessionID: "work", Owner: domain.SessionControllerOwner{ControllerGeneration: "one", ProviderConversationID: "thread"}}}
	s.observation.NetworkConnected = nil
	if s.agentTask(context.Background()) != nil {
		t.Fatal("unknown network authorized reconnect")
	}
	s.observation.NetworkConnected = &connected
	store.fail = true
	if s.agentTask(context.Background()) != nil || b.reconnectCalls != 0 {
		t.Fatal("reconnect happened without durable intent")
	}
	store.fail = false
	task := s.agentTask(context.Background())
	if task == nil {
		t.Fatal("guarded candidate ignored")
	}
	b.reconnectError = errors.New("existing provider could not be confirmed")
	s.runAgent(context.Background(), *task)
	if b.reconnectCalls != 1 || s.doc.AgentOutcome != "failed" {
		t.Fatal("failed attachment outcome hidden")
	}
	if s.agentTask(context.Background()) != nil {
		t.Fatal("uncertain generation was automatically retried")
	}
}

func TestAwakeIdlePolicyDoesNotPollNativeHelper(t *testing.T) {
	s, _, h, _, now := fixture(t)
	if s.needsObservations() {
		t.Fatal("unused feature scheduled native observations")
	}
	state := s.ObserveStatus(context.Background())
	if h.observes != 1 || state.Observation.ObservedAt.IsZero() {
		t.Fatal("General settings did not receive observations")
	}
	s.ObserveStatus(context.Background())
	if h.observes != 1 {
		t.Fatal("simultaneous views repeated native probe")
	}
	*now = now.Add(5 * time.Second)
	s.ObserveStatus(context.Background())
	if h.observes != 2 {
		t.Fatal("detailed view did not refresh")
	}
	start(t, s, domain.StartAwakeRequest{Mode: "indefinite"})
	if !s.needsObservations() {
		t.Fatal("active policy lost native monitoring")
	}
}

type authorizingHost struct {
	fakeHost
	entered chan struct{}
	release chan struct{}
}

func (h *authorizingHost) AuthorizeLowPower(ctx context.Context) error {
	close(h.entered)
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestLowPowerAuthorizationDoesNotBlockPolicyStatus(t *testing.T) {
	host := &authorizingHost{entered: make(chan struct{}), release: make(chan struct{})}
	svc, err := New(context.Background(), &memoryStore{}, host, &fakeBindings{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.AuthorizeLowPower(context.Background()) }()
	<-host.entered
	status := make(chan struct{})
	go func() { _ = svc.Status(); close(status) }()
	select {
	case <-status:
	case <-time.After(time.Second):
		t.Fatal("macOS authorization holds the policy lock")
	}
	close(host.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if host.observes != 1 {
		t.Fatal("authorization did not refresh capability discovery")
	}
}
func TestLowPowerAuthorizationCancellationDoesNotSaveSettings(t *testing.T) {
	host := &authorizingHost{entered: make(chan struct{}), release: make(chan struct{})}
	store := &memoryStore{}
	svc, err := New(context.Background(), store, host, &fakeBindings{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.AuthorizeLowPower(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("authorization error: %v", err)
	}
	if store.writes != 0 || svc.Status().Settings.LowPowerBatteryPercent != 0 {
		t.Fatal("canceled authorization changed settings")
	}
}
