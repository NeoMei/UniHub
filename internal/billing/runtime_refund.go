package billing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
)

type RuntimeRefundRequest struct {
	RequestKey string `json:"request_key"`
	Amount     string `json:"amount"`
	Reason     string `json:"reason"`
}

// RuntimeRefund reverses an already settled original-product charge. Neither
// payer nor asset can be chosen by the caller, and refunds cannot mint credit.
func (s *Service) RuntimeRefund(ctx context.Context, scope RuntimeScope, callID string, r RuntimeRefundRequest) (out RuntimeAdjustment, e error) {
	n, e := runtimeAmount(r.Amount)
	if e != nil || n == 0 || !runtimeText(r.RequestKey) || !runtimeText(r.Reason) {
		return out, ErrInvalid
	}
	call, e := s.runtimeCallOn(ctx, s.pool, scope, callID)
	if e != nil {
		return
	}
	digest := hash(r)
	entity := runtimeID(scope, "refund", call.ID+"\x00"+r.RequestKey)
	e = s.locked(ctx, call.AccountID, func(c *pgxpool.Conn) error {
		old, err := load[RuntimeAdjustment](ctx, c, `SELECT snapshot FROM product_runtime_adjustments WHERE id=$1`, entity)
		if err == nil {
			if old.RequestDigest != digest {
				return ErrConflict
			}
			if err = s.runtimeResume(ctx, c, entity); err != nil {
				return err
			}
			out, err = load[RuntimeAdjustment](ctx, c, `SELECT snapshot FROM product_runtime_adjustments WHERE id=$1`, entity)
			return err
		}
		if !errors.Is(err, accounts.ErrNotFound) {
			return err
		}
		if err = s.clear(ctx, c, call.AccountID); err != nil {
			return err
		}
		call, err = s.runtimeCallOn(ctx, c, scope, callID)
		if err != nil {
			return err
		}
		if call.Status != "SETTLED" {
			return ErrConflict
		}
		charged, err := runtimeAmount(call.Charged)
		refunded := int64(0)
		if call.Refunded != "" {
			refunded, e = runtimeAmount(call.Refunded)
			if e != nil {
				return e
			}
		}
		if err != nil || refunded > charged || n > charged-refunded {
			return ErrConflict
		}
		call.Refunded = str(refunded + n)
		out = RuntimeAdjustment{RuntimeScope: scope, ID: entity, AccountID: call.AccountID, Amount: r.Amount, Status: "PENDING", RequestDigest: digest, Source: encode(map[string]any{"call_id": callID, "request": r})}
		applied := out
		applied.Status = "APPLIED"
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer database.Rollback(tx)
		if _, err = tx.Exec(ctx, `INSERT INTO product_runtime_adjustments(id,account_id,product_id,instance_id,source_kind,source_key,request_digest,snapshot) VALUES($1,$2,$3,$4,'refund',$5,$6,$7)`, entity, call.AccountID, scope.ProductID, scope.InstanceID, call.ID+":"+r.RequestKey, digest, encode(out)); err != nil {
			return err
		}
		posts := []ledger.Posting{{Source: RuntimeAddress(scope, call.AccountID, "consumed"), Destination: RuntimeAddress(scope, call.AccountID, "available"), Asset: RuntimeAsset, Amount: n}}
		cmd, err := s.createCommand(ctx, tx, call.AccountID, call.ActorID, "runtime_refund", entity, posts, effect{RuntimeAdjustment: &applied, RuntimeCall: &call})
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		if err = s.runLocked(ctx, c, cmd.ID); err != nil {
			return err
		}
		out, err = load[RuntimeAdjustment](ctx, c, `SELECT snapshot FROM product_runtime_adjustments WHERE id=$1`, entity)
		return err
	})
	return
}
