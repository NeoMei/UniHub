package payments

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAlipayRefundRejectsChangedOriginalTotal(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		aliReply(t, w, platform, "alipay.trade.fastpay.refund.query", `{"code":"10000","out_trade_no":"Order1","trade_no":"Tx1","out_request_no":"Refund1","total_amount":"9.00","refund_amount":"1.00","refund_status":"REFUND_SUCCESS"}`)
	})
	p, _ := NewAlipay(aliConfig(merchant, platform, c))
	_, e := p.QueryRefund(context.Background(), RefundRequest{OrderID: "Order1", TransactionID: "Tx1", RefundID: "Refund1", Amount: "1000000", TotalAmount: "1000000"})
	if !errors.Is(e, ErrVerification) {
		t.Fatalf("changed original total accepted: %v", e)
	}
}
func TestWeChatRefundRejectsNonOriginalChannel(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		wcReply(t, w, platform, 200, `{"out_trade_no":"Order1","transaction_id":"Tx1","out_refund_no":"Refund1","refund_id":"WxRefund1","status":"SUCCESS","channel":"OTHER_BANKCARD","amount":{"refund":100,"total":100,"currency":"CNY"}}`)
	})
	p, _ := NewWeChat(wcConfig(merchant, platform, c))
	_, e := p.QueryRefund(context.Background(), RefundRequest{OrderID: "Order1", TransactionID: "Tx1", RefundID: "Refund1", Amount: "1000000", TotalAmount: "1000000"})
	if !errors.Is(e, ErrVerification) {
		t.Fatalf("non-original channel accepted: %v", e)
	}
}
func TestAlipaySignatureOriginalBytesAndResponseBinding(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	valid := `{ "code":"10000", "out_trade_no":"Order1", "trade_no":"Tx1", "trade_status":"TRADE_SUCCESS", "total_amount":"1.00" }`
	for _, tc := range []struct {
		name, body string
		tamper     bool
	}{
		{"tamper", valid, true}, {"wrong order", strings.Replace(valid, "Order1", "Other", 1), false}, {"wrong merchant", strings.Replace(valid, `"code"`, `"seller_id":"other","code"`, 1), false}, {"duplicate order", strings.Replace(valid, `"code"`, `"out_trade_no":"Other","code"`, 1), false}, {"wrong currency", strings.Replace(valid, `"code"`, `"trans_currency":"USD","code"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.tamper {
					sig := fixtureSign(t, platform, tc.body)
					b := strings.Replace(tc.body, "1.00", "9.00", 1)
					_, _ = io.WriteString(w, `{"alipay_trade_query_response":`+b+`,"sign":"`+sig+`"}`)
				} else {
					aliReply(t, w, platform, "alipay.trade.query", tc.body)
				}
			})
			p, _ := NewAlipay(aliConfig(merchant, platform, c))
			if _, e := p.Query(context.Background(), "Order1"); !errors.Is(e, ErrVerification) {
				t.Fatalf("accepted invalid signed payload: %v", e)
			}
		})
	}
}
func TestSignedNotFoundNeverMeansClosed(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	wcClient := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		wcReply(t, w, platform, 404, `{"code":"ORDER_NOT_EXIST"}`)
	})
	aliClient := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		aliReply(t, w, platform, r.Form.Get("method"), `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`)
	})
	wc, _ := NewWeChat(wcConfig(merchant, platform, wcClient))
	ali, _ := NewAlipay(aliConfig(merchant, platform, aliClient))
	for _, p := range []Provider{wc, ali} {
		r, e := p.Query(context.Background(), "Order1")
		if e != nil || r.Status != "NOT_FOUND" {
			t.Fatalf("%s %#v %v", p.ID(), r, e)
		}
		if e = p.Close(context.Background(), "Order1"); e == nil {
			t.Fatalf("%s nonexistence falsely closes payable link", p.ID())
		}
	}
}
func TestWeChatStaleResponseAndUntrustedQR(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	for _, tc := range []struct {
		name  string
		stale bool
		qr    string
	}{{"stale", true, "weixin://wxpay/bizpayurl?pr=fixture"}, {"external URL", false, "https://evil.example/pay"}, {"userinfo", false, "weixin://evil@wxpay/bizpayurl?pr=fixture"}} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				b, _ := json.Marshal(map[string]string{"code_url": tc.qr})
				h := wcHeaders(t, platform, string(b))
				if tc.stale {
					h.Set("Wechatpay-Timestamp", "1")
					h.Set("Wechatpay-Signature", fixtureSign(t, platform, "1\nfixture-nonce\n"+string(b)+"\n"))
				}
				for k, v := range h {
					w.Header()[k] = v
				}
				_, _ = w.Write(b)
			})
			p, _ := NewWeChat(wcConfig(merchant, platform, c))
			if _, e := p.Create(context.Background(), Request{OrderID: "Order1", Amount: "1000000", Description: "Plan", ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(e, ErrVerification) {
				t.Fatalf("accepted unsafe session %v", e)
			}
		})
	}
}
func TestRedirectDoesNotForwardMerchantAuthorization(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	calls := 0
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "https://evil.example/steal", http.StatusTemporaryRedirect)
	})
	wc, _ := NewWeChat(wcConfig(merchant, platform, c))
	ali, _ := NewAlipay(aliConfig(merchant, platform, c))
	for _, p := range []Provider{wc, ali} {
		if _, e := p.Query(context.Background(), "Order1"); e == nil {
			t.Fatal("redirect accepted")
		}
	}
	if calls != 2 {
		t.Fatalf("followed redirect: %d", calls)
	}
}
func TestPEMAndConfigFailClosed(t *testing.T) {
	key := testKey(t)
	pkcs8, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	public, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range [][]byte{pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})} {
		p, e := ParsePrivateKeyPEM(b)
		if e != nil || p.N.Cmp(key.N) != 0 {
			t.Fatalf("parse private: %v", e)
		}
		if _, e = ParsePrivateKeyPEM(append(b, b...)); !errors.Is(e, ErrInvalid) {
			t.Fatal("ambiguous private PEM accepted")
		}
	}
	for _, b := range [][]byte{pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}), pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})} {
		p, e := ParsePublicKeyPEM(b)
		if e != nil || p.N.Cmp(key.N) != 0 {
			t.Fatalf("parse public: %v", e)
		}
	}
	for _, b := range [][]byte{nil, []byte("not pem")} {
		if _, e = ParsePrivateKeyPEM(b); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad private key accepted")
		}
		if _, e = ParsePublicKeyPEM(b); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad public key accepted")
		}
	}
	cfg := wcConfig(key, key, nil)
	cfg.APIv3Key = []byte("short")
	if _, e := NewWeChat(cfg); !errors.Is(e, ErrInvalid) {
		t.Fatal("short AES key accepted")
	}
	cfg = wcConfig(key, key, nil)
	cfg.PlatformKeys = map[string]*rsa.PublicKey{"bad": nil}
	if _, e := NewWeChat(cfg); !errors.Is(e, ErrInvalid) {
		t.Fatal("nil trust key accepted")
	}
}
func TestAlipayNotificationTampering(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	p, _ := NewAlipay(aliConfig(merchant, platform, nil))
	v := url.Values{"app_id": {"app"}, "seller_id": {"seller"}, "out_trade_no": {"Order1"}, "trade_no": {"Tx1"}, "total_amount": {"1.00"}, "trade_status": {"TRADE_SUCCESS"}, "gmt_payment": {"2026-11-08 12:00:00"}, "sign_type": {"RSA2"}}
	v.Set("sign", fixtureSign(t, platform, fixtureCanonical(v, true)))
	v.Set("total_amount", "9.00")
	if _, e := p.VerifyNotification(http.Header{}, []byte(v.Encode())); !errors.Is(e, ErrVerification) {
		t.Fatal("tampered callback accepted")
	}
}

func TestRejectsJSONFieldsAmbiguousToGoDecoder(t *testing.T) {
	var v struct {
		Merchant string `json:"mchid"`
	}
	if e := strictJSON([]byte(`{"MCHID":"other","mchid":"merchant"}`), &v); !errors.Is(e, ErrVerification) {
		t.Fatal("case-variant duplicate accepted")
	}
}

func TestNotificationDecryptionRejectsMalformedAuthenticatedResource(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	p, _ := NewWeChat(wcConfig(merchant, platform, nil))
	for _, nonce := range []string{"short", "123456789012"} {
		body := `{"event_type":"TRANSACTION.SUCCESS","resource_type":"encrypt-resource","resource":{"algorithm":"AEAD_AES_256_GCM","original_type":"transaction","nonce":"` + nonce + `","associated_data":"transaction","ciphertext":"YWJj"}}`
		if _, e := p.VerifyNotification(wcHeaders(t, platform, body), []byte(body)); !errors.Is(e, ErrVerification) {
			t.Fatalf("accepted malformed AES resource: %v", e)
		}
	}
}
