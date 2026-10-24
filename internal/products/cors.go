package products

import (
	"net/http"
	"strings"
)

// CORS allows only explicitly registered browser origins. The authenticated API
// independently matches the actual token's product origin. Cookies are never
// enabled; the console retains its separate same-origin and CSRF checks.
func CORS(next http.Handler, clients map[string]Client) http.Handler {
	origins := map[string]bool{}
	for _, c := range clients {
		for _, o := range c.Origins {
			origins[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if r.Method == "OPTIONS" && r.Header.Get("Access-Control-Request-Method") != "" {
			method := r.Header.Get("Access-Control-Request-Method")
			allowedMethod := method == "GET" || method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE"
			allowed := origins[origin] && len(r.Header.Values("Origin")) == 1 && allowedMethod
			for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				switch strings.ToLower(strings.TrimSpace(h)) {
				case "", "authorization", "content-type", "idempotency-key":
				default:
					allowed = false
				}
			}
			if !allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		publicIdentity := r.Method == "POST" && (r.URL.Path == "/v1/identity/registrations" || r.URL.Path == "/v1/identity/authorize/password" || r.URL.Path == "/v1/identity/token")
		if origins[origin] && len(r.Header.Values("Origin")) == 1 && (r.Header.Get("Authorization") != "" || publicIdentity) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		next.ServeHTTP(w, r)
	})
}
