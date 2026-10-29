package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/metering"
	"unihub/internal/testutil"
)

type rateTestLedger struct {
	balanceEntered chan struct{}
	balanceRelease chan struct{}
	balances       map[string]int64
	transactions   map[string]ledger.Transaction
}

func (l *rateTestLedger) Post(_ context.Context, command ledger.Command) (ledger.Transaction, error) {
	if transaction, ok := l.transactions[command.Reference]; ok {
		return transaction, nil
	}
	for _, posting := range command.Postings {
		if posting.Source != "world" && l.balances[posting.Source+"/"+posting.Asset] < posting.Amount {
			return ledger.Transaction{}, ledger.ErrInsufficientFunds
		}
	}
	for _, posting := range command.Postings {
		if posting.Source != "world" {
			l.balances[posting.Source+"/"+posting.Asset] -= posting.Amount
		}
		l.balances[posting.Destination+"/"+posting.Asset] += posting.Amount
	}
	transaction := ledger.Transaction{ID: command.Reference, Reference: command.Reference, Postings: command.Postings}
	l.transactions[command.Reference] = transaction
	return transaction, nil
}
func (l *rateTestLedger) Find(_ context.Context, reference string) (*ledger.Transaction, error) {
	if transaction, ok := l.transactions[reference]; ok {
		return &transaction, nil
	}
	return nil, nil
}
func (l *rateTestLedger) Balance(_ context.Context, address, asset string) (int64, error) {
	if l.balanceEntered != nil {
		close(l.balanceEntered)
		<-l.balanceRelease
	}
	return l.balances[address+"/"+asset], nil
}

func TestPrepaidCredentialCanReserveDistinctOperationsForOneModel(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	accountStore := accounts.New(pool)
	user, err := accountStore.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://rate-test", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	accountList, err := accountStore.ListAccounts(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	ledgerFixture := &rateTestLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}}
	service := New(pool, accountStore, ledgerFixture)
	service.ConfigureOperators([]string{user.ID})
	service.ConfigureTrustedServices([]string{"router"})
	f := fixture{s: service, l: ledgerFixture, actor: user.ID, account: accountList[0].ID}
	profile := BillingProfile{ProductID: "superwagie", Usage: UsagePolicy{Enabled: true, FundingModes: []string{FundingPrepaidCash}, DefaultFundingMode: FundingPrepaidCash}}
	if _, err = service.SaveProfile(ctx, user.ID, profile.ProductID, profile); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PublishRate(ctx, user.ID, cashRate("cash-rate")); err != nil {
		t.Fatal(err)
	}
	fundCash(t, f, "llmrouter", 100)
	credential := cashCredential(t, f)
	imageRate := Rate{ID: "image-edit-rate", Model: "cash-model", Asset: CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "image_edit", Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "images", Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	if _, err = service.PublishRate(ctx, user.ID, imageRate); err != nil {
		t.Fatal(err)
	}
	if _, err = cashReserve(t, f, "chat-call", credential, "1"); err != nil {
		t.Fatalf("chat reservation: %v", err)
	}
	request := ReserveRequest{CallID: "image-call", ActorID: user.ID, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "image", RateID: imageRate.ID, Amount: "1", CredentialID: credential}
	if _, err = service.Reserve(ctx, "router", request); err != nil {
		t.Fatalf("image reservation: %v", err)
	}
	duplicate := imageRate
	duplicate.ID = "duplicate-image-rate"
	if _, err = service.PublishRate(ctx, user.ID, duplicate); err != nil {
		t.Fatal(err)
	}
	request.CallID = "ambiguous-call"
	request.RequestDigest = "ambiguous"
	if _, err = service.Reserve(ctx, "router", request); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("duplicate operation must fail closed: %v", err)
	}
}

func TestReservationHoldsCatalogLockUntilRecorded(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(pool)
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://rate-lock", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	aa, err := a.ListAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	l := &rateTestLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}, balanceEntered: make(chan struct{}), balanceRelease: make(chan struct{})}
	s := New(pool, a, l)
	s.ConfigureOperators([]string{u.ID})
	s.ConfigureTrustedServices([]string{"router"})
	f := fixture{s: s, l: l, actor: u.ID, account: aa[0].ID}
	_, err = s.SaveProfile(ctx, u.ID, "superwagie", BillingProfile{ProductID: "superwagie", Usage: UsagePolicy{Enabled: true, FundingModes: []string{FundingPrepaidCash}, DefaultFundingMode: FundingPrepaidCash}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PublishRate(ctx, u.ID, cashRate("cash-rate"))
	if err != nil {
		t.Fatal(err)
	}
	fundCash(t, f, "llmrouter", 100)
	credential := cashCredential(t, f)
	done := make(chan error, 1)
	go func() { _, err := cashReserve(t, f, "lock-call", credential, "1"); done <- err }()
	<-l.balanceEntered
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(tx)
	var acquired bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('unihub:billing-rate-catalog',0))`).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if acquired {
		close(l.balanceRelease)
		<-done
		t.Fatal("catalog was writable during reservation")
	}
	close(l.balanceRelease)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('unihub:billing-rate-catalog',0))`).Scan(&acquired); err != nil || !acquired {
		t.Fatalf("catalog lock leaked: acquired=%v err=%v", acquired, err)
	}
}

func TestRateCatalogMutationsWaitForReservationLock(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(pool)
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://catalog-lock", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool, a, &rateTestLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}})
	s.ConfigureOperators([]string{u.ID})
	if _, err = s.PublishRate(ctx, u.ID, cashRate("existing")); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('unihub:billing-rate-catalog',0))`); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"publish", "retire"} {
		timed, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		if op == "publish" {
			_, err = s.PublishRate(timed, u.ID, cashRate("new"))
		} else {
			err = s.RetireRate(timed, u.ID, "existing")
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s must wait for reservations: %v", op, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishRate(ctx, u.ID, cashRate("new")); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireRate(ctx, u.ID, "existing"); err != nil {
		t.Fatal(err)
	}
}
