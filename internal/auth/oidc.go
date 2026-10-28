// Package auth verifies identities presented to the UniHub API.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Claims contains the verified identity used by the account service.
type Claims struct {
	Issuer    string
	Subject   string
	Name      string
	Email     string
	ExpiresAt int64
	IssuedAt  int64
}

// Verifier verifies a raw bearer access token before exposing an identity.
type Verifier interface {
	Verify(context.Context, string) (Claims, error)
}

// OIDC validates Casdoor access tokens against a discovered issuer's keys.
type OIDC struct {
	verifier *oidc.IDTokenVerifier
	audience string
}

// NewOIDC discovers an issuer and configures strict RS256 bearer verification.
// HTTP is permitted only for loopback development. The service entry point must
// additionally require HTTPS when running in production.
func NewOIDC(ctx context.Context, issuer, audience string) (*OIDC, error) {
	if err := validateEndpoint(issuer); err != nil {
		return nil, fmt.Errorf("invalid OIDC issuer: %w", err)
	}
	if strings.TrimSpace(audience) == "" || strings.TrimSpace(audience) != audience {
		return nil, errors.New("OIDC audience must be a nonempty identifier without surrounding whitespace")
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("OIDC endpoint redirects are not permitted")
		},
	}
	clientCtx := oidc.ClientContext(ctx, client)
	discoveryCtx, cancel := context.WithTimeout(clientCtx, 5*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC issuer: %w", err)
	}
	var metadata struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("read OIDC metadata: %w", err)
	}
	if err := validateEndpoint(metadata.JWKSURI); err != nil {
		return nil, fmt.Errorf("invalid OIDC JWKS endpoint: %w", err)
	}
	issuerURL, _ := url.Parse(issuer)
	jwksURL, _ := url.Parse(metadata.JWKSURI)
	if issuerURL.Scheme == "https" && jwksURL.Scheme != "https" {
		return nil, errors.New("HTTPS OIDC issuer requires an HTTPS JWKS endpoint")
	}
	return &OIDC{verifier: provider.VerifierContext(clientCtx, &oidc.Config{
		ClientID:             audience,
		SupportedSigningAlgs: []string{oidc.RS256},
	}), audience: audience}, nil
}

func (v *OIDC) Verify(ctx context.Context, rawToken string) (Claims, error) {
	if err := ctx.Err(); err != nil {
		return Claims{}, err
	}
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Claims{}, fmt.Errorf("verify access token: %w", err)
	}
	var claims struct {
		Name            string   `json:"name"`
		Email           string   `json:"email"`
		TokenType       string   `json:"tokenType"`
		Scope           string   `json:"scope"`
		Expiry          *float64 `json:"exp"`
		IssuedAt        *float64 `json:"iat"`
		AuthorizedParty string   `json:"azp"`
	}
	if err := token.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("decode access token claims: %w", err)
	}
	if len(token.Audience) != 1 || token.Audience[0] != v.audience || (claims.AuthorizedParty != "" && claims.AuthorizedParty != v.audience) {
		return Claims{}, errors.New("access token must belong to exactly the configured OIDC client")
	}
	// Casdoor signs ID, access and refresh tokens with the same key. Signature,
	// issuer and audience verification alone cannot distinguish their purpose.
	if claims.TokenType != "access-token" {
		return Claims{}, errors.New("token is not a Casdoor access token")
	}
	if strings.TrimSpace(token.Subject) == "" {
		return Claims{}, errors.New("access token subject is required")
	}
	// Require a numeric, explicitly present positive exp, independently of the
	// upstream verifier's parsing and expiration check.
	if claims.Expiry == nil || *claims.Expiry <= 0 {
		return Claims{}, errors.New("access token requires a positive numeric expiry")
	}
	hasOpenID := false
	for _, scope := range strings.Fields(claims.Scope) {
		if scope == "openid" {
			hasOpenID = true
			break
		}
	}
	if !hasOpenID {
		return Claims{}, errors.New("access token requires openid scope")
	}
	var issuedAt int64
	if claims.IssuedAt != nil {
		issuedAt = int64(*claims.IssuedAt)
		if issuedAt <= 0 || issuedAt > time.Now().Add(5*time.Minute).Unix() || issuedAt >= token.Expiry.Unix() {
			return Claims{}, errors.New("access token has invalid issue time")
		}
	}
	return Claims{Issuer: token.Issuer, Subject: token.Subject, Name: claims.Name, Email: claims.Email, ExpiresAt: token.Expiry.Unix(), IssuedAt: issuedAt}, nil
}

func validateEndpoint(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return errors.New("URL is empty or contains surrounding whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("malformed URL")
	}
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return errors.New("URL requires a host and must not contain credentials, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("URL must use HTTPS, except for HTTP loopback development")
}
