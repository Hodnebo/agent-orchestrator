package freestyle

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/cleanuphook"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/cleanuphook/cleanuphooktest"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// vmSequence makes every test VM's id unique: Cleanup runs are remembered
// per VM id across clients.
var vmSequence atomic.Int64

// cleanupVM serves one session VM in state and returns its id and a client
// that waits up to five seconds for its Cleanup within a Delete.
func cleanupVM(t *testing.T, state string) (*fakeAPI, *Client, string) {
	t.Helper()
	api := newFakeAPI()
	id := fmt.Sprintf("vm-cleanup-%d", vmSequence.Add(1))
	view := vmView{ID: id, Slug: "ao-" + id, State: state}
	api.vms[view.ID], api.vms[view.Slug] = view, view
	client := testClient(t, api)
	client.cleanupInline = 5 * time.Second
	return api, client, id
}

// calls lists the requests the fake received, as "METHOD /path".
func (f *fakeAPI) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]string, 0, len(f.requests))
	for _, request := range f.requests {
		calls = append(calls, request.method+" "+request.path)
	}
	return calls
}

func (f *fakeAPI) execs() int {
	count := 0
	for _, call := range f.calls() {
		if strings.HasSuffix(call, "/exec-await") {
			count++
		}
	}
	return count
}

func (f *fakeAPI) exists(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.vms[id]
	return ok
}

// stepOutput is what the hook prints for a successful two-step Cleanup whose
// commands script carries nonce, followed by the hook command's status.
func stepOutput(nonce string) string {
	marker := "@@ao-step-" + nonce + " "
	return marker + "log /workspace/.ao/worker/cleanup-state/output\n" +
		marker + "1 2 start 1792344586000 / make down\nstopped\n" + marker + "1 2 end 0 120\n" +
		marker + "2 2 start 1792344586120 / docker compose down\nremoved\n" + marker + "2 2 end 0 30\n" +
		"\n" + cleanuphook.Marker + ":0\n"
}

// Delete runs the project's current Cleanup in the VM through one root exec,
// which runs the worker's hook as its owner under the budget, appends each
// step to the session, and only then deletes the VM.
func TestDeleteRunsCleanupInTheVMBeforeDeletingIt(t *testing.T) {
	t.Parallel()
	api, client, id := cleanupVM(t, "running")
	commands := []string{"make down", "docker compose down"}
	var script string
	api.execFunc = func(command string) (string, int) {
		var nonce string
		script, nonce = cleanuphooktest.Script(t, command)
		return stepOutput(nonce), 0
	}
	recorder := &cleanuphooktest.Recorder{}
	ctx := recorder.Context(sandbox.WithCleanupCommands(context.Background(), commands))
	if err := client.Delete(ctx, sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if calls := strings.Join(api.calls(), ", "); calls != fmt.Sprintf("GET /v5/vms/%[1]s, POST /v5/vms/%[1]s/exec-await, DELETE /v5/vms/%[1]s", id) {
		t.Fatalf("calls = %s; want the Cleanup exec before the delete", calls)
	}
	exec := api.last(http.MethodPost, "/exec-await").body
	command := stringValue(exec["command"])
	for _, want := range []string{
		"ao_hook='/workspace/.ao/worker/cleanup-hook.sh'",
		`set -- runuser -u "$ao_owner" -- "$ao_hook"`,
		"timeout -k 5 600 ",
		`case "$ao_owner" in ''|root|`,
		// The resumed worker and agent are stopped before the hook runs.
		`ao_stop "$ao_who" TERM`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("hook command missing %q:\n%s", want, command)
		}
	}
	if exec["linuxUser"] != "root" || exec["timeoutMs"] != float64((cleanuphook.Budget+cleanuphook.Margin)/time.Millisecond) {
		t.Fatalf("exec = %v", exec)
	}
	if !strings.Contains(script, "sh -c 'make down'") || !strings.Contains(script, "sh -c 'docker compose down'") {
		t.Fatalf("the exec does not carry the current Cleanup: %q", script)
	}
	events := recorder.Events(t)
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	for index, event := range events {
		if !worker.ValidScriptStepEvent(event) || event.Phase != worker.ScriptPhaseCleanup || event.Status != worker.SetupSucceeded ||
			event.Command != commands[index] || event.RunID != events[0].RunID {
			t.Fatalf("event %d = %+v", index, event)
		}
	}
	if events[0].Output != "stopped" || events[1].Output != "removed" {
		t.Fatalf("outputs = %q, %q", events[0].Output, events[1].Output)
	}
	if stage, ok := recorder.Stage(); !ok || stage.Err != nil || stage.Attrs["provider_id"] != id {
		t.Fatalf("stage = %+v", stage)
	}
	if api.exists(id) {
		t.Fatal("the VM was not deleted")
	}
}

// No exec runs for a project without Cleanup now, a Delete that skips
// Cleanup, or a Recreate; a VM with no hook installed reports a skipped
// stage and no steps. Every one of them deletes the VM.
func TestDeleteWithoutCleanupNeverRunsTheHook(t *testing.T) {
	t.Parallel()
	for name, ctx := range map[string]context.Context{
		"no Cleanup now":  sandbox.WithCleanupCommands(context.Background(), nil),
		"without Cleanup": sandbox.WithoutCleanup(context.Background()),
	} {
		api, client, id := cleanupVM(t, "running")
		if err := client.Delete(ctx, sandbox.ID(id)); err != nil {
			t.Fatalf("%s: delete: %v", name, err)
		}
		if api.execs() != 0 || api.exists(id) {
			t.Fatalf("%s: calls = %v", name, api.calls())
		}
	}

	api, client, id := cleanupVM(t, "running")
	if _, err := client.Recreate(context.Background(), sandbox.ID(id), sandbox.Spec{SessionID: "s1", RootFS: "snap"}); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if api.execs() != 0 {
		t.Fatalf("a recreate ran Cleanup: %v", api.calls())
	}

	api, client, id = cleanupVM(t, "running")
	api.execOut = "\n" + cleanuphook.Marker + ":" + cleanuphook.Skipped + "\n"
	recorder := &cleanuphooktest.Recorder{}
	if err := client.Delete(recorder.Context(context.Background()), sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if stage := recorder.WaitStage(t); stage.Err != nil || stage.Attrs["result"] != cleanuphook.Skipped {
		t.Fatalf("stage = %+v", stage)
	}
	if events := recorder.Events(t); len(events) != 0 || api.exists(id) {
		t.Fatalf("events = %+v, calls = %v", events, api.calls())
	}
}

// A failed Cleanup, one its budget stopped, and an exec that failed outright
// are reported, and the VM is deleted all the same.
func TestDeleteReportsAFailedCleanupAndStillDeletes(t *testing.T) {
	t.Parallel()
	api, client, id := cleanupVM(t, "running")
	api.execOut = "tearing down\ncleanup step 1 failed: exit status 4\n\n" + cleanuphook.Marker + ":4\n"
	recorder := &cleanuphooktest.Recorder{}
	if err := client.Delete(recorder.Context(context.Background()), sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	stage, _ := recorder.Stage()
	if stage.Err == nil || !strings.Contains(stage.Err.Error(), "exited 4") || !strings.Contains(stage.Attrs["output"], "tearing down") {
		t.Fatalf("stage = %+v", stage)
	}
	if events := recorder.Events(t); len(events) != 1 || events[0].Status != worker.SetupFailed || *events[0].ExitCode != 4 {
		t.Fatalf("events = %+v", events)
	}
	if api.exists(id) {
		t.Fatal("a failed Cleanup kept the VM")
	}

	// The hook command's timeout stopped the run mid-step.
	api, client, id = cleanupVM(t, "running")
	api.execOut = "@@ao-step-00000000000000aa 1 1 start 1792344586000 / make down\nwaiting\n\n" + cleanuphook.Marker + ":124\n"
	recorder = &cleanuphooktest.Recorder{}
	if err := client.Delete(recorder.Context(context.Background()), sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if events := recorder.Events(t); len(events) != 1 || events[0].Status != worker.SetupTimedOut {
		t.Fatalf("timed-out events = %+v", events)
	}
	if stage, _ := recorder.Stage(); stage.Err == nil || api.exists(id) {
		t.Fatalf("timed-out stage = %+v, calls = %v", stage, api.calls())
	}

	api, client, id = cleanupVM(t, "running")
	api.execStatus = http.StatusBadGateway
	recorder = &cleanuphooktest.Recorder{}
	if err := client.Delete(recorder.Context(context.Background()), sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if stage, _ := recorder.Stage(); stage.Err == nil || !strings.Contains(stage.Err.Error(), "did not report") {
		t.Fatalf("failed exec stage = %+v", stage)
	}
	if events := recorder.Events(t); len(events) != 0 || api.exists(id) {
		t.Fatalf("failed exec: events = %+v, calls = %v", events, api.calls())
	}
}

// A Cleanup still running hands the VM back to the reconciler instead of
// holding the Delete; re-issued Deletes wait for the same run, and the VM is
// deleted once it finishes.
func TestDeleteWaitsForARunningCleanupWithoutHoldingTheCaller(t *testing.T) {
	t.Parallel()
	api, client, id := cleanupVM(t, "running")
	client.cleanupInline = 50 * time.Millisecond
	api.execBlock = make(chan struct{})
	api.execOut = "\n" + cleanuphook.Marker + ":0\n"
	recorder := &cleanuphooktest.Recorder{}
	ctx := recorder.Context(context.Background())
	for range 2 {
		if err := client.Delete(ctx, sandbox.ID(id)); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if !api.exists(id) {
		t.Fatal("deleted while Cleanup ran")
	}
	close(api.execBlock)
	recorder.WaitStage(t)
	if err := client.Delete(ctx, sandbox.ID(id)); err != nil {
		t.Fatalf("delete after cleanup: %v", err)
	}
	if api.execs() != 1 || api.exists(id) {
		t.Fatalf("calls = %v", api.calls())
	}
}

// A paused VM is resumed for its Cleanup; one that does not resume is
// deleted with its steps reported as skipped and why.
func TestDeleteResumesAPausedVMForCleanup(t *testing.T) {
	t.Parallel()
	api, client, id := cleanupVM(t, "paused")
	api.startTo = "running"
	api.execOut = "\n" + cleanuphook.Marker + ":0\n"
	recorder := &cleanuphooktest.Recorder{}
	if err := client.Delete(recorder.Context(context.Background()), sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if calls := strings.Join(api.calls(), ", "); calls != fmt.Sprintf("GET /v5/vms/%[1]s, POST /v5/vms/%[1]s/start, "+
		"GET /v5/vms/%[1]s, POST /v5/vms/%[1]s/exec-await, DELETE /v5/vms/%[1]s", id) {
		t.Fatalf("calls = %s; want resume, Cleanup, delete", calls)
	}
	if stage, _ := recorder.Stage(); stage.Err != nil || stage.Attrs["resumed"] != "true" {
		t.Fatalf("stage = %+v", stage)
	}

	stuck, stuckClient, id := cleanupVM(t, "paused")
	stuckClient.cleanupResumeWait = 100 * time.Millisecond
	recorder = &cleanuphooktest.Recorder{}
	ctx := recorder.Context(sandbox.WithCleanupCommands(context.Background(), []string{"make down"}))
	if err := stuckClient.Delete(ctx, sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if stuck.execs() != 0 || stuck.exists(id) {
		t.Fatalf("calls = %v; want no exec and the VM deleted", stuck.calls())
	}
	stage, _ := recorder.Stage()
	if stage.Err == nil || stage.Attrs["result"] != cleanuphook.Skipped {
		t.Fatalf("stage = %+v", stage)
	}
	events := recorder.Events(t)
	if len(events) != 1 || events[0].Status != worker.ScriptStepSkipped || events[0].Command != "make down" ||
		!strings.Contains(events[0].Hint, "did not resume") || !worker.ValidScriptStepEvent(events[0]) {
		t.Fatalf("events = %+v", events)
	}
}

// A paused VM of an organization over its plan is not resumed for its
// Cleanup: it is deleted at once, each step reported as skipped with the
// reason, once. A running VM of such an organization still runs its Cleanup.
func TestDeleteKeepsAVMPausedForBillingPaused(t *testing.T) {
	t.Parallel()
	const reason = "The organization is over its plan."
	api, client, id := cleanupVM(t, "paused")
	api.startTo = "running"
	recorder := &cleanuphooktest.Recorder{}
	ctx := recorder.Context(sandbox.WithoutCleanupResume(
		sandbox.WithCleanupCommands(context.Background(), []string{"make down", "docker compose down"}), reason))
	if err := client.Delete(ctx, sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, call := range api.calls() {
		if strings.HasSuffix(call, "/start") || strings.HasSuffix(call, "/exec-await") {
			t.Fatalf("calls = %v; a VM paused for billing was resumed", api.calls())
		}
	}
	if api.exists(id) {
		t.Fatalf("calls = %v; want the VM deleted", api.calls())
	}
	stage, _ := recorder.Stage()
	if stage.Attrs["result"] != cleanuphook.Skipped || stage.Attrs["resumed"] != "false" {
		t.Fatalf("stage = %+v", stage)
	}
	events := recorder.Events(t)
	if len(events) != 2 || events[0].Status != worker.ScriptStepSkipped || events[0].Hint != reason || events[1].Hint != reason {
		t.Fatalf("events = %+v", events)
	}

	running, runningClient, id := cleanupVM(t, "running")
	running.execOut = "\n" + cleanuphook.Marker + ":0\n"
	ctx = sandbox.WithoutCleanupResume(context.Background(), reason)
	if err := runningClient.Delete(ctx, sandbox.ID(id)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if running.execs() != 1 {
		t.Fatalf("calls = %v; a running VM must still run its Cleanup", running.calls())
	}
}
