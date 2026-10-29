package productauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/url"
	"slices"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

type BrowserApproval struct {
	ClientID  string `json:"client_id"`
	HandoffID string `json:"handoff_id"`
	State     string `json:"state"`
}
type BrowserApprovalReceipt struct {
	FinishURL string `json:"finish_url"`
}
type BrowserExchange struct {
	ClientID    string `json:"client_id"`
	Code        string `json:"code"`
	Verifier    string `json:"code_verifier"`
	Nonce       string `json:"nonce"`
	RedirectURI string `json:"redirect_uri"`
}
type BrowserGrant struct {
	Access               productusers.Access `json:"access"`
	ProofToken           string              `json:"proof_token"`
	ExpiresAt            int64               `json:"expires_at"`
	SourceProofToken     string              `json:"source_proof_token,omitempty"`
	SourceProofExpiresAt int64               `json:"source_proof_expires_at,omitempty"`
}
type BrowserCompletion struct {
	User        accounts.User
	Claims      auth.Claims
	BindingID   string
	AuthVersion int64
	RedirectURI string
}

func (s *Service) ConfigureBrowserClients(clients map[string]products.Client) {
	s.browserClients = map[string]products.Client{}
	for id, c := range clients {
		s.browserClients[id] = c
	}
}
func (s *Service) BrowserClient(id, redirect string) (products.Client, error) {
	c, ok := s.browserClients[id]
	if !ok || !c.BrowserSSO || !s.users.Managed(c.ProductID, c.InstanceID) || redirect != "" && !slices.Contains(c.RedirectURIs, redirect) {
		return c, accounts.ErrForbidden
	}
	if len(c.RedirectURIs) != 1 {
		return c, accounts.ErrInvalid
	}
	return c, nil
}
func browserRedirect(raw string, values url.Values) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	for k, values := range values {
		q[k] = values
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func validBrowserState(raw string) bool {
	return len(raw) >= 32 && len(raw) <= 256 && !strings.ContainsAny(raw, "\r\n\x00")
}
func (s *Service) BeginBrowserHandoff(ctx context.Context, client, state, browser string) (string, error) {
	c, e := s.BrowserClient(client, "")
	if e != nil || !validBrowserState(state) || len(browser) < 32 {
		return "", accounts.ErrInvalid
	}
	id := randomToken()
	_, e = s.pool.Exec(ctx, `INSERT INTO product_browser_handoffs(id_hash,client_id,state_hash,browser_hash,expires_at) VALUES($1,$2,$3,$4,now()+interval '120 seconds')`, digest(id), client, digest(state), digest(browser))
	if e != nil {
		return "", e
	}
	return browserRedirect(c.RedirectURIs[0], url.Values{"handoff": {id}, "state": {state}}), nil
}
func (s *Service) ApproveBrowserHandoff(ctx context.Context, actor, product, instance string, in BrowserApproval) (BrowserApprovalReceipt, error) {
	c, e := s.BrowserClient(in.ClientID, "")
	if e != nil || c.ProductID != product || productusers.Instance(c.InstanceID) != productusers.Instance(instance) {
		return BrowserApprovalReceipt{}, accounts.ErrForbidden
	}
	u, e := s.VerifyActor(ctx, actor, product, instance)
	if e != nil {
		return BrowserApprovalReceipt{}, e
	}
	var binding string
	var version int64
	e = s.pool.QueryRow(ctx, `SELECT a.binding_id,a.auth_version FROM product_auth_assertions a WHERE token_hash=$1 AND expires_at>now()`, digest(actor)).Scan(&binding, &version)
	if e != nil {
		return BrowserApprovalReceipt{}, accounts.ErrForbidden
	}
	row, e := s.pool.Exec(ctx, `UPDATE product_browser_handoffs SET approved_user_id=$4,source_binding_id=$5,source_auth_version=$6 WHERE id_hash=$1 AND client_id=$2 AND state_hash=$3 AND expires_at>now() AND NOT consumed AND (approved_user_id IS NULL OR approved_user_id=$4)`, digest(in.HandoffID), in.ClientID, digest(in.State), u.ID, binding, version)
	if e != nil {
		return BrowserApprovalReceipt{}, e
	}
	if row.RowsAffected() != 1 {
		return BrowserApprovalReceipt{}, accounts.ErrForbidden
	}
	return BrowserApprovalReceipt{browserRedirect(s.issuer+"/auth/product/finish", url.Values{"handoff": {in.HandoffID}, "state": {in.State}})}, nil
}
func (s *Service) ensureBrokerAlias(ctx context.Context, u accounts.User) (auth.Claims, error) {
	issuer := s.issuer + "/product-broker"
	if _, e := s.pool.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$2) ON CONFLICT DO NOTHING`, issuer, u.ID); e != nil {
		return auth.Claims{}, e
	}
	var matched string
	if e := s.pool.QueryRow(ctx, `SELECT user_id FROM identities WHERE issuer=$1 AND subject=$2`, issuer, u.ID).Scan(&matched); e != nil || matched != u.ID {
		return auth.Claims{}, accounts.ErrConflict
	}
	return auth.Claims{Issuer: issuer, Subject: u.ID, Name: u.Name, Email: u.Email, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}
func (s *Service) FinishBrowserHandoff(ctx context.Context, id, state, browser string) (BrowserCompletion, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return BrowserCompletion{}, e
	}
	defer database.Rollback(tx)
	var client, user, binding string
	var version int64
	e = tx.QueryRow(ctx, `UPDATE product_browser_handoffs h SET consumed=true FROM product_auth_subjects p,product_runtime_bindings b,product_user_access a,users u WHERE h.id_hash=$1 AND h.state_hash=$2 AND h.browser_hash=$3 AND h.expires_at>now() AND NOT h.consumed AND h.approved_user_id IS NOT NULL AND h.approved_user_id=b.user_id AND p.binding_id=h.source_binding_id AND p.auth_version=h.source_auth_version AND NOT p.deleted AND b.id=p.binding_id AND a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id AND a.active AND u.id=b.user_id AND u.active RETURNING h.client_id,h.approved_user_id,h.source_binding_id,h.source_auth_version`, digest(id), digest(state), digest(browser)).Scan(&client, &user, &binding, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		return BrowserCompletion{}, accounts.ErrForbidden
	}
	if e != nil {
		return BrowserCompletion{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return BrowserCompletion{}, e
	}
	c, e := s.BrowserClient(client, "")
	if e != nil {
		return BrowserCompletion{}, e
	}
	u, e := s.users.RuntimeUser(ctx, user)
	if e != nil {
		return BrowserCompletion{}, e
	}
	claims, e := s.ensureBrokerAlias(ctx, u)
	if e != nil {
		return BrowserCompletion{}, e
	}
	return BrowserCompletion{u, claims, binding, version, browserRedirect(c.RedirectURIs[0], url.Values{"handoff_done": {"1"}, "state": {state}})}, nil
}
func (s *Service) BrowserSessionAllowed(ctx context.Context, sessionHash string) bool {
	var source bool
	var allowed bool
	e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_browser_session_origins WHERE session_id_hash=$1),EXISTS(SELECT 1 FROM product_browser_session_origins o JOIN product_auth_subjects p ON p.binding_id=o.binding_id JOIN product_runtime_bindings b ON b.id=p.binding_id JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id JOIN users u ON u.id=b.user_id WHERE o.session_id_hash=$1 AND o.auth_version=p.auth_version AND NOT p.deleted AND a.active AND u.active)`, sessionHash).Scan(&source, &allowed)
	return e == nil && (!source || allowed)
}

func currentBrowserSourceSession(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, sessionHash, expectedUser string) (time.Time, error) {
	var expiry time.Time
	err := q.QueryRow(ctx, `SELECT s.expires_at
		FROM browser_sessions s
		JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject
		JOIN users u ON u.id=i.user_id
		WHERE s.id_hash=$1 AND i.user_id=$2 AND s.expires_at>now() AND u.active
		  AND s.created_at>=u.session_not_before
		  AND NOT EXISTS (SELECT 1 FROM identity_revocations r WHERE r.issuer=s.issuer AND r.subject=s.subject AND r.pending)`, sessionHash, expectedUser).Scan(&expiry)
	return expiry, err
}

func newBrowserSourceProofToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "ubsp_" + hex.EncodeToString(b[:])
}

func (s *Service) VerifyBrowserSourceProof(ctx context.Context, raw, product, instance, expectedClientID, centralUserID string) error {
	if len(raw) != len("ubsp_")+64 || !strings.HasPrefix(raw, "ubsp_") || expectedClientID == "" || centralUserID == "" {
		return accounts.ErrForbidden
	}
	if _, err := hex.DecodeString(raw[len("ubsp_"):]); err != nil || strings.ToLower(raw) != raw {
		return accounts.ErrForbidden
	}
	var storedClient, storedUser string
	var binding *string
	var version *int64
	err := s.pool.QueryRow(ctx, `SELECT f.client_id,f.user_id,f.source_binding_id,f.source_auth_version
		FROM product_browser_source_proofs f
		JOIN browser_sessions s ON s.id_hash=f.source_session_hash
		JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject AND i.user_id=f.user_id
		JOIN users u ON u.id=i.user_id
		WHERE f.token_hash=$1 AND f.expires_at>now() AND f.product_id=$2 AND f.instance_id=$3
		  AND f.user_id=$4 AND s.expires_at>now() AND u.active AND s.created_at>=u.session_not_before
		  AND NOT EXISTS (SELECT 1 FROM identity_revocations r WHERE r.issuer=s.issuer AND r.subject=s.subject AND r.pending)
		  AND (f.source_binding_id IS NULL OR EXISTS (
		    SELECT 1 FROM product_auth_subjects p
		    JOIN product_runtime_bindings b ON b.id=p.binding_id
		    JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id
		    JOIN users source_user ON source_user.id=b.user_id
		    WHERE p.binding_id=f.source_binding_id AND p.auth_version=f.source_auth_version AND NOT p.deleted
		      AND source_user.active AND a.active AND b.user_id=f.user_id))`, browserSessionDigest(raw), product, instance, centralUserID).Scan(&storedClient, &storedUser, &binding, &version)
	if err != nil {
		return accounts.ErrForbidden
	}
	c, err := s.BrowserClient(storedClient, "")
	if err != nil || storedClient != expectedClientID || storedUser != centralUserID || c.ClientID != expectedClientID || c.ProductID != product || c.InstanceID != instance {
		return accounts.ErrForbidden
	}
	if binding != nil && (version == nil || !s.browserSourceAllowed(ctx, *binding, *version)) {
		return accounts.ErrForbidden
	}
	u, err := s.users.RuntimeUser(ctx, centralUserID)
	if err != nil {
		return accounts.ErrForbidden
	}
	access, err := s.users.Resolve(ctx, u, product, instance)
	if err != nil || !slices.Contains(access.Permissions, "product:use") {
		return accounts.ErrForbidden
	}
	return nil
}
func (s *Service) AuthorizeBrowser(ctx context.Context, claims auth.Claims, client, redirect, state, nonce, challenge, sessionHash string) (string, error) {
	c, e := s.BrowserClient(client, redirect)
	if e != nil || !validBrowserState(state) || !validBrowserState(nonce) || len(challenge) != 43 {
		return "", accounts.ErrInvalid
	}
	u, e := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: claims.Issuer, Subject: claims.Subject, Name: claims.Name, Email: claims.Email})
	if e != nil {
		return "", e
	}
	if _, e = currentBrowserSourceSession(ctx, s.pool, sessionHash, u.ID); e != nil {
		return "", accounts.ErrForbidden
	}
	access, e := s.users.Resolve(ctx, u, c.ProductID, c.InstanceID)
	if e != nil || !slices.Contains(access.Permissions, "product:use") {
		return "", accounts.ErrForbidden
	}
	var sourceBinding *string
	var sourceVersion *int64
	e = s.pool.QueryRow(ctx, `SELECT binding_id,auth_version FROM product_browser_session_origins WHERE session_id_hash=$1`, sessionHash).Scan(&sourceBinding, &sourceVersion)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if sourceBinding != nil && !s.browserSourceAllowed(ctx, *sourceBinding, *sourceVersion) {
		return "", accounts.ErrForbidden
	}
	code := randomToken()
	_, e = s.pool.Exec(ctx, `INSERT INTO product_browser_codes(code_hash,client_id,user_id,pkce_challenge,nonce_hash,source_binding_id,source_auth_version,source_session_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()+interval '120 seconds')`, digest(code), client, u.ID, challenge, digest(nonce), sourceBinding, sourceVersion, sessionHash)
	if e != nil {
		return "", e
	}
	return browserRedirect(redirect, url.Values{"broker": {"1"}, "code": {code}, "state": {state}}), nil
}
func (s *Service) ExchangeBrowser(ctx context.Context, product, instance string, in BrowserExchange) (BrowserGrant, error) {
	c, e := s.BrowserClient(in.ClientID, in.RedirectURI)
	if e != nil || c.ProductID != product || productusers.Instance(c.InstanceID) != productusers.Instance(instance) || len(in.Verifier) < 43 || len(in.Verifier) > 128 {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	sum := sha256.Sum256([]byte(in.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	var user string
	var sourceSessionHash *string
	var sourceBinding *string
	var sourceVersion *int64
	var sourceExpires *time.Time
	e = s.pool.QueryRow(ctx, `UPDATE product_browser_codes c SET consumed=true
		WHERE c.code_hash=$1 AND c.client_id=$2 AND c.pkce_challenge=$3 AND c.nonce_hash=$4
		  AND c.expires_at>now() AND NOT c.consumed
		  AND (c.source_session_hash IS NULL OR EXISTS (
		    SELECT 1 FROM browser_sessions s JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject
		    JOIN users u ON u.id=i.user_id
		    WHERE s.id_hash=c.source_session_hash AND c.user_id=i.user_id AND s.expires_at>now() AND u.active
		      AND s.created_at>=u.session_not_before
		      AND NOT EXISTS (SELECT 1 FROM identity_revocations r WHERE r.issuer=s.issuer AND r.subject=s.subject AND r.pending)))
		RETURNING c.user_id,c.source_binding_id,c.source_auth_version,c.source_session_hash,
		  (SELECT s.expires_at FROM browser_sessions s WHERE s.id_hash=c.source_session_hash)`, digest(in.Code), in.ClientID, challenge, digest(in.Nonce)).Scan(&user, &sourceBinding, &sourceVersion, &sourceSessionHash, &sourceExpires)
	if e != nil {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	if sourceBinding != nil && (sourceVersion == nil || !s.browserSourceAllowed(ctx, *sourceBinding, *sourceVersion)) {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	u, e := s.users.RuntimeUser(ctx, user)
	if e != nil {
		return BrowserGrant{}, e
	}
	access, e := s.users.Resolve(ctx, u, product, instance)
	if e != nil {
		return BrowserGrant{}, e
	}
	if !slices.Contains(access.Permissions, "product:use") {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	proof := "ubp_" + strings.TrimPrefix(randomToken(), "upa_")
	expires := time.Now().Add(120 * time.Second)
	var sourceProof string
	var sourceProofExpires time.Time
	if sourceSessionHash != nil && sourceExpires != nil {
		sourceProof = newBrowserSourceProofToken()
		sourceProofExpires = time.Now().Add(time.Hour)
		if sourceExpires.Before(sourceProofExpires) {
			sourceProofExpires = *sourceExpires
		}
	}
	if _, e = s.pool.Exec(ctx, `INSERT INTO product_browser_proofs(token_hash,client_id,user_id,expires_at,source_binding_id,source_auth_version) VALUES($1,$2,$3,$4,$5,$6)`, digest(proof), in.ClientID, user, expires, sourceBinding, sourceVersion); e != nil {
		return BrowserGrant{}, e
	}
	if sourceProof != "" {
		if _, e = s.pool.Exec(ctx, `INSERT INTO product_browser_source_proofs(token_hash,client_id,user_id,source_session_hash,source_binding_id,source_auth_version,product_id,instance_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, browserSessionDigest(sourceProof), in.ClientID, user, *sourceSessionHash, sourceBinding, sourceVersion, product, productusers.Instance(instance), sourceProofExpires); e != nil {
			return BrowserGrant{}, e
		}
	}
	access.ExpiresAt = expires.Unix()
	grant := BrowserGrant{Access: access, ProofToken: proof, ExpiresAt: expires.Unix()}
	if sourceProof != "" {
		grant.SourceProofToken = sourceProof
		grant.SourceProofExpiresAt = sourceProofExpires.Unix()
	}
	return grant, nil
}
func (s *Service) VerifyBrowserProof(ctx context.Context, raw, product, instance string) (accounts.User, auth.Claims, error) {
	if !strings.HasPrefix(raw, "ubp_") {
		return accounts.User{}, auth.Claims{}, accounts.ErrForbidden
	}
	var client, user string
	var sourceBinding *string
	var sourceVersion *int64
	e := s.pool.QueryRow(ctx, `SELECT client_id,user_id,source_binding_id,source_auth_version FROM product_browser_proofs WHERE token_hash=$1 AND expires_at>now() AND NOT consumed`, digest(raw)).Scan(&client, &user, &sourceBinding, &sourceVersion)
	if e != nil {
		return accounts.User{}, auth.Claims{}, accounts.ErrForbidden
	}
	if sourceBinding != nil && (sourceVersion == nil || !s.browserSourceAllowed(ctx, *sourceBinding, *sourceVersion)) {
		return accounts.User{}, auth.Claims{}, accounts.ErrForbidden
	}
	c, e := s.BrowserClient(client, "")
	if e != nil || c.ProductID != product || productusers.Instance(c.InstanceID) != productusers.Instance(instance) {
		return accounts.User{}, auth.Claims{}, accounts.ErrForbidden
	}
	u, e := s.users.RuntimeUser(ctx, user)
	if e != nil {
		return u, auth.Claims{}, e
	}
	if _, e = s.users.Resolve(ctx, u, product, instance); e != nil {
		return u, auth.Claims{}, e
	}
	claims, e := s.ensureBrokerAlias(ctx, u)
	return u, claims, e
}
func (s *Service) ConsumeBrowserProof(ctx context.Context, raw string) error {
	row, e := s.pool.Exec(ctx, `UPDATE product_browser_proofs f SET consumed=true WHERE token_hash=$1 AND NOT consumed AND expires_at>now() AND (source_binding_id IS NULL OR EXISTS(SELECT 1 FROM product_auth_subjects p JOIN product_runtime_bindings b ON b.id=p.binding_id JOIN users u ON u.id=b.user_id JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id WHERE p.binding_id=f.source_binding_id AND p.auth_version=f.source_auth_version AND NOT p.deleted AND u.active AND a.active))`, digest(raw))
	if e != nil {
		return e
	}
	if row.RowsAffected() != 1 {
		return accounts.ErrForbidden
	}
	return nil
}

func (s *Service) browserSourceAllowed(ctx context.Context, binding string, version int64) bool {
	var product, instance, local string
	if e := s.pool.QueryRow(ctx, `SELECT product_id,instance_id,local_user_id FROM product_runtime_bindings WHERE id=$1`, binding).Scan(&product, &instance, &local); e != nil {
		return false
	}
	out, e := s.Lookup(ctx, product, instance, local)
	return e == nil && out.AuthVersion == version && out.Access.Active && slices.Contains(out.Access.Permissions, "product:use")
}
