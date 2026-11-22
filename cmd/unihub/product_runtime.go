package main

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unihub/internal/httpapi"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

func parseRuntimeServices(raw string, registry productusers.Registry, legacyToken string) (map[string]httpapi.RuntimeService, error) {
	return parseRuntimeServicesWithClients(raw, registry, legacyToken, nil)
}

func parseRuntimeServicesWithClients(raw string, registry productusers.Registry, legacyToken string, clients map[string]products.Client) (map[string]httpapi.RuntimeService, error) {
	if err := validateIdentityLinkClients(registry, clients); err != nil {
		return nil, err
	}
	out := map[string]httpapi.RuntimeService{}
	if raw == "" {
		return out, nil
	}
	invalid := errors.New("invalid product runtime service configuration")
	if len(raw) > 64<<10 {
		return nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&out); e != nil {
		return nil, invalid
	}
	if decoder.Decode(&struct{}{}) != io.EOF || len(out) == 0 || len(out) > 64 {
		return nil, invalid
	}
	known := map[string]bool{"bindings:write": true, "access:read": true, "users:read": true, "users:write": true, "calls:refund": true, "calls:reprice": true, "identity:lifecycle": true, "billing:read": true, "billing:write": true, "cash:charge": true, "cash:payment": true, "credits:adjust": true, "credits:award": true, "credits:payment": true, "migrations:write": true, "budgets:read": true, "budgets:write": true, "identity:browser": true, "identity:register": true, "identity:authenticate": true, "identity:lookup": true, "identity:manage": true, "identity:import": true, "finance:read": true, "finance:write": true, "model:authorize": true}
	known["identity:link"] = true
	known["identity:session"] = true
	known["identity:source-lifecycle"] = true
	tokens := map[string]bool{}
	for id, c := range out {
		if id == "" || len(id) > 64 || strings.TrimSpace(id) != id || len(c.Token) < 32 || len(c.Token) > 512 || c.Token == legacyToken || strings.ContainsAny(c.Token, " \r\n\t") || tokens[c.Token] {
			return nil, invalid
		}
		tokens[c.Token] = true
		c.InstanceID = productusers.Instance(c.InstanceID)
		if _, ok := registry.Get(c.ProductID, c.InstanceID); !ok {
			return nil, invalid
		}
		seen := map[string]bool{}
		if len(c.AllowedOps) == 0 {
			return nil, invalid
		}
		for _, op := range c.AllowedOps {
			if !known[op] || seen[op] {
				return nil, invalid
			}
			seen[op] = true
		}
		financeEnabled := seen["finance:read"] || seen["finance:write"]
		if financeEnabled {
			if c.FinanceClientID == "" {
				return nil, invalid
			}
			client, ok := clients[c.FinanceClientID]
			if !ok || client.ClientID != c.FinanceClientID || client.ProductID != c.ProductID || productusers.Instance(client.InstanceID) != c.InstanceID {
				return nil, invalid
			}
			eligible := false
			for _, scope := range financeRouteScopes {
				eligible = eligible || client.HasScope(scope)
			}
			if !eligible {
				return nil, invalid
			}
		} else if c.FinanceClientID != "" {
			return nil, invalid
		}
		identityEnabled := seen["identity:link"] || seen["identity:session"] || seen["identity:source-lifecycle"]
		if identityEnabled {
			client, ok := clients[c.IdentityClientID]
			if !ok || client.ClientID != c.IdentityClientID || client.ProductID != c.ProductID || productusers.Instance(client.InstanceID) != c.InstanceID ||
				!client.BrowserSSO || client.IdentityLinkRole == "" {
				return nil, invalid
			}
		} else if c.IdentityClientID != "" {
			return nil, invalid
		}
		out[id] = c
	}
	return out, nil
}

func validateIdentityLinkClients(registry productusers.Registry, clients map[string]products.Client) error {
	for _, client := range clients {
		if client.IdentityLinkRole == "" {
			continue
		}
		manifest, ok := registry.Get(client.ProductID, client.InstanceID)
		if !ok || !slices.ContainsFunc(manifest.Roles, func(role productusers.Role) bool {
			return role.ID == client.IdentityLinkRole && !role.Privileged && !role.NativeBaseline &&
				(role.ProductRole == 0 || role.ProductRole == 1) &&
				len(role.Permissions) == 1 && role.Permissions[0] == "product:use"
		}) {
			return errors.New("identity_link_role requires a nonprivileged product instance role with only product:use")
		}
		defaultPermissions, err := productusers.EffectivePermissions(manifest, manifest.DefaultRoles, nil, 1)
		if err != nil || slices.Contains(defaultPermissions, "product:use") {
			return errors.New("identity linking requires valid default roles without product:use")
		}
	}
	return nil
}

var financeRouteScopes = []string{"accounts:read", "identity:read", "catalog:read", "subscriptions:read", "orders:read", "wallets:read", "recharges:read", "recharges:write", "operations:read", "model-credentials:write", "usage:read", "settings:write"}
