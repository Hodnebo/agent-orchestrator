package domain

import (
	"strings"
	"testing"
)

func TestCoderWorkspaceName(t *testing.T) {
	t.Parallel()
	const sessionID = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	longest := strings.Repeat("p", MaxCoderWorkspaceNamePrefix)
	for _, tc := range []struct {
		name, prefix, kind, displayName, sessionID, want string
	}{
		{"orchestrator", "acme", "orchestrator", "Orchestrator", sessionID, "acme-o-1a2b3c4d"},
		{"orchestrator ignores its display name", "acme", "orchestrator", "Fix login", sessionID, "acme-o-1a2b3c4d"},
		{"worker takes the first word", "acme", "worker", "Fix the login bug", sessionID, "acme-w-fix-1a2b3c4d"},
		{"word is lower-cased alphanumerics", "acme", "worker", "  OAuth2-callback: retry", sessionID, "acme-w-oauth2-1a2b3c4d"},
		{"leading punctuation is skipped", "acme", "worker", "#42: handle nil", sessionID, "acme-w-42-1a2b3c4d"},
		{"word is capped", "acme", "worker", "Internationalization pass", sessionID, "acme-w-internatio-1a2b3c4d"},
		{"no usable word is omitted", "acme", "worker", "修正 ✓", sessionID, "acme-w-1a2b3c4d"},
		{"empty display name", "acme", "worker", "", sessionID, "acme-w-1a2b3c4d"},
		{"empty prefix uses ao", "", "worker", "Fix", sessionID, "ao-w-fix-1a2b3c4d"},
		{"invalid prefix uses ao", "Bad--Prefix", "orchestrator", "", sessionID, "ao-o-1a2b3c4d"},
		{"long prefix keeps a short word", longest, "worker", "Refactor", sessionID, longest[:17] + "-w-ref-1a2b3c4d"},
		{"long prefix with a short word", longest, "worker", "ab", sessionID, longest[:18] + "-w-ab-1a2b3c4d"},
		{"long prefix orchestrator", longest, "orchestrator", "", sessionID, longest + "-o-1a2b3c4d"},
		{"long prefix without a word", longest, "worker", "", sessionID, longest + "-w-1a2b3c4d"},
		{"truncated prefix drops a trailing hyphen", "abcdefghijklmnop-rst", "worker", "Fix", sessionID, "abcdefghijklmnop-w-fix-1a2b3c4d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := CoderWorkspaceName(tc.prefix, tc.kind, tc.displayName, tc.sessionID)
			if got != tc.want {
				t.Fatalf("CoderWorkspaceName = %q, want %q", got, tc.want)
			}
			if err := ValidateCoderWorkspaceName(got); err != nil {
				t.Fatalf("CoderWorkspaceName = %q is not a valid Coder name: %v", got, err)
			}
		})
	}
}

// Every combination stays a valid Coder name that keeps its role marker and id.
func TestCoderWorkspaceNameAlwaysFitsCoderRules(t *testing.T) {
	t.Parallel()
	prefixes := []string{"", "a", "ao", "dev-kit", "a1-b2-c3", strings.Repeat("x", 19) + "y", "abcdefghijklmnopqr-s"}
	names := []string{"", "x", "Fix", "Refactor everything", strings.Repeat("Z", 40), "-- ++", "日本語 fix"}
	for _, prefix := range prefixes {
		for _, kind := range []string{"orchestrator", "worker"} {
			for _, displayName := range names {
				for _, sessionID := range []string{"ffffffff-0000-4000-8000-000000000000", "s-1"} {
					got := CoderWorkspaceName(prefix, kind, displayName, sessionID)
					if err := ValidateCoderWorkspaceName(got); err != nil {
						t.Fatalf("CoderWorkspaceName(%q, %q, %q, %q) = %q: %v", prefix, kind, displayName, sessionID, got, err)
					}
					role := "-w-"
					if kind == "orchestrator" {
						role = "-o-"
					}
					id := coderWorkspaceNameID(sessionID)
					if !strings.Contains(got, role) || !strings.HasSuffix(got, "-"+id) || len(id) != 8 {
						t.Fatalf("CoderWorkspaceName(%q, %q, %q, %q) = %q lost its role or id", prefix, kind, displayName, sessionID, got)
					}
				}
			}
		}
	}
}

func TestValidateCoderWorkspaceName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"ao-o-1a2b3c4d", "a", strings.Repeat("a", 32)} {
		if err := ValidateCoderWorkspaceName(name); err != nil {
			t.Errorf("ValidateCoderWorkspaceName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"", "-ao", "ao-", "ao--o", "Ao-o", "ao_o", strings.Repeat("a", 33)} {
		if err := ValidateCoderWorkspaceName(name); err == nil {
			t.Errorf("ValidateCoderWorkspaceName(%q) = nil, want an error", name)
		}
	}
}
