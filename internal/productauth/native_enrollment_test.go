package productauth

import (
	"context"
	"encoding/json"
	"errors"
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
	automatic := in
	if err := json.Unmarshal([]byte(`{"automatic":true,"email":" SAME@example.test "}`), &automatic); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollNativeIdentity(ctx, "agentwiki", "default", automatic); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("auto enrollment must not create a competing identity: %v", err)
	}
	if _, err := s.Lookup(ctx, "agentwiki", "default", in.Authentication.LocalUserID); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("collision created a mapping: %v", err)
	}
	automatic.Authentication.Method = "session"
	automatic.Email = "native-only@example.test"
	automatic.Authentication.LocalUserID = "native-only-wiki-user"
	native, err := s.EnrollNativeIdentity(ctx, "agentwiki", "default", automatic)
	if err != nil || native.Binding.LocalUserID != automatic.Authentication.LocalUserID || native.Binding.UserID == canonical.ID {
		t.Fatalf("verified native session should provision its own identity: %+v %v", native, err)
	}
	automatic.Email = "SAME@example.test"
	known, err := s.EnrollNativeIdentity(ctx, "agentwiki", "default", automatic)
	if err != nil || known.Binding != native.Binding {
		t.Fatalf("an existing mapping must take precedence over a profile collision: %+v %v", known, err)
	}
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
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&centralCount); err != nil || centralCount != 3 {
		t.Fatalf("expected exactly three distinct proven accounts: %d %v", centralCount, err)
	}
}
