// Package api publishes the machine-readable product API contract.
package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var specification []byte

// Handler is outside the optional console's cookie boundary.
func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.yaml" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=300")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write(specification)
		}
	})
}
