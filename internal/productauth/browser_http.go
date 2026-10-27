package productauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
)

func browserSessionDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func browserSecret() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (s *Service) browserCookie(w http.ResponseWriter, name, value, path string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, HttpOnly: true, Secure: strings.HasPrefix(s.issuer, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()), Expires: time.Now().Add(ttl)})
}
func (s *Service) currentBrowser(ctx context.Context, raw string) (auth.Claims, error) {
	if len(raw) != 43 || !s.BrowserSessionAllowed(ctx, browserSessionDigest(raw)) {
		return auth.Claims{}, accounts.ErrForbidden
	}
	var claims auth.Claims
	e := s.pool.QueryRow(ctx, `SELECT s.issuer,s.subject,s.name,s.email,extract(epoch from s.expires_at)::bigint FROM browser_sessions s JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject JOIN users u ON u.id=i.user_id WHERE s.id_hash=$1 AND s.expires_at>now() AND u.active`, browserSessionDigest(raw)).Scan(&claims.Issuer, &claims.Subject, &claims.Name, &claims.Email, &claims.ExpiresAt)
	if e != nil {
		return auth.Claims{}, accounts.ErrForbidden
	}
	return claims, nil
}

// BrowserHandler exposes only registered callback redirects. A central identity
// cookie is issued after the product proves the originating browser's own SID.
func (s *Service) BrowserHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/auth/product/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" {
			http.Error(w, "Method Not Allowed", 405)
			return
		}
		for _, values := range r.URL.Query() {
			if len(values) != 1 {
				http.Error(w, "Invalid Request", 400)
				return
			}
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/auth/product/start":
			binding := browserSecret()
			destination, e := s.BeginBrowserHandoff(r.Context(), q.Get("client_id"), q.Get("state"), binding)
			if e != nil {
				http.Error(w, "Invalid Handoff", 400)
				return
			}
			s.browserCookie(w, "unihub_product_handoff", binding, "/auth/product/finish", 120*time.Second)
			http.Redirect(w, r, destination, 303)
		case "/auth/product/finish":
			cookie, e := r.Cookie("unihub_product_handoff")
			if e != nil {
				http.Error(w, "Invalid Handoff", 401)
				return
			}
			completion, e := s.FinishBrowserHandoff(r.Context(), q.Get("handoff"), q.Get("state"), cookie.Value)
			if e != nil {
				http.Error(w, "Invalid Handoff", 401)
				return
			}
			id, csrf := browserSecret(), browserSecret()
			tx, e := s.pool.Begin(r.Context())
			if e != nil {
				http.Error(w, "Unavailable", 503)
				return
			}
			defer database.Rollback(tx)
			if old, e := r.Cookie("unihub_session"); e == nil {
				if _, e = tx.Exec(r.Context(), `DELETE FROM browser_sessions WHERE id_hash=$1`, browserSessionDigest(old.Value)); e != nil {
					http.Error(w, "Unavailable", 503)
					return
				}
			}
			expiry := time.Now().Add(time.Hour)
			if _, e = tx.Exec(r.Context(), `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, browserSessionDigest(id), completion.Claims.Issuer, completion.Claims.Subject, completion.Claims.Name, completion.Claims.Email, csrf, expiry); e != nil {
				http.Error(w, "Unavailable", 503)
				return
			}
			if _, e = tx.Exec(r.Context(), `INSERT INTO product_browser_session_origins(session_id_hash,binding_id,auth_version) VALUES($1,$2,$3)`, browserSessionDigest(id), completion.BindingID, completion.AuthVersion); e != nil {
				http.Error(w, "Unavailable", 503)
				return
			}
			if e = tx.Commit(r.Context()); e != nil {
				http.Error(w, "Unavailable", 503)
				return
			}
			s.browserCookie(w, "unihub_session", id, "/", time.Hour)
			s.browserCookie(w, "unihub_product_handoff", "", "/auth/product/finish", -time.Second)
			http.Redirect(w, r, completion.RedirectURI, 303)
		case "/auth/product/authorize":
			client, e := s.BrowserClient(q.Get("client_id"), q.Get("redirect_uri"))
			if e != nil || !validBrowserState(q.Get("state")) || !validBrowserState(q.Get("nonce")) || q.Get("code_challenge_method") != "S256" {
				http.Error(w, "Invalid Authorization", 400)
				return
			}
			cookie, e := r.Cookie("unihub_session")
			if e != nil {
				http.Redirect(w, r, browserRedirect(q.Get("redirect_uri"), url.Values{"broker": {"1"}, "error": {"login_required"}, "state": {q.Get("state")}}), 303)
				return
			}
			claims, e := s.currentBrowser(r.Context(), cookie.Value)
			if e != nil {
				http.Redirect(w, r, browserRedirect(q.Get("redirect_uri"), url.Values{"broker": {"1"}, "error": {"login_required"}, "state": {q.Get("state")}}), 303)
				return
			}
			destination, e := s.AuthorizeBrowser(r.Context(), claims, client.ClientID, q.Get("redirect_uri"), q.Get("state"), q.Get("nonce"), q.Get("code_challenge"), browserSessionDigest(cookie.Value))
			if e != nil {
				http.Redirect(w, r, browserRedirect(q.Get("redirect_uri"), url.Values{"broker": {"1"}, "error": {"access_denied"}, "state": {q.Get("state")}}), 303)
				return
			}
			http.Redirect(w, r, destination, 303)
		default:
			http.NotFound(w, r)
		}
	})
}
