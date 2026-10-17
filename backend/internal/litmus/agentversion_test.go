package litmus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type agentInstallRunner struct {
	t      *testing.T
	prefix string
	calls  int
}

func (r *agentInstallRunner) Run(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
	r.calls++
	want := "install -g --prefix " + r.prefix + " --no-audit --no-fund @github/copilot@1.2.3"
	if name != "npm" || strings.Join(args, " ") != want {
		r.t.Fatalf("install: %s %q", name, args)
	}
	root := filepath.Join(r.prefix, "lib", "node_modules", "@github", "copilot")
	is(r.t, "")(nil, os.MkdirAll(root, 0o700))
	is(r.t, "")(nil, os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@github/copilot","bin":{"copilot":"index.js"}}`), 0o600))
	is(r.t, "")(nil, os.WriteFile(filepath.Join(root, "index.js"), []byte("#!/usr/bin/env node\n"), 0o700))
	is(r.t, "")(nil, os.MkdirAll(filepath.Join(r.prefix, "bin"), 0o700))
	// Like npm on macOS, create a relative link from the global bin directory.
	is(r.t, "")(nil, os.Symlink("../lib/node_modules/@github/copilot/index.js", filepath.Join(r.prefix, "bin", "copilot")))
	return nil, nil
}

func TestAgentVersionUsesNPMGlobalLayout(t *testing.T) {
	svc, _ := newSvc(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(svc.ownerHome, ".zshrc"), "fixture owner rc\n")
	writeFile(t, filepath.Join(svc.ownerHome, ".codex", "auth.json"), "fixture credentials")
	run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	run.Legs = []Leg{{Name: "head", State: "prepared"}}
	is(t, "")(nil, svc.store.Save(run))
	dir := svc.store.legDir(run.ID, "head")
	r := &agentInstallRunner{t: t, prefix: dir}
	svc.runner = r
	is(t, "")(svc.AgentVersion(ctx, "s1", "head", "copilot", "1.2.3"))
	assertLegHomeUsesOwner(t, dir, svc.ownerHome)
	target, err := os.Readlink(filepath.Join(dir, "bin", "copilot"))
	is(t, "")(nil, err)
	if target != "../lib/node_modules/@github/copilot/index.js" || r.calls != 1 {
		t.Fatalf("binary points to %s, calls %d", target, r.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "agents")); !os.IsNotExist(err) {
		t.Fatalf("unexpected local install directory: %v", err)
	}
	for _, tc := range []struct{ leg, agent, version, code string }{
		{"other", "copilot", "1.2.3", "TESTING_LEG_INVALID"},
		{"head", "unknown", "1.2.3", "TESTING_AGENT_INVALID"},
		{"head", "copilot", "latest", "TESTING_AGENT_VERSION_INVALID"},
	} {
		is(t, tc.code)(svc.AgentVersion(ctx, "s1", tc.leg, tc.agent, tc.version))
	}
	run.Legs[0].State = "running"
	is(t, "")(nil, svc.store.Save(run))
	is(t, "TESTING_LEG_RUNNING")(svc.AgentVersion(ctx, "s1", "head", "copilot", "1.2.3"))
	if r.calls != 1 {
		t.Fatal("invalid request ran npm")
	}
	svc.sys = (&fakeSys{}).ops()
	is(t, "")(svc.Cleanup(ctx, "s1"))
	for _, sub := range []string{"lib", "bin"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); !os.IsNotExist(err) {
			t.Fatalf("%s remains after cleanup: %v", sub, err)
		}
	}
}

func TestAgentVersionRejectsInvalidNPMBinary(t *testing.T) {
	for _, entry := range []string{"../../../../outside", "missing.js", "../../../../bin/copilot"} {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			root := filepath.Join(dir, "lib", "node_modules", "@github", "copilot")
			is(t, "")(nil, os.MkdirAll(root, 0o700))
			is(t, "")(nil, os.MkdirAll(bin, 0o700))
			is(t, "")(nil, os.WriteFile(filepath.Join(dir, "outside"), []byte("#!/bin/sh\n"), 0o700))
			is(t, "")(nil, os.Symlink(filepath.Join(root, entry), filepath.Join(bin, "copilot")))
			if err := validateAgentVersion(dir, "copilot"); err == nil {
				t.Fatal("accepted entry outside the installed package")
			}
		})
	}
}
