package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

const (
	// MaxCoderWorkspaceName is Coder's workspace name length limit.
	MaxCoderWorkspaceName = 32
	// coderWorkspaceNameIDLength is the session-id segment of a readable name.
	coderWorkspaceNameIDLength = 8
	// maxCoderWorkspaceNameWord bounds the chat-name word of a worker's name.
	maxCoderWorkspaceNameWord = 10
	// minCoderWorkspaceNameWord is how much of the word survives a long prefix:
	// the prefix is shortened before the word drops below it.
	minCoderWorkspaceNameWord = 3
	// defaultCoderWorkspaceNamePrefix names workspaces of a project with no
	// workspace name prefix.
	defaultCoderWorkspaceNamePrefix = "ao"
)

var coderWorkspaceNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateCoderWorkspaceName accepts a name Coder takes for a workspace: at
// most 32 lowercase letters, digits and single hyphens, with no leading or
// trailing hyphen.
func ValidateCoderWorkspaceName(name string) error {
	if len(name) > MaxCoderWorkspaceName || !coderWorkspaceNamePattern.MatchString(name) {
		return errors.New("Coder workspace name must be 1-32 lowercase letters, digits, or single hyphens, with no leading or trailing hyphen")
	}
	return nil
}

// CoderWorkspaceName is the readable Coder workspace name AO gives a new
// session's workspace: <prefix>-o-<id> for an orchestrator and
// <prefix>-w-<word>-<id> for a worker, where <word> is the first word of the
// session's display name and <id> the first 8 characters of the session id.
// An empty or invalid prefix uses "ao"; a display name with no usable word
// omits the word. When <prefix> and <word> do not both fit Coder's 32
// characters, the word is shortened first, down to 3 characters, then the
// prefix; the role marker and the id are always kept. The name is chosen once,
// when the session is created, and stored on its sandbox profile; it never
// changes afterwards, even if the session is renamed.
func CoderWorkspaceName(prefix, kind, displayName, sessionID string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || ValidateCoderWorkspaceNamePrefix(prefix) != nil {
		prefix = defaultCoderWorkspaceNamePrefix
	}
	role, word := "w", coderWorkspaceNameWord(displayName)
	if kind == "orchestrator" {
		role, word = "o", ""
	}
	id := coderWorkspaceNameID(sessionID)
	// What remains for <prefix> and, for a worker, "-<word>".
	budget := MaxCoderWorkspaceName - len("-"+role+"-") - len(id)
	if word != "" {
		if room := budget - 1 - len(prefix); room < min(len(word), minCoderWorkspaceNameWord) {
			prefix = strings.TrimRight(prefix[:budget-1-min(len(word), minCoderWorkspaceNameWord)], "-")
		}
		word = word[:min(len(word), budget-1-len(prefix))]
	}
	name := prefix + "-" + role
	if word != "" {
		name += "-" + word
	}
	return name + "-" + id
}

// coderWorkspaceNameWord is the first run of ASCII letters and digits in a
// display name, lower-cased and at most maxCoderWorkspaceNameWord characters;
// "" when the name has none.
func coderWorkspaceNameWord(displayName string) string {
	var word strings.Builder
	for _, character := range strings.ToLower(displayName) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			word.WriteRune(character)
			if word.Len() == maxCoderWorkspaceNameWord {
				break
			}
			continue
		}
		if word.Len() > 0 {
			break
		}
	}
	return word.String()
}

// coderWorkspaceNameID is the leading 8 letters and digits of the session id
// (random hex for the v4 UUIDs AO issues), or of its SHA-256 when the id has
// fewer.
func coderWorkspaceNameID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	var compact strings.Builder
	for _, character := range strings.ToLower(sessionID) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			compact.WriteRune(character)
			if compact.Len() == coderWorkspaceNameIDLength {
				return compact.String()
			}
		}
	}
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])[:coderWorkspaceNameIDLength]
}
