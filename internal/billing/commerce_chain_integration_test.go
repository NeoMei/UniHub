package billing

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"unihub/internal/payments"
	"unihub/internal/wallet"
)

type commerceRoundTrip func(*http.Request) (*http.Response, error)

func (f commerceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercises the real adapter, durable commercial accounting, and native CREDIT
// source allocation together. Only merchant network/keys and tariff are fixtures.
func TestCommerceSignedPaymentNativeCreditsAndOriginalRefund(t *testing.T) {
	ctx := context.Background()
	f, credential := nativeFixture(t)
	profile := nativeCreditProfile(t, 1, "integration-only-v1", "3")
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", profile); e != nil {
		t.Fatal(e)
	}
	conversion := NativeConversion{Version: "integration-only-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
	f.s.ConfigureCheckoutReadiness(func(context.Context, string, string, string) (NativeCreditReadiness, error) {
		return NativeCreditReadiness{Ready: true, Conversion: &conversion}, nil
	})
	if _, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET funding_mode='credits',asset='CREDIT/6',pricing_source='llmrouter_native_credit',billing_profile_version=2 WHERE id=$1`, credential); e != nil {
		t.Fatal(e)
	}
	for _, offer := range []Offer{
		{ID: "integration-member", ProductID: "superwagie", Name: "测试会员积分", AccountKind: "personal", Kind: "membership", Period: "month", Price: "39000000", MonthlyCredit: "1000000000", Rank: 1, UpgradeRule: "remaining_term_prorata_v1", RefundRule: "unused_only_v1", Entitlements: map[string]string{"pricing_program": "superwagie-ai-points-v1"}},
		{ID: "integration-addon", ProductID: "superwagie", Name: "测试积分加量包", AccountKind: "personal", Kind: "addon", Price: "9800000", MonthlyCredit: "200000000", ValidityMonths: 12, RefundRule: "unused_only_v1", Entitlements: map[string]string{"pricing_program": "superwagie-ai-points-v1"}},
	} {
		if _, e := f.s.SaveOffer(ctx, f.actor, offer); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.PublishOffer(ctx, f.actor, offer.ID); e != nil {
			t.Fatal(e)
		}
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
		digest := sha256.Sum256(data)
		sig, e := rsa.SignPKCS1v15(rand.Reader, platform, crypto.SHA256, digest[:])
		if e != nil {
			t.Fatal(e)
		}
		return base64.StdEncoding.EncodeToString(sig)
	}
	refundSubmitted := false
	refundRequests := 0
	provider, e := payments.NewAlipay(payments.AlipayConfig{AppID: "integration-app", SellerID: "integration-seller", NotifyURL: "https://merchant.example/payments/alipay/notify", ReturnURL: "https://merchant.example/return", PrivateKey: merchant, PublicKey: &platform.PublicKey, HTTPClient: &http.Client{Transport: commerceRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "openapi.alipay.com" || r.URL.Path != "/gateway.do" {
			t.Fatal("unexpected gateway")
		}
		if e := r.ParseForm(); e != nil {
			t.Fatal(e)
		}
		var biz map[string]string
		if e := json.Unmarshal([]byte(r.Form.Get("biz_content")), &biz); e != nil {
			t.Fatal(e)
		}
		method := r.Form.Get("method")
		response := map[string]string{"code": "10000", "out_trade_no": biz["out_trade_no"], "trade_no": biz["trade_no"]}
		switch method {
		case "alipay.trade.refund":
			refundRequests++
			refundSubmitted = true
			if biz["refund_amount"] != "9.80" {
				t.Fatal("wrong original refund amount")
			}
		case "alipay.trade.fastpay.refund.query":
			// Official paid-trade/no-refund shape: success code, absent status.
			if refundSubmitted {
				response["out_request_no"] = biz["out_request_no"]
				response["refund_amount"] = "9.80"
				response["total_amount"] = "9.80"
				response["refund_status"] = "REFUND_SUCCESS"
			}
		default:
			t.Fatalf("unexpected provider operation %s", method)
		}
		raw, _ := json.Marshal(response)
		envelope, _ := json.Marshal(map[string]any{strings.ReplaceAll(method, ".", "_") + "_response": json.RawMessage(raw), "sign": sign(raw)})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(envelope))), Request: r}, nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	f.s.ConfigurePayments([]payments.Provider{provider})
	pay := func(offerID, key, yuan string) Checkout {
		t.Helper()
		x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: offerID, Provider: "alipay", Key: key})
		if e != nil || x.Session == nil || x.Session.Kind != "redirect" {
			t.Fatalf("checkout %+v %v", x, e)
		}
		v := url.Values{"app_id": {"integration-app"}, "seller_id": {"integration-seller"}, "out_trade_no": {x.ID}, "trade_no": {"tx-" + x.ID}, "trade_status": {"TRADE_SUCCESS"}, "total_amount": {yuan}, "gmt_payment": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "sign_type": {"RSA2"}}
		keys := make([]string, 0, len(v))
		for k := range v {
			if k != "sign_type" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+"="+v.Get(k))
		}
		v.Set("sign", sign([]byte(strings.Join(parts, "&"))))
		original := []byte(v.Encode())
		v.Set("total_amount", "0.01")
		if e = f.s.NotifyCheckout(ctx, "alipay", nil, []byte(v.Encode())); e == nil {
			t.Fatal("tampered receipt accepted")
		}
		for i := 0; i < 2; i++ {
			if e = f.s.NotifyCheckout(ctx, "alipay", nil, original); e != nil {
				t.Fatal(e)
			}
		}
		x, e = f.s.GetCheckout(ctx, f.actor, f.account, x.ID)
		if e != nil || x.PaymentStatus != "PAID" || x.FulfillmentStatus != "FULFILLED" {
			t.Fatalf("payment not fulfilled: %+v %v", x, e)
		}
		return x
	}
	member := pay("integration-member", "member-payment", "39.00")
	request := creditNativeRequest(f, credential, "commercial-native-call", "17", conversion)
	call, e := f.s.ReserveNative(ctx, "llmrouter", request)
	if e != nil || call.Held != "26" || call.ChargeAsset != CreditAsset || len(call.Allocations) != 1 {
		t.Fatalf("reserve %+v %v", call, e)
	}
	call, e = f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{CallID: request.CallID, PricingDigest: request.Quote.PricingDigest, NativeQuota: "7", Amount: "11", ProviderCost: "17"})
	if e != nil || call.Charged != "11" {
		t.Fatalf("settlement %+v %v", call, e)
	}
	addon := pay("integration-addon", "addon-payment", "9.80")
	for i := 0; i < 2; i++ {
		addon, e = f.s.RefundCheckout(ctx, f.actor, f.account, addon.ID, "addon-refund", "unused")
		if e != nil || addon.RefundStatus != "SUCCEEDED" {
			t.Fatalf("refund %+v %v", addon, e)
		}
	}
	if refundRequests != 1 {
		t.Fatalf("provider refund submitted %d times", refundRequests)
	}
	member, e = f.s.RefundCheckout(ctx, f.actor, f.account, member.ID, "member-refund", "used membership")
	if e != nil || member.RefundStatus != "REVIEW" || refundRequests != 1 {
		t.Fatalf("used membership bypassed review %+v %v", member, e)
	}
	var receipts, orders, batches int
	if e = f.s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM billing_payment_receipts),(SELECT count(*) FROM billing_orders),(SELECT count(*) FROM billing_batches)`).Scan(&receipts, &orders, &batches); e != nil || receipts != 2 || orders != 2 || batches != 2 {
		t.Fatalf("duplicate grants or receipts %d/%d/%d %v", receipts, orders, batches, e)
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil {
		t.Fatal(e)
	}
	var memberAvailable, addonAvailable string
	for _, b := range bs {
		if b.OrderID == member.OrderID {
			memberAvailable = b.Available
		}
		if b.OrderID == addon.OrderID {
			addonAvailable = b.Available
		}
	}
	if memberAvailable != "999999989" || addonAvailable != "0" {
		t.Fatalf("wrong source balances member=%s addon=%s", memberAvailable, addonAvailable)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000)
}
