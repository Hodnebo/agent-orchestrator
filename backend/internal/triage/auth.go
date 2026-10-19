package triage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/githubpat"
)

// GitHubCredential is accepted only over the desktop's private support-host
// pipe after its existing GitHub browser sign-in. It must never enter a run,
// provider prompt, response, or log.
type GitHubCredential struct {
	AccessToken           string `json:"accessToken"`
	RefreshToken          string `json:"refreshToken,omitempty"`
	ExpiresIn             int64  `json:"expiresIn,omitempty"`
	RefreshTokenExpiresIn int64  `json:"refreshTokenExpiresIn,omitempty"`
}

// GitHubIdentity is the only credential-store result returned to the desktop.
type GitHubIdentity struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// StoreGitHubCredential verifies a browser sign-in and saves it through AO's
// existing credential store without requiring a running daemon.
func StoreGitHubCredential(ctx context.Context, dataDir string, credential GitHubCredential) (GitHubIdentity, error) {
	return storeGitHubCredential(ctx, dataDir, credential, NewPublisher(dataDir))
}

func storeGitHubCredential(ctx context.Context, dataDir string, credential GitHubCredential, publisher *Publisher) (identity GitHubIdentity, err error) {
	defer func() {
		if err != nil {
			err = safeGitHubError(err, credential.AccessToken)
			if credential.RefreshToken != "" {
				err = safeGitHubError(err, credential.RefreshToken)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return identity, err
	}
	if strings.TrimSpace(credential.AccessToken) == "" || len(credential.AccessToken) > 16384 || strings.ContainsAny(credential.AccessToken, "\r\n\t ") || len(credential.RefreshToken) > 16384 || strings.ContainsAny(credential.RefreshToken, "\r\n\t ") {
		return identity, errors.New("GitHub sign-in returned an invalid credential")
	}
	// Bound untrusted lifetimes before converting seconds into a duration.
	const maxLifetimeSeconds = int64(10 * 365 * 24 * 60 * 60)
	if credential.ExpiresIn < 0 || credential.RefreshTokenExpiresIn < 0 || credential.ExpiresIn > maxLifetimeSeconds || credential.RefreshTokenExpiresIn > maxLifetimeSeconds || credential.RefreshTokenExpiresIn > 0 && credential.RefreshToken == "" {
		return identity, errors.New("GitHub sign-in returned invalid token lifetimes")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, base, err := publisher.client()
	if err != nil {
		return identity, err
	}
	api := publicationAPI{client: client, base: base, token: credential.AccessToken}
	var user githubUser
	if err := api.get(ctx, "/user", &user); err != nil {
		return identity, err
	}
	if user.identity() == "" {
		return identity, errors.New("GitHub sign-in returned an invalid account")
	}
	if err := ctx.Err(); err != nil {
		return identity, err
	}
	store := githubpat.New(dataDir)
	if credential.RefreshToken == "" && credential.ExpiresIn == 0 {
		err = store.StorePAT(ctx, credential.AccessToken)
	} else {
		now := time.Now().UTC()
		var expiresAt, refreshExpiresAt time.Time
		if credential.ExpiresIn > 0 {
			expiresAt = now.Add(time.Duration(credential.ExpiresIn) * time.Second)
		}
		if credential.RefreshTokenExpiresIn > 0 {
			refreshExpiresAt = now.Add(time.Duration(credential.RefreshTokenExpiresIn) * time.Second)
		}
		err = store.StoreOAuthToken(ctx, credential.AccessToken, credential.RefreshToken, expiresAt, refreshExpiresAt)
	}
	if err != nil {
		// A filesystem error may contain a private data-directory path.
		return identity, redactedGitHubError{message: "could not save GitHub sign-in in AO's credential store", cause: err}
	}
	return GitHubIdentity(user), nil
}
