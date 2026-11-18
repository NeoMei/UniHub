package billing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/wallet"
)

var ErrLegacyCreditAdoptionBlocked = errors.New("billing: legacy credit adoption blocked")

type LegacyCreditAdoptionRequest struct {
	ActorID   string   `json:"actor_id"`
	AccountID string   `json:"account_id"`
	ProductID string   `json:"product_id"`
	BatchIDs  []string `json:"batch_ids"`
}

type LegacyCashBalances struct {
	Asset   string `json:"asset"`
	Unified string `json:"unified"`
	Product string `json:"product"`
}

type LegacyCreditAdoptionBatch struct {
	BatchID string             `json:"batch_id"`
	State   string             `json:"state"`
	Reason  string             `json:"reason,omitempty"`
	Batch   *Batch             `json:"batch,omitempty"`
	Proof   *LegacyCreditProof `json:"proof,omitempty"`
}

type LegacyCreditAdoptionPlan struct {
	Request        LegacyCreditAdoptionRequest `json:"request"`
	Asset          string                      `json:"asset"`
	Digest         string                      `json:"digest"`
	ProfileVersion int64                       `json:"profile_version"`
	CanApply       bool                        `json:"can_apply"`
	Blockers       []string                    `json:"blockers"`
	Cash           LegacyCashBalances          `json:"cash_retained"`
	Batches        []LegacyCreditAdoptionBatch `json:"batches"`
	ActiveTerm     *LegacyCreditAdoptionTerm   `json:"active_legacy_term,omitempty"`
}

type LegacyCreditAdoptionTerm struct {
	Subscription    Subscription      `json:"subscription"`
	InstallmentFrom time.Time         `json:"installment_from"`
	InstallmentTo   time.Time         `json:"installment_to"`
	PaidOrderID     string            `json:"paid_order_id"`
	PaidOrderDigest string            `json:"paid_order_digest"`
	GrantBatchID    string            `json:"grant_batch_id,omitempty"`
	Proof           LegacyCreditProof `json:"proof"`
}

type LegacyCreditAdoptionReceipt struct {
	Plan         LegacyCreditAdoptionPlan `json:"plan"`
	AppliedAt    time.Time                `json:"applied_at"`
	CashAfter    LegacyCashBalances       `json:"cash_after"`
	CreditsAfter []Batch                  `json:"credits_after"`
}

type LegacyCreditAdoptionVerification struct {
	Receipt LegacyCreditAdoptionReceipt `json:"receipt"`
	Current []Batch                     `json:"current_batches"`
	Cash    LegacyCashBalances          `json:"current_cash"`
}

type storedLegacyCreditBatch struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	OrderID   string `json:"order_id"`
	SourceKey string `json:"source_key"`
	Batch     Batch  `json:"snapshot"`
}

func canonicalLegacyCreditAdoptionRequest(r LegacyCreditAdoptionRequest) (LegacyCreditAdoptionRequest, error) {
	if !keyOK(r.ActorID) || !keyOK(r.AccountID) || !billingProductID.MatchString(r.ProductID) || len(r.BatchIDs) > 100 {
		return r, ErrInvalid
	}
	r.BatchIDs = append([]string{}, r.BatchIDs...)
	slices.Sort(r.BatchIDs)
	for i, id := range r.BatchIDs {
		if !keyOK(id) || i > 0 && r.BatchIDs[i-1] == id {
			return r, ErrInvalid
		}
	}
	return r, nil
}

func (s *Service) PreviewLegacyCreditAdoption(ctx context.Context, request LegacyCreditAdoptionRequest) (LegacyCreditAdoptionPlan, error) {
	r, e := canonicalLegacyCreditAdoptionRequest(request)
	if e != nil {
		return LegacyCreditAdoptionPlan{}, e
	}
	var out LegacyCreditAdoptionPlan
	e = s.locked(ctx, r.AccountID, func(c *pgxpool.Conn) error {
		tx, e := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if e = s.legacyCreditAuthority(ctx, tx, r); e != nil {
			return e
		}
		out, e = s.legacyCreditAdoptionPlanOn(ctx, tx, r)
		return e
	})
	return out, e
}

func (s *Service) legacyCreditAuthority(ctx context.Context, q accounts.Querier, r LegacyCreditAdoptionRequest) error {
	if s.ledger == nil {
		return ErrUnsupported
	}
	if e := s.finance(ctx, q, r.ActorID, r.AccountID); e != nil {
		return e
	}
	var active bool
	if e := q.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, r.ActorID).Scan(&active); e != nil {
		return e
	}
	if !active {
		return accounts.ErrForbidden
	}
	return nil
}

func (s *Service) legacyCashBalances(ctx context.Context, r LegacyCreditAdoptionRequest) (LegacyCashBalances, error) {
	out := LegacyCashBalances{Asset: CashAsset}
	unified, e := s.ledger.Balance(ctx, wallet.Address(r.AccountID, ""), CashAsset)
	if e != nil {
		return out, e
	}
	productBalance, e := s.ledger.Balance(ctx, wallet.Address(r.AccountID, r.ProductID), CashAsset)
	if e != nil {
		return out, e
	}
	out.Unified, out.Product = str(unified), str(productBalance)
	return out, nil
}

func (s *Service) legacyCreditAdoptionPlanOn(ctx context.Context, tx pgx.Tx, r LegacyCreditAdoptionRequest) (LegacyCreditAdoptionPlan, error) {
	out := LegacyCreditAdoptionPlan{Request: r, Asset: CreditAsset, Blockers: []string{}, Batches: []LegacyCreditAdoptionBatch{}}
	profile, e := s.ProfileOn(ctx, tx, r.ProductID)
	if e != nil && !errors.Is(e, ErrInvalid) {
		return out, e
	}
	out.ProfileVersion = profile.Version
	ready, e := s.nativeCreditReadinessForProfile(ctx, tx, profile, ProductCreditScopeV1)
	if e != nil {
		return out, e
	}
	if !ready.Ready {
		out.Blockers = append(out.Blockers, "product_credit_scope_unavailable")
	}
	for _, check := range []struct {
		err    error
		reason string
	}{{s.clear(ctx, tx, r.AccountID), "unresolved_account_command"}, {s.commercialClear(ctx, tx, r.AccountID), "unresolved_commercial_order"}} {
		if errors.Is(check.err, ErrPending) {
			out.Blockers = append(out.Blockers, check.reason)
		} else if check.err != nil {
			return out, check.err
		}
	}
	now := s.now().UTC()
	term, termBlocker, e := s.legacyTermAdoptionProof(ctx, tx, r, now)
	if e != nil {
		return out, e
	}
	if termBlocker != "" {
		out.Blockers = append(out.Blockers, termBlocker)
	}
	out.ActiveTerm = term
	var unsettledCash bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM cash_checkouts c WHERE c.account_id=$1 AND (
		c.snapshot->>'payment_status' IN ('UNKNOWN','CREATED','PENDING','REVIEW') OR
		(c.snapshot->>'payment_status'='PAID' AND NOT EXISTS (
			SELECT 1 FROM wallet_operations w WHERE w.account_id=c.account_id AND w.id=c.snapshot->>'operation_id' AND w.status='APPLIED'
		)))
	)`, r.AccountID).Scan(&unsettledCash); e != nil {
		return out, e
	}
	if unsettledCash {
		out.Blockers = append(out.Blockers, "unresolved_cash_checkout")
	}
	out.Cash, e = s.legacyCashBalances(ctx, r)
	if e != nil {
		return out, e
	}
	_, unifiedErr := integer(out.Cash.Unified, false)
	_, productErr := integer(out.Cash.Product, false)
	if unifiedErr != nil || productErr != nil {
		out.Blockers = append(out.Blockers, "cash_reconciliation_required")
	}
	stored, e := list[storedLegacyCreditBatch](ctx, tx, `SELECT to_jsonb(b) FROM billing_batches b WHERE account_id=$1 AND (cardinality($2::text[])=0 OR id=ANY($2)) ORDER BY id`, r.AccountID, r.BatchIDs)
	if e != nil {
		return out, e
	}
	seen := make(map[string]bool, len(stored))
	for _, row := range stored {
		seen[row.ID] = true
		entry, e := s.classifyLegacyCreditBatch(ctx, tx, r, row, now)
		if e != nil {
			return out, e
		}
		out.Batches = append(out.Batches, entry)
	}
	for _, id := range r.BatchIDs {
		if !seen[id] {
			out.Batches = append(out.Batches, LegacyCreditAdoptionBatch{BatchID: id, State: "blocked", Reason: "batch_not_found_for_payer"})
		}
	}
	slices.SortFunc(out.Batches, func(a, b LegacyCreditAdoptionBatch) int { return strings.Compare(a.BatchID, b.BatchID) })
	out.CanApply = len(r.BatchIDs) > 0 && len(out.Blockers) == 0
	for _, b := range out.Batches {
		out.CanApply = out.CanApply && b.State == "eligible"
	}
	out.Digest = hash(out)
	return out, nil
}

// Paid legacy installments stay on their anchored term. A selective cutover
// cannot hide installments which the old subscription has yet to grant.
func (s *Service) legacyTermAdoptionProof(ctx context.Context, q dbq, r LegacyCreditAdoptionRequest, now time.Time) (*LegacyCreditAdoptionTerm, string, error) {
	var id string
	var snapshot []byte
	e := q.QueryRow(ctx, `SELECT id,snapshot FROM billing_subscriptions WHERE account_id=$1 AND product_id=$2`, r.AccountID, r.ProductID).Scan(&id, &snapshot)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if e != nil {
		return nil, "", e
	}
	var sub Subscription
	if e = json.Unmarshal(snapshot, &sub); e != nil {
		return nil, "", e
	}
	if sub.Offer.CreditScope != "" || sub.Status != "active" {
		return nil, "", nil
	}
	if sub.ID == "" || sub.ID != id || sub.Version < 1 || sub.AccountID != r.AccountID || sub.ProductID != r.ProductID ||
		sub.Offer.ProductID != r.ProductID || sub.Offer.Kind != "membership" || sub.Anchor.IsZero() || !sub.Anchor.Before(sub.PaidThrough) {
		return nil, "legacy_paid_term_unverified", nil
	}
	var term *LegacyCreditAdoptionTerm
	if now.Before(sub.PaidThrough) {
		amount, e := integer(sub.Offer.MonthlyCredit, false)
		start, end, valid := sub.Offer.currentPeriod(sub.Anchor, sub.PaidThrough, now)
		if e != nil || !valid {
			return nil, "legacy_paid_term_unverified", nil
		}
		if amount != 0 && end.Before(sub.PaidThrough) {
			return nil, "legacy_paid_future_installments", nil
		}
		term, e = s.verifyLegacyTermInstallment(ctx, q, sub, start, end, amount)
		var blocked legacyCreditProofError
		if errors.Is(e, accounts.ErrNotFound) || errors.As(e, &blocked) {
			return nil, "legacy_paid_term_unverified", nil
		}
		if e != nil {
			return nil, "", e
		}
	}
	a, e := load[Authorization](ctx, q, `SELECT snapshot FROM billing_authorizations WHERE account_id=$1 AND product_id=$2 AND purpose='renewal'`, r.AccountID, r.ProductID)
	if errors.Is(e, accounts.ErrNotFound) {
		return term, "", nil
	}
	if e != nil {
		return nil, "", e
	}
	if a.Enabled && now.Before(a.ExpiresAt) {
		return term, "legacy_renewal_mandate_enabled", nil
	}
	return term, "", nil
}

func (s *Service) classifyLegacyCreditBatch(ctx context.Context, tx pgx.Tx, r LegacyCreditAdoptionRequest, row storedLegacyCreditBatch, now time.Time) (LegacyCreditAdoptionBatch, error) {
	entry := LegacyCreditAdoptionBatch{BatchID: row.ID, State: "blocked"}
	b := row.Batch
	if b.ID != row.ID || b.AccountID != row.AccountID || b.OrderID == "" || b.OrderID != row.OrderID || b.SourceKey == "" || b.SourceKey != row.SourceKey || b.ProductID == "" || !b.StartsAt.Before(b.ExpiresAt) {
		entry.Reason = "unverified_batch_identity_or_validity"
		return entry, nil
	}
	if b.ProductID != r.ProductID {
		entry.State, entry.Reason = "retained", "different_origin_product"
		return entry, nil
	}
	if e := s.quantities(ctx, &b); e != nil {
		return entry, e
	}
	entry.Batch = &b
	if !closedCreditBatch(b) {
		entry.Reason = "credit_quantity_mismatch"
		return entry, nil
	}
	switch {
	case b.CreditScope == ProductCreditScopeV1:
		entry.State, entry.Reason = "retained", "already_product_scoped"
	case b.CreditScope != "":
		entry.Reason = "unknown_credit_scope"
	case b.Frozen || b.Reserved != "0":
		entry.Reason = "frozen_or_reserved_batch"
	case !now.Before(b.ExpiresAt):
		entry.State, entry.Reason = "retained", "expired_batch"
	case b.Available == "0":
		entry.State, entry.Reason = "retained", "no_unspent_credits"
	default:
		var openCall bool
		needle := encode([]map[string]string{{"batch_id": b.ID}})
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_calls WHERE account_id=$1 AND COALESCE(snapshot->>'status','') NOT IN ('SETTLED','RELEASED','REJECTED') AND snapshot->'allocations' @> $2::jsonb)`, r.AccountID, needle).Scan(&openCall); e != nil {
			return entry, e
		}
		if openCall {
			entry.Reason = "unresolved_legacy_call"
			return entry, nil
		}
		proof, e := s.legacyCreditProof(ctx, tx, b)
		var blocked legacyCreditProofError
		if errors.As(e, &blocked) {
			entry.Reason = string(blocked)
			return entry, nil
		}
		if e != nil {
			return entry, e
		}
		entry.State, entry.Proof = "eligible", &proof
	}
	return entry, nil
}

func closedCreditBatch(b Batch) bool {
	left, e := integer(b.Original, true)
	if e != nil {
		return false
	}
	for _, v := range []string{b.Available, b.Reserved, b.Consumed, b.Expired, b.Withdrawn} {
		n, e := integer(v, false)
		if e != nil || n > left {
			return false
		}
		left -= n
	}
	return left == 0
}

func (s *Service) ApplyLegacyCreditAdoption(ctx context.Context, request LegacyCreditAdoptionRequest, digest string) (LegacyCreditAdoptionVerification, error) {
	return s.runLegacyCreditAdoption(ctx, request, digest, true)
}

func (s *Service) VerifyLegacyCreditAdoption(ctx context.Context, request LegacyCreditAdoptionRequest, digest string) (LegacyCreditAdoptionVerification, error) {
	return s.runLegacyCreditAdoption(ctx, request, digest, false)
}

func (s *Service) runLegacyCreditAdoption(ctx context.Context, request LegacyCreditAdoptionRequest, digest string, apply bool) (LegacyCreditAdoptionVerification, error) {
	var out LegacyCreditAdoptionVerification
	r, e := canonicalLegacyCreditAdoptionRequest(request)
	if e != nil || len(r.BatchIDs) == 0 || !nativeDigest.MatchString(digest) {
		return out, ErrInvalid
	}
	e = s.locked(ctx, r.AccountID, func(c *pgxpool.Conn) error {
		opts := pgx.TxOptions{AccessMode: pgx.ReadOnly}
		if apply {
			opts.AccessMode = pgx.ReadWrite
		}
		tx, e := c.BeginTx(ctx, opts)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if e = s.legacyCreditAuthority(ctx, tx, r); e != nil {
			return e
		}
		saved, e := load[LegacyCreditAdoptionReceipt](ctx, tx, `SELECT receipt FROM billing_legacy_credit_adoption_runs WHERE plan_digest=$1 AND account_id=$2 AND product_id=$3 AND actor_id=$4`, digest, r.AccountID, r.ProductID, r.ActorID)
		if e == nil {
			if saved.Plan.Digest != digest {
				return ErrConflict
			}
			out, e = s.verifyLegacyCreditAdoptionOn(ctx, tx, r, saved)
			return e
		}
		if !apply || !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		if e = lockBillingProduct(ctx, tx, r.ProductID); e != nil {
			return e
		}
		plan, e := s.legacyCreditAdoptionPlanOn(ctx, tx, r)
		if e != nil {
			return e
		}
		if !plan.CanApply {
			return ErrLegacyCreditAdoptionBlocked
		}
		if plan.Digest != digest {
			return ErrConflict
		}
		receipt := LegacyCreditAdoptionReceipt{Plan: plan, AppliedAt: s.now().UTC(), CreditsAfter: []Batch{}}
		for _, entry := range plan.Batches {
			b := *entry.Batch
			tag, e := tx.Exec(ctx, `UPDATE billing_batches SET snapshot=jsonb_set(snapshot,'{credit_scope}',to_jsonb($4::text)) WHERE id=$1 AND account_id=$2 AND order_id=$3 AND COALESCE(snapshot->>'credit_scope','')=''`, b.ID, r.AccountID, b.OrderID, ProductCreditScopeV1)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			b.CreditScope = ProductCreditScopeV1
			readback, e := load[Batch](ctx, tx, `SELECT snapshot FROM billing_batches WHERE id=$1`, b.ID)
			if e != nil {
				return e
			}
			if e = s.quantities(ctx, &readback); e != nil {
				return e
			}
			if hash(b) != hash(readback) {
				return ErrConflict
			}
			receipt.CreditsAfter = append(receipt.CreditsAfter, readback)
		}
		receipt.CashAfter, e = s.legacyCashBalances(ctx, r)
		if e != nil {
			return e
		}
		if receipt.CashAfter != plan.Cash {
			return ErrConflict
		}
		if _, e = tx.Exec(ctx, `INSERT INTO billing_legacy_credit_adoption_runs(plan_digest,account_id,product_id,actor_id,receipt) VALUES($1,$2,$3,$4,$5)`, digest, r.AccountID, r.ProductID, r.ActorID, encode(receipt)); e != nil {
			return e
		}
		for _, b := range receipt.CreditsAfter {
			if _, e = tx.Exec(ctx, `INSERT INTO billing_legacy_credit_adopted_batches(batch_id,account_id,product_id,order_id,plan_digest) VALUES($1,$2,$3,$4,$5)`, b.ID, b.AccountID, b.ProductID, b.OrderID, digest); e != nil {
				return e
			}
		}
		if e = event(ctx, tx, r.AccountID, r.ActorID, digest, "credits.legacy_adoption_applied", map[string]any{"batch_ids": r.BatchIDs, "product_id": r.ProductID}); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		out = LegacyCreditAdoptionVerification{Receipt: receipt, Current: receipt.CreditsAfter, Cash: receipt.CashAfter}
		return nil
	})
	return out, e
}

func (s *Service) verifyLegacyCreditAdoptionOn(ctx context.Context, tx pgx.Tx, r LegacyCreditAdoptionRequest, receipt LegacyCreditAdoptionReceipt) (LegacyCreditAdoptionVerification, error) {
	out := LegacyCreditAdoptionVerification{Receipt: receipt, Current: []Batch{}}
	plan := receipt.Plan
	digest := plan.Digest
	plan.Digest = ""
	if digest != hash(plan) || hash(plan.Request) != hash(r) || plan.Cash != receipt.CashAfter ||
		len(receipt.CreditsAfter) != len(plan.Batches) || len(plan.Batches) != len(r.BatchIDs) {
		return out, ErrConflict
	}
	for i, after := range receipt.CreditsAfter {
		before := plan.Batches[i].Batch
		if before == nil || after.ID != r.BatchIDs[i] {
			return out, ErrConflict
		}
		expected := *before
		expected.CreditScope = ProductCreditScopeV1
		if hash(expected) != hash(after) {
			return out, ErrConflict
		}
		current, e := load[Batch](ctx, tx, `SELECT b.snapshot FROM billing_batches b JOIN billing_legacy_credit_adopted_batches a ON a.batch_id=b.id AND a.account_id=b.account_id AND a.order_id=b.order_id WHERE b.id=$1 AND a.plan_digest=$2 AND a.product_id=$3`, after.ID, digest, r.ProductID)
		if e != nil {
			return out, e
		}
		if !sameCreditBatchOrigin(after, current) || current.CreditScope != ProductCreditScopeV1 {
			return out, ErrConflict
		}
		if e = s.quantities(ctx, &current); e != nil {
			return out, e
		}
		if !closedCreditBatch(current) {
			return out, ErrConflict
		}
		out.Current = append(out.Current, current)
	}
	var e error
	out.Cash, e = s.legacyCashBalances(ctx, r)
	return out, e
}

func sameCreditBatchOrigin(a, b Batch) bool {
	return a.ID == b.ID && a.AccountID == b.AccountID && a.ProductID == b.ProductID && a.OrderID == b.OrderID &&
		a.SubscriptionID == b.SubscriptionID && a.SourceKey == b.SourceKey && a.Kind == b.Kind && a.Original == b.Original &&
		a.StartsAt.Equal(b.StartsAt) && a.ExpiresAt.Equal(b.ExpiresAt) && a.CreatedAt.Equal(b.CreatedAt)
}
