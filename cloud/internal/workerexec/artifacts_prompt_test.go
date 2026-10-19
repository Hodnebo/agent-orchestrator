package workerexec

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestSystemPromptArtifactSections(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.ToSlash(worker.ArtifactsDir(dataDir, "session-1"))
	cases := []struct {
		name        string
		launch      worker.LaunchContext
		wantSection bool
		wantDeliver bool
	}{
		{"artifacts off", worker.LaunchContext{SessionID: "session-1", Kind: "worker", RequestedOutput: domain.RequestedOutputArtifact}, false, false},
		{"worker", worker.LaunchContext{SessionID: "session-1", Kind: "worker", Artifacts: true}, true, false},
		{"artifact worker", worker.LaunchContext{SessionID: "session-1", Kind: "worker", Artifacts: true, RequestedOutput: domain.RequestedOutputArtifact}, true, true},
		{"orchestrator", worker.LaunchContext{SessionID: "session-1", Kind: "orchestrator", Artifacts: true, RequestedOutput: domain.RequestedOutputArtifact}, true, false},
		{"reviewer", worker.LaunchContext{SessionID: "session-1", Kind: "reviewer", Artifacts: true}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompt := HarnessBuilder{DataDir: dataDir}.systemPrompt(tc.launch, "/work")
			if got := strings.Contains(prompt, "## Session Artifacts") && strings.Contains(prompt, dir); got != tc.wantSection {
				t.Fatalf("artifacts section present = %v, want %v:\n%s", got, tc.wantSection, prompt)
			}
			if got := strings.Contains(prompt, "## Artifact Output"); got != tc.wantDeliver {
				t.Fatalf("artifact output section present = %v, want %v", got, tc.wantDeliver)
			}
		})
	}
}

func TestAgentDirectoriesIncludeTheArtifactDirectory(t *testing.T) {
	dataDir := t.TempDir()
	builder := HarnessBuilder{DataDir: dataDir}
	want := worker.ArtifactsDir(dataDir, "session-1")
	launch := worker.LaunchContext{SessionID: "session-1", Kind: "worker", Artifacts: true}
	if dirs := builder.agentDirectories(launch, "/work"); len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("agentDirectories = %v, want [%s]", dirs, want)
	}
	if dirs := builder.agentDirectories(worker.LaunchContext{SessionID: "session-1", Kind: "worker"}, "/work"); len(dirs) != 0 {
		t.Fatalf("agentDirectories with artifacts off = %v, want none", dirs)
	}
	reviewer := worker.LaunchContext{SessionID: "session-1", Kind: "reviewer", Artifacts: true}
	if dirs := builder.agentDirectories(reviewer, "/work"); len(dirs) != 0 {
		t.Fatalf("agentDirectories for a reviewer = %v, want none", dirs)
	}
	if args := codexWritableRootsArgs(builder.agentDirectories(launch, "/work")); len(args) != 2 || !strings.Contains(args[1], strconv.Quote(want)) {
		t.Fatalf("codex writable roots = %v, want the artifact directory", args)
	}
}
