package litmus

import "time"

const verdictRenderFile = "verdict-render.json"
const commentMediaFile = "comment-media.json"

// Publication resolves these URLs once. Refresh uses the recorded mapping
// with the same renderer, including when approval preceded final teardown.
type commentMedia struct {
	BugImages  map[string]string `json:"bugImages,omitempty"`
	CheckClips map[string]string `json:"checkClips,omitempty"`
}

// verdictRenderInputs preserves the tested run and render time. Later teardown
// must not turn a launched leg into "never launched" or count cleanup as testing.
type verdictRenderInputs struct {
	Run          Run       `json:"run"`
	Verdict      Verdict   `json:"verdict"`
	Claims       bool      `json:"claims"`
	VideoSummary string    `json:"videoSummary"`
	At           time.Time `json:"at"`
	Limits       []string  `json:"limits,omitempty"`
	Deadline     bool      `json:"deadline,omitempty"`
}

func (in verdictRenderInputs) render(runDir string) string {
	md := renderVerdictDocument(in.Run, in.Verdict, in.Claims, in.VideoSummary, runDir, in.At, in.Limits)
	if in.Deadline {
		md, _ = neutralizeVerdictLinks(runDir, md)
	}
	return md
}
