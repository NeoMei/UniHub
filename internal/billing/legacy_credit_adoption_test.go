package billing

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/productusers"
	"unihub/internal/wallet"
)

func TestLegacyCreditAdoptionReusesAuthoritativeBatchAndKeepsCash(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	fundCash(t, f, "superwagie", 40000000)
	original := f.buy(t, "sandbox-sw-month-1", "old-paid-membership")
	publishProductCreditOffers(t, f, "month")
	batches, err := f.s.Batches(ctx, f.actor, f.account)
	if err != nil || len(batches) != 1 || batches[0].OrderID != original.ID {
		t.Fatalf("no authoritative legacy batch: %+v %v", batches, err)
	}
	before := batches[0]
	r := LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie", BatchIDs: []string{before.ID}}
	preview, err := f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil || !preview.CanApply || len(preview.Batches) != 1 || preview.Batches[0].Proof == nil ||
		preview.Batches[0].Proof.PaymentTransactionID == "" || preview.Batches[0].Proof.GrantTransactionID == "" {
		t.Fatalf("incorrect preflight: %+v %v", preview, err)
	}
	unmodified, err := f.s.Batches(ctx, f.actor, f.account)
	if err != nil || len(unmodified) != 1 || unmodified[0] != before {
		t.Fatalf("preflight changed evidence: %+v %v", unmodified, err)
	}
	fundCash(t, f, "superwagie", 100000)
	if _, err = f.s.ApplyLegacyCreditAdoption(ctx, r, preview.Digest); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale digest allowed adoption: %v", err)
	}
	preview, err = f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.s.ApplyLegacyCreditAdoption(ctx, r, preview.Digest)
	if err != nil || got.Cash != preview.Cash || len(got.Current) != 1 {
		t.Fatalf("adopt: %+v %v", got, err)
	}
	expected := before
	expected.CreditScope = ProductCreditScopeV1
	if got.Current[0] != expected {
		t.Fatalf("adoption changed more than scope: %+v", got.Current[0])
	}
	replay, err := f.s.ApplyLegacyCreditAdoption(ctx, r, preview.Digest)
	if err != nil || len(replay.Current) != 1 || !replay.Receipt.AppliedAt.Equal(got.Receipt.AppliedAt) {
		t.Fatalf("adoption replay: %+v %v", replay, err)
	}
	legacyCall := ReserveRequest{CallID: "legacy-cannot-use-adopted", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "legacy", RateID: "fixture-sale-readiness", Amount: "1"}
	if _, err = f.s.Reserve(ctx, "router", legacyCall); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("legacy pool spent adopted credits: %v", err)
	}

	f.s.ConfigureTrustedServices([]string{"llmrouter"})
	f.s.ConfigureProductUsers(productusers.New(f.s.pool, productusers.Registry{"superwagie/default": {
		ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}},
	}}, "https://legacy-test.invalid", nil))
	credential := id("mc_")
	if _, err = f.s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version,credential_kind,instance_id,model_scope,pricing_source,credential_version,credit_scope) VALUES($1,$1,$2,$3,'superwagie','[]','',$4,'credits','CREDIT/6',1,'api_key','default','llmrouter_available','llmrouter_native_credit',2,'product_v1')`, credential, f.actor, f.account, f.s.now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cashBefore, err := f.l.Balance(ctx, "commercial:revenue:superwagie", CashAsset)
	if err != nil {
		t.Fatal(err)
	}
	call := creditNativeRequest(f, credential, "adopted-native-credit", "10", exactProductCreditTestConversion())
	call.CreditScope, call.SourceVerified = ProductCreditScopeV1, true
	if _, err = f.s.pool.Exec(ctx, `UPDATE billing_legacy_credit_adoption_runs SET receipt=jsonb_set(receipt, '{plan,digest}', to_jsonb(repeat('0',64))) WHERE plan_digest=$1`, preview.Digest); err != nil {
		t.Fatal(err)
	}
	bad := call
	bad.CallID = "legacy-credit-mismatched-receipt"
	badHeld, badErr := f.s.ReserveNative(ctx, "llmrouter", bad)
	if badErr == nil && badHeld.Status == "RESERVED" {
		t.Fatal("native reserve used a corrupted adoption receipt")
	}
	if _, err = f.s.pool.Exec(ctx, `UPDATE billing_orders SET snapshot=jsonb_set(snapshot,'{credit_scope}','"product_v1"') WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	bad.CallID = "legacy-credit-retagged-order"
	badHeld, badErr = f.s.ReserveNative(ctx, "llmrouter", bad)
	if badErr == nil && badHeld.Status == "RESERVED" {
		t.Fatal("retagging a legacy order bypassed the adoption receipt")
	}
	if _, err = f.s.pool.Exec(ctx, `UPDATE billing_orders SET snapshot=snapshot-'credit_scope' WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	cashBalance(t, f, "commercial:revenue:superwagie", cashBefore)
	if balance, e := f.l.Balance(ctx, BatchAddress(before.ID, "reserved"), CreditAsset); e != nil || balance != 0 {
		t.Fatalf("invalid reserve moved credits: %d %v", balance, e)
	}
	if _, err = f.s.pool.Exec(ctx, `UPDATE billing_legacy_credit_adoption_runs SET receipt=jsonb_set(receipt, '{plan,digest}', to_jsonb($1::text)) WHERE plan_digest=$1`, preview.Digest); err != nil {
		t.Fatal(err)
	}
	held, err := f.s.ReserveNative(ctx, "llmrouter", call)
	if err != nil || !slices.Equal(held.Allocations, []Allocation{{BatchID: before.ID, Amount: "10"}}) {
		t.Fatalf("scoped call did not use the original lot: %+v %v", held, err)
	}
	if _, err = f.s.pool.Exec(ctx, `DELETE FROM billing_legacy_credit_adopted_batches WHERE batch_id=$1`, before.ID); err != nil {
		t.Fatal(err)
	}
	if topped, e := f.s.TopUp(ctx, "llmrouter", call.CallID, "1", "missing-legacy-receipt"); e == nil && topped.Held != "10" {
		t.Fatalf("additional reserve used an unproven lot: %+v", topped)
	}
	if balance, e := f.l.Balance(ctx, BatchAddress(before.ID, "reserved"), CreditAsset); e != nil || balance != 10 {
		t.Fatalf("invalid topup moved credits: %d %v", balance, e)
	}
	if _, err = f.s.pool.Exec(ctx, `INSERT INTO billing_legacy_credit_adopted_batches(batch_id,account_id,product_id,order_id,plan_digest) VALUES($1,$2,$3,$4,$5)`, before.ID, before.AccountID, before.ProductID, before.OrderID, preview.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{CallID: call.CallID, PricingDigest: call.Quote.PricingDigest, NativeQuota: "10", Amount: "10"}); err != nil {
		t.Fatal(err)
	}
	cashBalance(t, f, "commercial:revenue:superwagie", cashBefore)
	cashBalance(t, f, "commercial:compute_recognized:superwagie", 0)
	verification, err := f.s.VerifyLegacyCreditAdoption(ctx, r, preview.Digest)
	if err != nil || len(verification.Current) != 1 || verification.Current[0].Consumed != "10" ||
		verification.Current[0].ExpiresAt != before.ExpiresAt || verification.Receipt.CreditsAfter[0].Consumed != before.Consumed || verification.Cash != preview.Cash {
		t.Fatalf("verify historical and current reconciliation: %+v %v", verification, err)
	}
}

func TestLegacyCreditAdoptionBindsCurrentTermAndGrantEvidence(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	order := f.buy(t, "sandbox-sw-month-1", "old-monthly")
	publishProductCreditOffers(t, f, "month")
	base, err := load[Batch](ctx, f.s.pool, `SELECT snapshot FROM billing_batches WHERE order_id=$1`, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie", BatchIDs: []string{base.ID}}
	preview, err := f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil || !preview.CanApply {
		t.Fatalf("valid legacy term: %+v %v", preview, err)
	}
	term := preview.ActiveTerm
	if term == nil || term.Subscription.ID != order.SubscriptionID || term.Subscription.Version != 1 ||
		!term.Subscription.Anchor.Equal(order.Start) || !term.Subscription.PaidThrough.Equal(order.End) ||
		!term.InstallmentFrom.Equal(base.StartsAt) || !term.InstallmentTo.Equal(base.ExpiresAt) ||
		term.PaidOrderID != order.ID || term.GrantBatchID != base.ID || term.Proof != *preview.Batches[0].Proof {
		t.Fatalf("preview did not bind the original paid installment: %+v", term)
	}
	if _, err = f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{version}',to_jsonb((snapshot->>'version')::bigint+1)) WHERE id=$1`, order.SubscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ApplyLegacyCreditAdoption(ctx, r, preview.Digest); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale subscription version allowed adoption: %v", err)
	}
	if _, err = f.s.pool.Exec(ctx, `DELETE FROM billing_batches WHERE id=$1`, base.ID); err != nil {
		t.Fatal(err)
	}
	preview, err = f.s.PreviewLegacyCreditAdoption(ctx, LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie"})
	if err != nil || !slices.Contains(preview.Blockers, "legacy_paid_term_unverified") {
		t.Fatalf("preview missed absent current grant: %+v %v", preview, err)
	}
}

func TestLegacyCreditAdoptionVerifiesPaidServiceOnlyTerm(t *testing.T) {
	f := setup(t)
	f.auth(t)
	setProfile(t, f, "subscription")
	offer := publishTestOffer(t, f, pureOffer("old-service-only"))
	o := f.buy(t, offer.ID, "old-paid-service")
	publishProductCreditOffers(t, f, "month")
	preview, err := f.s.PreviewLegacyCreditAdoption(context.Background(), LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie"})
	term := preview.ActiveTerm
	if err != nil || len(preview.Blockers) != 0 || term == nil || term.PaidOrderID != o.ID || term.Proof.PaymentTransactionID == "" || term.Proof.FulfillmentCommandID == "" || term.GrantBatchID != "" || term.Proof.GrantTransactionID != "" {
		t.Fatalf("paid service-only term not verified: %+v %v", preview, err)
	}
}

func TestLegacyCreditAdoptionBlocksAmbiguityAndPaidFutureInstallments(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-year", "old-annual-membership")
	publishProductCreditOffers(t, f, "month")
	original, err := f.s.Batches(ctx, f.actor, f.account)
	if err != nil || len(original) != 1 {
		t.Fatalf("legacy term fixture: %+v %v", original, err)
	}
	before := original[0]
	r := LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie", BatchIDs: []string{before.ID}}
	preview, err := f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil || preview.CanApply || !slices.Contains(preview.Blockers, "legacy_paid_future_installments") {
		t.Fatalf("future installments lost: %+v %v", preview, err)
	}
	if _, err = f.s.ApplyLegacyCreditAdoption(ctx, r, preview.Digest); !errors.Is(err, ErrLegacyCreditAdoptionBlocked) {
		t.Fatalf("active annual term adopted: %v", err)
	}
	now := f.s.now()
	unknown := seedProductCreditBatch(t, f, Batch{ProductID: "superwagie", Kind: "addon", Original: "100", StartsAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now}, "")
	r.BatchIDs = []string{unknown}
	preview, err = f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil || len(preview.Batches) != 1 || preview.Batches[0].Reason != "original_order_not_authoritative" {
		t.Fatalf("missing price or owner evidence inferred: %+v %v", preview, err)
	}
	r.ActorID = "other-actor"
	if _, err = f.s.PreviewLegacyCreditAdoption(ctx, r); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("unauthorized scope queried: %v", err)
	}
}

func TestLegacyCreditAdoptionRejectsPartialApplicationAcrossReservedBatch(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-month-1", "old-monthly")
	now := f.s.now()
	heldID := seedProductCreditBatch(t, f, Batch{ProductID: "superwagie", Kind: "addon", Original: "10", StartsAt: now, ExpiresAt: MonthAnchor(now, 12), CreatedAt: now}, "")
	batches, err := f.s.Batches(ctx, f.actor, f.account)
	if err != nil || len(batches) != 2 {
		t.Fatalf("expected two legacy grant sources: %+v %v", batches, err)
	}
	_, err = f.l.Post(ctx, ledger.Command{Reference: "legacy-finance-hold", Postings: []ledger.Posting{{Source: BatchAddress(heldID, "available"), Destination: BatchAddress(heldID, "reserved"), Asset: CreditAsset, Amount: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	publishProductCreditOffers(t, f, "month")
	r := LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie", BatchIDs: []string{batches[0].ID, batches[1].ID}}
	preview, err := f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil || preview.CanApply || !slices.ContainsFunc(preview.Batches, func(b LegacyCreditAdoptionBatch) bool { return b.Reason == "frozen_or_reserved_batch" }) {
		t.Fatalf("did not identify reserved lot: %+v %v", preview, err)
	}
	if _, err = f.s.ApplyLegacyCreditAdoption(ctx, r, preview.Digest); !errors.Is(err, ErrLegacyCreditAdoptionBlocked) {
		t.Fatalf("preflight blocker bypassed: %v", err)
	}
	cash, err := f.l.Balance(ctx, wallet.Address(f.account, ""), CashAsset)
	if err != nil || str(cash) != preview.Cash.Unified {
		t.Fatalf("blocked adoption changed real cash: %d %v", cash, err)
	}
	for _, b := range batches {
		current, err := load[Batch](ctx, f.s.pool, `SELECT snapshot FROM billing_batches WHERE id=$1`, b.ID)
		if err != nil || current.CreditScope != "" {
			t.Fatalf("blocked request partially applied: %+v %v", current, err)
		}
	}
}

func TestLegacyCreditAdoptionRequiresOriginalMerchantReceipt(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, err := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-sw-month-1", Provider: "wechat", Key: "legacy-external"})
	if err != nil {
		t.Fatal(err)
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "legacy-external-transaction", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, err = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if err != nil || x.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("checkout not fulfilled: %+v %v", x, err)
	}
	publishProductCreditOffers(t, f, "month")
	bs, err := f.s.Batches(ctx, f.actor, f.account)
	if err != nil || len(bs) != 1 {
		t.Fatalf("checkout batch: %+v %v", bs, err)
	}
	r := LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie", BatchIDs: []string{bs[0].ID}}
	passed, err := f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil || !passed.CanApply {
		t.Fatalf("verified merchant receipt blocked: %+v %v", passed, err)
	}
	if _, err = f.s.pool.Exec(ctx, `DELETE FROM billing_payment_receipts WHERE checkout_id=$1`, x.ID); err != nil {
		t.Fatal(err)
	}
	blocked, err := f.s.PreviewLegacyCreditAdoption(ctx, r)
	if err != nil || blocked.CanApply || blocked.Batches[0].Reason != "merchant_receipt_missing" {
		t.Fatalf("missing merchant receipt inferred: %+v %v", blocked, err)
	}
}
