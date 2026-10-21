package sessionmanager

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The computer-use section follows the setting at spawn time: on adds the
// ao cua pointer, off (or never wired) leaves the prompt as it was.
func TestBuildSystemPromptComputerUseFollowsSetting(t *testing.T) {
	ctx := context.Background()
	newManager := func() *Manager {
		st := newFakeStore()
		return New(Deps{
			Runtime:   &fakeRuntime{},
			Agents:    singleAgent{agent: &recordingAgent{}},
			Workspace: &fakeWorkspace{},
			Store:     st,
			Messenger: &fakeMessenger{},
			Lifecycle: &fakeLCM{store: st},
			DataDir:   t.TempDir(),
			LookPath:  func(string) (string, error) { return "/bin/true", nil },
		})
	}

	unwired := newManager()
	sp, err := unwired.buildSystemPrompt(ctx, domain.KindWorker, "mer", "mer-1", "", false)
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}
	if strings.Contains(sp, "## Computer use") {
		t.Fatal("computer use section present without the setting")
	}

	enabled := false
	m := newManager()
	m.SetComputerUseEnabled(func(context.Context) bool { return enabled })
	for _, kind := range []domain.SessionKind{domain.KindWorker, domain.KindOrchestrator} {
		sp, err = m.buildSystemPrompt(ctx, kind, "mer", "mer-1", "", false)
		if err != nil {
			t.Fatalf("buildSystemPrompt(%s): %v", kind, err)
		}
		if strings.Contains(sp, "## Computer use") {
			t.Fatalf("%s: computer use section present while off", kind)
		}
	}

	enabled = true
	for _, kind := range []domain.SessionKind{domain.KindWorker, domain.KindOrchestrator} {
		sp, err = m.buildSystemPrompt(ctx, kind, "mer", "mer-1", "", false)
		if err != nil {
			t.Fatalf("buildSystemPrompt(%s): %v", kind, err)
		}
		for _, want := range []string{"## Computer use", "`ao cua guide`", "`ao cua call`", "never `cua-driver call`", "Settings > Computer use agents", "commands/cua.md", "untrusted"} {
			if !strings.Contains(sp, want) {
				t.Fatalf("%s: prompt missing %q", kind, want)
			}
		}
	}
}
