package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

type config struct {
	Listen, DB, LedgerURL, LedgerName, Issuer, Audience, Merchant string
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
