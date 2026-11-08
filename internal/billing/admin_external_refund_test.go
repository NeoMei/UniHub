package billing_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/platformadmin"

	"encoding/json"
	"testing"
	"unihub/internal/billing"
)

// Seed the durable state produced by a fulfilled manual-policy external refund.
// Exercise the existing real platform authority, CAS, audit, and public read path.
func TestReviewManualExternalRefundResolution(t *testing.T) {
	s, authority, pool, ids, account := adminFixture(t)
	ctx := context.Background()
	o := billing.Order{ID: "review-order", AccountID: account, ActorID: ids["owner"], ProductID: "superwagie", PaymentSource: "external", CheckoutID: "ReviewCheckout", PaymentStatus: "PAID", FulfillmentStatus: "FULFILLED", RefundStatus: "MANUAL_REVIEW", Offer: billing.Offer{RefundRule: ""}}
	raw, _ := json.Marshal(o)
	if _, e := pool.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,'original','digest',$4)`, o.ID, account, ids["owner"], raw); e != nil {
		t.Fatal(e)
	}
	x := billing.Checkout{ID: o.CheckoutID, AccountID: account, ProductID: "superwagie", Provider: "wechat", Amount: "9800000", Currency: "CNY", PaymentStatus: "PAID", FulfillmentStatus: "FULFILLED", RefundStatus: "REVIEW", OrderID: o.ID}
	raw, _ = json.Marshal(x)
	if _, e := pool.Exec(ctx, `INSERT INTO billing_checkouts(id,account_id,product_id,request_key,request_digest,snapshot) VALUES($1,$2,'superwagie','original-checkout','digest',$3)`, x.ID, account, raw); e != nil {
		t.Fatal(e)
	}
	b := billing.Batch{ID: "review-batch", AccountID: account, ProductID: "superwagie", OrderID: o.ID, Frozen: true}
	raw, _ = json.Marshal(b)
	if _, e := pool.Exec(ctx, `INSERT INTO billing_batches(id,account_id,order_id,source_key,snapshot) VALUES($1,$2,$3,'review-source',$4)`, b.ID, account, o.ID, raw); e != nil {
		t.Fatal(e)
	}
	result, e := s.Administer(ctx, authority, ids["finance_admin"], billing.AdminOperation{Action: "refund.review", ID: o.ID, AccountID: account, Decision: "reject", Reason: "review rejects request", Version: revision(t, pool, "billing_orders", o.ID)})
	if e != nil {
		t.Fatal("admin rejection", e)
	}
	order := result.(billing.Order)
	x, e = s.GetCheckout(ctx, ids["owner"], account, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	var frozen, blocks bool
	if e = pool.QueryRow(ctx, `SELECT (snapshot->>'frozen')::boolean FROM billing_batches WHERE id=$1`, b.ID).Scan(&frozen); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_checkouts WHERE account_id=$1 AND snapshot->>'refund_status' IN ('PENDING','REVIEW'))`, account).Scan(&blocks); e != nil {
		t.Fatal(e)
	}
	t.Logf("after actual platform audited reject: order=%s checkout=%s batch_frozen=%v commercialClear_review_predicate=%v", order.RefundStatus, x.RefundStatus, frozen, blocks)
	if x.RefundStatus == "REVIEW" || blocks {
		t.Fatal("resolved manual refund still blocks commerce")
	}
}

type manualRoundTrip func(*http.Request) (*http.Response, error)

func (f manualRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type manualLedger struct {
	ledger.Ledger
	lose      bool
	onBalance func()
}

func (l *manualLedger) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	tx, e := l.Ledger.Post(ctx, c)
	if e == nil && l.lose {
		l.lose = false
		return ledger.Transaction{}, ledger.ErrUnknown
	}
	return tx, e
}

func (l *manualLedger) Balance(ctx context.Context, address, asset string) (int64, error) {
	if l.onBalance != nil {
		l.onBalance()
	}
	return l.Ledger.Balance(ctx, address, asset)
}

type manualFixture struct {
	s            *billing.Service
	authority    *platformadmin.Store
	pool         *pgxpool.Pool
	ids          map[string]string
	account      string
	x            billing.Checkout
	l            *manualLedger
	provider     payments.Provider
	refunds      int
	submitted    bool
	loseProvider bool
	refundID     string
}

func newManualFixture(t *testing.T) *manualFixture {
	return manualFixtureForKind(t, "addon")
}

func manualFixtureForKind(t *testing.T, kind string) *manualFixture {
	t.Helper()
	ctx := context.Background()
	_, a, p, ids, account := adminFixture(t)
	base := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if base == "" {
		t.Skip("UNIHUB_TEST_LEDGER_URL unset")
	}
	l := ledger.NewFormance(base, fmt.Sprintf("manual-refund-%d", time.Now().UnixNano()), &http.Client{Timeout: 5 * time.Second})
	if e := l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	f := &manualFixture{authority: a, pool: p, ids: ids, account: account, l: &manualLedger{Ledger: l}}
	f.s = billing.New(p, accounts.New(p), f.l)
	f.s.ConfigureOperators([]string{ids["owner"]})
	f.s.ConfigureSandbox(true)
	if _, e := f.s.SeedSandbox(ctx); e != nil {
		t.Fatal(e)
	}
	// This source retains a manual rule throughout the request and decision.
	offer := billing.Offer{ID: "manual-addon", ProductID: "superwagie", Name: "Manual add-on", AccountKind: "both", Kind: "addon", Price: "9800000", MonthlyCredit: "200000000", ValidityMonths: 12, Sandbox: true}
	if kind == "membership" {
		offer.Kind = "membership"
		offer.Period = "month"
	}
	profile, e := billing.BillingPreset("superwagie", "hybrid")
	if e != nil {
		t.Fatal(e)
	}
	profile.Usage.RequireSubscription = false
	if _, e = f.s.SaveProfile(ctx, ids["owner"], "superwagie", profile); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishRate(ctx, ids["owner"], billing.Rate{ID: "manual-rate", Model: "fixture-model", Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "0", Denominator: "1"}, Output: billing.Rational{Numerator: "0", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.SaveOffer(ctx, ids["owner"], offer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishOffer(ctx, ids["owner"], offer.ID); e != nil {
		t.Fatal(e)
	}
	merchant, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	platform, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	sign := func(data []byte) string {
		sum := sha256.Sum256(data)
		v, e := rsa.SignPKCS1v15(rand.Reader, platform, crypto.SHA256, sum[:])
		if e != nil {
			t.Fatal(e)
		}
		return base64.StdEncoding.EncodeToString(v)
	}
	f.provider, e = payments.NewAlipay(payments.AlipayConfig{AppID: "manual-app", SellerID: "manual-seller", NotifyURL: "https://merchant.example/payments/alipay/notify", ReturnURL: "https://merchant.example/return", PrivateKey: merchant, PublicKey: &platform.PublicKey, HTTPClient: &http.Client{Transport: manualRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "openapi.alipay.com" {
			t.Fatal("unexpected gateway")
		}
		if e := r.ParseForm(); e != nil {
			t.Fatal(e)
		}
		var committed bool
		if e := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_admin_audit WHERE action='refund.review') AND EXISTS(SELECT 1 FROM billing_checkouts WHERE snapshot->>'refund_status'='PENDING')`).Scan(&committed); e != nil || !committed {
			t.Fatalf("provider I/O before committed approval: %v", e)
		}
		var biz map[string]string
		if e := json.Unmarshal([]byte(r.Form.Get("biz_content")), &biz); e != nil {
			t.Fatal(e)
		}
		if biz["out_trade_no"] != f.x.ID || biz["trade_no"] != "manual-tx" {
			t.Fatal("original receipt lost", biz)
		}
		if f.refundID == "" {
			f.refundID = biz["out_request_no"]
		} else if f.refundID != biz["out_request_no"] {
			t.Fatal("refund identity replaced")
		}
		method := r.Form.Get("method")
		response := map[string]string{"code": "10000", "out_trade_no": biz["out_trade_no"], "trade_no": "manual-tx"}
		switch method {
		case "alipay.trade.refund":
			if biz["refund_amount"] != "9.80" {
				t.Fatal("partial or changed refund")
			}
			f.refunds++
			f.submitted = true
			if f.loseProvider {
				f.loseProvider = false
				return nil, errors.New("fixture lost provider response")
			}
		case "alipay.trade.fastpay.refund.query":
			if f.submitted {
				response["out_request_no"] = f.refundID
				response["refund_amount"] = "9.80"
				response["total_amount"] = "9.80"
				response["refund_status"] = "REFUND_SUCCESS"
			}
		default:
			t.Fatal("unexpected provider call", method)
		}
		raw, _ := json.Marshal(response)
		body, _ := json.Marshal(map[string]any{strings.ReplaceAll(method, ".", "_") + "_response": json.RawMessage(raw), "sign": sign(raw)})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	f.s.ConfigurePayments([]payments.Provider{f.provider})
	f.x, e = f.s.CreateCheckout(ctx, ids["owner"], account, billing.CheckoutRequest{OfferID: offer.ID, Provider: "alipay", Key: "manual-checkout"})
	if e != nil {
		t.Fatal(e)
	}
	values := url.Values{"app_id": {"manual-app"}, "seller_id": {"manual-seller"}, "out_trade_no": {f.x.ID}, "trade_no": {"manual-tx"}, "trade_status": {"TRADE_SUCCESS"}, "total_amount": {"9.80"}, "gmt_payment": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "sign_type": {"RSA2"}}
	var keys, parts []string
	for k := range values {
		if k != "sign_type" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+values.Get(k))
	}
	values.Set("sign", sign([]byte(strings.Join(parts, "&"))))
	if e = f.s.NotifyCheckout(ctx, "alipay", nil, []byte(values.Encode())); e != nil {
		t.Fatal(e)
	}
	f.x, e = f.s.GetCheckout(ctx, ids["owner"], account, f.x.ID)
	if e != nil || f.x.FulfillmentStatus != "FULFILLED" {
		t.Fatal(f.x, e)
	}
	f.x, e = f.s.RefundCheckout(ctx, ids["owner"], account, f.x.ID, "manual-request", "unused requested")
	if e != nil || f.x.RefundStatus != "REVIEW" {
		t.Fatal(f.x, e)
	}
	return f
}
func (f *manualFixture) decision(t *testing.T, decision string) billing.AdminOperation {
	return billing.AdminOperation{Action: "refund.review", ID: f.x.OrderID, AccountID: f.account, Decision: decision, Reason: "reviewed unused original full refund", Version: revision(t, f.pool, "billing_orders", f.x.OrderID)}
}
func TestManualExternalRefundSignedApprovalRecovery(t *testing.T) {
	f := newManualFixture(t)
	ctx := context.Background()
	request := f.decision(t, "approve")
	f.l.onBalance = func() {
		var inTransaction bool
		if e := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.locktype='advisory' AND l.granted AND ((l.classid::bigint<<32)|l.objid::bigint)=hashtextextended('unihub:platform-administration',0) AND a.xact_start IS NOT NULL)`).Scan(&inTransaction); e != nil || inTransaction {
			t.Fatalf("ledger network inside authority transaction: %v", e)
		}
	}

	out, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request)
	if e != nil {
		t.Fatal("manual approval", e)
	}
	if out.(billing.Order).RefundStatus != "FROZEN" || f.refunds != 0 {
		t.Fatal("approval must only persist instruction", out)
	}
	f.l.onBalance = nil
	for _, decision := range []string{"approve", "reject"} {
		request.Decision = decision
		if _, e = f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); !errors.Is(e, billing.ErrConflict) {
			t.Fatal("duplicate/conflicting decision", e)
		}
	}
	f.loseProvider = true
	if e = f.s.RecoverCheckouts(ctx); e != nil {
		t.Fatal(e)
	}
	x, e := f.s.GetCheckout(ctx, f.ids["owner"], f.account, f.x.ID)
	if e != nil || x.RefundStatus != "PENDING" || f.refunds != 1 {
		t.Fatal("lost provider response", x, e, f.refunds)
	}
	// Restart, with no active merchant. Bound original identity remains recoverable.
	f.s = billing.New(f.pool, accounts.New(f.pool), f.l)
	f.s.ConfigurePaymentSource(func(context.Context) ([]payments.Provider, []payments.Provider, error) {
		return nil, []payments.Provider{f.provider}, nil
	})
	f.l.lose = true
	if e = f.s.RecoverCheckouts(ctx); !errors.Is(e, billing.ErrPending) {
		t.Fatal("lost ledger response", e)
	}
	for i := 0; i < 2; i++ {
		if e = f.s.RecoverCheckouts(ctx); e != nil {
			t.Fatal(e)
		}
	}
	x, e = f.s.GetCheckout(ctx, f.ids["owner"], f.account, f.x.ID)
	if e != nil || x.RefundStatus != "SUCCEEDED" || f.refunds != 1 {
		t.Fatal(x, e, f.refunds)
	}
	var commands int
	var rule string
	if e = f.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE kind='external_refund' AND status='APPLIED'`).Scan(&commands); e != nil || commands != 1 {
		t.Fatal(commands, e)
	}
	if e = f.pool.QueryRow(ctx, `SELECT snapshot->'offer'->>'refund_rule' FROM billing_orders WHERE id=$1`, f.x.OrderID).Scan(&rule); e != nil || rule != "" {
		t.Fatal("manual rule changed", rule, e)
	}
	for _, address := range []string{"cash:" + f.account + ":unified", "cash:" + f.account + ":product:superwagie"} {
		n, e := f.l.Balance(ctx, address, "CNY/6")
		if e != nil || n != 0 {
			t.Fatal("cash wallet affected", n, e)
		}
	}
	n, e := f.l.Balance(ctx, "commercial:external:refunded:"+x.ID, "CNY/6")
	if e != nil || n != 9800000 {
		t.Fatal("refund amount", n, e)
	}
}
func TestManualExternalRefundAuthorityAuditAndUsedDenial(t *testing.T) {
	f := newManualFixture(t)
	ctx := context.Background()
	request := f.decision(t, "approve")
	for _, role := range []string{"owner", "auditor", "user_admin"} {
		if _, e := f.s.Administer(ctx, f.authority, f.ids[role], request); !errors.Is(e, accounts.ErrForbidden) {
			t.Fatal(role, e)
		}
	}
	stale := request
	stale.Version = "stale"
	if _, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], stale); !errors.Is(e, billing.ErrConflict) {
		t.Fatal("stale approval", e)
	}
	if _, e := f.pool.Exec(ctx, `CREATE FUNCTION fail_refund_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER fail_refund_audit BEFORE INSERT ON platform_admin_audit FOR EACH ROW EXECUTE FUNCTION fail_refund_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); e == nil {
		t.Fatal("audit failure ignored")
	}
	if revision(t, f.pool, "billing_orders", f.x.OrderID) != request.Version {
		t.Fatal("audit failure changed order")
	}
	x, e := f.s.GetCheckout(ctx, f.ids["owner"], f.account, f.x.ID)
	if e != nil || x.RefundStatus != "REVIEW" || f.refunds != 0 {
		t.Fatal("audit failure changed checkout", x, e)
	}
	if _, e = f.pool.Exec(ctx, `DROP TRIGGER fail_refund_audit ON platform_admin_audit`); e != nil {
		t.Fatal(e)
	}
	var batch string
	if e = f.pool.QueryRow(ctx, `SELECT id FROM billing_batches WHERE order_id=$1`, f.x.OrderID).Scan(&batch); e != nil {
		t.Fatal(e)
	}
	for _, bucket := range []string{"reserved", "consumed"} {
		if _, e = f.l.Post(ctx, ledger.Command{Reference: "fixture-" + bucket, Postings: []ledger.Posting{{Source: billing.BatchAddress(batch, "available"), Destination: billing.BatchAddress(batch, bucket), Asset: billing.CreditAsset, Amount: 1}}}); e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); !errors.Is(e, billing.ErrManualReview) {
			t.Fatal(bucket, "approved", e)
		}
		if _, e = f.l.Post(ctx, ledger.Command{Reference: "fixture-release-" + bucket, Postings: []ledger.Posting{{Source: billing.BatchAddress(batch, bucket), Destination: billing.BatchAddress(batch, "available"), Asset: billing.CreditAsset, Amount: 1}}}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.pool.Exec(ctx, `UPDATE platform_admin_roles SET roles='[]',version=version+1 WHERE user_id=$1`, f.ids["finance_admin"]); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("stale actor", e)
	}
	if f.refunds != 0 {
		t.Fatal("denied approval reached provider")
	}
}

func TestManualExternalRefundRejectRestoresCommerceAndAuditAtomicity(t *testing.T) {
	f := newManualFixture(t)
	ctx := context.Background()
	request := f.decision(t, "reject")
	if _, e := f.pool.Exec(ctx, `CREATE FUNCTION fail_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER fail_reject_audit BEFORE INSERT ON platform_admin_audit FOR EACH ROW EXECUTE FUNCTION fail_reject_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); e == nil {
		t.Fatal("audit failure ignored")
	}
	var frozen bool
	if e := f.pool.QueryRow(ctx, `SELECT (snapshot->>'frozen')::boolean FROM billing_batches WHERE order_id=$1`, f.x.OrderID).Scan(&frozen); e != nil || !frozen {
		t.Fatal("audit failure unfroze batch", e)
	}
	if _, e := f.pool.Exec(ctx, `DROP TRIGGER fail_reject_audit ON platform_admin_audit`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); e != nil {
		t.Fatal(e)
	}
	x, e := f.s.RefundCheckout(ctx, f.ids["owner"], f.account, f.x.ID, "manual-request", "unused requested")
	if e != nil || x.RefundStatus != "REJECTED" {
		t.Fatal("rejected replay", x, e)
	}
	next, e := f.s.CreateCheckout(ctx, f.ids["owner"], f.account, billing.CheckoutRequest{OfferID: "manual-addon", Provider: "alipay", Key: "purchase-after-reject"})
	if e != nil || next.ID == "" {
		t.Fatal("rejection still blocks purchasing", next, e)
	}
	bs, e := f.s.Batches(ctx, f.ids["owner"], f.account)
	if e != nil || len(bs) != 1 || bs[0].Frozen || bs[0].Available != "200000000" {
		t.Fatal("rejection did not restore own credit", bs, e)
	}
	if f.refunds != 0 {
		t.Fatal("reject called provider")
	}
}
func TestManualExternalRefundPendingCommandAndMissingReceiptDenial(t *testing.T) {
	f := newManualFixture(t)
	ctx := context.Background()
	request := f.decision(t, "approve")
	if _, e := f.pool.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES('pending-refund-test',$1,$2,'test','pending-test','digest','{}','{}','UNKNOWN')`, f.account, f.ids["owner"]); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); !errors.Is(e, billing.ErrPending) {
		t.Fatal("pending command accepted", e)
	}
	if _, e := f.pool.Exec(ctx, `DELETE FROM billing_commands WHERE id='pending-refund-test'`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `DELETE FROM billing_payment_receipts WHERE checkout_id=$1`, f.x.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request); e == nil {
		t.Fatal("missing original receipt accepted")
	}
	if f.refunds != 0 {
		t.Fatal("denial reached provider")
	}
}

func TestManualExternalRefundUnusedMembershipAndChangedSubscription(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f := manualFixtureForKind(t, "membership")
			ctx := context.Background()
			request := f.decision(t, "approve")
			if changed {
				if _, e := f.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{paid_through}',to_jsonb(((snapshot->>'paid_through')::timestamptz+interval '1 month'))) WHERE account_id=$1`, f.account); e != nil {
					t.Fatal(e)
				}
			}
			_, e := f.s.Administer(ctx, f.authority, f.ids["finance_admin"], request)
			if changed {
				if !errors.Is(e, billing.ErrManualReview) {
					t.Fatal("changed subscription accepted", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = f.s.RecoverCheckouts(ctx); e != nil {
				t.Fatal(e)
			}
			var status string
			if e = f.pool.QueryRow(ctx, `SELECT snapshot->>'status' FROM billing_subscriptions WHERE account_id=$1`, f.account).Scan(&status); e != nil || status != "refunded" {
				t.Fatal("membership retained after full refund", status, e)
			}
		})
	}
}
func TestManualExternalRefundDoesNotApproveLegacyWallet(t *testing.T) {
	s, a, p, ids, account := adminFixture(t)
	ctx := context.Background()
	order := billing.Order{ID: "legacy-manual", AccountID: account, ActorID: ids["owner"], PaymentSource: "wallet", RefundStatus: "MANUAL_REVIEW"}
	raw, _ := json.Marshal(order)
	if _, e := p.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,'legacy','digest',$4)`, order.ID, account, ids["owner"], raw); e != nil {
		t.Fatal(e)
	}
	_, e := s.Administer(ctx, a, ids["finance_admin"], billing.AdminOperation{Action: "refund.review", ID: order.ID, AccountID: account, Decision: "approve", Reason: "unsupported legacy policy", Version: revision(t, p, "billing_orders", order.ID)})
	if !errors.Is(e, billing.ErrUnsupported) {
		t.Fatal("legacy approval enabled", e)
	}
}

func TestManualExternalRefundUnfulfilledLiabilityCannotBeRejected(t *testing.T) {
	s, a, p, ids, account := adminFixture(t)
	ctx := context.Background()
	order := billing.Order{ID: "unfulfilled-review", AccountID: account, ActorID: ids["owner"], PaymentSource: "external", CheckoutID: "UnfulfilledCheckout", PaymentStatus: "PAID", FulfillmentStatus: "REVIEW", RefundStatus: "MANUAL_REVIEW"}
	raw, _ := json.Marshal(order)
	if _, e := p.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,'late-receipt','digest',$4)`, order.ID, account, ids["owner"], raw); e != nil {
		t.Fatal(e)
	}
	x := billing.Checkout{ID: order.CheckoutID, AccountID: account, OrderID: order.ID, ProductID: "superwagie", PaymentStatus: "REVIEW", FulfillmentStatus: "REVIEW", RefundStatus: "REVIEW"}
	raw, _ = json.Marshal(x)
	if _, e := p.Exec(ctx, `INSERT INTO billing_checkouts(id,account_id,product_id,request_key,request_digest,snapshot) VALUES($1,$2,'superwagie','late-receipt','digest',$3)`, x.ID, account, raw); e != nil {
		t.Fatal(e)
	}
	version := revision(t, p, "billing_orders", order.ID)
	_, e := s.Administer(ctx, a, ids["finance_admin"], billing.AdminOperation{Action: "refund.review", ID: order.ID, AccountID: account, Decision: "reject", Reason: "unfulfilled liability review", Version: version})
	if !errors.Is(e, billing.ErrPending) {
		t.Fatal("unfulfilled liability rejection accepted", e)
	}
	if revision(t, p, "billing_orders", order.ID) != version {
		t.Fatal("unfulfilled order changed")
	}
	x, e = s.GetCheckout(ctx, ids["owner"], account, x.ID)
	if e != nil || x.RefundStatus != "REVIEW" {
		t.Fatal("unfulfilled review cleared", x, e)
	}
}
