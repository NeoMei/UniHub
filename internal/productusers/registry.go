// Package productusers owns product identity roles independently of commerce.
package productusers

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

type Role struct {
	NativeBaseline bool     `json:"native_baseline,omitempty"`
	ProductRole    int      `json:"product_role,omitempty"`
	Privileged     bool     `json:"privileged,omitempty"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Permissions    []string `json:"permissions"`
}
type Manifest struct {
	ProductID    string   `json:"product_id"`
	InstanceID   string   `json:"instance_id"`
	Name         string   `json:"name"`
	EntryURL     string   `json:"entry_url"`
	Version      int64    `json:"version"`
	DefaultRoles []string `json:"default_roles"`
	Roles        []Role   `json:"roles"`
}
type Registry map[string]Manifest

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var permission = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}:[a-z][a-z0-9_-]{0,63}$`)

func key(product, instance string) string { return product + "/" + instance }
func Instance(value string) string {
	if value == "" {
		return "default"
	}
	return value
}
func Parse(raw []byte) (Registry, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var list []Manifest
	if d.Decode(&list) != nil || d.Decode(&struct{}{}) != io.EOF || len(list) > 256 {
		return nil, errors.New("invalid product user manifests")
	}
	r := Registry{}
	for _, m := range list {
		m.InstanceID = Instance(m.InstanceID)
		if !identifier.MatchString(m.ProductID) || !identifier.MatchString(m.InstanceID) || strings.TrimSpace(m.Name) == "" || len(m.Name) > 100 || m.Version < 1 || m.Version > 1_000_000_000 || !safeEntry(m.EntryURL) || len(m.Roles) > 64 {
			return nil, errors.New("invalid product user manifest")
		}
		if _, exists := r[key(m.ProductID, m.InstanceID)]; exists {
			return nil, errors.New("duplicate product instance manifest")
		}
		seen := map[string]Role{}
		for i, role := range m.Roles {
			if (role.NativeBaseline && role.ProductRole != 10 && role.ProductRole != 100) || (role.ProductRole != 0 && role.ProductRole != 1 && role.ProductRole != 10 && role.ProductRole != 100) || !identifier.MatchString(role.ID) || strings.TrimSpace(role.Name) == "" || len(role.Permissions) > 128 {
				return nil, errors.New("invalid product role")
			}
			if _, exists := seen[role.ID]; exists {
				return nil, errors.New("duplicate role")
			}
			for _, p := range role.Permissions {
				if !permission.MatchString(p) || strings.HasPrefix(p, "platform:") {
					return nil, errors.New("invalid product permission")
				}
			}
			role.Permissions = normalize(role.Permissions)
			seen[role.ID] = role
			m.Roles[i] = role
		}
		m.DefaultRoles = normalize(m.DefaultRoles)
		for _, id := range m.DefaultRoles {
			role, ok := seen[id]
			if !ok {
				return nil, errors.New("unknown default role")
			}
			if role.Privileged || role.ProductRole > 1 {
				return nil, errors.New("privileged roles cannot be defaults")
			}
		}
		slices.SortFunc(m.Roles, func(a, b Role) int { return strings.Compare(a.ID, b.ID) })
		r[key(m.ProductID, m.InstanceID)] = m
	}
	return r, nil
}
func safeEntry(raw string) bool {
	if strings.ContainsAny(raw, "\\\r\n\t ") {
		return false
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())))
}
func normalize(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return slices.Compact(out)
}
func (r Registry) Get(product, instance string) (Manifest, bool) {
	m, ok := r[key(product, Instance(instance))]
	return m, ok
}
func (r Registry) List() []Manifest {
	out := []Manifest{}
	for _, m := range r {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b Manifest) int {
		return strings.Compare(key(a.ProductID, a.InstanceID), key(b.ProductID, b.InstanceID))
	})
	return out
}
