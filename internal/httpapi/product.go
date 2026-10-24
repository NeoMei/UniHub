package httpapi

// A product's public OIDC client is deliberately narrower than the UniHub
// console. Product attribution is server configuration, never a request claim.
type productIdentityKey struct{}

func productRouteAllowed(pattern string) bool {
	switch pattern {
	case "GET /v1/me", "GET /v1/accounts", "GET /v1/products", "GET /v1/catalog",
		"GET /v1/accounts/{account}/subscriptions", "GET /v1/accounts/{account}/credits",
		"POST /v1/accounts/{account}/model-credentials":
		return true
	}
	return false
}
