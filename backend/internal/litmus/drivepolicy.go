package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// driveRefusals lists escape paths, rather than the browser actions a tester may use.
// Entries match a command prefix or a flag anywhere in the command.
var driveRefusals = []struct{ words, reason string }{
	{"connect", "attaches to a different browser endpoint"},
	{"profiles", "reads the owner's browser profiles"},
	{"auth", "uses saved credentials or external credential providers"},
	{"install", "installs software on the host"},
	{"upgrade", "replaces software on the host"},
	{"doctor", "inspects or repairs host installations"},
	{"plugin", "runs host plugins outside the leg"},
	{"plugins", "runs host plugins outside the leg"},
	{"window new", "opens a target outside the leg's app page"},
	{"session list", "inspects other browser sessions"},
	{"clipboard", "accesses the host clipboard"},
	{"inspect", "opens a separate DevTools window"},
	{"dashboard", "opens a host control server"},
	{"mcp", "opens a browser control server"},
	{"chat", "delegates browser commands outside drive's guards"},
	{"stream enable", "opens a browser control socket"},
	{"cookies import", "imports credentials from outside the leg"},
	{"--cdp", "the leg's CDP endpoint is fixed"},
	{"--auto-connect", "could attach to the owner's browser"},
	{"--session", "the leg's session is fixed"},
	{"--session-name", "could restore another session's credentials"},
	{"--namespace", "the leg's namespace is fixed"},
	{"--profile", "could load the owner's browser profile"},
	{"--state", "could restore external credentials"},
	{"--restore", "could restore external credentials"},
	{"--restore-save", "could persist external credentials"},
	{"--config", "could override the leg's connection and isolation"},
	{"--executable-path", "could launch a different browser"},
	{"--extension", "could run host browser extensions"},
	{"--init-script", "could read scripts outside the leg"},
	{"--args", "could override browser isolation"},
	{"--proxy", "could route through an external credential provider"},
	{"--plugin", "could run host plugins"},
	{"--provider", "could attach to another browser provider"},
	{"-p", "could attach to another browser provider"},
	{"--engine", "could launch another browser engine"},
	{"--allow-file-access", "could read host files"},
	{"--ca-cert", "could read or install host certificates"},
	{"--no-ca-cert", "could change host certificate trust"},
	{"--no-pin-tab", "could detach from the leg's target"},
	{"--pin-tab", "the leg's target binding is managed by Litmus"},
	{"--new-tab", "opens a target outside the leg's app page"},
	{"--new-window", "opens a window outside the leg's app page"},
	{"--all", "could affect other browser sessions"},
}

// readOnly stays small: unknown commands and subcommands get an after-shot.
func readOnly(action string, args []string) bool {
	switch action {
	case "snapshot", "screenshot", "console", "errors", "get", "is":
		return true
	case "network":
		return len(args) == 0 || args[0] == "requests" || args[0] == "request" || strings.HasPrefix(args[0], "--")
	case "tab":
		return len(args) == 0 || args[0] == "list" || args[0] == "--json"
	case "find":
		i := 2
		if len(args) > 0 && args[0] == "nth" {
			i = 3
		}
		return len(args) > i && args[i] == "text"
	}
	return false
}

// refArgs identifies selector arguments without changing text values such as fill's text.
func refArgs(action string, args []string) []int {
	switch action {
	case "click", "dblclick", "hover", "focus", "fill", "type", "select", "check", "uncheck", "upload", "download", "scrollintoview", "highlight", "wait", "frame", "tap":
		return []int{0}
	case "drag":
		return []int{0, 1}
	case "get", "is":
		return []int{1}
	case "screenshot":
		if len(args) > 0 && refPattern.MatchString(args[0]) {
			return []int{0}
		}
	case "find":
		if len(args) > 0 {
			switch args[0] {
			case "first", "last":
				return []int{1}
			case "nth":
				return []int{2}
			}
		}
	}
	var positions []int
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--selector" || args[i] == "-s" {
			positions = append(positions, i+1)
		}
	}
	return positions
}

// argv preserves command arguments, normalizes refs, and confines file inputs/outputs to this run.
func argv(action string, args []string, runDir, leg string) (cmd []string, file string, err error) {
	bad := func(msg string) ([]string, string, error) { return nil, "", invalid("TESTING_ARGS_INVALID", msg) }
	if action == "" || strings.HasPrefix(action, "-") {
		return bad("expected an agent-browser command")
	}
	args, options, pathErr := browserOptions(args, runDir)
	if pathErr != nil {
		return nil, "", pathErr
	}
	words := append([]string{action}, args...)
	for _, r := range driveRefusals {
		prefix := strings.Fields(r.words)
		blocked := len(words) >= len(prefix) && slices.Equal(words[:len(prefix)], prefix)
		if strings.HasPrefix(r.words, "-") {
			blocked = slices.ContainsFunc(append(slices.Clone(words), options...), func(a string) bool { return a == r.words || strings.HasPrefix(a, r.words+"=") })
		}
		if blocked {
			return nil, "", invalid("TESTING_ACTION_UNSUPPORTED", r.words+" is refused: "+r.reason)
		}
	}
	args = slices.Clone(args)
	if action == "tab" && len(args) > 0 && args[0] != "list" && args[0] != "--json" && args[0] != "--help" && !(args[0] == "close" && len(args) == 1) {
		return nil, "", invalid("TESTING_ACTION_UNSUPPORTED", "tab switching or creation is refused: commands must stay on the leg's app target")
	}
	for _, i := range refArgs(action, args) {
		if i < len(args) {
			if m := refPattern.FindStringSubmatch(args[i]); m != nil {
				args[i] = "@" + m[1]
			}
		}
	}
	// Keep convenient legacy aliases while accepting full subcommands.
	if action == "network" && (len(args) == 0 || strings.HasPrefix(args[0], "-")) {
		args = append([]string{"requests"}, args...)
	}
	if action == "tab" && (len(args) == 0 || strings.HasPrefix(args[0], "-")) {
		args = append([]string{"list"}, args...)
	}
	if action == "navigate" {
		action = "open"
	}
	pathAt := func(i int) error {
		if i >= len(args) {
			return nil
		}
		p, e := inRun(runDir, args[i])
		if e == nil {
			args[i] = p
		}
		return e
	}
	switch action {
	case "screenshot":
		// Screenshot accepts [selector] [path] and flags. Default files also stay in the run.
		var positional []int
		for i := 0; i < len(args); i++ {
			if strings.HasPrefix(args[i], "-") {
				if slices.Contains([]string{"--threshold", "--screenshot-quality", "--screenshot-format", "--screenshot-dir"}, args[i]) {
					i++
				}
				continue
			}
			positional = append(positional, i)
		}
		hasPath := len(positional) > 0
		if len(positional) == 1 {
			a := args[positional[0]]
			if refPattern.MatchString(a) || strings.HasPrefix(a, "#") || strings.HasPrefix(a, "[") || strings.HasPrefix(a, ".") && !strings.Contains(a, "/") && !slices.Contains([]string{".png", ".jpg", ".jpeg", ".webp"}, strings.ToLower(filepath.Ext(a))) {
				hasPath = false
			}
		}
		if hasPath {
			i := positional[len(positional)-1]
			file = args[i]
			if err = pathAt(i); err != nil {
				return nil, "", err
			}
			file = args[i]
		} else {
			file, err = inRun(runDir, filepath.Join("shots", fmt.Sprintf("%s-%d.png", leg, time.Now().UnixMilli())))
			if err != nil {
				return nil, "", err
			}
			args = append(args, file)
		}
	case "diff":
		for i, a := range args {
			if a == "-b" {
				err = pathAt(i + 1)
				if err != nil {
					break
				}
			}
		}
	case "wait":
		if _, err := engineCommandBudget(cmdTimeout, append([]string{"wait"}, args...), runDir); err != nil {
			return nil, "", err
		}
		for i, a := range args {
			if (a == "--download" || a == "-d") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				err = pathAt(i + 1)
				if err != nil {
					break
				}
			}
		}
	case "upload":
		for i := 1; i < len(args); i++ {
			if err = pathAt(i); err != nil {
				return nil, "", err
			}
		}
	case "download":
		err = pathAt(1)
	case "pdf":
		err = pathAt(0)
	case "record":
		if len(args) > 0 && (args[0] == "start" || args[0] == "restart") {
			err = pathAt(1)
		}
	case "trace", "profiler":
		if len(args) > 0 && args[0] == "stop" {
			err = pathAt(1)
		}
	case "network":
		if len(args) > 1 && args[0] == "har" && args[1] == "stop" {
			err = pathAt(2)
		}
	case "state":
		if len(args) > 0 && slices.Contains([]string{"save", "load", "show"}, args[0]) {
			err = pathAt(1)
		}
	case "cookies":
		for i, a := range args {
			if a == "--curl" {
				err = pathAt(i + 1)
				if err != nil {
					break
				}
			}
		}
	case "cdp":
		err = validateCDP(args, runDir)
	case "batch":
		for _, child := range args {
			if child == "--bail" || child == "--json" {
				continue
			}
			w, e := commandWords(child)
			if e != nil {
				return nil, "", e
			}
			_ = w // child guards run through Drive, including failed-child logging
		}
	}
	if err != nil {
		return nil, "", err
	}
	for _, a := range args {
		u, e := url.Parse(a)
		if e == nil && strings.EqualFold(u.Scheme, "file") {
			if _, e = inRun(runDir, u.Path); e != nil {
				return nil, "", e
			}
		}
	}
	if action == "webmcp" {
		for i, a := range args {
			if a == "--params" && i+1 < len(args) && strings.HasPrefix(args[i+1], "@") {
				p, e := inRun(runDir, strings.TrimPrefix(args[i+1], "@"))
				if e != nil {
					return nil, "", e
				}
				args[i+1] = "@" + p
			}
			if strings.HasPrefix(a, "--params=@") {
				p, e := inRun(runDir, strings.TrimPrefix(a, "--params=@"))
				if e != nil {
					return nil, "", e
				}
				args[i] = "--params=@" + p
			}
		}
	}
	// File-valued options are shared by several commands.
	for i, a := range args {
		for _, f := range []string{"--baseline", "--output", "-o", "--curl"} {
			if a == f {
				if err = pathAt(i + 1); err != nil {
					return nil, "", err
				}
			}
			if strings.HasPrefix(a, f+"=") {
				p, e := inRun(runDir, strings.TrimPrefix(a, f+"="))
				if e != nil {
					return nil, "", e
				}
				args[i] = f + "=" + p
			}
		}
	}
	if action != "cdp" && action != "batch" {
		args = append(args, options...)
	}
	return append([]string{action}, args...), file, nil
}

// browserOptions mirrors agent-browser's global-option removal before subcommand parsing.
// Keep safe options at the end so global flags cannot hide subcommands or file arguments.
func browserOptions(args []string, runDir string) (operands, options []string, err error) {
	values := strings.Fields("--session --restore-save --restore-check-url --restore-check-text --restore-check-fn --namespace --headers --executable-path --cdp --extension --init-script --enable --profile --state --proxy --proxy-bypass --args --user-agent -p --provider --device --session-name --color-scheme --download-path --max-output --allowed-domains --action-policy --confirm-actions --config --engine --input-mode --screenshot-dir --screenshot-quality --screenshot-format --idle-timeout --ca-cert --model")
	bools := strings.Fields("--json --headed --webgpu --no-webmcp --debug --ignore-https-errors --allow-file-access --hide-scrollbars --auto-connect --pin-tab --no-pin-tab --no-ca-cert --annotate --content-boundaries --confirm-interactive --no-auto-dialog -v --verbose -q --quiet --offline --quick --fix")
	for i := 0; i < len(args); i++ {
		key, value, inline := strings.Cut(args[i], "=")
		if slices.Contains(values, key) {
			if !inline {
				if i+1 >= len(args) {
					return nil, nil, invalid("TESTING_ARGS_INVALID", "missing value for "+key)
				}
				i++
				value = args[i]
			}
			if slices.Contains([]string{"--download-path", "--screenshot-dir", "--action-policy"}, key) {
				value, err = inRun(runDir, value)
				if err != nil {
					return nil, nil, err
				}
			}
			options = append(options, key, value)
		} else if slices.Contains(bools, key) {
			if !inline && i+1 < len(args) && (args[i+1] == "true" || args[i+1] == "false") {
				i++
				value = args[i]
				inline = true
			}
			if inline {
				options = append(options, key, value)
			} else {
				options = append(options, key)
			}
		} else {
			operands = append(operands, args[i])
		}
	}
	return operands, options, nil
}

// commandWords parses batch's quoted argument strings. No shell is executed.
func commandWords(input string) ([]string, error) {
	var out []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range input {
		if escaped {
			word.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			if started {
				out = append(out, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, invalid("TESTING_ARGS_INVALID", "unterminated batch quote or escape")
	}
	if started {
		out = append(out, word.String())
	}
	if len(out) == 0 {
		return nil, invalid("TESTING_ARGS_INVALID", "empty batch command")
	}
	return out, nil
}

func validateCDP(args []string, runDir string) error {
	if len(args) < 1 || len(args) > 2 || !strings.Contains(args[0], ".") {
		return invalid("TESTING_ARGS_INVALID", "usage: cdp <Method> [json-params]")
	}
	for _, domain := range []string{"Browser.", "Target.", "SystemInfo.", "Tethering."} {
		if strings.HasPrefix(args[0], domain) {
			return invalid("TESTING_ACTION_UNSUPPORTED", "cdp "+domain+" is refused: it controls the browser or targets outside the leg's page")
		}
	}
	var params map[string]any
	if len(args) == 2 {
		if err := json.Unmarshal([]byte(args[1]), &params); err != nil || params == nil {
			return invalid("TESTING_ARGS_INVALID", "CDP params must be a JSON object")
		}
	}
	for _, key := range []string{"targetId", "sessionId", "browserContextId"} {
		if _, ok := params[key]; ok {
			return invalid("TESTING_ACTION_UNSUPPORTED", "CDP "+key+" is refused: the leg's page target is fixed")
		}
	}
	if rawURL, ok := params["url"].(string); ok {
		u, e := url.Parse(rawURL)
		if e == nil && strings.EqualFold(u.Scheme, "file") {
			if _, e = inRun(runDir, u.Path); e != nil {
				return e
			}
		}
	}
	if args[0] == "Page.setDownloadBehavior" {
		if path, ok := params["downloadPath"].(string); ok {
			p, err := inRun(runDir, path)
			if err != nil {
				return err
			}
			params["downloadPath"] = p
		}
	}
	if args[0] == "DOM.setFileInputFiles" {
		files, _ := params["files"].([]any)
		for i, f := range files {
			p, ok := f.(string)
			if !ok {
				return invalid("TESTING_ARGS_INVALID", "CDP files must be paths")
			}
			path, err := inRun(runDir, p)
			if err != nil {
				return err
			}
			files[i] = path
		}
	}
	if len(args) == 2 {
		data, _ := json.Marshal(params)
		args[1] = string(data)
	}
	return nil
}

// rawCDP runs beforeSend immediately before the CDP call. For page
// mouse events it first reads the page viewport through the same connection,
// so the recorded point uses the viewport that belongs to the dispatch.
func (e *Engine) rawCDP(ctx context.Context, args []string, beforeSend func(*clickAt)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	p, err := e.pageCDP(ctx)
	if err != nil {
		return "", err
	}
	if p.conn != nil {
		defer func() { _ = p.conn.CloseNow() }()
	}
	params := json.RawMessage(rawCDPParams(args))
	var point *clickAt
	if len(args) >= 2 && args[0] == "Input.dispatchMouseEvent" {
		var event struct {
			Type string   `json:"type"`
			X    *float64 `json:"x"`
			Y    *float64 `json:"y"`
		}
		if json.Unmarshal(params, &event) == nil && event.Type != "mouseWheel" && event.X != nil && event.Y != nil {
			var size []float64
			if err := p.evaluate(ctx, "[innerWidth, innerHeight]", &size); err != nil {
				slog.Default().Debug("litmus: raw CDP pointer point unavailable", "error", err)
				if errors.Is(err, errTabDrift) || errors.Is(err, errLegDown) {
					return "", err
				}
			} else if len(size) == 2 && size[0] > 0 && size[1] > 0 {
				point = &clickAt{X: *event.X, Y: *event.Y, W: size[0], H: size[1]}
			} else {
				slog.Default().Debug("litmus: raw CDP pointer point unavailable", "error", "viewport size was invalid")
			}
		}
	}
	if beforeSend != nil {
		beforeSend(point)
	}
	var result json.RawMessage
	if err = p.call(ctx, args[0], params, &result); err != nil {
		return "", err
	}
	return string(result), nil
}
