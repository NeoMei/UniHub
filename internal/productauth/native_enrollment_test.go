package productauth

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func nativeEnrollmentTestService(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://wiki.test","version":1,"default_roles":[],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool, accounts.New(pool), productusers.New(pool, registry, "https://hub.test", nil), "https://hub.test")
	s.ConfigureBrowserClients(map[string]products.Client{"wiki": {
		ClientID: "wiki", ProductID: "agentwiki", BrowserSSO: true, IdentityLinkRole: "member", RedirectURIs: []string{"https://wiki.test/auth/return"},
	}})
	return s
}

func TestNativeEnrollmentIsStableAndNeverLinksByEmail(t *testing.T) {
	ctx := context.Background()
	s := nativeEnrollmentTestService(t)
	canonical, err := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://hub.test", Subject: "existing", Email: "same@example.test"})
	if err != nil {
		t.Fatal(err)
	}
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
	if err = s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&centralCount); err != nil || centralCount != 3 {
		t.Fatalf("expected exactly three distinct proven accounts: %d %v", centralCount, err)
	}
}

func TestAutomaticNativeEnrollmentWaitsForCentralCreation(t *testing.T) {
	s := nativeEnrollmentTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	creator, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(creator)
	central, err := s.accounts.EnsureIdentityOn(ctx, creator, accounts.Identity{
		Issuer: "https://issuer.test", Subject: "central-user", Email: " \tShared@Example.Test \n",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := NativeEnrollment{ClientID: "wiki", Automatic: true, Email: " shared@example.test ", Authentication: Authentication{
		LocalUserID: "concurrent-wiki-user", AuthenticationID: "concurrent-native-auth", Method: "session",
		AuthenticatedAt: time.Now().Unix(), AuthVersion: 1,
	}}
	finished := make(chan struct{})
	var enrollmentErr error
	go func() {
		_, enrollmentErr = s.EnrollNativeIdentity(ctx, "agentwiki", "default", in)
		close(finished)
	}()
	waitForBlockedTransaction(t, ctx, creator, finished)
	if err = creator.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-finished
	if !errors.Is(enrollmentErr, accounts.ErrConflict) {
		t.Fatalf("automatic enrollment returned %v after the matching account committed", enrollmentErr)
	}
	if _, err = s.Lookup(ctx, "agentwiki", "default", in.Authentication.LocalUserID); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("automatic enrollment left a product mapping: %v", err)
	}

	sameEmail := accounts.Identity{Issuer: "https://issuer.test", Subject: "different-central-user", Email: "shared@example.test"}
	distinct, err := s.accounts.EnsureIdentity(ctx, sameEmail)
	if err != nil || distinct.ID == central.ID || distinct.Email != sameEmail.Email {
		t.Fatalf("separately proven central account should retain its own identity and email: %+v, %v", distinct, err)
	}
	in.Automatic = false
	in.Authentication.Method = "password"
	native, err := s.EnrollNativeIdentity(ctx, "agentwiki", "default", in)
	if err != nil || native.Binding.UserID == central.ID || native.Binding.UserID == distinct.ID {
		t.Fatalf("explicit native enrollment should retain a separate identity: %+v, %v", native.Binding, err)
	}
}

func waitForBlockedTransaction(t *testing.T, ctx context.Context, blocker pgx.Tx, finished <-chan struct{}) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := blocker.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_locks WHERE NOT granted AND pg_backend_pid()=ANY(pg_blocking_pids(pid)))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-finished:
			t.Fatal("operation bypassed the blocking transaction")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}
