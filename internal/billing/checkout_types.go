package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/payments"
)

var ErrPaymentUnavailable = errors.New("billing: payment_not_configured")

type CheckoutRequest struct {
	OfferID  string `json:"offer_id,omitempty"`
	QuoteID  string `json:"quote_id,omitempty"`
	Provider string `json:"provider"`
	Key      string `json:"-"`
}
type Checkout struct {
	ID                string            `json:"id"`
	AccountID         string            `json:"account_id"`
	ProductID         string            `json:"product_id"`
	Provider          string            `json:"provider"`
	OfferName         string            `json:"offer_name"`
	Amount            string            `json:"amount"`
	Currency          string            `json:"currency"`
	PaymentStatus     string            `json:"payment_status"`
	FulfillmentStatus string            `json:"fulfillment_status"`
	RefundStatus      string            `json:"refund_status,omitempty"`
	ExpiresAt         time.Time         `json:"expires_at"`
	CreatedAt         time.Time         `json:"created_at"`
	Session           *payments.Session `json:"session,omitempty"`
	OrderID           string            `json:"order_id,omitempty"`
	ErrorCode         string            `json:"error_code,omitempty"`
}
type checkoutRecord struct {
	Checkout
	ActorID          string                 `json:"actor_id"`
	MerchantID       string                 `json:"merchant_id"`
	MerchantVersion  string                 `json:"merchant_version"`
	RequestDigest    string                 `json:"request_digest"`
	Order            Order                  `json:"order"`
	Quote            *Quote                 `json:"quote,omitempty"`
	Receipt          *payments.Result       `json:"receipt,omitempty"`
	RefundID         string                 `json:"refund_id,omitempty"`
	RefundKey        string                 `json:"refund_key,omitempty"`
	RefundReason     string                 `json:"refund_reason,omitempty"`
	RefundReceipt    *payments.RefundResult `json:"refund_receipt,omitempty"`
	RefundApprovedBy string                 `json:"refund_approved_by,omitempty"`
	CloseRequested   bool                   `json:"close_requested,omitempty"`
}

func (s *Service) ConfigurePayments(providers []payments.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paymentProviders = map[string]payments.Provider{}
	for _, p := range providers {
		if p != nil && (p.ID() == "wechat" || p.ID() == "alipay") && p.MerchantID() != "" {
			s.paymentProviders[p.ID()] = p
		}
	}
}

// ConfigurePaymentSource binds a durable source shared by API and worker. Each
// financial boundary reads current authority; failed reads never use stale state.
func (s *Service) ConfigurePaymentSource(source func(context.Context) ([]payments.Provider, []payments.Provider, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paymentSource = source
}
func (s *Service) paymentSnapshot(ctx context.Context) ([]payments.Provider, []payments.Provider, error) {
	s.mu.RLock()
	source := s.paymentSource
	if source != nil {
		s.mu.RUnlock()
		return source(ctx)
	}
	active := []payments.Provider{}
	for _, p := range s.paymentProviders {
		active = append(active, p)
	}
	s.mu.RUnlock()
	return active, active, nil
}
func (s *Service) PaymentProvidersContext(ctx context.Context) ([]string, error) {
	active, _, e := s.paymentSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, p := range active {
		out = append(out, p.ID())
	}
	sort.Strings(out)
	return out, nil
}
func (s *Service) checkoutProvider(ctx context.Context, name, merchant string) (payments.Provider, error) {
	active, _, e := s.paymentSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	for _, p := range active {
		if p.ID() == name && (merchant == "" || merchant == p.MerchantID()) {
			return p, nil
		}
	}
	return nil, ErrPaymentUnavailable
}
func (s *Service) commercialClear(ctx context.Context, c *pgxpool.Conn, account string) error {
	var pending bool
	e := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_checkouts WHERE account_id=$1 AND (snapshot->>'payment_status' IN ('CREATED','PENDING','UNKNOWN') OR (snapshot->>'payment_status' IN ('PAID','REVIEW') AND snapshot->>'fulfillment_status'<>'FULFILLED' AND COALESCE(snapshot->>'refund_status','')<>'SUCCEEDED') OR snapshot->>'refund_status' IN ('PENDING','REVIEW')))`, account).Scan(&pending)
	if e != nil {
		return e
	}
	if pending {
		return ErrPending
	}
	return nil
}
func readCheckout(ctx context.Context, q accounts.Querier, account, checkout string) (checkoutRecord, error) {
	return load[checkoutRecord](ctx, q, `SELECT snapshot FROM billing_checkouts WHERE account_id=$1 AND id=$2`, account, checkout)
}
func saveCheckout(ctx context.Context, c *pgxpool.Conn, x checkoutRecord) error {
	_, e := c.Exec(ctx, `UPDATE billing_checkouts SET snapshot=$2 WHERE id=$1`, x.ID, encode(x))
	return e
}
func (s *Service) GetCheckout(ctx context.Context, actor, account, checkout string) (Checkout, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Checkout{}, e
	}
	x, e := readCheckout(ctx, s.pool, account, checkout)
	if e != nil {
		return x.Checkout, e
	}
	return s.checkoutView(ctx, x)
}
func (s *Service) ListCheckouts(ctx context.Context, actor, account string) ([]Checkout, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	xs, e := list[checkoutRecord](ctx, s.pool, `SELECT snapshot FROM billing_checkouts WHERE account_id=$1 ORDER BY (snapshot->>'created_at')::timestamptz DESC,id LIMIT 200`, account)
	if e != nil {
		return nil, e
	}
	out := []Checkout{}
	for _, x := range xs {
		v, e := s.checkoutView(ctx, x)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) checkoutView(ctx context.Context, x checkoutRecord) (Checkout, error) {
	v := x.Checkout
	if v.OrderID != "" {
		o, e := load[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders WHERE id=$1`, v.OrderID)
		if e != nil {
			return v, e
		}
		v.FulfillmentStatus = o.FulfillmentStatus
		if v.FulfillmentStatus == "CANCELED" {
			v.FulfillmentStatus = "NOT_STARTED"
		}
		if o.RefundStatus == "REFUNDED" {
			v.RefundStatus = "SUCCEEDED"
		} else if o.RefundStatus == "MANUAL_REVIEW" {
			v.RefundStatus = "REVIEW"
		}
	}
	return v, nil
}

// ConfigureCheckoutReadiness binds the deployment's verified runtime protocol
// probe. New AI-points contracts fail closed when that probe is absent.
func (s *Service) ConfigureCheckoutReadiness(probe func(context.Context, string, string, string) (NativeCreditReadiness, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkoutReadiness = probe
}
func nativePointsOffer(o Offer) bool {
	return !o.Sandbox && o.Entitlements["pricing_program"] == "superwagie-ai-points-v1"
}

// NativeCheckoutReadiness is the runtime readiness used both for advertised
// capabilities and new points sales. Account-independent startup probes may
// ignore actor/account; caller still authorizes the finance operation.
func (s *Service) NativeCheckoutReadiness(ctx context.Context, actor, account, product string) (NativeCreditReadiness, error) {
	policy, e := s.NativeCreditsReadiness(ctx, product)
	if e != nil || !policy.Ready {
		return policy, e
	}
	s.mu.RLock()
	probe := s.checkoutReadiness
	s.mu.RUnlock()
	if probe == nil {
		policy.Ready = false
		policy.Reason = "native_credit_runtime_unverified"
		return policy, nil
	}
	return probe(ctx, actor, account, product)
}
func (s *Service) externalCheckoutReady(ctx context.Context, actor, account string, o Offer) error {
	if !nativePointsOffer(o) {
		return nil
	}
	r, e := s.NativeCheckoutReadiness(ctx, actor, account, o.ProductID)
	if e != nil {
		return e
	}
	if !r.Ready {
		return ErrCreditRatesUnavailable
	}
	return nil
}

// Legacy wallets remain usable, but opting into the new points program cannot
// bypass the same runtime readiness simply by choosing the wallet endpoint.
func (s *Service) newPurchaseReady(ctx context.Context, actor, account, key, offerID, quoteID string) error {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return e
	}
	var replay bool
	if e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_orders WHERE account_id=$1 AND request_key=$2)`, account, key).Scan(&replay); e != nil {
		return e
	}
	if replay {
		return nil
	}
	var offer Offer
	var e error
	if quoteID != "" {
		var q Quote
		q, e = load[Quote](ctx, s.pool, `SELECT snapshot FROM billing_quotes WHERE account_id=$1 AND id=$2`, account, quoteID)
		offer = q.Offer
	} else {
		offer, e = load[Offer](ctx, s.pool, `SELECT snapshot FROM billing_offers WHERE id=$1`, offerID)
	}
	if e != nil {
		return e
	}
	return s.externalCheckoutReady(ctx, actor, account, offer)
}

// ConfigurationID is implemented by official adapters and binds the application
// identity as well as merchant, excluding rotatable keys. Test providers fall back to
// an explicit merchant-scoped version. Old merchant/application identities must remain configured until resolved.
func paymentConfigurationID(p payments.Provider) string {
	if versioned, ok := p.(interface{ ConfigurationID() string }); ok && versioned.ConfigurationID() != "" {
		return versioned.ConfigurationID()
	}
	return hash([]string{"direct_v1", p.ID(), p.MerchantID()})
}
func (s *Service) checkoutBoundProvider(ctx context.Context, x checkoutRecord) (payments.Provider, error) {
	_, recovery, e := s.paymentSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	for _, p := range recovery {
		if p.ID() == x.Provider && p.MerchantID() == x.MerchantID && paymentConfigurationID(p) == x.MerchantVersion {
			return p, nil
		}
	}
	return nil, ErrPaymentUnavailable
}
func (s *Service) notificationProviders(ctx context.Context, name string) ([]payments.Provider, error) {
	_, recovery, e := s.paymentSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	out := []payments.Provider{}
	for _, p := range recovery {
		if p.ID() == name {
			out = append(out, p)
		}
	}
	return out, nil
}
