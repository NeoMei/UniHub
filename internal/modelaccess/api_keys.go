package modelaccess

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/billing"
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
