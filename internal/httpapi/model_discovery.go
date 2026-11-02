package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"unihub/internal/accounts"
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
	if originProduct(r) != "superwagie" {
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
	discovery, e := s.options.ModelAccess.DiscoverAccount(r.Context(), u.ID, r.PathValue("account"), "superwagie", instance)
	if e != nil {
		return e
	}
	setDiscoveryHeaders(w, discovery)
	return respond(w, http.StatusOK, discovery)
}
