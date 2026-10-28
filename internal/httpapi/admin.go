package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/platformadmin"
)

func adminListOptions(r *http.Request) (platformadmin.ListOptions, error) {
	q := r.URL.Query()
	o := platformadmin.ListOptions{Search: q.Get("q"), Cursor: q.Get("cursor"), Status: q.Get("status"), Role: q.Get("role"), Limit: 50}
	if q.Get("limit") != "" {
		v, e := strconv.Atoi(q.Get("limit"))
		if e != nil {
			return o, accounts.ErrInvalid
		}
		o.Limit = v
	}
	for _, b := range []struct {
		k      string
		target *time.Time
	}{{"created_from", &o.CreatedFrom}, {"created_to", &o.CreatedTo}} {
		if raw := q.Get(b.k); raw != "" {
			v, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return o, accounts.ErrInvalid
			}
			*b.target = v
		}
	}

	return o, nil
}
func (s *api) adminRoutes(add func(string, action)) {
	if s.options.PlatformAdmin == nil {
		return
	}
	central := func(pattern string, f action) {
		add(pattern, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			w.Header().Set("Cache-Control", "no-store")
			if originProduct(r) != "" {
				return accounts.ErrForbidden
			}
			p, e := s.options.PlatformAdmin.Principal(r.Context(), u.ID)
			if e != nil {
				return e
			}
			if !p.Enabled && pattern != "GET /admin/v1/me" {
				return accounts.ErrForbidden
			}
			return f(w, r, u)
		})
	}
	central("GET /admin/v1/me", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		p, e := s.options.PlatformAdmin.Principal(r.Context(), u.ID)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("GET /admin/v1/users", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminListOptions(r)
		if e != nil {
			return e
		}
		p, e := s.options.PlatformAdmin.ListUsers(r.Context(), u.ID, o)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("GET /admin/v1/users/{user}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		p, e := s.options.PlatformAdmin.GetUser(r.Context(), u.ID, r.PathValue("user"))
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("GET /admin/v1/admins", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminListOptions(r)
		if e != nil {
			return e
		}
		p, e := s.options.PlatformAdmin.ListAdmins(r.Context(), u.ID, o)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("PUT /admin/v1/admins/{user}/roles", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Roles   *[]string `json:"roles"`
			Version *int64    `json:"version"`
			Reason  string    `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Roles == nil || b.Version == nil {
			return accounts.ErrInvalid
		}
		p, e := s.options.PlatformAdmin.SetRoles(r.Context(), u.ID, r.PathValue("user"), *b.Roles, *b.Version, b.Reason)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("PUT /admin/v1/users/{user}/status", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Active  *bool  `json:"active"`
			Version *int64 `json:"version"`
			Reason  string `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Active == nil || b.Version == nil {
			return accounts.ErrInvalid
		}
		p, e := s.options.PlatformAdmin.SetStatus(r.Context(), u.ID, r.PathValue("user"), *b.Active, *b.Version, b.Reason)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("POST /admin/v1/users/status-batch", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Items []struct {
				UserID  string `json:"user_id"`
				Active  *bool  `json:"active"`
				Version *int64 `json:"version"`
			} `json:"items"`
			Reason string `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if len(b.Items) < 1 || len(b.Items) > 100 {
			return accounts.ErrInvalid
		}
		results := []map[string]any{}
		for _, i := range b.Items {
			result := map[string]any{"user_id": i.UserID}
			if i.Active == nil || i.Version == nil {
				return accounts.ErrInvalid
			}
			v, e := s.options.PlatformAdmin.SetOrdinaryStatus(r.Context(), u.ID, i.UserID, *i.Active, *i.Version, b.Reason)
			if e != nil {
				code := "operation_failed"
				switch {
				case errors.Is(e, accounts.ErrForbidden):
					code = "forbidden"
				case errors.Is(e, accounts.ErrConflict):
					code = "conflict"
				case errors.Is(e, accounts.ErrInvalid):
					code = "invalid_request"
				case errors.Is(e, accounts.ErrNotFound):
					code = "not_found"
				}
				result["error"] = code
			} else {
				result["user"] = v
			}

			results = append(results, result)
		}
		return respond(w, 200, map[string]any{"items": results})
	})
	central("GET /admin/v1/users/{user}/sessions", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminListOptions(r)
		if e != nil {
			return e
		}
		p, e := s.options.PlatformAdmin.ListSessions(r.Context(), u.ID, r.PathValue("user"), o)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("DELETE /admin/v1/users/{user}/sessions/{session}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Version *int64 `json:"version"`
			Reason  string `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Version == nil {
			return accounts.ErrInvalid
		}
		if e := s.options.PlatformAdmin.RevokeSession(r.Context(), u.ID, r.PathValue("user"), r.PathValue("session"), *b.Version, b.Reason); e != nil {
			return e
		}
		w.WriteHeader(204)
		return nil
	})
	central("POST /admin/v1/users/{user}/sessions/revoke-local", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Version *int64 `json:"version"`
			Reason  string `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Version == nil {
			return accounts.ErrInvalid
		}
		p, e := s.options.PlatformAdmin.RevokeLocalSessions(r.Context(), u.ID, r.PathValue("user"), *b.Version, b.Reason)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("GET /admin/v1/users/{user}/product-access", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		p, e := s.options.PlatformAdmin.UserProductAccess(r.Context(), u.ID, r.PathValue("user"))
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("PUT /admin/v1/users/{user}/product-access/{product}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Roles   *[]string `json:"roles"`
			Active  *bool     `json:"active"`
			Version *int64    `json:"version"`
			Reason  string    `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Roles == nil || b.Active == nil || b.Version == nil {
			return accounts.ErrInvalid
		}
		p, e := s.options.PlatformAdmin.SetProductAccess(r.Context(), u.ID, r.PathValue("user"), r.PathValue("product"), r.URL.Query().Get("instance"), *b.Roles, *b.Active, *b.Version, b.Reason)
		if e != nil {
			return e
		}
		return respond(w, 200, p)
	})
	central("GET /admin/v1/access-products", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := s.options.PlatformAdmin.Require(r.Context(), u.ID, "products:read"); e != nil {
			return e
		}
		if s.options.ProductUsers == nil {
			return respond(w, 200, []any{})
		}
		return respond(w, 200, s.options.ProductUsers.Products())
	})

	central("POST /admin/v1/users/{user}/api-keys/revoke", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			Version *int64 `json:"version"`
			Reason  string `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		if b.Version == nil {
			return accounts.ErrInvalid
		}
		n, e := s.options.PlatformAdmin.RevokeUserKeys(r.Context(), u.ID, r.PathValue("user"), *b.Version, b.Reason)
		if e != nil {
			return e
		}
		return respond(w, 200, map[string]any{"revoked": n})
	})

	s.adminOperationRoutes(central)
	s.adminIdentityRoutes(central)
}
