package postgres

import (
	"context"
	"testing"
)

func TestSessionPullRequestOpenerNamesTheSessionsPerson(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx,
		`UPDATE ao_users SET display_name = '', email = 'ada@example.com', avatar_url = 'https://ao.example/ada.png' WHERE id = $1`,
		fixture.userID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE ao_sessions SET created_by_user_id = $1 WHERE id = $2`, fixture.userID, fixture.sessionID); err != nil {
		t.Fatal(err)
	}
	opener, err := store.SessionPullRequestOpener(ctx, fixture.orgID, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if opener.Name != "ada" || opener.GitHubLogin != "" || opener.AvatarURL != "https://ao.example/ada.png" {
		t.Fatalf("opener without GitHub = %+v", opener)
	}

	if _, err := admin.Exec(ctx, `UPDATE ao_users SET display_name = 'Ada Lovelace' WHERE id = $1`, fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO ao_github_user_connections (
			user_id, github_user_id, github_login, github_avatar_url, access_token_ciphertext, access_token_nonce
		) VALUES ($1, 4242, 'ada-gh', 'https://avatars.example/ada', '\x01'::bytea, '\x000000000000000000000000'::bytea)`,
		fixture.userID,
	); err != nil {
		t.Fatal(err)
	}
	opener, err = store.SessionPullRequestOpener(ctx, fixture.orgID, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if opener.Name != "Ada Lovelace" || opener.GitHubLogin != "ada-gh" || opener.AvatarURL != "https://avatars.example/ada" {
		t.Fatalf("opener with GitHub = %+v", opener)
	}
}
