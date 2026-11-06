package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func nativeFixture(t *testing.T) (fixture, string) {
	t.Helper()
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://native.test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	var l ledger.Ledger = &rateTestLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}}
	if base := os.Getenv("UNIHUB_TEST_LEDGER_URL"); base != "" {
		remote := ledger.NewFormance(base, fmt.Sprintf("native-key-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
		if e = remote.Ensure(ctx); e != nil {
			t.Fatal(e)
		}
		l = remote
	}
	s := New(p, a, l)
	s.ConfigureOperators([]string{u.ID})
	s.ConfigureTrustedServices([]string{"llmrouter"})
	s.ConfigureProductUsers(productusers.New(p, productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}}, "https://native.test", nil))
	profile := BillingProfile{ProductID: "superwagie", Usage: UsagePolicy{Enabled: true, FundingModes: []string{FundingPrepaidCash}, DefaultFundingMode: FundingPrepaidCash, Models: []string{"legacy-model"}}}
	if _, e = s.SaveProfile(ctx, u.ID, "superwagie", profile); e != nil {
		t.Fatal(e)
	}
	f := fixture{s: s, l: l, actor: u.ID, account: as[0].ID}
	cid := id("mc_")
	_, e = p.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version,credential_kind,instance_id,model_scope,pricing_source,credential_version,max_total,max_per_call) VALUES($1,$1,$2,$3,'superwagie','[]','',$4,'prepaid_cash','CNY/6',1,'api_key','default','llmrouter_available','llmrouter_native',2,'100','50')`, cid, u.ID, f.account, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	fundCash(t, f, "llmrouter", 100)
	fundCash(t, f, "", 1000)
	return f, cid
}

func nativeRequest(f fixture, cid, call, quota string) NativeReserveRequest {
	return NativeReserveRequest{ReserveRequest: ReserveRequest{CallID: call, ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", CredentialID: cid, RequestDigest: "digest-" + call}, Model: "new-native-model", WireOperation: "images.generations.create", InstanceID: "default", CredentialVersion: 2, Quote: NativeQuote{PricingVersion: "lr-native-v1", PricingDigest: strings.Repeat("a", 64), BillingModel: "native-billing-alias", WireOperation: "images.generations.create", RouteID: "0", ChannelID: "7", Group: "default", NativeQuotaMax: quota, Conversion: NativeConversion{Version: "lr-quota-usd-cny-v1", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}, Asset: CashAsset, MaxAmount: quota}}
}

func TestNativeCallsKeepManagedFundingAndRequireTopupBeforeActualCharge(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	r := nativeRequest(f, cid, "native-one", "30")
	call, e := f.s.ReserveNative(ctx, "llmrouter", r)
	if e != nil {
		t.Fatal(e)
	}
	if call.FundingProductID != "llmrouter" || call.NativePricing == nil || call.CredentialID != cid || call.Rate.ID != "" || call.BillingProfileVersion != 1 {
		t.Fatalf("wrong immutable snapshot %+v", call)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 70)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000)
	if _, e = f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	changed := r
	changed.Quote.Group = "foreign"
	if _, e = f.s.ReserveNative(ctx, "llmrouter", changed); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed quote replay %v", e)
	}
	if _, e = f.s.Settle(ctx, "llmrouter", SettlementRequest{CallID: r.CallID, Usage: &Usage{Input: "1"}}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("old settlement accepted native call %v", e)
	}
	settle := NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "40", Amount: "40"}
	if _, e = f.s.SettleNative(ctx, "llmrouter", settle); !errors.Is(e, ErrPending) {
		t.Fatalf("over-held settled %v", e)
	}
	call, e = f.s.GetCall(ctx, "llmrouter", r.CallID)
	if e != nil || call.Status != "RESERVED" || call.Charged != "0" {
		t.Fatalf("pending obligation lost %+v %v", call, e)
	}
	if _, e = f.s.TopUp(ctx, "llmrouter", r.CallID, "10", "actual"); e != nil {
		t.Fatal(e)
	}
	call, e = f.s.SettleNative(ctx, "llmrouter", settle)
	if e != nil || call.Charged != "40" || call.PlatformDifference != "0" {
		t.Fatalf("settle %+v %v", call, e)
	}
	if _, e = f.s.SettleNative(ctx, "llmrouter", settle); e != nil {
		t.Fatal(e)
	}
	settle.Amount = "39"
	if _, e = f.s.SettleNative(ctx, "llmrouter", settle); !errors.Is(e, ErrInvalid) {
		t.Fatalf("conversion mismatch %v", e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 60)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000)
	cashBalance(t, f, "commercial:revenue:llmrouter", 40)
	held, used, e := UsageTotalsOn(ctx, f.s.pool, f.account, "superwagie", "default", CashAsset, cid)
	if e != nil || held != "0" || used != "40" {
		t.Fatalf("key totals %s %s %v", held, used, e)
	}
}

func TestNativeFreeCallHasEvidenceWithoutLedgerPosting(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	r := nativeRequest(f, cid, "native-free", "0")
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	call, e := f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "0", Amount: "0"})
	if e != nil || call.Status != "SETTLED" || call.NativeQuota != "0" {
		t.Fatalf("free receipt %+v %v", call, e)
	}
	var posts int
	if e = f.s.pool.QueryRow(ctx, `SELECT COALESCE(sum(jsonb_array_length(command->'postings')),0) FROM billing_commands WHERE effect#>>'{call,call_id}'=$1`, r.CallID).Scan(&posts); e != nil || posts != 0 {
		t.Fatalf("zero posts=%d err=%v", posts, e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
}

func TestNativeReserveRejectsScopeOwnershipExpiryAndBudgetViolations(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		mutate func(*NativeReserveRequest)
	}{
		{"version", func(r *NativeReserveRequest) { r.CredentialVersion = 1 }},
		{"actor", func(r *NativeReserveRequest) { r.ActorID = "other" }},
		{"instance", func(r *NativeReserveRequest) { r.InstanceID = "other" }},
		{"product", func(r *NativeReserveRequest) { r.OriginProductID = "agentwiki" }},
		{"budget", func(r *NativeReserveRequest) { r.Quote.NativeQuotaMax = "51"; r.Quote.MaxAmount = "51" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := nativeRequest(f, cid, test.name, "1")
			test.mutate(&r)
			if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e == nil {
				t.Fatal("unauthorized native hold")
			}
		})
	}
	if _, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET expires_at=now()-interval '1 second' WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.ReserveNative(ctx, "llmrouter", nativeRequest(f, cid, "expired", "1")); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("expired %v", e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
}

func TestNativeUncertainHoldRemainsReservedUntilRecovery(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	r := nativeRequest(f, cid, "native-lost", "40")
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); !errors.Is(e, ErrPending) {
		t.Fatalf("missing pending %v", e)
	}
	held, _, e := UsageTotalsOn(ctx, f.s.pool, f.account, "superwagie", "default", CashAsset, cid)
	if e != nil || held != "40" {
		t.Fatalf("lost exposure %s %v", held, e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 60)
	if _, e = f.s.Release(ctx, "llmrouter", r.CallID, "provider_not_dispatched"); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
}

type nativePausedLedger struct {
	ledger.Ledger
	entered chan struct{}
	release chan struct{}
}

func (l *nativePausedLedger) Balance(ctx context.Context, address, asset string) (int64, error) {
	close(l.entered)
	select {
	case <-l.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return l.Ledger.Balance(ctx, address, asset)
}

func TestNativeAdmissionSerializesAdministrativeRevocation(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	paused := &nativePausedLedger{Ledger: f.l, entered: make(chan struct{}), release: make(chan struct{})}
	f.s.ledger = paused
	done := make(chan error, 1)
	go func() {
		_, e := f.s.ReserveNative(ctx, "llmrouter", nativeRequest(f, cid, "lock-call", "10"))
		done <- e
	}()
	select {
	case <-paused.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	tx, e := f.s.pool.Begin(ctx)
	if e != nil {
		close(paused.release)
		t.Fatal(e)
	}
	defer database.Rollback(tx)
	var locked bool
	e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`).Scan(&locked)
	close(paused.release)
	if e != nil || locked {
		t.Fatalf("revocation bypassed admission lock: %v %v", locked, e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `UPDATE model_credentials SET revoked_at=now() WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	f.s.ledger = f.l
	if _, e = f.s.ReserveNative(ctx, "llmrouter", nativeRequest(f, cid, "after-revoke", "1")); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("revoked key admitted %v", e)
	}
	// A dispatched obligation is still settleable against its original payer.
	if _, e = f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{CallID: "lock-call", PricingDigest: strings.Repeat("a", 64), NativeQuota: "7", Amount: "7"}); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 93)
}

func TestNativeConcurrentHoldsCannotOverspendKeyBudget(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	if _, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET max_total='50' WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 2)
	for _, call := range []string{"concurrent-one", "concurrent-two"} {
		go func(call string) {
			_, e := f.s.ReserveNative(ctx, "llmrouter", nativeRequest(f, cid, call, "40"))
			done <- e
		}(call)
	}
	passed := 0
	for i := 0; i < 2; i++ {
		if e := <-done; e == nil {
			passed++
		} else if !errors.Is(e, accounts.ErrForbidden) {
			t.Fatal(e)
		}
	}
	if passed != 1 {
		t.Fatalf("expected exactly one budget hold, got %d", passed)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 60)
}

func TestNativeCallRetainsTeamPayerAndProductWallet(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	personal := f.account
	a := accounts.New(f.s.pool)
	team, e := a.CreateTeam(ctx, f.actor, "Native payer team")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.pool.Exec(ctx, `UPDATE model_credentials SET account_id=$2 WHERE id=$1`, cid, team.ID); e != nil {
		t.Fatal(e)
	}
	f.account = team.ID
	fundCash(t, f, "llmrouter", 100)
	fundCash(t, f, "", 1000)
	r := nativeRequest(f, cid, "team-native", "20")
	if _, e = f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "14", Amount: "14"}); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(team.ID, "llmrouter"), 86)
	cashBalance(t, f, wallet.Address(team.ID, ""), 1000)
	cashBalance(t, f, wallet.Address(personal, "llmrouter"), 100)
}

func TestNativeUsageReportsRetainChargeWithoutInventingTokenCoverage(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	for i, usage := range []*Usage{{}, {Input: "2", Output: "1", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}} {
		r := nativeRequest(f, cid, fmt.Sprintf("native-report-%d", i), "10")
		if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "7", Amount: "7", Usage: usage}); e != nil {
			t.Fatal(e)
		}
	}
	summary, e := f.s.QueryUsageSummary(ctx, f.actor, f.account, CallFilter{OriginProductID: "superwagie", Asset: CashAsset})
	if e != nil || summary.Charged != "14" || summary.Usage.Input != "2" || summary.UsageCoverage.LegacyTokenCalls != "1" || summary.UsageComplete {
		t.Fatalf("native summary %+v %v", summary, e)
	}
	view, e := f.s.QueryCall(ctx, f.actor, f.account, "native-report-0", "superwagie")
	if e != nil || view.PriceOwner != "product" || view.PriceVersion != "lr-native-v1" || view.Rate != nil || view.UsageStatus != "unavailable" {
		t.Fatalf("native projection %+v %v", view, e)
	}
}
