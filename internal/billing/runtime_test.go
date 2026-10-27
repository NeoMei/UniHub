package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func runtimeFixture(t *testing.T) (*Service, RuntimeScope, productusers.RuntimeBinding) {
	t.Helper()
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	identity := accounts.Identity{Issuer: "https://runtime-test", Subject: "owner"}
	u, e := a.EnsureIdentity(ctx, identity)
	if e != nil {
		t.Fatal(e)
	}
	reg := productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use", "credit:adjust"}}}}}
	users := productusers.New(p, reg, "https://runtime-test", []string{u.ID})
	binding, e := users.EnsureRuntimeBinding(ctx, "llmrouter", "default", "42", "bind", identity, u)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version) VALUES($1,'llmrouter','default','["member"]',true,100,1)`, u.ID); e != nil {
		t.Fatal(e)
	}
	l := ledger.NewFormance("http://127.0.0.1:53068", fmt.Sprintf("runtime-test-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if e = l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	s := New(p, a, l)
	s.ConfigureProductUsers(users)
	return s, RuntimeScope{ProductID: "llmrouter", InstanceID: "default"}, binding
}
func TestRuntimeLedgerUsesNativePriceAndDoesNotDoubleCharge(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	gift := RuntimeAdjustmentRequest{RequestKey: "gift", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "original LR gift", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}
	if _, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, gift); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, gift); e != nil {
		t.Fatal(e)
	}
	in := RuntimeReserveRequest{CallID: "call", LocalUserID: "42", LocalKeyID: "original-key", AccountID: b.PersonalAccountID, RequestDigest: "request", Model: "native-model-not-in-central-rates", Operation: "native-operation", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "lr-price-v7", PricingDigest: "pricing", Amount: "60"}
	if _, e := s.RuntimeReserve(ctx, scope, in); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RuntimeReserve(ctx, scope, in); e != nil {
		t.Fatal(e)
	}
	settlement := RuntimeSettlementRequest{Amount: "37", PricingDigest: "pricing", Usage: []byte(`{"requests":1}`)}
	out, e := s.RuntimeSettle(ctx, scope, "call", settlement)
	if e != nil || out.Charged != "37" {
		t.Fatalf("settle %+v %v", out, e)
	}
	if _, e = s.RuntimeSettle(ctx, scope, "call", settlement); e != nil {
		t.Fatal(e)
	}
	settlement.Amount = "38"
	if _, e = s.RuntimeSettle(ctx, scope, "call", settlement); !errors.Is(e, ErrConflict) {
		t.Fatalf("conflicting settlement: %v", e)
	}
	balance, e := s.RuntimeBalance(ctx, scope, "42", b.PersonalAccountID)
	if e != nil || balance.Available != "63" || balance.Held != "0" || balance.Consumed != "37" {
		t.Fatalf("balance %+v %v", balance, e)
	}
	if _, e = s.RuntimeGetCall(ctx, RuntimeScope{ProductID: "llmrouter", InstanceID: "other"}, "call"); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("cross instance read: %v", e)
	}
}
func TestRuntimeImportIsOncePerLegacyUserAndKeepsScope(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	in := RuntimeImportRequest{BatchID: "cutover-one", LegacyUserID: "42", LocalUserID: "42", AccountID: b.PersonalAccountID, SnapshotDigest: "verified-snapshot", Amount: "99", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, SourceUnit: RuntimeUnitVersion, Conversion: "identity"}
	first, e := s.RuntimeImportBalance(ctx, scope, in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.RuntimeImportBalance(ctx, scope, in)
	if e != nil || first.ID != second.ID {
		t.Fatalf("replay %v %v", second, e)
	}
	in.BatchID = "cutover-two"
	if _, e = s.RuntimeImportBalance(ctx, scope, in); !errors.Is(e, ErrConflict) {
		t.Fatalf("duplicate migration allowed: %v", e)
	}
	balance, e := s.RuntimeBalance(ctx, scope, "42", b.PersonalAccountID)
	if e != nil || balance.Available != "99" {
		t.Fatalf("import balance %+v %v", balance, e)
	}
	bad := RuntimeReserveRequest{CallID: "wrong-asset", LocalUserID: "42", AccountID: b.PersonalAccountID, RequestDigest: "r", Model: "m", Operation: "chat", Asset: "CREDIT/6", UnitVersion: RuntimeUnitVersion, PriceVersion: "v", PricingDigest: "p", Amount: "1"}
	if _, e = s.RuntimeReserve(ctx, scope, bad); !errors.Is(e, ErrInvalid) {
		t.Fatalf("implicit conversion allowed: %v", e)
	}
}

func TestRuntimeBudgetIsAConstraintAndRevocationDoesNotLoseReservedCharge(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	if _, e := s.pool.Exec(ctx, `INSERT INTO products(id,name,enabled) VALUES('llmrouter','LR',true) ON CONFLICT(id) DO NOTHING`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "gift", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "gift", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetRuntimeBudget(ctx, scope, b.UserID, RuntimeBudgetRequest{LocalUserID: "42", AccountID: b.PersonalAccountID, MaxTotal: "50", MaxPerCall: "40"}); e != nil {
		t.Fatal(e)
	}
	in := RuntimeReserveRequest{CallID: "allowed", LocalUserID: "42", AccountID: b.PersonalAccountID, RequestDigest: "request", Model: "m", Operation: "chat", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "v", PricingDigest: "p", Amount: "30"}
	if _, e := s.RuntimeReserve(ctx, scope, in); e != nil {
		t.Fatal(e)
	}
	in.CallID = "over-total"
	if _, e := s.RuntimeReserve(ctx, scope, in); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("budget ignored: %v", e)
	}
	if _, e := s.pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, b.UserID); e != nil {
		t.Fatal(e)
	}
	in.CallID = "revoked"
	in.Amount = "1"
	if _, e := s.RuntimeReserve(ctx, scope, in); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("revocation ignored: %v", e)
	}
	if _, e := s.RuntimeSettle(ctx, scope, "allowed", RuntimeSettlementRequest{Amount: "20", PricingDigest: "p", Usage: []byte(`{"requests":1}`)}); e != nil {
		t.Fatal(e)
	}
	available, e := s.ledger.Balance(ctx, RuntimeAddress(scope, b.PersonalAccountID, "available"), RuntimeAsset)
	if e != nil || available != 80 {
		t.Fatalf("settlement after revocation lost funds: %d %v", available, e)
	}
}

type runtimeLostResponse struct {
	ledger.Ledger
	once bool
}

func (l *runtimeLostResponse) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	tr, e := l.Ledger.Post(ctx, c)
	if e == nil && l.once {
		l.once = false
		return ledger.Transaction{}, ledger.ErrUnknown
	}
	return tr, e
}
func TestRuntimeUnknownReserveRecoversOneLedgerHoldAndTopupReplays(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	if _, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "gift", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "gift", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}); e != nil {
		t.Fatal(e)
	}
	s.ledger = &runtimeLostResponse{Ledger: s.ledger, once: true}
	in := RuntimeReserveRequest{CallID: "lost-response", LocalUserID: "42", AccountID: b.PersonalAccountID, RequestDigest: "request", Model: "m", Operation: "chat", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "v", PricingDigest: "p", Amount: "20"}
	if _, e := s.RuntimeReserve(ctx, scope, in); !errors.Is(e, ErrPending) {
		t.Fatalf("uncertain write was declared final: %v", e)
	}
	call, e := s.RuntimeReserve(ctx, scope, in)
	if e != nil || call.Status != "RESERVED" {
		t.Fatalf("reserve recovery %+v %v", call, e)
	}
	for i := 0; i < 2; i++ {
		call, e = s.RuntimeTopUp(ctx, scope, in.CallID, RuntimeTopUpRequest{RequestKey: "extend", Amount: "10"})
		if e != nil || call.Held != "30" {
			t.Fatalf("topup replay %+v %v", call, e)
		}
	}
	if _, e = s.RuntimeRelease(ctx, scope, in.CallID, RuntimeReleaseRequest{Reason: "before dispatch"}); e != nil {
		t.Fatal(e)
	}
	balance, e := s.RuntimeBalance(ctx, scope, "42", b.PersonalAccountID)
	if e != nil || balance.Available != "100" || balance.Held != "0" {
		t.Fatalf("recovery duplicated funds %+v %v", balance, e)
	}
}

func TestRuntimeSetAvailableUsesCASAndReplaysWithoutRecomputingDelta(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	base := RuntimeAdjustmentRequest{RequestKey: "gift", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "gift", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}
	if _, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, base); e != nil {
		t.Fatal(e)
	}
	target := "80"
	set := base
	set.RequestKey = "set"
	set.Amount = ""
	set.SetAvailable = &target
	set.ExpectedAvailable = "100"
	result, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, set)
	if e != nil || result.Amount != "-20" {
		t.Fatalf("atomic set %+v %v", result, e)
	}
	conflict := set
	conflict.RequestKey = "other-set"
	if _, e = s.RuntimeAdjustCredit(ctx, scope, b.UserID, conflict); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale expected balance accepted: %v", e)
	}
	base.RequestKey = "later-gift"
	base.Amount = "10"
	if _, e = s.RuntimeAdjustCredit(ctx, scope, b.UserID, base); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RuntimeAdjustCredit(ctx, scope, b.UserID, set); e != nil {
		t.Fatal(e)
	}
	balance, e := s.RuntimeBalance(ctx, scope, "42", b.PersonalAccountID)
	if e != nil || balance.Available != "90" {
		t.Fatalf("replay recomputed adjustment %+v %v", balance, e)
	}
	set.Amount = "1"
	if _, e = s.RuntimeAdjustCredit(ctx, scope, b.UserID, set); !errors.Is(e, ErrInvalid) {
		t.Fatalf("mixed delta and set accepted: %v", e)
	}
}

func TestRuntimeVerifiedReceiptAwardsOnceAndCannotChangeRecipient(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	in := RuntimeAwardRequest{ReceiptID: "verified-order-1", SourceKind: "payment", ReceiptDigest: "verified-provider-receipt-digest", Reason: "confirmed LR payment", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "25", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}
	first, e := s.RuntimeAward(ctx, scope, in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.RuntimeAward(ctx, scope, in)
	if e != nil || first.ID != second.ID {
		t.Fatalf("award replay %+v %v", second, e)
	}
	in.Amount = "26"
	if _, e = s.RuntimeAward(ctx, scope, in); !errors.Is(e, ErrConflict) {
		t.Fatalf("receipt mutation accepted: %v", e)
	}
	available, e := s.ledger.Balance(ctx, RuntimeAddress(scope, b.PersonalAccountID, "available"), RuntimeAsset)
	if e != nil || available != 25 {
		t.Fatalf("double award %d %v", available, e)
	}
	in.SourceKind = "user-supplied-credit"
	if _, e = s.RuntimeAward(ctx, scope, in); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unknown award source accepted: %v", e)
	}
}
