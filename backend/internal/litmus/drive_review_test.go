package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareActionOutputWritesOnlyClippedOutputAndCapsFile(t *testing.T) {
	store := NewStore(t.TempDir())
	service := newTestService(t, store, nil)
	short := actionRec{Seq: 1, Action: "eval"}
	service.prepareActionOutput("run", &short, "short result")
	if short.Output != "short result" || short.OutputPath != "" {
		t.Fatalf("short output = %+v", short)
	}
	if _, err := os.Stat(filepath.Join(store.root, "run", "outputs")); err == nil {
		t.Fatal("short output created an outputs directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking output directory: %v", err)
	}

	long := actionRec{Seq: 2, Action: "eval"}
	service.prepareActionOutput("run", &long, strings.Repeat("x", actionOutputFileCap+64))
	if long.OutputPath == "" || len([]byte(long.Output)) > actionOutputCap || !strings.Contains(long.Output, "full output: outputs/2-eval.txt") {
		t.Fatalf("clipped output record = %+v", long)
	}
	data, err := os.ReadFile(long.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > actionOutputFileCap || !strings.HasSuffix(string(data), "\n[full output truncated at 1048576 bytes]") {
		t.Fatalf("full-output file has %d bytes or no truncation marker", len(data))
	}
}

func TestScrubRedactsCookieValueWithSeparateNameField(t *testing.T) {
	service := newTestService(t, NewStore(t.TempDir()), nil)
	input := `{"cookies":[{"value":"cookie-secret","name":"custom-app-cookie","domain":"example.test"}]}`
	got := service.scrub(input)
	var parsed struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("scrub returned invalid JSON %q: %v", got, err)
	}
	if len(parsed.Cookies) != 1 || parsed.Cookies[0].Name != "custom-app-cookie" || parsed.Cookies[0].Value != "[REDACTED]" || strings.Contains(got, "cookie-secret") {
		t.Fatalf("cookie output was not redacted: %s", got)
	}
}

func TestClipBytesKeepsValidPrefixWhenCutMidRune(t *testing.T) {
	got := clipBytes("aéz", 2)
	if got != "a" {
		t.Fatalf("clipBytes returned %q, want valid prefix %q", got, "a")
	}
}

func TestClickTargetScrollsZeroSizeAndIFrameRefs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		snapshot   string
		initialBox string
		afterBox   string
		wantClickX float64
	}{
		{name: "zero size", snapshot: `- button [ref=e1]`, initialBox: `{"x":10,"y":20,"width":0,"height":0}`, afterBox: `{"x":50,"y":30,"width":20,"height":10}`, wantClickX: 60},
		{name: "iframe element", snapshot: `- iframe [ref=e1]`, initialBox: `{"x":10,"y":20,"width":100,"height":40}`, afterBox: `{"x":50,"y":30,"width":100,"height":40}`, wantClickX: 100},
		{name: "button inside iframe", snapshot: "- iframe [ref=e2]\n  - button \"Continue\" [ref=e1]", initialBox: `{"x":10,"y":20,"width":100,"height":40}`, afterBox: `{"x":50,"y":30,"width":100,"height":40}`, wantClickX: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEngine{fail: map[string]error{}, out: map[string]string{"get": tc.initialBox}}
			fake.hook = func(args []string) {
				if len(args) > 0 && args[0] == "scrollintoview" {
					fake.out["get"] = tc.afterBox
				}
			}
			engine := newEngine(EngineInfo{
				Binary:    "/fake/agent-browser",
				RunDir:    t.TempDir(),
				Session:   "head-test",
				Namespace: "head-test",
				AppOrigin: "http://app.test",
			}, fake.exec)
			t.Cleanup(func() { engine.Close(realSys()) })
			engine.refs = parseRefs(tc.snapshot)
			if tc.name == "button inside iframe" && !engine.refs["@e1"].inIframe {
				t.Fatal("snapshot ref lost iframe ancestry")
			}
			engine.connectPage = func(context.Context) (*pageCDP, error) {
				return &pageCDP{request: func(_ context.Context, method string, params, out any) error {
					if method != "Runtime.evaluate" || params.(map[string]any)["expression"] != "[innerWidth, innerHeight]" {
						t.Fatalf("unexpected viewport request: %s %v", method, params)
					}
					data, _ := json.Marshal(map[string]any{"result": map[string]any{"value": []float64{1280, 720}}})
					return json.Unmarshal(data, out)
				}}, nil
			}

			click, initial, visible, err := engine.clickTarget(context.Background(), "@e1")
			if err != nil {
				t.Fatal(err)
			}
			if initial == nil || visible == nil || click == nil || click.X != tc.wantClickX {
				t.Fatalf("initial=%+v visible=%+v click=%+v, want x=%v", initial, visible, click, tc.wantClickX)
			}
			var words []string
			for _, call := range fake.calls {
				if call[0] == "get" || call[0] == "scrollintoview" {
					words = append(words, call[0])
				}
			}
			if got := strings.Join(words, ","); got != "get,scrollintoview,get" {
				t.Fatalf("click measurement calls = %s", got)
			}
		})
	}
}
