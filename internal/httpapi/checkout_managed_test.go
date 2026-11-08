package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/paymentconfig"
	"unihub/internal/payments"
	"unihub/internal/platformadmin"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestManagedMerchantOriginalCheckoutSignedNotificationAfterDisable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fixturePool := testutil.Database(t)
	cfg := fixturePool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	base := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if base == "" {
		t.Fatal("local ledger fixture required")
	}
	l := ledger.NewFormance(base, fmt.Sprintf("managed-merchant-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if e = l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	b := billing.New(pool, a, l)
	b.ConfigureSandbox(true)
	b.ConfigureOperators([]string{u.ID})
	if _, e = b.SeedSandbox(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = b.PublishRate(ctx, u.ID, billing.Rate{ID: "fixture-managed-rate", Model: "fixture-managed-model", Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "0", Denominator: "1"}, Output: billing.Rational{Numerator: "0", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}); e != nil {
		t.Fatal(e)
	}
	admin := platformadmin.New(pool, nil)
	if e := admin.Bootstrap(ctx, "fixture", u.ID, "fixture bootstrap"); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(merchantTestDir(t), "key")
	os.WriteFile(path, make([]byte, 32), 0600)
	manager, e := paymentconfig.New(pool, admin, path, "https://hub.example")
	if e != nil {
		t.Fatal(e)
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	v := int64(0)
	enabled := true
	r := paymentconfig.UpdateRequest{ExpectedVersion: &v, Enabled: &enabled, Reason: "fixture merchant", AppID: "app1", SellerID: "seller1", PrivateKeyPEM: &private, PublicKeyPEM: &public}
	if _, e = manager.Update(ctx, u.ID, "alipay", r); e != nil {
		t.Fatal(e)
	}
	b.ConfigurePaymentSource(manager.Providers)
	request := billing.CheckoutRequest{OfferID: "sandbox-sw-month-1", Provider: "alipay", Key: "original-merchant"}
	checkout, e := b.CreateCheckout(ctx, u.ID, as[0].ID, request)
	if e != nil {
		t.Fatal(e)
	}
	v = 1
	r.AppID = "app2"
	r.SellerID = "seller2"
	r.PrivateKeyPEM = nil
	r.PublicKeyPEM = nil
	if _, e = manager.Update(ctx, u.ID, "alipay", r); e != nil {
		t.Fatal(e)
	}
	v = 2
	enabled = false
	if _, e = manager.Update(ctx, u.ID, "alipay", r); e != nil {
		t.Fatal(e)
	}
	replay, e := b.CreateCheckout(ctx, u.ID, as[0].ID, request)
	if e != nil || replay.ID != checkout.ID {
		t.Fatal("disabled original replay", e)
	}
	request.Key = "new-disabled"
	if _, e = b.CreateCheckout(ctx, u.ID, as[0].ID, request); !errors.Is(e, billing.ErrPaymentUnavailable) {
		t.Fatal("new disabled checkout", e)
	}
	form := url.Values{"app_id": {"app1"}, "seller_id": {"seller1"}, "out_trade_no": {checkout.ID}, "trade_no": {"fixture-managed-transaction"}, "total_amount": {"1.00"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"RSA2"}, "gmt_payment": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}}
	// The fixture offer amount is 1 CNY. Callback signs the original merchant identity.
	names := []string{}
	for name := range form {
		if name != "sign_type" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	parts := []string{}
	for _, name := range names {
		parts = append(parts, name+"="+form.Get(name))
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if e != nil {
		t.Fatal(e)
	}
	form.Set("sign", base64.StdEncoding.EncodeToString(sig))
	h := New(verifier{}, a, wallet.New(pool, a, l), Options{Billing: b, PlatformAdmin: admin, PaymentConfiguration: manager})
	for i := 0; i < 2; i++ {
		response := call(h, "POST", "/payments/alipay/notify", "", form.Encode(), "")
		if response.Code != 200 || response.Body.String() != "success" {
			t.Fatal("original signed callback", response.Code, response.Body)
		}
	}
	var receipts int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM billing_payment_receipts`).Scan(&receipts); e != nil || receipts != 1 {
		t.Fatal("callback deduplication", receipts, e)
	}
	got, e := b.GetCheckout(ctx, u.ID, as[0].ID, checkout.ID)
	if e != nil || got.PaymentStatus != "PAID" || got.FulfillmentStatus != "FULFILLED" {
		t.Fatal(got, e)
	}
	form.Set("total_amount", "30.00")
	if e = b.NotifyCheckout(ctx, "alipay", http.Header{}, []byte(form.Encode())); !errors.Is(e, payments.ErrVerification) {
		t.Fatal("tampered signature", e)
	}
}
