package billing

import (
	"context"
	"errors"
	"slices"
	"unihub/internal/accounts"
)

var ErrCreditRatesUnavailable = errors.New("billing: credit_rates_unavailable")

type SalesReadiness struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

// CatalogOffer is a transport view; immutable offer snapshots retain their hash.
type CatalogOffer struct {
	Offer
	SalesReadiness SalesReadiness `json:"sales_readiness"`
}

func creditSaleReadiness(p BillingProfile, rates []Rate) SalesReadiness {
	unavailable := SalesReadiness{Reason: "credit_rates_unavailable"}
	if !p.Usage.Enabled || !slices.Contains(p.Usage.FundingModes, FundingCredits) {
		return unavailable
	}
	for _, r := range rates {
		if r.ID == "" || r.Model == "" || r.Status != "published" || normalizedAsset(r.Asset) != CreditAsset {
			continue
		}
		if p.Usage.Models != nil && !slices.Contains(p.Usage.Models, r.Model) {
			continue
		}
		if validateRate(r) != nil || !ModelRatesUnambiguous(rates, r.Model, CreditAsset) {
			continue
		}
		for _, wire := range []string{"chat.completions.create", "responses.create", "rerank.create", "multimodal.embeddings.create", "embeddings.create", "audio.speech.create", "audio.transcriptions.create", "images.generations.create", "images.edits.create", "ocr.jobs.create"} {
			if RateMatchesWire(r, wire) {
				return SalesReadiness{Ready: true}
			}
		}
	}
	return unavailable
}

func (s *Service) saleReadinessOn(ctx context.Context, q accounts.Querier, o Offer) (SalesReadiness, error) {
	n, e := integer(o.MonthlyCredit, false)
	if e != nil {
		return SalesReadiness{}, e
	}
	if n == 0 {
		return SalesReadiness{Ready: true}, nil
	}
	p, e := s.ProfileOn(ctx, q, o.ProductID)
	if e != nil {
		return SalesReadiness{}, e
	}
	rates, e := load[[]Rate](ctx, q, `SELECT COALESCE(jsonb_agg(snapshot ORDER BY id),'[]'::jsonb) FROM billing_rates WHERE status='published'`)
	if e != nil {
		return SalesReadiness{}, e
	}
	return creditSaleReadiness(p, rates), nil
}

func (s *Service) requireSaleReadyOn(ctx context.Context, q accounts.Querier, o Offer) error {
	r, e := s.saleReadinessOn(ctx, q, o)
	if e != nil {
		return e
	}
	if !r.Ready {
		return ErrCreditRatesUnavailable
	}
	return nil
}

func (s *Service) CatalogView(ctx context.Context) ([]CatalogOffer, error) {
	offers, e := s.Catalog(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]CatalogOffer, 0, len(offers))
	for _, o := range offers {
		r, e := s.saleReadinessOn(ctx, s.pool, o)
		if e != nil {
			return nil, e
		}
		out = append(out, CatalogOffer{Offer: o, SalesReadiness: r})
	}
	return out, nil
}
