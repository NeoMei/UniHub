package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
)

type effect struct {
	RuntimeAttempt       int                `json:"runtime_attempt,omitempty"`
	RuntimeAcceptedCall  bool               `json:"runtime_accepted_call,omitempty"`
	RuntimeCall          *RuntimeCall       `json:"runtime_call,omitempty"`
	RuntimeAdjustment    *RuntimeAdjustment `json:"runtime_adjustment,omitempty"`
	RuntimeRequestDigest string             `json:"runtime_request_digest,omitempty"`
	Order                *Order             `json:"order,omitempty"`
	Subscription         *Subscription      `json:"subscription,omitempty"`
	Batches              []Batch            `json:"batches,omitempty"`
	Call                 *Call              `json:"call,omitempty"`
	Finance              bool               `json:"finance"`
}
type command struct {
	ID, Account, Actor, Kind, Key, Hash, Status string
	Ledger                                      ledger.Command
	Effect                                      effect
}

func (s *Service) createCommand(ctx context.Context, tx pgx.Tx, account, actor, kind, key string, posts []ledger.Posting, eff effect) (command, error) {
	c := command{ID: id("bc_"), Account: account, Actor: actor, Kind: kind, Key: key, Effect: eff, Status: "CREATED"}
	c.Ledger = ledger.Command{Reference: "unihub:billing:" + c.ID, Postings: posts}
	c.Hash = hash(struct {
		P []ledger.Posting
		E effect
	}{posts, eff})
	_, e := tx.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'CREATED')`, c.ID, c.Account, c.Actor, c.Kind, c.Key, c.Hash, encode(c.Ledger), encode(c.Effect))
	if e == nil {
		e = event(ctx, tx, account, actor, c.ID, kind+".created", map[string]string{"digest": c.Hash})
	}
	return c, e
}
func readCommand(ctx context.Context, q accounts.Querier, id string) (command, error) {
	var c command
	var a, b []byte
	e := q.QueryRow(ctx, `SELECT id,account_id,actor_id,kind,request_key,request_digest,status,command,effect FROM billing_commands WHERE id=$1`, id).Scan(&c.ID, &c.Account, &c.Actor, &c.Kind, &c.Key, &c.Hash, &c.Status, &a, &b)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(a, &c.Ledger); e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c.Effect)
	return c, e
}
func saveOrder(ctx context.Context, tx pgx.Tx, o Order) error {
	_, e := tx.Exec(ctx, `UPDATE billing_orders SET snapshot=$2 WHERE id=$1`, o.ID, encode(o))
	return e
}
func saveCall(ctx context.Context, tx pgx.Tx, c Call) error {
	_, e := tx.Exec(ctx, `UPDATE billing_calls SET snapshot=$2 WHERE id=$1`, c.CallID, encode(c))
	return e
}
func saveBatch(ctx context.Context, tx pgx.Tx, b Batch) error {
	b.Available = ""
	b.Reserved = ""
	b.Consumed = ""
	b.Expired = ""
	b.Withdrawn = ""
	_, e := tx.Exec(ctx, `INSERT INTO billing_batches(id,account_id,order_id,source_key,snapshot) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET snapshot=excluded.snapshot`, b.ID, b.AccountID, b.OrderID, b.SourceKey, encode(b))
	return e
}
func saveSubscription(ctx context.Context, tx pgx.Tx, sub Subscription) error {
	_, e := tx.Exec(ctx, `INSERT INTO billing_subscriptions(id,account_id,product_id,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT(account_id,product_id) DO UPDATE SET id=excluded.id,snapshot=excluded.snapshot`, sub.ID, sub.AccountID, sub.ProductID, encode(sub))
	return e
}
func (s *Service) apply(ctx context.Context, c *pgxpool.Conn, x command, ledgerID string) error {
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if x.Effect.RuntimeCall != nil {
		if e = saveRuntimeCall(ctx, tx, *x.Effect.RuntimeCall); e != nil {
			return e
		}
	}
	if x.Effect.RuntimeAdjustment != nil {
		if e = saveRuntimeAdjustment(ctx, tx, *x.Effect.RuntimeAdjustment); e != nil {
			return e
		}
	}
	if x.Effect.Order != nil {
		if e = saveOrder(ctx, tx, *x.Effect.Order); e != nil {
			return e
		}
	}
	if x.Effect.Subscription != nil {
		if e = saveSubscription(ctx, tx, *x.Effect.Subscription); e != nil {
			return e
		}
	}
	for _, b := range x.Effect.Batches {
		if e = saveBatch(ctx, tx, b); e != nil {
			return e
		}
	}
	if x.Effect.Call != nil {
		if e = saveCall(ctx, tx, *x.Effect.Call); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE billing_commands SET status='APPLIED',ledger_transaction_id=$2 WHERE id=$1`, x.ID, ledgerID); e != nil {
		return e
	}
	if e = event(ctx, tx, x.Account, x.Actor, x.ID, x.Kind+".applied", map[string]string{"ledger_transaction_id": ledgerID}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) reject(ctx context.Context, c *pgxpool.Conn, x command, status, reason string) error {
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if x.Kind == "runtime_reserve" && x.Effect.RuntimeCall != nil {
		call := *x.Effect.RuntimeCall
		call.Status = "REJECTED"
		if e = saveRuntimeCall(ctx, tx, call); e != nil {
			return e
		}
	}
	if x.Effect.RuntimeAdjustment != nil {
		adjustment := *x.Effect.RuntimeAdjustment
		adjustment.Status = status
		if e = saveRuntimeAdjustment(ctx, tx, adjustment); e != nil {
			return e
		}
	}
	if x.Kind == "payment" && x.Effect.Order != nil {
		o := *x.Effect.Order
		o.PaymentStatus = "REJECTED"
		o.FulfillmentStatus = "CANCELED"
		if e = saveOrder(ctx, tx, o); e != nil {
			return e
		}
		if e = s.releaseBudget(ctx, tx, o); e != nil {
			return e
		}
	}
	if x.Kind == "refund" && x.Effect.Order != nil {
		o := *x.Effect.Order
		o.RefundStatus = "MANUAL_REVIEW"
		o.RefundReason = reason
		if e = saveOrder(ctx, tx, o); e != nil {
			return e
		}
	}
	if (x.Kind == "reserve" || x.Kind == "topup") && x.Effect.Call != nil {
		call := *x.Effect.Call
		if x.Kind == "topup" {
			call, e = load[Call](ctx, tx, `SELECT snapshot FROM billing_calls WHERE id=$1`, call.CallID)
			if e != nil {
				return e
			}
		} else {
			call.Status = "REJECTED"
		}
		if e = saveCall(ctx, tx, call); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE billing_commands SET status=$2,error_code=$3 WHERE id=$1`, x.ID, status, reason)
	if e != nil {
		return e
	}
	if e = event(ctx, tx, x.Account, x.Actor, x.ID, x.Kind+"."+status, map[string]string{"reason": reason}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// runLocked persists SUBMITTING before external IO. Recovery never changes the
// immutable posting payload and verifies lookup results before applying effects.
func (s *Service) runLocked(ctx context.Context, c *pgxpool.Conn, cmdID string) error {
	x, e := readCommand(ctx, c, cmdID)
	if e != nil {
		return e
	}
	if x.Status == "APPLIED" || x.Status == "REJECTED" || x.Status == "CANCELED" {
		return nil
	}
	if x.Status == "CREATED" {
		if (x.Kind == "runtime_reserve" || x.Kind == "runtime_topup") && x.Effect.RuntimeCall != nil {
			call := x.Effect.RuntimeCall
			if call.ParentCallID != "" {
				if _, e = s.runtimeAcceptedParent(ctx, c, call.RuntimeScope, call.RuntimeReserveRequest); e != nil {
					return s.reject(ctx, c, x, "CANCELED", "original_obligation_mismatch")
				}
			} else if !x.Effect.RuntimeAcceptedCall {
				if _, e = s.runtimeAuthority(ctx, c, call.RuntimeScope, call.LocalUserID, call.AccountID); e != nil {
					return s.reject(ctx, c, x, "CANCELED", "product_authorization_revoked")
				}
			}
		}
		if x.Kind == "runtime_gift" && x.Effect.RuntimeAdjustment != nil {
			a := x.Effect.RuntimeAdjustment
			if s.productUsers == nil {
				return s.reject(ctx, c, x, "CANCELED", "product_authorization_revoked")
			}
			tx, beginError := c.Begin(ctx)
			if beginError != nil {
				return beginError
			}
			if e = s.productUsers.RequireRuntimeCreditOn(ctx, tx, x.Actor, a.ProductID, a.InstanceID, a.AccountID); e != nil {
				database.Rollback(tx)
				return s.reject(ctx, c, x, "CANCELED", "product_authorization_revoked")
			}
			// This is the authorization linearization point. Once SUBMITTING,
			// recovery must honor the accepted immutable financial obligation.
			if _, e = tx.Exec(ctx, `UPDATE billing_commands SET status='SUBMITTING' WHERE id=$1`, x.ID); e != nil {
				database.Rollback(tx)
				return e
			}
			if e = tx.Commit(ctx); e != nil {
				return e
			}
			x.Status = "SUBMITTING"
		}
		if x.Effect.Finance {
			if e = s.finance(ctx, c, x.Actor, x.Account); e != nil {
				return s.reject(ctx, c, x, "CANCELED", "authorization_revoked")
			}
		}
		if x.Kind == "payment" && x.Effect.Order != nil {
			if e = s.validatePaymentAuthorization(ctx, c, *x.Effect.Order); e != nil {
				return s.reject(ctx, c, x, "CANCELED", "funding_or_renewal_revoked")
			}
		}
		if (x.Kind == "reserve" || x.Kind == "topup") && x.Effect.Call != nil {
			call := x.Effect.Call
			if e = s.trust(call.ServiceID); e != nil {
				return s.reject(ctx, c, x, "CANCELED", "service_revoked")
			}
			if e = s.callFundingAllowed(ctx, c, *call, s.now().UTC()); e != nil {
				return s.reject(ctx, c, x, "CANCELED", "membership_revoked")
			}
			if e = s.validateNewAllocation(ctx, c, x); e != nil {
				return s.reject(ctx, c, x, "CANCELED", "batch_no_longer_eligible")
			}
		}
	}
	if x.Status == "CREATED" {
		if _, e = c.Exec(ctx, `UPDATE billing_commands SET status='SUBMITTING' WHERE id=$1`, x.ID); e != nil {
			return e
		}
	}
	if len(x.Ledger.Postings) == 0 {
		return s.apply(ctx, c, x, "")
	}
	found, e := s.ledger.Find(ctx, x.Ledger.Reference)
	if e != nil {
		return s.unknown(ctx, c, x, e)
	}
	var tr ledger.Transaction
	if found != nil {
		tr = *found
	} else {
		tr, e = s.ledger.Post(ctx, x.Ledger)
		if errors.Is(e, ledger.ErrInsufficientFunds) && x.Status == "CREATED" {
			if e = s.reject(ctx, c, x, "REJECTED", "insufficient_funds"); e != nil {
				return e
			}
			return ledger.ErrInsufficientFunds
		}
		if e != nil {
			return s.unknown(ctx, c, x, e)
		}
	}
	if tr.ID == "" || tr.Reference != x.Ledger.Reference || !reflect.DeepEqual(tr.Postings, x.Ledger.Postings) {
		return s.unknown(ctx, c, x, ledger.ErrConflict)
	}
	return s.apply(ctx, c, x, tr.ID)
}
func (s *Service) unknown(ctx context.Context, c *pgxpool.Conn, x command, cause error) error {
	_, e := c.Exec(ctx, `UPDATE billing_commands SET status='UNKNOWN',error_code=$2 WHERE id=$1`, x.ID, fmt.Sprint(cause))
	if e != nil {
		return e
	}
	return ErrPending
}
func (s *Service) Recover(ctx context.Context) (MaintenanceResult, error) {
	var out MaintenanceResult
	var failures error
	rows, e := s.pool.Query(ctx, `SELECT id,account_id FROM billing_commands WHERE status IN ('CREATED','SUBMITTING','UNKNOWN') ORDER BY created_at,id LIMIT 500`)
	if e != nil {
		return out, e
	}
	type item struct{ id, account string }
	items := []item{}
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.id, &x.account); e != nil {
			rows.Close()
			return out, e
		}
		items = append(items, x)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return out, e
	}
	for _, x := range items {
		e = s.locked(ctx, x.account, func(c *pgxpool.Conn) error { return s.runLocked(ctx, c, x.id) })
		if errors.Is(e, ErrPending) {
			out.Pending++
			continue
		}
		if e != nil && !errors.Is(e, ledger.ErrInsufficientFunds) {
			out.Pending++
			failures = errors.Join(failures, fmt.Errorf("recover %s: %w", x.id, e))
			continue
		}
		out.Processed++
	}
	orders, e := list[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders WHERE snapshot->>'payment_status'='PAID' AND snapshot->>'fulfillment_status'='PENDING' ORDER BY id LIMIT 500`)
	if e != nil {
		return out, e
	}
	for _, o := range orders {
		e = s.locked(ctx, o.AccountID, func(c *pgxpool.Conn) error { return s.fulfillLocked(ctx, c, o.ID, s.now().UTC()) })
		if errors.Is(e, ErrPending) {
			out.Pending++
		} else if e != nil {
			out.Pending++
			failures = errors.Join(failures, e)
		} else {
			out.Processed++
		}
	}
	refunds, e := list[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders WHERE snapshot->>'refund_status'='FROZEN' ORDER BY id LIMIT 500`)
	if e != nil {
		return out, e
	}
	for _, o := range refunds {
		_, e = s.RequestRefund(ctx, o.RefundActorID, o.AccountID, o.ID, o.RefundKey)
		if errors.Is(e, ErrPending) {
			out.Pending++
		} else if errors.Is(e, ErrManualReview) || errors.Is(e, accounts.ErrForbidden) {
			out.Pending++
		} else if e != nil {
			out.Pending++
			failures = errors.Join(failures, e)
		} else {
			out.Processed++
		}
	}
	return out, failures
}
func (s *Service) Maintain(ctx context.Context, now time.Time) (MaintenanceResult, error) {
	out, failures := s.Recover(ctx)
	var e error
	subs, e := list[Subscription](ctx, s.pool, `SELECT snapshot FROM billing_subscriptions ORDER BY id`)
	if e != nil {
		return out, e
	}
	for _, sub := range subs {
		e = s.locked(ctx, sub.AccountID, func(c *pgxpool.Conn) error {
			if e := s.clear(ctx, c, sub.AccountID); e != nil {
				return e
			}
			current, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1`, sub.ID)
			if e != nil {
				return e
			}
			if current.Status != "active" {
				return nil
			}
			if !now.Before(current.PaidThrough) {
				return s.renewLocked(ctx, c, current, now.UTC())
			}
			return s.monthlyLocked(ctx, c, current, now.UTC())
		})
		if errors.Is(e, ErrPending) {
			out.Pending++
		} else if e != nil && !errors.Is(e, ledger.ErrInsufficientFunds) && !errors.Is(e, accounts.ErrForbidden) {
			out.Pending++
			failures = errors.Join(failures, e)
		} else {
			out.Processed++
		}
	}
	bs, e := list[Batch](ctx, s.pool, `SELECT snapshot FROM billing_batches WHERE (snapshot->>'expires_at')::timestamptz <= $1 ORDER BY id`, now)
	if e != nil {
		return out, e
	}
	for _, b := range bs {
		e = s.locked(ctx, b.AccountID, func(c *pgxpool.Conn) error { return s.expireLocked(ctx, c, b, now) })
		if errors.Is(e, ErrPending) {
			out.Pending++
		} else if e != nil {
			out.Pending++
			failures = errors.Join(failures, e)
		}
	}
	return out, failures
}

func (s *Service) validateNewAllocation(ctx context.Context, q accounts.Querier, x command) error {
	if callAsset(*x.Effect.Call) == CashAsset {
		return s.validateCashAllocation(ctx, q, x)
	}
	now := s.now().UTC()
	for _, p := range x.Ledger.Postings {
		matched := false
		for _, a := range x.Effect.Call.Allocations {
			if p.Source != BatchAddress(a.BatchID, "available") {
				continue
			}
			matched = true
			b, e := load[Batch](ctx, q, `SELECT snapshot FROM billing_batches WHERE id=$1`, a.BatchID)
			if e != nil {
				return e
			}
			if b.AccountID != x.Account || b.Frozen || now.Before(b.StartsAt) || !now.Before(b.ExpiresAt) {
				return ErrInvalid
			}
			break
		}
		if !matched {
			return ErrInvalid
		}
	}
	return nil
}
