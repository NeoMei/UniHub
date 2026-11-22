package productauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

type BeginIdentityLink struct {
	ClientID       string         `json:"client_id"`
	State          string         `json:"state"`
	Nonce          string         `json:"nonce"`
	Challenge      string         `json:"code_challenge"`
	Authentication Authentication `json:"authentication"`
}

type IdentityLinkStart struct {
	LinkID       string `json:"link_id"`
	AuthorizeURL string `json:"authorize_url"`
}

type CompleteIdentityLink struct {
	ClientID       string         `json:"client_id"`
	LinkID         string         `json:"link_id"`
	Code           string         `json:"code"`
	RedirectURI    string         `json:"redirect_uri"`
	Nonce          string         `json:"nonce"`
	Verifier       string         `json:"code_verifier"`
	CentralUserID  string         `json:"central_user_id,omitempty"`
	Authentication Authentication `json:"authentication"`
}

type IdentityLinkPreview struct {
	CentralUserID string `json:"central_user_id"`
	DisplayName   string `json:"display_name"`
	MaskedEmail   string `json:"masked_email"`
}

func (s *Service) identityLinkClient(id, redirect string) (products.Client, error) {
	client, err := s.BrowserClient(id, redirect)
	if err != nil || client.IdentityLinkRole == "" {
		return products.Client{}, accounts.ErrForbidden
	}
	for _, manifest := range s.users.Products() {
		if manifest.ProductID != client.ProductID || manifest.InstanceID != productusers.Instance(client.InstanceID) {
			continue
		}
		for _, role := range manifest.Roles {
			if role.ID == client.IdentityLinkRole && !role.Privileged && !role.NativeBaseline && role.ProductRole <= 1 &&
				len(role.Permissions) == 1 && role.Permissions[0] == "product:use" {
				return client, nil
			}
		}
	}
	return products.Client{}, accounts.ErrForbidden
}

func (s *Service) BeginIdentityLink(ctx context.Context, product, instance string, in BeginIdentityLink) (IdentityLinkStart, error) {
	instance = productusers.Instance(instance)
	client, err := s.identityLinkClient(in.ClientID, "")
	if err != nil || client.ProductID != product || productusers.Instance(client.InstanceID) != instance {
		return IdentityLinkStart{}, accounts.ErrForbidden
	}
	if validateAuthentication(in.Authentication, time.Now()) != nil || in.Authentication.Method == "session" ||
		!validBrowserState(in.State) || !validBrowserState(in.Nonce) || len(in.Challenge) != 43 {
		return IdentityLinkStart{}, accounts.ErrInvalid
	}

	link := browserSecret()
	result, err := s.pool.Exec(ctx, `INSERT INTO product_identity_links
		(id_hash,client_id,product_id,instance_id,local_user_id,auth_version,native_auth_id,state_hash,nonce_hash,pkce_challenge,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now()+interval '5 minutes') ON CONFLICT DO NOTHING`,
		digest(link), client.ClientID, product, instance, in.Authentication.LocalUserID, in.Authentication.AuthVersion,
		in.Authentication.AuthenticationID, digest(in.State), digest(in.Nonce), in.Challenge)
	if err != nil {
		return IdentityLinkStart{}, err
	}
	if result.RowsAffected() != 1 {
		return IdentityLinkStart{}, accounts.ErrConflict
	}
	destination := browserRedirect(s.issuer+"/auth/product/link", url.Values{
		"client_id": {client.ClientID}, "redirect_uri": {client.RedirectURIs[0]}, "state": {in.State}, "link": {link},
	})
	return IdentityLinkStart{LinkID: link, AuthorizeURL: destination}, nil
}

func (s *Service) AuthorizeIdentityLink(ctx context.Context, clientID, redirect, state, link, sessionHash string) (string, error) {
	client, err := s.identityLinkClient(clientID, redirect)
	if err != nil || redirect == "" || !validBrowserState(state) || len(link) != 43 {
		return "", accounts.ErrForbidden
	}
	code := browserSecret()
	// A fresh central session proves only control of the canonical subject. It
	// provides neither a product grant nor authority to select a local user.
	result, err := s.pool.Exec(ctx, `UPDATE product_identity_links l
		SET code_hash=$1,central_user_id=u.id,source_session_hash=b.id_hash
		FROM browser_sessions b JOIN identities i ON i.issuer=b.issuer AND i.subject=b.subject
		JOIN users u ON u.id=i.user_id
		WHERE l.id_hash=$2 AND l.client_id=$3 AND l.state_hash=$4 AND l.expires_at>now() AND l.code_hash IS NULL
		AND b.id_hash=$5 AND b.expires_at>now() AND b.created_at>=now()-interval '5 minutes'
		AND b.created_at>=u.session_not_before AND u.active
		AND NOT EXISTS (SELECT 1 FROM identity_revocations r WHERE r.issuer=b.issuer AND r.subject=b.subject AND r.pending)`,
		digest(code), digest(link), client.ClientID, digest(state), sessionHash)
	if err != nil {
		return "", err
	}
	if result.RowsAffected() != 1 {
		return "", accounts.ErrForbidden
	}
	return browserRedirect(redirect, url.Values{"link": {"1"}, "code": {code}, "state": {state}}), nil
}

func (s *Service) handleIdentityLink(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	client, redirect, state := query.Get("client_id"), query.Get("redirect_uri"), query.Get("state")
	if _, err := s.identityLinkClient(client, redirect); err != nil || redirect == "" || !validBrowserState(state) {
		http.Error(w, "Invalid account link", http.StatusBadRequest)
		return
	}
	response := url.Values{"link": {"1"}, "state": {state}}
	cookie, err := r.Cookie("unihub_session")
	if err == nil {
		if _, err = s.currentBrowser(r.Context(), cookie.Value); err == nil {
			var destination string
			destination, err = s.AuthorizeIdentityLink(r.Context(), client, redirect, state, query.Get("link"), browserSessionDigest(cookie.Value))
			if err == nil {
				http.Redirect(w, r, destination, http.StatusSeeOther)
				return
			}
		}
	}
	if query.Get("reauth") != "1" && len(query.Get("link")) == 43 {
		var pending bool
		if err = s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM product_identity_links
			WHERE id_hash=$1 AND client_id=$2 AND state_hash=$3 AND code_hash IS NULL AND expires_at>now())`,
			digest(query.Get("link")), client, digest(state)).Scan(&pending); err == nil && pending {
			resume := browserRedirect("/auth/product/link", url.Values{
				"client_id": {client}, "redirect_uri": {redirect}, "state": {state}, "link": {query.Get("link")}, "reauth": {"1"},
			})
			http.Redirect(w, r, browserRedirect("/auth/login", url.Values{
				"method": {"central"}, "product_link_return": {resume},
			}), http.StatusSeeOther)
			return
		}
	}
	response.Set("error", "interaction_required")
	http.Redirect(w, r, browserRedirect(redirect, response), http.StatusSeeOther)
}

func (s *Service) identityLinkExchangeClient(product, instance string, in CompleteIdentityLink) (products.Client, error) {
	instance = productusers.Instance(instance)
	client, err := s.identityLinkClient(in.ClientID, in.RedirectURI)
	if err != nil || client.ProductID != product || productusers.Instance(client.InstanceID) != instance || in.RedirectURI == "" {
		return products.Client{}, accounts.ErrForbidden
	}
	if len(in.LinkID) != 43 || len(in.Code) != 43 || len(in.Verifier) < 43 || len(in.Verifier) > 128 ||
		!validBrowserState(in.Nonce) || validateAuthentication(in.Authentication, time.Now()) != nil {
		return products.Client{}, accounts.ErrInvalid
	}
	return client, nil
}

type identityLinkProof struct {
	central, session string
	completed        *string
}

func readIdentityLinkProof(ctx context.Context, q accounts.Querier, client products.Client, in CompleteIdentityLink, lock bool) (proof identityLinkProof, err error) {
	challenge := sha256.Sum256([]byte(in.Verifier))
	query := `SELECT central_user_id,source_session_hash,completed_digest FROM product_identity_links
		WHERE id_hash=$1 AND client_id=$2 AND product_id=$3 AND instance_id=$4 AND local_user_id=$5
		AND auth_version=$6 AND code_hash=$7 AND nonce_hash=$8 AND pkce_challenge=$9 AND expires_at>now()`
	if lock {
		query += " FOR UPDATE"
	}
	err = q.QueryRow(ctx, query, digest(in.LinkID), client.ClientID, client.ProductID, productusers.Instance(client.InstanceID),
		in.Authentication.LocalUserID, in.Authentication.AuthVersion, digest(in.Code), digest(in.Nonce),
		base64.RawURLEncoding.EncodeToString(challenge[:])).Scan(&proof.central, &proof.session, &proof.completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return proof, accounts.ErrForbidden
	}
	return proof, err
}

func (s *Service) PreviewIdentityLink(ctx context.Context, product, instance string, in CompleteIdentityLink) (IdentityLinkPreview, error) {
	client, err := s.identityLinkExchangeClient(product, instance, in)
	if err != nil {
		return IdentityLinkPreview{}, err
	}
	proof, err := readIdentityLinkProof(ctx, s.pool, client, in, false)
	if err != nil {
		return IdentityLinkPreview{}, err
	}
	if _, err = currentBrowserSourceSession(ctx, s.pool, proof.session, proof.central); err != nil ||
		!s.BrowserSessionAllowed(ctx, proof.session) {
		return IdentityLinkPreview{}, accounts.ErrForbidden
	}
	user, err := s.users.RuntimeUser(ctx, proof.central)
	if err != nil {
		return IdentityLinkPreview{}, err
	}
	maskedEmail := ""
	if local, domain, ok := strings.Cut(user.Email, "@"); ok && local != "" && domain != "" {
		maskedEmail = string([]rune(local)[0]) + "***@" + domain
	}
	return IdentityLinkPreview{CentralUserID: user.ID, DisplayName: user.Name, MaskedEmail: maskedEmail}, nil
}

func (s *Service) CompleteIdentityLink(ctx context.Context, product, instance string, in CompleteIdentityLink) (Identity, error) {
	client, err := s.identityLinkExchangeClient(product, instance, in)
	if err != nil || in.CentralUserID == "" {
		return Identity{}, accounts.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Identity{}, err
	}
	defer database.Rollback(tx)
	proof, err := readIdentityLinkProof(ctx, tx, client, in, true)
	if err != nil {
		return Identity{}, err
	}
	if proof.central != in.CentralUserID {
		return Identity{}, accounts.ErrForbidden
	}
	requestDigest := digest(in)
	if proof.completed != nil && *proof.completed != requestDigest {
		return Identity{}, accounts.ErrConflict
	}
	if proof.completed == nil {
		if err = s.bindLinkedIdentity(ctx, tx, client, proof.central, in.Authentication, digest(in.LinkID), requestDigest); err != nil {
			return Identity{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE product_identity_links SET completed_digest=$2 WHERE id_hash=$1`, digest(in.LinkID), requestDigest); err != nil {
			return Identity{}, err
		}
	}
	// Recheck after waiting for identity and binding locks. Taking shared row
	// locks serializes the final result with logout, lock and disable updates.
	if err = s.lockIdentityLinkSource(ctx, tx, proof); err != nil {
		return Identity{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Identity{}, err
	}
	return s.IdentityLinkByCanonicalUser(ctx, product, instance, proof.central)
}

func (s *Service) lockIdentityLinkSource(ctx context.Context, tx pgx.Tx, proof identityLinkProof) error {
	var row int
	err := tx.QueryRow(ctx, `SELECT 1 FROM browser_sessions b JOIN identities i ON i.issuer=b.issuer AND i.subject=b.subject
		JOIN users u ON u.id=i.user_id
		WHERE b.id_hash=$1 AND u.id=$2 AND b.expires_at>now() AND u.active AND b.created_at>=u.session_not_before
		AND NOT EXISTS (SELECT 1 FROM identity_revocations r WHERE r.issuer=b.issuer AND r.subject=b.subject AND r.pending)
		FOR SHARE OF b,u`, proof.session, proof.central).Scan(&row)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounts.ErrForbidden
	}
	if err != nil {
		return err
	}
	var origin string
	err = tx.QueryRow(ctx, `SELECT binding_id FROM product_browser_session_origins WHERE session_id_hash=$1`, proof.session).Scan(&origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var product, instance string
	err = tx.QueryRow(ctx, `SELECT b.product_id,b.instance_id FROM product_browser_session_origins o
		JOIN product_auth_subjects p ON p.binding_id=o.binding_id
		JOIN product_runtime_bindings b ON b.id=p.binding_id
		JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id
		WHERE o.session_id_hash=$1 AND b.user_id=$2 AND b.id=$3 AND p.auth_version=o.auth_version
		AND NOT p.deleted AND NOT p.native_locked AND a.active FOR SHARE OF p,a`,
		proof.session, proof.central, origin).Scan(&product, &instance)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounts.ErrForbidden
	}
	if err != nil {
		return err
	}
	if !s.users.Managed(product, instance) {
		return accounts.ErrForbidden
	}
	return s.users.RequirePermissionOn(ctx, tx, proof.central, product, instance, "product:use")
}

func (s *Service) bindLinkedIdentity(ctx context.Context, tx pgx.Tx, client products.Client, central string, proof Authentication, operationID, requestDigest string) error {
	instance := productusers.Instance(client.InstanceID)
	issuer := s.issuer + "/product-auth/" + client.ProductID + "/" + instance
	if err := accounts.LockIdentity(ctx, tx, issuer, proof.LocalUserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, issuer, proof.LocalUserID, central); err != nil {
		return err
	}
	binding, err := s.users.EnsureRuntimeBindingOn(ctx, tx, client.ProductID, instance, proof.LocalUserID, operationID,
		accounts.Identity{Issuer: issuer, Subject: proof.LocalUserID}, accounts.User{ID: central})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product_auth_subjects(binding_id,auth_version,registration_key,registration_digest)
		VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, binding.ID, proof.AuthVersion, operationID, requestDigest); err != nil {
		return err
	}
	var version int64
	var deleted, locked bool
	if err = tx.QueryRow(ctx, `SELECT auth_version,deleted,native_locked FROM product_auth_subjects WHERE binding_id=$1 FOR UPDATE`, binding.ID).Scan(&version, &deleted, &locked); err != nil {
		return err
	}
	if deleted || locked || version != proof.AuthVersion {
		return accounts.ErrConflict
	}
	roles, err := json.Marshal([]string{client.IdentityLinkRole})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version,product_role)
		VALUES($1,$2,$3,$4,true,1,1) ON CONFLICT DO NOTHING`, central, client.ProductID, instance, roles); err != nil {
		return err
	}
	if err = s.users.RequirePermissionOn(ctx, tx, central, client.ProductID, instance, "product:use"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO product_auth_events(binding_id,operation_id,request_digest,actor_id,event,snapshot)
		VALUES($1,$2,$3,$4,'identity_link',jsonb_build_object('auth_version',$5::bigint,'client_id',$6::text))`,
		binding.ID, operationID, requestDigest, central, proof.AuthVersion, client.ClientID)
	return err
}

func (s *Service) IdentityLinkByCanonicalUser(ctx context.Context, product, instance, central string) (Identity, error) {
	instance = productusers.Instance(instance)
	var local string
	err := s.pool.QueryRow(ctx, `SELECT local_user_id FROM product_runtime_bindings WHERE product_id=$1 AND instance_id=$2 AND user_id=$3`, product, instance, central).Scan(&local)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, accounts.ErrNotFound
	}
	if err != nil {
		return Identity{}, err
	}
	out, err := s.Lookup(ctx, product, instance, local)
	if err != nil {
		return Identity{}, err
	}
	if !out.Access.Active || !slices.Contains(out.Access.Permissions, "product:use") {
		return Identity{}, accounts.ErrForbidden
	}
	return out, nil
}
