package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/productusers"
)

type accessExpiryKey struct{}

func (s *api) productUserRoutes(add func(string, action)) {
	// Capabilities is identity policy, independent of billing configuration.
	add("GET /v1/capabilities", s.capabilities)
	if s.options.ProductUsers == nil {
		return
	}
	add("GET /v1/me/product-access", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product, instance, e := s.accessTarget(r)
		if e != nil {
			return e
		}
		a, e := s.options.ProductUsers.Resolve(r.Context(), u, product, instance)
		if e != nil {
			return e
		}
		a.ExpiresAt, _ = r.Context().Value(accessExpiryKey{}).(int64)
		if a.ExpiresAt <= time.Now().Unix() {
			return accounts.ErrForbidden
		}
		return respond(w, 200, a)
	})
	add("GET /v1/me/applications", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product, instance := "", ""
		if originProduct(r) != "" {
			c, ok := productPolicy(r)
			if !ok {
				return accounts.ErrForbidden
			}
			product, instance = c.ProductID, c.InstanceID
		}
		apps, e := s.options.ProductUsers.Applications(r.Context(), u, product, instance)
		if e != nil {
			return e
		}
		expiry, _ := r.Context().Value(accessExpiryKey{}).(int64)
		for i := range apps {
			apps[i].ExpiresAt = expiry
		}
		return respond(w, 200, apps)
	})
	add("GET /v1/user-management", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if originProduct(r) != "" {
			return accounts.ErrForbidden
		}
		return respond(w, 200, map[string]bool{"enabled": s.options.ProductUsers.Enabled(), "can_manage": s.options.ProductUsers.CanManage(u.ID)})
	})
	admin := func(pattern string, f action) {
		add(pattern, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			if originProduct(r) != "" || !s.options.ProductUsers.CanManage(u.ID) {
				return accounts.ErrForbidden
			}
			return f(w, r, u)
		})
	}
	admin("GET /v1/user-management/products", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		return respond(w, 200, s.options.ProductUsers.Products())
	})
	admin("GET /v1/user-management/users", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			v, e := strconv.Atoi(raw)
			if e != nil {
				return accounts.ErrInvalid
			}
			limit = v
		}
		out, e := s.options.ProductUsers.Users(r.Context(), r.URL.Query().Get("q"), limit)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	admin("GET /v1/user-management/users/{user}/access", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		out, e := s.options.ProductUsers.Assignments(r.Context(), r.PathValue("user"))
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	admin("PUT /v1/user-management/users/{user}/access/{product}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Roles   *[]string `json:"roles"`
			Active  *bool     `json:"active"`
			Version *int64    `json:"version"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Version == nil || b.Active == nil || b.Roles == nil {
			return accounts.ErrInvalid
		}
		out, e := s.options.ProductUsers.SetAccess(r.Context(), u.ID, r.PathValue("user"), r.PathValue("product"), r.URL.Query().Get("instance"), *b.Roles, *b.Active, *b.Version)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	admin("PUT /v1/user-management/users/{user}/status", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Active  *bool  `json:"active"`
			Version *int64 `json:"version"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Version == nil || b.Active == nil {
			return accounts.ErrInvalid
		}
		out, e := s.options.ProductUsers.SetStatus(r.Context(), u.ID, r.PathValue("user"), *b.Active, *b.Version)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
}
func (s *api) accessTarget(r *http.Request) (string, string, error) {
	q := r.URL.Query()
	if originProduct(r) != "" {
		c, ok := productPolicy(r)
		if !ok {
			return "", "", accounts.ErrForbidden
		}
		instance := productusers.Instance(c.InstanceID)
		if (q.Has("product_id") && q.Get("product_id") != c.ProductID) || (q.Has("instance") && q.Get("instance") != instance) {
			return "", "", accounts.ErrForbidden
		}
		return c.ProductID, instance, nil
	}
	if q.Get("product_id") == "" {
		return "", "", accounts.ErrInvalid
	}
	return q.Get("product_id"), productusers.Instance(q.Get("instance")), nil
}
func (s *api) capabilities(w http.ResponseWriter, r *http.Request, u accounts.User) error {
	scopes := []string{}
	clientID, instance, kind := "", "", "console"
	if c, ok := productPolicy(r); ok {
		scopes = c.Scopes
		clientID = c.ClientID
		instance = productusers.Instance(c.InstanceID)
		kind = "product"
	} else if originProduct(r) != "" {
		kind = "legacy_product"
	}
	return respond(w, 200, map[string]any{"kind": kind, "product_id": originProduct(r), "instance_id": instance, "client_id": clientID, "scopes": scopes, "sandbox": s.options.Sandbox, "real_payments_configured": false, "unified_wallet_recharge": originProduct(r) == "", "product_wallet_recharge": s.options.Sandbox, "refresh_token": false, "server_logout": false, "model_pool": "llmrouter", "amount_encoding": "decimal_integer_string"})
}

// Keep verified expiry in request context rather than accepting any client field.
func withAccessExpiry(r *http.Request, c auth.Claims) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), accessExpiryKey{}, c.ExpiresAt))
}
