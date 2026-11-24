// Package appintegrations manages browser identity bridges alongside deployment configuration.
package appintegrations

import (
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"unicode"

	"unihub/internal/accounts"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

type Input struct {
	ProductID      string `json:"product_id"`
	InstanceID     string `json:"instance_id"`
	Name           string `json:"name"`
	Origin         string `json:"origin"`
	EntryURL       string `json:"entry_url"`
	CallbackURL    string `json:"callback_url"`
	NativeLoginURL string `json:"native_login_url"`
	Version        int64  `json:"version"`
}

type Application struct {
	Input
	ClientID             string `json:"client_id"`
	Source               string `json:"source"`
	State                string `json:"state"`
	CredentialConfigured bool   `json:"credential_configured"`
	BrowserSSO           bool   `json:"browser_sso"`
	RuntimeConfigured    bool   `json:"runtime_configured"`
	InboundVerified      bool   `json:"inbound_verified"`
	OutboundVerified     bool   `json:"outbound_verified"`
	HubOrigin            string `json:"hub_origin"`
}

func normalize(input Input, hub string, allowLoopback bool) (Application, error) {
	input.InstanceID = productusers.Instance(input.InstanceID)
	input.Name = strings.TrimSpace(input.Name)
	app := Application{Input: input, ClientID: "bridge." + input.ProductID + "." + input.InstanceID,
		Source: "managed", State: "draft", BrowserSSO: true, HubOrigin: hub}
	origin, ok := parsedURL(app.Origin, allowLoopback)
	if !ok || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery ||
		strings.EqualFold(origin.Host, hubHost(hub)) || strings.IndexFunc(app.Name, unicode.IsControl) >= 0 {
		return Application{}, accounts.ErrInvalid
	}
	for _, value := range []string{app.EntryURL, app.CallbackURL, app.NativeLoginURL} {
		u, ok := parsedURL(value, allowLoopback)
		if !ok || u.Scheme+"://"+u.Host != app.Origin {
			return Application{}, accounts.ErrInvalid
		}
	}
	callback, _ := url.Parse(app.CallbackURL)
	if callback.Path == "" || callback.Path == "/" || callback.RawQuery != "" || callback.ForceQuery {
		return Application{}, accounts.ErrInvalid
	}
	manifest, _ := json.Marshal([]productusers.Manifest{app.Manifest()})
	if _, err := productusers.Parse(manifest); err != nil {
		return Application{}, accounts.ErrInvalid
	}
	client, _ := json.Marshal([]products.Client{app.Client()})
	if _, err := products.Parse(client); err != nil {
		return Application{}, accounts.ErrInvalid
	}
	return app, nil
}

func parsedURL(raw string, allowLoopback bool) (*url.URL, bool) {
	if len(raw) == 0 || len(raw) > 1024 || strings.ContainsAny(raw, "\\#") ||
		strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return nil, false
	}
	host := u.Hostname()
	loopback := strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
	if loopback {
		return u, allowLoopback && (u.Scheme == "http" || u.Scheme == "https")
	}
	return u, u.Scheme == "https" && strings.Contains(host, ".") && net.ParseIP(host) == nil
}

func hubHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func (a Application) Manifest() productusers.Manifest {
	return productusers.Manifest{ProductID: a.ProductID, InstanceID: a.InstanceID, Name: a.Name,
		EntryURL: a.EntryURL, Version: 1, DefaultRoles: []string{},
		Roles: []productusers.Role{{ID: "member", Name: "成员", Permissions: []string{"product:use"}}}}
}

func (a Application) Client() products.Client {
	return products.Client{ClientID: a.ClientID, ProductID: a.ProductID, InstanceID: a.InstanceID, BrowserSSO: true,
		Origins: []string{a.Origin}, RedirectURIs: []string{a.CallbackURL}, IdentityLinkRole: "member",
		NativeLoginURL: a.NativeLoginURL, NativeLoginLabel: "使用 " + a.Name + " 账号登录", NativeHandoffGeneration: "1"}
}

func (a Application) allows(state string) bool {
	switch state {
	case "pilot":
		return (a.State == "draft" || a.State == "paused") && a.CredentialConfigured
	case "active":
		return a.State == "pilot" && a.InboundVerified && a.OutboundVerified
	case "paused":
		return a.State == "pilot" || a.State == "active"
	default:
		return false
	}
}
