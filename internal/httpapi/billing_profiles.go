package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
)

func (s *api) billingProfileRoutes(add func(string, action), addOps func(string, action)) {
	b := s.options.Billing
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
			ProductID       string                    `json:"product_id"`
			Version         *int64                    `json:"version"`
			Subscription    *billing.BillingComponent `json:"subscription"`
			IncludedCredits *billing.BillingComponent `json:"included_credits"`
			Addons          *billing.BillingComponent `json:"addons"`
			Usage           *billing.UsagePolicy      `json:"usage"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.Version == nil || body.Subscription == nil || body.IncludedCredits == nil || body.Addons == nil || body.Usage == nil {
			return billing.ErrInvalid
		}
		p := billing.BillingProfile{ProductID: body.ProductID, Version: *body.Version, Subscription: *body.Subscription, IncludedCredits: *body.IncludedCredits, Addons: *body.Addons, Usage: *body.Usage}
		saved, e := b.SaveProfile(r.Context(), u.ID, r.PathValue("product"), p)
		if e != nil {
			return e
		}
		return respond(w, 200, saved)
	})
}
