package productauth

import (
	"context"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func TestNativeEnrollmentIsStableAndNeverLinksByEmail(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://wiki.test","version":1,"default_roles":[],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	accountStore := accounts.New(pool)
	canonical, err := accountStore.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://hub.test", Subject: "existing", Email: "same@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool, accountStore, productusers.New(pool, registry, "https://hub.test", nil), "https://hub.test")
	s.ConfigureBrowserClients(map[string]products.Client{"wiki": {
		ClientID: "wiki", ProductID: "agentwiki", BrowserSSO: true, IdentityLinkRole: "member", RedirectURIs: []string{"https://wiki.test/auth/return"},
	}})
	in := NativeEnrollment{ClientID: "wiki", Email: "same@example.test", Authentication: Authentication{
		LocalUserID: "existing-wiki-user", AuthenticationID: "fresh-native-sign-in", Method: "password", AuthenticatedAt: time.Now().Unix(), AuthVersion: 6,
	}}
	first, err := s.EnrollNativeIdentity(ctx, "agentwiki", "default", in)
	if err != nil || first.Binding.UserID == canonical.ID || first.Binding.LocalUserID != in.Authentication.LocalUserID || first.AuthVersion != 6 {
		t.Fatalf("wrong native enrollment: %+v %v", first, err)
	}
	in.Authentication.AuthenticationID = "another-native-sign-in"
	in.Email = "changed@example.test"
	repeat, err := s.EnrollNativeIdentity(ctx, "agentwiki", "default", in)
	if err != nil || repeat.Binding != first.Binding {
		t.Fatalf("enrollment retried under a new identity: %+v %v", repeat, err)
	}
	var centralCount int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&centralCount); err != nil || centralCount != 2 {
		t.Fatalf("expected only two distinct proven accounts: %d %v", centralCount, err)
	}
}
