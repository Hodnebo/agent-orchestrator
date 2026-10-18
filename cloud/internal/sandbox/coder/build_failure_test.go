package coder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

const testBuildID = "f79de6f6-2a8d-4850-9fe1-3409315eee5b"

// The build log of a real failed build (staging, 2026-10-18): the job error
// says only "terraform apply: exit status 1"; the log names the AWS quota.
var vcpuLimitBuildLog = []buildLogLine{
	{Stage: "Starting workspace", Level: "info", Output: "aws_instance.workspace: Creating..."},
	{Stage: "Starting workspace", Level: "info", Output: "aws_instance.workspace: Creation errored after 1s"},
	{Stage: "Starting workspace", Level: "error", Output: "Error: creating EC2 Instance: operation error EC2: RunInstances, https response error StatusCode: 400, RequestID: a74246f1-8db5-4c51-baed-4fed522bcbe8, api error VcpuLimitExceeded: You have requested more vCPU capacity than your current vCPU limit of 32 allows for the instance bucket that the specified instance type belongs to. Please visit http://aws.amazon.com/contact-us/ec2-request to request an adjustment to this limit."},
	{Stage: "Starting workspace", Level: "error", Output: `on main.tf line 372, in resource "aws_instance" "workspace":`},
	{Stage: "Starting workspace", Level: "error", Output: `  372: resource "aws_instance" "workspace" {`},
	{Stage: "Cleaning Up", Level: "info", Output: ""},
}

// failedBuildServer serves one workspace whose latest build ended with status
// and transition, and the build log (or a 500 when logs is nil).
func failedBuildServer(t *testing.T, status, transition string, completedAt time.Time, logs []buildLogLine) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var logReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/workspaces/"+testWorkspaceID:
			view := healthyWorkspace()
			view.LatestBuild = workspaceBuild{
				ID: testBuildID, Status: status, Transition: transition,
				Job: workspaceBuildJob{
					Status: status, Error: "terraform apply: exit status 1", CompletedAt: &completedAt,
				},
				CreatedAt: completedAt.Add(-time.Minute), UpdatedAt: completedAt,
			}
			writer.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(writer).Encode(view); err != nil {
				t.Errorf("write workspace: %v", err)
			}
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/workspacebuilds/"+testBuildID+"/logs":
			logReads.Add(1)
			if logs == nil {
				http.Error(writer, "logs unavailable", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(writer).Encode(logs); err != nil {
				t.Errorf("write build log: %v", err)
			}
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &logReads
}

func TestGetReportsFailedStartBuildWithBuildLogReason(t *testing.T) {
	t.Parallel()
	completedAt := time.Date(2026, 10, 18, 15, 26, 23, 0, time.UTC)
	server, _ := failedBuildServer(t, "failed", "start", completedAt, vcpuLimitBuildLog)
	environment, err := newTestClient(t, server.URL, nil).Get(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if environment.State != sandbox.StateFailed || environment.Failure == nil {
		t.Fatalf("environment = %+v, want failed with a failure", environment)
	}
	failure := environment.Failure
	if failure.Err.Code != sandbox.StartupErrorWorkspaceBuildFailed || !failure.At.Equal(completedAt) {
		t.Fatalf("failure = %+v at %s", failure.Err, failure.At)
	}
	const want = "The workspace failed to build: VcpuLimitExceeded: You have requested more vCPU capacity than your current vCPU limit of 32 allows"
	if !strings.HasPrefix(failure.Err.Message, want) {
		t.Fatalf("message = %q, want prefix %q", failure.Err.Message, want)
	}
	if strings.Contains(failure.Err.Message, "RequestID") || strings.Contains(failure.Err.Message, "main.tf") ||
		len(failure.Err.Message) > len("The workspace failed to build: ")+buildFailureMessageLimit {
		t.Fatalf("message is not a short, single cause: %q", failure.Err.Message)
	}
}

func TestGetFallsBackToJobErrorWhenBuildLogIsUnavailable(t *testing.T) {
	t.Parallel()
	server, logReads := failedBuildServer(t, "failed", "start", time.Now(), nil)
	environment, err := newTestClient(t, server.URL, nil).Get(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if environment.State != sandbox.StateFailed || environment.Failure == nil ||
		environment.Failure.Err.Message != "The workspace failed to build: terraform apply: exit status 1" {
		t.Fatalf("environment = %+v failure = %+v", environment, environment.Failure)
	}
	if logReads.Load() != 1 {
		t.Fatalf("build log reads = %d, want 1", logReads.Load())
	}
}

func TestGetReportsCanceledStartBuild(t *testing.T) {
	t.Parallel()
	completedAt := time.Date(2026, 10, 18, 15, 30, 0, 0, time.UTC)
	server, logReads := failedBuildServer(t, "canceled", "start", completedAt, vcpuLimitBuildLog)
	environment, err := newTestClient(t, server.URL, nil).Get(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if environment.State != sandbox.StateFailed || environment.Failure == nil ||
		environment.Failure.Err.Code != sandbox.StartupErrorWorkspaceBuildFailed ||
		!strings.Contains(environment.Failure.Err.Message, "canceled") || !environment.Failure.At.Equal(completedAt) {
		t.Fatalf("environment = %+v failure = %+v", environment, environment.Failure)
	}
	if logReads.Load() != 0 {
		t.Fatalf("a canceled build read its log %d times; the cancel is the reason", logReads.Load())
	}
}

// A failed stop or delete is not a startup failure: the reconciler simply
// asks for the transition it wants again.
func TestFailedStopBuildCarriesNoStartupFailure(t *testing.T) {
	t.Parallel()
	for _, transition := range []string{"stop", "delete"} {
		server, logReads := failedBuildServer(t, "failed", transition, time.Now(), vcpuLimitBuildLog)
		environment, err := newTestClient(t, server.URL, nil).Get(context.Background(), testWorkspaceID)
		if err != nil {
			t.Fatalf("%s: get: %v", transition, err)
		}
		if environment.State != sandbox.StateFailed || environment.Failure != nil || logReads.Load() != 0 {
			t.Fatalf("%s: environment = %+v failure = %+v log reads = %d",
				transition, environment, environment.Failure, logReads.Load())
		}
	}
}

func TestBuildFailedMessageRedactsAndBounds(t *testing.T) {
	t.Parallel()
	message := buildFailedMessage("", []string{
		"",
		"Error: provider rejected the request: password=hunter2-very-secret " + strings.Repeat("word ", 200),
	})
	if strings.Contains(message, "hunter2") {
		t.Fatalf("message leaked a secret: %q", message)
	}
	if !strings.HasSuffix(message, "...") || len(message) > len("The workspace failed to build: ")+buildFailureMessageLimit {
		t.Fatalf("message was not bounded: %d bytes", len(message))
	}
	if got := buildFailedMessage("", nil); got != "The workspace failed to build in Coder." {
		t.Fatalf("empty message = %q", got)
	}
}
