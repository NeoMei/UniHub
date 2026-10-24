package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/products"
)

// Product attribution is derived from a verified distinct client audience and
// deployment policy. No request body or query can provide this identity.
type productIdentityKey struct{}
type productPolicyKey struct{}

func productRouteAllowed(pattern string) bool {
	switch pattern {
	case "GET /v1/me", "GET /v1/accounts", "GET /v1/products", "GET /v1/catalog",
		"GET /v1/accounts/{account}/subscriptions", "GET /v1/accounts/{account}/credits",
		"POST /v1/accounts/{account}/model-credentials":
		return true
	}
	return false
}

func productScopeAllowed(pattern string, c products.Client) bool {
	scope := map[string]string{
		"GET /v1/me": "accounts:read", "GET /v1/capabilities": "accounts:read",
		"GET /v1/accounts": "accounts:read", "POST /v1/accounts": "accounts:write",
		"GET /v1/accounts/{account}/members": "members:read",
		"GET /v1/invitations":                "members:read", "GET /v1/accounts/{account}/invitations": "members:read",
		"POST /v1/accounts/{account}/invitations": "members:write", "POST /v1/invitations/{invitation}/accept": "members:write",
		"PATCH /v1/accounts/{account}/members/{member}": "members:write", "DELETE /v1/accounts/{account}/members/{member}": "members:write",
		"GET /v1/products": "catalog:read", "GET /v1/catalog": "catalog:read",
		"GET /v1/accounts/{account}/subscriptions": "subscriptions:read", "GET /v1/accounts/{account}/credits": "credits:read",
		"GET /v1/accounts/{account}/resources": "subscriptions:read", "GET /v1/accounts/{account}/wallets": "wallets:read",
		"POST /v1/accounts/{account}/recharges": "recharges:write", "GET /v1/accounts/{account}/recharges/{order}": "recharges:read",
		"POST /v1/accounts/{account}/recharges/{order}/sandbox-settle": "recharges:write", "POST /v1/accounts/{account}/transfers": "transfers:write",
		"GET /v1/accounts/{account}/operations": "operations:read", "GET /v1/accounts/{account}/operations/{operation}": "operations:read",
		"GET /v1/accounts/{account}/orders": "orders:read", "POST /v1/accounts/{account}/orders": "orders:write",
		"POST /v1/accounts/{account}/quotes": "orders:write", "POST /v1/accounts/{account}/quotes/{quote}/accept": "orders:write",
		"GET /v1/accounts/{account}/settings": "settings:read", "PUT /v1/accounts/{account}/settings": "settings:write",
		"POST /v1/accounts/{account}/orders/{order}/refund": "refunds:write",
		"POST /v1/accounts/{account}/model-credentials":     "model-credentials:write", "DELETE /v1/accounts/{account}/model-credentials/{credential}": "model-credentials:write",
		"GET /v1/accounts/{account}/calls": "usage:read", "GET /v1/accounts/{account}/calls/{call}": "usage:read", "GET /v1/accounts/{account}/usage/summary": "usage:read",
	}[pattern]
	return scope != "" && (c.HasScope(scope) || (pattern == "GET /v1/me" && c.HasScope("identity:read")))
}
func productPolicy(r *http.Request) (products.Client, bool) {
	c, ok := r.Context().Value(productPolicyKey{}).(products.Client)
	return c, ok
}
func originProduct(r *http.Request) string {
	p, _ := r.Context().Value(productIdentityKey{}).(string)
	return p
}
func allowedBillingProduct(r *http.Request, product string) bool {
	if p := originProduct(r); p != "" {
		if c, ok := productPolicy(r); ok {
			return c.AllowsBillingProduct(product)
		}
		return product == p
	}
	return true
}
func requireBillingProduct(r *http.Request, product string) error {
	if !allowedBillingProduct(r, product) {
		return accounts.ErrForbidden
	}
	return nil
}
func billingProducts(r *http.Request) []string {
	c, ok := productPolicy(r)
	if ok && len(c.BillingProducts) > 0 {
		return c.BillingProducts
	}
	return []string{originProduct(r)}
}
func filterProducts[T any](r *http.Request, in []T, id func(T) string) []T {
	if originProduct(r) == "" {
		return in
	}
	out := []T{}
	for _, x := range in {
		if allowedBillingProduct(r, id(x)) {
			out = append(out, x)
		}
	}
	return out
}
