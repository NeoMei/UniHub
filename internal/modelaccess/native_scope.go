package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
)

const (
	ScopeSelected                = "selected"
	ScopeLLMRouterAvailable      = "llmrouter_available"
	PricingLLMRouterNative       = "llmrouter_native"
	PricingLLMRouterNativeCredit = billing.NativeCreditPricingSource
)

type APIKeyScopeRequest struct {
	OriginProductID string `json:"origin_product_id,omitempty"`
	InstanceID      string `json:"instance_id,omitempty"`
	ModelScope      string `json:"model_scope"`
}

func (s *Service) nativeAuthorityOn(ctx context.Context, q accounts.Querier, actor, account, product, instance, funding string) (billing.UsageAuthorization, error) {
	if product != "superwagie" || !instanceID.MatchString(instance) || s.productUsers == nil || !s.productUsers.Managed(product, instance) || funding != "" && funding != billing.FundingPrepaidCash && funding != billing.FundingCredits {
		return billing.UsageAuthorization{}, accounts.ErrForbidden
	}
	policy, ok := s.eligibility.(BillingUsageEligibilityOn)
	if !ok {
		return billing.UsageAuthorization{}, accounts.ErrForbidden
	}
	if e := s.activeOn(ctx, q, actor); e != nil {
		return billing.UsageAuthorization{}, e
	}
	if _, e := s.accounts.RoleOn(ctx, q, actor, account); e != nil {
		return billing.UsageAuthorization{}, e
	}
	if e := s.apiKeyProductActive(ctx, q, actor, product, instance); e != nil {
		return billing.UsageAuthorization{}, e
	}
	auth, e := policy.AuthorizeUsageOn(ctx, q, actor, account, product, funding)
	if e != nil {
		return auth, e
	}
	if auth.ProfileVersion < 0 || !nativeFundingMatches(auth.FundingMode, auth.Asset, nativePricingSource(auth.Asset)) {
		return billing.UsageAuthorization{}, accounts.ErrForbidden
	}
	if auth.Asset == billing.CreditAsset {
		if _, e := s.nativeCreditConversionOn(ctx, q, product); e != nil {
			return billing.UsageAuthorization{}, e
		}
	}
	return auth, nil
}

func nativeScopeLocks(ctx context.Context, tx pgx.Tx, product, instance, account string) error {
	for _, lock := range []string{"unihub:platform-administration", "unihub:user-administration", "product-auth:" + product + "/" + instance, "runtime-access:" + product + "/" + instance} {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, lock); e != nil {
			return e
		}
	}
	return database.LockAccountTx(ctx, tx, account)
}

func (s *Service) createNativeAPIKey(ctx context.Context, actor, account string, r APIKeyRequest) (APIKey, error) {
	// Native keys opt into credits explicitly; a changed profile default never migrates cash keys.
	if r.FundingMode == "" {
		r.FundingMode = billing.FundingPrepaidCash
	}
	if r.FundingMode == billing.FundingCredits {
		ready, e := s.NativeCreditsReadiness(ctx, actor, account, r.OriginProductID, r.InstanceID)
		if e != nil {
			return APIKey{}, e
		}
		if !ready.Ready {
			return APIKey{}, accounts.ErrForbidden
		}
	}
	models := slices.Clone(r.Models)
	if models == nil {
		models = []string{}
	}
	for _, model := range models {
		if strings.TrimSpace(model) != model || model == "" || len(model) > 256 {
			return APIKey{}, billing.ErrInvalid
		}
	}
	slices.Sort(models)
	models = slices.Compact(models)
	token, e := opaque("uh_key_")
	if e != nil {
		return APIKey{}, e
	}
	id, e := opaque("mc_")
	if e != nil {
		return APIKey{}, e
	}
	expiry := permanentExpiry
	if r.ExpiresAt != nil {
		expiry = r.ExpiresAt.UTC()
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return APIKey{}, e
	}
	defer database.Rollback(tx)
	if e = nativeScopeLocks(ctx, tx, r.OriginProductID, r.InstanceID, account); e != nil {
		return APIKey{}, e
	}
	auth, e := s.nativeAuthorityOn(ctx, tx, actor, account, r.OriginProductID, r.InstanceID, r.FundingMode)
	if e != nil {
		return APIKey{}, e
	}
	data, _ := json.Marshal(models)
	_, e = tx.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version,credential_kind,name,instance_id,max_total,max_per_call,model_scope,pricing_source,credential_version) VALUES($1,$2,$3,$4,$5,$6,'',$7,$8,$9,$10,'api_key',$11,$12,$13,$14,$15,$16,1)`, id, digest(token), actor, account, r.OriginProductID, data, expiry, auth.FundingMode, auth.Asset, auth.ProfileVersion, r.Name, r.InstanceID, r.MaxTotal, r.MaxPerCall, ScopeLLMRouterAvailable, nativePricingSource(auth.Asset))
	if e != nil {
		return APIKey{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES($1,$2,$3,'model_credential.issued')`, account, actor, id); e != nil {
		return APIKey{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return APIKey{}, e
	}
	key, e := s.apiKey(ctx, id)
	key.Token = token
	return key, e
}

// UpgradeAPIKeyScope preserves the existing bearer and financial contract.
func (s *Service) UpgradeAPIKeyScope(ctx context.Context, actor, account, id string, r APIKeyScopeRequest) (APIKey, error) {
	if r.ModelScope != ScopeLLMRouterAvailable || r.OriginProductID != "superwagie" || !instanceID.MatchString(r.InstanceID) {
		return APIKey{}, billing.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return APIKey{}, e
	}
	defer database.Rollback(tx)
	if e = nativeScopeLocks(ctx, tx, r.OriginProductID, r.InstanceID, account); e != nil {
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
	if key.RevokedAt != nil || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
		return APIKey{}, accounts.ErrForbidden
	}
	rows, e := tx.Query(ctx, `SELECT user_id FROM product_user_access WHERE product_id=$1 AND instance_id=$2 AND user_id IN($3,$4) ORDER BY user_id FOR SHARE`, key.OriginProductID, key.InstanceID, actor, key.ActorID)
	if e != nil {
		return APIKey{}, e
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return APIKey{}, e
	}
	for _, user := range slices.Compact([]string{actor, key.ActorID}) {
		if _, e = s.nativeAuthorityOn(ctx, tx, user, account, key.OriginProductID, key.InstanceID, key.FundingMode); e != nil {
			return APIKey{}, e
		}
	}
	if key.Asset != billing.CashAsset && !(key.ModelScope == ScopeLLMRouterAvailable && key.Asset == billing.CreditAsset && key.PricingSource == PricingLLMRouterNativeCredit) {
		return APIKey{}, accounts.ErrForbidden
	}
	if key.ModelScope != ScopeLLMRouterAvailable {
		if key.ModelScope != ScopeSelected {
			return APIKey{}, accounts.ErrForbidden
		}
		_, e = tx.Exec(ctx, `UPDATE model_credentials SET model_scope=$2,pricing_source=$3,credential_version=credential_version+1 WHERE id=$1`, id, ScopeLLMRouterAvailable, PricingLLMRouterNative)
		if e != nil {
			return APIKey{}, e
		}
		key.ModelScope, key.PricingSource = ScopeLLMRouterAvailable, PricingLLMRouterNative
		key.CredentialVersion++
		detail, _ := json.Marshal(struct {
			ModelScope        string `json:"model_scope"`
			PricingSource     string `json:"pricing_source"`
			CredentialVersion int64  `json:"credential_version"`
		}{key.ModelScope, key.PricingSource, key.CredentialVersion})
		if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action,detail) VALUES($1,$2,$3,'model_credential.scope_upgraded',$4)`, account, actor, id, detail); e != nil {
			return APIKey{}, e
		}
	}
	key.Held, key.Used, e = billing.UsageTotalsOn(ctx, tx, account, key.OriginProductID, key.InstanceID, key.Asset, key.ID)
	if e != nil {
		return APIKey{}, e
	}
	key.Remaining = billing.UsageRemaining(key.MaxTotal, key.Held, key.Used)
	return key, tx.Commit(ctx)
}
