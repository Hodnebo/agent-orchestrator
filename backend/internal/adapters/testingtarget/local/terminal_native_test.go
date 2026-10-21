package local

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// JXA bridges NSArray.count as a string on this host. Inventory must normalize
// it without reading contents of pre-existing terminal windows.
func TestTerminalSnapshotNormalizesBridgedCount(t *testing.T) {
	for _, count := range []string{"0", "1", "2"} {
		t.Run(count, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			script := `const vm=require('node:vm');const source=` + strconv.Quote(terminalSnapshotScript) + `;
const value=vm.runInNewContext(source,{
 ObjC:{import(){}},
 $:{NSRunningApplication:{runningApplicationsWithBundleIdentifier(){return {count:` + strconv.Quote(count) + `,objectAtIndex(){return {processIdentifier:'90'}}}}}},
 Application(){return {windows(){return [{id(){return 72},tabs(){return [{tty(){return '/dev/ttys001'},contents(){throw Error('unowned terminal text read')}}]}}]}}}
});process.stdout.write(value);`
			out, err := exec.CommandContext(ctx, "node", "-e", script).CombinedOutput()
			if err != nil {
				t.Fatalf("terminal metadata script: %v: %s", err, out)
			}
			var windows []terminalWindow
			if err := json.Unmarshal(out, &windows); err != nil {
				t.Fatal(err)
			}
			if count == "1" {
				if len(windows) != 1 || windows[0].PID != 90 || windows[0].ID != "72" || windows[0].TTY != "/dev/ttys001" {
					t.Fatal(windows)
				}
			} else if len(windows) != 0 {
				t.Fatal("ambiguous application inventory accepted", windows)
			}
		})
	}
}
