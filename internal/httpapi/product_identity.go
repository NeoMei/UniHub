package httpapi

import (
	"net/http"

	"unihub/internal/accounts"
	"unihub/internal/productauth"
)

func (s *api) addProductIdentityRoutes(add func(string, string, func(http.ResponseWriter, *http.Request, RuntimeService) error)) {
	add("POST /internal/v1/product-runtime/identity/enroll", "identity:link", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.NativeEnrollment
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if !c.matchesIdentityClient(in.ClientID) {
			return accounts.ErrForbidden
		}
		out, err := s.options.ProductAuth.EnrollNativeIdentity(r.Context(), c.ProductID, c.InstanceID, in)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, out)
	})
	add("POST /internal/v1/product-runtime/identity/link/start", "identity:link", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.BeginIdentityLink
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if !c.matchesIdentityClient(in.ClientID) {
			return accounts.ErrForbidden
		}
		out, err := s.options.ProductAuth.BeginIdentityLink(r.Context(), c.ProductID, c.InstanceID, in)
		if err != nil {
			return err
		}
		return respond(w, http.StatusCreated, out)
	})
	add("POST /internal/v1/product-runtime/identity/link/complete", "identity:link", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.CompleteIdentityLink
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if !c.matchesIdentityClient(in.ClientID) {
			return accounts.ErrForbidden
		}
		out, err := s.options.ProductAuth.CompleteIdentityLink(r.Context(), c.ProductID, c.InstanceID, in)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, out)
	})
	add("POST /internal/v1/product-runtime/identity/link/preview", "identity:link", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.CompleteIdentityLink
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if !c.matchesIdentityClient(in.ClientID) {
			return accounts.ErrForbidden
		}
		out, err := s.options.ProductAuth.PreviewIdentityLink(r.Context(), c.ProductID, c.InstanceID, in)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, out)
	})
	add("POST /internal/v1/product-runtime/identity/session/exchange", "identity:session", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.BrowserExchange
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if !c.matchesIdentityClient(in.ClientID) {
			return accounts.ErrForbidden
		}
		out, err := s.options.ProductAuth.ExchangeIdentityBrowser(r.Context(), c.ProductID, c.InstanceID, in)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, out)
	})
	add("POST /internal/v1/product-runtime/identity/session/verify", "identity:session", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in struct {
			SourceProofToken string `json:"source_proof_token"`
			CentralUserID    string `json:"central_user_id"`
		}
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if c.IdentityClientID == "" || len(in.CentralUserID) == 0 || len(in.CentralUserID) > 256 {
			return accounts.ErrForbidden
		}
		if err := s.options.ProductAuth.VerifyProductBrowserSource(r.Context(), in.SourceProofToken, c.ProductID, c.InstanceID, c.IdentityClientID, in.CentralUserID); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	add("POST /internal/v1/product-runtime/identity/source-lifecycle", "identity:source-lifecycle", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in productauth.SourceLifecycle
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if c.IdentityClientID == "" {
			return accounts.ErrForbidden
		}
		out, err := s.options.ProductAuth.SyncSourceLifecycle(r.Context(), c.ProductID, c.InstanceID, in)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, out)
	})
	add("POST /internal/v1/product-runtime/identity/source-lifecycle/status", "identity:source-lifecycle", func(w http.ResponseWriter, r *http.Request, c RuntimeService) error {
		var in struct {
			LocalUserID string `json:"local_user_id"`
		}
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if c.IdentityClientID == "" {
			return accounts.ErrForbidden
		}
		out, err := s.options.ProductAuth.ReadSourceLifecycle(r.Context(), c.ProductID, c.InstanceID, in.LocalUserID)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, out)
	})
}

func (c RuntimeService) matchesIdentityClient(client string) bool {
	return c.IdentityClientID != "" && c.IdentityClientID == client
}
