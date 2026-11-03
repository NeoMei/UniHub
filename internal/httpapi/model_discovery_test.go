package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelBearerRejectsAmbiguousOrWrongCredentialHeaders(t *testing.T) {
	for name, headers := range map[string][]string{
		"missing":      nil,
		"duplicate":    {"Bearer uh_key_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Bearer uh_key_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"wrong-prefix": {"Bearer uh_model_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"wrong-scheme": {"Basic uh_key_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			for _, value := range headers {
				r.Header.Add("Authorization", value)
			}
			if token, ok := modelBearer(r); ok || token != "" {
				t.Fatalf("accepted malformed bearer: %q", token)
			}
		})
	}
}

func TestModelBearerAcceptsSingleKeyWithoutExposingIt(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer uh_key_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	token, ok := modelBearer(r)
	if !ok || token == "" {
		t.Fatal("valid key bearer was rejected")
	}
}
