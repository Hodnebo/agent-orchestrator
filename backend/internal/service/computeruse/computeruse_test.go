package computeruse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// pngHeader is enough for http.DetectContentType to call it image/png.
var pngHeader = []byte("\x89PNG\r\n\x1a\n0000")

type fakeDriver struct {
	mu       sync.Mutex
	missing  bool
	daemonUp bool
	startErr error
	calls    [][]string
	onCall   func(args []string) ([]byte, []byte, error)
	started  int
	startsUp bool
}

func (f *fakeDriver) Locate(context.Context) (string, error) {
	if f.missing {
		return "", ports.ErrComputerUseDriverNotFound
	}
	return "/opt/cua-driver", nil
}

func (f *fakeDriver) Run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	up := f.daemonUp
	f.mu.Unlock()
	switch args[0] {
	case "--version":
		return []byte("cua-driver 0.28.2\n"), nil, nil
	case "status":
		if up {
			return []byte("Cua Driver daemon is running\n  pid: 1\n"), nil, nil
		}
		return nil, []byte("Cua Driver daemon is not running.\n"), errors.New("exit status 1")
	case "check-update":
		return []byte(`{"latest_version":"0.37.0","update_available":true,"release_notes_url":"https://example.test/notes"}`), nil, nil
	case "permissions":
		return []byte(`{"accessibility":true,"screen_recording":false}`), nil, nil
	case "list-tools":
		return []byte("click: Click against a target pid\nzoom: Capture a cropped JPEG\nnot a tool line\n"), nil, nil
	}
	if f.onCall != nil {
		return f.onCall(args)
	}
	return []byte(`{}`), nil, nil
}

func (f *fakeDriver) StartDaemon(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	if f.startErr != nil {
		return f.startErr
	}
	if f.startsUp {
		f.daemonUp = true
	}
	return nil
}

func newTestService(t *testing.T, driver *fakeDriver, goos string) *Service {
	t.Helper()
	s := New(driver, t.TempDir())
	s.goos = goos
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func TestStatusNotInstalledIsNotAnError(t *testing.T) {
	s := newTestService(t, &fakeDriver{missing: true}, "darwin")
	status, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Installed || status.Daemon != DaemonUnknown || status.Platform != "darwin" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.HasSuffix(status.ImageDirectory, "computer-use") {
		t.Fatalf("ImageDirectory = %q", status.ImageDirectory)
	}
}

func TestStatusReportsVersionDaemonUpdateAndPermissions(t *testing.T) {
	s := newTestService(t, &fakeDriver{daemonUp: true}, "darwin")
	status, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || status.BinaryPath != "/opt/cua-driver" || status.Version != "0.28.2" {
		t.Fatalf("status = %+v", status)
	}
	if status.Daemon != DaemonRunning {
		t.Fatalf("Daemon = %q", status.Daemon)
	}
	if !status.UpdateAvailable || status.LatestVersion != "0.37.0" || status.ReleaseNotesURL == "" {
		t.Fatalf("update = %+v", status)
	}
	if status.Permissions == nil || status.Permissions.Accessibility != PermissionGranted || status.Permissions.ScreenRecording != PermissionMissing {
		t.Fatalf("Permissions = %+v", status.Permissions)
	}
}

// Permissions are read from the running service; with it stopped the driver
// could only report the caller's grants, which are not CuaDriver.app's.
func TestStatusPermissionsUnknownWhileDaemonStopped(t *testing.T) {
	s := newTestService(t, &fakeDriver{}, "darwin")
	status, _ := s.Status(context.Background())
	if status.Daemon != DaemonStopped {
		t.Fatalf("Daemon = %q", status.Daemon)
	}
	if status.Permissions.Accessibility != PermissionUnknown || status.Permissions.ScreenRecording != PermissionUnknown {
		t.Fatalf("Permissions = %+v", status.Permissions)
	}
}

func TestStatusOmitsPermissionsOffMacOS(t *testing.T) {
	s := newTestService(t, &fakeDriver{daemonUp: true}, "linux")
	status, _ := s.Status(context.Background())
	if status.Permissions != nil {
		t.Fatalf("Permissions = %+v, want nil on linux", status.Permissions)
	}
}

func TestGrantPermissionsOnlyOnMacOS(t *testing.T) {
	s := newTestService(t, &fakeDriver{daemonUp: true}, "windows")
	_, err := s.GrantPermissions(context.Background())
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindInvalid {
		t.Fatalf("err = %v, want invalid", err)
	}
}

// The core promise: a screenshot tool is pointed at a file under AO's data
// directory, and a path the caller supplied is replaced.
func TestCallWritesScreenshotsToFiles(t *testing.T) {
	driver := &fakeDriver{daemonUp: true}
	var sent map[string]any
	driver.onCall = func(args []string) ([]byte, []byte, error) {
		if err := json.Unmarshal([]byte(args[2]), &sent); err != nil {
			return nil, nil, err
		}
		path, _ := sent["screenshot_out_file"].(string)
		if err := os.WriteFile(path, pngHeader, 0o600); err != nil {
			return nil, nil, err
		}
		return []byte(fmt.Sprintf(`{"screenshot_file_path":%q,"elements":[]}`, path)), nil, nil
	}
	s := newTestService(t, driver, "darwin")

	result, err := s.Call(context.Background(), CallRequest{
		Tool:      "get_window_state",
		Arguments: map[string]any{"pid": 7, "window_id": 9, "screenshot_out_file": "/etc/elsewhere.png"},
		SessionID: "ao-12",
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	path, _ := sent["screenshot_out_file"].(string)
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(s.images.root, "ao-12"))
	if filepath.Dir(path) != wantDir || !strings.HasSuffix(path, "-get_window_state-1.png") {
		t.Fatalf("screenshot_out_file = %q, want a file in %q", path, wantDir)
	}
	if len(result.Images) != 1 || result.Images[0].Path != path || result.Images[0].MimeType != "image/png" {
		t.Fatalf("Images = %+v", result.Images)
	}
}

func TestCallLeavesTreeOnlySnapshotsAlone(t *testing.T) {
	driver := &fakeDriver{daemonUp: true}
	var sent map[string]any
	driver.onCall = func(args []string) ([]byte, []byte, error) {
		_ = json.Unmarshal([]byte(args[2]), &sent)
		return []byte(`{"elements":[]}`), nil, nil
	}
	s := newTestService(t, driver, "darwin")
	result, err := s.Call(context.Background(), CallRequest{Tool: "get_window_state", Arguments: map[string]any{"pid": 1, "window_id": 2, "include_screenshot": false, "screenshot_out_file": "/tmp/x.png"}})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if _, ok := sent["screenshot_out_file"]; ok {
		t.Fatalf("screenshot_out_file forwarded on a tree-only call: %v", sent)
	}
	if result.Images == nil || len(result.Images) != 0 {
		t.Fatalf("Images = %#v, want an empty list", result.Images)
	}
}

// Tools without a file option (zoom) still print base64; it is decoded to a
// file and never passed through.
func TestCallExternalizesInlineImages(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(pngHeader)
	driver := &fakeDriver{daemonUp: true}
	driver.onCall = func([]string) ([]byte, []byte, error) {
		return []byte(fmt.Sprintf(`{"width":480,"screenshot_png_b64":%q,"content":[{"type":"image","data":%q,"mimeType":"image/png"},{"type":"text","text":"ok"}],"broken_b64":"%%%%"}`, encoded, encoded)), nil, nil
	}
	s := newTestService(t, driver, "linux")
	result, err := s.Call(context.Background(), CallRequest{Tool: "zoom", Arguments: map[string]any{"pid": 1}})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	raw, _ := json.Marshal(result.Result)
	if strings.Contains(string(raw), encoded) || strings.Contains(string(raw), "_b64") {
		t.Fatalf("result still carries inline image data: %s", raw)
	}
	out := result.Result.(map[string]any)
	path, _ := out["screenshot_png_file_path"].(string)
	if data, err := os.ReadFile(path); err != nil || string(data) != string(pngHeader) {
		t.Fatalf("screenshot_png_file_path %q: %v", path, err)
	}
	block := out["content"].([]any)[0].(map[string]any)
	if _, ok := block["path"].(string); !ok {
		t.Fatalf("image block = %+v, want a path", block)
	}
	if out["broken_error"] == nil {
		t.Fatalf("undecodable image should report an error: %+v", out)
	}
	if len(result.Images) != 2 {
		t.Fatalf("Images = %+v", result.Images)
	}
}

func TestCallWrapsNonJSONOutput(t *testing.T) {
	driver := &fakeDriver{daemonUp: true}
	driver.onCall = func([]string) ([]byte, []byte, error) { return []byte("\x1b[32mdone\x1b[0m\n"), nil, nil }
	s := newTestService(t, driver, "linux")
	result, err := s.Call(context.Background(), CallRequest{Tool: "press_key"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if text := result.Result.(map[string]any)["text"]; text != "done" {
		t.Fatalf("text = %q", text)
	}
}

func TestCallStartsStoppedDaemonAndRetries(t *testing.T) {
	driver := &fakeDriver{startsUp: true}
	attempts := 0
	driver.onCall = func([]string) ([]byte, []byte, error) {
		attempts++
		driver.mu.Lock()
		up := driver.daemonUp
		driver.mu.Unlock()
		if !up {
			return nil, []byte("Cua Driver daemon is not running. Start it with: open -n -g -a CuaDriver --args serve"), errors.New("exit status 1")
		}
		return []byte(`{"apps":[]}`), nil, nil
	}
	s := newTestService(t, driver, "darwin")
	if _, err := s.Call(context.Background(), CallRequest{Tool: "list_apps"}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if driver.started != 1 || attempts != 2 {
		t.Fatalf("started = %d, attempts = %d", driver.started, attempts)
	}
}

func TestCallReportsDaemonStartFailure(t *testing.T) {
	driver := &fakeDriver{startErr: errors.New("no desktop session")}
	driver.onCall = func([]string) ([]byte, []byte, error) {
		return nil, []byte("Cua Driver daemon is not running."), errors.New("exit status 1")
	}
	s := newTestService(t, driver, "linux")
	_, err := s.Call(context.Background(), CallRequest{Tool: "list_apps"})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "COMPUTER_USE_DAEMON_START_FAILED" || !strings.Contains(apiErr.Message, "no desktop session") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallSurfacesDriverErrors(t *testing.T) {
	driver := &fakeDriver{daemonUp: true}
	driver.onCall = func([]string) ([]byte, []byte, error) {
		return nil, []byte("window_id_not_found: window 9 no longer exists"), errors.New("exit status 1")
	}
	s := newTestService(t, driver, "darwin")
	_, err := s.Call(context.Background(), CallRequest{Tool: "click", Arguments: map[string]any{"pid": 1}})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "COMPUTER_USE_CALL_FAILED" || !strings.Contains(apiErr.Message, "window_id_not_found") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallValidatesInput(t *testing.T) {
	s := newTestService(t, &fakeDriver{daemonUp: true}, "darwin")
	for name, request := range map[string]CallRequest{
		"empty tool":       {},
		"flag injection":   {Tool: "--help"},
		"path tool":        {Tool: "../x"},
		"session traverse": {Tool: "list_apps", SessionID: "../../etc"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Call(context.Background(), request)
			var apiErr *apierr.Error
			if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindInvalid {
				t.Fatalf("err = %v, want invalid", err)
			}
		})
	}
}

func TestCallWhenNotInstalled(t *testing.T) {
	s := newTestService(t, &fakeDriver{missing: true}, "darwin")
	_, err := s.Call(context.Background(), CallRequest{Tool: "list_apps"})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "COMPUTER_USE_NOT_INSTALLED" {
		t.Fatalf("err = %v", err)
	}
}

func TestToolsParsesListOutput(t *testing.T) {
	s := newTestService(t, &fakeDriver{daemonUp: true}, "darwin")
	tools, err := s.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "click" || tools[1].Description != "Capture a cropped JPEG" {
		t.Fatalf("tools = %+v", tools)
	}
}

// Cua Driver refuses an output path under a symlinked folder, such as macOS's
// /tmp, so the scope folder is handed over resolved.
func TestScopeResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dir, err := newImageStore(link).scope("ao-1")
	if err != nil {
		t.Fatalf("scope() error = %v", err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(real, "computer-use", "ao-1"))
	if dir != want {
		t.Fatalf("scope = %q, want resolved %q", dir, want)
	}
}

func TestPruneKeepsNewestImages(t *testing.T) {
	store := newImageStore(t.TempDir())
	dir, err := store.scope("")
	if err != nil {
		t.Fatalf("scope() error = %v", err)
	}
	if filepath.Base(dir) != sharedScope {
		t.Fatalf("scope = %q", dir)
	}
	for index := 0; index < keepImages+5; index++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%05d.png", index)), nil, 0o600)
	}
	store.prune(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != keepImages || entries[0].Name() != "00005.png" {
		t.Fatalf("kept %d, oldest %s", len(entries), entries[0].Name())
	}
}

func TestParseVersion(t *testing.T) {
	for input, want := range map[string]string{"cua-driver 0.28.2\n": "0.28.2", "v1.2.3": "1.2.3", "": ""} {
		if got := parseVersion(input); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", input, got, want)
		}
	}
}
