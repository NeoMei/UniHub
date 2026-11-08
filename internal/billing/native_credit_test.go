package billing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

// Ratios are test fixtures, never deployed tariffs.
func nativeCreditProfile(t *testing.T, version int64, tariff, numerator string) BillingProfile {
	t.Helper()
	p, _ := BillingPreset("superwagie", "hybrid")
	p.Version = version
	p.Usage.FundingModes = []string{FundingCredits, FundingPrepaidCash}
	raw, _ := json.Marshal(p)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["native_credit_conversion"] = map[string]string{"version": tariff, "numerator": numerator, "denominator": "2", "rounding": "lr-cumulative-ceil-v1"}
	raw, _ = json.Marshal([]any{fields})
	parsed, e := ParseBillingProfiles(raw)
	if e != nil {
		t.Fatal(e)
	}
	return parsed[0]
}

func nativeCreditFixture(t *testing.T) (fixture, string, NativeConversion) {
	t.Helper()
	f, cid := nativeFixture(t)
	ctx := context.Background()
	p := nativeCreditProfile(t, 1, "test-points-v1", "3")
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", p); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET funding_mode='credits',asset='CREDIT/6',pricing_source='llmrouter_native_credit',billing_profile_version=2 WHERE id=$1`, cid)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	sub := Subscription{ID: id("sub_"), AccountID: f.account, ProductID: "superwagie", Status: "active", PaidThrough: now.Add(24 * time.Hour)}
	_, e = f.s.pool.Exec(ctx, `INSERT INTO billing_subscriptions(id,account_id,product_id,snapshot) VALUES($1,$2,$3,$4)`, sub.ID, sub.AccountID, sub.ProductID, encode(sub))
	if e != nil {
		t.Fatal(e)
	}
	orderID := id("order_")
	_, e = f.s.pool.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,$1,$1,'{}')`, orderID, f.account, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	for i, duration := range []time.Duration{time.Hour, 2 * time.Hour} {
		b := Batch{ID: id("batch_"), AccountID: f.account, Kind: "base", Original: "20", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(duration), CreatedAt: now, SourceKey: []string{"points-first", "points-second"}[i]}
		_, e = f.s.pool.Exec(ctx, `INSERT INTO billing_batches(id,account_id,source_key,snapshot,order_id) VALUES($1,$2,$3,$4,$5)`, b.ID, b.AccountID, b.SourceKey, encode(b), orderID)
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.l.Post(ctx, ledger.Command{Reference: b.ID, Postings: []ledger.Posting{{Source: "world", Destination: BatchAddress(b.ID, "available"), Asset: CreditAsset, Amount: 20}}})
		if e != nil {
			t.Fatal(e)
		}
	}
	return f, cid, NativeConversion{Version: "test-points-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
}

func creditNativeRequest(f fixture, cid, call, quota string, c NativeConversion) NativeReserveRequest {
	r := nativeRequest(f, cid, call, quota)
	r.Quote.Asset = CreditAsset
	r.Quote.PricingVersion = "lr-native-credit-v1"
	r.Quote.Conversion = c
	r.Quote.MaxAmount, _ = nativeAmount(quota, c)
	return r
}

func TestNativeCreditConversionQuoteValidation(t *testing.T) {
	c := NativeConversion{Version: "test-points-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
	for _, tc := range []struct{ quota, want string }{{"0", "0"}, {"1", "2"}, {"3", "5"}, {"4", "6"}} {
		n, e := nativeAmount(tc.quota, c)
		if e != nil || n != tc.want {
			t.Fatalf("%s => %s %v", tc.quota, n, e)
		}
	}
	r := creditNativeRequest(fixture{}, "", "quote", "3", c)
	if e := validateNativeQuote(r.Quote, r.WireOperation); e != nil {
		t.Fatal(e)
	}
	r.Quote.Asset = CashAsset
	if e := validateNativeQuote(r.Quote, r.WireOperation); e == nil {
		t.Fatal("points ratio accepted as CNY")
	}
}

func TestNativeCreditLifecycleFreezesConversionAndExpiresRemainder(t *testing.T) {
	f, cid, c := nativeCreditFixture(t)
	ctx := context.Background()
	r := creditNativeRequest(f, cid, "credit-lifecycle", "17", c) // ceil(25.5)=26, across both sources.
	call, e := f.s.ReserveNative(ctx, "llmrouter", r)
	if e != nil {
		t.Fatal(e)
	}
	if call.ChargeAsset != CreditAsset || call.FundingMode != FundingCredits || call.Held != "26" || len(call.Allocations) != 2 || call.Allocations[0].Amount != "20" || call.CashHoldAddress != "" {
		t.Fatalf("bad snapshot %+v", call)
	}
	p := nativeCreditProfile(t, 2, "test-points-v2", "9")
	if _, e = f.s.SaveProfile(ctx, f.actor, "superwagie", p); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.ReserveNative(ctx, "llmrouter", creditNativeRequest(f, cid, "stale-tariff", "1", c)); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("stale tariff accepted %v", e)
	}
	f.s.now = func() time.Time { return time.Now().Add(90 * time.Minute) }
	req := NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "7", Amount: "11", ProviderCost: "17"}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e = f.s.SettleNative(ctx, "llmrouter", req); !errors.Is(e, ErrPending) {
		t.Fatalf("unknown settle %v", e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	call, e = f.s.SettleNative(ctx, "llmrouter", req)
	if e != nil || call.Charged != "11" || call.ProviderCost != "17" {
		t.Fatalf("settle %+v %v", call, e)
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil || bs[0].Consumed != "11" || bs[0].Expired != "9" || bs[0].Reserved != "0" || bs[1].Available != "20" {
		t.Fatalf("sources %+v %v", bs, e)
	}
	held, used, e := UsageTotalsOn(ctx, f.s.pool, f.account, "superwagie", "default", CreditAsset, cid)
	if e != nil || held != "0" || used != "11" {
		t.Fatalf("totals %s/%s %v", held, used, e)
	}
	report, e := f.s.QueryUsageSummary(ctx, f.actor, f.account, CallFilter{OriginProductID: "superwagie", Asset: CreditAsset})
	if e != nil || report.Charged != "11" {
		t.Fatalf("report %+v %v", report, e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000)
}

func TestNativeCreditMembershipBudgetAndNoCashFallback(t *testing.T) {
	f, cid, c := nativeCreditFixture(t)
	ctx := context.Background()
	// A forged cheaper tariff and the legacy cash quote cannot authorize points.
	cheap := c
	cheap.Numerator = "1"
	for _, r := range []NativeReserveRequest{creditNativeRequest(f, cid, "cheap", "1", cheap), nativeRequest(f, cid, "wrong-asset", "1"), creditNativeRequest(f, cid, "over-budget", "34", c), creditNativeRequest(f, cid, "insufficient-credit", "30", c)} {
		if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e == nil {
			t.Fatalf("admitted %s", r.CallID)
		}
	}
	r := creditNativeRequest(f, cid, "credit-release", "10", c)
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.TopUp(ctx, "llmrouter", r.CallID, "5", "extend"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Release(ctx, "llmrouter", r.CallID, "not_dispatched"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.ReserveNative(ctx, "llmrouter", creditNativeRequest(f, cid, "credit-free", "0", c)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{status}','"canceled"') WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.ReserveNative(ctx, "llmrouter", creditNativeRequest(f, cid, "no-membership", "1", c)); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("membership %v", e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
}

func TestNativeCreditTariffVersionIsImmutableAcrossHistory(t *testing.T) {
	f, _, _ := nativeCreditFixture(t)
	ctx := context.Background()
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", nativeCreditProfile(t, 2, "test-points-v1", "5")); !errors.Is(e, ErrConflict) {
		t.Fatalf("tariff version changed %v", e)
	}
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", nativeCreditProfile(t, 2, "test-points-v2", "5")); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", nativeCreditProfile(t, 3, "test-points-v1", "7")); !errors.Is(e, ErrConflict) {
		t.Fatalf("historical tariff changed %v", e)
	}
}

func TestNativeCreditFrozenSourcesDoNotAuthorizeCashFallback(t *testing.T) {
	f, cid, c := nativeCreditFixture(t)
	ctx := context.Background()
	if _, e := f.s.pool.Exec(ctx, `UPDATE billing_batches SET snapshot=jsonb_set(snapshot,'{frozen}','true') WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.ReserveNative(ctx, "llmrouter", creditNativeRequest(f, cid, "frozen-points", "1", c)); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("frozen source accepted %v", e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
}

func TestNativeCreditOnlyMembershipKeepsLegacyCashUsable(t *testing.T) {
	f, cid, c := nativeCreditFixture(t)
	ctx := context.Background()
	cashID := id("mc_")
	_, e := f.s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version,credential_kind,instance_id,model_scope,pricing_source,credential_version,max_total,max_per_call) SELECT $2,$2,actor_id,account_id,origin_product_id,models,task_id,expires_at,'prepaid_cash','CNY/6',1,credential_kind,instance_id,model_scope,'llmrouter_native',credential_version,max_total,max_per_call FROM model_credentials WHERE id=$1`, cid, cashID)
	if e != nil {
		t.Fatal(e)
	}
	p := nativeCreditProfile(t, 2, "test-points-v1", "3")
	p.Usage.RequireSubscription = false
	p.Usage.RequireSubscriptionForCredits = true
	p.Usage.DefaultFundingMode = FundingPrepaidCash
	if _, e = f.s.SaveProfile(ctx, f.actor, "superwagie", p); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{status}','"canceled"') WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	cash, e := f.s.ReserveNative(ctx, "llmrouter", nativeRequest(f, cashID, "mixed-old-cash", "5"))
	if e != nil || cash.ChargeAsset != CashAsset {
		t.Fatalf("old cash %+v %v", cash, e)
	}
	if _, e = f.s.ReserveNative(ctx, "llmrouter", creditNativeRequest(f, cid, "mixed-credit-denied", "5", c)); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("point membership bypass %v", e)
	}
	if _, e = f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{status}','"active"') WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	points, e := f.s.ReserveNative(ctx, "llmrouter", creditNativeRequest(f, cid, "mixed-credit-member", "5", c))
	if e != nil || points.ChargeAsset != CreditAsset || points.Held != "8" {
		t.Fatalf("member points %+v %v", points, e)
	}
	// Explicit global membership retains its existing semantics, including cash.
	p.Version = 3
	p.Usage.RequireSubscription = true
	if _, e = f.s.SaveProfile(ctx, f.actor, "superwagie", p); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{status}','"canceled"') WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.ReserveNative(ctx, "llmrouter", nativeRequest(f, cashID, "mixed-global-cash-denied", "1")); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("global membership bypass %v", e)
	}
}
