package wallet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/money"
)

var ErrConflict = errors.New("idempotency conflict")
var ErrInvalid = errors.New("invalid input")
var ErrPending = errors.New("operation unresolved")

const Asset = "CNY/6"

type Service struct {
	pool     *pgxpool.Pool
	accounts *accounts.Store
	ledger   ledger.Ledger
}
type Operation struct {
	ID            string    `json:"id"`
	AccountID     string    `json:"account_id"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	TransactionID *string   `json:"ledger_transaction_id,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}
type Order struct {
	ID          string  `json:"id"`
	AccountID   string  `json:"account_id"`
	ProductID   string  `json:"product_id"`
	Amount      string  `json:"amount"`
	Currency    string  `json:"currency"`
	Merchant    string  `json:"merchant"`
	OperationID *string `json:"operation_id,omitempty"`
}
type Receipt struct {
	Merchant      string `json:"merchant"`
	TransactionID string `json:"transaction_id"`
	OrderID       string `json:"order_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
}
type Product struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Balance struct {
	ProductID string `json:"product_id"`
	Asset     string `json:"asset"`
	Amount    string `json:"amount"`
}

func New(pool *pgxpool.Pool, a *accounts.Store, l ledger.Ledger) *Service {
	return &Service{pool, a, l}
}
func newID(prefix string) string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b[:])
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validKey(s string) bool { return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s }
func Address(account, product string) string {
	if product == "" {
		return "cash:" + account + ":unified"
	}
	return "cash:" + account + ":product:" + product
}
func dbError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return accounts.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return ErrConflict
	}
	return e
}
func (s *Service) finance(ctx context.Context, q accounts.Querier, actor, account string) error {
	r, e := s.accounts.RoleOn(ctx, q, actor, account)
	if e != nil {
		return e
	}
	if r != "owner" && r != "billing_admin" {
		return accounts.ErrForbidden
	}
	return nil
}
func checkProduct(ctx context.Context, q accounts.Querier, product string) error {
	if product == "" {
		return nil
	}
	var ok bool
	if e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1 AND enabled)`, product).Scan(&ok); e != nil {
		return e
	}
	if !ok {
		return ErrInvalid
	}
	return nil
}
func (s *Service) Products(ctx context.Context) ([]Product, error) {
	rows, e := s.pool.Query(ctx, `SELECT id,name FROM products WHERE enabled ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	p := []Product{}
	for rows.Next() {
		var x Product
		if e = rows.Scan(&x.ID, &x.Name); e != nil {
			return nil, e
		}
		p = append(p, x)
	}
	return p, rows.Err()
}

const opColumns = `id,account_id,kind,status,ledger_transaction_id,error_code,created_at`

func scanOp(row pgx.Row) (Operation, error) {
	var o Operation
	e := row.Scan(&o.ID, &o.AccountID, &o.Kind, &o.Status, &o.TransactionID, &o.ErrorCode, &o.CreatedAt)
	return o, dbError(e)
}
func (s *Service) GetOperation(ctx context.Context, actor, account, id string) (Operation, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Operation{}, e
	}
	return scanOp(s.pool.QueryRow(ctx, `SELECT `+opColumns+` FROM wallet_operations WHERE account_id=$1 AND id=$2`, account, id))
}
func (s *Service) Operations(ctx context.Context, actor, account string) ([]Operation, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	rows, e := s.pool.Query(ctx, `SELECT `+opColumns+` FROM wallet_operations WHERE account_id=$1 ORDER BY created_at DESC,id LIMIT 100`, account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Operation{}
	for rows.Next() {
		o, e := scanOp(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *Service) Balances(ctx context.Context, actor, account string) ([]Balance, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	p, e := s.Products(ctx)
	if e != nil {
		return nil, e
	}
	out := []Balance{}
	for _, x := range append([]Product{{ID: ""}}, p...) {
		n, e := s.ledger.Balance(ctx, Address(account, x.ID), Asset)
		if e != nil {
			return nil, e
		}
		out = append(out, Balance{ProductID: x.ID, Asset: Asset, Amount: strconv.FormatInt(n, 10)})
	}
	return out, nil
}
func audit(ctx context.Context, tx pgx.Tx, account, actor, op, action string) error {
	_, e := tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES($1,$2,$3,$4)`, account, actor, op, action)
	return e
}
func (s *Service) Transfer(ctx context.Context, actor, account, product, amount, key string) (Operation, error) {
	n, e := money.Parse(amount)
	if e != nil || product == "" || !validKey(key) {
		return Operation{}, ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Operation{}, e
	}
	defer database.Rollback(tx)
	if e = database.LockAccountTx(ctx, tx, account); e != nil {
		return Operation{}, e
	}
	if e = s.finance(ctx, tx, actor, account); e != nil {
		return Operation{}, e
	}
	if e = checkProduct(ctx, tx, product); e != nil {
		return Operation{}, e
	}
	h := digest([]string{"transfer", account, product, strconv.FormatInt(n, 10)})
	var old string
	o, e := scanOpWithHash(tx.QueryRow(ctx, `SELECT `+opColumns+`,payload_hash FROM wallet_operations WHERE account_id=$1 AND kind='transfer' AND request_key=$2`, account, key), &old)
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
	cmd := ledger.Command{Reference: "unihub:" + id, Postings: []ledger.Posting{{Source: Address(account, ""), Destination: Address(account, product), Asset: Asset, Amount: n}}}
	b, _ := json.Marshal(cmd)
	o, e = scanOp(tx.QueryRow(ctx, `INSERT INTO wallet_operations(id,account_id,actor_id,kind,request_key,payload_hash,command) VALUES($1,$2,$3,'transfer',$4,$5,$6) RETURNING `+opColumns, id, account, actor, key, h, b))
	if e != nil {
		return Operation{}, dbError(e)
	}
	if e = audit(ctx, tx, account, actor, id, "transfer.created"); e != nil {
		return Operation{}, e
	}
	return o, tx.Commit(ctx)
}
func scanOpWithHash(row pgx.Row, h *string) (Operation, error) {
	var o Operation
	e := row.Scan(&o.ID, &o.AccountID, &o.Kind, &o.Status, &o.TransactionID, &o.ErrorCode, &o.CreatedAt, h)
	return o, dbError(e)
}
func (s *Service) CreateRecharge(ctx context.Context, actor, account, product, amount, key, merchant string) (Order, error) {
	n, e := money.ParseRecharge(amount)
	if e != nil || !validKey(key) || merchant == "" {
		return Order{}, ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Order{}, e
	}
	defer database.Rollback(tx)
	if e = database.LockAccountTx(ctx, tx, account); e != nil {
		return Order{}, e
	}
	if e = s.finance(ctx, tx, actor, account); e != nil {
		return Order{}, e
	}
	if e = checkProduct(ctx, tx, product); e != nil {
		return Order{}, e
	}
	h := digest([]string{account, product, strconv.FormatInt(n, 10), "CNY", merchant})
	var old string
	o, e := scanOrder(tx.QueryRow(ctx, `SELECT id,account_id,COALESCE(product_id,''),amount::text,currency,merchant,operation_id,payload_hash FROM recharge_orders WHERE account_id=$1 AND request_key=$2`, account, key), &old)
	if e == nil {
		if h != old {
			return Order{}, ErrConflict
		}
		return o, tx.Commit(ctx)
	}
	if !errors.Is(e, accounts.ErrNotFound) {
		return Order{}, e
	}
	o, e = scanOrder(tx.QueryRow(ctx, `INSERT INTO recharge_orders(id,account_id,actor_id,product_id,amount,currency,merchant,request_key,payload_hash) VALUES($1,$2,$3,NULLIF($4,''),$5,'CNY',$6,$7,$8) RETURNING id,account_id,COALESCE(product_id,''),amount::text,currency,merchant,operation_id,payload_hash`, newID("re_"), account, actor, product, n, merchant, key, h), &old)
	if e != nil {
		return Order{}, dbError(e)
	}
	if e = audit(ctx, tx, account, actor, o.ID, "recharge.created"); e != nil {
		return Order{}, e
	}
	return o, tx.Commit(ctx)
}
func scanOrder(row pgx.Row, h *string) (Order, error) {
	var o Order
	e := row.Scan(&o.ID, &o.AccountID, &o.ProductID, &o.Amount, &o.Currency, &o.Merchant, &o.OperationID, h)
	return o, dbError(e)
}
func (s *Service) GetOrder(ctx context.Context, actor, account, id string) (Order, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Order{}, e
	}
	var h string
	return scanOrder(s.pool.QueryRow(ctx, `SELECT id,account_id,COALESCE(product_id,''),amount::text,currency,merchant,operation_id,payload_hash FROM recharge_orders WHERE account_id=$1 AND id=$2`, account, id), &h)
}

// Receive accepts a receipt only after the HTTP payment adapter authenticates it.
// It persists the receipt and posting command together; no balance changes here.
func (s *Service) Receive(ctx context.Context, r Receipt) (Operation, error) {
	n, e := money.ParseRecharge(r.Amount)
	if e != nil || r.Status != "SUCCEEDED" || r.Currency != "CNY" || !validKey(r.TransactionID) {
		return Operation{}, ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Operation{}, e
	}
	defer database.Rollback(tx)
	var ignored, actor string
	o, e := scanOrder(tx.QueryRow(ctx, `SELECT id,account_id,COALESCE(product_id,''),amount::text,currency,merchant,operation_id,payload_hash FROM recharge_orders WHERE id=$1 FOR UPDATE`, r.OrderID), &ignored)
	if e != nil {
		return Operation{}, e
	}
	if o.Amount != strconv.FormatInt(n, 10) || o.Currency != r.Currency || o.Merchant != r.Merchant {
		return Operation{}, ErrInvalid
	}
	h := digest(r)
	if o.OperationID != nil {
		var saved string
		e = tx.QueryRow(ctx, `SELECT payload_hash FROM payment_receipts WHERE order_id=$1`, o.ID).Scan(&saved)
		if e != nil {
			return Operation{}, e
		}
		if saved != h {
			return Operation{}, ErrConflict
		}
		return scanOp(tx.QueryRow(ctx, `SELECT `+opColumns+` FROM wallet_operations WHERE id=$1`, *o.OperationID))
	}
	if e = tx.QueryRow(ctx, `SELECT actor_id FROM recharge_orders WHERE id=$1`, o.ID).Scan(&actor); e != nil {
		return Operation{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO payment_receipts(merchant,transaction_id,order_id,payload_hash) VALUES($1,$2,$3,$4)`, r.Merchant, r.TransactionID, o.ID, h); e != nil {
		return Operation{}, dbError(e)
	}
	id := newID("op_")
	cmd := ledger.Command{Reference: "unihub:" + id, Postings: []ledger.Posting{{Source: "world", Destination: Address(o.AccountID, o.ProductID), Asset: Asset, Amount: n}}}
	b, _ := json.Marshal(cmd)
	op, e := scanOp(tx.QueryRow(ctx, `INSERT INTO wallet_operations(id,account_id,actor_id,kind,request_key,payload_hash,command) VALUES($1,$2,$3,'recharge',$4,$5,$6) RETURNING `+opColumns, id, o.AccountID, actor, "receipt:"+o.ID, h, b))
	if e != nil {
		return Operation{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE recharge_orders SET operation_id=$2 WHERE id=$1`, o.ID, id); e != nil {
		return Operation{}, e
	}
	if e = audit(ctx, tx, o.AccountID, actor, id, "payment.verified"); e != nil {
		return Operation{}, e
	}
	return op, tx.Commit(ctx)
}
func (s *Service) Audit(ctx context.Context, actor, account string) ([]map[string]any, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	rows, e := s.pool.Query(ctx, `SELECT id,actor_id,operation_id,action,created_at FROM audit_events WHERE account_id=$1 ORDER BY id DESC LIMIT 100`, account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var a, op *string
		var action string
		var at time.Time
		if e = rows.Scan(&id, &a, &op, &action, &at); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": fmt.Sprint(id), "actor_id": a, "operation_id": op, "action": action, "created_at": at})
	}
	return out, rows.Err()
}
