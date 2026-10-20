package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	awakesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/awake"
)

// restoreMobileOnBoot re-arms the Connect Mobile LAN listener across daemon
// restarts. If the persisted state says the bridge was enabled, it delegates to
// BridgeService.RestoreOnBoot, which reuses the existing password (no rotation
// — the paired phone keeps working), derives the auth hash in memory, restarts
// the listener on its last bound port, and — critically — re-applies the
// secure-pairing `tailscale serve` proxy against whatever port Start actually
// returned, not the persisted port. That distinction matters: Start falls back
// to an ephemeral port when the persisted one is taken, and a stale `serve`
// config would otherwise keep proxying the tailnet at a dead (or, worse,
// someone else's) port. Legacy mobile wake preferences are migrated to desktop
// ownership before the listener can restore them. Migration or listener errors
// are logged by the caller; Connect Mobile is best-effort, not load-bearing.
func restoreMobileOnBoot(ctx context.Context, path string, bs *controllers.BridgeService, awake controllers.AwakeService) error {
	state, err := mobilebridge.Load(path)
	if err != nil {
		return fmt.Errorf("load mobile bridge state: %w", err)
	}
	var migrationErr error
	if state.KeepAwake || state.KeepAwakeLid {
		if state.KeepAwake {
			if awake == nil {
				migrationErr = errors.New("migrate mobile wake: desktop wake is unavailable")
			} else if !awake.Status().Paused {
				// Preserve the old opt-in, including idle-only wake. Migration must
				// never turn a saved idle request into closed-lid wake or rearm a pause.
				request := domain.StartAwakeRequest{Scope: "ao", Mode: "indefinite", LidClosed: state.KeepAwakeLid}
				err = awake.Start(ctx, request)
				if request.LidClosed && errors.Is(err, awakesvc.ErrLidHelperUpgrade) {
					request.LidClosed = false
					err = awake.Start(ctx, request)
				}
				if err != nil {
					migrationErr = fmt.Errorf("migrate mobile wake: %w", err)
				}
			}
		}
		// Persist retirement before restoring LAN, even when desktop wake was
		// refused (for example, by battery/heat protection). Otherwise the old
		// lease would restart independently and bypass the desktop safety policy.
		state.KeepAwake, state.KeepAwakeLid = false, false
		if err := mobilebridge.Save(path, state); err != nil {
			return errors.Join(migrationErr, fmt.Errorf("retire legacy mobile wake: %w", err))
		}
	}
	if !state.Enabled {
		return migrationErr
	}
	if err := bs.RestoreOnBoot(state); err != nil {
		return errors.Join(migrationErr, fmt.Errorf("restart mobile LAN listener: %w", err))
	}
	return migrationErr
}
