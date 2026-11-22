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
	wrong := complete
	wrong.Authentication.LocalUserID = "another-wiki-user"
	if _, err = s.CompleteIdentityLink(ctx, "agentwiki", "default", wrong); err == nil {
		t.Fatal("different native subject accepted")
	}
	if _, err = s.CompleteIdentityLink(ctx, "agentwiki", "other", complete); err == nil {
		t.Fatal("cross-instance link accepted")
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
}
