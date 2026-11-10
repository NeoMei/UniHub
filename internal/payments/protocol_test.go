package payments

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	base := server.Client().Transport
	target, _ := url.Parse(server.URL)
	return &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.mch.weixin.qq.com" && r.URL.Host != "openapi.alipay.com" {
			t.Errorf("untrusted gateway %s", r.URL.Host)
		}
		cloned := r.Clone(r.Context())
		u := *r.URL
		u.Host = target.Host
		cloned.URL = &u
		return base.RoundTrip(cloned)
	})}
}
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func fixtureSign(t *testing.T, k *rsa.PrivateKey, s string) string {
	t.Helper()
	h := sha256.Sum256([]byte(s))
	b, e := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, h[:])
	if e != nil {
		t.Fatal(e)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func fixtureVerify(t *testing.T, k *rsa.PublicKey, s, sig string) {
	t.Helper()
	b, e := base64.StdEncoding.DecodeString(sig)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte(s))
	if e = rsa.VerifyPKCS1v15(k, crypto.SHA256, h[:], b); e != nil {
		t.Error("merchant request signature invalid")
	}
}
func wcHeaders(t *testing.T, k *rsa.PrivateKey, body string) http.Header {
	h := http.Header{}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	h.Set("Wechatpay-Timestamp", ts)
	h.Set("Wechatpay-Nonce", "fixture-nonce")
	h.Set("Wechatpay-Serial", "platform")
	h.Set("Wechatpay-Signature", fixtureSign(t, k, ts+"\nfixture-nonce\n"+body+"\n"))
	return h
}
func wcReply(t *testing.T, w http.ResponseWriter, k *rsa.PrivateKey, status int, body string) {
	for name, values := range wcHeaders(t, k, body) {
		w.Header()[name] = values
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
func wcConfig(merchant, platform *rsa.PrivateKey, c *http.Client) WeChatConfig {
	return WeChatConfig{AppID: "app", MerchantID: "merchant", CertificateSerial: "cert", NotifyURL: "https://merchant.example/wc", PrivateKey: merchant, APIv3Key: []byte("12345678901234567890123456789012"), PlatformKeys: map[string]*rsa.PublicKey{"platform": &platform.PublicKey}, HTTPClient: c}
}
func aliConfig(merchant, platform *rsa.PrivateKey, c *http.Client) AlipayConfig {
	return AlipayConfig{AppID: "app", SellerID: "seller", NotifyURL: "https://merchant.example/ali", ReturnURL: "https://merchant.example/done", PrivateKey: merchant, PublicKey: &platform.PublicKey, HTTPClient: c}
}
func fixtureCanonical(v url.Values, notification bool) string {
	keys := []string{}
	for k := range v {
		if k != "sign" && (!notification || k != "sign_type") && v.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, k+"="+v.Get(k))
	}
	return strings.Join(parts, "&")
}
func aliReply(t *testing.T, w http.ResponseWriter, k *rsa.PrivateKey, method, body string) {
	sig := fixtureSign(t, k, body)
	_, _ = fmt.Fprintf(w, `{"%s_response":%s,"sign":%q}`, strings.ReplaceAll(method, ".", "_"), body, sig)
}

func TestWeChatSignedHTTPAndRefundRecovery(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	var requests int
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		fields := map[string]string{}
		for _, m := range regexp.MustCompile(`([a-z_]+)="([^"]*)"`).FindAllStringSubmatch(r.Header.Get("Authorization"), -1) {
			fields[m[1]] = m[2]
		}
		if fields["mchid"] != "merchant" || fields["serial_no"] != "cert" {
			t.Error("merchant header binding")
		}
		fixtureVerify(t, &merchant.PublicKey, r.Method+"\n"+r.URL.RequestURI()+"\n"+fields["timestamp"]+"\n"+fields["nonce_str"]+"\n"+string(body)+"\n", fields["signature"])
		switch r.URL.Path {
		case "/v3/pay/transactions/native":
			var v map[string]json.RawMessage
			_ = json.Unmarshal(body, &v)
			if string(v["appid"]) != `"app"` || string(v["mchid"]) != `"merchant"` || !strings.Contains(string(v["amount"]), `"total":19900`) {
				t.Error("wrong create binding/amount")
			}
			wcReply(t, w, platform, 200, `{"code_url":"weixin://wxpay/bizpayurl?pr=fixture"}`)
		case "/v3/pay/transactions/out-trade-no/Order1":
			if r.URL.Query().Get("mchid") != "merchant" {
				t.Error("query merchant missing")
			}
			wcReply(t, w, platform, 200, `{ "appid":"app", "mchid":"merchant", "out_trade_no":"Order1", "transaction_id":"Tx1", "trade_state":"SUCCESS", "success_time":"2026-11-08T12:00:00+08:00", "amount":{"total":19900,"currency":"CNY"}}`)
		case "/v3/pay/transactions/out-trade-no/Order1/close":
			wcReply(t, w, platform, 204, "")
		case "/v3/refund/domestic/refunds":
			if !strings.Contains(string(body), `"out_refund_no":"Refund1"`) || !strings.Contains(string(body), `"transaction_id":"Tx1"`) || strings.Contains(string(body), `"out_trade_no"`) {
				t.Error("refund must use exactly one original transaction identifier")
			}
			wcReply(t, w, platform, 200, `{"out_trade_no":"Order1","transaction_id":"Tx1","out_refund_no":"Refund1","refund_id":"WxRefund1","channel":"ORIGINAL","status":"SUCCESS","amount":{"refund":19900,"total":19900,"currency":"CNY"}}`)
		case "/v3/refund/domestic/refunds/Refund1":
			wcReply(t, w, platform, 200, `{"out_trade_no":"Order1","transaction_id":"Tx1","out_refund_no":"Refund1","refund_id":"WxRefund1","channel":"ORIGINAL","status":"SUCCESS","amount":{"refund":19900,"total":19900,"currency":"CNY"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	p, e := NewWeChat(wcConfig(merchant, platform, c))
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	s, e := p.Create(ctx, Request{OrderID: "Order1", Amount: "199000000", Description: "Plan", ExpiresAt: expiry})
	if e != nil || s.Kind != "qr" || s.ExpiresAt != expiry {
		t.Fatalf("create %#v %v", s, e)
	}
	result, e := p.Query(ctx, "Order1")
	if e != nil || result.Status != "PAID" || result.Amount != "199000000" || result.TransactionID != "Tx1" {
		t.Fatalf("query %#v %v", result, e)
	}
	if e = p.Close(ctx, "Order1"); e != nil {
		t.Fatal(e)
	}
	req := RefundRequest{OrderID: "Order1", TransactionID: "Tx1", RefundID: "Refund1", Amount: "199000000", TotalAmount: "199000000"}
	refund, e := p.Refund(ctx, req)
	if e != nil || refund.Status != "PENDING" {
		t.Fatalf("submit must need independent confirmation: %#v %v", refund, e)
	}
	refund, e = p.QueryRefund(ctx, req)
	if e != nil || refund.Status != "SUCCEEDED" || refund.RefundID != "Refund1" || refund.TransactionID != "Tx1" || refund.Amount != req.Amount {
		t.Fatalf("refund query %#v %v", refund, e)
	}
	if requests != 5 {
		t.Errorf("requests %d", requests)
	}
}

func TestWeChatVerificationRejectsTamperAndWrongBinding(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	for _, tc := range []struct {
		name, body string
		tamper     bool
	}{
		{"wrong merchant", `{"appid":"app","mchid":"other","out_trade_no":"Order1","transaction_id":"Tx1","trade_state":"SUCCESS","success_time":"2026-11-08T12:00:00+08:00","amount":{"total":100,"currency":"CNY"}}`, false},
		{"duplicate", `{"appid":"app","mchid":"wrong","mchid":"merchant","out_trade_no":"Order1"}`, false},
		{"tampered", `{"appid":"app","mchid":"merchant"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				h := wcHeaders(t, platform, tc.body)
				for k, v := range h {
					w.Header()[k] = v
				}
				body := tc.body
				if tc.tamper {
					body += " "
				}
				_, _ = io.WriteString(w, body)
			})
			p, e := NewWeChat(wcConfig(merchant, platform, c))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = p.Query(context.Background(), "Order1"); !errors.Is(e, ErrVerification) {
				t.Fatalf("accepted invalid response: %v", e)
			}
		})
	}
}
func TestWeChatAuthenticatedEncryptedNotification(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	cfg := wcConfig(merchant, platform, nil)
	p, e := NewWeChat(cfg)
	if e != nil {
		t.Fatal(e)
	}
	plaintext := `{"appid":"app","mchid":"merchant","out_trade_no":"Order1","transaction_id":"Tx1","trade_state":"SUCCESS","success_time":"2026-11-08T12:00:00+08:00","amount":{"total":100,"currency":"CNY"}}`
	block, _ := aes.NewCipher(cfg.APIv3Key)
	gcm, _ := cipher.NewGCM(block)
	encrypted := gcm.Seal(nil, []byte("123456789012"), []byte(plaintext), []byte("transaction"))
	body := fmt.Sprintf(`{"event_type":"TRANSACTION.SUCCESS","resource_type":"encrypt-resource","resource":{"algorithm":"AEAD_AES_256_GCM","original_type":"transaction","nonce":"123456789012","associated_data":"transaction","ciphertext":%q}}`, base64.StdEncoding.EncodeToString(encrypted))
	h := wcHeaders(t, platform, body)
	for i := 0; i < 2; i++ {
		result, e := p.VerifyNotification(h, []byte(body))
		if e != nil || result.Status != "PAID" || result.Amount != "1000000" {
			t.Fatalf("notification %#v %v", result, e)
		}
	}
	h.Set("Wechatpay-Serial", "unknown")
	if _, e = p.VerifyNotification(h, []byte(body)); !errors.Is(e, ErrVerification) {
		t.Fatal("unknown platform key accepted")
	}
	h = wcHeaders(t, platform, body)
	h.Add("Wechatpay-Timestamp", h.Get("Wechatpay-Timestamp"))
	if _, e = p.VerifyNotification(h, []byte(body)); !errors.Is(e, ErrVerification) {
		t.Fatal("duplicate signature header accepted")
	}
}

func TestAlipayPagePayAndSignedHTTPRecovery(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if e := r.ParseForm(); e != nil {
			t.Fatal(e)
		}
		fixtureVerify(t, &merchant.PublicKey, fixtureCanonical(r.Form, false), r.Form.Get("sign"))
		if r.Form.Get("app_id") != "app" || r.Form.Get("sign_type") != "RSA2" {
			t.Error("request binding")
		}
		method := r.Form.Get("method")
		switch method {
		case "alipay.trade.query":
			aliReply(t, w, platform, method, `{ "code":"10000", "out_trade_no":"Order1", "trade_no":"Tx1", "trade_status":"TRADE_SUCCESS", "total_amount":"199.00", "send_pay_date":"2026-11-08 12:00:00" }`)
		case "alipay.trade.close":
			aliReply(t, w, platform, method, `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1"}`)
		case "alipay.trade.refund":
			if !strings.Contains(r.Form.Get("biz_content"), `"out_request_no":"Refund1"`) {
				t.Error("missing stable refund id")
			}
			aliReply(t, w, platform, method, `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1","fund_change":"Y","refund_fee":"199.00"}`)
		case "alipay.trade.fastpay.refund.query":
			aliReply(t, w, platform, method, `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1","out_request_no":"Refund1","refund_amount":"199.00","refund_status":"REFUND_SUCCESS"}`)
		default:
			t.Errorf("wrong method %s", method)
		}
	})
	p, e := NewAlipay(aliConfig(merchant, platform, c))
	if e != nil {
		t.Fatal(e)
	}
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	s, e := p.Create(context.Background(), Request{OrderID: "Order1", Amount: "199000000", Description: "Plan & points", ExpiresAt: expiry})
	if e != nil || s.Kind != "redirect" || s.ExpiresAt != expiry {
		t.Fatalf("create %#v %v", s, e)
	}
	u, e := url.Parse(s.URL)
	if e != nil || u.Scheme != "https" || u.Host != "openapi.alipay.com" || u.Path != "/gateway.do" {
		t.Fatalf("untrusted page URL %s", s.URL)
	}
	q := u.Query()
	fixtureVerify(t, &merchant.PublicKey, fixtureCanonical(q, false), q.Get("sign"))
	var biz map[string]string
	_ = json.Unmarshal([]byte(q.Get("biz_content")), &biz)
	if q.Get("method") != "alipay.trade.page.pay" || biz["product_code"] != "FAST_INSTANT_TRADE_PAY" || biz["total_amount"] != "199.00" || biz["time_expire"] != expiry.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05") {
		t.Fatalf("wrong page contract %#v", biz)
	}
	result, e := p.Query(context.Background(), "Order1")
	if e != nil || result.Status != "PAID" || result.Amount != "199000000" {
		t.Fatalf("query %#v %v", result, e)
	}
	if e = p.Close(context.Background(), "Order1"); e != nil {
		t.Fatal(e)
	}
	req := RefundRequest{OrderID: "Order1", TransactionID: "Tx1", RefundID: "Refund1", Amount: "199000000", TotalAmount: "199000000"}
	refund, e := p.Refund(context.Background(), req)
	if e != nil || refund.Status != "PENDING" {
		t.Fatalf("refund submit %#v %v", refund, e)
	}
	refund, e = p.QueryRefund(context.Background(), req)
	if e != nil || refund.Status != "SUCCEEDED" || refund.Amount != req.Amount {
		t.Fatalf("refund query %#v %v", refund, e)
	}
}
func TestAlipayNotificationBindingAndAmbiguity(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	p, e := NewAlipay(aliConfig(merchant, platform, nil))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		modify func(url.Values)
		ok     bool
	}{
		{"valid", func(v url.Values) {}, true}, {"wrong seller", func(v url.Values) { v.Set("seller_id", "other") }, false}, {"wrong app", func(v url.Values) { v.Set("app_id", "other") }, false}, {"duplicate amount", func(v url.Values) { v.Add("total_amount", "900.00") }, false}, {"subcent", func(v url.Values) { v.Set("total_amount", "1.001") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := url.Values{"app_id": {"app"}, "seller_id": {"seller"}, "out_trade_no": {"Order1"}, "trade_no": {"Tx1"}, "total_amount": {"1.00"}, "trade_status": {"TRADE_SUCCESS"}, "gmt_payment": {"2026-11-08 12:00:00"}, "sign_type": {"RSA2"}}
			tc.modify(v)
			v.Set("sign", fixtureSign(t, platform, fixtureCanonical(v, true)))
			r, e := p.VerifyNotification(http.Header{}, []byte(v.Encode()))
			if tc.ok {
				if e != nil || r.Amount != "1000000" || r.Status != "PAID" {
					t.Fatalf("%#v %v", r, e)
				}
			} else if !errors.Is(e, ErrVerification) {
				t.Fatalf("accepted bad notification: %v", e)
			}
		})
	}
}
func TestAlipayRefundRequiresExplicitSuccessAndExactBinding(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		{"missing status", `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1","out_request_no":"Refund1","refund_amount":"1.00"}`, false},
		{"request not received", `{"code":"10000","msg":"Success"}`, false},
		{"partial unconfirmed identity", `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1"}`, false},
		{"unconfirmed wrong order", `{"code":"10000","out_trade_no":"Other"}`, true},
		{"unconfirmed wrong transaction", `{"code":"10000","trade_no":"Other"}`, true},
		{"unconfirmed wrong refund", `{"code":"10000","out_request_no":"Other"}`, true},
		{"unconfirmed wrong amount", `{"code":"10000","refund_amount":"2.00"}`, true},
		{"unconfirmed wrong total", `{"code":"10000","total_amount":"2.00"}`, true},
		{"success missing identity", `{"code":"10000","refund_amount":"1.00","refund_status":"REFUND_SUCCESS"}`, true},
		{"success missing amount", `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1","out_request_no":"Refund1","refund_status":"REFUND_SUCCESS"}`, true},
		{"wrong amount", `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1","out_request_no":"Refund1","refund_amount":"2.00","refund_status":"REFUND_SUCCESS"}`, true},
		{"wrong refund", `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1","out_request_no":"Other","refund_amount":"1.00","refund_status":"REFUND_SUCCESS"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				aliReply(t, w, platform, "alipay.trade.fastpay.refund.query", tc.body)
			})
			p, e := NewAlipay(aliConfig(merchant, platform, c))
			if e != nil {
				t.Fatal(e)
			}
			r, e := p.QueryRefund(context.Background(), RefundRequest{OrderID: "Order1", TransactionID: "Tx1", RefundID: "Refund1", Amount: "1000000", TotalAmount: "1000000"})
			if tc.wantErr {
				if !errors.Is(e, ErrVerification) {
					t.Fatalf("expected verification failure %v", e)
				}
			} else if e != nil || r.Status != "NOT_FOUND" || r.RefundID != "Refund1" || r.TransactionID != "Tx1" || r.Amount != "1000000" {
				t.Fatalf("unconfirmed refund cannot retry its original identity %#v %v", r, e)
			}
		})
	}
}
func TestAlipayPOSTCharsetLivesInQueryForSignedRefund(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("charset") != "utf-8" {
			t.Fatalf("charset must be in query: %s", r.URL.RawQuery)
		}
		if e := r.ParseForm(); e != nil {
			t.Fatal(e)
		}
		if r.PostForm.Get("charset") != "" {
			t.Fatal("charset must not be sent in the form body")
		}
		if r.Form.Get("method") != "alipay.trade.refund" {
			t.Fatalf("method=%q", r.Form.Get("method"))
		}
		if !strings.Contains(r.Form.Get("biz_content"), "退款") {
			t.Fatal("non-ASCII refund reason missing from biz_content")
		}
		fixtureVerify(t, &merchant.PublicKey, fixtureCanonical(r.Form, false), r.Form.Get("sign"))
		aliReply(t, w, platform, "alipay.trade.refund", `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1"}`)
	})
	p, e := NewAlipay(aliConfig(merchant, platform, c))
	if e != nil {
		t.Fatal(e)
	}
	r, e := p.Refund(context.Background(), RefundRequest{OrderID: "Order1", TransactionID: "Tx1", RefundID: "Refund1", Amount: "1000000", TotalAmount: "1000000", Reason: "联调测试退款"})
	if e != nil || r.Status != "PENDING" {
		t.Fatalf("%#v %v", r, e)
	}
}
func TestProviderUnavailableAndValidation(t *testing.T) {
	if _, e := NewWeChat(WeChatConfig{}); !errors.Is(e, ErrUnconfigured) {
		t.Fatal(e)
	}
	if _, e := NewAlipay(AlipayConfig{}); !errors.Is(e, ErrUnconfigured) {
		t.Fatal(e)
	}
	merchant, platform := testKey(t), testKey(t)
	c := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	wc, _ := NewWeChat(wcConfig(merchant, platform, c))
	ali, _ := NewAlipay(aliConfig(merchant, platform, c))
	for _, p := range []Provider{wc, ali} {
		if _, e := p.Query(context.Background(), "Order1"); !errors.Is(e, ErrUnavailable) {
			t.Fatalf("timeout %v", e)
		}
		if _, e := p.Create(context.Background(), Request{OrderID: "Order1", Amount: "10001", Description: "Plan", ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(e, ErrInvalid) {
			t.Fatal("subcent accepted")
		}
		if _, e := p.Query(context.Background(), "../bad"); !errors.Is(e, ErrInvalid) {
			t.Fatal("order injection accepted")
		}
	}
	cfg := aliConfig(merchant, platform, nil)
	cfg.ReturnURL = "http://unsafe.example"
	if _, e := NewAlipay(cfg); !errors.Is(e, ErrInvalid) {
		t.Fatal("unsafe callback accepted")
	}
}
