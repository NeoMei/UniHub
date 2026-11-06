package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
)

var permanentExpiry = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
var instanceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func (s *Service) apiKeyProductActive(ctx context.Context, q accounts.Querier, actor, product, instance string) error {
	if s.productUsers != nil {
		if e := s.productUsers.RequirePermissionOn(ctx, q, actor, product, instance, "product:use"); e != nil {
			return e
		}
	}
	var allowed bool
	if e := q.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3 AND NOT active)`, actor, product, instance).Scan(&allowed); e != nil {
		return e
	}
	if !allowed {
		return accounts.ErrForbidden
	}
	return nil
}

type APIKeyRequest struct {
	Name            string     `json:"name"`
	OriginProductID string     `json:"origin_product_id,omitempty"`
	InstanceID      string     `json:"instance_id"`
	Models          []string   `json:"models"`
	FundingMode     string     `json:"funding_mode,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	MaxTotal        string     `json:"max_total,omitempty"`
	MaxPerCall      string     `json:"max_per_call,omitempty"`
}

type APIKeyModelsRequest struct {
	OriginProductID string   `json:"origin_product_id,omitempty"`
	InstanceID      string   `json:"instance_id,omitempty"`
	Models          []string `json:"models"`
}

// APIKey is safe to list. Token is populated only by CreateAPIKey and is never
// recoverable from the stored SHA-256 digest.
type APIKey struct {
	ID                    string     `json:"id"`
	CredentialKind        string     `json:"credential_kind"`
	Name                  string     `json:"name"`
	ActorID               string     `json:"actor_id"`
	AccountID             string     `json:"account_id"`
	OriginProductID       string     `json:"origin_product_id"`
	InstanceID            string     `json:"instance_id"`
	Models                []string   `json:"models"`
	FundingMode           string     `json:"funding_mode"`
	Asset                 string     `json:"asset"`
	BillingProfileVersion int64      `json:"billing_profile_version"`
	ExpiresAt             *time.Time `json:"expires_at"`
	CreatedAt             time.Time  `json:"created_at"`
	RevokedAt             *time.Time `json:"revoked_at"`
	MaxTotal              string     `json:"max_total"`
	MaxPerCall            string     `json:"max_per_call"`
	Held                  string     `json:"held"`
	Used                  string     `json:"used"`
	Remaining             *string    `json:"remaining"`
	Token                 string     `json:"token,omitempty"`
}

func (s *Service) CreateAPIKey(ctx context.Context, actor, account string, r APIKeyRequest) (APIKey, error) {
	if strings.TrimSpace(r.Name) == "" || len(r.Name) > 128 || !instanceID.MatchString(r.InstanceID) || (r.ExpiresAt != nil && (!r.ExpiresAt.After(time.Now()) || !r.ExpiresAt.Before(permanentExpiry))) {
		return APIKey{}, billing.ErrInvalid
	}
	if e := billing.ValidateUsageLimits(r.MaxTotal, r.MaxPerCall); e != nil {
		return APIKey{}, e
	}
	r.Name = strings.TrimSpace(r.Name)
	g, e := s.issue(ctx, actor, account, Request{OriginProductID: r.OriginProductID, Models: slices.Clone(r.Models), FundingMode: r.FundingMode}, &r)
	if e != nil {
		return APIKey{}, e
	}
	key, e := s.apiKey(ctx, g.ID)
	if e != nil {
		return APIKey{}, e
	}
	key.Token = g.Token
	return key, nil
}

const apiKeyColumns = `id,credential_kind,name,actor_id,account_id,origin_product_id,instance_id,models,funding_mode,asset,billing_profile_version,expires_at,created_at,revoked_at,max_total,max_per_call`

func scanAPIKey(row pgx.Row) (APIKey, error) {
	var key APIKey
	var models []byte
	var expires time.Time
	e := row.Scan(&key.ID, &key.CredentialKind, &key.Name, &key.ActorID, &key.AccountID, &key.OriginProductID, &key.InstanceID, &models, &key.FundingMode, &key.Asset, &key.BillingProfileVersion, &expires, &key.CreatedAt, &key.RevokedAt, &key.MaxTotal, &key.MaxPerCall)
	if e != nil {
		return key, e
	}
	if !expires.Equal(permanentExpiry) {
		key.ExpiresAt = &expires
	}
	e = json.Unmarshal(models, &key.Models)
	return key, e
}

func (s *Service) apiKey(ctx context.Context, id string) (APIKey, error) {
	key, e := scanAPIKey(s.pool.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM model_credentials WHERE id=$1 AND credential_kind='api_key'`, id))
	if e != nil {
		return key, e
	}
	key.Held, key.Used, e = billing.UsageTotalsOn(ctx, s.pool, key.AccountID, key.OriginProductID, key.InstanceID, key.Asset, key.ID)
	key.Remaining = billing.UsageRemaining(key.MaxTotal, key.Held, key.Used)
	return key, e
}

func (s *Service) ListAPIKeys(ctx context.Context, actor, account, product, instance string) ([]APIKey, error) {
	if !instanceID.MatchString(instance) {
		return nil, billing.ErrInvalid
	}
	role, e := s.accounts.Role(ctx, actor, account)
	if e != nil {
		return nil, e
	}
	if e = s.activeOn(ctx, s.pool, actor); e != nil {
		return nil, e
	}
	finance := role == "owner" || role == "billing_admin"
	rows, e := s.pool.Query(ctx, `SELECT `+apiKeyColumns+` FROM model_credentials WHERE credential_kind='api_key' AND account_id=$1 AND origin_product_id=$2 AND instance_id=$3 AND ($4 OR actor_id=$5) ORDER BY created_at DESC,id DESC LIMIT 500`, account, product, instance, finance, actor)
	if e != nil {
		return nil, e
	}
	out := []APIKey{}
	for rows.Next() {
		key, e := scanAPIKey(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, key)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	// Release the result connection before reading totals (single-connection
	// pools must remain usable by product deployments).
	for i := range out {
		key := &out[i]
		key.Held, key.Used, e = billing.UsageTotalsOn(ctx, s.pool, account, product, instance, key.Asset, key.ID)
		if e != nil {
			return nil, e
		}
		key.Remaining = billing.UsageRemaining(key.MaxTotal, key.Held, key.Used)
	}
	return out, nil
}

func (s *Service) RevokeAPIKey(ctx context.Context, actor, account, id, product, instance string) error {
	return s.revoke(ctx, actor, account, id, product, instance, "api_key")
}

// AddAPIKeyModels expands an existing key without changing its token, payer,
// holder, instance, limits or expiry. The account lock serializes additions with
// membership changes and ordinary key revocation; the row lock also protects
// against administrative revocation. Repeating an addition creates no audit.
func (s *Service) AddAPIKeyModels(ctx context.Context, actor, account, id string, r APIKeyModelsRequest) (APIKey, error) {
	if r.OriginProductID == "" || !instanceID.MatchString(r.InstanceID) || len(r.Models) == 0 || len(r.Models) > 32 {
		return APIKey{}, billing.ErrInvalid
	}
	// Expansion requires authorization on the transaction that owns the payer
	// lock. Production billing implements this without a nested pool connection.
	policy, ok := s.eligibility.(BillingUsageEligibilityOn)
	if !ok {
		return APIKey{}, accounts.ErrForbidden
	}
	var rates []billing.Rate
	_, storedRates := s.eligibility.(*billing.Service)
	if !storedRates {
		var e error
		rates, e = s.eligibility.Rates(ctx)
		if e != nil {
			return APIKey{}, e
		}
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return APIKey{}, e
	}
	defer database.Rollback(tx)
	// Administration paths take these locks before changing user status or
	// product assignments. Match their lock-before-account order, including new
	// assignments that do not yet have a row to lock.
	for _, lock := range []string{"unihub:platform-administration", "unihub:user-administration", "product-auth:" + r.OriginProductID + "/" + r.InstanceID, "runtime-access:" + r.OriginProductID + "/" + r.InstanceID} {
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, lock); e != nil {
			return APIKey{}, e
		}
	}
	if e = database.LockAccountTx(ctx, tx, account); e != nil {
		return APIKey{}, e
	}
	role, e := s.accounts.RoleOn(ctx, tx, actor, account)
	if e != nil {
		return APIKey{}, e
	}
	key, e := scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM model_credentials WHERE id=$1 AND account_id=$2 AND origin_product_id=$3 AND instance_id=$4 AND credential_kind='api_key' FOR UPDATE`, id, account, r.OriginProductID, r.InstanceID))
	if errors.Is(e, pgx.ErrNoRows) {
		return APIKey{}, accounts.ErrNotFound
	}
	if e != nil {
		return APIKey{}, e
	}
	if actor != key.ActorID && role != "owner" && role != "billing_admin" {
		return APIKey{}, accounts.ErrForbidden
	}
	if key.RevokedAt != nil || (key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now())) {
		return APIKey{}, accounts.ErrForbidden
	}
	// Imports can update an existing assignment directly. Lock both assignments
	// in the same order as runtime user administration before checking access.
	rows, e := tx.Query(ctx, `SELECT user_id FROM product_user_access WHERE product_id=$1 AND instance_id=$2 AND user_id IN($3,$4) ORDER BY user_id FOR SHARE`, key.OriginProductID, key.InstanceID, actor, key.ActorID)
	if e != nil {
		return APIKey{}, e
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return APIKey{}, e
	}
	// A finance administrator cannot expand a key whose holder has lost access.
	for _, user := range slices.Compact([]string{actor, key.ActorID}) {
		if e = s.activeOn(ctx, tx, user); e != nil {
			return APIKey{}, e
		}
		if _, e = s.accounts.RoleOn(ctx, tx, user, account); e != nil {
			return APIKey{}, e
		}
		if e = s.apiKeyProductActive(ctx, tx, user, key.OriginProductID, key.InstanceID); e != nil {
			return APIKey{}, e
		}
	}
	// Profile updates lock the product row, and rate retirement takes the
	// exclusive catalog lock. Take catalog before product, matching purchases.
	if storedRates {
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('unihub:billing-rate-catalog',0))`); e != nil {
			return APIKey{}, e
		}
	}
	var product string
	if e = tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 AND enabled FOR SHARE`, key.OriginProductID).Scan(&product); errors.Is(e, pgx.ErrNoRows) {
		return APIKey{}, accounts.ErrForbidden
	} else if e != nil {
		return APIKey{}, e
	}
	auth, e := policy.AuthorizeUsageOn(ctx, tx, key.ActorID, account, key.OriginProductID, key.FundingMode)
	if e != nil {
		return APIKey{}, e
	}
	if auth.FundingMode != key.FundingMode || auth.Asset != key.Asset || auth.ProfileVersion < 0 {
		return APIKey{}, accounts.ErrForbidden
	}
	allowlist, e := s.productModels(ctx, tx, key.OriginProductID)
	if e != nil {
		return APIKey{}, e
	}
	if storedRates {
		var raw []byte
		if e = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(snapshot),'[]'::jsonb) FROM billing_rates WHERE status='published'`).Scan(&raw); e != nil {
			return APIKey{}, e
		}
		if e = json.Unmarshal(raw, &rates); e != nil {
			return APIKey{}, e
		}
	}
	models := append(slices.Clone(key.Models), r.Models...)
	slices.Sort(models)
	models = slices.Compact(models)
	if len(models) > 32 {
		return APIKey{}, billing.ErrInvalid
	}
	if e = checkModelRates(models, allowlist, rates, key.Asset); e != nil {
		return APIKey{}, e
	}
	// Waiting for policy/catalog locks must not extend the original lifetime.
	now := time.Now()
	if (key.ExpiresAt != nil && !key.ExpiresAt.After(now)) || (!auth.ExpiresAt.IsZero() && !auth.ExpiresAt.After(now)) {
		return APIKey{}, accounts.ErrForbidden
	}
	added := []string{}
	for _, model := range models {
		if !slices.Contains(key.Models, model) {
			added = append(added, model)
		}
	}
	if len(added) > 0 {
		data, _ := json.Marshal(models)
		if _, e = tx.Exec(ctx, `UPDATE model_credentials SET models=$2 WHERE id=$1`, id, data); e != nil {
			return APIKey{}, e
		}
		detail, _ := json.Marshal(struct {
			AddedModels []string `json:"added_models"`
			Models      []string `json:"models"`
		}{added, models})
		if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action,detail) VALUES($1,$2,$3,'model_credential.models_added',$4)`, account, actor, id, detail); e != nil {
			return APIKey{}, e
		}
		key.Models = models
	}
	key.Held, key.Used, e = billing.UsageTotalsOn(ctx, tx, account, key.OriginProductID, key.InstanceID, key.Asset, key.ID)
	if e != nil {
		return APIKey{}, e
	}
	key.Remaining = billing.UsageRemaining(key.MaxTotal, key.Held, key.Used)
	return key, tx.Commit(ctx)
}
