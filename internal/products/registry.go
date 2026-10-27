// Package products holds public product client policy. It never stores passwords,
// client secrets, payment credentials or trusted model service credentials.
package products

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type Client struct {
	InstanceID      string   `json:"instance_id,omitempty"`
	ProductID       string   `json:"product_id"`
	ClientID        string   `json:"client_id"`
	Application     string   `json:"application,omitempty"`
	Organization    string   `json:"organization,omitempty"`
	RedirectURIs    []string `json:"redirect_uris,omitempty"`
	Origins         []string `json:"origins,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`
	BillingProducts []string `json:"billing_products,omitempty"`
	PasswordLogin   bool     `json:"password_login,omitempty"`
	Registration    bool     `json:"registration,omitempty"`
}

var productID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var supportedScopes = map[string]bool{
	"access:read": true, "identity:read": true, "accounts:read": true, "accounts:write": true,
	"profile:write": true,
	"members:read":  true, "members:write": true, "catalog:read": true,
	"subscriptions:read": true, "credits:read": true, "wallets:read": true,
	"recharges:read": true, "recharges:write": true, "transfers:write": true,
	"operations:read": true, "orders:read": true, "orders:write": true,
	"settings:read": true, "settings:write": true, "refunds:write": true,
	"usage:read": true, "usage:all-products": true, "model-credentials:write": true,
}

func (c Client) HasScope(scope string) bool      { return slices.Contains(c.Scopes, scope) }
func (c Client) AllowsOrigin(origin string) bool { return slices.Contains(c.Origins, origin) }
func (c Client) AllowsBillingProduct(product string) bool {
	if product == "" {
		return false
	}
	if len(c.BillingProducts) == 0 {
		return product == c.ProductID
	}
	return slices.Contains(c.BillingProducts, product)
}

// Parse reads a single strict JSON array, returning clients indexed by their
// distinct OIDC audience/client ID. An omitted scope list grants no capability.
func Parse(raw []byte) (map[string]Client, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var clients []Client
	if err := d.Decode(&clients); err != nil {
		return nil, errors.New("invalid product client registry")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("invalid product client registry")
	}
	out := map[string]Client{}
	for _, c := range clients {
		if !productID.MatchString(c.ProductID) || strings.TrimSpace(c.ClientID) != c.ClientID || c.ClientID == "" || len(c.ClientID) > 256 {
			return nil, errors.New("invalid product or client ID")
		}
		if _, ok := out[c.ClientID]; ok {
			return nil, errors.New("duplicate product client audience")
		}
		if c.InstanceID == "" {
			c.InstanceID = "default"
		}
		if !productID.MatchString(c.InstanceID) {
			return nil, errors.New("invalid instance ID")
		}
		for _, s := range c.Scopes {
			if !supportedScopes[s] || (s == "usage:all-products" && c.ProductID != "llmrouter") {
				return nil, errors.New("unknown product scope")
			}
		}
		for _, p := range c.BillingProducts {
			if !productID.MatchString(p) {
				return nil, errors.New("invalid billing product")
			}
		}
		for _, v := range c.Origins {
			if !validURL(v, true) {
				return nil, errors.New("invalid product origin")
			}
		}
		for _, v := range c.RedirectURIs {
			if !validURL(v, false) {
				return nil, errors.New("invalid product redirect URI")
			}
		}
		out[c.ClientID] = c
	}
	return out, nil
}
func validURL(value string, origin bool) bool {
	if strings.ContainsAny(value, "*\\\r\n\t ") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	decodedQuery, queryErr := url.QueryUnescape(u.RawQuery)
	if queryErr != nil || strings.ContainsAny(u.Path+decodedQuery, "*\\\r\n\t ") {
		return false
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return false
		}
	}
	if origin && (u.Path != "" || u.RawQuery != "" || u.ForceQuery) {
		return false
	}
	return true
}
