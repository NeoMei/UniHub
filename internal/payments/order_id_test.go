package payments

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Cash wallet checkouts use a ct_ prefixed id (see cashtopup.CheckoutIDPrefix).
// The shared payment providers validate every out_trade_no with identifier,
// so they must accept the prefix for Create, Query, Close and notifications.
func TestIdentifierAcceptsCashCheckoutOrderID(t *testing.T) {
	ids := []string{
		"ct_0123456789abcdef01234567",
		"ct_ffffffffffffffffffffffff",
	}
	for _, id := range ids {
		if !identifier.MatchString(id) {
			t.Fatalf("identifier rejected cash checkout id %q", id)
		}
		r := Request{OrderID: id, Amount: "1000000", Description: "Cash wallet topup", ExpiresAt: time.Now().Add(time.Hour)}
		if e := validRequest(r); e != nil {
			t.Fatalf("validRequest rejected cash checkout id %q: %v", id, e)
		}
	}
}

func TestIdentifierRejectsUnsafeCharacters(t *testing.T) {
	for _, id := range []string{"", "has space", "a/b", "a?b", "a.b", "a@b", "订单1", "ct/bad"} {
		if identifier.MatchString(id) {
			t.Fatalf("identifier accepted unsafe order id %q", id)
		}
	}
}

// Exercise the exact provider call chain cash topups use. A ct_ checkout id
// must not be rejected before the gateway request is issued.
func TestProviderAcceptsCashCheckoutOrderID(t *testing.T) {
	merchant, platform := testKey(t), testKey(t)
	id := "ct_0123456789abcdef01234567"
	req := Request{OrderID: id, Amount: "1000000", Description: "Cash wallet topup", ExpiresAt: time.Now().Add(time.Hour)}

	wcClient := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		wcReply(t, w, platform, 200, `{"code_url":"weixin://wxpay/bizpayurl?pr=test"}`)
	})
	wc, e := NewWeChat(wcConfig(merchant, platform, wcClient))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = wc.Create(context.Background(), req); e != nil {
		t.Fatalf("wechat Create rejected cash checkout id: %v", e)
	}

	aliClient := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		aliReply(t, w, platform, r.FormValue("method"), `{"code":"10000"}`)
	})
	ali, e := NewAlipay(aliConfig(merchant, platform, aliClient))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ali.Create(context.Background(), req); e != nil {
		t.Fatalf("alipay Create rejected cash checkout id: %v", e)
	}
}
