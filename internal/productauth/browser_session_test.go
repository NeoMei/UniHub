package productauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math"
	"net/url"
	"strings"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

const sessionTestRegistry = `[
	{"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://wiki.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"admin","name":"Admin","product_role":10,"privileged":true,"permissions":["product:use","user:write"]}]},
	{"product_id":"agentwiki","instance_id":"edge","name":"AgentWiki Edge","entry_url":"https://edge.wiki.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]},
	{"product_id":"native","name":"Native","entry_url":"https://native.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}
]`

const sessionTestClient = "wiki-browser"
const sessionTestDestination = "wiki-user"

func newBrowserSessionTestService(t *testing.T) (*Service, Identity, products.Client) {
	t.Helper()
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(sessionTestRegistry))
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool, accounts.New(pool), productusers.New(pool, registry, "https://hub.test", nil), "https://hub.test")
	source, err := s.Register(ctx, "native", "default", Registration{
		LocalUserID: "native-user", OperationID: "register-native-user", Username: "native-user", AuthVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := products.Client{
		ClientID: sessionTestClient, ProductID: "agentwiki", InstanceID: "default",
		BrowserSSO: true, IdentityLinkRole: "member", RedirectURIs: []string{"https://wiki.test/callback"},
	}
	s.ConfigureBrowserClients(map[string]products.Client{sessionTestClient: client})
	return s, source, client
}

func TestSourceLifecycleVersionReplayAndTerminalState(t *testing.T) {
	ctx := context.Background()
	s, source, _ := newBrowserSessionTestService(t)
	apply := func(in SourceLifecycle, want error) {
		t.Helper()
		out, err := s.SyncSourceLifecycle(ctx, "native", "default", in)
		if !errors.Is(err, want) || (want == nil && out.AuthVersion != in.NextAuthVersion) {
			t.Fatalf("transition %+v: got (%+v, %v), want error %v", in, out, err, want)
		}
	}
	assertState := func(version int64, locked, deleted, assignmentActive bool) {
		t.Helper()
		var gotVersion, assignmentVersion int64
		var gotLocked, gotDeleted, gotActive bool
		err := s.pool.QueryRow(ctx, `SELECT p.auth_version,p.native_locked,p.deleted,a.active,a.version
			FROM product_auth_subjects p JOIN product_runtime_bindings b ON b.id=p.binding_id
			JOIN product_user_access a USING(user_id,product_id,instance_id)
			WHERE p.binding_id=$1`, source.Binding.ID).Scan(&gotVersion, &gotLocked, &gotDeleted, &gotActive, &assignmentVersion)
		if err != nil {
			t.Fatal(err)
		}
		if gotVersion != version || gotLocked != locked || gotDeleted != deleted || gotActive != assignmentActive || assignmentVersion != 1 {
			t.Fatalf("got version=%d locked=%t deleted=%t assignment=(%t,%d), want version=%d locked=%t deleted=%t assignment=(%t,1)",
				gotVersion, gotLocked, gotDeleted, gotActive, assignmentVersion, version, locked, deleted, assignmentActive)
		}
	}
	transition := func(op string, previous int64, state SourceState) SourceLifecycle {
		return SourceLifecycle{Lifecycle: Lifecycle{LocalUserID: source.Binding.LocalUserID, OperationID: op,
			PreviousAuthVersion: previous, NextAuthVersion: previous + 1}, State: state}
	}

	legacy := transition("legacy-version", 1, "")
	if _, err := s.SyncLifecycle(ctx, "native", "default", legacy.Lifecycle); err != nil {
		t.Fatal(err)
	}
	apply(legacy, nil)
	legacy.State = SourceStateLocked
	apply(legacy, accounts.ErrConflict)

	lock := transition("native-lock-1", 2, SourceStateLocked)
	apply(lock, nil)
	assertState(3, true, false, true)
	apply(lock, nil)
	apply(transition("stale-unlock", 2, SourceStateActive), accounts.ErrConflict)
	apply(transition("native-lock-1", 2, SourceStateActive), accounts.ErrConflict)
	apply(transition("locked-version-only", 3, ""), nil)
	assertState(4, true, false, true)

	if _, err := s.pool.Exec(ctx, `UPDATE product_user_access SET active=false WHERE user_id=$1 AND product_id='native'`, source.Binding.UserID); err != nil {
		t.Fatal(err)
	}
	apply(transition("native-unlock", 4, SourceStateActive), nil)
	apply(lock, nil)
	assertState(5, false, false, false)

	deletion := transition("native-deletion", 5, SourceStateDeleted)
	apply(deletion, nil)
	apply(deletion, nil)
	apply(transition("reopen-deleted", 6, SourceStateActive), accounts.ErrForbidden)
	apply(transition("relock-deleted", 6, SourceStateLocked), accounts.ErrForbidden)
	apply(transition("delete-again", 6, SourceStateDeleted), accounts.ErrForbidden)
	apply(transition("deleted-version-only", 6, ""), nil)
	assertState(7, false, true, false)

	malformed := transition("invalid-state", 7, "unknown")
	apply(malformed, accounts.ErrInvalid)
	malformed = transition("overflow-version", math.MaxInt64, SourceStateActive)
	apply(malformed, accounts.ErrInvalid)
	malformed = transition("skip-version", 7, SourceStateActive)
	malformed.NextAuthVersion = 9
	apply(malformed, accounts.ErrInvalid)
}

func TestSourceLifecycleConcurrentOperationsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	s, source, _ := newBrowserSessionTestService(t)
	inputs := []SourceLifecycle{
		{Lifecycle: Lifecycle{LocalUserID: source.Binding.LocalUserID, OperationID: "concurrent-lock", PreviousAuthVersion: 1, NextAuthVersion: 2}, State: SourceStateLocked},
		{Lifecycle: Lifecycle{LocalUserID: source.Binding.LocalUserID, OperationID: "concurrent-delete", PreviousAuthVersion: 1, NextAuthVersion: 2}, State: SourceStateDeleted},
	}
	start := make(chan struct{})
	results := make([]chan error, len(inputs))
	for i, in := range inputs {
		results[i] = make(chan error, 1)
		go func() {
			<-start
			_, err := s.SyncSourceLifecycle(ctx, "native", "default", in)
			results[i] <- err
		}()
	}
	close(start)
	winners := 0
	for i, result := range results {
		err := <-result
		if err == nil {
			winners++
			out, err := s.SyncSourceLifecycle(ctx, "native", "default", inputs[i])
			if err != nil || out.AuthVersion != 2 {
				t.Fatalf("winning operation could not replay: %+v, %v", out, err)
			}
		} else if !errors.Is(err, accounts.ErrConflict) {
			t.Errorf("competing operation: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent operations applied %d times, want 1", winners)
	}
}

func authorizeBrowserSessionTest(t *testing.T, s *Service, source Identity, client products.Client, native bool) (BrowserExchange, string) {
	t.Helper()
	ctx := context.Background()
	sidHash := browserSessionDigest(browserSecret())
	b := source.Binding
	if _, err := s.pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at)
		VALUES($1,$2,$3,'Test User','','csrf',now()+interval '30 minutes')`, sidHash, b.Issuer, b.Subject); err != nil {
		t.Fatal(err)
	}
	if native {
		if _, err := s.pool.Exec(ctx, `INSERT INTO product_browser_session_origins(session_id_hash,binding_id,auth_version)
			VALUES($1,$2,$3)`, sidHash, b.ID, source.AuthVersion); err != nil {
			t.Fatal(err)
		}
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state, nonce := browserSecret(), browserSecret()
	redirect := client.RedirectURIs[0]
	location, err := s.AuthorizeBrowser(ctx, auth.Claims{Issuer: b.Issuer, Subject: b.Subject}, client.ClientID, redirect, state, nonce, challenge, sidHash)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	return BrowserExchange{
		ClientID: client.ClientID, Code: u.Query().Get("code"), Verifier: verifier, Nonce: nonce, RedirectURI: redirect,
	}, sidHash
}

func issueIdentityBrowserSessionTestProof(t *testing.T, s *Service, source Identity, client products.Client, native bool) (ProductIdentitySession, string) {
	t.Helper()
	in, sidHash := authorizeBrowserSessionTest(t, s, source, client, native)
	out, err := s.ExchangeIdentityBrowser(context.Background(), client.ProductID, client.InstanceID, in)
	if err != nil || out.SourceProofToken == "" || out.Identity.Binding.UserID != source.Binding.UserID {
		t.Fatalf("identity exchange did not issue a bound proof: %v", err)
	}
	return out, sidHash
}

func linkBrowserSessionTestDestination(t *testing.T, s *Service, source Identity, client products.Client) string {
	t.Helper()
	ctx := context.Background()
	admin, err := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://hub.test", Subject: "fixture-admin"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(sessionTestRegistry))
	if err != nil {
		t.Fatal(err)
	}
	s.users = productusers.New(s.pool, registry, "https://hub.test", []string{admin.ID})

	b := source.Binding
	destination, err := s.users.EnsureRuntimeBinding(ctx, client.ProductID, client.InstanceID, sessionTestDestination, "test-link",
		accounts.Identity{Issuer: b.Issuer, Subject: b.Subject}, accounts.User{ID: b.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO product_auth_subjects(binding_id,auth_version,registration_key,registration_digest)
		VALUES($1,1,'test-link','test-link')`, destination.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.users.SetAccess(ctx, admin.ID, b.UserID, client.ProductID, client.InstanceID, []string{"member"}, true, 0); err != nil {
		t.Fatal(err)
	}
	return admin.ID
}

func TestIdentityBrowserProofRequiresCurrentDestinationEpoch(t *testing.T) {
	s, source, client := newBrowserSessionTestService(t)
	admin := linkBrowserSessionTestDestination(t, s, source, client)
	ctx := context.Background()
	user := source.Binding.UserID
	if _, err := s.users.SetAccess(ctx, admin, admin, client.ProductID, client.InstanceID, []string{"admin"}, true, 0); err != nil {
		t.Fatal(err)
	}
	issue := func() ProductIdentitySession {
		t.Helper()
		out, _ := issueIdentityBrowserSessionTestProof(t, s, source, client, false)
		return out
	}
	verify := func(out ProductIdentitySession, want error) {
		t.Helper()
		err := s.VerifyProductBrowserSource(ctx, out.SourceProofToken, client.ProductID, client.InstanceID, client.ClientID, user)
		if !errors.Is(err, want) {
			t.Fatalf("identity proof verification got %v, want %v", err, want)
		}
	}
	setAccess := func(instance string, roles []string, active bool, expected int64) {
		t.Helper()
		if _, err := s.users.SetAccess(ctx, admin, user, client.ProductID, instance, roles, active, expected); err != nil {
			t.Fatal(err)
		}
	}
	setStatus := func(active bool, expected int64) {
		t.Helper()
		if _, err := s.users.SetStatus(ctx, admin, user, active, expected); err != nil {
			t.Fatal(err)
		}
	}
	setProductUse := func(op string, allow bool, expected int64) {
		t.Helper()
		out, err := s.Mutate(ctx, client.ProductID, client.InstanceID, admin, sessionTestDestination, Mutation{
			OperationID: op, ExpectedVersion: expected, Event: "authorization",
			PermissionOverrides: map[string]map[string]bool{"product": {"use": allow}},
		})
		if err != nil || out.AuthVersion != 1 {
			t.Fatalf("product-use change should preserve native auth version: got version %d, %v", out.AuthVersion, err)
		}
	}
	setNativeState := func(op string, previous int64, state SourceState) {
		t.Helper()
		_, err := s.SyncSourceLifecycle(ctx, client.ProductID, client.InstanceID, SourceLifecycle{
			Lifecycle: Lifecycle{LocalUserID: sessionTestDestination, OperationID: op,
				PreviousAuthVersion: previous, NextAuthVersion: previous + 1}, State: state,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	old := issue()
	verify(old, nil)
	setAccess("edge", []string{"member"}, false, 0)
	verify(old, nil)
	setAccess("default", []string{}, true, 1)
	setAccess("default", []string{"member"}, true, 2)
	verify(old, accounts.ErrForbidden)

	old = issue()
	verify(old, nil)
	setAccess("default", []string{"member"}, false, 3)
	setAccess("default", []string{"member"}, true, 4)
	verify(old, accounts.ErrForbidden)

	old = issue()
	verify(old, nil)
	setProductUse("deny-product-use", false, old.Identity.Access.Version)
	setProductUse("restore-product-use", true, old.Identity.Access.Version+1)
	verify(old, accounts.ErrForbidden)

	old = issue()
	verify(old, nil)
	setStatus(false, 1)
	setStatus(true, 2)
	verify(old, accounts.ErrForbidden)

	old = issue()
	verify(old, nil)
	setNativeState("destination-lock", 1, SourceStateLocked)
	setNativeState("destination-unlock", 2, SourceStateActive)
	verify(old, accounts.ErrForbidden)
	fresh := issue()
	verify(fresh, nil)
	if fresh.Identity.AuthVersion != 3 {
		t.Fatalf("fresh proof carries native auth version %d, want 3", fresh.Identity.AuthVersion)
	}
}

func TestVerifyProductBrowserSourceChecksCurrentClientAndScope(t *testing.T) {
	s, source, client := newBrowserSessionTestService(t)
	linkBrowserSessionTestDestination(t, s, source, client)
	session, _ := issueIdentityBrowserSessionTestProof(t, s, source, client, false)
	ctx := context.Background()
	userID := source.Binding.UserID
	in, _ := authorizeBrowserSessionTest(t, s, source, client, false)
	generic, err := s.ExchangeBrowser(ctx, client.ProductID, client.InstanceID, in)
	if err != nil || generic.SourceProofToken == "" {
		t.Fatalf("generic browser exchange failed to issue proof: %v", err)
	}
	verify := func(proof, product, instance, expectedClient, user string, want error) {
		t.Helper()
		if err := s.VerifyProductBrowserSource(ctx, proof, product, instance, expectedClient, user); !errors.Is(err, want) {
			t.Fatalf("verification for %s/%s with client %q: got %v, want %v", product, instance, expectedClient, err, want)
		}
	}
	verify(session.SourceProofToken, "agentwiki", "default", sessionTestClient, userID, nil)
	verify(session.SourceProofToken, "agentwiki", "", sessionTestClient, userID, nil)
	verify(session.SourceProofToken, "agentwiki", "default", sessionTestClient, "another-user", accounts.ErrForbidden)
	verify(session.SourceProofToken, "agentwiki", "edge", sessionTestClient, userID, accounts.ErrForbidden)
	verify(session.SourceProofToken, "native", "default", sessionTestClient, userID, accounts.ErrForbidden)
	verify(generic.ProofToken, "agentwiki", "default", sessionTestClient, userID, accounts.ErrForbidden)
	verify(generic.SourceProofToken, "agentwiki", "default", sessionTestClient, userID, accounts.ErrForbidden)
	verify("", "agentwiki", "default", sessionTestClient, userID, accounts.ErrForbidden)
	verify(session.SourceProofToken, "agentwiki", "default", "", userID, accounts.ErrForbidden)
	if err := s.VerifyBrowserSourceProof(ctx, generic.SourceProofToken, client.ProductID, client.InstanceID, client.ClientID, userID); err != nil {
		t.Fatalf("generic browser proof failed its original verifier: %v", err)
	}
	if err := s.VerifyBrowserSourceProof(ctx, session.SourceProofToken, client.ProductID, client.InstanceID, client.ClientID, userID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("identity proof at the generic browser verifier got %v, want forbidden", err)
	}

	other := client
	other.ClientID = "other-wiki-browser"
	s.ConfigureBrowserClients(map[string]products.Client{sessionTestClient: client, other.ClientID: other})
	verify(session.SourceProofToken, "agentwiki", "default", other.ClientID, userID, accounts.ErrForbidden)
	s.ConfigureBrowserClients(nil)
	verify(session.SourceProofToken, "agentwiki", "default", sessionTestClient, userID, accounts.ErrForbidden)
	client.BrowserSSO = false
	s.ConfigureBrowserClients(map[string]products.Client{sessionTestClient: client})
	verify(session.SourceProofToken, "agentwiki", "default", sessionTestClient, userID, accounts.ErrForbidden)
	client.BrowserSSO, client.InstanceID = true, "edge"
	s.ConfigureBrowserClients(map[string]products.Client{sessionTestClient: client})
	verify(session.SourceProofToken, "agentwiki", "default", sessionTestClient, userID, accounts.ErrForbidden)
	verify(session.SourceProofToken, "agentwiki", "edge", sessionTestClient, userID, accounts.ErrForbidden)
}

func TestVerifyProductBrowserSourceRequiresCurrentSID(t *testing.T) {
	s, source, client := newBrowserSessionTestService(t)
	linkBrowserSessionTestDestination(t, s, source, client)
	ctx := context.Background()
	verify := func(t *testing.T, grant ProductIdentitySession, want error) {
		t.Helper()
		if err := s.VerifyProductBrowserSource(ctx, grant.SourceProofToken, "agentwiki", "default", sessionTestClient, source.Binding.UserID); !errors.Is(err, want) {
			t.Fatalf("source verification got %v, want %v", err, want)
		}
	}
	apply := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		revoke func(t *testing.T, sidHash string)
	}{
		{"logout only revokes its SID", func(t *testing.T, sidHash string) {
			other, _ := issueIdentityBrowserSessionTestProof(t, s, source, client, false)
			apply(t, `DELETE FROM browser_sessions WHERE id_hash=$1`, sidHash)
			verify(t, other, nil)
		}},
		{"expired SID", func(t *testing.T, sidHash string) {
			apply(t, `UPDATE browser_sessions SET expires_at=now() WHERE id_hash=$1`, sidHash)
		}},
		{"expired proof", func(t *testing.T, sidHash string) {
			apply(t, `UPDATE product_browser_source_proofs SET expires_at=now() WHERE source_session_hash=$1`, sidHash)
		}},
		{"changed SID origin", func(t *testing.T, sidHash string) {
			apply(t, `INSERT INTO product_browser_session_origins(session_id_hash,binding_id,auth_version) VALUES($1,$2,1)`, sidHash, source.Binding.ID)
		}},
		{"inactive destination", func(t *testing.T, _ string) {
			t.Cleanup(func() {
				apply(t, `UPDATE product_user_access SET active=true WHERE user_id=$1 AND product_id='agentwiki'`, source.Binding.UserID)
			})
			apply(t, `UPDATE product_user_access SET active=false WHERE user_id=$1 AND product_id='agentwiki'`, source.Binding.UserID)
		}},
		{"destination lost product use", func(t *testing.T, _ string) {
			t.Cleanup(func() {
				apply(t, `UPDATE product_user_access SET permission_overrides='{}' WHERE user_id=$1 AND product_id='agentwiki'`, source.Binding.UserID)
			})
			apply(t, `UPDATE product_user_access SET permission_overrides='{"product":{"use":false}}' WHERE user_id=$1 AND product_id='agentwiki'`, source.Binding.UserID)
		}},
		{"destination has only default access", func(t *testing.T, _ string) {
			t.Cleanup(func() {
				apply(t, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version)
					VALUES($1,'agentwiki','default','["member"]',true,1)`, source.Binding.UserID)
			})
			apply(t, `DELETE FROM product_user_access WHERE user_id=$1 AND product_id='agentwiki'`, source.Binding.UserID)
			in, _ := authorizeBrowserSessionTest(t, s, source, client, false)
			out, err := s.ExchangeIdentityBrowser(ctx, client.ProductID, client.InstanceID, in)
			if !errors.Is(err, accounts.ErrForbidden) || out.SourceProofToken != "" {
				t.Fatalf("exchange without explicit destination assignment got %v, want forbidden", err)
			}
		}},
		{"destination native subject locked", func(t *testing.T, _ string) {
			query := `UPDATE product_auth_subjects p SET native_locked=$2 FROM product_runtime_bindings b
				WHERE b.id=p.binding_id AND b.user_id=$1 AND b.product_id='agentwiki'`
			t.Cleanup(func() { apply(t, query, source.Binding.UserID, false) })
			apply(t, query, source.Binding.UserID, true)
		}},
		{"central logout cutoff", func(t *testing.T, _ string) {
			t.Cleanup(func() { apply(t, `UPDATE users SET session_not_before='epoch' WHERE id=$1`, source.Binding.UserID) })
			apply(t, `UPDATE users SET session_not_before=now()+interval '1 second' WHERE id=$1`, source.Binding.UserID)
		}},
		{"central user inactive", func(t *testing.T, _ string) {
			t.Cleanup(func() { apply(t, `UPDATE users SET active=true WHERE id=$1`, source.Binding.UserID) })
			apply(t, `UPDATE users SET active=false WHERE id=$1`, source.Binding.UserID)
		}},
		{"pending identity revocation", func(t *testing.T, _ string) {
			t.Cleanup(func() { apply(t, `DELETE FROM identity_revocations WHERE id='pending-test'`) })
			apply(t, `INSERT INTO identity_revocations(id,issuer,subject,revoked_after,pending)
				VALUES('pending-test',$1,$2,'epoch',true)`, source.Binding.Issuer, source.Binding.Subject)
		}},
		{"completed identity revocation cutoff", func(t *testing.T, _ string) {
			t.Cleanup(func() { apply(t, `DELETE FROM identity_revocations WHERE id='completed-test'`) })
			apply(t, `INSERT INTO identity_revocations(id,issuer,subject,revoked_after,pending)
				VALUES('completed-test',$1,$2,now()+interval '1 second',false)`, source.Binding.Issuer, source.Binding.Subject)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grant, sidHash := issueIdentityBrowserSessionTestProof(t, s, source, client, false)
			verify(t, grant, nil)
			tc.revoke(t, sidHash)
			verify(t, grant, accounts.ErrForbidden)
		})
	}
}

func TestVerifyProductBrowserSourceRequiresCurrentNativeOrigin(t *testing.T) {
	s, source, client := newBrowserSessionTestService(t)
	linkBrowserSessionTestDestination(t, s, source, client)
	ctx := context.Background()
	verify := func(grant ProductIdentitySession, want error) {
		t.Helper()
		if err := s.VerifyProductBrowserSource(ctx, grant.SourceProofToken, "agentwiki", "default", sessionTestClient, source.Binding.UserID); !errors.Is(err, want) {
			t.Fatalf("source verification got %v, want %v", err, want)
		}
	}
	apply := func(query string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	transition := func(op string, previous int64, state SourceState) {
		t.Helper()
		_, err := s.SyncSourceLifecycle(ctx, "native", "default", SourceLifecycle{
			Lifecycle: Lifecycle{LocalUserID: source.Binding.LocalUserID, OperationID: op,
				PreviousAuthVersion: previous, NextAuthVersion: previous + 1}, State: state,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	independent, _ := issueIdentityBrowserSessionTestProof(t, s, source, client, false)
	missingOrigin, missingSID := issueIdentityBrowserSessionTestProof(t, s, source, client, true)
	apply(`DELETE FROM product_browser_session_origins WHERE session_id_hash=$1`, missingSID)
	verify(missingOrigin, accounts.ErrForbidden)

	native, sid := issueIdentityBrowserSessionTestProof(t, s, source, client, true)
	verify(native, nil)
	apply(`UPDATE product_user_access SET active=false WHERE user_id=$1 AND product_id='native'`, source.Binding.UserID)
	verify(native, accounts.ErrForbidden)
	apply(`UPDATE product_user_access SET active=true,permission_overrides='{"product":{"use":false}}' WHERE user_id=$1 AND product_id='native'`, source.Binding.UserID)
	verify(native, accounts.ErrForbidden)
	apply(`UPDATE product_user_access SET permission_overrides='{}' WHERE user_id=$1 AND product_id='native'`, source.Binding.UserID)
	verify(native, nil)
	transition("source-lock", 1, SourceStateLocked)
	verify(native, accounts.ErrForbidden)
	// Even evidence at the locked epoch must fail. Version comparison alone
	// cannot enforce native lock if a caller minted evidence at that epoch.
	apply(`UPDATE product_browser_session_origins SET auth_version=2 WHERE session_id_hash=$1`, sid)
	apply(`UPDATE product_browser_source_proofs SET source_auth_version=2 WHERE source_session_hash=$1`, sid)
	verify(native, accounts.ErrForbidden)
	verify(independent, nil)

	transition("source-unlock", 2, SourceStateActive)
	verify(native, accounts.ErrForbidden)
	source.AuthVersion = 3
	fresh, freshSID := issueIdentityBrowserSessionTestProof(t, s, source, client, true)
	verify(fresh, nil)
	transition("source-delete", 3, SourceStateDeleted)
	apply(`UPDATE product_browser_session_origins SET auth_version=4 WHERE session_id_hash=$1`, freshSID)
	apply(`UPDATE product_browser_source_proofs SET source_auth_version=4 WHERE source_session_hash=$1`, freshSID)
	verify(fresh, accounts.ErrForbidden)
	verify(independent, nil)
}
