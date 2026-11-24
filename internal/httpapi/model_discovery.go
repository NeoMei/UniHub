package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
)

type modelListResponse struct {
	Object string              `json:"object"`
	Data   []modelaccess.Model `json:"data"`
}

func modelBearer(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || !strings.HasPrefix(fields[1], "uh_key_") {
		return "", false
	}
	return fields[1], true
}

// modelList serves the standard OpenAI-compatible /v1/models contract for a
// durable UniHub API key. Scope is read from the key itself; optional product
// headers are only additional equality checks and never select a scope.
func (s *api) modelList(w http.ResponseWriter, r *http.Request) {
	token, ok := modelBearer(r)
	if !ok || r.URL.RawQuery != "" {
		fail(w, http.StatusUnauthorized, "invalid_model_credential")
		return
	}
	product, instance := r.Header.Get("X-UniHub-Product"), r.Header.Get("X-UniHub-Instance")
	discovery, e := s.options.ModelAccess.DiscoverAPIKey(r.Context(), token, product, instance)
	if e != nil {
		if errors.Is(e, modelaccess.ErrCredential) {
			fail(w, http.StatusUnauthorized, "invalid_model_credential")
		} else {
			fail(w, http.StatusServiceUnavailable, "model_catalog_unavailable")
		}
		return
	}
	if discovery.CreditScope == billing.ProductCreditScopeV1 {
		proof, ok := singleProductModelProof(r)
		grant, err := s.options.ModelAccess.VerifyForInstance(r.Context(), token, "", discovery.InstanceID)
		if !ok || err != nil || s.authorizeProductModelGrant(&grant, proof) != nil {
			fail(w, http.StatusForbidden, "forbidden")
			return
		}
	}
	setDiscoveryHeaders(w, discovery)
	_ = respond(w, http.StatusOK, modelListResponse{Object: "list", Data: discovery.Models})
}

func setDiscoveryHeaders(w http.ResponseWriter, discovery modelaccess.Discovery) {
	w.Header().Set("X-UniHub-Account-ID", discovery.AccountID)
	w.Header().Set("X-UniHub-Instance-ID", discovery.InstanceID)
	w.Header().Set("X-UniHub-Origin-Product-ID", discovery.OriginProductID)
	if !discovery.ExpiresAt.IsZero() {
		w.Header().Set("X-UniHub-Expires-At", discovery.ExpiresAt.UTC().Format(time.RFC3339))
	}
}

func (s *api) modelDiscoveryRoute(w http.ResponseWriter, r *http.Request, u accounts.User) error {
	product := originProduct(r)
	if product != "superwagie" && !productWalletRequested(r) {
		return accounts.ErrForbidden
	}
	client, ok := productPolicy(r)
	if !ok {
		return accounts.ErrForbidden
	}
	instance := client.InstanceID
	if instance == "" {
		instance = "default"
	}
	var body struct {
		ModelScope  string `json:"model_scope"`
		FundingMode string `json:"funding_mode,omitempty"`
	}
	if r.ContentLength != 0 {
		if e := decode(w, r, &body); e != nil {
			return e
		}
	}
	creditScope := ""
	if productWalletRequested(r) {
		if err := s.requireProductWallet(r); err != nil {
			return err
		}
		creditScope = billing.ProductCreditScopeV1
	}
	discovery, e := s.options.ModelAccess.DiscoverAccountWithCreditScope(r.Context(), u.ID, r.PathValue("account"), product, instance, body.ModelScope, body.FundingMode, creditScope)
	if e != nil {
		return e
	}
	out := struct {
		modelaccess.Discovery
		ManagedModels        billing.ManagedModels `json:"managed_models,omitzero"`
		ManagedModelsVersion int64                 `json:"managed_models_version,omitempty"`
	}{Discovery: discovery}
	if discovery.OriginProductID == "superwagie" && discovery.CreditScope == billing.ProductCreditScopeV1 && discovery.ModelScope == modelaccess.ScopeLLMRouterAvailable {
		p, err := s.options.Billing.Profile(r.Context(), discovery.OriginProductID)
		if err != nil {
			return err
		}
		if p.Usage.ManagedModels != nil {
			out.ManagedModels, out.ManagedModelsVersion = p.Usage.ManagedModels, p.Version
		}
	}
	setDiscoveryHeaders(w, discovery)
	return respond(w, http.StatusOK, out)
}
