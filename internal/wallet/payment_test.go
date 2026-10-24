package wallet

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestReceiptRejectsWrongMerchantCurrencyAndState(t *testing.T) {
	s, _, user, account := fixture(t)
	ctx := context.Background()
	order, err := s.CreateRecharge(ctx, user.ID, account.ID, "", "1000000", "receipt-boundaries", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	base := Receipt{Merchant: "sandbox", TransactionID: "tx", OrderID: order.ID, Amount: "1000000", Currency: "CNY", Status: "SUCCEEDED"}
	for _, name := range []string{"merchant", "currency", "status"} {
		r := base
		switch name {
		case "merchant":
			r.Merchant = "another-merchant"
		case "currency":
			r.Currency = "USD"
		case "status":
			r.Status = "PENDING"
		}
		if _, err := s.Receive(ctx, r); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var receipts int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM payment_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("invalid payments persisted: %d %v", receipts, err)
	}
}

func TestPaymentSignatureRejectsTamperAndStale(t *testing.T) {
	now := time.Unix(1800000000, 0)
	body := []byte(`{"order_id":"1"}`)
	key := []byte("a-development-signing-key-at-least-32-bytes")
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := SignReceipt(key, ts, body)
	if e := VerifyReceipt(key, ts, sig, body, now); e != nil {
		t.Fatal(e)
	}
	if e := VerifyReceipt(key, ts, sig, []byte(`{"order_id":"2"}`), now); e == nil {
		t.Fatal("tampered receipt accepted")
	}
	if e := VerifyReceipt(key, ts, sig, body, now.Add(6*time.Minute)); e == nil {
		t.Fatal("expired receipt accepted")
	}
}
