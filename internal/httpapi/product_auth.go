package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/productauth"
)

func (s *api) productAuthRoutes(m *http.ServeMux) {
	if s.options.ProductAuth == nil {
		return
	}
	add := func(pattern, op string, next func(http.ResponseWriter, *http.Request, RuntimeService) error) {
		m.HandleFunc(pattern, s.runtimeAuthenticated(op, func(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
			if r.URL.RawQuery != "" {
				return accounts.ErrInvalid
			}
			return next(w, r, service)
		}))
	}
	add("POST /internal/v1/product-runtime/identity/register", "identity:register", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.Registration
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := s.options.ProductAuth.Register(r.Context(), c.ProductID, c.InstanceID, in)
		if e != nil {
			return e
		}
		return respond(w, 201, out)
	})
	add("POST /internal/v1/product-runtime/identity/authenticate", "identity:authenticate", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.Authentication
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := s.options.ProductAuth.Authenticate(r.Context(), c.ProductID, c.InstanceID, in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	add("POST /internal/v1/product-runtime/identity/import", "identity:import", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.Import
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := s.options.ProductAuth.Import(r.Context(), c.ProductID, c.InstanceID, in)
		if e != nil {
			return e
		}
		return respond(w, 201, out)
	})
	add("POST /internal/v1/product-runtime/identity/lifecycle", "identity:lifecycle", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.Lifecycle
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := s.options.ProductAuth.SyncLifecycle(r.Context(), c.ProductID, c.InstanceID, in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})

	add("POST /internal/v1/product-runtime/identity/browser/approve", "identity:browser", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		raw, ok := runtimeBearer(r, "X-UniHub-Actor-Authorization")
		if !ok {
			return errRuntimeActor
		}
		var in productauth.BrowserApproval
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := s.options.ProductAuth.ApproveBrowserHandoff(r.Context(), raw, c.ProductID, c.InstanceID, in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	add("POST /internal/v1/product-runtime/identity/browser/exchange", "identity:browser", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.BrowserExchange
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := s.options.ProductAuth.ExchangeBrowser(r.Context(), c.ProductID, c.InstanceID, in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})

	add("GET /internal/v1/product-runtime/identity/{local}", "identity:lookup", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		out, e := s.options.ProductAuth.Lookup(r.Context(), c.ProductID, c.InstanceID, r.PathValue("local"))
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	add("PATCH /internal/v1/product-runtime/identity/{local}", "identity:manage", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		actor, _, e := s.runtimeActor(r, c)
		if e != nil {
			return e
		}
		var in productauth.Mutation
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := s.options.ProductAuth.Mutate(r.Context(), c.ProductID, c.InstanceID, actor.ID, r.PathValue("local"), in)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
}
