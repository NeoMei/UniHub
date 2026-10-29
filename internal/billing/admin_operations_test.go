package billing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/platformadmin"
	"unihub/internal/testutil"
)

func adminFixture(t *testing.T) (*billing.Service, *platformadmin.Store, *pgxpool.Pool, map[string]string, string) {
	t.Helper()
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	ids := map[string]string{}
	for _, role := range []string{"super_admin", "finance_admin", "user_admin", "auditor", "owner"} {
		u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://admin-billing.test", Subject: role})
		if e != nil {
			t.Fatal(e)
		}
		ids[role] = u.ID
		if role != "owner" {
			raw, _ := json.Marshal([]string{role})
			if _, e = pool.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,$2,'test',$1)`, u.ID, raw); e != nil {
				t.Fatal(e)
			}
		}
	}
	accts, e := a.ListAccounts(ctx, ids["owner"])
	if e != nil {
		t.Fatal(e)
	}
	return billing.New(pool, a, nil), platformadmin.New(pool, nil), pool, ids, accts[0].ID
}
func revision(t *testing.T, pool *pgxpool.Pool, table, id string) string {
	t.Helper()
	var out string
	if e := pool.QueryRow(context.Background(), `SELECT md5(snapshot::text) FROM `+table+` WHERE id=$1`, id).Scan(&out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestPlatformFinanceScopedWritesAndOriginalAllowlist(t *testing.T) {
	s, authority, pool, ids, account := adminFixture(t)
	ctx := context.Background()
	profile, _ := billing.BillingPreset("superwagie", "prepaid")
	req := billing.AdminOperation{Action: "profile.save", ProductID: "superwagie", Profile: profile, Version: "0", Reason: "test policy"}
	for _, role := range []string{"user_admin", "auditor", "owner"} {
		if _, e := s.Administer(ctx, authority, ids[role], req); !errors.Is(e, accounts.ErrForbidden) {
			t.Fatalf("%s wrote: %v", role, e)
		}
	}
	if _, e := s.Administer(ctx, authority, ids["finance_admin"], req); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SaveProfile(ctx, ids["finance_admin"], "superwagie", profile); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("legacy allowlist expanded: %v", e)
	}
	if _, e := s.Administer(ctx, authority, ids["finance_admin"], req); !errors.Is(e, billing.ErrConflict) {
		t.Fatalf("profile stale accepted: %v", e)
	}
	budget := billing.AdminOperation{Action: "budget.save", AccountID: account, ProductID: "superwagie", InstanceID: "default", FundingMode: "prepaid_cash", Budget: billing.UsageBudgetRequest{MaxTotal: "0", MaxPerCall: "0"}, Reason: "deny future calls"}
	out, e := s.Administer(ctx, authority, ids["finance_admin"], budget)
	if e != nil {
		t.Fatal(e)
	}
	if out.(billing.UsageBudget).Version != 1 {
		t.Fatal(out)
	}
	if _, e = s.Administer(ctx, authority, ids["finance_admin"], budget); !errors.Is(e, billing.ErrConflict) {
		t.Fatalf("budget stale accepted %v", e)
	}
	var actor string
	if e = pool.QueryRow(ctx, `SELECT updated_by FROM usage_budgets WHERE account_id=$1`, account).Scan(&actor); e != nil || actor != ids["finance_admin"] {
		t.Fatalf("actor impersonated %q %v", actor, e)
	}
	var owners int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM account_members WHERE account_id=$1 AND user_id=$2`, account, ids["finance_admin"]).Scan(&owners); e != nil || owners != 0 {
		t.Fatal("admin added to payer", e)
	}
	if _, e = s.Administer(ctx, authority, ids["finance_admin"], billing.AdminOperation{Action: "refund.review", AccountID: account, ID: "missing", Decision: "approve", Reason: "test"}); !errors.Is(e, billing.ErrUnsupported) {
		t.Fatalf("approval accepted %v", e)
	}
	offer := billing.Offer{ID: "admin-draft", ProductID: "agentwiki", Name: "Pure service", AccountKind: "both", Kind: "membership", Period: "month", Price: "1000000", MonthlyCredit: "1000000"}
	create := billing.AdminOperation{Action: "offer.save", ID: offer.ID, Offer: offer, Version: "0", Reason: "draft policy"}
	if _, e = s.Administer(ctx, authority, ids["finance_admin"], create); e != nil {
		t.Fatal(e)
	}
	v := revision(t, pool, "billing_offers", offer.ID)
	if _, e = s.Administer(ctx, authority, ids["finance_admin"], billing.AdminOperation{Action: "offer.publish", ID: offer.ID, Version: v, Reason: "publish policy"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Administer(ctx, authority, ids["finance_admin"], create); !errors.Is(e, billing.ErrConflict) {
		t.Fatalf("published edited %v", e)
	}
	v = revision(t, pool, "billing_offers", offer.ID)
	if _, e = s.Administer(ctx, authority, ids["finance_admin"], billing.AdminOperation{Action: "offer.retire", ID: offer.ID, Version: v, Reason: "retire policy"}); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `CREATE FUNCTION reject_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$; CREATE TRIGGER reject_admin_audit BEFORE INSERT ON platform_admin_audit FOR EACH ROW EXECUTE FUNCTION reject_admin_audit()`); e != nil {
		t.Fatal(e)
	}
	offer.ID = "rollback-draft"
	create.ID = offer.ID
	create.Offer = offer
	if _, e = s.Administer(ctx, authority, ids["finance_admin"], create); e == nil {
		t.Fatal("audit failure ignored")
	}
	var exists bool
	if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_offers WHERE id=$1)`, offer.ID).Scan(&exists); e != nil || exists {
		t.Fatal("mutation survived failed audit", e)
	}
}

func TestAdminRateMutationsWaitForActiveReservation(t *testing.T) {
	s, authority, pool, ids, _ := adminFixture(t)
	ctx := context.Background()
	rate := billing.Rate{ID: "admin-lock-rate", Model: "model", Asset: billing.CashAsset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "1"}, Output: billing.Rational{Numerator: "1", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}
	publish := billing.AdminOperation{Action: "rate.publish", ID: rate.ID, Rate: rate, Version: "0", Reason: "test"}
	if _, err := s.Administer(ctx, authority, ids["finance_admin"], publish); err != nil {
		t.Fatal(err)
	}
	retire := billing.AdminOperation{Action: "rate.retire", ID: rate.ID, Version: revision(t, pool, "billing_rates", rate.ID), Reason: "test"}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('unihub:billing-rate-catalog',0))`); err != nil {
		t.Fatal(err)
	}
	for _, action := range []billing.AdminOperation{retire, func() billing.AdminOperation { r := publish; r.ID = "admin-lock-next"; r.Rate.ID = r.ID; return r }()} {
		blocked, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		_, err := s.Administer(blocked, authority, ids["finance_admin"], action)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s crossed active reservation: %v", action.Action, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Administer(ctx, authority, ids["finance_admin"], retire); err != nil {
		t.Fatal(err)
	}
}
func TestPlatformFinanceLedgerAuthoritativeAndOutage(t *testing.T) {
	s, authority, pool, ids, account := adminFixture(t)
	ctx := context.Background()
	if _, e := s.AdminCashBalance(ctx, authority, ids["finance_admin"], account, ""); e == nil {
		t.Fatal("missing ledger returned balance")
	}
	base := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if base == "" {
		t.Skip("UNIHUB_TEST_LEDGER_URL unset")
	}
	l := ledger.NewFormance(base, fmt.Sprintf("platform-admin-test-%d", time.Now().UnixNano()), &http.Client{Timeout: 5 * time.Second})
	if e := l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	s = billing.New(pool, accounts.New(pool), l)
	if _, e := l.Post(ctx, ledger.Command{Reference: "test-admin-funding", Postings: []ledger.Posting{{Source: "world", Destination: "cash:" + account + ":unified", Asset: "CNY/6", Amount: 2345678}}}); e != nil {
		t.Fatal(e)
	}
	out, e := s.AdminCashBalance(ctx, authority, ids["finance_admin"], account, "")
	if e != nil || out["amount"] != "2345678" {
		t.Fatalf("authoritative read %v %v", out, e)
	}
	command := ledger.Command{Reference: "unihub:billing:bc-reconcile", Postings: []ledger.Posting{{Source: "world", Destination: "cash:" + account + ":unified", Asset: "CNY/6", Amount: 100}}}
	original, e := l.Post(ctx, command)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(command)
	if _, e = pool.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES('bc-reconcile',$1,$2,'test','original-key','original-digest',$3,'{}','UNKNOWN')`, account, ids["owner"], raw); e != nil {
		t.Fatal(e)
	}
	recovered, e := s.AdminReconcile(ctx, authority, ids["finance_admin"], "bc-reconcile", "UNKNOWN", "read original ledger result", "reconcile-1")
	if e != nil || recovered["status"] != "APPLIED" {
		t.Fatalf("reconciliation %v %v", recovered, e)
	}
	var financialActor, tid string
	if e = pool.QueryRow(ctx, `SELECT actor_id,ledger_transaction_id FROM billing_commands WHERE id='bc-reconcile'`).Scan(&financialActor, &tid); e != nil || financialActor != ids["owner"] || tid != original.ID {
		t.Fatal("financial command changed", e)
	}
	out, e = s.AdminCashBalance(ctx, authority, ids["finance_admin"], account, "")
	if e != nil || out["amount"] != "2345778" {
		t.Fatalf("duplicated command %v %v", out, e)
	}
	bad := ledger.NewFormance("http://127.0.0.1:1", "unavailable", &http.Client{Timeout: time.Second})
	s = billing.New(pool, accounts.New(pool), bad)
	if _, e = s.AdminCashBalance(ctx, authority, ids["finance_admin"], account, ""); e == nil {
		t.Fatal("ledger outage became zero")
	}
	command.Reference = "unihub:billing:bc-outage"
	raw, _ = json.Marshal(command)
	if _, e = pool.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES('bc-outage',$1,$2,'test','outage-key','digest',$3,'{}','UNKNOWN')`, account, ids["owner"], raw); e != nil {
		t.Fatal(e)
	}
	recovered, e = s.AdminReconcile(ctx, authority, ids["finance_admin"], "bc-outage", "UNKNOWN", "check uncertain original operation", "reconcile-2")
	if e != nil || recovered["status"] != "UNKNOWN" || recovered["reconciliation_pending"] != true {
		t.Fatalf("unknown forced to success %v %v", recovered, e)
	}
}
