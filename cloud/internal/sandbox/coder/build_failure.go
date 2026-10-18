package coder

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/cloud/internal/activity"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

// Coder workspace build statuses for a build that ended without converging.
const (
	buildStatusFailed   = "failed"
	buildStatusCanceled = "canceled"
)

// buildFailureMessageLimit bounds the build error shown to users. The full
// build log tail stays in the startup error's operator detail.
const buildFailureMessageLimit = 400

func buildStatus(build workspaceBuild) string {
	return strings.ToLower(strings.TrimSpace(build.Status))
}

// buildFailure explains a failed or canceled workspace build. Only a build
// that was bringing the workspace up is a startup failure: a failed stop or
// delete leaves the reconciler to issue the transition it wants again.
func buildFailure(build workspaceBuild) *sandbox.Failure {
	switch strings.ToLower(strings.TrimSpace(build.Transition)) {
	case "", "start":
	default:
		return nil
	}
	message := buildFailedMessage(build.Job.Error, nil)
	if buildStatus(build) == buildStatusCanceled {
		message = "The workspace build was canceled in Coder before the workspace started."
	}
	return &sandbox.Failure{
		ID:  build.ID,
		Err: &sandbox.StartupError{Code: sandbox.StartupErrorWorkspaceBuildFailed, Message: message},
	}
}

// buildErrorLines returns the error-level lines of a build's log. It is best
// effort: a log that cannot be read leaves the job error to explain the build.
func (c *Client) buildErrorLines(ctx context.Context, buildID string) []string {
	if buildID == "" {
		return nil
	}
	var lines []buildLogLine
	if err := c.do(ctx, http.MethodGet,
		"/api/v2/workspacebuilds/"+url.PathEscape(buildID)+"/logs", nil, &lines); err != nil {
		return nil
	}
	var found []string
	for _, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line.Level), "error") {
			found = append(found, line.Output)
		}
	}
	return found
}

// buildFailedMessage renders a failed build's user-facing reason from the
// build log's error lines, falling back to the provisioner job's error.
func buildFailedMessage(jobError string, errorLines []string) string {
	reason := buildErrorReason(errorLines)
	if reason == "" {
		reason = cleanBuildError(jobError)
	}
	if reason == "" {
		return "The workspace failed to build in Coder."
	}
	return "The workspace failed to build: " + reason
}

// buildErrorReason picks the line that names the cause. Terraform prints a
// diagnostic as an "Error: <summary>" line followed by where it happened, so
// the summary is preferred over the context lines after it.
func buildErrorReason(lines []string) string {
	first := ""
	for _, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if summary, ok := strings.CutPrefix(text, "Error:"); ok {
			if reason := cleanBuildError(summary); reason != "" {
				return reason
			}
		}
		if first == "" {
			first = text
		}
	}
	return cleanBuildError(first)
}

var (
	// apiErrorPattern finds the cloud API's own error code and message inside
	// an SDK error ("... RequestID: <id>, api error VcpuLimitExceeded: ...").
	apiErrorPattern = regexp.MustCompile(`\bapi error ([A-Za-z0-9_.]+: .*)$`)
	// requestIDPattern drops cloud request identifiers, which mean nothing to
	// a user and make every occurrence of one failure look different.
	requestIDPattern = regexp.MustCompile(`(?i),?\s*request ?id:\s*[A-Za-z0-9-]+`)
	spacePattern     = regexp.MustCompile(`\s+`)
)

// cleanBuildError reduces one build error to a short, redacted, single line.
func cleanBuildError(text string) string {
	text = strings.TrimSpace(activity.Excerpt(text, 0))
	if match := apiErrorPattern.FindStringSubmatch(text); match != nil {
		text = match[1]
	}
	text = requestIDPattern.ReplaceAllString(text, "")
	text = strings.TrimSpace(spacePattern.ReplaceAllString(text, " "))
	return truncateWords(text, buildFailureMessageLimit)
}

// truncateWords cuts text to at most limit bytes on a word boundary.
func truncateWords(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	const marker = "..."
	cut := text[:limit-len(marker)]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if space := strings.LastIndexByte(cut, ' '); space > limit/2 {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, " ,;:") + marker
}
