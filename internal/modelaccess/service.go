// Package modelaccess issues short-lived opaque capabilities for the llmrouter
// audience. A model capability is never a login, wallet or settlement credential.
package modelaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"slices"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
)

var ErrCredential = errors.New("model credential invalid or no longer authorized")

type Eligibility interface {
	Subscriptions(context.Context, string, string) ([]billing.Subscription, error)
	Rates(context.Context) ([]billing.Rate, error)
}

// BillingUsageEligibility supplies current composable usage policy. Older
// test/adaptor implementations remain restricted to legacy member credits.
type BillingUsageEligibility interface {
	AuthorizeUsage(context.Context, string, string, string, string) (billing.UsageAuthorization, error)
}

type BillingUsageEligibilityOn interface {
	BillingUsageEligibility
	AuthorizeUsageOn(context.Context, accounts.Querier, string, string, string, string) (billing.UsageAuthorization, error)
}
type Service struct {
	pool        *pgxpool.Pool
	accounts    *accounts.Store
	eligibility Eligibility
}
type Request struct {
	FundingMode     string   `json:"funding_mode,omitempty"`
	OriginProductID string   `json:"origin_product_id"`
	Models          []string `json:"models"`
	TaskID          string   `json:"task_id"`
}
type Claims struct {
	FundingMode           string    `json:"funding_mode"`
	Asset                 string    `json:"asset"`
	BillingProfileVersion int64     `json:"billing_profile_version"`
	ID                    string    `json:"id"`
	Audience              string    `json:"audience"`
	ActorID               string    `json:"actor_id"`
	AccountID             string    `json:"account_id"`
	OriginProductID       string    `json:"origin_product_id"`
	Models                []string  `json:"models"`
	TaskID                string    `json:"task_id"`
	ExpiresAt             time.Time `json:"expires_at"`
}
type Grant struct {
	Claims
	Token string `json:"token"`
}

func New(pool *pgxpool.Pool, a *accounts.Store, e Eligibility) *Service { return &Service{pool, a, e} }
func opaque(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + hex.EncodeToString(b), nil
}
func digest(token string) string { h := sha256.Sum256([]byte(token)); return hex.EncodeToString(h[:]) }
func (s *Service) eligible(ctx context.Context, actor, account, product string) (time.Time, error) {
	if _, e := s.accounts.Role(ctx, actor, account); e != nil {
		return time.Time{}, e
	}
	subs, e := s.eligibility.Subscriptions(ctx, actor, account)
	if e != nil {
		return time.Time{}, e
	}
	for _, sub := range subs {
		if sub.ProductID == product && sub.Status == "active" && sub.PaidThrough.After(time.Now().UTC()) {
			return sub.PaidThrough, nil
		}
	}
	return time.Time{}, accounts.ErrForbidden
}
func (s *Service) authorize(ctx context.Context, actor, account, product, mode string) (billing.UsageAuthorization, error) {
	if policy, ok := s.eligibility.(BillingUsageEligibility); ok {
		auth, e := policy.AuthorizeUsage(ctx, actor, account, product, mode)
		if e != nil {
			return auth, e
		}
		if (mode != "" && auth.FundingMode != mode) || auth.ProfileVersion < 0 || (auth.FundingMode != billing.FundingCredits && auth.FundingMode != billing.FundingPrepaidCash) || (auth.FundingMode == billing.FundingCredits && auth.Asset != billing.CreditAsset) || (auth.FundingMode == billing.FundingPrepaidCash && auth.Asset != billing.CashAsset) {
			return billing.UsageAuthorization{}, accounts.ErrForbidden
		}
		return auth, nil
	}
	if mode != "" && mode != billing.FundingCredits {
		return billing.UsageAuthorization{}, accounts.ErrForbidden
	}
	until, e := s.eligible(ctx, actor, account, product)
	return billing.UsageAuthorization{FundingMode: billing.FundingCredits, Asset: billing.CreditAsset, ExpiresAt: until}, e
}

func (s *Service) Issue(ctx context.Context, actor, account string, r Request) (Grant, error) {
	if r.OriginProductID == "" || len(r.Models) == 0 || len(r.Models) > 32 || strings.TrimSpace(r.TaskID) == "" || len(r.TaskID) > 128 {
		return Grant{}, billing.ErrInvalid
	}
	policyModels, e := s.productModels(ctx, s.pool, r.OriginProductID)
	if e != nil {
		return Grant{}, e
	}
	auth, e := s.authorize(ctx, actor, account, r.OriginProductID, r.FundingMode)
	if e != nil {
		return Grant{}, e
	}
	rates, e := s.eligibility.Rates(ctx)
	if e != nil {
		return Grant{}, e
	}
	models := slices.Clone(r.Models)
	slices.Sort(models)
	models = slices.Compact(models)
	if e = checkModelRates(models, policyModels, rates, auth.Asset); e != nil {
		return Grant{}, e
	}
	token, e := opaque("uh_model_")
	if e != nil {
		return Grant{}, e
	}
	id, e := opaque("mc_")
	if e != nil {
		return Grant{}, e
	}
	expiry := time.Now().UTC().Add(10 * time.Minute)
	if !auth.ExpiresAt.IsZero() && auth.ExpiresAt.Before(expiry) {
		expiry = auth.ExpiresAt
	}
	grant := Grant{Claims: Claims{ID: id, Audience: "llmrouter", ActorID: actor, AccountID: account, OriginProductID: r.OriginProductID, Models: models, TaskID: r.TaskID, ExpiresAt: expiry, FundingMode: auth.FundingMode, Asset: auth.Asset, BillingProfileVersion: auth.ProfileVersion}, Token: token}
	data, _ := json.Marshal(models)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Grant{}, e
	}
	defer database.Rollback(tx)
	if e = database.LockAccountTx(ctx, tx, account); e != nil {
		return Grant{}, e
	}
	if _, e = s.accounts.RoleOn(ctx, tx, actor, account); e != nil {
		return Grant{}, e
	}
	// Recheck current policy inside the membership mutation lock before saving.
	current := auth
	if policy, ok := s.eligibility.(BillingUsageEligibilityOn); ok {
		current, e = policy.AuthorizeUsageOn(ctx, tx, actor, account, r.OriginProductID, auth.FundingMode)
		if e != nil {
			return Grant{}, e
		}
	}
	if current.Asset != auth.Asset || current.FundingMode != auth.FundingMode || current.ProfileVersion < 0 {
		return Grant{}, accounts.ErrForbidden
	}
	policyModels, e = s.productModels(ctx, tx, r.OriginProductID)
	if e != nil {
		return Grant{}, e
	}
	// Real billing rates are re-read inside the transaction, so retirement cannot
	// leave a newly issued capability using stale catalog state.
	if _, ok := s.eligibility.(*billing.Service); ok {
		var ratesData []byte
		if e = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(snapshot),'[]'::jsonb) FROM billing_rates WHERE status='published'`).Scan(&ratesData); e != nil {
			return Grant{}, e
		}
		if e = json.Unmarshal(ratesData, &rates); e != nil {
			return Grant{}, e
		}
	}
	if e = checkModelRates(models, policyModels, rates, current.Asset); e != nil {
		return Grant{}, e
	}
	grant.BillingProfileVersion = current.ProfileVersion
	if !current.ExpiresAt.IsZero() && current.ExpiresAt.Before(grant.ExpiresAt) {
		grant.ExpiresAt = current.ExpiresAt
	}
	if _, e = tx.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, digest(token), actor, account, r.OriginProductID, data, r.TaskID, grant.ExpiresAt, grant.FundingMode, grant.Asset, grant.BillingProfileVersion); e != nil {
		return Grant{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES($1,$2,$3,'model_credential.issued')`, account, actor, id); e != nil {
		return Grant{}, e
	}
	return grant, tx.Commit(ctx)
}

// Verify is called only by the separately authenticated gateway. It returns no
// capability secret. New reservations also recheck membership in billing's lock.
func (s *Service) Verify(ctx context.Context, token, model string) (Claims, error) {
	return s.verify(ctx, token, model, true)
}

// VerifyReceipt authenticates the immutable credential scope for a historical
// call. It deliberately does not require a currently published rate: rate
// retirement must not make durable receipts unreconcilable.
func (s *Service) VerifyReceipt(ctx context.Context, token, model string) (Claims, error) {
	return s.verify(ctx, token, model, false)
}

func (s *Service) verify(ctx context.Context, token, model string, enforceRate bool) (Claims, error) {
	if len(token) != 73 || !strings.HasPrefix(token, "uh_model_") {
		return Claims{}, ErrCredential
	}
	var c Claims
	var data []byte
	e := s.pool.QueryRow(ctx, `SELECT id,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version FROM model_credentials WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now()`, digest(token)).Scan(&c.ID, &c.ActorID, &c.AccountID, &c.OriginProductID, &data, &c.TaskID, &c.ExpiresAt, &c.FundingMode, &c.Asset, &c.BillingProfileVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, ErrCredential
	}
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(data, &c.Models); e != nil {
		return c, e
	}
	if model != "" && !slices.Contains(c.Models, model) {
		return Claims{}, ErrCredential
	}
	auth, e := s.authorize(ctx, c.ActorID, c.AccountID, c.OriginProductID, c.FundingMode)
	if e != nil || auth.Asset != c.Asset {
		return Claims{}, ErrCredential
	}
	models, e := s.productModels(ctx, s.pool, c.OriginProductID)
	if e != nil {
		return Claims{}, ErrCredential
	}
	for _, m := range c.Models {
		if models != nil && !slices.Contains(models, m) {
			return Claims{}, ErrCredential
		}
	}
	if enforceRate {
		rates, e := s.eligibility.Rates(ctx)
		if e != nil {
			return Claims{}, e
		}
		if e = checkModelRates(c.Models, models, rates, c.Asset); e != nil {
			return Claims{}, ErrCredential
		}
	}
	c.Audience = "llmrouter"
	return c, nil
}
func (s *Service) Revoke(ctx context.Context, actor, account, id string) error {
	return s.RevokeForProduct(ctx, actor, account, id, "")
}

// RevokeForProduct preserves account and actor checks while restricting product
// clients to their own immutable credential origin. Empty product is console scope.
func (s *Service) RevokeForProduct(ctx context.Context, actor, account, id, product string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if e = database.LockAccountTx(ctx, tx, account); e != nil {
		return e
	}
	role, e := s.accounts.RoleOn(ctx, tx, actor, account)
	if e != nil {
		return e
	}
	var owner, origin string
	if e = tx.QueryRow(ctx, `SELECT actor_id,origin_product_id FROM model_credentials WHERE id=$1 AND account_id=$2`, id, account).Scan(&owner, &origin); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return accounts.ErrNotFound
		}
		return e
	}
	if product != "" && product != origin {
		return accounts.ErrNotFound
	}
	if actor != owner && role != "owner" && role != "billing_admin" {
		return accounts.ErrForbidden
	}
	if _, e = tx.Exec(ctx, `UPDATE model_credentials SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES($1,$2,$3,'model_credential.revoked')`, account, actor, id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Service) productModels(ctx context.Context, q accounts.Querier, product string) ([]string, error) {
	var enabled bool
	if e := q.QueryRow(ctx, `SELECT enabled FROM products WHERE id=$1`, product).Scan(&enabled); e != nil || !enabled {
		return nil, accounts.ErrForbidden
	}
	var data []byte
	e := q.QueryRow(ctx, `SELECT snapshot FROM product_billing_profiles WHERE product_id=$1 ORDER BY version DESC LIMIT 1`, product).Scan(&data)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var profile billing.BillingProfile
	if e = json.Unmarshal(data, &profile); e != nil {
		return nil, e
	}
	if !profile.Usage.Enabled {
		return nil, accounts.ErrForbidden
	}
	return profile.Usage.Models, nil
}
func checkModelRates(models, allowlist []string, rates []billing.Rate, asset string) error {
	counts := map[string]int{}
	for _, r := range rates {
		a := r.Asset
		if a == "" {
			a = billing.CreditAsset
		}
		if r.Status == "published" && a == asset {
			counts[r.Model]++
		}
	}
	for _, m := range models {
		if m == "" || (allowlist != nil && !slices.Contains(allowlist, m)) || counts[m] != 1 {
			return accounts.ErrForbidden
		}
	}
	return nil
}

type Consistency struct {
	ProductID       string         `json:"product_id"`
	Asset           string         `json:"asset"`
	Allowlist       []string       `json:"allowlist"`
	PublishedRates  []billing.Rate `json:"published_rates"`
	MissingModels   []string       `json:"missing_models"`
	ExtraModels     []string       `json:"extra_models"`
	DuplicateModels []string       `json:"duplicate_models"`
}

func (s *Service) Consistency(ctx context.Context, product, asset string) (Consistency, error) {
	out := Consistency{ProductID: product, Asset: asset, Allowlist: []string{}, PublishedRates: []billing.Rate{}, MissingModels: []string{}, ExtraModels: []string{}, DuplicateModels: []string{}}
	models, e := s.productModels(ctx, s.pool, product)
	if e != nil {
		return out, e
	}
	out.Allowlist = append(out.Allowlist, models...)
	rates, e := s.eligibility.Rates(ctx)
	if e != nil {
		return out, e
	}
	counts := map[string]int{}
	for _, r := range rates {
		a := r.Asset
		if a == "" {
			a = billing.CreditAsset
		}
		if r.Status == "published" && a == asset {
			out.PublishedRates = append(out.PublishedRates, r)
			counts[r.Model]++
		}
	}
	for _, m := range models {
		if counts[m] == 0 {
			out.MissingModels = append(out.MissingModels, m)
		}
	}
	for m, n := range counts {
		if models != nil && !slices.Contains(models, m) {
			out.ExtraModels = append(out.ExtraModels, m)
		}
		if n > 1 {
			out.DuplicateModels = append(out.DuplicateModels, m)
		}
	}
	slices.Sort(out.Allowlist)
	slices.Sort(out.MissingModels)
	slices.Sort(out.ExtraModels)
	slices.Sort(out.DuplicateModels)
	return out, nil
}
