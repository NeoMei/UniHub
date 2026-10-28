package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/platformadmin"
)

func adminOperationsOptions(r *http.Request) (platformadmin.OperationsOptions, error) {
	q := r.URL.Query()
	o := platformadmin.OperationsOptions{ListOptions: platformadmin.ListOptions{Search: q.Get("q"), Status: q.Get("status"), Cursor: q.Get("cursor")}, AccountID: q.Get("account_id"), ProductID: q.Get("product_id"), UserID: q.Get("user_id")}
	var e error
	if raw := q.Get("limit"); raw != "" {
		o.Limit, e = strconv.Atoi(raw)
		if e != nil || o.Limit < 1 || o.Limit > 100 {
			return o, billing.ErrInvalid
		}
	}
	for _, bound := range []struct {
		k string
		p *time.Time
	}{{"from", &o.From}, {"to", &o.To}} {
		if raw := q.Get(bound.k); raw != "" {
			*bound.p, e = time.Parse(time.RFC3339, raw)
			if e != nil {
				return o, billing.ErrInvalid
			}
		}
	}
	return o, nil
}

func (s *api) adminOperationRoutes(add func(string, action)) {
	a := s.options.PlatformAdmin
	add("GET /admin/v1/overview", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := a.OperationOverview(r.Context(), u.ID)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	for _, resource := range []string{"accounts", "api-keys", "calls", "transactions", "recharges", "orders", "refunds", "subscriptions", "credit-batches", "products", "offers", "rates", "billing-profiles", "usage-budgets", "audit-events"} {
		add("GET /admin/v1/"+resource, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			o, e := adminOperationsOptions(r)
			if e != nil {
				return e
			}
			v, e := a.ListOperations(r.Context(), u.ID, resource, o)
			if e != nil {
				return e
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if e = a.CompleteOperationPage(ctx, u.ID, resource, s.options.Billing, &v); e != nil {
				if resource == "credit-batches" {
					fail(w, 503, "ledger_balance_unavailable")
					return nil
				}
				return e
			}
			return respond(w, 200, v)
		})
	}
	add("GET /admin/v1/call-attempts", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := a.Require(r.Context(), u.ID, "calls:read"); e != nil {
			return e
		}
		fail(w, 409, "supplier_attempt_telemetry_unavailable")
		return nil
	})
	add("GET /admin/v1/wallets", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := a.Require(r.Context(), u.ID, "finance:read"); e != nil {
			return e
		}
		o, e := adminOperationsOptions(r)
		if e != nil {
			return e
		}
		if o.AccountID == "" {
			return billing.ErrInvalid
		}
		if s.options.Billing == nil {
			fail(w, 503, "ledger_not_configured")
			return nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, e := s.options.Billing.AdminCashBalance(ctx, a, u.ID, o.AccountID, o.ProductID)
		if e != nil {
			if errors.Is(e, accounts.ErrNotFound) || errors.Is(e, accounts.ErrForbidden) || errors.Is(e, billing.ErrInvalid) {
				return e
			}
			fail(w, 503, "ledger_balance_unavailable")
			return nil
		}
		if e = a.RecordOperationRead(r.Context(), u.ID, "wallets", o); e != nil {
			return e
		}
		v["as_of"] = time.Now().UTC()
		return respond(w, 200, platformadmin.Page[map[string]any]{Items: []map[string]any{v}, Total: 1, AsOf: time.Now().UTC()})
	})
	add("GET /admin/v1/system-status", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := a.OperationSystemStatus(r.Context(), u.ID)
		if e != nil {
			return e
		}
		v["sandbox"] = s.options.Sandbox
		v["payment_configured"] = s.options.Merchant != "" && len(s.options.SigningKey) >= 32
		v["billing_configured"] = s.options.Billing != nil
		return respond(w, 200, v)
	})
	add("GET /admin/v1/exports", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminOperationsOptions(r)
		if e != nil {
			return e
		}
		resource := r.URL.Query().Get("resource")
		v, e := a.ExportOperations(r.Context(), u.ID, resource, o)
		if e != nil {
			return e
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="unihub-admin-`+resource+`.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		_, e = w.Write(v)
		return e
	})
	add("POST /admin/v1/api-keys/{key}/revoke", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Reason  string `json:"reason"`
			Version *int64 `json:"version"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil {
			return billing.ErrInvalid
		}
		v, e := a.RevokeOperationKey(r.Context(), u.ID, r.PathValue("key"), body.Reason, *body.Version)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	add("POST /admin/v1/transactions/{command}/reconcile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := a.Require(r.Context(), u.ID, "finance:write"); e != nil {
			return e
		}
		var body struct {
			Source  string  `json:"source"`
			Reason  string  `json:"reason"`
			Version *string `json:"version"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil || r.Header.Get("Idempotency-Key") == "" {
			return billing.ErrInvalid
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var out map[string]any
		var e error
		switch body.Source {
		case "billing_commands":
			if s.options.Billing == nil {
				return billing.ErrUnsupported
			}
			out, e = s.options.Billing.AdminReconcile(ctx, a, u.ID, r.PathValue("command"), *body.Version, body.Reason, r.Header.Get("Idempotency-Key"))
		case "wallet_operations":
			if s.wallet == nil {
				return billing.ErrUnsupported
			}
			out, e = s.wallet.AdminReconcile(ctx, a, u.ID, r.PathValue("command"), *body.Version, body.Reason, r.Header.Get("Idempotency-Key"))
		default:
			return billing.ErrInvalid
		}
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	add("GET /admin/v1/accounts/{account}/usage-budget", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := a.Require(r.Context(), u.ID, "finance:read"); e != nil {
			return e
		}
		if s.options.Billing == nil {
			return billing.ErrUnsupported
		}
		q := r.URL.Query()
		instance := q.Get("instance_id")
		if instance == "" {
			instance = "default"
		}
		v, e := s.options.Billing.AdminUsageBudget(r.Context(), a, u.ID, r.PathValue("account"), q.Get("product_id"), instance, q.Get("funding_mode"))
		if e != nil {
			return e
		}
		if e = a.RecordOperationRead(r.Context(), u.ID, "usage-budget", platformadmin.OperationsOptions{AccountID: r.PathValue("account"), ProductID: q.Get("product_id")}); e != nil {
			return e
		}
		return respond(w, 200, v)
	})
	s.adminFinanceOperationRoutes(add)
}

func (s *api) adminFinanceOperationRoutes(add func(string, action)) {
	run := func(w http.ResponseWriter, r *http.Request, u accounts.User, request billing.AdminOperation) error {
		if e := s.options.PlatformAdmin.Require(r.Context(), u.ID, "finance:write"); e != nil {
			return e
		}
		if s.options.Billing == nil {
			fail(w, 503, "billing_not_configured")
			return nil
		}
		v, e := s.options.Billing.Administer(r.Context(), s.options.PlatformAdmin, u.ID, request)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	}
	saveOffer := func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Offer   billing.Offer `json:"offer"`
			Reason  string        `json:"reason"`
			Version *string       `json:"version"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil {
			return billing.ErrInvalid
		}
		id := r.PathValue("offer")
		if id == "" {
			id = body.Offer.ID
		}
		return run(w, r, u, billing.AdminOperation{Action: "offer.save", ID: id, Offer: body.Offer, Version: *body.Version, Reason: body.Reason})
	}
	add("POST /admin/v1/offers", saveOffer)
	add("PUT /admin/v1/offers/{offer}", saveOffer)
	for _, action := range []string{"publish", "retire"} {
		add("POST /admin/v1/offers/{offer}/"+action, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			var body struct {
				Reason  string  `json:"reason"`
				Version *string `json:"version"`
			}
			if e := decode(w, r, &body); e != nil {
				return e
			}
			if body.Version == nil {
				return billing.ErrInvalid
			}
			return run(w, r, u, billing.AdminOperation{Action: "offer." + action, ID: r.PathValue("offer"), Version: *body.Version, Reason: body.Reason})
		})
	}
	add("POST /admin/v1/rates", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Rate    billing.Rate `json:"rate"`
			Reason  string       `json:"reason"`
			Version *int64       `json:"version"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil || *body.Version != 0 {
			return billing.ErrInvalid
		}
		return run(w, r, u, billing.AdminOperation{Action: "rate.publish", ID: body.Rate.ID, Rate: body.Rate, Reason: body.Reason, Version: "0"})
	})
	add("POST /admin/v1/rates/{rate}/retire", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Reason  string  `json:"reason"`
			Version *string `json:"version"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil {
			return billing.ErrInvalid
		}
		return run(w, r, u, billing.AdminOperation{Action: "rate.retire", ID: r.PathValue("rate"), Reason: body.Reason, Version: *body.Version})
	})
	add("PUT /admin/v1/billing-profiles/{product}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Profile billing.BillingProfile `json:"profile"`
			Reason  string                 `json:"reason"`
			Version *int64                 `json:"version"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil || *body.Version != body.Profile.Version {
			return billing.ErrInvalid
		}
		return run(w, r, u, billing.AdminOperation{Action: "profile.save", ProductID: r.PathValue("product"), Profile: body.Profile, Reason: body.Reason, Version: strconv.FormatInt(*body.Version, 10)})
	})
	add("POST /admin/v1/accounts/{account}/orders/{order}/refund-review", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			Decision string  `json:"decision"`
			Reason   string  `json:"reason"`
			Version  *string `json:"version"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil {
			return billing.ErrInvalid
		}
		return run(w, r, u, billing.AdminOperation{Action: "refund.review", ID: r.PathValue("order"), AccountID: r.PathValue("account"), Decision: body.Decision, Reason: body.Reason, Version: *body.Version})
	})
	add("PUT /admin/v1/accounts/{account}/usage-budget", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			ProductID   string `json:"product_id"`
			InstanceID  string `json:"instance_id"`
			FundingMode string `json:"funding_mode"`
			MaxTotal    string `json:"max_total"`
			MaxPerCall  string `json:"max_per_call"`
			Version     *int64 `json:"version"`
			Reason      string `json:"reason"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil {
			return billing.ErrInvalid
		}
		return run(w, r, u, billing.AdminOperation{Action: "budget.save", AccountID: r.PathValue("account"), ProductID: body.ProductID, InstanceID: body.InstanceID, FundingMode: body.FundingMode, Budget: billing.UsageBudgetRequest{Version: *body.Version, MaxTotal: body.MaxTotal, MaxPerCall: body.MaxPerCall}, Version: strconv.FormatInt(*body.Version, 10), Reason: body.Reason})
	})
}
