package httpapi

import (
	"context"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/wallet"
)

func (s *api) productQueryRoutes(add func(string, action)) {
	// Explicit pool reports require a registered router and a current financial role.
	for _, route := range []string{"GET /v1/accounts/{account}/model-pool/calls", "GET /v1/accounts/{account}/model-pool/calls/{call}", "GET /v1/accounts/{account}/model-pool/usage/summary"} {
		add(route, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			c, ok := productPolicy(r)
			if !ok || c.ProductID != "llmrouter" || !c.HasScope("usage:all-products") {
				return accounts.ErrForbidden
			}
			role, e := s.accounts.Role(r.Context(), u.ID, r.PathValue("account"))
			if e != nil {
				return e
			}
			if role != "owner" && role != "billing_admin" {
				return accounts.ErrForbidden
			}
			// Clearing attribution is local to these explicit reporting handlers.
			r = r.WithContext(context.WithValue(r.Context(), productIdentityKey{}, ""))
			if r.PathValue("call") != "" {
				q := r.URL.Query()
				for k, v := range q {
					if k != "origin_product_id" || len(v) != 1 {
						return billing.ErrInvalid
					}
				}
				v, e := s.options.Billing.QueryCall(r.Context(), u.ID, r.PathValue("account"), r.PathValue("call"), q.Get("origin_product_id"))
				if e != nil {
					return e
				}
				return respond(w, 200, v)
			}
			summary := strings.HasSuffix(r.URL.Path, "/usage/summary")
			f, page, e := callQuery(r, !summary)
			if e != nil {
				return e
			}
			if summary {
				v, e := s.options.Billing.QueryUsageSummary(r.Context(), u.ID, r.PathValue("account"), f)
				if e != nil {
					return e
				}
				return respond(w, 200, v)
			}
			v, e := s.options.Billing.QueryCalls(r.Context(), u.ID, r.PathValue("account"), f, page)
			if e != nil {
				return e
			}
			return respond(w, 200, v)
		})
	}

	add("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		scopes := []string{}
		clientID := ""
		kind := "console"
		if c, ok := productPolicy(r); ok {
			scopes = c.Scopes
			clientID = c.ClientID
			kind = "product"
		} else if originProduct(r) != "" {
			kind = "legacy_product"
		}
		return respond(w, 200, map[string]any{"kind": kind, "product_id": originProduct(r), "client_id": clientID, "scopes": scopes, "sandbox": s.options.Sandbox, "real_payments_configured": false,
			"unified_wallet_recharge": originProduct(r) == "", "product_wallet_recharge": s.options.Sandbox,
			"refresh_token": false, "server_logout": false, "model_pool": "llmrouter", "amount_encoding": "decimal_integer_string"})
	})
	add("GET /v1/accounts/{account}/calls", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		f, page, e := callQuery(r, true)
		if e != nil {
			return e
		}
		v, e := s.options.Billing.QueryCalls(r.Context(), u.ID, r.PathValue("account"), f, page)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	add("GET /v1/accounts/{account}/usage/summary", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		f, _, e := callQuery(r, false)
		if e != nil {
			return e
		}
		v, e := s.options.Billing.QueryUsageSummary(r.Context(), u.ID, r.PathValue("account"), f)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	add("GET /v1/accounts/{account}/calls/{call}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		q := r.URL.Query()
		for k, v := range q {
			if k != "origin_product_id" || len(v) != 1 {
				return billing.ErrInvalid
			}
		}
		origin := q.Get("origin_product_id")
		if p := originProduct(r); p != "" {
			if origin != "" && origin != p {
				return accounts.ErrForbidden
			}
			origin = p
		}
		v, e := s.options.Billing.QueryCall(r.Context(), u.ID, r.PathValue("account"), r.PathValue("call"), origin)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	add("GET /v1/accounts/{account}/resources", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		account := r.PathValue("account")
		subs, e := s.options.Billing.Subscriptions(r.Context(), u.ID, account)
		if e != nil {
			return e
		}
		out := map[string]any{"account_id": account, "product_id": originProduct(r), "subscriptions": filterProducts(r, subs, func(x billing.Subscription) string { return x.ProductID }), "payments": map[string]any{"mode": map[bool]string{true: "sandbox", false: "unconfigured"}[s.options.Sandbox], "real_payments_configured": false}}
		if origin := originProduct(r); origin != "" {
			profile, e := s.options.Billing.Profile(r.Context(), origin)
			if e != nil {
				return e
			}
			out["billing_profile"] = profile
		}
		policy, explicit := productPolicy(r)
		if !explicit || policy.HasScope("credits:read") {
			bs, e := s.options.Billing.Batches(r.Context(), u.ID, account)
			if e != nil {
				return e
			}
			spendable := new(big.Int)
			reserved := new(big.Int)
			frozen := new(big.Int)
			now := time.Now().UTC()
			for _, b := range bs {
				av, ok := new(big.Int).SetString(b.Available, 10)
				if !ok {
					return billing.ErrInvalid
				}
				rv, ok := new(big.Int).SetString(b.Reserved, 10)
				if !ok {
					return billing.ErrInvalid
				}
				reserved.Add(reserved, rv)
				if b.Frozen {
					frozen.Add(frozen, av)
				} else if !now.Before(b.StartsAt) && now.Before(b.ExpiresAt) {
					spendable.Add(spendable, av)
				}
			}
			out["credits"] = map[string]any{"asset": billing.CreditAsset, "pool": "llmrouter", "spendable": spendable.String(), "reserved": reserved.String(), "frozen": frozen.String()}
		}
		if !explicit || policy.HasScope("wallets:read") {
			role, e := s.accounts.Role(r.Context(), u.ID, account)
			if e != nil {
				return e
			}
			if role == "owner" || role == "billing_admin" {
				var cash []wallet.Balance
				var e error
				if originProduct(r) != "" {
					cash, e = s.wallet.BalancesForProducts(r.Context(), u.ID, account, billingProducts(r))
				} else {
					cash, e = s.wallet.Balances(r.Context(), u.ID, account)
				}
				if e != nil {
					return e
				}
				visible := []wallet.Balance{}
				for _, b := range cash {
					if b.ProductID == "" || allowedBillingProduct(r, b.ProductID) {
						visible = append(visible, b)
					}
				}
				out["wallets"] = visible
			}
		}
		out["as_of"] = time.Now().UTC()
		return respond(w, 200, out)
	})
}

// Continuations restore only signed time bounds. The domain independently binds
// the full query, account, actor and current role on every page.
func callQuery(r *http.Request, paged bool) (billing.CallFilter, billing.CallPageRequest, error) {
	q := r.URL.Query()
	f := billing.CallFilter{}
	page := billing.CallPageRequest{Limit: 50}
	for k, v := range q {
		if len(v) != 1 {
			return f, page, billing.ErrInvalid
		}
		switch k {
		case "from", "to", "model", "origin_product_id", "asset":
		case "limit", "cursor":
			if !paged {
				return f, page, billing.ErrInvalid
			}
		default:
			return f, page, billing.ErrInvalid
		}
	}
	f.Model = q.Get("model")
	f.Asset = q.Get("asset")
	if f.Asset != "" && f.Asset != billing.CreditAsset && f.Asset != billing.CashAsset {
		return f, page, billing.ErrInvalid
	}
	f.OriginProductID = q.Get("origin_product_id")
	if p := originProduct(r); p != "" {
		if f.OriginProductID != "" && f.OriginProductID != p {
			return f, page, accounts.ErrForbidden
		}
		f.OriginProductID = p
	}
	if len(f.Model) > 256 {
		return f, page, billing.ErrInvalid
	}
	if q.Get("cursor") != "" {
		restored, e := billing.RestoreCallFilter(q.Get("cursor"))
		if e != nil {
			return f, page, e
		}
		page.Cursor = q.Get("cursor")
		f.From = restored.From
		f.To = restored.To
		if f.Asset == "" {
			f.Asset = restored.Asset
		}
	}
	for k, dest := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if q.Get(k) != "" {
			v, e := time.Parse(time.RFC3339Nano, q.Get(k))
			if e != nil {
				return f, page, billing.ErrInvalid
			}
			*dest = v.UTC()
		}
	}
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-30 * 24 * time.Hour)
	}
	if !f.From.Before(f.To) || f.To.Sub(f.From) > 90*24*time.Hour {
		return f, page, billing.ErrInvalid
	}
	if q.Get("limit") != "" {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			return f, page, billing.ErrInvalid
		}
		page.Limit = n
	}
	return f, page, nil
}
