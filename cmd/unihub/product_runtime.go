package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unihub/internal/httpapi"
	"unihub/internal/productusers"
)

func parseRuntimeServices(raw string, registry productusers.Registry, legacyToken string) (map[string]httpapi.RuntimeService, error) {
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
	known := map[string]bool{"bindings:write": true, "access:read": true, "users:read": true, "users:write": true, "calls:refund": true, "calls:reprice": true, "identity:lifecycle": true, "billing:read": true, "billing:write": true, "credits:adjust": true, "credits:award": true, "credits:payment": true, "migrations:write": true, "budgets:read": true, "budgets:write": true, "identity:browser": true, "identity:register": true, "identity:authenticate": true, "identity:lookup": true, "identity:manage": true, "identity:import": true}
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
		out[id] = c
	}
	return out, nil
}
