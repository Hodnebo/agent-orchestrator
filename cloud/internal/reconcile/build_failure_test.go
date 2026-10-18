package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

// buildFailureProvider is a provider whose workspace build failed, counting
// the transitions the reconciler asks for.
type buildFailureProvider struct {
	lifecycleProvider
	stops     int
	creates   int
	createErr error
}

func (p *buildFailureProvider) Stop(context.Context, sandbox.ID) error {
	p.stops++
	return nil
}

func (p *buildFailureProvider) FindBySession(context.Context, string) (sandbox.Environment, bool, error) {
	return sandbox.Environment{}, false, nil
}

func (p *buildFailureProvider) Create(context.Context, sandbox.Spec) (sandbox.Environment, error) {
	p.creates++
	if p.createErr != nil {
		return sandbox.Environment{}, p.createErr
	}
	return p.environment, nil
}

const vcpuBuildMessage = "The workspace failed to build: VcpuLimitExceeded: You have requested more vCPU capacity than your current vCPU limit of 32 allows."

func failedBuildEnvironment(failedAt time.Time) sandbox.Environment {
	return sandbox.Environment{
		ID: "workspace-1", State: sandbox.StateFailed,
		Failure: &sandbox.Failure{
			Err: &sandbox.StartupError{Code: sandbox.StartupErrorWorkspaceBuildFailed, Message: vcpuBuildMessage},
			At:  failedAt,
		},
	}
}

// startingRecord is a session whose worker has not checked in, in a startup
// window opened at startedAt.
func startingRecord(startedAt time.Time, connectionID string) domain.Sandbox {
	return domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderCoder,
		ProviderEnvironmentID: "workspace-1",
		ProviderConnectionID:  connectionID,
		DesiredState:          domain.SandboxDesiredRunning,
		ObservedState:         domain.SandboxObservedProvisioning,
		StartupStartedAt:      &startedAt,
		UpdatedAt:             startedAt,
	}
}

// A workspace build that failed in the current startup attempt parks the
// session at once with Coder's reason, instead of leaving it "provisioning"
// for the whole startup budget.
func TestFailedBuildParksSessionWithBuildError(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-90 * time.Second)
	for _, test := range []struct {
		name         string
		connectionID string
		wantStops    int
	}{
		// AO's own compute is stopped so nothing half-built keeps billing.
		{name: "managed", wantStops: 1},
		// A customer's own workspace is left exactly as it is.
		{name: "bring your own", connectionID: "connection-1", wantStops: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &lifecycleStore{}
			provider := &buildFailureProvider{lifecycleProvider: lifecycleProvider{
				environment: failedBuildEnvironment(started.Add(80 * time.Second)),
			}}
			if err := testReconciler(store, provider).reconcileSandbox(
				context.Background(), startingRecord(started, test.connectionID),
			); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if len(store.startupErrors) != 1 ||
				store.startupErrors[0] != (startupErrorRecord{code: sandbox.StartupErrorWorkspaceBuildFailed, message: vcpuBuildMessage}) {
				t.Fatalf("startup errors = %+v", store.startupErrors)
			}
			if len(store.observations) != 1 || store.observations[0] != domain.SandboxObservedTerminated {
				t.Fatalf("observations = %v, want terminated", store.observations)
			}
			if provider.starts != 0 || provider.stops != test.wantStops {
				t.Fatalf("starts = %d stops = %d, want 0 and %d", provider.starts, provider.stops, test.wantStops)
			}
		})
	}
}

// A user retry opens a new startup window. The failed build predates it, so
// the reconciler starts a new build (once) and clears nothing it should keep.
func TestRetryAfterFailedBuildStartsNewBuild(t *testing.T) {
	t.Parallel()
	failedAt := time.Now().Add(-10 * time.Minute)
	store := &lifecycleStore{}
	provider := &buildFailureProvider{lifecycleProvider: lifecycleProvider{
		environment: failedBuildEnvironment(failedAt),
	}}
	reconciler := testReconciler(store, provider)
	retried := startingRecord(time.Now(), "connection-1")
	if err := reconciler.reconcileSandbox(context.Background(), retried); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if provider.starts != 1 || provider.stops != 0 || len(store.startupErrors) != 0 {
		t.Fatalf("starts = %d stops = %d startup errors = %+v; want one new build and no error",
			provider.starts, provider.stops, store.startupErrors)
	}
	if len(store.observations) != 1 || store.observations[0] != domain.SandboxObservedProvisioning {
		t.Fatalf("observations = %v, want provisioning", store.observations)
	}

	// Once the new build runs the provider reports it provisioning, so the
	// next pass waits instead of starting another build.
	provider.environment = sandbox.Environment{ID: "workspace-1", State: sandbox.StateProvisioning}
	if err := reconciler.reconcileSandbox(context.Background(), retried); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if provider.starts != 1 {
		t.Fatalf("starts = %d after the new build began, want 1", provider.starts)
	}

	// The new build fails too, after the retry: that failure is reported.
	provider.environment = failedBuildEnvironment(time.Now())
	if err := reconciler.reconcileSandbox(context.Background(), retried); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if provider.starts != 1 || len(store.startupErrors) != 1 ||
		store.startupErrors[0].code != sandbox.StartupErrorWorkspaceBuildFailed {
		t.Fatalf("starts = %d startup errors = %+v; want the new failure parked", provider.starts, store.startupErrors)
	}
}

// A failed build seen for a session whose startup window is closed (it was
// running) is recovered by a new build rather than parked: a retry would be
// refused for a worker that once checked in.
func TestFailedBuildOfRunningSessionStartsNewBuild(t *testing.T) {
	t.Parallel()
	store := &lifecycleStore{}
	provider := &buildFailureProvider{lifecycleProvider: lifecycleProvider{
		environment: failedBuildEnvironment(time.Now()),
	}}
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), runningRecord(false)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if provider.starts != 1 || len(store.startupErrors) != 0 {
		t.Fatalf("starts = %d startup errors = %+v, want one new build", provider.starts, store.startupErrors)
	}
}

// A paused session whose stop build failed asks for the stop again; a failed
// build never restarts compute the user paused.
func TestFailedBuildOfPausedSessionIsStoppedAgain(t *testing.T) {
	t.Parallel()
	store := &lifecycleStore{}
	provider := &buildFailureProvider{lifecycleProvider: lifecycleProvider{
		environment: sandbox.Environment{ID: "workspace-1", State: sandbox.StateFailed},
	}}
	record := runningRecord(false)
	record.DesiredState = domain.SandboxDesiredPaused
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), record); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if provider.stops != 1 || provider.starts != 0 || len(store.startupErrors) != 0 {
		t.Fatalf("stops = %d starts = %d startup errors = %+v", provider.stops, provider.starts, store.startupErrors)
	}
}

// uncreatedRecord is a session whose compute has never been created: Create
// has been failing since startedAt.
func uncreatedRecord(startedAt time.Time) domain.Sandbox {
	return domain.Sandbox{
		SessionID: "session-1", OrgID: "org-1", Provider: sandbox.ProviderFreestyle,
		DesiredState:     domain.SandboxDesiredRunning,
		ObservedState:    domain.SandboxObservedFailed,
		StartupStartedAt: &startedAt,
		UpdatedAt:        time.Now(),
	}
}

// Compute that cannot be created is bounded like compute that never becomes
// ready: AO keeps retrying silently within the startup budget, explains why
// past it, and parks the session past the hard ceiling.
func TestCreateFailureIsBoundedByStartupBudget(t *testing.T) {
	t.Parallel()
	createErr := errors.New(`coder: API returned 400: {"message":"Unable to create workspace","detail":"token=abc123secretvalue rejected"}`)
	for _, test := range []struct {
		name          string
		age           time.Duration
		wantErrors    int
		wantParked    bool
		wantFailures  int
		wantProviders int
	}{
		{name: "within budget", age: time.Minute, wantErrors: 0, wantFailures: 1, wantProviders: 1},
		{name: "past budget", age: DefaultStartupTimeout + time.Minute, wantErrors: 1, wantFailures: 1, wantProviders: 1},
		{name: "past ceiling", age: DefaultTerminalStartupTimeout + time.Minute, wantErrors: 1, wantParked: true, wantProviders: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &lifecycleStore{}
			provider := &buildFailureProvider{createErr: createErr}
			err := testReconciler(store, provider).reconcileSandbox(
				context.Background(), uncreatedRecord(time.Now().Add(-test.age)),
			)
			if test.wantParked != (err == nil) {
				t.Fatalf("reconcile error = %v, parked = %t", err, test.wantParked)
			}
			if provider.creates != test.wantProviders || len(store.failures) != test.wantFailures {
				t.Fatalf("creates = %d failures = %v", provider.creates, store.failures)
			}
			if len(store.startupErrors) != test.wantErrors {
				t.Fatalf("startup errors = %+v, want %d", store.startupErrors, test.wantErrors)
			}
			if test.wantErrors > 0 {
				got := store.startupErrors[0]
				if got.code != sandbox.StartupErrorWorkspaceCreateFailed ||
					!strings.HasPrefix(got.message, "AO couldn't create this session's workspace: coder: API returned 400") ||
					strings.Contains(got.message, "abc123secretvalue") {
					t.Fatalf("startup error = %+v", got)
				}
			}
			parked := len(store.observations) == 1 && store.observations[0] == domain.SandboxObservedTerminated
			if parked != test.wantParked {
				t.Fatalf("observations = %v, want parked = %t", store.observations, test.wantParked)
			}
		})
	}
}

// A provider with no capacity is bounded the same way.
func TestCapacityWaitIsBoundedByStartupBudget(t *testing.T) {
	t.Parallel()
	store := &lifecycleStore{}
	provider := &buildFailureProvider{createErr: sandbox.ErrAtCapacity}
	record := uncreatedRecord(time.Now().Add(-(DefaultStartupTimeout + time.Minute)))
	record.ObservedState = domain.SandboxObservedProvisioning
	if err := testReconciler(store, provider).reconcileSandbox(context.Background(), record); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.startupErrors) != 1 || store.startupErrors[0].code != sandbox.StartupErrorWorkspaceCreateFailed ||
		!strings.Contains(store.startupErrors[0].message, "no capacity") {
		t.Fatalf("startup errors = %+v", store.startupErrors)
	}
	if len(store.observations) != 1 || store.observations[0] != domain.SandboxObservedProvisioning {
		t.Fatalf("observations = %v, want still waiting for capacity", store.observations)
	}
}
