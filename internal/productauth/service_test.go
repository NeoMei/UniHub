package productauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func TestAuthenticationRequiresFreshCompleteNativeEvidence(t *testing.T) {
	now := time.Unix(2000, 0)
	for _, method := range []string{"password", "2fa", "passkey", "wechat", "telegram", "oauth:github", "session"} {
		v := Authentication{LocalUserID: "17", AuthenticationID: "event-1234567890123456", Method: method, AuthenticatedAt: now.Unix(), AuthVersion: 1}
		if e := validateAuthentication(v, now); e != nil {
			t.Fatalf("valid %s: %v", method, e)
		}
		v.AuthenticatedAt -= 61
		if e := validateAuthentication(v, now); e == nil {
			t.Fatal("stale proof accepted")
		}
	}
	for _, method := range []string{"", "mfa_pending", "oauth:", "root", "oidc\nadmin"} {
		if e := validateAuthentication(Authentication{LocalUserID: "17", AuthenticationID: "event-1234567890123456", Method: method, AuthenticatedAt: now.Unix(), AuthVersion: 1}, now); e == nil {
			t.Fatal("incomplete or invalid authentication accepted")
		}
	}
}

func TestStableNativeBindingReplayRevocationAndProductRole(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	registry, e := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"admin","name":"Admin","privileged":true,"permissions":["product:use","user:read","user:write","channel:read","channel:write"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	users := productusers.New(pool, registry, "https://hub.test", nil)
	s := New(pool, accounts.New(pool), users, "https://hub.test")
	if _, e = s.Lookup(ctx, "llmrouter", "default", "unknown"); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("lookup created identity", e)
	}
	member := Registration{LocalUserID: "17", OperationID: "register-member-17", Username: "member", Email: "same@example.test", AuthVersion: 1}
	first, e := s.Register(ctx, "llmrouter", "default", member)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Register(ctx, "llmrouter", "default", member)
	if e != nil || again.Binding.UserID != first.Binding.UserID || again.Binding.LocalUserID != "17" || again.ProductRole != 1 {
		t.Fatal("unstable or elevated registration", e)
	}
	root := Registration{LocalUserID: "1", OperationID: "import-root-0001", Username: "root", Email: "same@example.test", AuthVersion: 3}
	admin, e := s.Import(ctx, "llmrouter", "default", Import{Registration: root, ReviewedBatchID: "reviewed-batch-001", SnapshotDigest: strings.Repeat("a", 64), ProductRole: 100, Active: true})
	if e != nil {
		t.Fatal(e)
	}
	if admin.Binding.UserID == first.Binding.UserID || admin.ProductRole != 100 {
		t.Fatal("email merge or administrator mapping lost")
	}
	proof := Authentication{LocalUserID: "17", AuthenticationID: "authenticate-001", Method: "passkey", AuthenticatedAt: time.Now().Unix(), AuthVersion: 1}
	session, e := s.Authenticate(ctx, "llmrouter", "default", proof)
	if e != nil || session.ActorToken == "" {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, "llmrouter", "default", proof); !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("authentication replay accepted", e)
	}
	if _, e = s.VerifyActor(ctx, session.ActorToken, "llmrouter", "other"); e == nil {
		t.Fatal("cross-instance actor accepted")
	}
	next := int64(2)
	updated, e := s.Mutate(ctx, "llmrouter", "default", first.Binding.UserID, "17", Mutation{OperationID: "password-event-001", Event: "password", ExpectedVersion: first.Access.Version, NextAuthVersion: &next})
	if e != nil {
		t.Fatal(e)
	}
	if updated.AuthVersion != 2 {
		t.Fatal("credential event version lost")
	}
	if _, e = s.VerifyActor(ctx, session.ActorToken, "llmrouter", "default"); e == nil {
		t.Fatal("old actor survived credential change")
	}
	proof.AuthenticationID = "authenticate-002"
	if _, e = s.Authenticate(ctx, "llmrouter", "default", proof); e == nil {
		t.Fatal("old local credential version accepted")
	}
	proof.AuthVersion = 2
	session, e = s.Authenticate(ctx, "llmrouter", "default", proof)
	if e != nil {
		t.Fatal(e)
	}
	no := false
	_, e = s.Mutate(ctx, "llmrouter", "default", admin.Binding.UserID, "17", Mutation{OperationID: "disable-event-001", Event: "disable", ExpectedVersion: updated.Access.Version, Active: &no})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.VerifyActor(ctx, session.ActorToken, "llmrouter", "default"); e == nil {
		t.Fatal("disabled actor survived")
	}
	if _, e = s.Authenticate(ctx, "llmrouter", "default", Authentication{LocalUserID: "17", AuthenticationID: "authenticate-003", Method: "password", AuthenticatedAt: time.Now().Unix(), AuthVersion: 2}); e == nil {
		t.Fatal("disabled subject authenticated")
	}
}

func TestReviewedImportExplicitlyLinksExistingCanonicalIdentity(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	registry, e := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"admin","name":"Admin","native_baseline":true,"product_role":10,"privileged":true,"permissions":["product:use","user:write","channel:read"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	canonical, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://oidc.test", Subject: "canonical", Email: "same@test"})
	if e != nil {
		t.Fatal(e)
	}
	users := productusers.New(pool, registry, "https://hub.test", []string{canonical.ID})
	s := New(pool, a, users, "https://hub.test")
	request := Import{Registration: Registration{LocalUserID: "17", OperationID: "import-local-17", Username: "legacy", Email: "same@test", AuthVersion: 1}, CentralUserID: canonical.ID, ReviewedBatchID: "reviewed-native-17", SnapshotDigest: strings.Repeat("a", 64), ProductRole: 100, Active: true}
	out, e := s.Import(ctx, "llmrouter", "default", request)
	if e != nil {
		t.Fatal(e)
	}
	if out.Binding.UserID != canonical.ID || out.Binding.LocalUserID != "17" || out.ProductRole != 100 {
		t.Fatal("explicit canonical link or root lost")
	}
	if _, e = s.Import(ctx, "llmrouter", "default", request); e != nil {
		t.Fatal("reviewed replay rejected", e)
	}
	other, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://oidc.test", Subject: "other", Email: "same@test"})
	if e != nil {
		t.Fatal(e)
	}
	request.CentralUserID = other.ID
	if _, e = s.Import(ctx, "llmrouter", "default", request); e == nil {
		t.Fatal("existing mapping replaced by same-email identity")
	}
	if _, e = users.SetAccess(ctx, canonical.ID, canonical.ID, "llmrouter", "default", []string{"member"}, true, 2); e != nil {
		t.Fatal(e)
	}
	out, e = s.Lookup(ctx, "llmrouter", "default", "17")
	if e != nil {
		t.Fatal(e)
	}
	if out.ProductRole != 1 || len(out.Access.Permissions) != 1 || out.Access.Permissions[0] != "product:use" {
		t.Fatal("central role replacement resurrected imported privilege")
	}
}

func TestNativeRoleReplacementRemovesCentralRoleAndCannotDelegateDeniedPermission(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	registry, e := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"product_admin","name":"Admin","native_baseline":true,"product_role":10,"privileged":true,"permissions":["product:use","user:write","credit:adjust","redemption:write"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	manifest := registry["llmrouter/default"]
	manifest.Roles = append(manifest.Roles, productusers.Role{ID: "viewer", Name: "Viewer", Privileged: true, ProductRole: 10, Permissions: []string{"product:use"}})
	registry["llmrouter/default"] = manifest
	users := productusers.New(pool, registry, "https://hub.test", nil)
	s := New(pool, accounts.New(pool), users, "https://hub.test")
	root, e := s.Import(ctx, "llmrouter", "default", Import{Registration: Registration{LocalUserID: "1", OperationID: "root-register", AuthVersion: 1}, ReviewedBatchID: "root-reviewed", SnapshotDigest: strings.Repeat("a", 64), ProductRole: 100, Active: true})
	if e != nil {
		t.Fatal(e)
	}
	target, e := s.Register(ctx, "llmrouter", "default", Registration{LocalUserID: "2", OperationID: "target-register", AuthVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	_, e = pool.Exec(ctx, `UPDATE product_user_access SET roles='["product_admin"]',product_role=10 WHERE user_id=$1`, target.Binding.UserID)
	if e != nil {
		t.Fatal(e)
	}
	target, e = s.Lookup(ctx, "llmrouter", "default", "2")
	if e != nil {
		t.Fatal(e)
	}
	one := 1
	out, e := s.Mutate(ctx, "llmrouter", "default", root.Binding.UserID, "2", Mutation{OperationID: "native-demote", ExpectedVersion: target.Access.Version, ProductRole: &one, Event: "authorization"})
	if e != nil {
		t.Fatal(e)
	}
	if out.ProductRole != 1 || len(out.Access.Permissions) != 1 || out.Access.Permissions[0] != "product:use" {
		t.Fatalf("native demotion left grants: role %d permissions %v", out.ProductRole, out.Access.Permissions)
	}
	if _, e = pool.Exec(ctx, `UPDATE product_user_access SET permission_overrides='{"credit":{"adjust":false}}' WHERE user_id=$1`, root.Binding.UserID); e != nil {
		t.Fatal(e)
	}
	ten := 10
	_, e = s.Mutate(ctx, "llmrouter", "default", root.Binding.UserID, "2", Mutation{OperationID: "forbidden-delegation", ExpectedVersion: out.Access.Version, ProductRole: &ten, Event: "authorization"})
	if !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("denied actor permission delegated via native baseline", e)
	}
	final, e := s.Lookup(ctx, "llmrouter", "default", "2")
	if e != nil {
		t.Fatal(e)
	}
	if final.ProductRole != 1 {
		t.Fatal("rejected promotion changed target")
	}
	administrator, e := s.Import(ctx, "llmrouter", "default", Import{Registration: Registration{LocalUserID: "3", OperationID: "native-admin-register", AuthVersion: 1}, ReviewedBatchID: "native-admin-reviewed", SnapshotDigest: strings.Repeat("c", 64), ProductRole: 10, Active: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `UPDATE product_user_access SET roles='["viewer"]',product_role=1 WHERE user_id=$1`, target.Binding.UserID); e != nil {
		t.Fatal(e)
	}
	final, e = s.Lookup(ctx, "llmrouter", "default", "2")
	if e != nil {
		t.Fatal(e)
	}
	if final.ProductRole != 10 {
		t.Fatal("fixture did not create delegated effective admin")
	}
	if _, e = s.Mutate(ctx, "llmrouter", "default", administrator.Binding.UserID, "2", Mutation{OperationID: "cannot-edit-peer-viewer", Event: "profile", ExpectedVersion: final.Access.Version}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("native1/effective10 peer was treated as ordinary", e)
	}
	var assignmentVersion int64
	if e = pool.QueryRow(ctx, `SELECT version FROM product_user_access WHERE user_id=$1 AND product_id='llmrouter'`, target.Binding.UserID).Scan(&assignmentVersion); e != nil {
		t.Fatal(e)
	}
	if _, e = users.SetRuntimeAccess(ctx, administrator.Binding.UserID, target.Binding.UserID, "llmrouter", "default", []string{"member"}, true, assignmentVersion); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("runtime native1/effective10 peer was treated as ordinary", e)
	}

}
