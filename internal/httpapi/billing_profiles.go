package httpapi

import (
	"errors"
	"net/http"

	"unihub/internal/accounts"
	"unihub/internal/billing"
)

func (s *api) billingProfileRoutes(m *http.ServeMux, add func(string, action), addOps func(string, action)) {
	b := s.options.Billing
	m.HandleFunc("GET /v1/products/{product}/managed-models", func(w http.ResponseWriter, r *http.Request) {
		p, err := b.Profile(r.Context(), r.PathValue("product"))
		if errors.Is(err, billing.ErrInvalid) {
			err = accounts.ErrNotFound
		}
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		_ = respond(w, http.StatusOK, struct {
			ProductID     string                `json:"product_id"`
			Version       int64                 `json:"version"`
			ManagedModels billing.ManagedModels `json:"managed_models"`
		}{p.ProductID, p.Version, p.Usage.ManagedModels})
	})
	add("GET /v1/billing-presets", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		out := map[string]billing.BillingProfile{}
		for _, name := range []string{"hybrid", "prepaid", "subscription"} {
			p, e := billing.BillingPreset("template", name)
			if e != nil {
				return e
			}
			out[name] = p
		}
		return respond(w, 200, out)
	})
	add("GET /v1/products/{product}/billing-profile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product := r.PathValue("product")
		if origin := originProduct(r); origin != "" && product != origin && !allowedBillingProduct(r, product) {
			return accounts.ErrForbidden
		}
		p, e := b.Profile(r.Context(), product)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	addOps("GET /ops/v1/products/{product}/billing-profile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		// Domain writes enforce the configured operator; this read has the distinct
		// operator token boundary and reveals only public commercial metadata.
		p, e := b.Profile(r.Context(), r.PathValue("product"))
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	addOps("PUT /ops/v1/products/{product}/billing-profile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			CreditScope            string                    `json:"credit_scope,omitempty"`
			ProductID              string                    `json:"product_id"`
			Version                *int64                    `json:"version"`
			Subscription           *billing.BillingComponent `json:"subscription"`
			IncludedCredits        *billing.BillingComponent `json:"included_credits"`
			Addons                 *billing.BillingComponent `json:"addons"`
			Usage                  *billing.UsagePolicy      `json:"usage"`
			NativeCreditConversion *billing.NativeConversion `json:"native_credit_conversion,omitempty"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil || body.Subscription == nil || body.IncludedCredits == nil || body.Addons == nil || body.Usage == nil {
			return billing.ErrInvalid
		}
		p := billing.BillingProfile{CreditScope: body.CreditScope, NativeCreditConversion: body.NativeCreditConversion, ProductID: body.ProductID, Version: *body.Version, Subscription: *body.Subscription, IncludedCredits: *body.IncludedCredits, Addons: *body.Addons, Usage: *body.Usage}
		saved, e := b.SaveProfile(r.Context(), u.ID, r.PathValue("product"), p)
		if e != nil {
			return e
		}
		return respond(w, 200, saved)
	})
}
