package billing

import (
	"math"

	"unihub/internal/ledger"
)

const productCreditsPerCNY int64 = 100 // CNY/6 and CREDIT/6 use the same scale.

func validateProductCreditOffer(o Offer) error {
	if o.CreditScope == "" {
		if o.ServiceAmount != "" || o.ComputeAmount != "" {
			return ErrInvalid
		}
		return nil
	}
	if o.CreditScope != ProductCreditScopeV1 {
		return ErrInvalid
	}
	service, serviceErr := integer(o.ServiceAmount, false)
	compute, computeErr := integer(o.ComputeAmount, true)
	price, _ := integer(o.Price, true)
	monthly, _ := integer(o.MonthlyCredit, true)
	if serviceErr != nil || computeErr != nil || service > price || compute != price-service || compute > math.MaxInt64/productCreditsPerCNY {
		return ErrInvalid
	}
	credits := compute * productCreditsPerCNY
	switch {
	case o.Kind == "addon":
		if service != 0 || o.ValidityMonths != 1 || monthly != credits {
			return ErrInvalid
		}
	case o.Period == "year":
		if monthly > math.MaxInt64/12 || monthly*12 != credits {
			return ErrInvalid
		}
	default:
		if monthly != credits {
			return ErrInvalid
		}
	}
	return nil
}

func freezeProductCreditOrder(o *Order) error {
	if o.Offer.CreditScope == "" {
		if o.AutoAddonPolicyVersion != 0 {
			return ErrInvalid
		}
		return nil
	}
	if (o.Kind == "upgrade" && o.ProductCreditUpgrade == nil) || o.GrantAmount != o.Offer.MonthlyCredit || o.ProductID != o.Offer.ProductID {
		return ErrInvalid
	}
	if e := validateProductCreditOffer(o.Offer); e != nil {
		return e
	}
	o.CreditScope = o.Offer.CreditScope
	o.ServiceAmount, o.ComputeAmount = o.Offer.ServiceAmount, o.Offer.ComputeAmount
	if !hasLegacyNetUpgrade(o.ProductCreditUpgrade) {
		if o.Amount != o.Offer.Price {
			return ErrInvalid
		}
		return nil
	}
	net, netErr := integer(o.Amount, true)
	applied, appliedErr := integer(o.ProductCreditUpgrade.AppliedCreditAmount, false)
	gross, _ := integer(o.Offer.Price, true)
	if netErr != nil || appliedErr != nil || o.Kind != "upgrade" || o.ProductCreditUpgrade.EstimatedRefundAmount != "0" ||
		net > gross || applied != gross-net {
		return ErrInvalid
	}
	// A carried credit reduces the price. The previous cash can be in a
	// retained ledger, so fund only the new cash and allocate compute first.
	nominalCompute, _ := integer(o.Offer.ComputeAmount, true)
	compute := min(net, nominalCompute)
	o.ServiceAmount, o.ComputeAmount = str(net-compute), str(compute)
	return nil
}

func hasLegacyNetUpgrade(upgrade *ProductCreditUpgrade) bool {
	return upgrade != nil && upgrade.AppliedCreditAmount != ""
}

// Separate the prepaid compute by original order so the exact funding and
// reversal remain auditable even as its grants span multiple calendar months.
func productComputePrepay(o Order) string {
	return "commercial:compute_prepay:" + o.ProductID + ":" + o.ID
}

type orderCashAllocation struct {
	address string
	amount  int64
}

func orderCashAllocations(o Order) ([]orderCashAllocation, error) {
	revenue := "commercial:revenue:" + o.ProductID
	amount, e := integer(o.Amount, true)
	if e != nil {
		return nil, e
	}
	if o.CreditScope == "" {
		return []orderCashAllocation{{revenue, amount}}, nil
	}
	if o.CreditScope != ProductCreditScopeV1 {
		return nil, ErrInvalid
	}
	service, serviceErr := integer(o.ServiceAmount, false)
	compute, computeErr := integer(o.ComputeAmount, true)
	if serviceErr != nil || computeErr != nil || service > amount || compute != amount-service {
		return nil, ErrConflict
	}
	return []orderCashAllocation{{revenue, service}, {productComputePrepay(o), compute}}, nil
}

func orderPaymentPostings(o Order, source string) ([]ledger.Posting, error) {
	allocation, e := orderCashAllocations(o)
	if e != nil {
		return nil, e
	}
	posts := make([]ledger.Posting, 0, len(allocation))
	for _, a := range allocation {
		if a.amount > 0 {
			posts = append(posts, ledger.Posting{Source: source, Destination: a.address, Asset: CashAsset, Amount: a.amount})
		}
	}
	return posts, nil
}

func orderRefundPostings(o Order, destinations []orderCashAllocation) ([]ledger.Posting, error) {
	sources, e := orderCashAllocations(o)
	if e != nil {
		return nil, e
	}
	posts := []ledger.Posting{}
	for _, d := range destinations {
		remaining := d.amount
		for i := range sources {
			s := &sources[i]
			take := min(s.amount, remaining)
			if take > 0 {
				posts = append(posts, ledger.Posting{Source: s.address, Destination: d.address, Asset: CashAsset, Amount: take})
				remaining -= take
				s.amount -= take
			}
		}
		if remaining != 0 {
			return nil, ErrConflict
		}
	}
	for _, s := range sources {
		if s.amount != 0 {
			return nil, ErrConflict
		}
	}
	return posts, nil
}
