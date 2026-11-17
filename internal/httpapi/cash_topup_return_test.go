package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"unihub/internal/products"
)

func TestCashTopupReturnUsesVerifiedProductOrigin(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/accounts/a/cash-checkouts?return_url=https://evil.example", nil)
	assert.Empty(t, cashTopupReturnURL(r))
	for _, tc := range []struct{ product, origin, want string }{
		{"llmrouter", "https://llmrouter.quukk.com", "https://llmrouter.quukk.com/wallet"},
		{"superwagie", "https://superwagie.quukk.com", ""},
		{"llmrouter", "https://evil.example/path", ""},
	} {
		t.Run(tc.product+tc.origin, func(t *testing.T) {
			policy := products.Client{ProductID: tc.product, Origins: []string{tc.origin}}
			req := r.WithContext(context.WithValue(r.Context(), productPolicyKey{}, policy))
			assert.Equal(t, tc.want, cashTopupReturnURL(req))
		})
	}
}
