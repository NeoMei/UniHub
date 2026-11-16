package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/money"
)

// CreditCashTopup records a verified real-money payment as a durable
// world->cash posting. The payment adapter has already authenticated the
// receipt; this method only freezes the amount, destination and idempotency
// key. The wallet worker applies the posting like any other operation.
// key must be stable per checkout so replays never double-credit.
func (s *Service) CreditCashTopup(ctx context.Context, account, actor, product, amount, key, transactionID string) (Operation, error) {
	n, e := money.ParseRecharge(amount)
	if e != nil || !validKey(key) || !validKey(transactionID) {
		return Operation{}, ErrInvalid
	}
	if e = checkProduct(ctx, s.pool, product); e != nil {
		return Operation{}, e
	}
	// The caller (cashtopup service) already verified account membership and
	// receipt authenticity. A second authority check keeps the boundary explicit.
	role, e := s.accounts.RoleOn(ctx, s.pool, actor, account)
	if e != nil {
		return Operation{}, e
	}
	if role != "owner" && role != "billing_admin" {
		return Operation{}, accounts.ErrForbidden
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Operation{}, e
	}
	defer database.Rollback(tx)
	h := digest([]string{"cash-topup", account, product, strconv.FormatInt(n, 10), transactionID})
	var old string
	op, e := scanOpWithHash(tx.QueryRow(ctx, "SELECT "+opColumns+",payload_hash FROM wallet_operations WHERE account_id=$1 AND kind='cash_topup' AND request_key=$2", account, key), &old)
	if e == nil {
		if h != old {
			return Operation{}, ErrConflict
		}
		return op, tx.Commit(ctx)
	}
	if !errors.Is(e, accounts.ErrNotFound) {
		return Operation{}, e
	}
	id := newID("op_")
	cmd := ledger.Command{Reference: "unihub:" + id, Postings: []ledger.Posting{{Source: "world", Destination: Address(account, product), Asset: Asset, Amount: n}}}
	b, _ := json.Marshal(cmd)
	op, e = scanOp(tx.QueryRow(ctx, "INSERT INTO wallet_operations(id,account_id,actor_id,kind,request_key,payload_hash,command) VALUES($1,$2,$3,'cash_topup',$4,$5,$6) RETURNING "+opColumns, id, account, actor, key, h, b))
	if e != nil {
		return Operation{}, dbError(e)
	}
	if e = audit(ctx, tx, account, actor, id, "payment.cash_topup"); e != nil {
		return Operation{}, e
	}
	return op, tx.Commit(ctx)
}
