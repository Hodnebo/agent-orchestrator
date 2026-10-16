package sessionmanager

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type taskNamingLauncher struct {
	recordingLauncher
	result string
	err    error
	wait   bool
}

func (l *taskNamingLauncher) RunBackgroundTask(ctx context.Context, harness domain.AgentHarness, cfg ports.ChatStartConfig, prompt string) (string, error) {
	l.background = append(l.background, cfg)
	l.backgroundHarnesses = append(l.backgroundHarnesses, harness)
	l.backgroundPrompts = append(l.backgroundPrompts, prompt)
	if l.wait {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return l.result, l.err
}

func TestTaskNamingBeforeAgentLaunch(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		for _, mode := range []domain.SessionMode{domain.SessionModeChat, domain.SessionModeTUI} {
			t.Run(string(mode)+"/prepared="+map[bool]string{false: "false", true: "true"}[prepared], func(t *testing.T) {
				m, st, _, _ := newGitTaskPreparationManager(t)
				m.dataDir = t.TempDir()
				launcher := &taskNamingLauncher{result: `{"title":"Fix login redirect","slug":"fix-login-redirect"}`}
				m.chat = launcher
				var token domain.TaskPreparationToken
				var err error
				if prepared {
					token, err = m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
					if err != nil {
						t.Fatal(err)
					}
				}
				cfg := asyncChatSpawnConfig("Fix login redirect after expiry")
				cfg.RequestedMode, cfg.NameFromPrompt, cfg.TaskPreparation = mode, true, token
				cfg.DisplayName = "Fix login redirect after expiry"
				cfg.AgentConfig.Model, cfg.AgentConfig.Effort = "gpt-5.4", "low"
				var work []func()
				m.runBackground = func(fn func()) { work = append(work, fn) }
				if mode == domain.SessionModeTUI {
					m.runBackground = func(fn func()) { fn() }
				}
				rec, _, _, err := m.Spawn(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				if mode == domain.SessionModeChat {
					if len(launcher.background) != 0 || len(launcher.started) != 0 || len(work) != 1 {
						t.Fatal("API waited for naming or agent startup")
					}
					m.runBackground = func(fn func()) { fn() }
					work[0]()
				}
				actual := st.sessions[rec.ID]
				if actual.Metadata.Branch != "ao/fix-login-redirect/root" || filepath.Base(actual.Metadata.WorkspacePath) != "fix-login-redirect" || actual.DisplayName != "Fix login redirect" {
					t.Fatalf("named session = %+v", actual)
				}
				if len(launcher.background) != 1 || launcher.background[0].Model != "gpt-5.4" || launcher.background[0].Effort != "low" {
					t.Fatalf("naming calls = %+v", launcher.background)
				}
				if mode == domain.SessionModeChat && launcher.started[0].WorkspacePath != actual.Metadata.WorkspacePath {
					t.Fatal("agent launched in temporary workspace")
				}
			})
		}
	}
}

func TestTaskNamingFallbackAndManualTitle(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result string
		err    error
	}{
		{name: "provider error", err: errors.New("offline")},
		{name: "malformed response", result: "Here is a title"},
		{name: "unusable response", result: `{"title":"...","slug":"你好"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, st, _, _ := newGitTaskPreparationManager(t)
			m.dataDir = t.TempDir()
			m.chat = &taskNamingLauncher{result: tt.result, err: tt.err}
			cfg := asyncChatSpawnConfig("Fix login redirect")
			cfg.NameFromPrompt, cfg.Async, cfg.DisplayName = true, false, "My manual title"
			rec, _, _, err := m.Spawn(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if actual := st.sessions[rec.ID]; actual.Metadata.Branch != "ao/fix-login-redirect/root" || actual.DisplayName != "My manual title" {
				t.Fatalf("fallback = %+v", actual)
			}
		})
	}
}

func TestTaskNamingDeadlineAndCapacity(t *testing.T) {
	m, st, _ := newChatManager(&taskNamingLauncher{wait: true})
	m.dataDir = t.TempDir()
	cfg := asyncChatSpawnConfig("Fix login redirect")
	cfg.NameFromPrompt = true
	rec := domain.SessionRecord{ID: "mer-1", Harness: cfg.Harness}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	wait, stop := m.startTaskNaming(ctx, cfg, rec, st.projects["mer"].Config)
	defer stop()
	if got := wait(); got.Slug != "fix-login-redirect" {
		t.Fatalf("deadline fallback = %+v", got)
	}
	if err := m.WaitBackgroundWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.taskNamingSlots = make(chan struct{}, 1)
	m.taskNamingSlots <- struct{}{}
	wait, stop = m.startTaskNaming(context.Background(), cfg, rec, st.projects["mer"].Config)
	defer stop()
	if got := wait(); got.Slug != "fix-login-redirect" {
		t.Fatalf("capacity fallback = %+v", got)
	}
}

func TestParseTaskName(t *testing.T) {
	fallback := generatedTaskName{Slug: "fallback"}
	for _, tt := range []struct{ raw, slug string }{
		{`{"title":"Fix redirect","slug":"Fix / Login..Redirect"}`, "fix-login-redirect"},
		{"```json\n{\"title\":\"Fix redirect\",\"slug\":\"fix-login\"}\n```", "fix-login"},
		{`{"slug":"../../"}`, "fallback"},
		{`{"slug":"one two three four five six seven"}`, "one-two-three"},
		{`{"slug":"` + strings.Repeat("a", 100) + `"}`, strings.Repeat("a", 24)},
		{`{"slug":"vaporwave-site-redesign"}`, "vaporwave-site-redesign"},
		{`broken`, "fallback"},
	} {
		if got := parseTaskName(tt.raw, fallback); got.Slug != tt.slug {
			t.Errorf("parse %q = %+v, want %q", tt.raw, got, tt.slug)
		}
	}
}

func TestTaskNamingPreservesManualRename(t *testing.T) {
	m, st, ws, _ := newGitTaskPreparationManager(t)
	rec, err := st.CreateSession(context.Background(), domain.SessionRecord{ProjectID: "mer", Kind: domain.KindWorker, DisplayName: "Provisional"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := ws.Create(context.Background(), ports.WorkspaceConfig{ProjectID: "mer", SessionID: rec.ID, Branch: "ao/mer-1/root", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	manual := rec
	manual.DisplayName = "My title"
	st.sessions[rec.ID] = manual
	_, _, err = m.nameTaskWorkspace(context.Background(), rec, root, nil, generatedTaskName{Title: "Generated title", Slug: "fix-login"})
	if err != nil || st.sessions[rec.ID].DisplayName != "My title" {
		t.Fatalf("manual title overwritten: %+v, %v", st.sessions[rec.ID], err)
	}
}

func TestTaskNamingPersistsMultiRepoPaths(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	m.dataDir = t.TempDir()
	child := newManagerGitRepo(t)
	runManagerGit(t, repo, "clone", child, "child")
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	st.projects["mer"] = project
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{{Name: "child", RelativePath: "child", DefaultBranch: "main"}}
	m.chat = &taskNamingLauncher{result: `{"title":"Fix login redirect","slug":"fix-login-redirect"}`}
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	cfg := asyncChatSpawnConfig("Fix login redirect")
	cfg.Async, cfg.NameFromPrompt, cfg.TaskPreparation = false, true, token
	rec, _, _, err := m.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	actual := st.sessions[rec.ID]
	if rows := st.worktrees[rec.ID]; len(rows) != 2 {
		t.Fatalf("worktree rows = %+v", rows)
	} else {
		for _, row := range rows {
			if row.Branch != "ao/fix-login-redirect/root" || !strings.HasPrefix(row.WorktreePath, actual.Metadata.WorkspacePath) {
				t.Fatalf("stale durable worktree row: %+v", row)
			}
			if got := strings.TrimSpace(runManagerGit(t, row.WorktreePath, "branch", "--show-current")); got != row.Branch {
				t.Fatalf("actual branch = %q, durable = %q", got, row.Branch)
			}
		}
	}
}

func BenchmarkTaskNameParsing(b *testing.B) {
	fallback := generatedTaskName{Slug: "fix-login-redirect"}
	for b.Loop() {
		parseTaskName(`{"title":"Preserve destination after login","slug":"fix-login-redirect"}`, fallback)
	}
}
