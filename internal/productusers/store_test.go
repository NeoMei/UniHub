package productusers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

const manifests = `[{"product_id":"notes","name":"Notes","entry_url":"https://notes.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"operator","name":"Operator","permissions":["product:use","channel:read"]}]},{"product_id":"notes","instance_id":"other","name":"Other Notes","entry_url":"https://other.test","version":2,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"operator","name":"Operator","permissions":["channel:read"]}]}]`

func TestDefaultsExplicitEmptyIsolationAndOptimisticAudit(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	r, e := Parse([]byte(manifests))
	if e != nil {
		t.Fatal(e)
	}
	s := New(p, r, "https://hub.test", []string{u.ID})
	v, e := s.Resolve(ctx, u, "notes", "default")
	if e != nil || len(v.Permissions) != 1 || v.Permissions[0] != "product:use" {
		t.Fatalf("default %#v %v", v, e)
	}
	assignment, e := s.SetAccess(ctx, u.ID, u.ID, "notes", "default", []string{"operator"}, true, 0)
	if e != nil || assignment.Version != 1 {
		t.Fatalf("assignment %#v %v", assignment, e)
	}
	v, e = s.Resolve(ctx, u, "notes", "other")
	if e != nil || len(v.Permissions) != 1 || v.Permissions[0] != "product:use" {
		t.Fatal("cross-instance leakage", v, e)
	}
	if _, e = s.SetAccess(ctx, u.ID, u.ID, "notes", "default", nil, true, 0); !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("stale accepted", e)
	}
	if _, e = s.SetAccess(ctx, u.ID, u.ID, "notes", "default", []string{}, true, 1); e != nil {
		t.Fatal(e)
	}
	v, e = s.Resolve(ctx, u, "notes", "default")
	if e != nil || len(v.Roles) != 0 || len(v.Permissions) != 0 {
		t.Fatal("defaults resurrected", v, e)
	}
	if _, e = s.SetAccess(ctx, u.ID, u.ID, "notes", "default", nil, false, 2); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Resolve(ctx, u, "notes", "default"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("blocked membership allowed", e)
	}
	var count int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM product_user_audit WHERE actor_id=$1 AND target_id=$1 AND product_id='notes' AND instance_id='default'`, u.ID).Scan(&count); e != nil || count != 3 {
		t.Fatalf("audit %d %v", count, e)
	}
	var billing int
	p.QueryRow(ctx, `SELECT count(*) FROM products WHERE id='notes'`).Scan(&billing)
	if billing != 0 {
		t.Fatal("identity directory depends on billing registry")
	}
}
func TestGlobalStatusKeepsLastAdminAndSerializesWriters(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	database.Migrate(ctx, p)
	a := accounts.New(p)
	u, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "admin"})
	v, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "member"})
	r, _ := Parse([]byte(manifests))
	s := New(p, r, "https://hub.test", []string{u.ID})
	if _, e := s.SetStatus(ctx, u.ID, u.ID, false, 1); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("last admin disabled", e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.SetStatus(ctx, u.ID, v.ID, false, 1); results <- e }()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, accounts.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success %d conflict %d", success, conflicts)
	}
	if e := s.CheckActive(ctx, v.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("disabled active", e)
	}
}
func TestManifestRejectsUnsafeDefaultsAndUnknownRoles(t *testing.T) {
	for _, raw := range []string{`[{"product_id":"notes","name":"Notes","entry_url":"javascript:alert(1)","version":1,"default_roles":[],"roles":[]}]`, `[{"product_id":"notes","name":"Notes","entry_url":"https://notes.test","version":1,"default_roles":["admin"],"roles":[{"id":"admin","name":"Admin","privileged":true,"permissions":["channel:write"]}]}]`} {
		if _, e := Parse([]byte(raw)); e == nil {
			t.Fatal("unsafe manifest accepted")
		}
	}
}

func TestOrdinaryProductBaselineCanDeclareItsOwnPermissions(t *testing.T) {
	r, e := Parse([]byte(`[{"product_id":"articles","name":"Articles","entry_url":"https://articles.test","version":1,"default_roles":["author"],"roles":[{"id":"author","name":"Author","permissions":["article:create"]}]}]`))
	if e != nil || len(r) != 1 {
		t.Fatal("ordinary baseline rejected", e)
	}
}

func TestConcurrentDisableOfTwoConfiguredAdminsLeavesOneAvailable(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	first, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "first-admin"})
	second, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "second-admin"})
	r, _ := Parse([]byte(manifests))
	s := New(p, r, "https://hub.test", []string{first.ID, second.ID})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, u := range []accounts.User{first, second} {
		go func(u accounts.User) { <-start; _, e := s.SetStatus(ctx, u.ID, u.ID, false, 1); results <- e }(u)
	}
	close(start)
	success, denied := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if errors.Is(e, accounts.ErrForbidden) {
			denied++
		} else {
			t.Fatal(e)
		}
	}
	var available int
	if e := p.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=ANY($1) AND active`, []string{first.ID, second.ID}).Scan(&available); e != nil {
		t.Fatal(e)
	}
	if success != 1 || denied != 1 || available != 1 {
		t.Fatalf("success=%d denied=%d available=%d", success, denied, available)
	}
}

func TestStatusTransitionsAdvanceEffectiveAccessWithoutChangingAssignmentVersion(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	accountStore := accounts.New(p)
	admin, err := accountStore.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "revision-admin"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := accountStore.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "revision-member"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := Parse([]byte(manifests))
	if err != nil {
		t.Fatal(err)
	}
	store := New(p, registry, "https://hub.test", []string{admin.ID})
	if _, err = store.SetAccess(ctx, admin.ID, member.ID, "notes", "default", []string{"member"}, true, 0); err != nil {
		t.Fatal(err)
	}
	before, err := store.Resolve(ctx, member, "notes", "default")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := store.SetStatus(ctx, admin.ID, member.ID, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Resolve(ctx, member, "notes", "default"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("disabled resolve: %v", err)
	}
	if _, err = store.SetStatus(ctx, admin.ID, member.ID, true, disabled.Version); err != nil {
		t.Fatal(err)
	}
	after, err := store.Resolve(ctx, member, "notes", "default")
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version+2 {
		t.Fatalf("effective revision after disable/reenable = %d, before %d; want +2", after.Version, before.Version)
	}
	assignments, err := store.Assignments(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if assignments[0].Version != 1 || assignments[0].ManifestVersion != 1 {
		t.Fatalf("status changed independent assignment/manifest revision: %#v", assignments[0])
	}
	if _, err = store.SetAccess(ctx, admin.ID, member.ID, "notes", "default", []string{}, true, 1); err != nil {
		t.Fatalf("unchanged assignment precondition rejected: %v", err)
	}
}
