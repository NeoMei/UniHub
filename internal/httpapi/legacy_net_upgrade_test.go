package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/products"
)

func TestLegacyNetUpgradeRequiresRegisteredSuperWagieCapability(t *testing.T) {
	legacyNet := billing.Quote{
		Offer:                billing.Offer{ProductID: "superwagie"},
		ProductCreditUpgrade: &billing.ProductCreditUpgrade{AppliedCreditAmount: "0"},
	}
	ordinary := legacyNet
	ordinary.ProductCreditUpgrade = &billing.ProductCreditUpgrade{}
	for _, tc := range []struct {
		name, product, client, wallet string
		capability                    []string
		want                          bool
	}{
		{"trusted", "superwagie", "sw", "product_v1", []string{"1"}, true},
		{"missing", "superwagie", "sw", "product_v1", nil, false},
		{"duplicate", "superwagie", "sw", "product_v1", []string{"1", "1"}, false},
		{"combined", "superwagie", "sw", "product_v1", []string{"1, 1"}, false},
		{"misses wallet", "superwagie", "sw", "", []string{"1"}, false},
		{"another product", "agentwiki", "aw", "product_v1", []string{"1"}, false},
		{"unregistered", "superwagie", "", "product_v1", []string{"1"}, false},
		{"account client", "", "", "product_v1", []string{"1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/accounts/example/quotes", nil)
			ctx := context.WithValue(r.Context(), productIdentityKey{}, tc.product)
			if tc.client != "" {
				ctx = context.WithValue(ctx, productPolicyKey{}, products.Client{ClientID: tc.client, ProductID: tc.product})
			}
			r = r.WithContext(ctx)
			r.Header.Set(productWalletHeader, tc.wallet)
			for _, value := range tc.capability {
				r.Header.Add(legacyNetUpgradeHeader, value)
			}
			if got := legacyNetUpgradeRequested(r); got != tc.want {
				t.Fatalf("capability admitted = %v, want %v", got, tc.want)
			}
			if err := requireLegacyNetUpgrade(r, legacyNet); (tc.want && err != nil) || (!tc.want && !errors.Is(err, accounts.ErrForbidden)) {
				t.Fatalf("checkout mutation guard: %v", err)
			}
			if err := requireLegacyNetUpgrade(r, ordinary); err != nil {
				t.Fatalf("ordinary upgrade requires a new capability: %v", err)
			}
		})
	}
}
