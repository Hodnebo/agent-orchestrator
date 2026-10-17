package litmus

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// prepareLegEnv isolates HOME and install prefixes for the entire leg process tree.
func prepareLegEnv(env []string, legDir, ownerHome string) ([]string, error) {
	if ownerHome == "" {
		return nil, errors.New("litmus: empty owner home for shell setup")
	}
	values := make(map[string]string, len(env)+4)
	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			values[key] = value
		}
	}
	if legDir == "" {
		return nil, errors.New("litmus: empty leg directory for shell setup")
	}
	var err error
	legDir, err = filepath.Abs(legDir)
	if err != nil {
		return nil, err
	}
	if err := buildLegHome(ownerHome, legDir); err != nil {
		return nil, err
	}
	delete(values, "GROK_HOME")
	for key, value := range legInstallEnv(legDir, ownerHome) {
		values[key] = value
	}
	path := cleanPath(values["PATH"])
	if path == "" {
		path = cleanPath(os.Getenv("PATH"))
		values["PATH"] = path
	}
	values["PATH"] = prependPath(legBinPath(legDir), path)

	if runtime.GOOS != "windows" {
		zsh, err := resolveZsh(path)
		if err != nil {
			return nil, err
		}
		shellDir := filepath.Join(legDir, "shell")
		if err := writeLegZshFiles(shellDir, values["ZDOTDIR"], ownerHome, legDir); err != nil {
			return nil, err
		}
		values["SHELL"] = zsh
		values["ZDOTDIR"] = shellDir
	}

	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	slices.Sort(result)
	return result, nil
}

func sameShellPath(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	if resolvedA, err := filepath.EvalSymlinks(a); err == nil {
		if resolvedB, err := filepath.EvalSymlinks(b); err == nil && filepath.Clean(resolvedA) == filepath.Clean(resolvedB) {
			return true
		}
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}

func prependPath(dir, path string) string {
	path = cleanPath(path)
	if dir == "" {
		return path
	}
	if path == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + path
}

func cleanPath(path string) string {
	var dirs []string
	for _, dir := range filepath.SplitList(path) {
		if dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

func resolveZsh(path string) (string, error) {
	for _, candidate := range []string{"/bin/zsh", "/usr/bin/zsh"} {
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir != "" {
			if candidate := filepath.Join(dir, "zsh"); isExecutable(candidate) {
				return candidate, nil
			}
		}
	}
	return "", errors.New("litmus: zsh is required for per-leg shell setup")
}

func writeLegZshFiles(shellDir, originalZdotDir, home, legDir string) error {
	if originalZdotDir != "" && sameShellPath(originalZdotDir, shellDir) {
		for _, file := range []string{".zshenv", ".zprofile", ".zshrc"} {
			if info, err := os.Stat(filepath.Join(shellDir, file)); err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("litmus: refusing to source generated zsh file %s as the owner's startup file", file)
			}
		}
		return nil
	}
	if originalZdotDir == "" {
		originalZdotDir = home
	}
	if originalZdotDir == "" {
		return errors.New("litmus: empty owner zsh startup directory")
	}
	if err := os.MkdirAll(shellDir, 0o700); err != nil {
		return fmt.Errorf("litmus: create per-leg zsh directory: %w", err)
	}
	content := func(profile string) []byte {
		path := filepath.Join(originalZdotDir, profile)
		var b strings.Builder
		b.WriteString("export ZDOTDIR=")
		b.WriteString(shellQuote(originalZdotDir))
		b.WriteString("\nif [ -r ")
		b.WriteString(shellQuote(path))
		b.WriteString(" ]; then . ")
		b.WriteString(shellQuote(path))
		b.WriteString("; fi\nexport ZDOTDIR=")
		b.WriteString(shellQuote(shellDir))
		b.WriteString("\n")
		b.WriteString(legShellTrailer(legDir, home))
		return []byte(b.String())
	}
	for _, file := range []string{".zshenv", ".zprofile", ".zshrc"} {
		if err := writeAtomic(filepath.Join(shellDir, file), content(file)); err != nil {
			return fmt.Errorf("litmus: write per-leg %s: %w", file, err)
		}
		if err := os.Chmod(filepath.Join(shellDir, file), 0o600); err != nil {
			return fmt.Errorf("litmus: set per-leg %s permissions: %w", file, err)
		}
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

var legRCFiles = []string{".zshrc", ".zprofile", ".zshenv", ".bashrc", ".bash_profile", ".profile", ".gitconfig"}

var legBashFiles = []string{".bash_profile", ".profile", ".bashrc"}

func legBinPath(legDir string) string {
	home := filepath.Join(legDir, "home")
	return strings.Join([]string{
		filepath.Join(legDir, "bin"), filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".kimi-code", "bin"), filepath.Join(home, ".grok", "bin"), filepath.Join(home, ".opencode", "bin"),
	}, string(os.PathListSeparator))
}

func legShellTrailer(legDir, ownerHome string) string {
	var b strings.Builder
	b.WriteString("\nold_path=${PATH:-}\nexport PATH=")
	b.WriteString(shellQuote(legBinPath(legDir)))
	b.WriteString("\nwhile [ -n \"$old_path\" ]; do\n")
	b.WriteString("  case $old_path in *:*) path_entry=${old_path%%:*}; old_path=${old_path#*:} ;; *) path_entry=$old_path; old_path= ;; esac\n")
	b.WriteString("  if [ -n \"$path_entry\" ]; then PATH=$PATH:$path_entry; fi\ndone\nexport PATH\n")
	installEnv := legInstallEnv(legDir, ownerHome)
	if _, ok := installEnv["GROK_HOME"]; !ok {
		b.WriteString("unset GROK_HOME\n")
	}
	for _, key := range slices.Sorted(maps.Keys(installEnv)) {
		fmt.Fprintf(&b, "export %s=%s\n", key, shellQuote(installEnv[key]))
	}
	return b.String()
}

func legInstallEnv(legDir, ownerHome string) map[string]string {
	env := map[string]string{
		"HOME":              filepath.Join(legDir, "home"),
		"GOBIN":             filepath.Join(legDir, "bin"),
		"npm_config_prefix": legDir, "NPM_CONFIG_PREFIX": legDir,
		"UV_TOOL_DIR": filepath.Join(legDir, "uv", "tools"), "UV_TOOL_BIN_DIR": filepath.Join(legDir, "bin"),
		"PIPX_HOME": filepath.Join(legDir, "pipx"), "PIPX_BIN_DIR": filepath.Join(legDir, "bin"),
		"HOMEBREW_FORBIDDEN_TAPS": "homebrew/core", "HOMEBREW_ALLOWED_TAPS": "none/none",
		"HOMEBREW_FORBID_CASKS": "1", "HOMEBREW_NO_AUTO_UPDATE": "1",
	}
	if info, err := os.Stat(filepath.Join(ownerHome, ".grok")); err == nil && info.IsDir() {
		env["GROK_HOME"] = filepath.Join(ownerHome, ".grok")
	}
	return env
}

type legInstallRoot struct {
	path     string
	children []string
	whole    bool
}

var legInstallRoots = []legInstallRoot{
	{path: ".opencode"},
	{path: ".grok", children: []string{"bin", "downloads", "completions", "config.toml", "managed_config.toml", "requirements.toml"}},
	{path: ".kimi-code", children: []string{"bin", "updates"}},
	{path: ".local/share/claude", whole: true},
}

func (root legInstallRoot) clonedPaths() []string {
	if root.whole {
		return []string{filepath.FromSlash(root.path)}
	}
	paths := make([]string, 0, len(root.children))
	for _, child := range root.children {
		paths = append(paths, filepath.Join(filepath.FromSlash(root.path), child))
	}
	return paths
}

// Tests inject a failure at the same boundary used for every APFS clone.
var cloneLegInstall = cloneInstallPath

// buildLegHome never overwrites a leg's installs or copied rc files on relaunch.
// Credentials remain links, so refresh-token rotation reaches the owner's files.
func buildLegHome(ownerHome, legDir string) error {
	owner, err := filepath.EvalSymlinks(ownerHome)
	if err != nil {
		return fmt.Errorf("litmus: resolve owner home: %w", err)
	}
	leg, err := filepath.Abs(legDir)
	if err != nil {
		return err
	}
	resolved, err := resolveMissingPath(leg)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(owner, resolved); err == nil && filepath.IsLocal(rel) {
		// The normal run lives inside ~/.ao. No other owner-home location is an
		// authorized leg destination, including a symlink into an install dir.
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 5 || parts[0] != ".ao" || parts[len(parts)-4] != "litmus" || parts[len(parts)-3] != "runs" || (parts[len(parts)-1] != "head" && parts[len(parts)-1] != "base") {
			return fmt.Errorf("litmus: refusing leg directory in owner home: %s", legDir)
		}
	}
	dirs := []string{"", "home", "home/.ao", "home/.local", "home/.local/bin", "home/.local/share", "home/.local/lib", "home/.local/state", "bin", "lib", "lib/node_modules", "uv", "uv/tools", "pipx", "shell"}
	var installRoots []legInstallRoot
	for _, root := range legInstallRoots {
		if err := checkLegWriteDir(leg, filepath.Join(leg, "home", filepath.FromSlash(root.path))); err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(owner, filepath.FromSlash(root.path)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("litmus: inspect owner install root %s: %w", root.path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("litmus: owner install root is not a directory: %s", root.path)
		}
		installRoots = append(installRoots, root)
	}
	// Validate every install target before the first write. MkdirAll alone would
	// silently accept a symlink to the owner's install directories.
	for _, dir := range dirs {
		if err := checkLegWriteDir(leg, filepath.Join(leg, filepath.FromSlash(dir))); err != nil {
			return err
		}
	}
	home := filepath.Join(leg, "home")
	for _, name := range legRCFiles {
		if info, err := os.Lstat(filepath.Join(home, name)); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("litmus: refusing non-file leg rc: %s", name)
			}
			if source, err := os.Stat(filepath.Join(owner, name)); err == nil && os.SameFile(info, source) {
				return fmt.Errorf("litmus: refusing owner-linked leg rc: %s", name)
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(leg, filepath.FromSlash(dir)), 0o700); err != nil {
			return fmt.Errorf("litmus: create leg directory: %w", err)
		}
	}
	if err := os.Chmod(filepath.Join(home, ".ao"), 0o700); err != nil {
		return fmt.Errorf("litmus: set private leg AO directory permissions: %w", err)
	}
	entries, err := os.ReadDir(owner)
	if err != nil {
		return fmt.Errorf("litmus: read owner home: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".ao" || name == ".local" || slices.Contains(legRCFiles, name) || slices.ContainsFunc(legInstallRoots, func(root legInstallRoot) bool { return root.path == name }) {
			continue
		}
		if err := mirrorHomeEntry(filepath.Join(owner, name), filepath.Join(home, name), leg); err != nil {
			return err
		}
	}
	if err := mirrorHomeChildren(filepath.Join(owner, ".local"), filepath.Join(home, ".local"), []string{"bin", "share", "lib", "state"}, leg); err != nil {
		return err
	}
	for _, name := range []string{"bin", "share", "lib", "state"} {
		var skip []string
		for _, root := range legInstallRoots {
			if filepath.Dir(filepath.FromSlash(root.path)) == filepath.Join(".local", name) {
				skip = append(skip, filepath.Base(root.path))
			}
		}
		if err := mirrorHomeChildren(filepath.Join(owner, ".local", name), filepath.Join(home, ".local", name), skip, leg); err != nil {
			return err
		}
	}
	for _, root := range installRoots {
		dest := filepath.Join(home, filepath.FromSlash(root.path))
		if !root.whole {
			if err := os.MkdirAll(dest, 0o700); err != nil {
				return fmt.Errorf("litmus: create private install root %s: %w", root.path, err)
			}
		}
		for _, path := range root.clonedPaths() {
			if err := cloneHomeEntry(filepath.Join(owner, path), filepath.Join(home, path)); err != nil {
				return err
			}
		}
		if err := os.Chmod(dest, 0o700); err != nil {
			return fmt.Errorf("litmus: set private install root permissions %s: %w", root.path, err)
		}
		if !root.whole {
			if err := mirrorHomeChildren(filepath.Join(owner, filepath.FromSlash(root.path)), dest, root.children, leg); err != nil {
				return err
			}
		}
	}
	if err := retargetLegBinLinks(owner, home); err != nil {
		return err
	}
	for _, name := range legRCFiles {
		dest := filepath.Join(home, name)
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := os.ReadFile(filepath.Join(owner, name))
		bash := slices.Contains(legBashFiles, name)
		if os.IsNotExist(err) && !bash {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("litmus: read owner %s: %w", name, err)
		}
		if name == ".bash_profile" && os.IsNotExist(err) {
			// Creating .bash_profile must preserve bash's normal login fallback.
			login := shellQuote(filepath.Join(home, ".bash_login"))
			profile := shellQuote(filepath.Join(home, ".profile"))
			data = fmt.Appendf(data, "if [ -r %s ]; then . %s; elif [ -r %s ]; then . %s; fi\n", login, login, profile, profile)
		}
		if bash {
			data = append(data, legShellTrailer(leg, ownerHome)...)
		}
		if err := writeAtomic(dest, data); err != nil {
			return fmt.Errorf("litmus: copy leg %s: %w", name, err)
		}
	}
	return nil
}

func cloneHomeEntry(source, dest string) error {
	if info, err := os.Lstat(dest); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("litmus: refusing symlink install clone destination %s; refusing to start leg", dest)
		}
		if sourceInfo, err := os.Stat(source); err == nil && os.SameFile(info, sourceInfo) {
			return fmt.Errorf("litmus: refusing owner-linked install clone destination: %s", dest)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("litmus: refusing symlink install clone source %s; refusing to start leg", source)
	}
	if err := cloneLegInstall(source, dest); err != nil {
		return fmt.Errorf("litmus: APFS clone %s to %s failed; refusing to start leg: %w", source, dest, err)
	}
	return nil
}

func retargetLegBinLinks(owner, home string) error {
	bin := filepath.Join(owner, ".local", "bin")
	entries, err := os.ReadDir(bin)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		source, dest := filepath.Join(bin, entry.Name()), filepath.Join(home, ".local", "bin", entry.Name())
		link, err := os.Readlink(dest)
		if err != nil {
			if info, statErr := os.Lstat(dest); statErr == nil && info.Mode()&os.ModeSymlink == 0 {
				continue // Preserve a leg's replacement binary on relaunch.
			}
			return err
		}
		if link != source {
			continue // Preserve links installed by the leg itself.
		}
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(bin, target)
		}
		for hops := 0; ; hops++ {
			if hops == 32 {
				return errors.New("litmus: too many owner .local/bin symlink hops")
			}
			// Keep the final symlink component. An owner link may point at a
			// mutable install link such as .grok/bin/grok -> ../downloads/A.
			// Retargeting through that link would bypass later relinks in the leg.
			targetDir, err := resolveMissingPath(filepath.Dir(target))
			if err != nil {
				return err
			}
			target = filepath.Join(targetDir, filepath.Base(target))
			if !pathWithin(bin, target) {
				break
			}
			info, err := os.Lstat(target)
			if os.IsNotExist(err) || err != nil || info.Mode()&os.ModeSymlink == 0 {
				break
			}
			next, err := os.Readlink(target)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(next) {
				next = filepath.Join(filepath.Dir(target), next)
			}
			target = next
		}
		for _, root := range legInstallRoots {
			for _, cloned := range root.clonedPaths() {
				rel, err := filepath.Rel(filepath.Join(owner, cloned), target)
				if err != nil || !filepath.IsLocal(rel) {
					continue
				}
				if _, err := os.Lstat(filepath.Join(home, cloned)); os.IsNotExist(err) {
					continue
				} else if err != nil {
					return err
				}
				if err := os.Remove(dest); err != nil {
					return err
				}
				if err := os.Symlink(filepath.Join(home, cloned, rel), dest); err != nil {
					return fmt.Errorf("litmus: point leg binary at clone: %w", err)
				}
			}
		}
	}
	return nil
}

// resolveMissingPath resolves existing ancestors without creating anything.
func resolveMissingPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) || filepath.Dir(path) == path {
		return "", err
	}
	parent, err := resolveMissingPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func checkLegWriteDir(leg, target string) error {
	for path := target; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil && !info.IsDir() {
			return fmt.Errorf("litmus: refusing redirected leg directory: %s", path)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if path == leg {
			return nil
		}
	}
}

func mirrorHomeChildren(source, dest string, skip []string, leg string) error {
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("litmus: read owner directory %s: %w", source, err)
	}
	for _, entry := range entries {
		if !slices.Contains(skip, entry.Name()) {
			if err := mirrorHomeEntry(filepath.Join(source, entry.Name()), filepath.Join(dest, entry.Name()), leg); err != nil {
				return err
			}
		}
	}
	return nil
}

func mirrorHomeEntry(source, dest, leg string) error {
	if info, err := os.Lstat(dest); err == nil {
		if info.Mode().IsRegular() {
			if sourceInfo, err := os.Stat(source); err == nil && os.SameFile(info, sourceInfo) {
				return fmt.Errorf("litmus: refusing owner-linked home mirror: %s", dest)
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(dest)
			if err != nil {
				return err
			}
			if target != source {
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(dest), target)
				}
				resolved, err := resolveMissingPath(target)
				if err != nil {
					return err
				}
				realLeg, err := filepath.EvalSymlinks(leg)
				if err != nil {
					return err
				}
				if !pathWithin(realLeg, resolved) {
					return fmt.Errorf("litmus: refusing redirected home mirror: %s", dest)
				}
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(source, dest); err != nil {
		return fmt.Errorf("litmus: mirror owner entry: %w", err)
	}
	return nil
}
