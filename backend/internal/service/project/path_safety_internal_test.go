package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRepositorySetupPathSafetyUsesLegHomeWithoutOwnerAO(t *testing.T) {
	owner := t.TempDir()
	ownerAO := filepath.Join(owner, ".ao")
	legHome := filepath.Join(ownerAO, "data", "litmus", "runs", "run_safety", "head", "home")
	privateAO := filepath.Join(legHome, ".ao")
	if err := os.MkdirAll(privateAO, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", privateAO, err)
	}
	privateInfo, err := os.Lstat(privateAO)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", privateAO, err)
	}
	if privateInfo.Mode()&os.ModeSymlink != 0 || !privateInfo.IsDir() || privateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("leg .ao = mode %s, want a real 0700 directory", privateInfo.Mode())
	}

	t.Setenv("HOME", legHome)

	runScratch := filepath.Join(ownerAO, "data", "litmus", "runs", "run_safety", "scratch", "fixture")
	if err := os.MkdirAll(runScratch, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", runScratch, err)
	}
	if err := validateRepositorySetupPathSafety(runScratch); err != nil {
		t.Fatalf("validateRepositorySetupPathSafety(%q) rejected owner AO run scratch with leg HOME: %v", runScratch, err)
	}

	legAOChild := filepath.Join(privateAO, "x")
	if err := os.MkdirAll(legAOChild, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", legAOChild, err)
	}
	if err := validateRepositorySetupPathSafety(legAOChild); err == nil {
		t.Fatalf("validateRepositorySetupPathSafety(%q) accepted a path below the leg HOME .ao directory", legAOChild)
	}
}
