package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

var claimLink = regexp.MustCompile(`\[([^\]\n]*)\]\([^\)\n]*\)`)

func normalizeClaim(s string) string {
	s = claimLink.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("*", "", "_", "", "`", "", "‘", "'", "’", "'", "“", `"`, "”", `"`,
		"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "―", "-").Replace(s)
	return strings.TrimRight(strings.Join(strings.Fields(strings.ToLower(s)), " "), " .,:;!?")
}

// claimInPR matches only the saved title and body, including earlier versions.
func claimInPR(sentence string, words []string) bool {
	sentence = normalizeClaim(sentence)
	if sentence == "" {
		return false
	}
	for _, source := range words {
		for _, line := range strings.Split(source, "\n") {
			if sentence == normalizeClaim(line) {
				return true
			}
		}
		if len(strings.Fields(sentence)) >= 3 && strings.Contains(normalizeClaim(source), sentence) {
			return true
		}
	}
	return false
}

func claimWords(runDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "intake-claims.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return readIntakeClaims(runDir), nil // source text from older runs' context packs
	}
	if err != nil {
		return nil, err
	}
	var words []string
	err = json.Unmarshal(data, &words)
	return words, err
}

// checkClaim runs under mu, before any ledger change.
func (s *Service) checkClaim(sessionID string, choice CheckClassification) error {
	if choice.Purpose != "claim" {
		return nil
	}
	runID, err := runID(sessionID)
	if err != nil {
		return err
	}
	words, err := claimWords(filepath.Join(s.store.root, runID))
	if err != nil {
		return err
	}
	if len(words) == 0 || claimInPR(choice.ClaimSentence, words) {
		return nil
	}
	return apierr.Invalid("TESTING_CHECK_CLAIM_SENTENCE", "quote the PR author's title or body. PR author's lines:\n"+strings.Join(words, "\n"), map[string]any{"intakeClaims": words})
}

// withClaimWords refreshes a missed claim once, then repeats the guarded change
// against the current ledger. Network I/O never holds mu.
func (s *Service) withClaimWords(ctx context.Context, sessionID string, choice CheckClassification, change func() (Check, error)) (Check, error) {
	c, err := change()
	var mismatch *apierr.Error
	if choice.Purpose != "claim" || choice.ClaimSentence == "" || !errors.As(err, &mismatch) || mismatch.Code != "TESTING_CHECK_CLAIM_SENTENCE" {
		return c, err
	}
	s.mu.Lock()
	run, loadErr := s.load(sessionID)
	if loadErr != nil {
		s.mu.Unlock()
		return Check{}, loadErr
	}
	now := s.clock()
	if last, ok := s.claimRefresh[run.ID]; ok && now.Sub(last) < time.Minute {
		s.mu.Unlock()
		return change()
	}
	if s.claimRefresh == nil {
		s.claimRefresh = map[string]time.Time{}
	}
	s.claimRefresh[run.ID] = now
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d, fetchErr := s.pullWords(ctx, run.Repo, run.PRNumber)
	if fetchErr != nil {
		return Check{}, apierr.Invalid(mismatch.Code, mismatch.Message+"\nPR text refresh failed: "+fetchErr.Error(), mismatch.Details)
	}
	err = func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, err := s.load(sessionID); err != nil {
			return err
		}
		words, err := claimWords(filepath.Join(s.store.root, run.ID))
		if err != nil {
			return err
		}
		before := slices.Clone(words)
		for _, source := range []string{d.pr.Title, d.pr.Body} {
			if strings.TrimSpace(source) != "" && !slices.Contains(words, source) {
				words = append(words, source)
			}
		}
		if slices.Equal(words, before) {
			return nil
		}
		return s.store.put(run.ID, "intake-claims.json", words)
	}()
	if err != nil {
		return Check{}, err
	}
	return change()
}
