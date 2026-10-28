package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestOperationsCSVFormulaEscaping(t *testing.T) {
	for _, s := range []string{"=SUM(A1)", "+1", "-2", "@x", "\t=x", "\r=x", "  =x"} {
		if !strings.HasPrefix(safeCSVCell(s), "'") {
			t.Errorf("unsafe %q", s)
		}
	}
	if safeCSVCell("普通,用户") != "普通,用户" {
		t.Fatal("plain cell changed")
	}
}

func TestOperationCursorBindsFilterActorAndResource(t *testing.T) {
	o := OperationsOptions{ListOptions: ListOptions{Limit: 50, Search: "alice"}}
	raw, e := encodeOperationCursor(operationCursor{Binding: operationBinding("a", "accounts", o), ID: "acct", At: operationEpoch, AsOf: operationEpoch, Expires: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = decodeOperationCursor(raw, operationBinding("b", "accounts", o)); e == nil {
		t.Fatal("actor changed")
	}
	o.Search = "bob"
	if _, e = decodeOperationCursor(raw, operationBinding("a", "accounts", o)); e == nil {
		t.Fatal("filter changed")
	}
}

func TestOperationsAuthorizationPaginationAndSafeKeys(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	ids := map[string]string{}
	for _, role := range []string{"super_admin", "finance_admin", "user_admin", "auditor", "ordinary"} {
		u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://operations.test", Subject: role, Name: role})
		if e != nil {
			t.Fatal(e)
		}
		ids[role] = u.ID
		if role != "ordinary" {
			b, _ := json.Marshal([]string{role})
			if _, e = p.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,$2,'test',$1)`, u.ID, b); e != nil {
				t.Fatal(e)
			}
		}
	}
	for i := 0; i < 112; i++ {
		if _, e := p.Exec(ctx, `INSERT INTO accounts(id,kind,name) VALUES($1,'team',$2)`, fmt.Sprintf("team_%03d", i), fmt.Sprintf("Team %03d", i)); e != nil {
			t.Fatal(e)
		}
	}
	s := New(p, nil)
	for _, actor := range []string{ids["finance_admin"], ids["auditor"]} {
		if _, e := s.ListOperations(ctx, actor, "orders", OperationsOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	for _, actor := range []string{ids["user_admin"], ids["ordinary"]} {
		if _, e := s.ListOperations(ctx, actor, "orders", OperationsOptions{}); !errors.Is(e, accounts.ErrForbidden) {
			t.Fatalf("finance leaked: %v", e)
		}
	}
	opt := OperationsOptions{ListOptions: ListOptions{Limit: 37, Search: "Team"}}
	seen := map[string]bool{}
	for {
		page, e := s.ListOperations(ctx, ids["user_admin"], "accounts", opt)
		if e != nil {
			t.Fatal(e)
		}
		if page.Total != 112 {
			t.Fatalf("total %d", page.Total)
		}
		for _, v := range page.Items {
			id := v["id"].(string)
			if seen[id] {
				t.Fatal("duplicate")
			}
			seen[id] = true
		}
		if page.NextCursor == "" {
			break
		}
		opt.Cursor = page.NextCursor
	}
	if len(seen) != 112 {
		t.Fatalf("truncated: %d", len(seen))
	}
	accts, e := a.ListAccounts(ctx, ids["ordinary"])
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,credential_kind,name) VALUES('safe-key','super-secret-digest',$1,$2,'llmrouter','[]','task-private',now()+interval '1 hour','api_key','safe')`, ids["ordinary"], accts[0].ID); e != nil {
		t.Fatal(e)
	}
	page, e := s.ListOperations(ctx, ids["finance_admin"], "api-keys", OperationsOptions{})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(page)
	for _, secret := range []string{"super-secret", "token_hash", "task-private", "token\""} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret leaked: %s", raw)
		}
	}
	for _, resource := range []string{"products", "calls", "transactions", "recharges", "orders", "refunds", "subscriptions", "credit-batches", "offers", "rates", "billing-profiles", "usage-budgets", "audit-events"} {
		if _, e := s.ListOperations(ctx, ids["super_admin"], resource, OperationsOptions{}); e != nil {
			t.Errorf("%s: %v", resource, e)
		}
	}
}

func TestOperationsProtectedKeyAuditAndExports(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	s := New(pool, nil)
	ids := map[string]string{}
	for _, role := range []string{"super_admin", "user_admin", "finance_admin", "auditor", "ordinary"} {
		u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://keys.test", Subject: role})
		if e != nil {
			t.Fatal(e)
		}
		ids[role] = u.ID
		if role != "ordinary" {
			raw, _ := json.Marshal([]string{role})
			if _, e = pool.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,$2,'test',$1)`, u.ID, raw); e != nil {
				t.Fatal(e)
			}
		}
		accts, e := a.ListAccounts(ctx, u.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,credential_kind) VALUES($1,$2,$3,$4,'llmrouter','[]','task',now()+interval '1 hour','api_key')`, role, role+"-private-digest", u.ID, accts[0].ID); e != nil {
			t.Fatal(e)
		}
	}
	for _, role := range []string{"finance_admin", "auditor"} {
		if _, e := s.RevokeOperationKey(ctx, ids[role], "ordinary", "revoke test", 0); !errors.Is(e, accounts.ErrForbidden) {
			t.Fatalf("%s revoked: %v", role, e)
		}
	}
	if _, e := s.RevokeOperationKey(ctx, ids["user_admin"], "super_admin", "protected", 0); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("protected key revoked %v", e)
	}
	if _, e := s.RevokeOperationKey(ctx, ids["user_admin"], "ordinary", "ordinary key", 0); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RevokeOperationKey(ctx, ids["user_admin"], "ordinary", "stale", 0); !errors.Is(e, billing.ErrConflict) {
		t.Fatal("stale revoke accepted", e)
	}
	if _, e := pool.Exec(ctx, `UPDATE platform_admin_roles SET roles='["super_admin","user_admin"]' WHERE user_id=$1`, ids["super_admin"]); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RevokeOperationKey(ctx, ids["super_admin"], "finance_admin", "stacked super key", 0); e != nil {
		t.Fatal(e)
	}
	if _, e := pool.Exec(ctx, `INSERT INTO platform_admin_audit(actor_id,action,target_id,reason,detail) VALUES($1,'finance.offer','hidden','finance amount','{"before":{"token":"TOPSECRET","price":"123"},"after":{"status":"published"}}'),($2,'user.status','visible','status support','{"after":{"active":false}}')`, ids["super_admin"], ids["super_admin"]); e != nil {
		t.Fatal(e)
	}
	page, e := s.ListOperations(ctx, ids["user_admin"], "audit-events", OperationsOptions{})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "finance amount") || strings.Contains(string(raw), "TOPSECRET") {
		t.Fatalf("scoped audit leaked %s", raw)
	}
	all, e := s.ListOperations(ctx, ids["auditor"], "audit-events", OperationsOptions{})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(all)
	if strings.Contains(string(raw), "TOPSECRET") || !strings.Contains(string(raw), "123") {
		t.Fatalf("audit redaction lost boundary %s", raw)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO accounts(id,kind,name) VALUES('formula','team','=HYPERLINK(""https://invalid"")')`); e != nil {
		t.Fatal(e)
	}
	csv, e := s.ExportOperations(ctx, ids["user_admin"], "accounts", OperationsOptions{ListOptions: ListOptions{Search: "HYPERLINK"}})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(csv), "'=HYPERLINK") {
		t.Fatalf("unescaped CSV %s", csv)
	}
	if _, e = s.ExportOperations(ctx, ids["user_admin"], "orders", OperationsOptions{}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("export bypass", e)
	}
}
