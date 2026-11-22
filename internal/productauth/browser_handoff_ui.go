package productauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/database"
)

const (
	browserHandoffCookie     = "unihub_product_handoff"
	browserHandoffFinishPath = "/auth/product/finish"
)

const browserPageHead = `<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root { color-scheme: light dark; font: 17px system-ui, sans-serif }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 1rem; box-sizing: border-box }
main { width: 100%; max-width: 26rem }
h1 { font-size: 1.55rem; margin: 0 0 1.25rem }
p { line-height: 1.5; overflow-wrap: anywhere }
nav, form { display: grid; gap: .65rem; margin-top: 1.5rem }
a, button { display: block; border: 1px solid currentColor; border-radius: .45rem; padding: .75rem 1rem; font: inherit; text-align: center; cursor: pointer }
</style>`

var browserLoginPickerPage = template.Must(template.New("login-picker").Parse(`<!doctype html>
<html lang="zh-CN"><head><title>选择登录方式 · UniHub</title>` + browserPageHead + `</head>
<body><main>
<h1>选择登录方式</h1>
<nav aria-label="登录方式">
<a href="/auth/login?method=central">使用 UniHub 账号登录</a>
{{range .}}<a href="{{.URL}}">{{.Label}}</a>{{end}}
</nav>
</main></body></html>`))

var browserSwitchPage = template.Must(template.New("account-switch").Parse(`<!doctype html>
<html lang="zh-CN"><head><title>切换 UniHub 账号</title>` + browserPageHead + `</head>
<body><main>
<h1>切换 UniHub 账号</h1>
<p>当前账号　{{.Current}}</p>
<p>将切换至　{{.Incoming}}</p>
<form method="post" action="/auth/product/finish">
<input type="hidden" name="handoff" value="{{.Handoff}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<button name="action" value="confirm">确认切换</button>
<button name="action" value="cancel">取消，保留当前账号</button>
</form>
</main></body></html>`))

func browserAuthHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func browserPageHeaders(w http.ResponseWriter) {
	browserAuthHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}

func (s *Service) showBrowserLoginPicker(w http.ResponseWriter, r *http.Request) bool {
	query := r.URL.Query()
	if query.Get("method") == "central" || query.Get("switch_account") == "1" {
		return false
	}
	type loginMethod struct{ URL, Label string }
	var methods []loginMethod
	for _, id := range slices.Sorted(maps.Keys(s.browserClients)) {
		client := s.browserClients[id]
		if client.NativeLoginURL != "" {
			methods = append(methods, loginMethod{URL: client.NativeLoginURL, Label: client.NativeLoginLabel})
		}
	}
	if len(methods) == 0 {
		return false
	}
	browserPageHeaders(w)
	_ = browserLoginPickerPage.Execute(w, methods)
	return true
}

// pendingBrowserHandoff checks the browser binding without redeeming the handoff.
func (s *Service) pendingBrowserHandoff(ctx context.Context, id, state, binding string) (clientID string, user accounts.User, err error) {
	err = s.pool.QueryRow(ctx, `SELECT h.client_id,u.id,u.name,u.email
		FROM product_browser_handoffs h
		JOIN users u ON u.id=h.approved_user_id
		WHERE h.id_hash=$1 AND h.state_hash=$2 AND h.browser_hash=$3
		  AND h.expires_at>now() AND NOT h.consumed AND u.active`,
		digest(id), digest(state), digest(binding)).Scan(&clientID, &user.ID, &user.Name, &user.Email)
	return
}

// The HttpOnly handoff cookie is the signing key. Bind the form to this
// handoff, the central session that saw the prompt, and the approved account.
func browserSwitchToken(binding, handoff, state, session, incomingUser string) string {
	mac := hmac.New(sha256.New, []byte(binding))
	mac.Write([]byte(digest([]string{handoff, state, session, incomingUser})))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func browserAccountLabel(name, email, id string) string {
	switch {
	case name != "" && email != "" && name != email:
		return name + " (" + email + ")"
	case name != "":
		return name
	case email != "":
		return email
	default:
		return id
	}
}

func (s *Service) confirmBrowserHandoff(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if r.URL.RawQuery != "" || r.ParseForm() != nil {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}
	for key, values := range r.PostForm {
		if !slices.Contains([]string{"handoff", "state", "action", "csrf"}, key) || len(values) != 1 {
			http.Error(w, "Invalid Request", http.StatusBadRequest)
			return
		}
	}
	s.browserHandoffFinish(w, r, r.PostForm.Get("handoff"), r.PostForm.Get("state"), r.PostForm.Get("action"), r.PostForm.Get("csrf"))
}

func (s *Service) browserHandoffFinish(w http.ResponseWriter, r *http.Request, handoff, state, action, csrf string) {
	ctx := r.Context()
	binding, err := r.Cookie(browserHandoffCookie)
	if err != nil {
		http.Error(w, "Invalid Handoff", http.StatusUnauthorized)
		return
	}
	clientID, incoming, err := s.pendingBrowserHandoff(ctx, handoff, state, binding.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Invalid Handoff", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "Unavailable", http.StatusServiceUnavailable)
		return
	}
	client, err := s.BrowserClient(clientID, "")
	if err != nil {
		http.Error(w, "Invalid Handoff", http.StatusUnauthorized)
		return
	}
	old, _ := r.Cookie("unihub_session")
	preserve := false
	protectAccount := client.IdentityLinkRole != ""
	if protectAccount {
		var current accounts.User
		if old != nil {
			claims, userID, err := s.browserSessionIdentity(ctx, old.Value)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, "Unavailable", http.StatusServiceUnavailable)
				return
			}
			// Protect the current account even if its product source is no
			// longer usable. Source validity only controls session reuse.
			current = accounts.User{ID: userID, Name: claims.Name, Email: claims.Email}
		}
		switching := current.ID != "" && current.ID != incoming.ID
		if r.Method == http.MethodPost {
			if !switching || !hmac.Equal([]byte(csrf), []byte(browserSwitchToken(binding.Value, handoff, state, old.Value, incoming.ID))) {
				http.Error(w, "Invalid Confirmation", http.StatusForbidden)
				return
			}
			switch action {
			case "cancel":
				s.browserCookie(w, browserHandoffCookie, "", browserHandoffFinishPath, -time.Second)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			case "confirm":
			default:
				http.Error(w, "Invalid Confirmation", http.StatusBadRequest)
				return
			}
		} else if switching {
			browserPageHeaders(w)
			_ = browserSwitchPage.Execute(w, struct{ Current, Incoming, Handoff, State, CSRF string }{
				Current:  browserAccountLabel(current.Name, current.Email, current.ID),
				Incoming: browserAccountLabel(incoming.Name, incoming.Email, incoming.ID),
				Handoff:  handoff,
				State:    state,
				CSRF:     browserSwitchToken(binding.Value, handoff, state, old.Value, incoming.ID),
			})
			return
		} else {
			preserve = current.ID != "" && s.BrowserSessionAllowed(ctx, browserSessionDigest(old.Value))
		}
	} else if r.Method == http.MethodPost {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	completion, err := s.FinishBrowserHandoff(ctx, handoff, state, binding.Value)
	if err != nil || (protectAccount && completion.User.ID != incoming.ID) {
		http.Error(w, "Invalid Handoff", http.StatusUnauthorized)
		return
	}
	if !preserve && !s.writeBrowserHandoffSession(w, r, completion, old) {
		return
	}
	s.browserCookie(w, browserHandoffCookie, "", browserHandoffFinishPath, -time.Second)
	http.Redirect(w, r, completion.RedirectURI, http.StatusSeeOther)
}

func (s *Service) writeBrowserHandoffSession(w http.ResponseWriter, r *http.Request, completion BrowserCompletion, old *http.Cookie) bool {
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		http.Error(w, "Unavailable", http.StatusServiceUnavailable)
		return false
	}
	defer database.Rollback(tx)
	if old != nil {
		deleted, err := tx.Exec(ctx, `DELETE FROM browser_sessions WHERE id_hash=$1`, browserSessionDigest(old.Value))
		if err != nil {
			http.Error(w, "Unavailable", http.StatusServiceUnavailable)
			return false
		}
		if r.Method == http.MethodPost && deleted.RowsAffected() != 1 {
			http.Error(w, "Session Changed", http.StatusConflict)
			return false
		}
	}
	id, csrf := browserSecret(), browserSecret()
	idHash := browserSessionDigest(id)
	expiry := time.Now().Add(time.Hour)
	if _, err = tx.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		idHash, completion.Claims.Issuer, completion.Claims.Subject, completion.Claims.Name, completion.Claims.Email, csrf, expiry); err != nil {
		http.Error(w, "Unavailable", http.StatusServiceUnavailable)
		return false
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product_browser_session_origins(session_id_hash,binding_id,auth_version) VALUES($1,$2,$3)`,
		idHash, completion.BindingID, completion.AuthVersion); err != nil {
		http.Error(w, "Unavailable", http.StatusServiceUnavailable)
		return false
	}
	if err = tx.Commit(ctx); err != nil {
		http.Error(w, "Unavailable", http.StatusServiceUnavailable)
		return false
	}
	s.browserCookie(w, "unihub_session", id, "/", time.Hour)
	return true
}
