package billing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

type LegacyCreditProof struct {
	PaymentCommandID         string `json:"payment_command_id"`
	PaymentTransactionID     string `json:"payment_transaction_id"`
	PaymentLedger            string `json:"payment_ledger,omitempty"`
	FulfillmentCommandID     string `json:"fulfillment_command_id"`
	FulfillmentTransactionID string `json:"fulfillment_transaction_id,omitempty"`
	FulfillmentLedger        string `json:"fulfillment_ledger,omitempty"`
	GrantCommandID           string `json:"grant_command_id"`
	GrantTransactionID       string `json:"grant_transaction_id"`
	GrantLedger              string `json:"grant_ledger,omitempty"`
}

type legacyCreditProofReader interface {
	Find(context.Context, string) (*ledger.Transaction, error)
}

type legacyCreditProofSource struct {
	currentName, retainedName string
	retained                  legacyCreditProofReader
}

type legacyCreditCommandReceipt struct {
	transactionID, ledgerName string
}

// ConfigureLegacyCreditProofLedgers is called at startup by the adoption operator.
// The retained reader only serves original payment and grant proof.
func (s *Service) ConfigureLegacyCreditProofLedgers(currentName, retainedName string, retained legacyCreditProofReader) {
	s.legacyCreditProofSource = legacyCreditProofSource{currentName: currentName, retainedName: retainedName, retained: retained}
}

type legacyCreditProofError string

func (e legacyCreditProofError) Error() string { return string(e) }

func (s *Service) verifiedLegacyCreditCommand(ctx context.Context, q accounts.Querier, account, kind, key string) (command, legacyCreditCommandReceipt, error) {
	var receipt legacyCreditCommandReceipt
	var id, transactionID string
	e := q.QueryRow(ctx, `SELECT id,COALESCE(ledger_transaction_id,'') FROM billing_commands WHERE account_id=$1 AND kind=$2 AND request_key=$3 AND status='APPLIED'`, account, kind, key).Scan(&id, &transactionID)
	if errors.Is(e, pgx.ErrNoRows) {
		return command{}, receipt, accounts.ErrNotFound
	}
	if e != nil {
		return command{}, receipt, e
	}
	c, e := readCommand(ctx, q, id)
	if e != nil {
		return c, receipt, e
	}
	if c.Account != account || c.Kind != kind || c.Key != key || c.Ledger.Reference != "unihub:billing:"+id {
		return c, receipt, legacyCreditProofError("command_scope_mismatch")
	}
	if len(c.Ledger.Postings) == 0 && transactionID == "" {
		return c, receipt, nil
	}
	source := s.legacyCreditProofSource
	tr, e := s.ledger.Find(ctx, c.Ledger.Reference)
	name := source.currentName
	if e == nil && tr == nil && source.retained != nil {
		tr, e = source.retained.Find(ctx, c.Ledger.Reference)
		name = source.retainedName
	}
	if e != nil {
		return c, receipt, e
	}
	if tr == nil || transactionID == "" || tr.ID != transactionID || tr.Reference != c.Ledger.Reference || !slices.Equal(tr.Postings, c.Ledger.Postings) {
		return c, receipt, legacyCreditProofError("ledger_command_mismatch")
	}
	return c, legacyCreditCommandReceipt{transactionID: transactionID, ledgerName: name}, nil
}

func (s *Service) legacyCreditProof(ctx context.Context, q accounts.Querier, b Batch) (LegacyCreditProof, error) {
	o, e := load[Order](ctx, q, `SELECT snapshot FROM billing_orders WHERE id=$1 AND account_id=$2`, b.OrderID, b.AccountID)
	if errors.Is(e, accounts.ErrNotFound) {
		return LegacyCreditProof{}, legacyCreditProofError("original_order_missing")
	}
	if e != nil {
		return LegacyCreditProof{}, e
	}
	return s.legacyCreditGrantProof(ctx, q, o, b)
}

func (s *Service) legacyCreditGrantProof(ctx context.Context, q accounts.Querier, o Order, b Batch) (LegacyCreditProof, error) {
	if !isLegacyCreditOrder(o, b) || refundBlocks(o.RefundStatus) {
		return LegacyCreditProof{}, legacyCreditProofError("original_order_not_authoritative")
	}
	proof, grant, e := s.legacyPaidOrderProof(ctx, q, o)
	if e != nil {
		return proof, e
	}
	grantReceipt := legacyCreditCommandReceipt{transactionID: proof.FulfillmentTransactionID, ledgerName: proof.FulfillmentLedger}
	if len(grant.Effect.Batches) == 0 || grant.Effect.Batches[0].ID != b.ID {
		grant, grantReceipt, e = s.verifiedLegacyCreditCommand(ctx, q, o.AccountID, "monthly", b.SourceKey)
		if errors.Is(e, accounts.ErrNotFound) {
			return proof, legacyCreditProofError("grant_command_missing")
		}
		if e != nil {
			return proof, e
		}
	}
	amount, _ := integer(b.Original, true)
	wanted := ledger.Posting{Source: "world", Destination: BatchAddress(b.ID, "available"), Asset: CreditAsset, Amount: amount}
	if grantReceipt.transactionID == "" || len(grant.Effect.Batches) != 1 || !sameCreditBatchOrigin(b, grant.Effect.Batches[0]) ||
		grant.Effect.Batches[0].CreditScope != "" || !slices.Equal(legacyPostingsForAsset(grant.Ledger.Postings, CreditAsset), []ledger.Posting{wanted}) {
		return proof, legacyCreditProofError("grant_postings_or_batch_mismatch")
	}
	proof.GrantCommandID, proof.GrantTransactionID, proof.GrantLedger = grant.ID, grantReceipt.transactionID, grantReceipt.ledgerName
	return proof, nil
}

func (s *Service) legacyPaidOrderProof(ctx context.Context, q accounts.Querier, o Order) (LegacyCreditProof, command, error) {
	var proof LegacyCreditProof
	done, fulfillmentReceipt, e := s.verifiedLegacyCreditCommand(ctx, q, o.AccountID, "fulfillment", o.ID)
	if errors.Is(e, accounts.ErrNotFound) {
		return proof, done, legacyCreditProofError("fulfillment_command_missing")
	}
	if e != nil {
		return proof, done, e
	}
	if done.Effect.Order == nil || !sameLegacyOrderTerms(o, *done.Effect.Order) || done.Effect.Order.PaymentStatus != "PAID" || done.Effect.Order.FulfillmentStatus != "FULFILLED" {
		return proof, done, legacyCreditProofError("fulfillment_order_mismatch")
	}
	proof.FulfillmentCommandID, proof.FulfillmentTransactionID, proof.FulfillmentLedger = done.ID, fulfillmentReceipt.transactionID, fulfillmentReceipt.ledgerName

	payKind, payKey, expected, e := legacyPaymentProof(ctx, q, o, done)
	if e != nil {
		return proof, done, e
	}
	paid, paymentReceipt, e := s.verifiedLegacyCreditCommand(ctx, q, o.AccountID, payKind, payKey)
	if errors.Is(e, accounts.ErrNotFound) {
		return proof, done, legacyCreditProofError("payment_command_missing")
	}
	if e != nil {
		return proof, done, e
	}
	if paid.Actor != o.ActorID || paid.Effect.Order == nil || paid.Effect.Order.PaymentStatus != "PAID" ||
		!sameLegacyOrderTerms(o, *paid.Effect.Order) || !slices.Equal(paid.Ledger.Postings, expected) {
		return proof, done, legacyCreditProofError("payment_postings_or_order_mismatch")
	}
	proof.PaymentCommandID, proof.PaymentTransactionID, proof.PaymentLedger = paid.ID, paymentReceipt.transactionID, paymentReceipt.ledgerName
	return proof, done, nil
}

func (s *Service) verifyLegacyTermInstallment(ctx context.Context, q dbq, sub Subscription, start, end time.Time, amount int64) (*LegacyCreditAdoptionTerm, error) {
	term := &LegacyCreditAdoptionTerm{Subscription: sub, InstallmentFrom: start, InstallmentTo: end}
	var b Batch
	var o Order
	var e error
	if amount != 0 {
		source := sub.ID + ":" + start.Format(time.RFC3339Nano) + ":base"
		row, e := load[storedLegacyCreditBatch](ctx, q, `SELECT to_jsonb(b) FROM billing_batches b WHERE account_id=$1 AND source_key=$2`, sub.AccountID, source)
		if e != nil {
			return nil, e
		}
		b = row.Batch
		if b.ID != row.ID || b.AccountID != sub.AccountID || b.ProductID != sub.ProductID || b.OrderID == "" || b.OrderID != row.OrderID || b.SourceKey != source || b.SubscriptionID != sub.ID || b.Kind != "base" || b.Original != sub.Offer.MonthlyCredit || !b.StartsAt.Equal(start) || !b.ExpiresAt.Equal(end) {
			return nil, legacyCreditProofError("current_grant_mismatch")
		}
		o, e = load[Order](ctx, q, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND id=$2`, sub.AccountID, b.OrderID)
		if e != nil {
			return nil, e
		}
	} else {
		orders, e := list[Order](ctx, q, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND id=snapshot->>'id'
			AND snapshot->>'subscription_id'=$2 AND snapshot->>'product_id'=$3
			AND snapshot->>'kind' IN ('membership','renewal') AND snapshot->>'payment_status'='PAID'
			AND (snapshot->>'start')::timestamptz<=$4 AND (snapshot->>'end')::timestamptz=$5 LIMIT 2`, sub.AccountID, sub.ID, sub.ProductID, start, sub.PaidThrough)
		if e != nil {
			return nil, e
		}
		if len(orders) != 1 {
			return nil, legacyCreditProofError("current_order_not_unique")
		}
		o = orders[0]
	}
	if o.ID == "" || o.AccountID != sub.AccountID || o.ProductID != sub.ProductID || o.SubscriptionID != sub.ID ||
		(o.Kind != "membership" && o.Kind != "renewal") || o.CreditScope != "" || o.ServiceAmount != "" || o.ComputeAmount != "" ||
		o.PaymentStatus != "PAID" || o.FulfillmentStatus != "FULFILLED" || refundBlocks(o.RefundStatus) ||
		o.Start.Before(sub.Anchor) || o.Start.After(start) || !o.End.Equal(sub.PaidThrough) || hash(o.Offer) != hash(sub.Offer) || o.GrantAmount != sub.Offer.MonthlyCredit {
		return nil, legacyCreditProofError("current_order_mismatch")
	}
	term.PaidOrderID, term.PaidOrderDigest = o.ID, hash(o)
	if amount != 0 {
		term.GrantBatchID = b.ID
		term.Proof, e = s.legacyCreditGrantProof(ctx, q, o, b)
	} else {
		var fulfillment command
		term.Proof, fulfillment, e = s.legacyPaidOrderProof(ctx, q, o)
		if e == nil && (len(fulfillment.Effect.Batches) != 0 || len(legacyPostingsForAsset(fulfillment.Ledger.Postings, CreditAsset)) != 0) {
			e = legacyCreditProofError("unexpected_current_grant")
		}
	}
	if e != nil {
		return nil, e
	}
	return term, nil
}

func isLegacyCreditOrder(o Order, b Batch) bool {
	return o.ID == b.OrderID && o.AccountID == b.AccountID && o.ProductID == b.ProductID && o.Offer.ProductID == b.ProductID &&
		o.CreditScope == "" && o.Offer.CreditScope == "" && o.ServiceAmount == "" && o.ComputeAmount == "" &&
		o.PaymentStatus == "PAID" && o.FulfillmentStatus == "FULFILLED" && (o.Kind == "addon" && b.Kind == "addon" ||
		(o.Kind == "membership" || o.Kind == "renewal") && b.Kind == "base" && o.SubscriptionID != "" && o.SubscriptionID == b.SubscriptionID ||
		o.Kind == "upgrade" && b.Kind == "upgrade" && o.SubscriptionID != "" && o.SubscriptionID == b.SubscriptionID)
}

func sameLegacyOrderTerms(a, b Order) bool {
	clearMutable := func(o Order) Order {
		o.PaymentStatus, o.FulfillmentStatus, o.RefundStatus = "", "", ""
		o.RefundActorID, o.RefundKey, o.RefundReason = "", "", ""
		return o
	}
	return hash(clearMutable(a)) == hash(clearMutable(b))
}

func legacyPostingsForAsset(postings []ledger.Posting, asset string) []ledger.Posting {
	var out []ledger.Posting
	for _, p := range postings {
		if p.Asset == asset {
			out = append(out, p)
		}
	}
	return out
}

func legacyPaymentProof(ctx context.Context, q accounts.Querier, o Order, done command) (string, string, []ledger.Posting, error) {
	amount, e := integer(o.Amount, true)
	if e != nil {
		return "", "", nil, legacyCreditProofError("legacy_cash_amount_unverified")
	}
	revenue := "commercial:revenue:" + o.ProductID
	switch o.PaymentSource {
	case "", "wallet":
		product, productErr := integer(o.ProductFunding, false)
		unified, unifiedErr := integer(o.UnifiedFunding, false)
		if productErr != nil || unifiedErr != nil || product > amount || unified != amount-product || len(legacyPostingsForAsset(done.Ledger.Postings, CashAsset)) != 0 {
			return "", "", nil, legacyCreditProofError("legacy_wallet_funding_unverified")
		}
		source := wallet.Address(o.AccountID, o.ProductID)
		posts := []ledger.Posting{}
		if unified > 0 {
			posts = append(posts, ledger.Posting{Source: wallet.Address(o.AccountID, ""), Destination: source, Asset: CashAsset, Amount: unified})
		}
		posts = append(posts, ledger.Posting{Source: source, Destination: revenue, Asset: CashAsset, Amount: amount})
		return "payment", o.ID, posts, nil
	case "external":
		x, e := readCheckout(ctx, q, o.AccountID, o.CheckoutID)
		if errors.Is(e, accounts.ErrNotFound) {
			return "", "", nil, legacyCreditProofError("merchant_checkout_missing")
		}
		if e != nil {
			return "", "", nil, e
		}
		r, e := load[payments.Result](ctx, q, `SELECT snapshot FROM billing_payment_receipts WHERE checkout_id=$1 AND transaction_id=$2 AND provider=$3 AND merchant_id=$4`, o.CheckoutID, o.ProviderTransactionID, x.Provider, x.MerchantID)
		if errors.Is(e, accounts.ErrNotFound) {
			return "", "", nil, legacyCreditProofError("merchant_receipt_missing")
		}
		if e != nil {
			return "", "", nil, e
		}
		source := externalReceived(o.CheckoutID)
		cashPosted := []ledger.Posting{{Source: source, Destination: revenue, Asset: CashAsset, Amount: amount}}
		if x.ID != o.CheckoutID || x.ProductID != o.ProductID || x.OrderID != o.ID || x.PaymentStatus != "PAID" || refundBlocks(x.RefundStatus) ||
			x.Amount != o.Amount || r.OrderID != o.CheckoutID || r.TransactionID != o.ProviderTransactionID ||
			r.Status != "PAID" || r.Amount != o.Amount || r.Currency != "CNY" ||
			!slices.Equal(legacyPostingsForAsset(done.Ledger.Postings, CashAsset), cashPosted) {
			return "", "", nil, legacyCreditProofError("merchant_payment_or_recognition_mismatch")
		}
		return "external_payment", o.CheckoutID, []ledger.Posting{{Source: "world", Destination: source, Asset: CashAsset, Amount: amount}}, nil
	default:
		return "", "", nil, legacyCreditProofError("unknown_legacy_payment_source")
	}
}

func legacyAdoptionCoversBatch(ctx context.Context, q accounts.Querier, b Batch) (bool, error) {
	var digest, actor string
	var raw []byte
	e := q.QueryRow(ctx, `SELECT r.plan_digest,r.actor_id,r.receipt FROM billing_legacy_credit_adopted_batches a JOIN billing_legacy_credit_adoption_runs r USING (plan_digest)
		WHERE a.batch_id=$1 AND a.account_id=$2 AND a.product_id=$3 AND a.order_id=$4 AND r.account_id=$2 AND r.product_id=$3`,
		b.ID, b.AccountID, b.ProductID, b.OrderID).Scan(&digest, &actor, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var receipt LegacyCreditAdoptionReceipt
	if e = json.Unmarshal(raw, &receipt); e != nil {
		return false, e
	}
	plan := receipt.Plan
	if plan.Digest != digest || !plan.CanApply || len(plan.Blockers) != 0 || plan.Asset != CreditAsset ||
		plan.Request.ActorID != actor || plan.Request.AccountID != b.AccountID || plan.Request.ProductID != b.ProductID ||
		receipt.AppliedAt.IsZero() || receipt.CashAfter != plan.Cash || len(plan.Batches) != len(receipt.CreditsAfter) || len(plan.Batches) != len(plan.Request.BatchIDs) {
		return false, nil
	}
	if !plan.HasValidDigest() {
		return false, nil
	}
	for i, id := range plan.Request.BatchIDs {
		if id != b.ID {
			continue
		}
		entry, after := plan.Batches[i], receipt.CreditsAfter[i]
		if entry.BatchID != id || entry.State != "eligible" || entry.Batch == nil || entry.Batch.CreditScope != "" ||
			entry.Proof == nil || entry.Proof.PaymentCommandID == "" || entry.Proof.PaymentTransactionID == "" ||
			entry.Proof.FulfillmentCommandID == "" || entry.Proof.GrantCommandID == "" || entry.Proof.GrantTransactionID == "" ||
			b.CreditScope != ProductCreditScopeV1 || !sameCreditBatchOrigin(after, b) {
			return false, nil
		}
		expected := *entry.Batch
		expected.CreditScope = ProductCreditScopeV1
		return hash(expected) == hash(after), nil
	}
	return false, nil
}

func requireLegacyCreditAdoption(ctx context.Context, q accounts.Querier, o Order, b Batch) error {
	if !isLegacyCreditOrder(o, b) {
		return ErrConflict
	}
	ok, e := legacyAdoptionCoversBatch(ctx, q, b)
	if e != nil {
		return e
	}
	if !ok {
		return ErrConflict
	}
	return nil
}

func validateProductCreditBatchForHold(ctx context.Context, q accounts.Querier, b Batch) error {
	o, e := load[Order](ctx, q, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND id=$2`, b.AccountID, b.OrderID)
	if e != nil {
		return e
	}
	if o.ID != b.OrderID || o.AccountID != b.AccountID || o.ProductID != b.ProductID || o.Offer.ProductID != b.ProductID ||
		o.PaymentStatus != "PAID" || o.FulfillmentStatus != "FULFILLED" || refundBlocks(o.RefundStatus) {
		return ErrConflict
	}
	if o.CreditScope == ProductCreditScopeV1 && o.Offer.CreditScope == ProductCreditScopeV1 {
		return nil
	}
	return requireLegacyCreditAdoption(ctx, q, o, b)
}
