package triage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/githubpat"
)

func TestGitHubCredentialStoreReusesExistingPATAndOAuthStore(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "PAT", true: "OAuth"}[oauth], func(t *testing.T) {
			f := newPublisherFixture(t)
			dataDir := t.TempDir()
			credential := GitHubCredential{AccessToken: "synthetic-token"}
			if oauth {
				credential.RefreshToken, credential.ExpiresIn, credential.RefreshTokenExpiresIn = "synthetic-refresh", 3600, 7200
			}
			before := time.Now()
			identity, err := storeGitHubCredential(context.Background(), dataDir, credential, f.publisher)
			if err != nil || identity.Login != "reporter" || identity.ID != 7 {
				t.Fatalf("identity=%+v err=%v", identity, err)
			}
			token, err := githubpat.New(dataDir).GetToken(context.Background())
			if err != nil || token != credential.AccessToken {
				t.Fatalf("credential unavailable to existing service: %v", err)
			}
			path := filepath.Join(dataDir, "github-pat.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var stored struct {
				RefreshToken string    `json:"refresh_token"`
				ExpiresAt    time.Time `json:"expires_at"`
			}
			if err := json.Unmarshal(data, &stored); err != nil {
				t.Fatal(err)
			}
			if oauth && (stored.RefreshToken != credential.RefreshToken || stored.ExpiresAt.Before(before.Add(time.Hour))) || !oauth && !stored.ExpiresAt.IsZero() {
				t.Fatal("OAuth refresh material or lifetimes were lost")
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("credential file permissions err=%v", err)
			}
			projection, err := json.Marshal(identity)
			if err != nil || strings.Contains(string(projection), "synthetic") {
				t.Fatal("identity response exposed a credential")
			}
			if requests, posts := f.counts(); requests != 1 || posts != 0 {
				t.Fatalf("credential verification requests=%d posts=%d", requests, posts)
			}
		})
	}
}

func TestGitHubCredentialStoreRejectsBadInputsAndKeepsOldCredential(t *testing.T) {
	for _, mode := range []string{"empty", "header injection", "negative expiry", "overflow expiry", "missing refresh", "invalid user", "unauthorized", "foreign redirect"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublisherFixture(t)
			dataDir := t.TempDir()
			if err := githubpat.New(dataDir).StorePAT(context.Background(), "old-synthetic-token"); err != nil {
				t.Fatal(err)
			}
			credential := GitHubCredential{AccessToken: "synthetic-token"}
			switch mode {
			case "empty":
				credential.AccessToken = ""
			case "header injection":
				credential.AccessToken += "\nX-Injected: value"
			case "negative expiry":
				credential.ExpiresIn = -1
			case "overflow expiry":
				credential.ExpiresIn = 1 << 62
			case "missing refresh":
				credential.RefreshTokenExpiresIn = 3600
			}
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/user" {
					return false
				}
				switch mode {
				case "invalid user":
					f.json(w, 200, map[string]any{"id": 0, "login": "invented"})
				case "unauthorized":
					f.json(w, 401, map[string]any{"message": "bad synthetic-token"})
				case "foreign redirect":
					http.Redirect(w, r, "https://example.invalid/user", http.StatusFound)
				default:
					return false
				}
				return true
			}
			identity, err := storeGitHubCredential(context.Background(), dataDir, credential, f.publisher)
			if err == nil || identity.ID != 0 || strings.Contains(err.Error(), "synthetic-token") {
				t.Fatalf("identity=%+v err=%v", identity, err)
			}
			old, err := githubpat.New(dataDir).GetToken(context.Background())
			if err != nil || old != "old-synthetic-token" {
				t.Fatalf("replaced existing credential: %v", err)
			}
		})
	}
}

func TestGitHubCredentialStoreCancellationAndDeadlineDoNotPersist(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublisherFixture(t)
			dataDir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
				defer cancel()
				want = context.DeadlineExceeded
			}
			f.publisher.HTTPClient = &http.Client{Transport: publisherRoundTripper(func(r *http.Request) (*http.Response, error) {
				if mode == "cancel" {
					cancel()
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}
			_, err := storeGitHubCredential(ctx, dataDir, GitHubCredential{AccessToken: "synthetic-token"}, f.publisher)
			if !errors.Is(err, want) {
				t.Fatalf("lost cancellation/deadline: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dataDir, "github-pat.json")); !os.IsNotExist(err) {
				t.Fatalf("saved credential after canceled verification: %v", err)
			}
		})
	}
}
