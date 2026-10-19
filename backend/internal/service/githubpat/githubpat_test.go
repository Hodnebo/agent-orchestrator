package githubpat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func githubClient(status int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(`{"message":"test response"}`)),
			Header:     make(http.Header),
		}, nil
	})}
}

func TestListReposKeepsTokenOnUnauthorized(t *testing.T) {
	service := newWithClient(t.TempDir(), githubClient(http.StatusUnauthorized), "https://github.example")
	if err := service.StorePAT(context.Background(), "rejected-token"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}

	_, err := service.ListRepos(context.Background())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ListRepos error = %v, want ErrInvalidCredentials", err)
	}
	// A 401 must NOT delete the stored token. A transient GitHub 401 would
	// otherwise permanently wipe a still-valid credential and force a full
	// reconnect. The token is preserved until the user reconnects (overwrite)
	// or explicitly disconnects (DeletePAT).
	if !service.HasPAT(context.Background()) {
		t.Fatal("HasPAT = false: token was deleted on a 401 (it must be preserved)")
	}
}

func TestListReposDoesNotRemoveTokenForTransientFailure(t *testing.T) {
	service := newWithClient(t.TempDir(), githubClient(http.StatusInternalServerError), "https://github.example")
	if err := service.StorePAT(context.Background(), "still-valid-token"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}

	if _, err := service.ListRepos(context.Background()); err == nil {
		t.Fatal("ListRepos error = nil, want failure")
	}
	if !service.HasPAT(context.Background()) {
		t.Fatal("HasPAT = false after a transient GitHub failure")
	}
}

// oauthRoutingClient routes the refresh POST and the repos GET, minting
// "new-token" on refresh and only accepting it on /user/repos.
func oauthRoutingClient(refreshCalls *int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/login/oauth/access_token"):
			*refreshCalls++
			return jsonResp(http.StatusOK, `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":28800,"refresh_token_expires_in":15552000}`), nil
		case strings.Contains(r.URL.Path, "/user/repos"):
			if r.Header.Get("Authorization") != "Bearer new-token" {
				return jsonResp(http.StatusUnauthorized, `{"message":"bad credentials"}`), nil
			}
			return jsonResp(http.StatusOK, `[{"name":"r","full_name":"o/r","private":false,"default_branch":"main","clone_url":"https://github.com/o/r.git"}]`), nil
		default:
			return jsonResp(http.StatusNotFound, `{}`), nil
		}
	})}
}

func oauthService(t *testing.T, refreshCalls *int) *Service {
	t.Helper()
	s := newWithClient(t.TempDir(), oauthRoutingClient(refreshCalls), "https://api.github.example")
	s.oauthURL = "https://github.example"
	s.clientID = "client-id"
	s.clientSecret = "client-secret"
	return s
}

func TestListReposRefreshesExpiringToken(t *testing.T) {
	var refreshCalls int
	s := oauthService(t, &refreshCalls)
	// An OAuth token already past its expiry: ListRepos must renew it proactively
	// before calling GitHub, not force the user to reconnect.
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(30 * 24 * time.Hour)
	if err := s.StoreOAuthToken(context.Background(), "old-token", "old-refresh", past, future); err != nil {
		t.Fatalf("StoreOAuthToken: %v", err)
	}
	repos, err := s.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].FullName != "o/r" {
		t.Fatalf("repos = %+v, want one o/r", repos)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1 (proactive refresh)", refreshCalls)
	}
	if tok, _ := s.GetToken(context.Background()); tok != "new-token" {
		t.Fatalf("stored token = %q, want new-token", tok)
	}
}

func TestListReposForceRefreshesOnUnexpected401(t *testing.T) {
	var refreshCalls int
	s := oauthService(t, &refreshCalls)
	// Token is not yet expired by its recorded deadline, so no proactive refresh;
	// GitHub rejects it anyway (early expiry / rotation). One forced refresh +
	// retry must recover without a reconnect prompt.
	accessExpiry := time.Now().Add(time.Hour)
	refreshExpiry := time.Now().Add(180 * 24 * time.Hour)
	if err := s.StoreOAuthToken(context.Background(), "old-token", "old-refresh", accessExpiry, refreshExpiry); err != nil {
		t.Fatalf("StoreOAuthToken: %v", err)
	}
	repos, err := s.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("repos = %+v, want one", repos)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1 (forced refresh on 401)", refreshCalls)
	}
}

func TestListReposNoRefreshMaterialStaysInvalidOn401(t *testing.T) {
	var refreshCalls int
	s := oauthService(t, &refreshCalls)
	// A plain PAT (no refresh token) that GitHub rejects must surface
	// ErrInvalidCredentials without attempting a refresh.
	if err := s.StorePAT(context.Background(), "plain-rejected"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}
	_, err := s.ListRepos(context.Background())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ListRepos error = %v, want ErrInvalidCredentials", err)
	}
	if refreshCalls != 0 {
		t.Fatalf("refreshCalls = %d, want 0 (no refresh token to use)", refreshCalls)
	}
}

func TestServicesShareRefreshLockAndRereadRotatedToken(t *testing.T) {
	root := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return jsonResp(200, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":28800}`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}
	first := newWithClient(root, client, "https://api.github.example")
	second := newWithClient(root, client, "https://api.github.example")
	for _, s := range []*Service{first, second} {
		s.clientID, s.clientSecret = "synthetic-client", "synthetic-secret"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := first.StoreOAuthToken(ctx, "expired-access", "old-refresh", time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		token string
		err   error
	}
	results := make(chan result, 2)
	go func() { token, err := first.GetToken(ctx); results <- result{token, err} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { token, err := second.GetToken(ctx); results <- result{token, err} }()
	close(release)
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil || got.token != "rotated-access" {
				t.Fatalf("GetToken=%q, %v", got.token, got.err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d, want one shared refresh", calls.Load())
	}
}

func TestCredentialLockHonorsCancellationAcrossServiceInstances(t *testing.T) {
	root := t.TempDir()
	first := newWithClient(root, nil, "https://api.github.example")
	second := newWithClient(root, nil, "https://api.github.example")
	unlock, err := first.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, operation := range []string{"read", "login", "logout"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			var err error
			switch operation {
			case "read":
				_, err = second.GetToken(ctx)
			case "login":
				err = second.StorePAT(ctx, "synthetic-login")
			case "logout":
				err = second.DeletePAT(ctx)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error=%v, want deadline", err)
			}
		})
	}
}

func TestLateUnauthorizedResponseDoesNotOverwriteNewLogin(t *testing.T) {
	root := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	var refreshes atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "access_token") {
			refreshes.Add(1)
			return jsonResp(200, `{"access_token":"stale-refresh","expires_in":28800}`), nil
		}
		if r.Header.Get("Authorization") == "Bearer original-access" {
			close(entered)
			select {
			case <-release:
				return jsonResp(401, `{}`), nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		if r.Header.Get("Authorization") != "Bearer newer-login" {
			t.Error("request did not use the new login")
		}
		return jsonResp(200, `[]`), nil
	})}
	s := newWithClient(root, client, "https://api.github.example")
	s.clientID, s.clientSecret = "synthetic-client", "synthetic-secret"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.StoreOAuthToken(ctx, "original-access", "original-refresh", time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := s.ListRepos(ctx); result <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	other := newWithClient(root, client, "https://api.github.example")
	if err := other.StorePAT(ctx, "newer-login"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if refreshes.Load() != 0 {
		t.Fatal("late 401 refreshed an obsolete credential")
	}
	if token, err := other.GetToken(ctx); err != nil || token != "newer-login" {
		t.Fatalf("stored=%q, %v", token, err)
	}
}

func TestRefreshCannotResurrectLoggedOutCredential(t *testing.T) {
	root := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		select {
		case <-release:
			return jsonResp(200, `{"access_token":"refreshed-access","expires_in":28800}`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}
	s := newWithClient(root, client, "https://api.github.example")
	s.clientID, s.clientSecret = "synthetic-client", "synthetic-secret"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.StoreOAuthToken(ctx, "old-access", "old-refresh", time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() { _, err := s.GetToken(ctx); refreshed <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	other := newWithClient(root, client, "https://api.github.example")
	deleted := make(chan error, 1)
	go func() { deleted <- other.DeletePAT(ctx) }()
	close(release)
	for _, result := range []<-chan error{refreshed, deleted} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if other.HasPAT(ctx) {
		t.Fatal("refresh resurrected a logged-out credential")
	}
}

func TestCredentialReplacementIsAtomicAndPrivate(t *testing.T) {
	s := newWithClient(t.TempDir(), nil, "https://api.github.example")
	ctx := context.Background()
	if err := s.StorePAT(ctx, "synthetic-initial"); err != nil {
		t.Fatal(err)
	}
	// A reader outside the service still sees complete old or new JSON.
	done, readErrors := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				readErrors <- nil
				return
			default:
			}
			data, err := os.ReadFile(s.patPath())
			if err != nil {
				readErrors <- err
				return
			}
			var stored storedPAT
			if err := json.Unmarshal(data, &stored); err != nil {
				readErrors <- err
				return
			}
			if !strings.HasPrefix(stored.Token, "synthetic-") {
				readErrors <- errors.New("partial credential read")
				return
			}
		}
	}()
	for i := range 60 {
		if err := s.StorePAT(ctx, fmt.Sprintf("synthetic-%d-%s", i, strings.Repeat("x", 4096))); err != nil {
			close(done)
			t.Fatal(err)
		}
	}
	close(done)
	if err := <-readErrors; err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.patPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".github-pat-") {
			t.Fatal("temporary credential file was left behind")
		}
	}
}

func TestForcedRefreshNetworkFailureDoesNotRequireLogin(t *testing.T) {
	s := newWithClient(t.TempDir(), &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "access_token") {
			return nil, errors.New("synthetic network unavailable")
		}
		return jsonResp(401, `{}`), nil
	})}, "https://api.github.example")
	s.clientID, s.clientSecret = "synthetic-client", "synthetic-secret"
	ctx := context.Background()
	if err := s.StoreOAuthToken(ctx, "old-access", "old-refresh", time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err := s.ListRepos(ctx)
	if err == nil || errors.Is(err, ErrInvalidCredentials) || !strings.Contains(err.Error(), "synthetic network unavailable") {
		t.Fatalf("error=%v", err)
	}
	if !s.HasPAT(ctx) {
		t.Fatal("temporary network error removed credentials")
	}
}
