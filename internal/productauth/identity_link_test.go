package productauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func TestIdentityLinkPreservesCanonicalUserAndExplicitAdmission(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	manifests, err := productusers.Parse([]byte(`[{"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://wiki.test","version":1,"default_roles":[],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	store := accounts.New(pool)
	users := productusers.New(pool, manifests, "https://hub.test", nil)
	s := New(pool, store, users, "https://hub.test")
	client := products.Client{ClientID: "wiki", ProductID: "agentwiki", InstanceID: "default", BrowserSSO: true, IdentityLinkRole: "member", RedirectURIs: []string{"https://wiki.test/auth/return"}}
	s.ConfigureBrowserClients(map[string]products.Client{"wiki": client})
	claims := accounts.Identity{Issuer: "https://issuer.test", Subject: "existing", Email: "same@example.test"}
	canonical, err := store.EnsureIdentity(ctx, claims)
	if err != nil {
		t.Fatal(err)
	}
	session := browserSessionDigest(browserSecret())
	if _, err = pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,$2,$3,'Fixture','same@example.test','csrf',now()+interval '10 minutes')`, session, claims.Issuer, claims.Subject); err != nil {
		t.Fatal(err)
	}
	before, err := users.Resolve(ctx, canonical, "agentwiki", "default")
	if err != nil || len(before.Permissions) != 0 {
		t.Fatalf("unbound account has product access: %+v, %v", before, err)
	}
	verifier := browserSecret()
	sum := sha256.Sum256([]byte(verifier))
	begin := BeginIdentityLink{
		ClientID: "wiki", State: browserSecret(), Nonce: browserSecret(),
		Challenge:      base64.RawURLEncoding.EncodeToString(sum[:]),
		Authentication: Authentication{LocalUserID: "original-wiki-user", AuthenticationID: "fresh-native-auth", Method: "password", AuthenticatedAt: time.Now().Unix(), AuthVersion: 4},
	}
	hint, err := s.BeginIdentityLink(ctx, "agentwiki", "default", begin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginIdentityLink(ctx, "agentwiki", "default", begin); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("native authentication reused: %v", err)
	}
	if _, err = s.AuthorizeIdentityLink(ctx, "wiki", client.RedirectURIs[0], browserSecret(), hint.LinkID, session); err == nil {
		t.Fatal("invalid state accepted")
	}
	destination, err := s.AuthorizeIdentityLink(ctx, "wiki", client.RedirectURIs[0], begin.State, hint.LinkID, session)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(destination)
	if err != nil || callback.Query().Get("state") != begin.State {
		t.Fatalf("bad callback: %s, %v", destination, err)
	}
	complete := CompleteIdentityLink{
		ClientID: "wiki", LinkID: hint.LinkID, Code: callback.Query().Get("code"), RedirectURI: client.RedirectURIs[0], Nonce: begin.Nonce, Verifier: verifier,
		Authentication: Authentication{LocalUserID: "original-wiki-user", AuthenticationID: "verified-local-session", Method: "session", AuthenticatedAt: time.Now().Unix(), AuthVersion: 4},
	}
	preview, err := s.PreviewIdentityLink(ctx, "agentwiki", "default", complete)
	if err != nil || preview.CentralUserID != canonical.ID || preview.MaskedEmail != "s***@example.test" {
		t.Fatalf("wrong account preview: %+v, %v", preview, err)
	}
	if _, err = s.IdentityLinkByCanonicalUser(ctx, "agentwiki", "default", canonical.ID); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("preview created a binding: %v", err)
	}
	if _, err = s.CompleteIdentityLink(ctx, "agentwiki", "default", complete); err == nil {
		t.Fatal("binding without account confirmation was accepted")
	}
	complete.CentralUserID = canonical.ID
	wrong := complete
	wrong.Authentication.LocalUserID = "another-wiki-user"
	if _, err = s.CompleteIdentityLink(ctx, "agentwiki", "default", wrong); err == nil {
		t.Fatal("different native subject accepted")
	}
	if _, err = s.CompleteIdentityLink(ctx, "agentwiki", "other", complete); err == nil {
		t.Fatal("cross-instance link accepted")
	}
	wrong = complete
	wrong.CentralUserID = "different-confirmed-account"
	if _, err = s.CompleteIdentityLink(ctx, "agentwiki", "default", wrong); err == nil {
		t.Fatal("confirmation from another account accepted")
	}
	got, err := s.CompleteIdentityLink(ctx, "agentwiki", "default", complete)
	if err != nil || got.Binding.LocalUserID != "original-wiki-user" || got.Binding.UserID != canonical.ID || got.AuthVersion != 4 || strings.Join(got.Access.Permissions, ",") != "product:use" {
		t.Fatalf("wrong binding or access: %+v, %v", got, err)
	}
	replayed, err := s.CompleteIdentityLink(ctx, "agentwiki", "default", complete)
	if err != nil || replayed.Binding.ID != got.Binding.ID {
		t.Fatalf("exact retry changed the binding: %+v, %v", replayed, err)
	}
	out, err := s.IdentityLinkByCanonicalUser(ctx, "agentwiki", "default", canonical.ID)
	if err != nil || out.Binding.ID != got.Binding.ID {
		t.Fatalf("reverse lookup failed: %+v, %v", out, err)
	}

	t.Run("source logout while completion waits for the original identity", func(t *testing.T) {
		claims.Subject = "revoked-central-account"
		other, err := store.EnsureIdentity(ctx, claims)
		if err != nil {
			t.Fatal(err)
		}
		session := browserSessionDigest(browserSecret())
		if _, err = pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at)
			VALUES($1,$2,$3,'Other fixture','','csrf',now()+interval '10 minutes')`, session, claims.Issuer, claims.Subject); err != nil {
			t.Fatal(err)
		}
		begin.Authentication.LocalUserID = "concurrent-old-user"
		begin.Authentication.AuthenticationID = "concurrent-native-auth"
		hint, err := s.BeginIdentityLink(ctx, "agentwiki", "default", begin)
		if err != nil {
			t.Fatal(err)
		}
		destination, err := s.AuthorizeIdentityLink(ctx, "wiki", client.RedirectURIs[0], begin.State, hint.LinkID, session)
		if err != nil {
			t.Fatal(err)
		}
		callback, err := url.Parse(destination)
		if err != nil {
			t.Fatal(err)
		}
		complete.CentralUserID = other.ID
		complete.Authentication.LocalUserID = begin.Authentication.LocalUserID
		complete.LinkID, complete.Code = hint.LinkID, callback.Query().Get("code")

		lock, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Rollback(lock)
		if err = accounts.LockIdentity(ctx, lock, "https://hub.test/product-auth/agentwiki/default", complete.Authentication.LocalUserID); err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			_, e := s.CompleteIdentityLink(bounded, "agentwiki", "default", complete)
			finished <- e
		}()
		for {
			var waiting bool
			if err = pool.QueryRow(bounded, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database() AND wait_event='advisory' AND query LIKE 'SELECT pg_advisory_xact_lock%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, err = pool.Exec(ctx, `DELETE FROM browser_sessions WHERE id_hash=$1`, session); err != nil {
			t.Fatal(err)
		}
		if err = lock.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err = <-finished; !errors.Is(err, accounts.ErrForbidden) {
			t.Fatalf("revoked link was committed: %v", err)
		}
		if _, err = s.IdentityLinkByCanonicalUser(ctx, "agentwiki", "default", other.ID); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("revoked link left a binding: %v", err)
		}
	})
}

func TestIdentityLinkRechecksEffectiveSourcePermission(t *testing.T) {
	s, source, client := newBrowserSessionTestService(t)
	ctx := context.Background()
	session := browserSessionDigest(browserSecret())
	if _, err := s.pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at)
		VALUES($1,$2,$3,'Fixture','fixture@example.test','csrf',now()+interval '10 minutes')`,
		session, source.Binding.Issuer, source.Binding.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO product_browser_session_origins(session_id_hash,binding_id,auth_version)
		VALUES($1,$2,$3)`, session, source.Binding.ID, source.AuthVersion); err != nil {
		t.Fatal(err)
	}

	verifier := browserSecret()
	sum := sha256.Sum256([]byte(verifier))
	native := Authentication{LocalUserID: "destination-with-revoked-source", AuthenticationID: "fresh-destination-native",
		Method: "password", AuthenticatedAt: time.Now().Unix(), AuthVersion: 1}
	begin := BeginIdentityLink{ClientID: client.ClientID, State: browserSecret(), Nonce: browserSecret(),
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Authentication: native}
	link, err := s.BeginIdentityLink(ctx, client.ProductID, client.InstanceID, begin)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := s.AuthorizeIdentityLink(ctx, client.ClientID, client.RedirectURIs[0], begin.State, link.LinkID, session)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(destination)
	if err != nil {
		t.Fatal(err)
	}
	complete := CompleteIdentityLink{ClientID: client.ClientID, LinkID: link.LinkID, Code: callback.Query().Get("code"),
		RedirectURI: client.RedirectURIs[0], Nonce: begin.Nonce, Verifier: verifier, CentralUserID: source.Binding.UserID, Authentication: native}
	if _, err = s.PreviewIdentityLink(ctx, client.ProductID, client.InstanceID, complete); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE product_user_access SET permission_overrides='{"product":{"use":false}}',version=version+1
		WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, source.Binding.UserID, source.Binding.ProductID, source.Binding.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewIdentityLink(ctx, client.ProductID, client.InstanceID, complete); !errors.Is(err, accounts.ErrForbidden) {
		t.Errorf("preview accepted a product session without product:use: %v", err)
	}
	if _, err = s.CompleteIdentityLink(ctx, client.ProductID, client.InstanceID, complete); !errors.Is(err, accounts.ErrForbidden) {
		t.Errorf("completion accepted a product session without product:use: %v", err)
	}
	if _, err = s.IdentityLinkByCanonicalUser(ctx, client.ProductID, client.InstanceID, source.Binding.UserID); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("denied link left a destination binding: %v", err)
	}
}
