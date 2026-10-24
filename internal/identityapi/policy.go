package identityapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unihub/internal/products"
)

type organizationPolicy struct {
	Name                   string   `json:"name"`
	PasswordType           string   `json:"passwordType"`
	PasswordObfuscatorType string   `json:"passwordObfuscatorType"`
	PasswordOptions        []string `json:"passwordOptions"`
	MasterPassword         string   `json:"masterPassword"`
	MasterVerificationCode string   `json:"masterVerificationCode"`
	DefaultPassword        string   `json:"defaultPassword"`
}

func (s *Server) organization(ctx context.Context) (organizationPolicy, error) {
	var env envelope
	var org organizationPolicy
	if s.providerJSON(ctx, "GET", "/api/get-organization?id=admin%2Funihub", nil, true, &env) != nil || env.Status != "ok" || json.Unmarshal(env.Data, &org) != nil || org.Name != "unihub" || org.PasswordType != "bcrypt" || org.PasswordObfuscatorType != "" && org.PasswordObfuscatorType != "None" || org.MasterPassword != "" || org.MasterVerificationCode != "" || org.DefaultPassword != "" {
		return org, errors.New("unsupported identity organization policy")
	}
	if !hasPolicyFields(env.Data, "name", "passwordType", "passwordObfuscatorType", "passwordOptions", "masterPassword", "masterVerificationCode", "defaultPassword") {
		return org, errors.New("incomplete organization policy")
	}
	for _, v := range org.PasswordOptions {
		switch v {
		case "AtLeast6", "AtLeast8", "Aa123", "SpecialChar", "NoRepeat":
		default:
			return org, errors.New("unsupported identity password policy")
		}
	}
	return org, nil
}

type appPolicy struct {
	Name              string `json:"name"`
	Organization      string `json:"organization"`
	ClientID          string `json:"clientId"`
	Shared            *bool  `json:"isShared"`
	EnableSignUp      *bool  `json:"enableSignUp"`
	DisableSelfSignup *bool  `json:"disableSelfSignup"`
	DefaultGroup      string `json:"defaultGroup"`
	DefaultTag        string `json:"defaultTag"`
	SignupItems       []struct {
		Name     string `json:"name"`
		Visible  bool   `json:"visible"`
		Required bool   `json:"required"`
		Rule     string `json:"rule"`
		Regex    string `json:"regex"`
	} `json:"signupItems"`
	SigninMethods []struct {
		Name string `json:"name"`
		Rule string `json:"rule"`
	} `json:"signinMethods"`
	Providers []struct {
		Rule     string `json:"rule"`
		Provider *struct {
			Category string `json:"category"`
		} `json:"provider"`
	} `json:"providers"`
}

func (s *Server) applicationPolicy(ctx context.Context, c products.Client, registration bool) (string, error) {
	// This public, read-only endpoint returns signup/CAPTCHA policy. The CAS branch
	// reads an existing static app and requires no nonce, password or management
	// Authorization. It is never used to authenticate the user.
	var env envelope
	var app appPolicy
	path := "/api/get-app-login?type=cas&id=" + url.QueryEscape("admin/"+c.Application)
	if s.providerJSON(ctx, "GET", path, nil, false, &env) != nil || env.Status != "ok" || json.Unmarshal(env.Data, &app) != nil || app.Name != c.Application || app.ClientID != c.ClientID || app.Organization != "unihub" || app.Shared == nil || *app.Shared {
		return "", errors.New("unsupported identity application policy")
	}
	if !hasPolicyFields(env.Data, "name", "organization", "clientId", "isShared", "enableSignUp", "disableSelfSignup", "signupItems", "signinMethods", "providers", "defaultGroup", "defaultTag") {
		return "", errors.New("incomplete application policy")
	}
	password := false
	for _, method := range app.SigninMethods {
		if method.Name == "Password" && method.Rule != "Hide password" && method.Rule != "Hide-Password" {
			password = true
		}
	}
	if !password {
		return "provider_policy", nil
	}
	for _, item := range app.Providers {
		if item.Provider == nil {
			return "", errors.New("unknown provider policy")
		}
		if item.Provider.Category == "Captcha" {
			return "captcha", nil
		}
	}
	if !registration {
		return "", nil
	}
	if app.EnableSignUp == nil || !*app.EnableSignUp || app.DisableSelfSignup == nil || *app.DisableSelfSignup {
		return "registration_policy", nil
	}
	if len(app.SignupItems) == 0 || app.DefaultGroup != "" || app.DefaultTag != "" {
		return "registration_policy", nil
	}
	username, passwordField := false, false
	for _, item := range app.SignupItems {
		if !item.Visible {
			continue
		}
		if item.Regex != "" {
			return "registration_policy", nil
		}
		switch item.Name {
		case "Username":
			username = true
		case "Password":
			passwordField = true
		case "Display name":
			if item.Rule != "" && item.Rule != "All" {
				return "registration_policy", nil
			}
		case "Email":
			if item.Required || item.Rule != "No verification" {
				return "email_verification", nil
			}
		default:
			if item.Required || strings.Contains(strings.ToLower(item.Name), "invitation") {
				return "registration_policy", nil
			}
		}
	}
	if !username || !passwordField {
		return "registration_policy", nil
	}
	return "", nil
}

// Resolve the private credential's own public application policy before using
// its Basic credentials. Reading unihub alone cannot distinguish an org admin
// from a dangerous built-in/global management application.
func (s *Server) managementPolicy(ctx context.Context) error {
	var env envelope
	q := url.Values{"type": {"code"}, "responseType": {"code"}, "clientId": {s.opts.ManagementClientID}}
	if s.providerJSON(ctx, "GET", "/api/get-app-login?"+q.Encode(), nil, false, &env) != nil {
		return errors.New("management application unavailable")
	}
	var app struct {
		Organization string `json:"organization"`
		ClientID     string `json:"clientId"`
		IsShared     *bool  `json:"isShared"`
	}
	// Invalid redirect/scope is expected on this read-only policy lookup; Casdoor
	// still returns the resolved, masked application in data. No code is created.
	if json.Unmarshal(env.Data, &app) != nil || app.Organization != "unihub" || app.ClientID != s.opts.ManagementClientID || app.IsShared == nil || *app.IsShared {
		return errors.New("unsafe management application")
	}
	return nil
}

func hasPolicyFields(raw json.RawMessage, fields ...string) bool {
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return false
	}
	for _, field := range fields {
		if _, ok := data[field]; !ok {
			return false
		}
	}
	return true
}
