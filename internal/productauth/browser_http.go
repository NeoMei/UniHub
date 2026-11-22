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

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/auth"
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
func (s *Service) browserSessionIdentity(ctx context.Context, raw string) (claims auth.Claims, userID string, err error) {
	if len(raw) != 43 {
		return claims, "", pgx.ErrNoRows
	}
	err = s.pool.QueryRow(ctx, `SELECT s.issuer,s.subject,s.name,s.email,extract(epoch from s.expires_at)::bigint,u.id
		FROM browser_sessions s
		JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject
		JOIN users u ON u.id=i.user_id
		WHERE s.id_hash=$1 AND s.expires_at>now() AND u.active AND s.created_at>=u.session_not_before
		AND NOT EXISTS (SELECT 1 FROM identity_revocations v WHERE v.issuer=s.issuer AND v.subject=s.subject AND v.pending)`,
		browserSessionDigest(raw)).Scan(&claims.Issuer, &claims.Subject, &claims.Name, &claims.Email, &claims.ExpiresAt, &userID)
	return
}

func (s *Service) currentBrowser(ctx context.Context, raw string) (auth.Claims, error) {
	if len(raw) != 43 || !s.BrowserSessionAllowed(ctx, browserSessionDigest(raw)) {
		return auth.Claims{}, accounts.ErrForbidden
	}
	claims, _, err := s.browserSessionIdentity(ctx, raw)
	if err != nil {
		return auth.Claims{}, accounts.ErrForbidden
	}
	return claims, nil
}

// BrowserHandler serves configured login methods and browser-bound product SSO.
func (s *Service) BrowserHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/login" && r.Method == http.MethodGet && s.showBrowserLoginPicker(w, r) {
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/auth/product/") {
			next.ServeHTTP(w, r)
			return
		}
		browserAuthHeaders(w)
		if r.URL.Path == browserHandoffFinishPath && r.Method == http.MethodPost {
			s.confirmBrowserHandoff(w, r)
			return
		}
		if r.Method != http.MethodGet {
			allow := http.MethodGet
			if r.URL.Path == browserHandoffFinishPath {
				allow += ", " + http.MethodPost
			}
			w.Header().Set("Allow", allow)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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
			s.browserCookie(w, browserHandoffCookie, binding, browserHandoffFinishPath, 120*time.Second)
			http.Redirect(w, r, destination, 303)
		case browserHandoffFinishPath:
			s.browserHandoffFinish(w, r, q.Get("handoff"), q.Get("state"), "", "")
		case "/auth/product/link":
			s.handleIdentityLink(w, r)
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
