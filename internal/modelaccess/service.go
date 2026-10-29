// Package modelaccess issues task and durable opaque capabilities for the
// llmrouter audience. A capability is never a login or settlement credential.
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
	"unihub/internal/productusers"
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
	pool         *pgxpool.Pool
	accounts     *accounts.Store
	eligibility  Eligibility
	productUsers *productusers.Store
}
type Request struct {
	FundingMode     string   `json:"funding_mode,omitempty"`
	OriginProductID string   `json:"origin_product_id"`
	Models          []string `json:"models"`
	TaskID          string   `json:"task_id"`
}
type Claims struct {
	Operations            map[string][]string `json:"operations,omitempty"`
	CredentialKind        string              `json:"credential_kind,omitempty"`
	InstanceID            string              `json:"instance_id,omitempty"`
	FundingMode           string              `json:"funding_mode"`
	Asset                 string              `json:"asset"`
	BillingProfileVersion int64               `json:"billing_profile_version"`
	ID                    string              `json:"id"`
	Audience              string              `json:"audience"`
	ActorID               string              `json:"actor_id"`
	AccountID             string              `json:"account_id"`
	OriginProductID       string              `json:"origin_product_id"`
	Models                []string            `json:"models"`
	TaskID                string              `json:"task_id"`
	ExpiresAt             time.Time           `json:"expires_at"`
}
type Grant struct {
	Claims
	Token string `json:"token"`
}

func New(pool *pgxpool.Pool, a *accounts.Store, e Eligibility) *Service {
	return &Service{pool: pool, accounts: a, eligibility: e}
}
func (s *Service) ConfigureProductUsers(users *productusers.Store) { s.productUsers = users }
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
	return s.issue(ctx, actor, account, r, nil)
}

func (s *Service) issue(ctx context.Context, actor, account string, r Request, key *APIKeyRequest) (Grant, error) {
	if e := s.activeOn(ctx, s.pool, actor); e != nil {
		return Grant{}, e
	}
	if r.OriginProductID == "" || len(r.Models) == 0 || len(r.Models) > 32 || (key == nil && strings.TrimSpace(r.TaskID) == "") || len(r.TaskID) > 128 {
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
	if key != nil {
		token, e = opaque("uh_key_")
	}
	if e != nil {
		return Grant{}, e
	}
	id, e := opaque("mc_")
	if e != nil {
		return Grant{}, e
	}
	expiry := time.Now().UTC().Add(10 * time.Minute)
	if key != nil {
		expiry = permanentExpiry
		if key.ExpiresAt != nil {
			expiry = key.ExpiresAt.UTC()
		}
	}
	if key == nil && !auth.ExpiresAt.IsZero() && auth.ExpiresAt.Before(expiry) {
		expiry = auth.ExpiresAt
	}
	grant := Grant{Claims: Claims{ID: id, Audience: "llmrouter", ActorID: actor, AccountID: account, OriginProductID: r.OriginProductID, Models: models, TaskID: r.TaskID, ExpiresAt: expiry, FundingMode: auth.FundingMode, Asset: auth.Asset, BillingProfileVersion: auth.ProfileVersion}, Token: token}
	if key != nil {
		grant.CredentialKind, grant.InstanceID = "api_key", key.InstanceID
	}
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
	if e = s.activeOn(ctx, tx, actor); e != nil {
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
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('unihub:billing-rate-catalog',0))`); e != nil {
			return Grant{}, e
		}
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
	if key == nil && !current.ExpiresAt.IsZero() && current.ExpiresAt.Before(grant.ExpiresAt) {
		grant.ExpiresAt = current.ExpiresAt
	}
	if _, e = tx.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, digest(token), actor, account, r.OriginProductID, data, r.TaskID, grant.ExpiresAt, grant.FundingMode, grant.Asset, grant.BillingProfileVersion); e != nil {
		return Grant{}, e
	}
	if key != nil {
		if e = s.apiKeyProductActive(ctx, tx, actor, r.OriginProductID, key.InstanceID); e != nil {
			return Grant{}, e
		}
		if _, e = tx.Exec(ctx, `UPDATE model_credentials SET credential_kind='api_key',name=$2,instance_id=$3,max_total=$4,max_per_call=$5 WHERE id=$1`, id, key.Name, key.InstanceID, key.MaxTotal, key.MaxPerCall); e != nil {
			return Grant{}, e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES($1,$2,$3,'model_credential.issued')`, account, actor, id); e != nil {
		return Grant{}, e
	}
	return grant, tx.Commit(ctx)
}

// Verify is called only by the separately authenticated gateway. It returns no
// capability secret. New reservations also recheck membership in billing's lock.
func (s *Service) Verify(ctx context.Context, token, model string) (Claims, error) {
	return s.verify(ctx, token, model, "", true)
}

func (s *Service) VerifyForInstance(ctx context.Context, token, model, instance string) (Claims, error) {
	return s.verify(ctx, token, model, instance, true)
}

// VerifyWireForInstance authorizes a fixed gateway route for the requested model.
// Historical receipt reads retain credential authentication but do not depend on
// the current tariff catalog.
func (s *Service) VerifyWireForInstance(ctx context.Context, token, model, instance, wire string, receipt bool) (Claims, error) {
	if wire == "" || model == "" || billing.WireBillingOperation(wire) == "" {
		return Claims{}, ErrCredential
	}
	c, err := s.verify(ctx, token, model, instance, !receipt)
	if err != nil {
		return Claims{}, err
	}
	if !receipt {
		rates, err := s.eligibility.Rates(ctx)
		if err != nil {
			return Claims{}, err
		}
		found := false
		for _, rate := range rates {
			asset := rate.Asset
			if asset == "" {
				asset = billing.CreditAsset
			}
			if rate.Status == "published" && rate.Model == model && asset == c.Asset && billing.RateMatchesWire(rate, wire) {
				found = true
				break
			}
		}
		if !found {
			return Claims{}, ErrCredential
		}
	}
	c.Operations = map[string][]string{model: {wire}}
	return c, nil
}

// VerifyReceipt authenticates the immutable credential scope for a historical
// call. It deliberately does not require a currently published rate: rate
// retirement must not make durable receipts unreconcilable.
func (s *Service) VerifyReceipt(ctx context.Context, token, model string) (Claims, error) {
	return s.verify(ctx, token, model, "", false)
}

func (s *Service) VerifyReceiptForInstance(ctx context.Context, token, model, instance string) (Claims, error) {
	return s.verify(ctx, token, model, instance, false)
}

func (s *Service) verify(ctx context.Context, token, model, instance string, enforceRate bool) (Claims, error) {
	if !(len(token) == 73 && strings.HasPrefix(token, "uh_model_")) && !(len(token) == 71 && strings.HasPrefix(token, "uh_key_")) {
		return Claims{}, ErrCredential
	}
	var c Claims
	var data []byte
	e := s.pool.QueryRow(ctx, `SELECT id,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version,credential_kind,instance_id FROM model_credentials WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now()`, digest(token)).Scan(&c.ID, &c.ActorID, &c.AccountID, &c.OriginProductID, &data, &c.TaskID, &c.ExpiresAt, &c.FundingMode, &c.Asset, &c.BillingProfileVersion, &c.CredentialKind, &c.InstanceID)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, ErrCredential
	}
	if e != nil {
		return c, e
	}
	// PostgreSQL timestamptz may be decoded in the host's local timezone. The
	// no-expiry sentinel must stay UTC so a positive offset cannot render year
	// 10000 and break JSON serialization after a success status was written.
	c.ExpiresAt = c.ExpiresAt.UTC()
	if c.CredentialKind == "api_key" && (instance == "" || instance != c.InstanceID) {
		return Claims{}, ErrCredential
	}
	if c.CredentialKind != "api_key" {
		if c.InstanceID == "" {
			c.InstanceID = "default"
		}
		if instance != "" && instance != c.InstanceID {
			return Claims{}, ErrCredential
		}
	}
	if e = json.Unmarshal(data, &c.Models); e != nil {
		return c, e
	}
	if model != "" && !slices.Contains(c.Models, model) {
		return Claims{}, ErrCredential
	}
	// Receipt reads may outlive rate retirement, but a revoked/expired key is
	// never public authority. Internal service reconciliation uses stored calls.
	if !enforceRate {
		if c.CredentialKind == "api_key" {
			if e = s.activeOn(ctx, s.pool, c.ActorID); e != nil {
				return Claims{}, ErrCredential
			}
			if _, e = s.accounts.Role(ctx, c.ActorID, c.AccountID); e != nil {
				return Claims{}, ErrCredential
			}
			if e = s.apiKeyProductActive(ctx, s.pool, c.ActorID, c.OriginProductID, c.InstanceID); e != nil {
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
			for _, model := range c.Models {
				if models != nil && !slices.Contains(models, model) {
					return Claims{}, ErrCredential
				}
			}
		}
		c.Audience = "llmrouter"
		return c, nil
	}
	if c.CredentialKind == "api_key" {
		if e = s.apiKeyProductActive(ctx, s.pool, c.ActorID, c.OriginProductID, c.InstanceID); e != nil {
			return Claims{}, ErrCredential
		}
	}
	if enforceRate {
		if e = s.activeOn(ctx, s.pool, c.ActorID); e != nil {
			return Claims{}, ErrCredential
		}
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
	return s.revoke(ctx, actor, account, id, product, "", "task")
}
func (s *Service) revoke(ctx context.Context, actor, account, id, product, instance, kind string) error {
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
	var owner, origin, storedInstance, storedKind string
	if e = tx.QueryRow(ctx, `SELECT actor_id,origin_product_id,instance_id,credential_kind FROM model_credentials WHERE id=$1 AND account_id=$2`, id, account).Scan(&owner, &origin, &storedInstance, &storedKind); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return accounts.ErrNotFound
		}
		return e
	}
	if product != "" && product != origin {
		return accounts.ErrNotFound
	}
	if (kind != "" && kind != storedKind) || (instance != "" && instance != storedInstance) {
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
	for _, model := range models {
		if model == "" || (allowlist != nil && !slices.Contains(allowlist, model)) || !billing.ModelRatesUnambiguous(rates, model, asset) {
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
		if n > 1 && !billing.ModelRatesUnambiguous(rates, m, asset) {
			out.DuplicateModels = append(out.DuplicateModels, m)
		}
	}
	slices.Sort(out.Allowlist)
	slices.Sort(out.MissingModels)
	slices.Sort(out.ExtraModels)
	slices.Sort(out.DuplicateModels)
	return out, nil
}

func (s *Service) activeOn(ctx context.Context, q accounts.Querier, actor string) error {
	var active bool
	if e := q.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, actor).Scan(&active); e != nil {
		return e
	}
	if !active {
		return accounts.ErrForbidden
	}
	return nil
}
