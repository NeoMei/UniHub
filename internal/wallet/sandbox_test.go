package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/ledger"
)

func TestSandboxRechargeDefaultsToDeniedBeforeCreatingOrder(t *testing.T) {
	s, base, user, account := fixture(t)
	restricted := New(s.pool, s.accounts, base)
	ctx := context.Background()
	_, err := restricted.CreateRecharge(ctx, user.ID, account.ID, "", "1000000", "default-denied", "sandbox")
	if !errors.Is(err, accounts.ErrForbidden) {
		t.Errorf("unconfigured policy must deny new recharge, got %v", err)
	}
	assertRechargeRows(t, s, 0, 0, 0)
}

func TestSandboxRechargePermissionUsesUserIDAndPreservesFinanceRole(t *testing.T) {
	s, _, user, personal := fixture(t)
	ctx := context.Background()
	other, err := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://other", Subject: "same-name", Name: user.Name})
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.accounts.CreateTeam(ctx, user.ID, "Sandbox team")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := s.accounts.Invite(ctx, user.ID, team.ID, other.ID, "billing_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.accounts.AcceptInvitation(ctx, other.ID, invitation.ID); err != nil {
		t.Fatal(err)
	}
	ids := []string{user.ID}
	s.ConfigureSandboxRechargeUsers(ids)
	ids[0] = other.ID
	if !s.CanSandboxRecharge(user.ID) || s.CanSandboxRecharge(other.ID) || s.CanSandboxRecharge(user.Name) || s.CanSandboxRecharge("") {
		t.Fatal("only the copied immutable user ID can grant sandbox recharge")
	}
	if err := s.RequireSandboxRecharge(ctx, other.ID, team.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("same-name finance user cannot settle another user's order: %v", err)
	}
	if _, err := s.CreateRecharge(ctx, other.ID, team.ID, "", "1000000", "same-name", "sandbox"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("same-name finance user cannot create recharge: %v", err)
	}
	if err := s.RequireSandboxRecharge(ctx, user.ID, personal.ID); err != nil {
		t.Fatalf("allowed owner: %v", err)
	}
	if _, err := s.CreateRecharge(ctx, user.ID, team.ID, "", "1000000", "allowed-team", "sandbox"); err != nil {
		t.Fatalf("allowed user can recharge their team: %v", err)
	}
	s.ConfigureSandboxRechargeUsers([]string{other.ID})
	if err := s.RequireSandboxRecharge(ctx, other.ID, personal.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("allowed UUID does not grant another payer's finance permission: %v", err)
	}
	if err := s.accounts.ChangeRole(ctx, user.ID, team.ID, other.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireSandboxRecharge(ctx, other.ID, team.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("allowed UUID with revoked finance role: %v", err)
	}
	if _, err := s.CreateRecharge(ctx, other.ID, team.ID, "", "1000000", "member", "sandbox"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("allowed UUID must retain create finance check: %v", err)
	}
}

func TestSandboxRechargeExistingOrderAndReceiptRemainIdempotent(t *testing.T) {
	s, base, user, account := fixture(t)
	ctx := context.Background()
	order, op := pendingRecharge(t, s, user, account)
	if err := s.Process(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	restricted := New(s.pool, s.accounts, base)
	again, err := restricted.CreateRecharge(ctx, user.ID, account.ID, "", "1000000", "pending-recharge", "sandbox")
	if err != nil || again.ID != order.ID {
		t.Fatalf("old exact request remains a read: %+v %v", again, err)
	}
	if _, err := restricted.CreateRecharge(ctx, user.ID, account.ID, "", "2000000", "pending-recharge", "sandbox"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed historical request must remain conflicting: %v", err)
	}
	receipt := receiptFor(order)
	replayed, err := restricted.Receive(ctx, receipt)
	if err != nil || replayed.ID != op.ID || replayed.Status != "APPLIED" {
		t.Fatalf("confirmed receipt readback: %+v %v", replayed, err)
	}
	receipt.TransactionID = "different-payment"
	if _, err := restricted.Receive(ctx, receipt); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed receipt must remain conflicting: %v", err)
	}
	assertRechargeRows(t, s, 1, 1, 1)
	if len(base.tx) != 1 {
		t.Fatalf("replays changed ledger transaction count: %d", len(base.tx))
	}
}

func TestSandboxRechargeRestrictedOrderCannotCreateReceipt(t *testing.T) {
	s, base, user, account := fixture(t)
	ctx := context.Background()
	order, err := s.CreateRecharge(ctx, user.ID, account.ID, "", "1000000", "legacy-order", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	restricted := New(s.pool, s.accounts, base)
	_, err = restricted.Receive(ctx, receiptFor(order))
	if !errors.Is(err, accounts.ErrForbidden) {
		t.Errorf("new receipt for restricted persisted actor: %v", err)
	}
	assertRechargeRows(t, s, 1, 0, 0)
	got, err := restricted.GetOrder(ctx, user.ID, account.ID, order.ID)
	if err != nil || got.ID != order.ID || got.OperationID != nil {
		t.Fatalf("existing order remains readable and unpaid: %+v %v", got, err)
	}
}

func TestSandboxRechargeRestrictedWorkerNeverPosts(t *testing.T) {
	for _, state := range []string{"CREATED", "SUBMITTING", "UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			s, base, user, account := fixture(t)
			ctx := context.Background()
			order, op := pendingRecharge(t, s, user, account)
			if _, err := s.pool.Exec(ctx, `UPDATE wallet_operations SET status=$2 WHERE id=$1`, op.ID, state); err != nil {
				t.Fatal(err)
			}
			observed := &sandboxLedger{testLedger: base}
			restricted := New(s.pool, s.accounts, observed)
			err := restricted.Process(ctx, op.ID)
			wantStatus := "UNKNOWN"
			if state == "CREATED" {
				wantStatus = "CANCELED"
				if err != nil {
					t.Errorf("cancel unsent restricted recharge: %v", err)
				}
			} else if !errors.Is(err, ErrPending) {
				t.Errorf("in-flight outcome must remain pending: %v", err)
			}
			got, err := restricted.GetOperation(ctx, user.ID, account.ID, op.ID)
			if err != nil || got.Status != wantStatus || got.TransactionID != nil {
				t.Errorf("restricted %s recovery: %+v %v", state, got, err)
			}
			if observed.posts != 0 || observed.finds != 1 {
				t.Errorf("restricted worker calls: posts=%d finds=%d", observed.posts, observed.finds)
			}
			balance, err := base.Balance(ctx, Address(account.ID, ""), Asset)
			if err != nil || balance != 0 {
				t.Errorf("restricted recharge changed balance: %d %v", balance, err)
			}
			again, err := restricted.Receive(ctx, receiptFor(order))
			if err != nil || again.ID != op.ID || again.Status != wantStatus {
				t.Errorf("existing receipt must retain its actual result: %+v %v", again, err)
			}
		})
	}
}

func TestSandboxRechargeRestrictedWorkerConfirmsExistingPost(t *testing.T) {
	for _, state := range []string{"CREATED", "SUBMITTING", "UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			s, base, user, account := fixture(t)
			ctx := context.Background()
			_, op := pendingRecharge(t, s, user, account)
			var raw []byte
			if err := s.pool.QueryRow(ctx, `UPDATE wallet_operations SET status=$2 WHERE id=$1 RETURNING command`, op.ID, state).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var command ledger.Command
			if err := json.Unmarshal(raw, &command); err != nil {
				t.Fatal(err)
			}
			posted, err := base.Post(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			observed := &sandboxLedger{testLedger: base}
			restricted := New(s.pool, s.accounts, observed)
			if err := restricted.Process(ctx, op.ID); err != nil {
				t.Fatal(err)
			}
			got, err := restricted.GetOperation(ctx, user.ID, account.ID, op.ID)
			if err != nil || got.Status != "APPLIED" || got.TransactionID == nil || *got.TransactionID != posted.ID {
				t.Fatalf("confirmed original posting: %+v %v", got, err)
			}
			if observed.posts != 0 || observed.finds != 1 {
				t.Fatalf("confirmation must only look up the original reference: posts=%d finds=%d", observed.posts, observed.finds)
			}
			balance, err := base.Balance(ctx, Address(account.ID, ""), Asset)
			if err != nil || balance != 1000000 {
				t.Fatalf("confirmed balance: %d %v", balance, err)
			}
		})
	}
}

func TestSandboxRechargeRestrictedLookupFailureRemainsUnknown(t *testing.T) {
	s, base, user, account := fixture(t)
	ctx := context.Background()
	_, op := pendingRecharge(t, s, user, account)
	if _, err := s.pool.Exec(ctx, `UPDATE wallet_operations SET status='SUBMITTING' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	observed := &sandboxLedger{testLedger: base, lookupErr: ledger.ErrUnknown}
	restricted := New(s.pool, s.accounts, observed)
	if err := restricted.Process(ctx, op.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("unconfirmed lookup must remain pending: %v", err)
	}
	got, err := restricted.GetOperation(ctx, user.ID, account.ID, op.ID)
	if err != nil || got.Status != "UNKNOWN" || observed.posts != 0 {
		t.Fatalf("lookup failure must neither post nor cancel: %+v %v posts=%d", got, err, observed.posts)
	}
}

func TestSandboxRechargeUnsubmittedLookupFailureCanCancelOnRetry(t *testing.T) {
	s, base, user, account := fixture(t)
	ctx := context.Background()
	deposit(t, s, user, account)
	_, op := pendingRecharge(t, s, user, account)
	transfer, err := s.Transfer(ctx, user.ID, account.ID, "superwagie", "1000000", "after-restricted-recharge")
	if err != nil {
		t.Fatal(err)
	}
	observed := &sandboxLedger{testLedger: base, lookupErr: ledger.ErrUnknown}
	restricted := New(s.pool, s.accounts, observed)
	if err := restricted.Process(ctx, op.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("failed lookup remains pending: %v", err)
	}
	got, err := restricted.GetOperation(ctx, user.ID, account.ID, op.ID)
	if err != nil || got.Status != "CREATED" || observed.posts != 0 {
		t.Fatalf("unsent recharge must retain CREATED after lookup failure: %+v %v posts=%d", got, err, observed.posts)
	}
	var delayed bool
	if err := s.pool.QueryRow(ctx, `SELECT next_attempt_at>now() FROM wallet_operations WHERE id=$1`, op.ID).Scan(&delayed); err != nil || !delayed {
		t.Fatalf("failed lookup must delay recovery: delayed=%t err=%v", delayed, err)
	}
	if err := restricted.Recover(ctx); err != nil || observed.finds != 1 {
		t.Fatalf("backoff must defer lookup and followers: finds=%d err=%v", observed.finds, err)
	}
	observed.lookupErr = nil
	if err := restricted.Process(ctx, op.ID); err != nil {
		t.Fatalf("second lookup confirms no posting: %v", err)
	}
	got, err = restricted.GetOperation(ctx, user.ID, account.ID, op.ID)
	if err != nil || got.Status != "CANCELED" || observed.posts != 0 || observed.finds != 2 {
		t.Fatalf("retry must cancel without posting: %+v %v posts=%d finds=%d", got, err, observed.posts, observed.finds)
	}
	if err := restricted.Process(ctx, transfer.ID); err != nil {
		t.Fatalf("canceled recharge must release following transfer: %v", err)
	}
	got, err = restricted.GetOperation(ctx, user.ID, account.ID, transfer.ID)
	if err != nil || got.Status != "APPLIED" || observed.posts != 1 {
		t.Fatalf("only following transfer may post: %+v %v posts=%d", got, err, observed.posts)
	}
	if balance, err := base.Balance(ctx, Address(account.ID, ""), Asset); err != nil || balance != 99000000 {
		t.Fatalf("restricted recharge added no funds: balance=%d err=%v", balance, err)
	}
}

type sandboxLedger struct {
	*testLedger
	posts, finds int
	lookupErr    error
}

func (l *sandboxLedger) Post(ctx context.Context, command ledger.Command) (ledger.Transaction, error) {
	l.posts++
	return l.testLedger.Post(ctx, command)
}

func (l *sandboxLedger) Find(ctx context.Context, reference string) (*ledger.Transaction, error) {
	l.finds++
	if l.lookupErr != nil {
		return nil, l.lookupErr
	}
	return l.testLedger.Find(ctx, reference)
}

func pendingRecharge(t *testing.T, s *Service, user accounts.User, account accounts.Account) (Order, Operation) {
	t.Helper()
	ctx := context.Background()
	order, err := s.CreateRecharge(ctx, user.ID, account.ID, "", "1000000", "pending-recharge", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.Receive(ctx, receiptFor(order))
	if err != nil {
		t.Fatal(err)
	}
	return order, op
}

func receiptFor(order Order) Receipt {
	return Receipt{Merchant: "sandbox", TransactionID: "sandbox-payment", OrderID: order.ID, Amount: order.Amount, Currency: "CNY", Status: "SUCCEEDED"}
}

func assertRechargeRows(t *testing.T, s *Service, orders, receipts, operations int) {
	t.Helper()
	var gotOrders, gotReceipts, gotOperations int
	err := s.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM recharge_orders), (SELECT count(*) FROM payment_receipts), (SELECT count(*) FROM wallet_operations)`).Scan(&gotOrders, &gotReceipts, &gotOperations)
	if err != nil {
		t.Fatal(err)
	}
	if gotOrders != orders || gotReceipts != receipts || gotOperations != operations {
		t.Errorf("persisted rows: orders=%d receipts=%d operations=%d; want %d/%d/%d", gotOrders, gotReceipts, gotOperations, orders, receipts, operations)
	}
}
