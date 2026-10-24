package wallet

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sync"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
)

func TestTransferKeyCannotBlockPaidRecharge(t *testing.T) {
	s, _, u, a := fixture(t)
	ctx := context.Background()
	order, err := s.CreateRecharge(ctx, u.ID, a.ID, "", "100000000", "order", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := s.Transfer(ctx, u.ID, a.ID, "superwagie", "1000000", "receipt:"+order.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{Merchant: "sandbox", TransactionID: "payment", OrderID: order.ID, Amount: order.Amount, Currency: "CNY", Status: "SUCCEEDED"}
	recharge, err := s.Receive(ctx, receipt)
	if err != nil {
		t.Fatalf("valid paid callback blocked by public transfer key: %v", err)
	}
	if recharge.ID == transfer.ID || recharge.Kind != "recharge" {
		t.Fatalf("wrong callback operation: %+v", recharge)
	}
	again, err := s.Receive(ctx, receipt)
	if err != nil || again.ID != recharge.ID {
		t.Fatalf("callback replay: %+v %v", again, err)
	}
	replayed, err := s.Transfer(ctx, u.ID, a.ID, "superwagie", "1000000", "receipt:"+order.ID)
	if err != nil || replayed.ID != transfer.ID {
		t.Fatalf("transfer replay: %+v %v", replayed, err)
	}
	if _, err = s.Transfer(ctx, u.ID, a.ID, "superwagie", "2000000", "receipt:"+order.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("transfer mismatch accepted: %v", err)
	}
}

func TestForwardMigrationPreservesExistingOrdersAndSeparatesOperationKeys(t *testing.T) {
	s, _, u, a := fixture(t)
	ctx := context.Background()
	order, err := s.CreateRecharge(ctx, u.ID, a.ID, "", "1000000", "existing-order", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	// Emulate a schema that already applied the original migration 002 before
	// operation keys gained a kind scope. Only this disposable schema is altered.
	rows, err := s.pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid='wallet_operations'::regclass AND contype='u'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err = s.pool.Exec(ctx, `ALTER TABLE wallet_operations DROP CONSTRAINT `+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.pool.Exec(ctx, `ALTER TABLE wallet_operations ADD CONSTRAINT wallet_operations_account_id_request_key_key UNIQUE(account_id,request_key)`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version LIKE '%004_wallet_operation_key_scope.sql'`); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, s.pool); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetOrder(ctx, u.ID, a.ID, order.ID); err != nil {
		t.Fatalf("existing order lost: %v", err)
	}
	if _, err = s.Transfer(ctx, u.ID, a.ID, "superwagie", "10000", "receipt:"+order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Receive(ctx, Receipt{Merchant: "sandbox", TransactionID: "existing-order-payment", OrderID: order.ID, Amount: order.Amount, Currency: "CNY", Status: "SUCCEEDED"}); err != nil {
		t.Fatalf("old schema key scope not migrated: %v", err)
	}
	if err = database.Migrate(ctx, s.pool); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
}

func teamFinanceFixture(t *testing.T) (*Service, *testLedger, accounts.User, accounts.User, accounts.Account) {
	t.Helper()
	s, l, owner, _ := fixture(t)
	ctx := context.Background()
	member, err := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "finance"})
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.accounts.CreateTeam(ctx, owner.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.accounts.Invite(ctx, owner.ID, team.ID, member.ID, "billing_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.accounts.AcceptInvitation(ctx, member.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	deposit(t, s, owner, team)
	return s, l, owner, member, team
}

func TestFinanceRevocationBeforeSubmissionCancelsQueuedTransfer(t *testing.T) {
	s, l, owner, member, team := teamFinanceFixture(t)
	ctx := context.Background()
	op, err := s.Transfer(ctx, member.ID, team.ID, "superwagie", "80000000", "queued")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.accounts.RemoveMember(ctx, owner.ID, team.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Process(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOperation(ctx, owner.ID, team.ID, op.ID)
	if err != nil || got.Status != "CANCELED" || got.ErrorCode != "authorization_revoked" {
		t.Fatalf("revoked queued operation: %+v %v", got, err)
	}
	if n, _ := l.Balance(ctx, Address(team.ID, ""), Asset); n != 100000000 {
		t.Fatalf("revoked transfer debited unified wallet: %d", n)
	}
	if n, _ := l.Balance(ctx, Address(team.ID, "superwagie"), Asset); n != 0 {
		t.Fatalf("revoked transfer credited product: %d", n)
	}
}

type heldCommitLedger struct {
	base             *testLedger
	reference        string
	entered, release chan struct{}
	mu               sync.Mutex
	postCalls        int
	unknown          bool
}

func (l *heldCommitLedger) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	if c.Reference != l.reference {
		return l.base.Post(ctx, c)
	}
	l.mu.Lock()
	l.postCalls++
	first := l.postCalls == 1
	l.mu.Unlock()
	if first {
		close(l.entered)
		select {
		case <-l.release:
		case <-ctx.Done():
			return ledger.Transaction{}, ctx.Err()
		}
	}
	tx, err := l.base.Post(ctx, c)
	if err != nil {
		return tx, err
	}
	l.mu.Lock()
	l.unknown = true
	l.mu.Unlock()
	return ledger.Transaction{}, ledger.ErrUnknown
}
func (l *heldCommitLedger) Find(ctx context.Context, ref string) (*ledger.Transaction, error) {
	l.mu.Lock()
	unknown := l.unknown
	l.mu.Unlock()
	if ref == l.reference && unknown {
		return nil, ledger.ErrUnknown
	}
	return l.base.Find(ctx, ref)
}
func (l *heldCommitLedger) Balance(ctx context.Context, a, asset string) (int64, error) {
	return l.base.Balance(ctx, a, asset)
}

func TestInFlightUnknownRecoversAfterSerializedFinanceRevocation(t *testing.T) {
	s, base, owner, member, team := teamFinanceFixture(t)
	ctx := context.Background()
	op, err := s.Transfer(ctx, member.ID, team.ID, "superwagie", "80000000", "inflight")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.Transfer(ctx, member.ID, team.ID, "agentwiki", "10000", "after-inflight")
	if err != nil {
		t.Fatal(err)
	}
	l := &heldCommitLedger{base: base, reference: "unihub:" + op.ID, entered: make(chan struct{}), release: make(chan struct{})}
	s.ledger = l
	processCtx, cancelProcess := context.WithTimeout(ctx, 5*time.Second)
	defer cancelProcess()
	result := make(chan error, 1)
	go func() { result <- s.Process(processCtx, op.ID) }()
	select {
	case <-l.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("submission never entered ledger")
	}
	removeCtx, cancelRemove := context.WithTimeout(ctx, 100*time.Millisecond)
	err = s.accounts.RemoveMember(removeCtx, owner.ID, team.ID, member.ID)
	cancelRemove()
	if err == nil {
		t.Fatal("membership revocation crossed in-flight submission lock")
	}
	close(l.release)
	if err = <-result; !errors.Is(err, ErrPending) {
		t.Fatalf("lost committed response/readback: %v", err)
	}
	before, err := s.GetOperation(ctx, owner.ID, team.ID, op.ID)
	if err != nil || before.Status != "UNKNOWN" {
		t.Fatalf("uncertain operation: %+v %v", before, err)
	}
	if err = s.accounts.RemoveMember(ctx, owner.ID, team.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.pool, s.accounts, base)
	if err = restarted.Process(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	got, err := restarted.GetOperation(ctx, owner.ID, team.ID, op.ID)
	if err != nil || got.Status != "APPLIED" {
		t.Fatalf("in-flight recovery canceled after revocation: %+v %v", got, err)
	}
	next, err := restarted.GetOperation(ctx, owner.ID, team.ID, queued.ID)
	if err != nil || next.Status != "CANCELED" {
		t.Fatalf("new queued submission survived revocation: %+v %v", next, err)
	}
	if n, _ := base.Balance(ctx, Address(team.ID, ""), Asset); n != 20000000 {
		t.Fatalf("unified balance: %d", n)
	}
	if n, _ := base.Balance(ctx, Address(team.ID, "superwagie"), Asset); n != 80000000 {
		t.Fatalf("product balance: %d", n)
	}
	if n, _ := base.Balance(ctx, Address(team.ID, "agentwiki"), Asset); n != 0 {
		t.Fatalf("canceled follower changed product balance: %d", n)
	}
	if _, exists := base.tx["unihub:"+op.ID]; !exists {
		t.Fatal("recovery lost original business reference")
	}
	if len(base.tx) != 2 || l.postCalls != 1 {
		t.Fatalf("duplicated settlement: transactions=%d submissions=%d", len(base.tx), l.postCalls)
	}
}

type blockedLedger struct {
	base                 *testLedger
	blocked              string
	findCalls, postCalls int
}

func (l *blockedLedger) Find(ctx context.Context, ref string) (*ledger.Transaction, error) {
	if ref == l.blocked {
		l.findCalls++
		return nil, ledger.ErrUnknown
	}
	return l.base.Find(ctx, ref)
}
func (l *blockedLedger) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	if c.Reference == l.blocked {
		l.postCalls++
		return ledger.Transaction{}, ledger.ErrUnknown
	}
	return l.base.Post(ctx, c)
}
func (l *blockedLedger) Balance(ctx context.Context, a, asset string) (int64, error) {
	return l.base.Balance(ctx, a, asset)
}

func TestRecoveryBlockedAccountCannotStarveOtherAccounts(t *testing.T) {
	s, base, u, a := fixture(t)
	ctx := context.Background()
	head, err := s.Transfer(ctx, u.ID, a.ID, "superwagie", "10000", "blocked")
	if err != nil {
		t.Fatal(err)
	}
	l := &blockedLedger{base: base, blocked: "unihub:" + head.ID}
	s.ledger = l
	if err = s.Process(ctx, head.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("initial unknown: %v", err)
	}
	for i := range 105 {
		if _, err = s.Transfer(ctx, u.ID, a.ID, "agentwiki", "10000", fmt.Sprint("queued-", i)); err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "other"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.accounts.ListAccounts(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err := s.CreateRecharge(ctx, other.ID, all[0].ID, "", "1000000", "independent", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.Receive(ctx, Receipt{Merchant: "sandbox", TransactionID: "independent-payment", OrderID: order.ID, Amount: order.Amount, Currency: "CNY", Status: "SUCCEEDED"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE wallet_operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, head.ID); err != nil {
		t.Fatal(err)
	}
	l.findCalls = 0
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOperation(ctx, other.ID, all[0].ID, op.ID)
	if err != nil || got.Status != "APPLIED" {
		t.Fatalf("unrelated account starved: %+v %v", got, err)
	}
	if l.findCalls != 1 {
		t.Fatalf("blocked head attempted %d times in one cycle", l.findCalls)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if l.findCalls != 1 {
		t.Fatalf("backoff ignored: calls=%d", l.findCalls)
	}
}
