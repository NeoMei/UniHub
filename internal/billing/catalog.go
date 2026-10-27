package billing

import (
	"context"
	"errors"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

func validateOffer(o Offer) error {
	if !keyOK(o.ID) || o.Name == "" || (o.AccountKind != "personal" && o.AccountKind != "team" && o.AccountKind != "both") || (o.Kind != "membership" && o.Kind != "addon") || o.Rank < 0 {
		return ErrInvalid
	}
	if _, e := integer(o.Price, true); e != nil {
		return e
	}
	if _, e := integer(o.MonthlyCredit, o.Kind == "addon"); e != nil {
		return e
	}
	if o.Kind == "membership" && (o.Period != "month" && o.Period != "year") {
		return ErrInvalid
	}
	if o.Kind == "addon" && (o.Period != "" || o.ValidityMonths < 1 || o.ValidityMonths > 120) {
		return ErrInvalid
	}
	if o.Kind == "membership" && o.MonthlyCredit == "0" && o.RefundRule == "unused_only_v1" {
		return ErrInvalid
	}
	if o.RefundRule != "" && o.RefundRule != "unused_only_v1" {
		return ErrInvalid
	}
	if o.UpgradeRule != "" && o.UpgradeRule != "remaining_term_prorata_v1" {
		return ErrInvalid
	}
	return nil
}
func (s *Service) SaveOffer(ctx context.Context, actor string, o Offer) (Offer, error) {
	if e := s.operator(actor); e != nil {
		return Offer{}, e
	}
	if e := validateOffer(o); e != nil {
		return Offer{}, e
	}
	if o.Sandbox && !s.isSandbox() {
		return Offer{}, accounts.ErrForbidden
	}
	if e := s.offerForSaleOn(ctx, s.pool, o); e != nil {
		return Offer{}, e
	}
	o.Status = "draft"
	tag, e := s.pool.Exec(ctx, `INSERT INTO billing_offers(id,product_id,status,snapshot) VALUES($1,$2,'draft',$3) ON CONFLICT(id) DO UPDATE SET product_id=excluded.product_id,snapshot=excluded.snapshot WHERE billing_offers.status='draft'`, o.ID, o.ProductID, encode(o))
	if e != nil {
		return Offer{}, e
	}
	if tag.RowsAffected() != 1 {
		return Offer{}, ErrConflict
	}
	return o, nil
}
func (s *Service) PublishOffer(ctx context.Context, actor, offerID string) (Offer, error) {
	if e := s.operator(actor); e != nil {
		return Offer{}, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Offer{}, e
	}
	defer database.Rollback(tx)
	o, e := load[Offer](ctx, tx, `SELECT snapshot FROM billing_offers WHERE id=$1 FOR UPDATE`, offerID)
	if e != nil {
		return Offer{}, e
	}
	if o.Sandbox && !s.isSandbox() {
		return Offer{}, accounts.ErrForbidden
	}
	if e = validateOffer(o); e != nil {
		return Offer{}, e
	}
	if e = lockBillingProduct(ctx, tx, o.ProductID); e != nil {
		return Offer{}, e
	}
	if e = s.offerForSaleOn(ctx, tx, o); e != nil {
		return Offer{}, e
	}
	o.Status = "published"
	_, e = tx.Exec(ctx, `UPDATE billing_offers SET status='published',snapshot=$2 WHERE id=$1`, o.ID, encode(o))
	if e != nil {
		return Offer{}, e
	}
	return o, tx.Commit(ctx)
}
func (s *Service) Catalog(ctx context.Context) ([]Offer, error) {
	offers, e := list[Offer](ctx, s.pool, `SELECT o.snapshot FROM billing_offers o JOIN products p ON p.id=o.product_id AND p.enabled WHERE o.status='published' AND (NOT (o.snapshot->>'sandbox')::boolean OR $1) ORDER BY o.product_id,o.id`, s.isSandbox())
	if e != nil {
		return nil, e
	}
	out := make([]Offer, 0, len(offers))
	for _, o := range offers {
		if e = s.offerForSaleOn(ctx, s.pool, o); errors.Is(e, accounts.ErrForbidden) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, nil
}
func (s *Service) OfferDrafts(ctx context.Context, actor string) ([]Offer, error) {
	if e := s.operator(actor); e != nil {
		return nil, e
	}
	return list[Offer](ctx, s.pool, `SELECT snapshot FROM billing_offers ORDER BY product_id,id`)
}
func (s *Service) SeedSandbox(ctx context.Context) ([]Offer, error) {
	if !s.isSandbox() {
		return nil, accounts.ErrForbidden
	}
	out := []Offer{}
	for _, o := range []Offer{
		{ID: "sandbox-sw-month-1", ProductID: "superwagie", Name: "沙盒月度基础", AccountKind: "both", Kind: "membership", Period: "month", Price: "1000000", MonthlyCredit: "1000000", Rank: 1, UpgradeRule: "remaining_term_prorata_v1"},
		{ID: "sandbox-sw-month-2", ProductID: "superwagie", Name: "沙盒月度进阶", AccountKind: "both", Kind: "membership", Period: "month", Price: "2000000", MonthlyCredit: "2000000", Rank: 2, UpgradeRule: "remaining_term_prorata_v1"},
		{ID: "sandbox-sw-month-3", ProductID: "superwagie", Name: "沙盒月度高级", AccountKind: "both", Kind: "membership", Period: "month", Price: "3000000", MonthlyCredit: "3000000", Rank: 3, UpgradeRule: "remaining_term_prorata_v1"},
		{ID: "sandbox-sw-year", ProductID: "superwagie", Name: "沙盒年度基础", AccountKind: "both", Kind: "membership", Period: "year", Price: "12000000", MonthlyCredit: "1000000", Rank: 1, UpgradeRule: "remaining_term_prorata_v1"},
		{ID: "sandbox-addon", ProductID: "llmrouter", Name: "沙盒加量包", AccountKind: "both", Kind: "addon", Price: "1000000", MonthlyCredit: "1000000", ValidityMonths: 12},
	} {
		o.RefundRule = "unused_only_v1"
		o.Sandbox = true
		o.Status = "published"
		o.Entitlements = map[string]string{"sandbox": "true"}
		if _, e := s.pool.Exec(ctx, `INSERT INTO billing_offers(id,product_id,status,snapshot) VALUES($1,$2,'published',$3) ON CONFLICT(id) DO NOTHING`, o.ID, o.ProductID, encode(o)); e != nil {
			return nil, e
		}
		saved, e := load[Offer](ctx, s.pool, `SELECT snapshot FROM billing_offers WHERE id=$1`, o.ID)
		if e != nil {
			return nil, e
		}
		out = append(out, saved)
	}
	return out, nil
}
func (s *Service) buyable(ctx context.Context, q accounts.Querier, offerID, account string) (Offer, error) {
	o, e := load[Offer](ctx, q, `SELECT snapshot FROM billing_offers WHERE id=$1 AND status='published'`, offerID)
	if e != nil {
		return o, e
	}
	if o.Sandbox && !s.isSandbox() {
		return o, accounts.ErrForbidden
	}
	var kind string
	if e = q.QueryRow(ctx, `SELECT kind FROM accounts WHERE id=$1`, account).Scan(&kind); e != nil {
		return o, e
	}
	if o.AccountKind != "both" && o.AccountKind != kind {
		return o, accounts.ErrForbidden
	}
	return o, s.offerForSaleOn(ctx, q, o)
}
func (s *Service) PublishRate(ctx context.Context, actor string, r Rate) (Rate, error) {
	if e := s.operator(actor); e != nil {
		return Rate{}, e
	}
	if !keyOK(r.ID) || r.Model == "" {
		return Rate{}, ErrInvalid
	}
	r.Asset = normalizedAsset(r.Asset)
	if e := validateRate(r); e != nil {
		return Rate{}, e
	}
	r.Status = "published"
	_, e := s.pool.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, r.ID, encode(r))
	if e != nil {
		var old Rate
		old, e = load[Rate](ctx, s.pool, `SELECT snapshot FROM billing_rates WHERE id=$1`, r.ID)
		old.Asset = normalizedAsset(old.Asset)
		if e == nil && hash(old) == hash(r) {
			return old, nil
		}
		return Rate{}, ErrConflict
	}
	return r, nil
}
func (s *Service) Rates(ctx context.Context) ([]Rate, error) {
	return list[Rate](ctx, s.pool, `SELECT snapshot FROM billing_rates WHERE status='published' ORDER BY id`)
}

func (s *Service) AllOffers(ctx context.Context, actor string) ([]Offer, error) {
	return s.OfferDrafts(ctx, actor)
}

// RetireRate removes only prospective rate selection. Published snapshots and
// existing call snapshots remain immutable for settlement/reconciliation.
func (s *Service) RetireRate(ctx context.Context, actor, id string) error {
	if e := s.operator(actor); e != nil {
		return e
	}
	result, e := s.pool.Exec(ctx, `UPDATE billing_rates SET status='retired' WHERE id=$1`, id)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return accounts.ErrNotFound
	}
	return nil
}
