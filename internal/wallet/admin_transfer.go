package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/money"
)

// AdminTransferTarget resolves and re-checks the destination inside the posting
// transaction, after the source account is locked. The two HTTP gates
// (super_admin to any account, team owner to one of its members) differ only
// here, so the conserved posting, idempotency and audit stay in one path.
type AdminTransferTarget func(ctx context.Context, q accounts.Querier) (account, product string, err error)

// AdminTransfer moves cash from the actor's own personal unified wallet to a
// destination cash wallet without minting from world. The worker applies the
// conserved posting like any other kind: no recharge gate, insufficient funds
// becomes REJECTED/insufficient_funds, APPLIED is the only success confirmation.
func (s *Service) AdminTransfer(ctx context.Context, actor, amount, key string, resolve AdminTransferTarget) (Operation, error) {
	n, e := money.ParseRecharge(amount)
	if e != nil || !validKey(key) || resolve == nil {
		return Operation{}, ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Operation{}, e
	}
	defer database.Rollback(tx)
	// The payer is always the actor's own personal account unified wallet; no
	// request field can name the source.
	var source string
	e = tx.QueryRow(ctx, `SELECT id FROM accounts WHERE kind='personal' AND personal_user_id=$1`, actor).Scan(&source)
	if errors.Is(e, pgx.ErrNoRows) {
		return Operation{}, accounts.ErrForbidden
	}
	if e != nil {
		return Operation{}, e
	}
	if e = database.LockAccountTx(ctx, tx, source); e != nil {
		return Operation{}, e
	}
	target, product, e := resolve(ctx, tx)
	if e != nil {
		return Operation{}, e
	}
	if target == "" {
		return Operation{}, ErrInvalid
	}
	if product != "" {
		if e = checkProduct(ctx, tx, product); e != nil {
			return Operation{}, e
		}
	}
	h := digest([]string{"admin-transfer", source, target, product, strconv.FormatInt(n, 10)})
	var old string
	o, e := scanOpWithHash(tx.QueryRow(ctx, `SELECT `+opColumns+`,payload_hash FROM wallet_operations WHERE account_id=$1 AND kind='admin_transfer' AND request_key=$2`, source, key), &old)
	if e == nil {
		if h != old {
			return Operation{}, ErrConflict
		}
		return o, tx.Commit(ctx)
	}
	if !errors.Is(e, accounts.ErrNotFound) {
		return Operation{}, e
	}
	id := newID("op_")
	cmd := ledger.Command{Reference: "unihub:" + id, Postings: []ledger.Posting{{Source: Address(source, ""), Destination: Address(target, product), Asset: Asset, Amount: n}}}
	b, _ := json.Marshal(cmd)
	o, e = scanOp(tx.QueryRow(ctx, `INSERT INTO wallet_operations(id,account_id,actor_id,kind,request_key,payload_hash,command) VALUES($1,$2,$3,'admin_transfer',$4,$5,$6) RETURNING `+opColumns, id, source, actor, key, h, b))
	if e != nil {
		return Operation{}, dbError(e)
	}
	if e = audit(ctx, tx, source, actor, id, "transfer.admin"); e != nil {
		return Operation{}, e
	}
	return o, tx.Commit(ctx)
}
