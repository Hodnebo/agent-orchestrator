// Package agentaccount runs Claude Code and Codex under subscription accounts
// that are config folders of the CLIs themselves. Each folder keeps only its
// own login; everything a conversation needs to resume under another account
// is a link to the computer's own folder. Nothing here reads a token.
package agentaccount

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// linkShared makes home/name a symlink to shared/name. A missing target is
// created first (a directory, or an empty file when file is true), so the link
// never dangles. An existing link is left alone; so is a real entry, unless it
// is an empty directory or replaceable, which the CLI made before the link did.
func linkShared(home, shared, name string, file, replaceable bool) error {
	target := filepath.Join(shared, name)
	link := filepath.Join(home, name)
	if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		if file {
			f, createErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
			if createErr != nil {
				return fmt.Errorf("create shared %s: %w", name, createErr)
			}
			_ = f.Close()
		} else if mkErr := os.MkdirAll(target, 0o700); mkErr != nil {
			return fmt.Errorf("create shared %s: %w", name, mkErr)
		}
	} else if err != nil {
		return err
	}
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return nil
	case info.IsDir() && emptyDir(link), replaceable:
		if err := os.RemoveAll(link); err != nil {
			return fmt.Errorf("replace %s: %w", name, err)
		}
	default:
		return nil // the account's own copy; leave it rather than lose it
	}
	if err := os.Symlink(target, link); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("link %s: %w", name, err)
	}
	return nil
}

func emptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

// writeIfChanged atomically replaces path with data unless it already holds it.
func writeIfChanged(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// neutralize returns key="" for each key set in the daemon's environment, so a
// key or token the computer has for its own login cannot override the account.
func neutralize(env map[string]string, keys ...string) {
	for _, key := range keys {
		if _, set := os.LookupEnv(key); set {
			env[key] = ""
		}
	}
}
