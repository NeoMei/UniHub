package httpapi

import (
	"net/http/httptest"
	"testing"
	"unihub/internal/auth"
)

func TestVerifiedBrowserIdentityDoesNotOverrideExplicitBearer(t *testing.T) {
	h := setup(t)
	for _, tc := range []struct {
		header string
		want   int
	}{{"", 200}, {"Bearer invalid", 401}, {"Basic invalid", 401}} {
		r := httptest.NewRequest("GET", "/v1/me", nil)
		r = r.WithContext(auth.WithBrowserClaims(r.Context(), auth.Claims{Issuer: "https://test", Subject: "alice", Name: "Alice"}))
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("header=%q status=%d want=%d", tc.header, w.Code, tc.want)
		}
	}
}
