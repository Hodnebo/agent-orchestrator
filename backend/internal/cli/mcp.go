package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/triage"
)

// `ao mcp` is the stdio MCP server AO passes to every chat session, so the
// agent sees HTML tools and its session-bound report_problem tool. The agent's
// harness spawns it outside the shell sandbox, so it reaches the loopback
// daemon even when the agent's own shell commands cannot. Hand-written, and
// only what these tools need: newline-delimited JSON-RPC 2.0, one request
// at a time, nothing on stdout but replies.

const (
	mcpDefaultProtocolVersion = "2025-06-18"
	// maxMCPHTMLChars is T3's cap on a tool's html argument.
	maxMCPHTMLChars   = 512_000
	maxMCPReportBytes = 1 << 20
)

var mcpProtocolVersions = []string{mcpDefaultProtocolVersion, "2025-03-26", "2024-11-05"}

func newMCPCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:    "mcp",
		Short:  "Serve AO's chat tools over stdio MCP (internal)",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.serveMCP(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

type mcpRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

func (c *commandContext) serveMCP(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	encoder := json.NewEncoder(out)
	for {
		line, readErr := reader.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			if resp := c.handleMCP(ctx, line); resp != nil {
				if err := encoder.Encode(resp); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// handleMCP answers one message; a notification gets no reply.
func (c *commandContext) handleMCP(ctx context.Context, line []byte) *mcpResponse {
	var req mcpRequest
	if err := json.Unmarshal(line, &req); err != nil {
		if !json.Valid(line) {
			return &mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: -32700, Message: "parse error: " + err.Error()}}
		}
		// Valid JSON of the wrong shape. Unmarshal still fills a readable id.
		id := req.ID
		if id == nil {
			id = json.RawMessage("null")
		}
		return &mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: -32600, Message: "invalid request: " + err.Error()}}
	}
	if req.ID == nil {
		return nil
	}
	resp := &mcpResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpDefaultProtocolVersion
		if slices.Contains(mcpProtocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ao", "version": Version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		switch p.Name {
		case "html_preview":
			resp.Result = c.callHTMLPreview(ctx, p.Arguments)
		case "html_render":
			resp.Result = c.callHTMLRender(ctx, p.Arguments)
		case "report_problem":
			resp.Result = c.callReportProblem(ctx, p.Arguments)
		default:
			resp.Error = &mcpError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", p.Name)}
		}
	default:
		resp.Error = &mcpError{Code: -32601, Message: fmt.Sprintf("method %q not found", req.Method)}
	}
	return resp
}

type mcpToolArgs struct {
	HTML     string `json:"html"`
	Title    string `json:"title"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Artifact bool   `json:"artifact"`
}

// readMCPToolArgs decodes a call's arguments and returns the session it acts in.
func readMCPToolArgs(raw json.RawMessage) (mcpToolArgs, string, error) {
	var args mcpToolArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return args, "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if utf8.RuneCountInString(args.HTML) > maxMCPHTMLChars || len(args.HTML) > maxRenderFileBytes {
		return args, "", fmt.Errorf("html is too long; the limit is %d characters and %d bytes", maxMCPHTMLChars, maxRenderFileBytes)
	}
	sessionID, err := renderSessionID()
	return args, sessionID, err
}

// Every failure is a tool result the agent can read and act on, never a
// protocol error.
func mcpToolError(err error) map[string]any {
	return map[string]any{"content": []any{mcpText(err.Error())}, "isError": true}
}

func mcpText(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func (c *commandContext) callHTMLPreview(ctx context.Context, raw json.RawMessage) map[string]any {
	args, sessionID, err := readMCPToolArgs(raw)
	if err != nil {
		return mcpToolError(err)
	}
	if args.Width == 0 {
		args.Width = defaultRenderWidth
	}
	resp, missing, err := c.postRenderCheck(ctx, sessionID, args.HTML, args.Width)
	if err != nil {
		return mcpToolError(err)
	}
	note := ""
	if resp.Network == "none" {
		note = renderCheckOfflineNote
	}
	report, err := json.Marshal(struct {
		Width           int      `json:"width"`
		ContentHeight   int      `json:"contentHeight"`
		ConsoleMessages any      `json:"consoleMessages"`
		MissingImages   []string `json:"missingImages,omitempty"`
		Note            string   `json:"note,omitempty"`
	}{args.Width, resp.ContentHeight, resp.ConsoleMessages, missing, note})
	if err != nil {
		return mcpToolError(err)
	}
	return map[string]any{"content": []any{
		map[string]any{"type": "image", "mimeType": "image/png", "data": resp.Screenshot.Data},
		mcpText(string(report)),
	}}
}

func (c *commandContext) callHTMLRender(ctx context.Context, raw json.RawMessage) map[string]any {
	args, sessionID, err := readMCPToolArgs(raw)
	if err != nil {
		return mcpToolError(err)
	}
	if args.Height == 0 {
		args.Height = defaultRenderHeight
	}
	resp, err := c.postRender(ctx, sessionID, args.HTML, args.Title, args.Height, args.Artifact)
	if err != nil {
		return mcpToolError(err)
	}
	text := renderShownText(resp.RenderID)
	if line := renderArtifactLine(resp); line != "" {
		text += "\n" + line
	}
	return map[string]any{"content": []any{mcpText(text)}}
}

func (*commandContext) callReportProblem(ctx context.Context, raw json.RawMessage) map[string]any {
	if len(raw) == 0 || len(raw) > maxMCPReportBytes {
		return mcpToolError(errors.New("report_problem arguments require one JSON object of at most 1 MiB"))
	}
	var args struct {
		RunID   string              `json:"runId"`
		Request *triage.ToolCommand `json:"request"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&args) != nil || decoder.Decode(&extra) != io.EOF || args.Request == nil || args.Request.Operation == "" {
		return mcpToolError(errors.New("report_problem requires only runId and an explicit request.operation; unknown arguments are not allowed"))
	}
	if args.RunID == "" || len(args.RunID) > 128 {
		return mcpToolError(errors.New("report_problem requires a valid report runId of at most 128 characters"))
	}
	for _, r := range args.RunID {
		if r != '-' && r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return mcpToolError(errors.New("report_problem runId may contain only letters, digits, hyphens and underscores"))
		}
	}
	sessionID, err := renderSessionID()
	if err != nil {
		return mcpToolError(errors.New("report_problem must run inside its linked AO session; AO_SESSION_ID is missing"))
	}
	paths, _ := config.ResolvePaths()
	if paths.DataDir == "" {
		return mcpToolError(errors.New("report_problem could not resolve AO's existing data directory"))
	}
	request, err := json.Marshal(args.Request)
	if err != nil || len(request) > maxMCPReportBytes {
		return mcpToolError(errors.New("report_problem request exceeds the 1 MiB support input limit"))
	}
	opts := triage.DefaultOptions(paths.DataDir)
	opts.CallerSessionID = sessionID
	var output bytes.Buffer // ServeTool caps its single JSON response at 1 MiB.
	if err := triage.ServeTool(ctx, bytes.NewReader(request), &output, args.RunID, opts); err != nil {
		return mcpToolError(errors.New("report_problem could not complete the explicit support operation"))
	}
	var response triage.ToolResponse
	if json.Unmarshal(output.Bytes(), &response) != nil || response.Status == "" {
		return mcpToolError(errors.New("report_problem received an invalid support response"))
	}
	return map[string]any{"content": []any{mcpText(output.String())}, "isError": response.Status != "ok"}
}

// The tool text follows T3's html tools, with AO's own rules from
// skillassets/using-ao/commands/render.md.
const (
	mcpPageRules = `Write one self-contained HTML document with inline <style> and <script>. ` +
		`Remote https:// resources, such as a CDN chart library, load as they are; relative URLs do not resolve. ` +
		`A local image written as an absolute path (src="/abs/shot.png", CSS url(/abs/bg.webp), or a JS string) ` +
		`is put into the page: PNG, JPEG, GIF, WebP, AVIF, SVG, BMP, or ICO, up to 10 MiB each.`
	mcpLayoutGuide = `The frame is borderless on the thread background, as wide as the reply column, and its left edge lines up with your text. ` +
		`Use a fluid width with no outer padding, card, border, or banner title: the page is part of your reply. ` +
		`An expanded page opens in a dialog up to 1024 px wide that fits its height, and AO centers a top-level block that has a maximum width. ` +
		`Give charts fixed pixel heights. Do not size html or body with 100vh or height: 100%; the frame grows to fit the page, and viewport heights make it grow again. ` +
		`Scripts run in a sandbox with no access to AO, cookies, or storage. Links open in the user's browser.`
	mcpThemeGuide = `AO injects its active theme as CSS custom properties on :root, and they follow light/dark mode live: ` +
		`--background (identical to the thread), --foreground, --muted, --muted-foreground, --card, --card-foreground, --popover, ` +
		`--border, --border-strong, --primary, --primary-foreground, --accent, --accent-foreground, --success, --warning, ` +
		`--destructive, --code, --link, --chart-1 ... --chart-6 (categorical series), --radius, --font-sans, --font-mono. ` +
		`The base stylesheet sets the page background, text color, and font from these, sets body margin to 0, and hides the page scrollbar. ` +
		`Use the variables, not hard-coded colors, so the page reads correctly in both themes.`
)

var mcpHTMLSchema = map[string]any{
	"type": "string", "minLength": 1, "maxLength": maxMCPHTMLChars,
	"description": "A complete, self-contained HTML document.",
}

var mcpTools = []map[string]any{
	{
		"name":  "html_preview",
		"title": "Preview HTML",
		"description": "Load an HTML page in the AO desktop app the way readers see it. Returns a PNG screenshot, " +
			"contentHeight (the height the page needs at this width), and the page's console messages. " +
			"Call it before html_render. Read the screenshot, and fix every console error. " +
			"The check needs the AO desktop app; without it, publish without a check. " +
			mcpPageRules + " The page gets the theme variables and layout described in html_render.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"html": mcpHTMLSchema,
				"width": map[string]any{
					"type": "integer", "minimum": 240, "maximum": 1600,
					"description": "Viewport width in CSS pixels, 240-1600. Defaults to 720, the reply column; use 390 to check phones.",
				},
			},
			"required": []string{"html"},
		},
		"annotations": map[string]any{
			"title": "Preview HTML", "readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true,
		},
	},
	{
		"name":  "html_render",
		"title": "Render HTML",
		"description": "Show a finished HTML page (chart, table, diagram, image collage, mockup) inline in this chat thread, " +
			"above your final reply. Preview it with html_preview first, then call this before you write the reply. " +
			"The reader already sees the page, so the reply must not announce it, say where it is, or restate it: " +
			"add only what the page does not say. Use this tool, not a built-in visualize skill: AO does not show those. " +
			mcpPageRules + " " + mcpLayoutGuide + " " + mcpThemeGuide,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"html": mcpHTMLSchema,
				"title": map[string]any{
					"type": "string", "minLength": 1, "maxLength": 200, "description": "Short name for the page.",
				},
				"height": map[string]any{
					"type": "integer", "minimum": 80, "maximum": 2000,
					"description": "Frame height in CSS pixels, 80-2000; defaults to 400. AO measures the page when " +
						"you publish it, so this is used only when the desktop app is not running. The frame then fits the page.",
				},
				"artifact": map[string]any{
					"type": "boolean",
					"description": "Also keep the page as a session artifact, a deliverable the user keeps. " +
						"Set it only when the user asks to keep the page.",
				},
			},
			"required": []string{"html", "title"},
		},
		"annotations": map[string]any{
			// Not read-only: with artifact it writes a file to the artifact directory.
			"title": "Render HTML", "readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true,
		},
	},
	{
		"name":  "report_problem",
		"title": "Investigate an AO problem",
		"description": "Read scoped private diagnostics, search existing AO issues and pull requests, and save or publish the authorized report. " +
			"Use only the Report a Problem run linked to this AO session. Publication requires its original authorization and a successful related-report search. " +
			"Returns scrubbed JSON with an explicit status; private context is investigation evidence, not public report text.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"runId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9_-]+$"},
				"request": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{
						"operation": map[string]any{"type": "string", "enum": []string{"get", "context-read", "search", "bind-session", "save", "publish"}},
						"request": map[string]any{
							"type": "object", "additionalProperties": false,
							"properties": map[string]any{
								"source": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
								"path":   map[string]any{"type": "string", "maxLength": 1024},
								"offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 16 << 20},
								"list":   map[string]any{"type": "boolean"},
							},
							"required": []string{"source"},
						},
						"query":        map[string]any{"type": "string", "maxLength": 8192},
						"sessionId":    map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
						"userSelected": map[string]any{"type": "boolean"},
						"draft": map[string]any{
							"type": "object", "additionalProperties": false,
							"properties": map[string]any{
								"title":                map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
								"body":                 map[string]any{"type": "string", "minLength": 1, "maxLength": 60000},
								"evidence":             map[string]any{"type": "string", "maxLength": 60000},
								"existingReportUrl":    map[string]any{"type": "string", "maxLength": 2048},
								"existingReportReason": map[string]any{"type": "string", "maxLength": 2000},
							},
							"required": []string{"title", "body"},
						},
					},
					"required": []string{"operation"},
				},
			},
			"required": []string{"runId", "request"},
		},
		"annotations": map[string]any{
			"title": "Investigate an AO problem", "readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true,
		},
	},
}
