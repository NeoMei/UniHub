package billing

import (
	"context"
	"errors"
	"math"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

const (
	FundingCredits     = "credits"
	FundingPrepaidCash = "prepaid_cash"
	CashAsset          = "CNY/6"
)

type BillingComponent struct {
	Enabled bool `json:"enabled"`
}
type UsagePolicy struct {
	Enabled             bool     `json:"enabled"`
	RequireSubscription bool     `json:"require_subscription"`
	FundingModes        []string `json:"funding_modes"`
	DefaultFundingMode  string   `json:"default_funding_mode"`
}
type BillingProfile struct {
	ProductID       string           `json:"product_id"`
	Version         int64            `json:"version"`
	Subscription    BillingComponent `json:"subscription"`
	IncludedCredits BillingComponent `json:"included_credits"`
	Addons          BillingComponent `json:"addons"`
	Usage           UsagePolicy      `json:"usage"`
}

var billingProductID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Validate checks the composition independently of a product's immutable offer
// terms. Enabled credit sources must have an explicit credits usage mode.
func (p BillingProfile) Validate() error {
	if !billingProductID.MatchString(p.ProductID) || p.Version < 0 {
		return ErrInvalid
	}
	modes := map[string]bool{}
	for _, mode := range p.Usage.FundingModes {
		if (mode != FundingCredits && mode != FundingPrepaidCash) || modes[mode] {
			return ErrInvalid
		}
		modes[mode] = true
	}
	if p.Usage.Enabled {
		if len(modes) == 0 || !modes[p.Usage.DefaultFundingMode] {
			return ErrInvalid
		}
	} else if len(modes) > 0 || p.Usage.DefaultFundingMode != "" || p.Usage.RequireSubscription {
		return ErrInvalid
	}
	if p.Usage.RequireSubscription && !p.Subscription.Enabled {
		return ErrInvalid
	}
	if p.IncludedCredits.Enabled && (!p.Subscription.Enabled || !modes[FundingCredits]) {
		return ErrInvalid
	}
	if p.Addons.Enabled && !modes[FundingCredits] {
		return ErrInvalid
	}
	return nil
}

func BillingPreset(product, preset string) (BillingProfile, error) {
	p := BillingProfile{ProductID: product, Usage: UsagePolicy{FundingModes: []string{}}}
	switch preset {
	case "hybrid":
		p.Subscription.Enabled = true
		p.IncludedCredits.Enabled = true
		p.Addons.Enabled = true
		p.Usage = UsagePolicy{Enabled: true, RequireSubscription: true, FundingModes: []string{FundingCredits}, DefaultFundingMode: FundingCredits}
	case "prepaid":
		p.Usage = UsagePolicy{Enabled: true, FundingModes: []string{FundingPrepaidCash}, DefaultFundingMode: FundingPrepaidCash}
	case "subscription":
		p.Subscription.Enabled = true
	default:
		return BillingProfile{}, ErrInvalid
	}
	return p, p.Validate()
}

func (s *Service) Profile(ctx context.Context, product string) (BillingProfile, error) {
	return s.ProfileOn(ctx, s.pool, product)
}

// ProfileOn also serves credential and reserve checks already holding a DB lock.
// An undeclared product profile retains the legacy version-zero hybrid policy.
func (s *Service) ProfileOn(ctx context.Context, q accounts.Querier, productID string) (BillingProfile, error) {
	if e := product(ctx, q, productID); e != nil {
		return BillingProfile{}, e
	}
	p, e := load[BillingProfile](ctx, q, `SELECT snapshot FROM product_billing_profiles WHERE product_id=$1 ORDER BY version DESC LIMIT 1`, productID)
	if errors.Is(e, accounts.ErrNotFound) {
		return BillingPreset(productID, "hybrid")
	}
	return canonicalProfile(p), e
}

func canonicalProfile(p BillingProfile) BillingProfile {
	if p.Usage.FundingModes == nil {
		p.Usage.FundingModes = []string{}
	}
	return p
}

func (s *Service) Profiles(ctx context.Context, products []string) ([]BillingProfile, error) {
	out := make([]BillingProfile, 0, len(products))
	for _, product := range products {
		p, e := s.Profile(ctx, product)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}

// SaveProfile appends after comparing the caller's version with the current
// product version. The product row serializes first writes and concurrent updates.
func (s *Service) SaveProfile(ctx context.Context, actor, productID string, p BillingProfile) (BillingProfile, error) {
	p = canonicalProfile(p)
	if e := s.operator(actor); e != nil {
		return BillingProfile{}, e
	}
	if p.ProductID != productID || p.Version == math.MaxInt64 {
		return BillingProfile{}, ErrInvalid
	}
	if e := p.Validate(); e != nil {
		return BillingProfile{}, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return BillingProfile{}, e
	}
	defer database.Rollback(tx)
	if e = lockBillingProduct(ctx, tx, productID); e != nil {
		return BillingProfile{}, e
	}
	current, e := s.ProfileOn(ctx, tx, productID)
	if e != nil {
		return BillingProfile{}, e
	}
	if current.Version != p.Version {
		return BillingProfile{}, ErrConflict
	}
	p.Version++
	if e = insertBillingProfile(ctx, tx, actor, p); e != nil {
		return BillingProfile{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return BillingProfile{}, e
	}
	return p, nil
}

func lockBillingProduct(ctx context.Context, tx pgx.Tx, productID string) error {
	var id string
	e := tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 AND enabled FOR UPDATE`, productID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrInvalid
	}
	return e
}
func insertBillingProfile(ctx context.Context, tx pgx.Tx, actor string, p BillingProfile) error {
	_, e := tx.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) VALUES($1,$2,$3,$4)`, p.ProductID, p.Version, actor, encode(p))
	return e
}

// ApplyConfiguredProfiles is the trusted deployment path. Its explicit versions
// are monotonic, equal versions must match exactly, and a deployment is atomic.
func (s *Service) ApplyConfiguredProfiles(ctx context.Context, profiles []BillingProfile) error {
	items := append([]BillingProfile(nil), profiles...)
	for i := range items {
		items[i] = canonicalProfile(items[i])
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ProductID < items[j].ProductID })
	for i, p := range items {
		if e := p.Validate(); e != nil {
			return e
		}
		if p.Version == 0 || (i > 0 && items[i-1].ProductID == p.ProductID) {
			return ErrInvalid
		}
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	for _, p := range items {
		if e = lockBillingProduct(ctx, tx, p.ProductID); e != nil {
			return e
		}
		current, e := s.ProfileOn(ctx, tx, p.ProductID)
		if e != nil {
			return e
		}
		if p.Version < current.Version {
			return ErrConflict
		}
		if p.Version == current.Version {
			if hash(p) != hash(current) {
				return ErrConflict
			}
			continue
		}
		if e = insertBillingProfile(ctx, tx, "deployment", p); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// offerForSaleOn checks only a new commercial commitment. Paid order recovery
// and monthly issuance deliberately keep using their original offer snapshots.
func (s *Service) offerForSaleOn(ctx context.Context, q accounts.Querier, o Offer) error {
	p, e := s.ProfileOn(ctx, q, o.ProductID)
	if e != nil {
		return e
	}
	n, e := integer(o.MonthlyCredit, false)
	if e != nil {
		return e
	}
	if o.Kind == "membership" {
		if !p.Subscription.Enabled || (p.IncludedCredits.Enabled && n == 0) || (!p.IncludedCredits.Enabled && n > 0) {
			return accounts.ErrForbidden
		}
	} else if o.Kind == "addon" {
		if !p.Addons.Enabled || n == 0 {
			return accounts.ErrForbidden
		}
	} else {
		return ErrInvalid
	}
	return nil
}
