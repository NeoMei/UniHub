// Runnable product-owned user system. No passwords or IdP management credentials.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
)

type config struct {
	Mode            string `json:"mode"`
	Listen          string `json:"listen"`
	PublicURL       string `json:"public_url"`
	APIURL          string `json:"api_url"`
	CentralIssuer   string `json:"central_issuer"`
	OIDCIssuer      string `json:"oidc_issuer"`
	ClientID        string `json:"client_id"`
	ClientSecret    string `json:"client_secret"`
	ProductID       string `json:"product_id"`
	InstanceID      string `json:"instance_id"`
	Database        string `json:"database"`
	AccessTokenType string `json:"access_token_type"`
}

func loadConfig() (config, error) {
	var c config
	path := os.Getenv("PRODUCT_CONFIG")
	if path == "" {
		return c, errors.New("PRODUCT_CONFIG must name a private explicit JSON config")
	}
	f, e := os.Open(path)
	if e != nil {
		return c, errors.New("config unavailable")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, errors.New("invalid config")
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return c, errors.New("invalid config")
	}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return c, errors.New("public_url must be an HTTPS or loopback origin")
	}
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	if (c.Mode != "unihub" && c.Mode != "standalone") || c.Listen == "" || c.Database == "" || c.ProductID == "" || c.InstanceID == "" || c.ClientID == "" || c.OIDCIssuer == "" {
		return c, errors.New("mode, listen, database, identity and product configuration required")
	}
	if c.Mode == "unihub" && (c.APIURL == "" || c.CentralIssuer == "") {
		return c, errors.New("unihub API and canonical issuer required")
	}
	return c, nil
}
func randomID() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b)
}

type profile struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Notebook        string   `json:"notebook"`
	Initializations int      `json:"initializations"`
	Articles        []string `json:"articles"`
}
type store struct {
	mu   sync.Mutex
	path string
	lock *os.File
	rows map[string]profile
}

// One process owns the database. Each transaction commits identity, extension and
// resources in one fsync+atomic-rename snapshot; failed commits publish nothing.
// Replace this seam with your SQL transaction/unique(issuer,subject) provisioner.
func openStore(path string) (*store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, errors.New("product database already owned or unavailable")
	}
	s := &store{path: path, lock: lock, rows: map[string]profile{}}
	b, e := os.ReadFile(path)
	if e == nil {
		e = json.Unmarshal(b, &s.rows)
	}
	if e != nil && !os.IsNotExist(e) {
		s.Close()
		return nil, e
	}
	return s, nil
}
func (s *store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.lock.Close()
	if e == nil {
		e = os.Remove(s.path + ".lock")
	}
	return e
}
func identityKey(a userkit.Access) string {
	h := sha256.Sum256([]byte(a.Issuer + "\x00" + a.Subject))
	return hex.EncodeToString(h[:])
}
func (s *store) transaction(change func(map[string]profile) error) error {
	next := make(map[string]profile, len(s.rows))
	for k, v := range s.rows {
		v.Articles = append([]string{}, v.Articles...)
		next[k] = v
	}
	if e := change(next); e != nil {
		return e
	}
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".product-transaction-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(name, s.path)
	}
	if e != nil {
		return e
	}
	s.rows = next
	// Rename is the commit point. Directory sync improves crash durability.
	if dir, e := os.Open(filepath.Dir(s.path)); e == nil {
		defer dir.Close()
		_ = dir.Sync()
	}
	return nil
}
func (s *store) Ensure(_ context.Context, a userkit.Access) (userkit.LocalUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := identityKey(a)
	if p, ok := s.rows[key]; ok {
		return userkit.LocalUser{ID: p.ID}, nil
	}
	p := profile{ID: randomID(), Name: a.User.Name, Notebook: "Personal notebook", Initializations: 1, Articles: []string{}}
	e := s.transaction(func(rows map[string]profile) error { rows[key] = p; return nil })
	if e != nil {
		return userkit.LocalUser{}, e
	}
	return userkit.LocalUser{ID: p.ID}, nil
}
func (s *store) profile(id string) (profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.rows {
		if p.ID == id {
			return p, nil
		}
	}
	return profile{}, userkit.ErrDenied
}
func (s *store) Allow(_ context.Context, p userkit.Principal, permission, resource string) error {
	if resource != "" && resource != p.LocalUser.ID {
		return userkit.ErrDenied
	}
	return nil
}
func (s *store) add(id, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transaction(func(rows map[string]profile) error {
		for k, p := range rows {
			if p.ID == id {
				p.Articles = append(p.Articles, text)
				rows[k] = p
				return nil
			}
		}
		return userkit.ErrDenied
	})
}

type standaloneProvider struct {
	mu     sync.Mutex
	c      config
	proofs map[string]userkit.Access
}

func (p *standaloneProvider) remember(i userkit.BrowserIdentity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, a := range p.proofs {
		if a.ExpiresAt <= time.Now().Unix() {
			delete(p.proofs, key)
		}
	}
	p.proofs[i.AccessToken] = userkit.Access{Issuer: p.c.OIDCIssuer, Subject: i.Subject, ProductID: p.c.ProductID, InstanceID: p.c.InstanceID, User: userkit.User{ID: i.Subject}, Roles: []string{"member"}, Permissions: []string{"product:use", "article:create"}, Version: 1, Active: true, ExpiresAt: i.ExpiresAt}
}

// This standalone seam trusts only Browser-verified product-owned OIDC proofs.
// It has local member defaults, no central roles and no password implementation.
func (p *standaloneProvider) Resolve(_ context.Context, proof string) (userkit.Access, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.proofs[proof]
	if !ok || a.ExpiresAt <= time.Now().Unix() {
		delete(p.proofs, proof)
		return userkit.Access{}, userkit.ErrDenied
	}
	return a, nil
}

type session struct {
	proof, csrf string
	expires     int64
}
type app struct {
	c        config
	browser  *userkit.Browser
	service  *userkit.Service
	store    *store
	local    *standaloneProvider
	mu       sync.Mutex
	sessions map[string]session
}

func (a *app) cookieName() string {
	h := sha256.Sum256([]byte(a.c.PublicURL + "\x00" + a.c.ProductID))
	return "product_" + hex.EncodeToString(h[:8])
}
func (a *app) cookie(w http.ResponseWriter, name, value string, expires int64) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(a.c.PublicURL, "https:"), SameSite: http.SameSiteLaxMode, Expires: time.Unix(expires, 0)})
}
func (a *app) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	name := a.cookieName()
	if r.Method == "GET" && r.URL.Path == "/login" {
		binding := randomID()
		location, e := a.browser.Begin(binding, "/")
		if e != nil {
			http.Error(w, "Login unavailable", 503)
			return
		}
		a.cookie(w, name+"_login", binding, time.Now().Add(5*time.Minute).Unix())
		http.Redirect(w, r, location, 302)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/auth/callback" {
		binding, e := r.Cookie(name + "_login")
		if e != nil {
			http.Error(w, "Login rejected", 401)
			return
		}
		identity, e := a.browser.Complete(r.Context(), binding.Value, a.c.PublicURL+r.URL.RequestURI())
		if e != nil {
			http.Error(w, "Login rejected", 401)
			return
		}
		if a.local != nil {
			a.local.remember(identity)
		}
		if _, e = a.service.Authenticate(r.Context(), identity.AccessToken); e != nil {
			http.Error(w, "Access denied", 403)
			return
		}
		sid := randomID()
		a.mu.Lock()
		for key, s := range a.sessions {
			if s.expires <= time.Now().Unix() {
				delete(a.sessions, key)
			}
		}
		a.sessions[sid] = session{identity.AccessToken, randomID(), identity.ExpiresAt}
		a.mu.Unlock()
		a.cookie(w, name, sid, identity.ExpiresAt)
		a.cookie(w, name+"_login", "", 1)
		http.Redirect(w, r, identity.ReturnTo, 302)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
		return
	}
	cookie, e := r.Cookie(name)
	if e != nil {
		http.Error(w, "Authentication required", 401)
		return
	}
	a.mu.Lock()
	s, ok := a.sessions[cookie.Value]
	if !ok || s.expires <= time.Now().Unix() {
		delete(a.sessions, cookie.Value)
		ok = false
	}
	a.mu.Unlock()
	if !ok {
		http.Error(w, "Authentication required", 401)
		return
	}
	if r.Method != "GET" && (r.Header.Get("Origin") != a.c.PublicURL || r.Header.Get("X-Product-CSRF") != s.csrf) {
		http.Error(w, "Request denied", 403)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/logout" {
		a.mu.Lock()
		delete(a.sessions, cookie.Value)
		a.mu.Unlock()
		a.cookie(w, name, "", 1)
		w.WriteHeader(204)
		return
	}
	p, e := a.service.Authenticate(r.Context(), s.proof)
	if e != nil {
		http.Error(w, "Access denied", 403)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" && r.URL.Path == "/api/me" {
		profile, e := a.store.profile(p.LocalUser.ID)
		if e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access": p.Access, "profile": profile, "csrf_token": s.csrf})
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/articles" {
		var body struct {
			Text  string `json:"text"`
			Owner string `json:"owner"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if e = d.Decode(&body); e != nil || len(body.Text) == 0 || len(body.Text) > 1000 {
			http.Error(w, "Invalid article", 400)
			return
		}
		if body.Owner == "" {
			body.Owner = p.LocalUser.ID
		}
		if e = p.RequirePermission(r.Context(), "article:create", body.Owner); e != nil {
			http.Error(w, "Access denied", 403)
			return
		}
		if e = a.store.add(body.Owner, body.Text); e != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{"created":true}`)
		return
	}
	http.NotFound(w, r)
}
func main() {
	c, e := loadConfig()
	if e != nil {
		log.Fatal(e)
	}
	s, e := openStore(c.Database)
	if e != nil {
		log.Fatal(e)
	}
	defer s.Close()
	b, e := userkit.NewBrowser(context.Background(), userkit.BrowserConfig{Issuer: c.OIDCIssuer, ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURI: c.PublicURL + "/auth/callback", AccessTokenType: c.AccessTokenType})
	if e != nil {
		log.Fatal("OIDC initialization failed")
	}
	var provider userkit.Provider
	var local *standaloneProvider
	if c.Mode == "unihub" {
		provider, e = userkit.NewClient(userkit.Config{BaseURL: c.APIURL, Issuer: c.CentralIssuer, ClientID: c.ClientID, ProductID: c.ProductID, InstanceID: c.InstanceID})
	} else {
		local = &standaloneProvider{c: c, proofs: map[string]userkit.Access{}}
		provider = local
	}
	if e != nil {
		log.Fatal("Provider configuration failed")
	}
	svc, e := userkit.NewService(provider, s, s)
	if e != nil {
		log.Fatal(e)
	}
	a := &app{c: c, browser: b, service: svc, store: s, local: local, sessions: map[string]session{}}
	server := &http.Server{Addr: c.Listen, Handler: http.HandlerFunc(a.handle), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Print("Product user example ready")
	log.Fatal(server.ListenAndServe())
}

const page = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Product Notes</title><style>body{font:17px system-ui;max-width:850px;margin:4rem auto;padding:0 2rem;background:#f7f6f1;color:#182b2d}h1{font-size:3rem}button,a{padding:.7rem;background:#235b5a;color:white;border:0;border-radius:5px;cursor:pointer}pre{white-space:pre-wrap;background:white;padding:1.5rem;border-radius:12px}input{padding:.7rem;width:55%}</style><h1>Product Notes</h1><p>One identity. Your own notebook.</p><p><a href="/login">Sign in</a> <button id="reload">Refresh access</button> <button id="logout">Sign out</button></p><pre id="identity">Loading…</pre><form id="article"><input id="text" required placeholder="Write a note"><button>Save note</button></form><p id="status" role="status"></p><script>let csrf='';async function load(){const r=await fetch('/api/me');if(!r.ok){document.querySelector('#identity').textContent='Sign in to open your notebook';return}const d=await r.json();csrf=d.csrf_token;document.querySelector('#identity').textContent=JSON.stringify({subject:d.access.subject,roles:d.access.roles,permissions:d.access.permissions,profile:d.profile},null,2)}document.querySelector('#reload').onclick=load;document.querySelector('#logout').onclick=async()=>{await fetch('/logout',{method:'POST',headers:{'X-Product-CSRF':csrf}});location.reload()};document.querySelector('#article').onsubmit=async e=>{e.preventDefault();const r=await fetch('/api/articles',{method:'POST',headers:{'Content-Type':'application/json','X-Product-CSRF':csrf},body:JSON.stringify({text:document.querySelector('#text').value})});document.querySelector('#status').textContent=r.ok?'Note saved':'Access denied';if(r.ok)load()};load()</script></html>`
