package auth

import "context"

type browserClaimsKey struct{}

// WithBrowserClaims attaches identity verified by the browser session boundary.
// The API must only consume this identity in the absence of Authorization.
func WithBrowserClaims(ctx context.Context, claims Claims) context.Context {
	return context.WithValue(ctx, browserClaimsKey{}, claims)
}

// BrowserClaims returns a trusted browser identity, never client supplied headers.
func BrowserClaims(ctx context.Context) (Claims, bool) {
	claims, ok := ctx.Value(browserClaimsKey{}).(Claims)
	return claims, ok
}
