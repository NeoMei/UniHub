package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"unihub/internal/accounts"
	"unihub/internal/cashtopup"
)

func (s *api) cashTopupRoutes(add func(string, action)) {
	c := s.options.CashTopup
	if c == nil {
		return
	}
	add("POST /v1/accounts/{account}/cash-checkouts", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if len(r.Header.Values("Idempotency-Key")) != 1 || !financeIdempotencyKey.MatchString(r.Header.Get("Idempotency-Key")) {
			return cashtopup.ErrInvalid
		}
		var body cashtopup.Request
		if e := decode(w, r, &body); e != nil {
			return e
		}
		body.Key = r.Header.Get("Idempotency-Key")
		if e := requireBillingProduct(r, body.ProductID); e != nil {
			return e
		}
		body.ReturnURL = cashTopupReturnURL(r)
		x, e := c.Create(r.Context(), u.ID, r.PathValue("account"), body)
		if e != nil {
			return e
		}
		return respond(w, 202, x)
	})
	add("GET /v1/accounts/{account}/cash-checkouts", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		xs, e := c.List(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		return respond(w, 200, xs)
	})
	add("GET /v1/accounts/{account}/cash-checkouts/{checkout}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		x, e := c.Get(r.Context(), u.ID, r.PathValue("account"), r.PathValue("checkout"))
		if e != nil {
			return e
		}
		return respond(w, 200, x)
	})
	add("POST /v1/accounts/{account}/cash-checkouts/{checkout}/reconcile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		x, e := c.Reconcile(r.Context(), u.ID, r.PathValue("account"), r.PathValue("checkout"))
		if e != nil {
			return e
		}
		return respond(w, 200, x)
	})
	add("POST /v1/accounts/{account}/cash-checkouts/{checkout}/close", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		x, e := c.Close(r.Context(), u.ID, r.PathValue("account"), r.PathValue("checkout"))
		if e != nil {
			return e
		}
		return respond(w, 200, x)
	})
}

// Return destinations come only from the verified client's registered origin.
// Other products and central-console payments retain their merchant defaults.
func cashTopupReturnURL(r *http.Request) string {
	policy, ok := productPolicy(r)
	if !ok || policy.ProductID != "llmrouter" {
		return ""
	}
	for _, origin := range policy.Origins {
		u, err := url.Parse(origin)
		if err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" {
			return origin + "/wallet"
		}
	}
	return ""
}

// notifyCashTopup is called by the shared /payments/{provider}/notify route.
// It returns (handled, error). Cash-topup notifications are best-effort routed
// by their ct_ order-id prefix; commercial checkouts without that prefix fall
// through to billing's own handler.
func (s *api) notifyCashTopup(r *http.Request, provider string, headers http.Header, body []byte) (bool, error) {
	c := s.options.CashTopup
	if c == nil {
		return false, nil
	}
	if e := c.Notify(r.Context(), provider, headers, body); e != nil {
		if errors.Is(e, cashtopup.ErrInvalid) || errors.Is(e, cashtopup.ErrConflict) {
			return true, e
		}
		return false, nil
	}
	return true, nil
}
