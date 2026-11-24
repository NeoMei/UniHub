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
	ClientID                string `json:"client_id"`
	HandoffID               string `json:"handoff_id"`
	State                   string `json:"state"`
	NativeSessionID         string `json:"native_session_id,omitempty"`
	NativeSessionExpiresAt  int64  `json:"native_session_expires_at,omitempty"`
	NativeHandoffGeneration string `json:"native_handoff_generation,omitempty"`
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
	ClientID               string
	PilotStartedAt         *time.Time
	User                   accounts.User
	Claims                 auth.Claims
	BindingID              string
	AuthVersion            int64
	NativeSessionID        string
	NativeSessionExpiresAt *time.Time
	RedirectURI            string
}

func (s *Service) ConfigureBrowserClients(clients map[string]products.Client) {
	s.browserClients = map[string]products.Client{}
	for id, c := range clients {
		s.browserClients[id] = c
	}
}

// BrowserRegistryOverlay serves administrator-managed native-identity clients.
type BrowserRegistryOverlay interface {
	BrowserClient(context.Context, accounts.Querier, string, bool) (products.Client, bool)
	PublicBrowserClients(context.Context) map[string]products.Client
	AcceptBrowserWork(context.Context, accounts.Querier, string, *time.Time, BrowserReceipt) error
}

type BrowserReceipt int

const (
	CheckBrowserWork BrowserReceipt = iota
	RecordInbound
	RecordOutbound
)

func (s *Service) acceptBrowserWork(ctx context.Context, q accounts.Querier, id string, pilot *time.Time, receipt BrowserReceipt) error {
	if s.browserOverlay == nil {
		return nil
	}
	return s.browserOverlay.AcceptBrowserWork(ctx, q, id, pilot, receipt)
}

func (s *Service) ConfigureBrowserRegistryOverlay(overlay BrowserRegistryOverlay) {
	s.browserOverlay = overlay
}

func (s *Service) BrowserClient(id, redirect string) (products.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.browserClientOn(ctx, s.pool, id, redirect, false)
}
func (s *Service) browserClientOn(ctx context.Context, q accounts.Querier, id, redirect string, includePaused bool) (products.Client, error) {
	c, ok := s.browserClients[id]
	if ok {
		ok = s.users.ManagedOn(ctx, q, c.ProductID, c.InstanceID)
	} else if s.browserOverlay != nil {
		c, ok = s.browserOverlay.BrowserClient(ctx, q, id, includePaused)
	}
	if !ok || !c.BrowserSSO || redirect != "" && !slices.Contains(c.RedirectURIs, redirect) {
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

func allowsBrowserHandoff(c products.Client, nativeSession, generation string) bool {
	if c.NativeHandoffGeneration == "" {
		return nativeSession == "" && generation == ""
	}
	return c.NativeHandoffGeneration != "disabled" && nativeSession != "" && generation == c.NativeHandoffGeneration
}

func (s *Service) BeginBrowserHandoff(ctx context.Context, client, state, browser string) (string, error) {
	c, e := s.BrowserClient(client, "")
	if e != nil || c.NativeHandoffGeneration == "disabled" || !validBrowserState(state) || len(browser) < 32 {
		return "", accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer database.Rollback(tx)
	if e = s.acceptBrowserWork(ctx, tx, client, c.PilotStartedAt, CheckBrowserWork); e != nil {
		return "", e
	}
	id := randomToken()
	_, e = tx.Exec(ctx, `INSERT INTO product_browser_handoffs(id_hash,client_id,state_hash,browser_hash,expires_at,application_pilot_at)
		VALUES($1,$2,$3,$4,now()+$5::interval,$6)`, digest(id), client, digest(state), digest(browser), nativeHandoffLifetime, c.PilotStartedAt)
	if e != nil {
		return "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}

	return browserRedirect(c.RedirectURIs[0], url.Values{"handoff": {id}, "state": {state}}), nil
}
func (s *Service) ApproveBrowserHandoff(ctx context.Context, actor, product, instance string, in BrowserApproval) (BrowserApprovalReceipt, error) {
	var expiry *time.Time
	if in.NativeSessionID != "" {
		now := time.Now()
		if !nativeBrowserSessionID.MatchString(in.NativeSessionID) ||
			in.NativeSessionExpiresAt <= now.Unix() || !validNativeSessionExpiry(in.NativeSessionExpiresAt, now) {
			return BrowserApprovalReceipt{}, accounts.ErrInvalid
		}
		deadline := time.Unix(in.NativeSessionExpiresAt, 0)
		expiry = &deadline
	} else if in.NativeSessionExpiresAt != 0 {
		return BrowserApprovalReceipt{}, accounts.ErrInvalid
	}
	c, e := s.BrowserClient(in.ClientID, "")
	if e != nil || c.ProductID != product || productusers.Instance(c.InstanceID) != productusers.Instance(instance) {
		return BrowserApprovalReceipt{}, accounts.ErrForbidden
	}
	if !allowsBrowserHandoff(c, in.NativeSessionID, in.NativeHandoffGeneration) {
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
	tx, e := s.beginRuntimeMutation(ctx, pgx.TxOptions{})
	if e != nil {
		return BrowserApprovalReceipt{}, e
	}
	defer database.Rollback(tx)
	if e = s.acceptBrowserWork(ctx, tx, c.ClientID, c.PilotStartedAt, CheckBrowserWork); e != nil {
		return BrowserApprovalReceipt{}, e
	}

	row, e := tx.Exec(ctx, `UPDATE product_browser_handoffs h
		SET approved_user_id=$4,source_binding_id=$5,source_auth_version=$6,
		  native_session_id=NULLIF($7,''),native_session_expires_at=$8,native_handoff_generation=$9
		WHERE h.id_hash=$1 AND h.client_id=$2 AND h.state_hash=$3 AND h.expires_at>now() AND NOT h.consumed
		  AND h.application_pilot_at IS NOT DISTINCT FROM $10::timestamptz
		  AND ($8::timestamptz IS NULL OR $8>clock_timestamp())
		  AND (h.approved_user_id IS NULL OR (h.approved_user_id=$4 AND h.native_session_id IS NOT DISTINCT FROM NULLIF($7,'')
		    AND h.native_session_expires_at IS NOT DISTINCT FROM $8 AND h.native_handoff_generation=$9))
		  AND NOT EXISTS (SELECT 1 FROM product_browser_native_session_revocations r
		    WHERE r.binding_id=$5 AND r.native_session_id=$7 AND r.expires_at>clock_timestamp())`,
		digest(in.HandoffID), in.ClientID, digest(in.State), u.ID, binding, version, in.NativeSessionID, expiry, in.NativeHandoffGeneration, c.PilotStartedAt)
	if e != nil {
		return BrowserApprovalReceipt{}, e
	}
	if row.RowsAffected() != 1 {
		return BrowserApprovalReceipt{}, accounts.ErrForbidden
	}
	if e = tx.Commit(ctx); e != nil {
		return BrowserApprovalReceipt{}, e
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
	var client, user, binding, nativeSession, generation string
	var version int64
	var nativeExpiry *time.Time
	var pilot *time.Time
	e = tx.QueryRow(ctx, `SELECT client_id,application_pilot_at FROM product_browser_handoffs
		WHERE id_hash=$1 AND state_hash=$2 AND browser_hash=$3 AND expires_at>now() AND NOT consumed`,
		digest(id), digest(state), digest(browser)).Scan(&client, &pilot)
	if errors.Is(e, pgx.ErrNoRows) {
		return BrowserCompletion{}, accounts.ErrForbidden
	}
	if e != nil {
		return BrowserCompletion{}, e
	}
	if e = s.acceptBrowserWork(ctx, tx, client, pilot, CheckBrowserWork); e != nil {
		return BrowserCompletion{}, e
	}

	e = tx.QueryRow(ctx, `UPDATE product_browser_handoffs h SET consumed=true
		FROM product_auth_subjects p,product_runtime_bindings b,product_user_access a,users u
		WHERE h.id_hash=$1 AND h.state_hash=$2 AND h.browser_hash=$3 AND h.expires_at>now()
		  AND NOT h.consumed AND h.approved_user_id=b.user_id AND p.binding_id=h.source_binding_id
		  AND p.auth_version=h.source_auth_version AND NOT p.deleted AND NOT p.native_locked
		  AND b.id=p.binding_id AND a.user_id=b.user_id AND a.product_id=b.product_id
		  AND a.instance_id=b.instance_id AND a.active AND u.id=b.user_id AND u.active
		  AND (h.native_session_id IS NULL OR h.native_session_expires_at>clock_timestamp())
		  AND NOT EXISTS (SELECT 1 FROM product_browser_native_session_revocations r
		    WHERE r.binding_id=b.id AND r.native_session_id=h.native_session_id AND r.expires_at>clock_timestamp())
		RETURNING h.approved_user_id,h.source_binding_id,h.source_auth_version,
		  COALESCE(h.native_session_id,''),h.native_session_expires_at,h.native_handoff_generation`,
		digest(id), digest(state), digest(browser)).Scan(&user, &binding, &version, &nativeSession, &nativeExpiry, &generation)
	if errors.Is(e, pgx.ErrNoRows) {
		return BrowserCompletion{}, accounts.ErrForbidden
	}
	if e != nil {
		return BrowserCompletion{}, e
	}
	c, e := s.browserClientOn(ctx, tx, client, "", false)
	if e != nil {
		return BrowserCompletion{}, e
	}
	if !allowsBrowserHandoff(c, nativeSession, generation) {
		return BrowserCompletion{}, accounts.ErrForbidden
	}
	if e = tx.Commit(ctx); e != nil {
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
	return BrowserCompletion{
		ClientID: client, PilotStartedAt: pilot, User: u, Claims: claims, BindingID: binding, AuthVersion: version,
		NativeSessionID: nativeSession, NativeSessionExpiresAt: nativeExpiry,
		RedirectURI: browserRedirect(c.RedirectURIs[0], url.Values{"handoff_done": {"1"}, "state": {state}}),
	}, nil
}
func (s *Service) BrowserSessionAllowed(ctx context.Context, sessionHash string) bool {
	var user, product, instance string
	var current bool
	err := s.pool.QueryRow(ctx, `SELECT b.user_id,b.product_id,b.instance_id,
		o.auth_version=p.auth_version AND NOT p.deleted AND NOT p.native_locked AND u.active AND COALESCE(a.active,false)
		FROM product_browser_session_origins o
		JOIN product_auth_subjects p ON p.binding_id=o.binding_id
		JOIN product_runtime_bindings b ON b.id=p.binding_id
		JOIN users u ON u.id=b.user_id
		LEFT JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id
		WHERE o.session_id_hash=$1`, sessionHash).Scan(&user, &product, &instance, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	return err == nil && current && s.users.Managed(product, instance) &&
		s.users.RequirePermissionOn(ctx, s.pool, user, product, instance, "product:use") == nil
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
		    WHERE p.binding_id=f.source_binding_id AND p.auth_version=f.source_auth_version AND NOT p.deleted AND NOT p.native_locked
		      AND source_user.active AND a.active AND b.user_id=f.user_id))`, browserSessionDigest(raw), product, instance, centralUserID).Scan(&storedClient, &storedUser, &binding, &version)
	if err != nil {
		return accounts.ErrForbidden
	}
	c, err := s.BrowserClient(storedClient, "")
	if err != nil || storedClient != expectedClientID || storedUser != centralUserID || c.ClientID != expectedClientID || c.ProductID != product || c.InstanceID != instance {
		return accounts.ErrForbidden
	}
	if binding != nil && (version == nil || !s.browserSourceAllowed(ctx, s.pool, *binding, *version)) {
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
	var sourceBinding *string
	var sourceVersion *int64
	e = s.pool.QueryRow(ctx, `SELECT binding_id,auth_version FROM product_browser_session_origins WHERE session_id_hash=$1`, sessionHash).Scan(&sourceBinding, &sourceVersion)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if sourceBinding != nil && (sourceVersion == nil || !s.browserSourceAllowed(ctx, s.pool, *sourceBinding, *sourceVersion)) {
		return "", accounts.ErrForbidden
	}
	code := randomToken()
	if c.IdentityLinkRole != "" {
		// Evaluate the permission and capture the destination versions from
		// one snapshot. Later changes cannot be adopted by this code.
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return "", err
		}
		defer database.Rollback(tx)
		if err = s.acceptBrowserWork(ctx, tx, c.ClientID, c.PilotStartedAt, CheckBrowserWork); err != nil {
			return "", err
		}

		if err = s.users.RequirePermissionOn(ctx, tx, u.ID, c.ProductID, c.InstanceID, "product:use"); err != nil {
			return "", err
		}
		result, err := tx.Exec(ctx, `INSERT INTO product_browser_codes(
			code_hash,client_id,user_id,pkce_challenge,nonce_hash,source_binding_id,source_auth_version,source_session_hash,expires_at,
			destination_binding_id,destination_assignment_version,destination_status_version,destination_auth_version,application_pilot_at)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,now()+interval '120 seconds',b.id,a.version,u.status_version,p.auth_version,$11
			FROM product_runtime_bindings b JOIN product_auth_subjects p ON p.binding_id=b.id
			JOIN product_user_access a USING(user_id,product_id,instance_id)
			JOIN users u ON u.id=b.user_id
			WHERE b.user_id=$3 AND b.product_id=$9 AND b.instance_id=$10
			  AND u.active AND a.active AND NOT p.deleted AND NOT p.native_locked`,
			digest(code), client, u.ID, challenge, digest(nonce), sourceBinding, sourceVersion, sessionHash,
			c.ProductID, productusers.Instance(c.InstanceID), c.PilotStartedAt)
		if err != nil {
			return "", err
		}
		if result.RowsAffected() != 1 {
			return "", accounts.ErrForbidden
		}
		if err = tx.Commit(ctx); err != nil {
			return "", err
		}
	} else {
		access, err := s.users.Resolve(ctx, u, c.ProductID, c.InstanceID)
		if err != nil || !slices.Contains(access.Permissions, "product:use") {
			return "", accounts.ErrForbidden
		}
		_, err = s.pool.Exec(ctx, `INSERT INTO product_browser_codes
			(code_hash,client_id,user_id,pkce_challenge,nonce_hash,source_binding_id,source_auth_version,source_session_hash,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()+interval '120 seconds')`,
			digest(code), client, u.ID, challenge, digest(nonce), sourceBinding, sourceVersion, sessionHash)
		if err != nil {
			return "", err
		}
	}
	return browserRedirect(redirect, url.Values{"broker": {"1"}, "code": {code}, "state": {state}}), nil
}
func (s *Service) ExchangeBrowser(ctx context.Context, product, instance string, in BrowserExchange) (BrowserGrant, error) {
	client, e := s.BrowserClient(in.ClientID, in.RedirectURI)
	if e != nil {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	tx, e := s.beginRuntimeMutation(ctx, pgx.TxOptions{})
	if e != nil {
		return BrowserGrant{}, e
	}
	defer database.Rollback(tx)
	grant, e := s.exchangeBrowserOn(ctx, tx, client, product, instance, in)
	if e != nil {
		return BrowserGrant{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return BrowserGrant{}, e
	}
	return grant, nil
}

func (s *Service) exchangeBrowserOn(ctx context.Context, tx pgx.Tx, client products.Client, product, instance string, in BrowserExchange) (BrowserGrant, error) {
	if client.ProductID != product || productusers.Instance(client.InstanceID) != productusers.Instance(instance) || len(in.Verifier) < 43 || len(in.Verifier) > 128 {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	if e := s.acceptBrowserWork(ctx, tx, in.ClientID, client.PilotStartedAt, CheckBrowserWork); e != nil {
		return BrowserGrant{}, e
	}
	sum := sha256.Sum256([]byte(in.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	var user string
	var sourceSessionHash *string
	var sourceBinding *string
	var sourceVersion *int64
	var sourceExpires *time.Time
	e := tx.QueryRow(ctx, `UPDATE product_browser_codes c SET consumed=true
		WHERE c.code_hash=$1 AND c.client_id=$2 AND c.pkce_challenge=$3 AND c.nonce_hash=$4
		  AND c.expires_at>now() AND NOT c.consumed
		  AND c.application_pilot_at IS NOT DISTINCT FROM $8::timestamptz
		  AND ((NOT $5::boolean AND c.destination_binding_id IS NULL) OR
		    ($5 AND EXISTS (`+currentIdentityBrowserCodeDestination+`
		      AND b.product_id=$6 AND b.instance_id=$7)))
		  AND (c.source_session_hash IS NULL OR EXISTS (
		    SELECT 1 FROM browser_sessions s JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject
		    JOIN users u ON u.id=i.user_id
		    WHERE s.id_hash=c.source_session_hash AND c.user_id=i.user_id AND s.expires_at>now() AND u.active
		      AND s.created_at>=u.session_not_before
		      AND NOT EXISTS (SELECT 1 FROM identity_revocations r WHERE r.issuer=s.issuer AND r.subject=s.subject AND r.pending)))
		RETURNING c.user_id,c.source_binding_id,c.source_auth_version,c.source_session_hash,
		  (SELECT s.expires_at FROM browser_sessions s WHERE s.id_hash=c.source_session_hash)`, digest(in.Code), in.ClientID, challenge, digest(in.Nonce),
		client.IdentityLinkRole != "", product, productusers.Instance(instance), client.PilotStartedAt).Scan(&user, &sourceBinding, &sourceVersion, &sourceSessionHash, &sourceExpires)
	if e != nil {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	if sourceBinding != nil && (sourceVersion == nil || !s.browserSourceAllowed(ctx, tx, *sourceBinding, *sourceVersion)) {
		return BrowserGrant{}, accounts.ErrForbidden
	}
	u, e := runtimeUserOn(ctx, tx, user)
	if e != nil {
		return BrowserGrant{}, e
	}
	access, e := s.users.ResolveOn(ctx, tx, u, product, instance)
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
	if _, e = tx.Exec(ctx, `INSERT INTO product_browser_proofs(token_hash,client_id,user_id,expires_at,source_binding_id,source_auth_version) VALUES($1,$2,$3,$4,$5,$6)`, digest(proof), in.ClientID, user, expires, sourceBinding, sourceVersion); e != nil {
		return BrowserGrant{}, e
	}
	if sourceProof != "" {
		if _, e = tx.Exec(ctx, `INSERT INTO product_browser_source_proofs(token_hash,client_id,user_id,source_session_hash,source_binding_id,source_auth_version,product_id,instance_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, browserSessionDigest(sourceProof), in.ClientID, user, *sourceSessionHash, sourceBinding, sourceVersion, product, productusers.Instance(instance), sourceProofExpires); e != nil {
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
	if sourceBinding != nil && (sourceVersion == nil || !s.browserSourceAllowed(ctx, s.pool, *sourceBinding, *sourceVersion)) {
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
	return consumeBrowserProofOn(ctx, s.pool, raw)
}

func consumeBrowserProofOn(ctx context.Context, q accounts.Querier, raw string) error {
	var consumed bool
	err := q.QueryRow(ctx, `UPDATE product_browser_proofs f SET consumed=true WHERE token_hash=$1 AND NOT consumed AND expires_at>now() AND (source_binding_id IS NULL OR EXISTS(SELECT 1 FROM product_auth_subjects p JOIN product_runtime_bindings b ON b.id=p.binding_id JOIN users u ON u.id=b.user_id JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id WHERE p.binding_id=f.source_binding_id AND p.auth_version=f.source_auth_version AND NOT p.deleted AND NOT p.native_locked AND u.active AND a.active)) RETURNING consumed`, digest(raw)).Scan(&consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounts.ErrForbidden
	}
	return err
}

func (s *Service) browserSourceAllowed(ctx context.Context, q accounts.Querier, binding string, version int64) bool {
	var product, instance, user string
	if e := q.QueryRow(ctx, `SELECT b.product_id,b.instance_id,b.user_id FROM product_runtime_bindings b
		JOIN product_auth_subjects p ON p.binding_id=b.id
		WHERE b.id=$1 AND p.auth_version=$2 AND NOT p.deleted AND NOT p.native_locked`, binding, version).Scan(&product, &instance, &user); e != nil {
		return false
	}
	access, e := s.users.ResolveOn(ctx, q, accounts.User{ID: user}, product, instance)
	return e == nil && access.Active && slices.Contains(access.Permissions, "product:use")
}
