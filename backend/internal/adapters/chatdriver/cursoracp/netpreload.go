package cursoracp

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// netPreload makes Cursor reuse its API connections. Cursor's CLI closes the
// connection after every API call, and opening a session makes about a dozen
// calls, mostly one after another, so each pays a new TCP and TLS handshake.
// Measured from a 225ms-RTT link, the preload took a fresh process's first
// session/new from a 6.9s median to 5.0s. See netpreload.cjs for the
// mechanism.
//
//go:embed netpreload.cjs
var netPreload []byte

// writeNetPreload stores the preload under AO's data dir, named by its content
// so concurrent launches never write a file another process is reading.
func writeNetPreload(dataDir string) (string, error) {
	sum := sha256.Sum256(netPreload)
	path := filepath.Join(dataDir, "cursor", fmt.Sprintf("ao-net-%x.cjs", sum[:6]))
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(netPreload) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create Cursor preload directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ao-net-*.cjs")
	if err != nil {
		return "", fmt.Errorf("create Cursor preload: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(netPreload); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write Cursor preload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write Cursor preload: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("install Cursor preload: %w", err)
	}
	return path, nil
}

// nodeOptionsWithPreload appends the preload to the NODE_OPTIONS the agent
// would otherwise inherit. Node reads forward slashes on every platform, and
// the quotes keep a path with spaces in one argument.
func nodeOptionsWithPreload(existing, preload string) string {
	flag := `--require "` + filepath.ToSlash(preload) + `"`
	if strings.TrimSpace(existing) == "" {
		return flag
	}
	return strings.TrimSpace(existing) + " " + flag
}
