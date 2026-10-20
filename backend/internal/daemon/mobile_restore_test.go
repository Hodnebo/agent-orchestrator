package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	awakesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/awake"
)

// fakeLAN is a minimal httpd.LANController fake for exercising
// restoreMobileOnBoot without a real listener.
type fakeLAN struct {
	started bool
	hash    string
	port    int
	// returnPort, when non-zero, is what Start returns instead of the port it
	// was asked for — simulating LANManager's ephemeral-port fallback when the
	// requested port is already taken (e.g. by another AO instance). Left zero,
	// Start behaves like a normal listener and echoes the requested port back.
	returnPort int
}

func (f *fakeLAN) Start(port int) (int, error) {
	f.started = true
	if f.returnPort != 0 {
		f.port = f.returnPort
		return f.returnPort, nil
	}
	f.port = port
	return port, nil
}
func (f *fakeLAN) StartLoopback(port int) (int, error) {
	return f.Start(port)
}
func (f *fakeLAN) Stop(ctx context.Context) error { return nil }
func (f *fakeLAN) Running() bool                  { return f.started }
func (f *fakeLAN) BoundPort() int                 { return f.port }
func (f *fakeLAN) SetPasswordHash(hash string)    { f.hash = hash }
func (f *fakeLAN) PasswordHash() string           { return f.hash }

func TestRestoreEnabledStartsListener(t *testing.T) {
	dir := t.TempDir()
	path := mobilebridge.Path(dir)
	if err := mobilebridge.Save(path, mobilebridge.State{Enabled: true, Password: "secret12", LastPort: 3011}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	lan := &fakeLAN{}
	bs := &controllers.BridgeService{LAN: lan}
	if err := restoreMobileOnBoot(context.Background(), path, bs, nil); err != nil {
		t.Fatalf("restoreMobileOnBoot: %v", err)
	}
	if !lan.started {
		t.Fatal("expected LAN listener started from persisted enabled state")
	}
	// Restore derives the auth hash from the persisted plaintext password (no
	// rotation), so the fake must have received HashPassword(persisted password).
	if want := mobilebridge.HashPassword("secret12"); lan.hash != want {
		t.Fatalf("expected hash derived from persisted password %q, got %q", want, lan.hash)
	}
	if lan.port != 3011 {
		t.Fatalf("expected persisted port reused, got %d", lan.port)
	}
}

func TestRestoreDisabledDoesNotStart(t *testing.T) {
	dir := t.TempDir()
	path := mobilebridge.Path(dir)
	if err := mobilebridge.Save(path, mobilebridge.State{Enabled: false}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	lan := &fakeLAN{}
	bs := &controllers.BridgeService{LAN: lan}
	if err := restoreMobileOnBoot(context.Background(), path, bs, nil); err != nil {
		t.Fatalf("restoreMobileOnBoot: %v", err)
	}
	if lan.started {
		t.Fatal("expected LAN listener NOT started when persisted state is disabled")
	}
}

// TestRestoreAppliesServeToStartReturnedPort is the regression test for the
// bridge's named primary failure mode: LANManager.Start falls back to an
// ephemeral port when its persisted default is taken (observed live: asked
// for 3011, got 54014 because another AO instance held 3011). A `tailscale
// serve` config pinned to the stale persisted port is exactly what secure
// pairing exists to prevent — and serve's config persists inside tailscaled
// across AO restarts, so a stale entry silently proxies the tailnet at this
// machine's hostname to whatever now holds that dead port.
//
// fakeLAN.returnPort is deliberately set to diverge from the requested port
// here — do not "simplify" it back to echoing the request, or this test stops
// being able to catch a regression to the old lan.Start(state.LastPort)-then-
// applyServe(state.LastPort) bug.
func TestRestoreAppliesServeToStartReturnedPort(t *testing.T) {
	dir := t.TempDir()
	path := mobilebridge.Path(dir)
	if err := mobilebridge.Save(path, mobilebridge.State{
		Enabled:       true,
		Password:      "secret12",
		LastPort:      3011,
		SecurePairing: true,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	lan := &fakeLAN{returnPort: 54014}
	var appliedPort int
	bs := &controllers.BridgeService{
		LAN: lan,
		ApplyServe: func(port int) error {
			appliedPort = port
			return nil
		},
	}
	if err := restoreMobileOnBoot(context.Background(), path, bs, nil); err != nil {
		t.Fatalf("restoreMobileOnBoot: %v", err)
	}
	if appliedPort != 54014 {
		t.Fatalf("expected ApplyServe called with Start's returned port 54014, got %d (state.LastPort was 3011)", appliedPort)
	}
}

// Unused AwakeService methods remain unimplemented: migration must not resume
// safety pauses or change settings/rules while transferring the legacy opt-in.
type migrationAwake struct {
	controllers.AwakeService
	state    domain.AwakeState
	requests []domain.StartAwakeRequest
	start    func(domain.StartAwakeRequest) error
}

func (a *migrationAwake) Status() domain.AwakeState { return a.state }
func (a *migrationAwake) Start(_ context.Context, request domain.StartAwakeRequest) error {
	a.requests = append(a.requests, request)
	if a.start != nil {
		return a.start(request)
	}
	a.state.Active = true
	return nil
}

type legacyWakeProbe struct{ starts int }

func (k *legacyWakeProbe) Start() error                  { k.starts++; return nil }
func (*legacyWakeProbe) Stop()                           {}
func (*legacyWakeProbe) Active() bool                    { return false }
func (*legacyWakeProbe) HasBattery() bool                { return true }
func (*legacyWakeProbe) ConfigureLid(bool, bool) error   { return nil }
func (*legacyWakeProbe) LidStatus() (bool, bool, string) { return true, false, "" }

func TestRestoreMigratesMobileWakeOnceBeforeRestoringBridge(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, lid := range []bool{false, true} {
			t.Run(fmt.Sprintf("bridge=%t/lid=%t", enabled, lid), func(t *testing.T) {
				path := mobilebridge.Path(t.TempDir())
				before := mobilebridge.State{Enabled: enabled, Password: "secret12", LastPort: 3011, KeepAwake: true, KeepAwakeLid: lid}
				if err := mobilebridge.Save(path, before); err != nil {
					t.Fatal(err)
				}
				lan := &fakeLAN{}
				legacy := &legacyWakeProbe{}
				bs := &controllers.BridgeService{LAN: lan, ConfigPath: path, KeepAwake: legacy}
				awake := &migrationAwake{state: domain.AwakeState{Settings: domain.AwakeSettings{StopBatteryPercent: 25}}}
				awake.start = func(request domain.StartAwakeRequest) error {
					saved, err := mobilebridge.Load(path)
					if err != nil {
						return err
					}
					if !saved.KeepAwake || lan.started {
						t.Fatal("legacy preference retired or listener restored before desktop start")
					}
					if request.Scope != "ao" || request.Mode != "indefinite" || request.LidClosed != lid {
						t.Fatalf("changed the saved intent: %+v", request)
					}
					awake.state.Active = true
					return nil
				}
				if err := restoreMobileOnBoot(context.Background(), path, bs, awake); err != nil {
					t.Fatal(err)
				}
				saved, err := mobilebridge.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if saved.KeepAwake || saved.KeepAwakeLid {
					t.Fatal("legacy ownership remains persisted")
				}
				if saved.Enabled != enabled || saved.Password != before.Password || saved.LastPort != before.LastPort {
					t.Fatalf("changed pairing settings: %+v", saved)
				}
				if lan.started != enabled || legacy.starts != 0 || !awake.state.Active {
					t.Fatal("legacy lease restarted or desktop intent was not transferred")
				}
				if awake.state.Settings.StopBatteryPercent != 25 {
					t.Fatal("changed configured safety cutoff")
				}
				if err := restoreMobileOnBoot(context.Background(), path, bs, awake); err != nil {
					t.Fatal(err)
				}
				if len(awake.requests) != 1 || legacy.starts != 0 {
					t.Fatal("migration repeated or legacy hold restarted")
				}
			})
		}
	}
}

func TestRestoreMigrationDoesNotRearmSafetyPause(t *testing.T) {
	path := mobilebridge.Path(t.TempDir())
	if err := mobilebridge.Save(path, mobilebridge.State{Enabled: true, Password: "secret12", KeepAwake: true, KeepAwakeLid: true}); err != nil {
		t.Fatal(err)
	}
	lan := &fakeLAN{}
	legacy := &legacyWakeProbe{}
	awake := &migrationAwake{state: domain.AwakeState{Paused: true, Reason: "critical_heat"}}
	if err := restoreMobileOnBoot(context.Background(), path, &controllers.BridgeService{LAN: lan, ConfigPath: path, KeepAwake: legacy}, awake); err != nil {
		t.Fatal(err)
	}
	saved, err := mobilebridge.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(awake.requests) != 0 || !awake.state.Paused || saved.KeepAwake || saved.KeepAwakeLid || legacy.starts != 0 || !lan.started {
		t.Fatal("migration rearmed wake or failed to retire the legacy hold")
	}
}

func TestRestoreMigrationFailureCannotRestartLegacyWake(t *testing.T) {
	for _, reason := range []string{"critical_heat", "low_battery", "setup failed", "unavailable"} {
		t.Run(reason, func(t *testing.T) {
			path := mobilebridge.Path(t.TempDir())
			if err := mobilebridge.Save(path, mobilebridge.State{Enabled: true, Password: "secret12", KeepAwake: true}); err != nil {
				t.Fatal(err)
			}
			lan := &fakeLAN{}
			legacy := &legacyWakeProbe{}
			var awake controllers.AwakeService
			if reason != "unavailable" {
				awake = &migrationAwake{start: func(domain.StartAwakeRequest) error { return errors.New(reason) }}
			}
			err := restoreMobileOnBoot(context.Background(), path, &controllers.BridgeService{LAN: lan, ConfigPath: path, KeepAwake: legacy}, awake)
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("migration failure not reported: %v", err)
			}
			saved, loadErr := mobilebridge.Load(path)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if saved.KeepAwake || saved.KeepAwakeLid || legacy.starts != 0 || !lan.started {
				t.Fatal("failed migration restored independent wake or disabled mobile access")
			}
		})
	}
}

func TestRestoreMigrationFallsBackForOldLidHelper(t *testing.T) {
	path := mobilebridge.Path(t.TempDir())
	if err := mobilebridge.Save(path, mobilebridge.State{KeepAwake: true, KeepAwakeLid: true}); err != nil {
		t.Fatal(err)
	}
	awake := &migrationAwake{start: func(request domain.StartAwakeRequest) error {
		if request.LidClosed {
			return fmt.Errorf("registered helper: %w", awakesvc.ErrLidHelperUpgrade)
		}
		return nil
	}}
	if err := restoreMobileOnBoot(context.Background(), path, &controllers.BridgeService{ConfigPath: path, LAN: &fakeLAN{}}, awake); err != nil {
		t.Fatal(err)
	}
	if len(awake.requests) != 2 || !awake.requests[0].LidClosed || awake.requests[1].LidClosed {
		t.Fatalf("idle fallback was not used: %+v", awake.requests)
	}
}

func TestRestoreRetiresDisabledLidPreferenceWithoutStartingWake(t *testing.T) {
	path := mobilebridge.Path(t.TempDir())
	if err := mobilebridge.Save(path, mobilebridge.State{KeepAwakeLid: true}); err != nil {
		t.Fatal(err)
	}
	awake := &migrationAwake{}
	if err := restoreMobileOnBoot(context.Background(), path, &controllers.BridgeService{}, awake); err != nil {
		t.Fatal(err)
	}
	saved, err := mobilebridge.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(awake.requests) != 0 || saved.KeepAwakeLid {
		t.Fatal("an off preference started wake or was not retired")
	}
}

func TestRestoreDoesNotStartBridgeIfLegacyRetirementCannotBeSaved(t *testing.T) {
	path := mobilebridge.Path(t.TempDir())
	if err := mobilebridge.Save(path, mobilebridge.State{Enabled: true, Password: "secret12", KeepAwake: true}); err != nil {
		t.Fatal(err)
	}
	awake := &migrationAwake{start: func(domain.StartAwakeRequest) error {
		// Make atomic replacement fail after the initial successful load.
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Mkdir(path, 0700)
	}}
	lan := &fakeLAN{}
	err := restoreMobileOnBoot(context.Background(), path, &controllers.BridgeService{LAN: lan}, awake)
	if err == nil || !strings.Contains(err.Error(), "retire legacy mobile wake") || lan.started {
		t.Fatalf("bridge restored after failed retirement: %v", err)
	}
}
