package billing

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/productusers"
	"unihub/internal/wallet"
)

func seedProductCreditBatch(t *testing.T, f fixture, b Batch, scope string) string {
	t.Helper()
	ctx := context.Background()
	orderID := id("order_")
	n, e := integer(b.Original, true)
	if e != nil {
		t.Fatal(e)
	}
	compute := (n + 9) / 10
	order := Order{ID: orderID, AccountID: f.account, ActorID: f.actor, ProductID: b.ProductID, CreditScope: scope, Amount: str(compute), ServiceAmount: "0", ComputeAmount: str(compute), PaymentStatus: "PAID", FulfillmentStatus: "FULFILLED"}
	if _, e := f.s.pool.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,$1,$1,$4)`, orderID, f.account, f.actor, encode(order)); e != nil {
		t.Fatal(e)
	}
	b.ID, b.AccountID, b.SourceKey, b.OrderID = id("batch_"), f.account, id("grant_"), orderID
	scopeJSON, _ := json.Marshal(scope)
	if _, e := f.s.pool.Exec(ctx, `INSERT INTO billing_batches(id,account_id,source_key,snapshot,order_id) VALUES($1,$2,$3,jsonb_set($4::jsonb,'{credit_scope}',$5::jsonb),$6)`, b.ID, b.AccountID, b.SourceKey, encode(b), scopeJSON, orderID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.l.Post(ctx, ledger.Command{Reference: b.ID, Postings: []ledger.Posting{{Source: "world", Destination: BatchAddress(b.ID, "available"), Asset: CreditAsset, Amount: n}, {Source: "world", Destination: productComputePrepay(order), Asset: CashAsset, Amount: compute}}}); e != nil {
		t.Fatal(e)
	}
	return b.ID
}

func TestLegacyNativeCreditCannotReserveProductGrant(t *testing.T) {
	f, cid, conversion := nativeCreditFixture(t)
	now := time.Now().UTC()
	product := seedProductCreditBatch(t, f, Batch{ProductID: "superwagie", Kind: "base", Original: "20", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Minute), CreatedAt: now}, "product_v1")
	call, e := f.s.ReserveNative(context.Background(), "llmrouter", creditNativeRequest(f, cid, "legacy-credit-excludes-product", "3", conversion))
	if e != nil || len(call.Allocations) != 1 {
		t.Fatalf("expected historical pool hold, got %+v, %v", call, e)
	}
	if call.Allocations[0].BatchID == product {
		t.Fatalf("legacy call reserved a product grant %+v", call.Allocations)
	}
}

func exactProductCreditTestConversion() NativeConversion {
	return NativeConversion{Version: "product-credit-fixture", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
}

func TestProductCreditReserveUsesOnlyAuthenticatedProductAndPrioritizesSubscription(t *testing.T) {
	f, cid, legacyConversion := nativeCreditFixture(t)
	conversion := exactProductCreditTestConversion()
	ctx := context.Background()
	now := time.Now().UTC()
	p := nativeCreditProfile(t, 2, conversion.Version, conversion.Numerator)
	p.NativeCreditConversion = &conversion
	p.CreditScope = ProductCreditScopeV1
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", p); e != nil {
		t.Fatal(e)
	}
	legacyCall := creditNativeRequest(f, cid, "legacy-credit-after-opt-in", "3", legacyConversion)
	if old, e := f.s.ReserveNative(ctx, "llmrouter", legacyCall); e != nil || old.Held != "5" || old.CreditScope != "" {
		t.Fatalf("scoped profile changed the original legacy tariff: %+v %v", old, e)
	}
	if _, e := f.s.Release(ctx, "llmrouter", legacyCall.CallID, "fixture-complete"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET credit_scope='product_v1' WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	seedProductCreditBatch(t, f, Batch{ProductID: "llmrouter", Kind: "base", Original: "70", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Minute), CreatedAt: now}, ProductCreditScopeV1)
	addon := seedProductCreditBatch(t, f, Batch{ProductID: "superwagie", Kind: "addon", Original: "7", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Minute), CreatedAt: now}, ProductCreditScopeV1)
	base := seedProductCreditBatch(t, f, Batch{ProductID: "superwagie", Kind: "base", Original: "6", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), CreatedAt: now}, ProductCreditScopeV1)
	r := creditNativeRequest(f, cid, "product-holds", "11", conversion)
	r.CreditScope = ProductCreditScopeV1
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("accepted without a trusted source: %v", e)
	}
	r.SourceVerified = true
	call, e := f.s.ReserveNative(ctx, "llmrouter", r)
	if e != nil {
		t.Fatal(e)
	}
	if call.CreditScope != ProductCreditScopeV1 || call.FundingProductID != "superwagie" || !reflect.DeepEqual(call.Allocations, []Allocation{{base, "6"}, {addon, "5"}}) {
		t.Fatalf("incorrect product hold: %+v", call)
	}
	if _, e = f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	r.CallID = "product-cannot-overlap"
	if _, e = f.s.ReserveNative(ctx, "llmrouter", r); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("used credits of another contract: %v", e)
	}
	f.s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, e = f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{CallID: "product-holds", PricingDigest: r.Quote.PricingDigest, NativeQuota: "5", Amount: "5"}); e != nil {
		t.Fatal(e)
	}
	w, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	if e != nil || w.Available != "1" || w.Reserved != "0" || !w.ActiveMembership || len(w.Batches) != 2 || w.Batches[0].Consumed != "5" || w.Batches[1].Expired != "5" {
		t.Fatalf("incorrect product release after expiration: %+v, %v", w, e)
	}
}

func TestProductOfferRejectsMissingSplitAndWrongTerm(t *testing.T) {
	offer := Offer{ID: "product-addon", ProductID: "superwagie", Name: "Addon", AccountKind: "both", Kind: "addon", Price: "1000000", MonthlyCredit: "9999999", ValidityMonths: 6}
	raw, _ := json.Marshal(offer)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["credit_scope"] = "product_v1"
	raw, _ = json.Marshal(fields)
	_ = json.Unmarshal(raw, &offer)
	if e := validateOffer(offer); !errors.Is(e, ErrInvalid) {
		t.Fatalf("accepted product offer with incorrect value, expiry, and missing allocation: %v", e)
	}
}

func publishProductCreditOffers(t *testing.T, f fixture, period string) (Offer, Offer) {
	t.Helper()
	ctx := context.Background()
	p, e := f.s.Profile(ctx, "superwagie")
	if e != nil {
		t.Fatal(e)
	}
	p.Subscription.Enabled = true
	p.Addons.Enabled = true
	p.IncludedCredits.Enabled = true
	p.Usage.Enabled = true
	p.Usage.DefaultFundingMode = FundingCredits
	p.Usage.FundingModes = []string{FundingCredits}
	p.Usage.RequireSubscriptionForCredits = true
	p.CreditScope = ProductCreditScopeV1
	conversion := exactProductCreditTestConversion()
	p.NativeCreditConversion = &conversion
	if _, e = f.s.SaveProfile(ctx, f.actor, "superwagie", p); e != nil {
		t.Fatal(e)
	}
	f.s.ConfigureCheckoutReadiness(func(ctx context.Context, _, _, product string) (NativeCreditReadiness, error) {
		return f.s.NativeCreditsReadiness(ctx, product)
	})
	membership := Offer{ID: "scoped-membership", ProductID: "superwagie", Name: "Scoped membership", AccountKind: "personal", Kind: "membership", Period: period, Price: "3000000", MonthlyCredit: "10000000", CreditScope: ProductCreditScopeV1, ServiceAmount: "2000000", ComputeAmount: "1000000", RefundRule: "unused_only_v1"}
	if period == "year" {
		membership.Price, membership.ServiceAmount, membership.ComputeAmount = "36000000", "24000000", "12000000"
	}
	addon := Offer{ID: "scoped-addon", ProductID: "superwagie", Name: "Scoped add-on", AccountKind: "personal", Kind: "addon", Price: "2000000", MonthlyCredit: "20000000", ValidityMonths: 12, CreditScope: ProductCreditScopeV1, ServiceAmount: "0", ComputeAmount: "2000000", RefundRule: "unused_only_v1"}
	for _, o := range []Offer{membership, addon} {
		if _, e := f.s.SaveOffer(ctx, f.actor, o); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.PublishOffer(ctx, f.actor, o.ID); e != nil {
			t.Fatal(e)
		}
	}
	return membership, addon
}

func TestProductAnnualMembershipFundsTwelveGrantsAndAddonSurvivesLapse(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	start := f.s.now()
	annual, addon := publishProductCreditOffers(t, f, "year")
	order := f.buy(t, annual.ID, "annual-product")
	replayed := f.buy(t, annual.ID, "annual-product")
	if replayed.ID != order.ID || order.CreditScope != ProductCreditScopeV1 || order.ServiceAmount != "24000000" || order.ComputeAmount != "12000000" {
		t.Fatalf("order allocation not frozen: %+v and %+v", order, replayed)
	}
	cashBalance(t, f, "commercial:revenue:superwagie", 24000000)
	cashBalance(t, f, productComputePrepay(order), 12000000)
	for month := 1; month < 12; month++ {
		now := MonthAnchor(start, month)
		f.s.now = func() time.Time { return now }
		if _, e := f.s.Maintain(ctx, now); e != nil {
			t.Fatal(e)
		}
	}
	addonOrder := f.buy(t, addon.ID, "late-annual-addon")
	if !addonOrder.End.Equal(MonthAnchor(MonthAnchor(start, 11), 12)) {
		t.Fatalf("wrong add-on expiry: %v", addonOrder.End)
	}
	now := MonthAnchor(start, 12)
	f.s.now = func() time.Time { return now }
	if _, e := f.s.Maintain(ctx, now); e != nil {
		t.Fatal(e)
	}
	wallet, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	if e != nil || wallet.ActiveMembership || wallet.Available != "0" || len(wallet.Batches) != 13 {
		t.Fatalf("expected 12 grants plus one locked add-on: %+v %v", wallet, e)
	}
	if last := wallet.Batches[12]; last.Kind != "addon" || last.Available != "20000000" || !last.ExpiresAt.Equal(addonOrder.End) {
		t.Fatalf("add-on lost original validity: %+v", last)
	}
	if _, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: addon.ID, Key: "while-expired"}); !errors.Is(e, ErrSubscriptionRequired) {
		t.Fatalf("expired membership allowed a new add-on: %v", e)
	}
	f.buy(t, annual.ID, "resume-after-lapse")
	wallet, e = f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	if e != nil || !wallet.ActiveMembership || wallet.Available != "30000000" || !wallet.Batches[13].ExpiresAt.Equal(addonOrder.End) {
		t.Fatalf("rejoining failed to resume remaining credits: %+v %v", wallet, e)
	}
}

func TestProductOrderRefundRestoresWalletAndComputeSplit(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	membership, _ := publishProductCreditOffers(t, f, "month")
	fundCash(t, f, "superwagie", 2200000)
	order := f.buy(t, membership.ID, "scoped-alloc-refund")
	cashBalance(t, f, "commercial:revenue:superwagie", 2000000)
	cashBalance(t, f, productComputePrepay(order), 1000000)
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 2200000)
	cashBalance(t, f, wallet.Address(f.account, ""), 997000000)
	for range 2 {
		out, e := f.s.RequestRefund(ctx, f.actor, f.account, order.ID, "original-scoped-refund")
		if e != nil || out.RefundStatus != "REFUNDED" {
			t.Fatalf("refund or reuse failed: %+v %v", out, e)
		}
	}
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 2200000)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000000000)
	cashBalance(t, f, "commercial:revenue:superwagie", 0)
	cashBalance(t, f, productComputePrepay(order), 0)
}

func TestProductComputeReconcilesFractionalCreditOnceOnRecoveredSettlement(t *testing.T) {
	f, cid, _ := nativeCreditFixture(t)
	conversion := exactProductCreditTestConversion()
	ctx := context.Background()
	membership, _ := publishProductCreditOffers(t, f, "month")
	if _, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET credit_scope='product_v1' WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.pool.Exec(ctx, `DELETE FROM billing_subscriptions WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	fundCash(t, f, "", 3000000)
	order := f.buy(t, membership.ID, "scoped-recognition")
	for _, sample := range []struct{ id, quota, charge string }{{"first-fraction", "5", "5"}, {"second-fraction", "6", "6"}} {
		r := creditNativeRequest(f, cid, sample.id, sample.quota, conversion)
		r.CreditScope, r.SourceVerified = ProductCreditScopeV1, true
		if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
			t.Fatal(e)
		}
		settlement := NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: sample.quota, Amount: sample.charge}
		if sample.id == "second-fraction" {
			f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
			if _, e := f.s.SettleNative(ctx, "llmrouter", settlement); !errors.Is(e, ErrPending) {
				t.Fatalf("expected uncertain response: %v", e)
			}
			if _, e := f.s.Recover(ctx); e != nil {
				t.Fatal(e)
			}
		}
		for range 2 {
			receipt, e := f.s.SettleNative(ctx, "llmrouter", settlement)
			if e != nil || receipt.Charged != sample.charge || receipt.ProviderCost != "" {
				t.Fatalf("bad replay or estimate fabricated as supplier cost: %+v %v", receipt, e)
			}
		}
	}
	cashBalance(t, f, productComputePrepay(order), 999999)
	cashBalance(t, f, "commercial:compute_recognized:superwagie", 1)
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 0)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000)
}

func TestProductCreditAccountSeparatesTwoAuthenticatedOrigins(t *testing.T) {
	f, firstKey, _ := nativeCreditFixture(t)
	conversion := exactProductCreditTestConversion()
	ctx := context.Background()
	publishProductCreditOffers(t, f, "month")
	p, e := f.s.Profile(ctx, "superwagie")
	if e != nil {
		t.Fatal(e)
	}
	p.ProductID, p.Version = "agentwiki", 0
	if _, e = f.s.SaveProfile(ctx, f.actor, p.ProductID, p); e != nil {
		t.Fatal(e)
	}
	registry := productusers.Registry{}
	for _, product := range []string{"superwagie", "agentwiki"} {
		registry[product+"/default"] = productusers.Manifest{ProductID: product, InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}
	}
	f.s.ConfigureProductUsers(productusers.New(f.s.pool, registry, "https://products.test", nil))
	secondKey := id("mc_")
	if _, e = f.s.pool.Exec(ctx, `UPDATE model_credentials SET credit_scope='product_v1' WHERE id=$1`, firstKey); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version,credential_kind,instance_id,model_scope,pricing_source,credential_version,credit_scope)
		SELECT $2,$2,actor_id,account_id,'agentwiki',models,task_id,expires_at,funding_mode,asset,1,credential_kind,instance_id,model_scope,pricing_source,credential_version,credit_scope FROM model_credentials WHERE id=$1`, firstKey, secondKey); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	sub := Subscription{ID: id("sub_"), AccountID: f.account, ProductID: "agentwiki", Status: "active", PaidThrough: now.Add(time.Hour)}
	if _, e = f.s.pool.Exec(ctx, `INSERT INTO billing_subscriptions(id,account_id,product_id,snapshot) VALUES($1,$2,$3,$4)`, sub.ID, f.account, sub.ProductID, encode(sub)); e != nil {
		t.Fatal(e)
	}
	firstBatch := seedProductCreditBatch(t, f, Batch{ProductID: "superwagie", Kind: "base", Original: "6", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), CreatedAt: now}, ProductCreditScopeV1)
	secondBatch := seedProductCreditBatch(t, f, Batch{ProductID: "agentwiki", Kind: "base", Original: "8", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), CreatedAt: now}, ProductCreditScopeV1)
	first := creditNativeRequest(f, firstKey, "product-account-first", "6", conversion)
	first.CreditScope, first.SourceVerified = ProductCreditScopeV1, true
	second := first
	second.OriginProductID, second.CallID = "agentwiki", "product-account-second"
	if _, e = f.s.ReserveNative(ctx, "llmrouter", second); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("first product authority funded a different origin: %v", e)
	}
	second.CredentialID = secondKey
	for _, candidate := range []struct {
		request NativeReserveRequest
		batchID string
	}{{first, firstBatch}, {second, secondBatch}} {
		call, e := f.s.ReserveNative(ctx, "llmrouter", candidate.request)
		if e != nil || !reflect.DeepEqual(call.Allocations, []Allocation{{candidate.batchID, "6"}}) {
			t.Fatalf("product failed to reserve its own points: %+v %v", call, e)
		}
	}
}

func TestExternalProductCheckoutRefundReversesOriginalCashAllocation(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	membership, _ := publishProductCreditOffers(t, f, "month")
	checkout, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: membership.ID, Provider: "wechat", Key: "scoped-external"})
	if e != nil {
		t.Fatal(e)
	}
	p.result = payments.Result{OrderID: checkout.ID, TransactionID: "scoped-external-provider", Amount: checkout.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	for range 2 {
		checkout, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, checkout.ID)
		if e != nil || checkout.FulfillmentStatus != "FULFILLED" {
			t.Fatalf("payment reconciliation: %+v %v", checkout, e)
		}
	}
	order, e := load[Order](ctx, f.s.pool, `SELECT snapshot FROM billing_orders WHERE id=$1`, checkout.OrderID)
	if e != nil || order.CreditScope != ProductCreditScopeV1 {
		t.Fatalf("order lost credit scope: %+v %v", order, e)
	}
	cashBalance(t, f, "commercial:revenue:superwagie", 2000000)
	cashBalance(t, f, productComputePrepay(order), 1000000)
	for range 2 {
		got, e := f.s.RefundCheckout(ctx, f.actor, f.account, checkout.ID, "scoped-refund", "unused")
		if e != nil || got.RefundStatus != "SUCCEEDED" {
			t.Fatalf("external refund: %+v %v", got, e)
		}
	}
	cashBalance(t, f, "commercial:external:refunded:"+checkout.ID, 3000000)
	cashBalance(t, f, productComputePrepay(order), 0)
	cashBalance(t, f, "commercial:revenue:superwagie", 0)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000000000)
}

func TestScopedRenewalUsesUnifiedCashOnlyAfterRuntimeReady(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	start := f.s.now()
	membership, _ := publishProductCreditOffers(t, f, "month")
	fundCash(t, f, "superwagie", 5000000)
	f.buy(t, membership.ID, "before-renewal")
	_, e := f.s.SetAuthorization(ctx, f.actor, f.account, Authorization{ProductID: "superwagie", Purpose: "renewal", Enabled: true, MaxPerOperation: "3000000", MaxTotal: "3000000", ExpiresAt: MonthAnchor(start, 2)})
	if e != nil {
		t.Fatal(e)
	}
	now := MonthAnchor(start, 1)
	f.s.now = func() time.Time { return now }
	f.s.ConfigureCheckoutReadiness(func(context.Context, string, string, string) (NativeCreditReadiness, error) {
		return NativeCreditReadiness{Reason: "native_credit_protocol_unsupported"}, nil
	})
	if _, e = f.s.Maintain(ctx, now); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 997000000)
	f.s.ConfigureCheckoutReadiness(func(ctx context.Context, _, _, product string) (NativeCreditReadiness, error) {
		return f.s.NativeCreditsReadiness(ctx, product)
	})
	for range 2 {
		if _, e = f.s.Maintain(ctx, now); e != nil {
			t.Fatal(e)
		}
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 994000000)
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 5000000)
	subs, e := f.s.Subscriptions(ctx, f.actor, f.account)
	if e != nil || len(subs) != 1 || !subs[0].PaidThrough.Equal(MonthAnchor(start, 2)) {
		t.Fatalf("wrong renewal period: %+v %v", subs, e)
	}
}

func TestProductCreditProfileRejectsLegacyDoubleRounding(t *testing.T) {
	p := nativeCreditProfile(t, 2, "legacy-test-quota", "3")
	p.CreditScope = ProductCreditScopeV1
	if e := p.Validate(); !errors.Is(e, ErrInvalid) {
		t.Fatalf("product microcredit settlement accepted a conversion that would round again: %v", e)
	}
	p.NativeCreditConversion.Numerator, p.NativeCreditConversion.Denominator = "1", "1"
	if e := p.Validate(); e != nil {
		t.Fatalf("exact product microcredit conversion rejected: %v", e)
	}
}
