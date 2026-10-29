package identityapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/platformadmin"
)

// This adapter is independent from the development-only profile API. The pin
// includes UniHub's audited session/token expiration patch, not an arbitrary
// upstream build with a matching marketing version.
const AdminPinnedVersion = "1b91d9d319a127e9b1af88fa7a20bb35c052b9d8"

type RecoveryDelivery interface {
	DeliverIdentityRecovery(context.Context, string, string, string) error
}
type ActivationDelivery interface {
	DeliverIdentityActivation(context.Context, string, string, string) error
}
type AdminProviderOptions struct {
	PinnedVersion      string
	Organization       string
	PublicOrigin       string
	DeploymentVerified bool
	// Both flags require deployment-equivalent acceptance, not source inspection.
	PasswordResetVerified     bool
	SessionRevocationVerified bool
	Delivery                  RecoveryDelivery
}
type adminProvider struct {
	server  *Server
	options AdminProviderOptions
}

func (s *Server) AdministrationProvider(ctx context.Context, o AdminProviderOptions) (platformadmin.IdentityProvider, error) {
	if s.client == nil || !s.opts.AuditRedactionVerified || !o.DeploymentVerified || o.PinnedVersion != AdminPinnedVersion || o.Organization != "unihub" {
		return nil, platformadmin.ErrIdentityUnavailable
	}
	if _, e := s.organization(ctx); e != nil {
		return nil, platformadmin.ErrIdentityUnavailable
	}
	return &adminProvider{server: s, options: o}, nil
}
func (p *adminProvider) Capabilities(context.Context) platformadmin.IdentityCapabilities {
	recovery := p.options.PasswordResetVerified && p.options.SessionRevocationVerified && p.options.Delivery != nil && safeURL(p.options.PublicOrigin, p.server.opts.Production)
	_, activationDelivery := p.options.Delivery.(ActivationDelivery)
	c := platformadmin.IdentityCapabilities{Directory: true, DisplayName: true, CreatePending: true, Activation: recovery && activationDelivery, Recovery: recovery, RevokeSessions: p.options.SessionRevocationVerified, ProductionVerified: p.options.DeploymentVerified, Issuer: p.server.opts.Issuer, Organization: p.options.Organization, ProviderVersion: AdminPinnedVersion}
	if !recovery {
		c.Reason = "recovery requires deployment-verified password/session adapter and verified-channel delivery"
	}
	return c
}

type adminProviderUser struct {
	Owner         string            `json:"owner"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	DisplayName   string            `json:"displayName"`
	Email         string            `json:"email"`
	EmailVerified bool              `json:"emailVerified"`
	IsAdmin       bool              `json:"isAdmin"`
	IsForbidden   bool              `json:"isForbidden"`
	IsDeleted     bool              `json:"isDeleted"`
	CreatedTime   string            `json:"createdTime"`
	Properties    map[string]string `json:"properties"`
}

func (p *adminProvider) publicUser(u adminProviderUser) platformadmin.ProviderIdentity {
	return platformadmin.ProviderIdentity{Issuer: p.server.opts.Issuer, Subject: u.ID, Username: u.Name, DisplayName: u.DisplayName, Email: u.Email, EmailVerified: u.EmailVerified, Disabled: u.IsForbidden || u.IsDeleted, Pending: u.Properties["unihubActivation"] == "pending", Manageable: u.Owner == p.options.Organization && u.Type == "normal-user" && !u.IsAdmin && !u.IsDeleted && u.ID != "" && u.Name != "", CreatedAt: u.CreatedTime}
}
func (p *adminProvider) read(ctx context.Context, issuer, subject string) (adminProviderUser, error) {
	if issuer != p.server.opts.Issuer || subject == "" {
		return adminProviderUser{}, accounts.ErrForbidden
	}
	var env envelope
	e := p.server.providerJSON(ctx, "GET", "/api/get-user?owner="+url.QueryEscape(p.options.Organization)+"&userId="+url.QueryEscape(subject), nil, true, &env)
	if e != nil || env.Status != "ok" {
		return adminProviderUser{}, platformadmin.ErrIdentityUnavailable
	}
	var u adminProviderUser
	if json.Unmarshal(env.Data, &u) != nil || u.ID != subject || u.Owner != p.options.Organization || u.Name == "" {
		return u, platformadmin.ErrIdentityUnavailable
	}
	return u, nil
}
func (p *adminProvider) ReadIdentity(ctx context.Context, issuer, subject string) (platformadmin.ProviderIdentity, error) {
	u, e := p.read(ctx, issuer, subject)
	if e != nil {
		return platformadmin.ProviderIdentity{}, e
	}
	return p.publicUser(u), nil
}

type providerDirectoryCursor struct {
	Page    int    `json:"page"`
	Binding string `json:"binding"`
}

func (p *adminProvider) Directory(ctx context.Context, o platformadmin.ListOptions) (platformadmin.Page[platformadmin.ProviderIdentity], error) {
	out := platformadmin.Page[platformadmin.ProviderIdentity]{Items: []platformadmin.ProviderIdentity{}, AsOf: time.Now().UTC()}
	if o.Status != "" || o.Role != "" {
		return out, accounts.ErrInvalid
	}
	limit := o.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	binding := p.options.Organization + ":" + o.Search + ":" + strconv.Itoa(limit)
	page := 1
	if o.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(o.Cursor)
		var c providerDirectoryCursor
		if e != nil || json.Unmarshal(raw, &c) != nil || c.Binding != binding || c.Page < 1 {
			return out, accounts.ErrInvalid
		}
		page = c.Page
	}
	q := url.Values{"owner": {p.options.Organization}, "p": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(limit)}, "sortField": {"name"}, "sortOrder": {"ascend"}}
	if o.Search != "" {
		q.Set("field", "name")
		q.Set("value", o.Search)
	}
	var env envelope
	if p.server.providerJSON(ctx, "GET", "/api/get-users?"+q.Encode(), nil, true, &env) != nil || env.Status != "ok" {
		return out, platformadmin.ErrIdentityUnavailable
	}
	var users []adminProviderUser
	var pages int
	if json.Unmarshal(env.Data, &users) != nil || json.Unmarshal(env.Data2, &pages) != nil || pages < 0 || len(users) > limit {
		return out, platformadmin.ErrIdentityUnavailable
	}
	if len(users) == 0 {
		if page > 1 {
			return out, accounts.ErrConflict
		}
		return out, nil
	}
	lastCount := len(users)
	if pages > page {
		last := q
		last.Set("p", strconv.Itoa(pages))
		var lastEnv envelope
		var lastUsers []adminProviderUser
		if p.server.providerJSON(ctx, "GET", "/api/get-users?"+last.Encode(), nil, true, &lastEnv) != nil || lastEnv.Status != "ok" || json.Unmarshal(lastEnv.Data, &lastUsers) != nil || len(lastUsers) > limit {
			return out, platformadmin.ErrIdentityUnavailable
		}
		lastCount = len(lastUsers)
	}
	if pages < 1 {
		pages = 1
	}
	out.Total = int64((pages-1)*limit + lastCount)
	for _, u := range users {
		if u.Owner != p.options.Organization || u.ID == "" {
			return out, platformadmin.ErrIdentityUnavailable
		}
		out.Items = append(out.Items, p.publicUser(u))
	}
	if page < pages {
		raw, _ := json.Marshal(providerDirectoryCursor{Page: page + 1, Binding: binding})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}
func (p *adminProvider) CreatePendingIdentity(ctx context.Context, operation string, in platformadmin.CreateIdentityInput) (platformadmin.ProviderIdentity, error) {
	// Omit password entirely and forbid login until a distinct verified activation.
	body := map[string]any{"owner": p.options.Organization, "name": in.Username, "type": "normal-user", "displayName": in.DisplayName, "email": in.Email, "emailVerified": false, "isAdmin": false, "isForbidden": true, "properties": map[string]string{"unihubActivation": "pending", "unihubOperation": operation}}
	var env envelope
	if p.server.providerJSON(ctx, "POST", "/api/add-user", body, true, &env) != nil || env.Status != "ok" {
		return platformadmin.ProviderIdentity{}, platformadmin.ErrIdentityUnknown
	}
	var read envelope
	if p.server.providerJSON(ctx, "GET", "/api/get-user?id="+url.QueryEscape(p.options.Organization+"/"+in.Username), nil, true, &read) != nil || read.Status != "ok" {
		return platformadmin.ProviderIdentity{}, platformadmin.ErrIdentityUnknown
	}
	var u adminProviderUser
	if json.Unmarshal(read.Data, &u) != nil || u.Name != in.Username || u.Properties["unihubOperation"] != operation || u.EmailVerified || u.IsAdmin || !u.IsForbidden {
		return platformadmin.ProviderIdentity{}, platformadmin.ErrIdentityUnknown
	}
	return p.publicUser(u), nil
}
func (p *adminProvider) SetIdentityDisplayName(ctx context.Context, issuer, subject, name string) error {
	u, e := p.read(ctx, issuer, subject)
	if e != nil {
		return e
	}
	if !p.publicUser(u).Manageable {
		return accounts.ErrForbidden
	}
	path := "/api/update-user?id=" + url.QueryEscape(p.options.Organization+"/"+u.Name) + "&columns=displayName"
	if name == "" {
		path += "&allowEmpty=true"
	}
	var env envelope
	if p.server.providerJSON(ctx, "POST", path, map[string]string{"displayName": name}, true, &env) != nil || env.Status != "ok" {
		return platformadmin.ErrIdentityUnknown
	}
	return nil
}
func (p *adminProvider) DeliverRecovery(ctx context.Context, id platformadmin.ProviderIdentity, secret, request string) error {
	if !p.Capabilities(ctx).Recovery || !id.EmailVerified || id.Email == "" {
		return platformadmin.ErrRecoveryUnsupported
	}
	read, e := p.ReadIdentity(ctx, id.Issuer, id.Subject)
	if e != nil || !read.EmailVerified || read.Email != id.Email {
		return platformadmin.ErrRecoveryUnsupported
	}
	return p.options.Delivery.DeliverIdentityRecovery(ctx, id.Email, platformadmin.RecoveryURL(p.options.PublicOrigin, secret), request)
}
func (p *adminProvider) SetRecoveryPassword(ctx context.Context, id platformadmin.ProviderIdentity, password, operation string) error {
	if !p.Capabilities(ctx).Recovery {
		return platformadmin.ErrIdentityUnavailable
	}
	u, e := p.read(ctx, id.Issuer, id.Subject)
	if e != nil || !p.publicUser(u).Manageable {
		return platformadmin.ErrIdentityUnavailable
	}
	org, e := p.server.organization(ctx)
	if e != nil || !validPassword(password, u.Name, u.Email, org.PasswordOptions) {
		return ErrInvalidPassword
	}
	var env envelope
	form := url.Values{"userOwner": {p.options.Organization}, "userName": {u.Name}, "oldPassword": {""}, "newPassword": {password}}
	if p.server.providerForm(ctx, "/api/set-password", form, &env) != nil || env.Status != "ok" {
		return platformadmin.ErrIdentityUnknown
	}
	return nil
}

type adminProviderSession struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Application string `json:"application"`
}
type adminProviderToken struct {
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Organization string `json:"organization"`
	User         string `json:"user"`
}

func (p *adminProvider) RevokeIdentitySessions(ctx context.Context, id platformadmin.ProviderIdentity, operation string) error {
	if !p.options.SessionRevocationVerified {
		return platformadmin.ErrIdentityUnavailable
	}
	u, e := p.read(ctx, id.Issuer, id.Subject)
	if e != nil || !p.publicUser(u).Manageable {
		return platformadmin.ErrIdentityUnavailable
	}
	// Delete every session row for the exact organization/user. Pinned provider
	// DeleteSession destroys Beego SSO and expires tokens minted under its IDs.
	for attempts := 0; attempts < 20; attempts++ {
		q := url.Values{"owner": {p.options.Organization}, "field": {"name"}, "value": {u.Name}, "p": {"1"}, "pageSize": {"100"}}
		var env envelope
		var sessions []adminProviderSession
		if p.server.providerJSON(ctx, "GET", "/api/get-sessions?"+q.Encode(), nil, true, &env) != nil || env.Status != "ok" || json.Unmarshal(env.Data, &sessions) != nil {
			return platformadmin.ErrIdentityUnknown
		}
		if len(sessions) == 0 {
			break
		}
		deleted := 0
		for _, session := range sessions {
			if session.Owner != p.options.Organization || session.Name != u.Name {
				continue
			}
			var result envelope
			if p.server.providerJSON(ctx, "POST", "/api/delete-session", session, true, &result) != nil || result.Status != "ok" {
				return platformadmin.ErrIdentityUnknown
			}
			deleted++
		}
		if deleted == 0 || attempts == 19 {
			return platformadmin.ErrIdentityUnknown
		}
	}
	// Tokens created without a browser session (including refresh/code grants)
	// also require removal. Query by exact user and validate organization again.
	for attempts := 0; attempts < 20; attempts++ {
		q := url.Values{"owner": {"admin"}, "organization": {p.options.Organization}, "field": {"user"}, "value": {u.Name}, "p": {"1"}, "pageSize": {"100"}}
		var env envelope
		var tokens []adminProviderToken
		if p.server.providerJSON(ctx, "GET", "/api/get-tokens?"+q.Encode(), nil, true, &env) != nil || env.Status != "ok" || json.Unmarshal(env.Data, &tokens) != nil {
			return platformadmin.ErrIdentityUnknown
		}
		if len(tokens) == 0 {
			return nil
		}
		deleted := 0
		for _, token := range tokens {
			if token.Organization != p.options.Organization || token.User != u.Name {
				continue
			}
			var result envelope
			if p.server.providerJSON(ctx, "POST", "/api/delete-token", token, true, &result) != nil || result.Status != "ok" {
				return platformadmin.ErrIdentityUnknown
			}
			deleted++
		}
		if deleted == 0 {
			return platformadmin.ErrIdentityUnknown
		}
	}
	return platformadmin.ErrIdentityUnknown
}

var _ platformadmin.IdentityProvider = (*adminProvider)(nil)

func (p *adminProvider) DeliverActivation(ctx context.Context, id platformadmin.ProviderIdentity, secret, request string) error {
	if !p.Capabilities(ctx).Activation || !id.Pending || !id.Disabled || id.Email == "" || id.EmailVerified {
		return platformadmin.ErrRecoveryUnsupported
	}
	read, e := p.ReadIdentity(ctx, id.Issuer, id.Subject)
	if e != nil || !read.Pending || !read.Disabled || read.Email != id.Email || read.EmailVerified {
		return platformadmin.ErrRecoveryUnsupported
	}
	delivery, ok := p.options.Delivery.(ActivationDelivery)
	if !ok {
		return platformadmin.ErrRecoveryUnsupported
	}
	return delivery.DeliverIdentityActivation(ctx, id.Email, platformadmin.RecoveryURL(p.options.PublicOrigin, secret), request)
}
func (p *adminProvider) ActivateIdentity(ctx context.Context, id platformadmin.ProviderIdentity, operation string) error {
	if !p.Capabilities(ctx).Activation {
		return platformadmin.ErrIdentityUnavailable
	}
	u, e := p.read(ctx, id.Issuer, id.Subject)
	if e != nil || !p.publicUser(u).Manageable || u.Properties["unihubActivation"] != "pending" || u.Email != id.Email {
		return platformadmin.ErrIdentityUnknown
	}
	u.Properties["unihubActivation"] = "verified"
	var env envelope
	body := map[string]any{"emailVerified": true, "isForbidden": false, "properties": u.Properties}
	if p.server.providerJSON(ctx, "POST", "/api/update-user?id="+url.QueryEscape(p.options.Organization+"/"+u.Name)+"&columns=emailVerified,isForbidden,properties", body, true, &env) != nil || env.Status != "ok" {
		return platformadmin.ErrIdentityUnknown
	}
	return nil
}
