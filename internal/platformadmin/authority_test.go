package platformadmin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func coreFixture(t *testing.T) (*Store, []string) {
	t.Helper()
	p := testutil.Database(t)
	if e := database.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	a := accounts.New(p)
	for i := 0; i < 5; i++ {
		u, e := a.EnsureIdentity(context.Background(), accounts.Identity{Issuer: "fixture", Subject: fmt.Sprint(i), Name: fmt.Sprint("User ", i)})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, u.ID)
	}
	s := New(p, nil)
	if e := s.Bootstrap(context.Background(), "deployment-cli", ids[0], "initial explicit bootstrap"); e != nil {
		t.Fatal(e)
	}
	return s, ids
}
func TestRolesExactIdentityLegacyAndProtectedTargets(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	if e := s.Require(ctx, "fixture", "users:read"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("non UUID allowed", e)
	}
	if _, e := s.SetRoles(ctx, ids[0], ids[1], []string{"user_admin"}, 0, "support role"); e != nil {
		t.Fatal(e)
	}
	if e := s.Require(ctx, ids[1], "finance:read"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("finance leakage", e)
	}
	if e := s.RequireTarget(ctx, ids[1], ids[0], "users:status"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("admin target allowed", e)
	}
	if _, e := s.SetRoles(ctx, ids[0], ids[0], []string{"super_admin", "user_admin"}, 1, "stack roles"); e != nil {
		t.Fatal(e)
	}
	if e := s.RequireTarget(ctx, ids[0], ids[1], "users:status"); e != nil {
		t.Fatal("stacked super denied", e)
	}
	if e := s.ImportLegacy(ctx, "deployment-cli", []string{ids[2]}, "legacy import"); e != nil {
		t.Fatal(e)
	}
	p, e := s.Principal(ctx, ids[2])
	if e != nil || len(p.Roles) != 0 || !p.LegacyUserAdmin {
		t.Fatal("legacy escalated", p, e)
	}
	if e = s.Require(ctx, ids[2], "identity:manage"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("legacy expanded", e)
	}
	if _, e = s.SetRoles(ctx, ids[0], ids[2], []string{}, 1, "revoke legacy"); e != nil {
		t.Fatal(e)
	}
	if e = s.ImportLegacy(ctx, "deployment-cli", []string{ids[2]}, "repeat import"); e != nil {
		t.Fatal(e)
	}
	if e = s.Require(ctx, ids[2], "users:read"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("import resurrected revoked grant", e)
	}
}
func TestConcurrentSuperDemotionAndSelfProtection(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	if _, e := s.SetRoles(ctx, ids[0], ids[0], []string{}, 1, "self demote"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e := s.SetRoles(ctx, ids[0], ids[1], []string{"super_admin"}, 0, "second super"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, e := s.SetRoles(ctx, ids[i], ids[1-i], []string{}, 1, "concurrent demotion")
			out <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(out)
	successes := 0
	for e := range out {
		if e == nil {
			successes++
		} else if !errors.Is(e, accounts.ErrForbidden) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatal("demotions", successes)
	}
	var n int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM platform_admin_roles a JOIN users u ON u.id=a.user_id WHERE u.active AND a.roles ? 'super_admin'`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestDirectoryTraversesMoreThan100AndBindsFilters(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	for i := 0; i < 125; i++ {
		if _, e := s.pool.Exec(ctx, `INSERT INTO users(id,name) VALUES($1,$2)`, fmt.Sprintf("directory-%03d", i), "Directory"); e != nil {
			t.Fatal(e)
		}
	}
	opts := ListOptions{Limit: 33}
	seen := map[string]bool{}
	for {
		p, e := s.ListUsers(ctx, ids[0], opts)
		if e != nil {
			t.Fatal(e)
		}
		if p.Total != 130 {
			t.Fatal("total", p.Total)
		}
		for _, u := range p.Items {
			if seen[u.ID] {
				t.Fatal("duplicate", u.ID)
			}
			seen[u.ID] = true
		}
		if p.NextCursor == "" {
			break
		}
		opts.Cursor = p.NextCursor
		if len(seen) == 33 {
			bad := opts
			bad.Search = "different"
			if _, e := s.ListUsers(ctx, ids[0], bad); !errors.Is(e, accounts.ErrInvalid) {
				t.Fatal("changed filter accepted", e)
			}
		}
	}
	if len(seen) != 130 {
		t.Fatal("truncated", len(seen))
	}
}
func TestDisableRevokesOnlyTargetCredentialsPermanently(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	for i := 1; i < 3; i++ {
		_, e := s.pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,'fixture',$2,'','','x',now()+interval '1 hour')`, fmt.Sprint(i), fmt.Sprint(i))
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.SetStatus(ctx, ids[0], ids[1], false, 1, "security disable"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetStatus(ctx, ids[0], ids[1], true, 2, "reenable"); e != nil {
		t.Fatal(e)
	}
	var count int
	var cutoff bool
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM browser_sessions`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if e := s.pool.QueryRow(ctx, `SELECT session_not_before>'epoch' FROM users WHERE id=$1`, ids[1]).Scan(&cutoff); e != nil || !cutoff {
		t.Fatal(cutoff, e)
	}
}
func TestDisableIsAtomicWithKeysAuditAndCutoff(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	if _, e := s.pool.Exec(ctx, `INSERT INTO products(id,name,enabled) VALUES('fixture','Fixture',true)`); e != nil {
		t.Fatal(e)
	}
	for i := 1; i < 3; i++ {
		_, e := s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,credential_kind) SELECT $1,$1,$2,a.id,'fixture','[]','fixture',now()+interval '1 day','api_key' FROM accounts a WHERE a.personal_user_id=$2`, fmt.Sprint("key-", i), ids[i])
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.pool.Exec(ctx, `CREATE FUNCTION reject_status_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.status' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER status_audit_failure BEFORE INSERT ON platform_admin_audit FOR EACH ROW EXECUTE FUNCTION reject_status_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetStatus(ctx, ids[0], ids[1], false, 1, "fail transaction"); e == nil {
		t.Fatal("audit failure ignored")
	}
	var active, revoked, cutoff bool
	if e := s.pool.QueryRow(ctx, `SELECT u.active,c.revoked_at IS NOT NULL,u.session_not_before>'epoch' FROM users u JOIN model_credentials c ON c.actor_id=u.id WHERE u.id=$1`, ids[1]).Scan(&active, &revoked, &cutoff); e != nil || !active || revoked || cutoff {
		t.Fatal("partial mutation", active, revoked, cutoff, e)
	}
	if _, e := s.pool.Exec(ctx, `DROP TRIGGER status_audit_failure ON platform_admin_audit`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetStatus(ctx, ids[0], ids[1], false, 1, "disable"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetStatus(ctx, ids[0], ids[1], true, 2, "reenable"); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM model_credentials WHERE revoked_at IS NOT NULL`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if e := s.CredentialAllowed(ctx, ids[1], 0); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("missing issue time restored", e)
	}
}
func TestConcurrentSuperDisableLeavesOne(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	if _, e := s.SetRoles(ctx, ids[0], ids[1], []string{"super_admin"}, 0, "second super"); e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) { <-start; _, e := s.SetStatus(ctx, ids[i], ids[1-i], false, 1, "cross disable"); out <- e }(i)
	}
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		if e := <-out; e == nil {
			success++
		} else if !errors.Is(e, accounts.ErrForbidden) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
}
func TestDisabledUserCannotIssueCredentialAfterRevocation(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	if _, e := s.pool.Exec(ctx, `INSERT INTO products(id,name,enabled) VALUES('fixture','Fixture',true)`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetStatus(ctx, ids[0], ids[1], false, 1, "security disable"); e != nil {
		t.Fatal(e)
	}
	_, e := s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,credential_kind) SELECT 'late-key','late-key',$1,a.id,'fixture','[]','fixture',now()+interval '1 day','api_key' FROM accounts a WHERE a.personal_user_id=$1`, ids[1])
	if e == nil {
		t.Fatal("credential issuance raced past disabled user revocation")
	}
}
func TestSessionsPaginateSafeReferencesAndRejectStaleCursor(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	for i := 0; i < 105; i++ {
		if _, e := s.pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,'fixture','1','','','secret-csrf',now()+interval '1 hour')`, fmt.Sprintf("secret-session-hash-%d", i)); e != nil {
			t.Fatal(e)
		}
	}
	o := ListOptions{Limit: 40}
	seen := map[string]bool{}
	for {
		page, e := s.ListSessions(ctx, ids[0], ids[1], o)
		if e != nil {
			t.Fatal(e)
		}
		if page.Total != 105 {
			t.Fatal(page.Total)
		}
		for _, session := range page.Items {
			if len(session.ID) != 64 || seen[session.ID] {
				t.Fatal("unsafe/duplicate reference", session.ID)
			}
			seen[session.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		o.Cursor = page.NextCursor
		if len(seen) == 40 {
			if _, e := s.ListSessions(ctx, ids[0], ids[2], o); !errors.Is(e, accounts.ErrInvalid) {
				t.Fatal("cursor changed target accepted", e)
			}
			bad := o
			bad.Cursor = "tampered" + bad.Cursor
			if _, e := s.ListSessions(ctx, ids[0], ids[1], bad); !errors.Is(e, accounts.ErrInvalid) {
				t.Fatal("tamper accepted", e)
			}
		}
	}
	if len(seen) != 105 {
		t.Fatal(len(seen))
	}
}
func TestDirectoryCreationRangeAndBatchProtectedTargets(t *testing.T) {
	ctx := context.Background()
	s, ids := coreFixture(t)
	if _, e := s.pool.Exec(ctx, `UPDATE users SET created_at='2001-01-01' WHERE id=$1`, ids[1]); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetOrdinaryStatus(ctx, ids[0], ids[0], false, 1, "batch"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("batch administrator allowed", e)
	}
	before := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)
	p, e := s.ListUsers(ctx, ids[0], ListOptions{CreatedTo: before})
	if e != nil || p.Total != 1 || len(p.Items) != 1 || p.Items[0].ID != ids[1] {
		t.Fatal(p, e)
	}
}
