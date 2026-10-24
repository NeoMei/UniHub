package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unihub/internal/products"
)

type config struct {
	Listen, DB, LedgerURL, LedgerName, Issuer, Audience, Merchant string
	PublicURL, ClientSecret, StaticDir, ModelServiceToken         string
	OperatorAudience                                              string
	OperatorIDs                                                   []string
	ProductClients                                                map[string]string
	ProductAccess                                                 map[string]products.Client
	IdentityKey                                                   []byte
	IdentityManagementClientID, IdentityManagementSecret          string
	IdentityAuditVerified                                         bool
	IdentityTrustLoopbackProxy                                    bool
	Production, SandboxCatalog                                    bool
	Sandbox                                                       bool
	SigningKey                                                    []byte
}

func readConfig(get func(string) string) (config, error) {
	c := config{Listen: get("LISTEN_ADDR"), DB: get("DATABASE_URL"), LedgerURL: get("LEDGER_URL"), LedgerName: get("LEDGER_NAME"), Issuer: get("OIDC_ISSUER"), Audience: get("OIDC_AUDIENCE"), Merchant: get("PAYMENT_MERCHANT"), SigningKey: []byte(get("PAYMENT_SIGNING_KEY"))}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8088"
	}
	if c.LedgerName == "" {
		c.LedgerName = "unihub"
	}
	if c.DB == "" || c.LedgerURL == "" || c.Issuer == "" || c.Audience == "" {
		return c, fmt.Errorf("DATABASE_URL, LEDGER_URL, OIDC_ISSUER and OIDC_AUDIENCE are required")
	}
	env := get("APP_ENV")
	if env != "development" && env != "production" {
		return c, fmt.Errorf("APP_ENV must be development or production")
	}
	c.Production = env == "production"
	c.PublicURL = strings.TrimSuffix(get("PUBLIC_URL"), "/")
	c.ClientSecret = get("OIDC_CLIENT_SECRET")
	c.StaticDir = get("CONSOLE_STATIC_DIR")
	if c.StaticDir == "" {
		c.StaticDir = "web/dist"
	}
	c.ModelServiceToken = get("MODEL_SERVICE_TOKEN")
	c.OperatorAudience = get("OPS_OIDC_AUDIENCE")
	if c.OperatorAudience != "" && c.OperatorAudience == c.Audience {
		return c, fmt.Errorf("OPS_OIDC_AUDIENCE must be a separate operator client")
	}
	c.ProductClients = map[string]string{}
	usedAudiences := map[string]bool{c.Audience: true, c.OperatorAudience: true}
	if raw := get("OIDC_PRODUCT_CLIENTS"); raw != "" {
		for _, pair := range strings.Split(raw, ",") {
			product, audience, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(product) || audience == "" || strings.TrimSpace(audience) != audience || strings.Contains(audience, "=") || usedAudiences[audience] || c.ProductClients[product] != "" {
				return c, fmt.Errorf("OIDC_PRODUCT_CLIENTS requires distinct product=audience pairs, separate from console and operators")
			}
			c.ProductClients[product] = audience
			usedAudiences[audience] = true
		}
	}
	c.ProductAccess = map[string]products.Client{}
	if file := get("PRODUCT_CLIENTS_FILE"); file != "" {
		f, err := os.Open(file)
		if err != nil {
			return c, fmt.Errorf("product registry is unavailable")
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			f.Close()
			return c, fmt.Errorf("product registry must be a bounded regular file")
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		f.Close()
		if readErr != nil || len(raw) > 1<<20 {
			return c, fmt.Errorf("product registry is unavailable")
		}
		c.ProductAccess, err = products.Parse(raw)
		if err != nil {
			return c, fmt.Errorf("invalid product client policy")
		}
		for id := range c.ProductAccess {
			if usedAudiences[id] {
				return c, fmt.Errorf("product client audiences must be separate from legacy, console and operators")
			}
			usedAudiences[id] = true
		}
	}
	c.IdentityKey = []byte(get("HEADLESS_IDENTITY_KEY"))
	c.IdentityManagementClientID = get("HEADLESS_MANAGEMENT_CLIENT_ID")
	c.IdentityManagementSecret = get("HEADLESS_MANAGEMENT_CLIENT_SECRET")
	audit := get("HEADLESS_AUDIT_REDACTION_VERIFIED")
	if audit != "" && audit != "false" && audit != "true" {
		return c, fmt.Errorf("HEADLESS_AUDIT_REDACTION_VERIFIED must be true or false")
	}
	c.IdentityAuditVerified = audit == "true"
	proxy := get("HEADLESS_TRUST_LOOPBACK_PROXY")
	if proxy != "" && proxy != "true" && proxy != "false" {
		return c, fmt.Errorf("HEADLESS_TRUST_LOOPBACK_PROXY must be true or false")
	}
	c.IdentityTrustLoopbackProxy = proxy == "true"
	enabledIdentity := false
	for _, client := range c.ProductAccess {
		enabledIdentity = enabledIdentity || client.PasswordLogin || client.Registration
	}
	if len(c.IdentityKey) > 0 && len(c.IdentityKey) < 32 {
		return c, fmt.Errorf("HEADLESS_IDENTITY_KEY requires at least 32 bytes")
	}
	if enabledIdentity && (len(c.IdentityKey) < 32 || c.IdentityManagementClientID == "" || c.IdentityManagementSecret == "" || !c.IdentityAuditVerified) {
		return c, fmt.Errorf("headless identity requires private management configuration and verified upstream audit privacy")
	}
	if c.ModelServiceToken != "" && len(c.ModelServiceToken) < 32 {
		return c, fmt.Errorf("MODEL_SERVICE_TOKEN requires at least 32 bytes")
	}
	for _, id := range strings.Split(get("BILLING_OPERATOR_IDS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			c.OperatorIDs = append(c.OperatorIDs, id)
		}
	}
	if v := get("SANDBOX_CATALOG"); v != "" && v != "false" && v != "true" {
		return c, fmt.Errorf("SANDBOX_CATALOG must be true or false")
	}
	c.SandboxCatalog = get("SANDBOX_CATALOG") == "true"
	if c.SandboxCatalog && c.Production {
		return c, fmt.Errorf("sandbox catalog requires development")
	}
	if c.PublicURL != "" {
		p, err := url.Parse(c.PublicURL)
		if err != nil || p.Hostname() == "" || p.User != nil || p.Path != "" || p.RawQuery != "" || p.ForceQuery || p.Fragment != "" || strings.Contains(c.PublicURL, "#") {
			return c, fmt.Errorf("PUBLIC_URL must be an origin without credentials, path, query or fragment")
		}
		ip := net.ParseIP(p.Hostname())
		local := p.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if p.Scheme != "https" && (p.Scheme != "http" || c.Production || !local) {
			return c, fmt.Errorf("PUBLIC_URL requires HTTPS except loopback development")
		}
		if c.ClientSecret == "" {
			return c, fmt.Errorf("browser console requires OIDC_CLIENT_SECRET")
		}
	}
	mode := get("PAYMENT_MODE")
	if mode == "" {
		mode = "disabled"
	}
	if mode != "disabled" && mode != "sandbox" {
		return c, fmt.Errorf("PAYMENT_MODE must be disabled or sandbox")
	}
	c.Sandbox = mode == "sandbox"
	if c.Sandbox && (env != "development" || len(c.SigningKey) < 32 || c.Merchant == "") {
		return c, fmt.Errorf("sandbox requires development, merchant and signing key of at least 32 bytes")
	}
	if c.Sandbox {
		host, _, err := net.SplitHostPort(c.Listen)
		ip := net.ParseIP(host)
		if err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return c, fmt.Errorf("sandbox listener must be loopback")
		}
	}
	u, e := url.Parse(c.Issuer)
	if e != nil || u.Host == "" {
		return c, fmt.Errorf("invalid OIDC_ISSUER")
	}
	if env == "production" && u.Scheme != "https" {
		return c, fmt.Errorf("production OIDC_ISSUER requires HTTPS")
	}
	if strings.ContainsAny(c.LedgerName, "/?#") || len(c.LedgerName) > 100 {
		return c, fmt.Errorf("invalid LEDGER_NAME")
	}
	return c, nil
}
