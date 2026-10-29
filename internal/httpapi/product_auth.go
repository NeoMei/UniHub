package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
	add("POST /internal/v1/product-runtime/identity/browser/source/verify", "identity:browser", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		if c.FinanceClientID == "" || r.ContentLength > 4096 || len(r.Header.Values("Origin")) != 0 {
			return accounts.ErrForbidden
		}
		var in struct {
			SourceProofToken string `json:"source_proof_token"`
			CentralUserID    string `json:"central_user_id"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		start, err := d.Token()
		if err != nil || start != json.Delim('{') {
			return accounts.ErrForbidden
		}
		seen := map[string]bool{}
		for d.More() {
			keyToken, tokenErr := d.Token()
			key, ok := keyToken.(string)
			if tokenErr != nil || !ok || seen[key] {
				return accounts.ErrForbidden
			}
			seen[key] = true
			switch key {
			case "source_proof_token":
				err = d.Decode(&in.SourceProofToken)
			case "central_user_id":
				err = d.Decode(&in.CentralUserID)
			default:
				return accounts.ErrForbidden
			}
			if err != nil {
				return accounts.ErrForbidden
			}
		}
		end, endErr := d.Token()
		if endErr != nil || end != json.Delim('}') || len(seen) != 2 || d.Decode(&struct{}{}) != io.EOF || len(in.SourceProofToken) != len("ubsp_")+64 || len(in.CentralUserID) == 0 || len(in.CentralUserID) > 256 || strings.ContainsAny(in.CentralUserID, "\r\n\x00") {
			return accounts.ErrForbidden
		}
		if err := s.options.ProductAuth.VerifyBrowserSourceProof(r.Context(), in.SourceProofToken, c.ProductID, c.InstanceID, c.FinanceClientID, in.CentralUserID); err != nil {
			return accounts.ErrForbidden
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
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
