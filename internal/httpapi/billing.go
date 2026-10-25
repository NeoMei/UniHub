package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
)

func (s *api) billingRoutes(m *http.ServeMux, add func(string, action)) {
	b := s.options.Billing
	s.productQueryRoutes(add)
	addOps := func(pattern string, fn action) {
		m.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if s.options.OperatorVerifier == nil || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(r.Header.Values("Authorization")) != 1 {
				fail(w, 401, "operator_authentication_required")
				return
			}
			operatorAPI := *s
			operatorAPI.verifier = s.options.OperatorVerifier
			operatorAPI.authenticated(fn)(w, r)
		})
	}
	s.billingProfileRoutes(add, addOps)
	add("GET /v1/catalog", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := b.Catalog(r.Context())
		if e != nil {
			return e
		}
		v = filterProducts(r, v, func(o billing.Offer) string { return o.ProductID })
		return respond(w, 200, v)
	})
	add("GET /v1/accounts/{account}/subscriptions", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := b.Subscriptions(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		v = filterProducts(r, v, func(o billing.Subscription) string { return o.ProductID })
		return respond(w, 200, v)
	})
	add("GET /v1/accounts/{account}/orders", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var v []billing.Order
		var e error
		if originProduct(r) != "" {
			v, e = b.ProductOrders(r.Context(), u.ID, r.PathValue("account"), billingProducts(r))
		} else {
			v, e = b.Orders(r.Context(), u.ID, r.PathValue("account"))
		}
		if e != nil {
			return e
		}
		v = filterProducts(r, v, func(o billing.Order) string { return o.ProductID })
		return respond(w, 200, v)
	})
	add("GET /v1/accounts/{account}/credits", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := b.Batches(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		if originProduct(r) != "" {
			return respond(w, 200, publicCreditBatches(v))
		}
		return respond(w, 200, v)
	})
	add("GET /v1/accounts/{account}/settings", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := b.Settings(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		v = filterProducts(r, v, func(o billing.Authorization) string { return o.ProductID })
		return respond(w, 200, v)
	})
	add("POST /v1/accounts/{account}/orders", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			OfferID string `json:"offer_id"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if e := s.authorizeOffer(r, body.OfferID); e != nil {
			return e
		}
		v, e := b.Purchase(r.Context(), u.ID, r.PathValue("account"), billing.PurchaseRequest{OfferID: body.OfferID, Key: r.Header.Get("Idempotency-Key")})
		if e != nil {
			return e
		}
		return respond(w, 202, v)
	})
	add("POST /v1/accounts/{account}/quotes", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			SubscriptionID string `json:"subscription_id"`
			OfferID        string `json:"offer_id"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if e := s.authorizeOffer(r, body.OfferID); e != nil {
			return e
		}
		if originProduct(r) != "" {
			sub, e := b.GetSubscription(r.Context(), u.ID, r.PathValue("account"), body.SubscriptionID)
			if e != nil {
				return e
			}
			if e = requireBillingProduct(r, sub.ProductID); e != nil {
				return e
			}
		}
		v, e := b.QuoteUpgrade(r.Context(), u.ID, r.PathValue("account"), body.SubscriptionID, body.OfferID)
		if e != nil {
			return e
		}
		return respond(w, 201, v)
	})
	add("POST /v1/accounts/{account}/quotes/{quote}/accept", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct{}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if originProduct(r) != "" {
			q, e := b.GetQuote(r.Context(), u.ID, r.PathValue("account"), r.PathValue("quote"))
			if e != nil {
				return e
			}
			if e = requireBillingProduct(r, q.Offer.ProductID); e != nil {
				return e
			}
		}
		v, e := b.AcceptUpgrade(r.Context(), u.ID, r.PathValue("account"), r.PathValue("quote"), r.Header.Get("Idempotency-Key"))
		if e != nil {
			return e
		}
		return respond(w, 202, v)
	})
	add("PUT /v1/accounts/{account}/settings", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			ProductID       string    `json:"product_id"`
			Purpose         string    `json:"purpose"`
			Enabled         bool      `json:"enabled"`
			Version         int64     `json:"version"`
			MaxPerOperation string    `json:"max_per_operation"`
			MaxTotal        string    `json:"max_total"`
			ExpiresAt       time.Time `json:"expires_at"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if e := requireBillingProduct(r, body.ProductID); e != nil {
			return e
		}
		v, e := b.SetAuthorization(r.Context(), u.ID, r.PathValue("account"), billing.Authorization{ProductID: body.ProductID, Purpose: body.Purpose, Enabled: body.Enabled, Version: body.Version, MaxPerOperation: body.MaxPerOperation, MaxTotal: body.MaxTotal, ExpiresAt: body.ExpiresAt})
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	add("POST /v1/accounts/{account}/orders/{order}/refund", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct{}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if originProduct(r) != "" {
			o, e := b.GetOrder(r.Context(), u.ID, r.PathValue("account"), r.PathValue("order"))
			if e != nil {
				return e
			}
			if e = requireBillingProduct(r, o.ProductID); e != nil {
				return e
			}
		}
		v, e := b.RequestRefund(r.Context(), u.ID, r.PathValue("account"), r.PathValue("order"), r.Header.Get("Idempotency-Key"))
		if e != nil {
			return e
		}
		return respond(w, 202, v)
	})
	// Catalog operators are an explicit deployment allowlist enforced again by
	// the domain. Owning a personal/team account does not grant this capability.
	addOps("GET /ops/v1/offers", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := b.AllOffers(r.Context(), u.ID)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	addOps("POST /ops/v1/offers", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body billing.Offer
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := b.SaveOffer(r.Context(), u.ID, body)
		if e != nil {
			return e
		}
		return respond(w, 201, v)
	})
	addOps("POST /ops/v1/offers/{offer}/publish", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct{}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := b.PublishOffer(r.Context(), u.ID, r.PathValue("offer"))
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	addOps("POST /ops/v1/rates", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body billing.Rate
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := b.PublishRate(r.Context(), u.ID, body)
		if e != nil {
			return e
		}
		return respond(w, 201, v)
	})
	addOps("POST /ops/v1/accounts/{account}/orders/{order}/refund-review", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := b.ResolveRefundReview(r.Context(), u.ID, r.PathValue("account"), r.PathValue("order"), body.Decision, body.Reason)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	addOps("GET /ops/v1/orders", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := b.OperatorOrders(r.Context(), u.ID, r.URL.Query().Get("account_id"))
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	service := func(pattern string, fn func(http.ResponseWriter, *http.Request) error) {
		m.HandleFunc(pattern, s.serviceAuthenticated(fn))
	}
	service("GET /internal/v1/rates", func(w http.ResponseWriter, r *http.Request) error {
		v, e := b.Rates(r.Context())
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	service("GET /internal/v1/calls/{call}", func(w http.ResponseWriter, r *http.Request) error {
		v, e := b.GetCall(r.Context(), "llmrouter", r.PathValue("call"))
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	service("POST /internal/v1/calls/{call}/topup", func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			Amount string `json:"amount"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := b.TopUp(r.Context(), "llmrouter", r.PathValue("call"), body.Amount, r.Header.Get("Idempotency-Key"))
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	service("POST /internal/v1/calls/{call}/settle", func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			Usage        *billing.Usage `json:"usage"`
			ProviderCost string         `json:"provider_cost"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := b.Settle(r.Context(), "llmrouter", billing.SettlementRequest{CallID: r.PathValue("call"), Usage: body.Usage, ProviderCost: body.ProviderCost})
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	service("POST /internal/v1/calls/{call}/release", func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			Reason string `json:"reason"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := b.Release(r.Context(), "llmrouter", r.PathValue("call"), body.Reason)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
}

func (s *api) serviceAuthenticated(next func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := s.options.ServiceToken
		h := r.Header.Get("Authorization")
		if len(expected) < 32 || !strings.HasPrefix(h, "Bearer ") || len(h) > 1024 || len(r.Header.Values("Authorization")) != 1 {
			fail(w, 401, "unauthorized_service")
			return
		}
		got := sha256.Sum256([]byte(strings.TrimPrefix(h, "Bearer ")))
		want := sha256.Sum256([]byte(expected))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			fail(w, 401, "unauthorized_service")
			return
		}
		if e := next(w, r); e != nil {
			writeError(w, e)
		}
	}
}
