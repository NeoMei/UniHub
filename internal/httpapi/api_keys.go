package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
)

// Product and instance always come from the verified client registry. Console
// clients select a product explicitly; the payer still comes from account roles.
func apiKeyScope(r *http.Request, product, instance string) (string, string, error) {
	if origin := originProduct(r); origin != "" {
		if product != "" && product != origin {
			return "", "", accounts.ErrForbidden
		}
		product = origin
		client, _ := productPolicy(r)
		bound := client.InstanceID
		if bound == "" {
			bound = "default"
		}
		if instance != "" && instance != bound {
			return "", "", accounts.ErrForbidden
		}
		instance = bound
	}
	if instance == "" {
		instance = "default"
	}
	if product == "" {
		return "", "", billing.ErrInvalid
	}
	return product, instance, nil
}

func (s *api) apiKeyRoutes(add func(string, action)) {
	a := s.options.ModelAccess
	b := s.options.Billing
	add("POST /v1/accounts/{account}/api-keys", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body modelaccess.APIKeyRequest
		if e := decode(w, r, &body); e != nil {
			return e
		}
		var e error
		body.OriginProductID, body.InstanceID, e = apiKeyScope(r, body.OriginProductID, body.InstanceID)
		if e != nil {
			return e
		}
		out, e := a.CreateAPIKey(r.Context(), u.ID, r.PathValue("account"), body)
		if e != nil {
			return e
		}
		return respond(w, http.StatusCreated, out)
	})
	add("GET /v1/accounts/{account}/api-keys", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product, instance, e := apiKeyScope(r, r.URL.Query().Get("product_id"), r.URL.Query().Get("instance_id"))
		if e != nil {
			return e
		}
		out, e := a.ListAPIKeys(r.Context(), u.ID, r.PathValue("account"), product, instance)
		if e != nil {
			return e
		}
		return respond(w, http.StatusOK, out)
	})
	add("DELETE /v1/accounts/{account}/api-keys/{key}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product, instance, e := apiKeyScope(r, r.URL.Query().Get("product_id"), r.URL.Query().Get("instance_id"))
		if e != nil {
			return e
		}
		if e = a.RevokeAPIKey(r.Context(), u.ID, r.PathValue("account"), r.PathValue("key"), product, instance); e != nil {
			return e
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	budgetScope := func(r *http.Request) (string, string, string, error) {
		product, instance, e := apiKeyScope(r, r.URL.Query().Get("product_id"), r.URL.Query().Get("instance_id"))
		if e != nil {
			return "", "", "", e
		}
		mode := r.URL.Query().Get("funding_mode")
		if mode == "" {
			profile, e := b.Profile(r.Context(), product)
			if e != nil {
				return "", "", "", e
			}
			mode = profile.Usage.DefaultFundingMode
		}
		return product, instance, mode, nil
	}
	add("GET /v1/accounts/{account}/usage-budget", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product, instance, mode, e := budgetScope(r)
		if e != nil {
			return e
		}
		out, e := b.GetUsageBudget(r.Context(), u.ID, r.PathValue("account"), product, instance, mode)
		if e != nil {
			return e
		}
		return respond(w, http.StatusOK, out)
	})
	add("PUT /v1/accounts/{account}/usage-budget", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body billing.UsageBudgetRequest
		if e := decode(w, r, &body); e != nil {
			return e
		}
		product, instance, mode, e := budgetScope(r)
		if e != nil {
			return e
		}
		out, e := b.SetUsageBudget(r.Context(), u.ID, r.PathValue("account"), product, instance, mode, body)
		if e != nil {
			return e
		}
		return respond(w, http.StatusOK, out)
	})
}
