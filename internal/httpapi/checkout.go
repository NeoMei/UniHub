package httpapi

import (
	"io"
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
)

func (s *api) checkoutRoutes(m *http.ServeMux, add func(string, action)) {
	b := s.options.Billing
	authorize := func(r *http.Request, u accounts.User) (billing.Checkout, error) {
		x, e := b.GetCheckout(r.Context(), u.ID, r.PathValue("account"), r.PathValue("checkout"))
		if e != nil {
			return x, e
		}
		return x, requireBillingProduct(r, x.ProductID)
	}
	add("POST /v1/accounts/{account}/checkouts", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body billing.CheckoutRequest
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 || !financeIdempotencyKey.MatchString(r.Header.Get("Idempotency-Key")) {
			return billing.ErrInvalid
		}
		body.Key = r.Header.Get("Idempotency-Key")
		if body.OfferID != "" {
			if e := s.authorizeOffer(r, body.OfferID); e != nil {
				return e
			}
		} else if body.QuoteID != "" {
			q, e := b.GetQuote(r.Context(), u.ID, r.PathValue("account"), body.QuoteID)
			if e != nil {
				return e
			}
			if e = requireBillingProduct(r, q.Offer.ProductID); e != nil {
				return e
			}
		}
		x, e := b.CreateCheckout(r.Context(), u.ID, r.PathValue("account"), body)
		if e != nil {
			return e
		}
		return respond(w, 202, x)
	})
	add("GET /v1/accounts/{account}/checkouts", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		xs, e := b.ListCheckouts(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		xs = filterProducts(r, xs, func(x billing.Checkout) string { return x.ProductID })
		return respond(w, 200, xs)
	})
	add("GET /v1/accounts/{account}/checkouts/{checkout}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		x, e := authorize(r, u)
		if e != nil {
			return e
		}
		return respond(w, 200, x)
	})
	add("POST /v1/accounts/{account}/checkouts/{checkout}/reconcile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct{}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if _, e := authorize(r, u); e != nil {
			return e
		}
		x, e := b.ReconcileCheckout(r.Context(), u.ID, r.PathValue("account"), r.PathValue("checkout"))
		if e != nil {
			return e
		}
		return respond(w, 200, x)
	})
	add("POST /v1/accounts/{account}/checkouts/{checkout}/close", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct{}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if _, e := authorize(r, u); e != nil {
			return e
		}
		x, e := b.CloseCheckout(r.Context(), u.ID, r.PathValue("account"), r.PathValue("checkout"))
		if e != nil {
			return e
		}
		return respond(w, 200, x)
	})
	add("POST /v1/accounts/{account}/checkouts/{checkout}/refund", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Reason string `json:"reason"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 || !financeIdempotencyKey.MatchString(r.Header.Get("Idempotency-Key")) {
			return billing.ErrInvalid
		}
		if _, e := authorize(r, u); e != nil {
			return e
		}
		x, e := b.RefundCheckout(r.Context(), u.ID, r.PathValue("account"), r.PathValue("checkout"), r.Header.Get("Idempotency-Key"), body.Reason)
		if e != nil {
			return e
		}
		return respond(w, 202, x)
	})
	m.HandleFunc("POST /payments/{provider}/notify", func(w http.ResponseWriter, r *http.Request) {
		provider := r.PathValue("provider")
		if provider != "wechat" && provider != "alipay" {
			fail(w, 404, "not_found")
			return
		}
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if e != nil {
			fail(w, 400, "invalid_notification")
			return
		}
		if e = b.NotifyCheckout(r.Context(), provider, r.Header, body); e != nil {
			fail(w, 400, "invalid_notification")
			return
		}
		if provider == "alipay" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("success"))
			return
		}
		respond(w, 200, map[string]string{"code": "SUCCESS", "message": "成功"})
	})
}
