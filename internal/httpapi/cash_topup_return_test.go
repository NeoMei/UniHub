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

func TestCashTopupUsesVerifiedProductForUnifiedWallet(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/accounts/a/cash-checkouts?wallet_target=unified", nil)
	assert.False(t, cashTopupUsesUnifiedWallet(r, "llmrouter"))
	for _, id := range []string{"llmrouter", "superwagie"} {
		ctx := context.WithValue(r.Context(), productPolicyKey{}, products.Client{ProductID: id})
		ctx = context.WithValue(ctx, productIdentityKey{}, id)
		assert.Equal(t, id == "llmrouter", cashTopupUsesUnifiedWallet(r.WithContext(ctx), "llmrouter"))
		assert.False(t, cashTopupUsesUnifiedWallet(r.WithContext(ctx), ""))
		assert.False(t, cashTopupUsesUnifiedWallet(r.WithContext(ctx), "superwagie"))
	}
}
