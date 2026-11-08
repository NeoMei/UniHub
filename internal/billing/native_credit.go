package billing

import (
	"context"
	"regexp"
	"slices"
	"unihub/internal/accounts"
)

const NativeCreditPricingSource = "llmrouter_native_credit"
const NativeCreditPricingVersion = "lr-native-credit-v1"

var nativeConversionVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func validNativeCreditConversion(c NativeConversion) bool {
	if !nativeConversionVersion.MatchString(c.Version) || c.Version == "lr-quota-usd-cny-v1" || c.Rounding != "lr-cumulative-ceil-v1" {
		return false
	}
	_, n := integer(c.Numerator, true)
	_, d := integer(c.Denominator, true)
	return n == nil && d == nil
}

// NativeCreditReadiness describes server policy, not provider reachability,
// account membership, balance, or a production merchant acceptance result.
type NativeCreditReadiness struct {
	Ready      bool              `json:"ready"`
	Reason     string            `json:"reason,omitempty"`
	Conversion *NativeConversion `json:"conversion,omitempty"`
}

func (s *Service) NativeCreditsReadiness(ctx context.Context, product string) (NativeCreditReadiness, error) {
	return s.NativeCreditsReadinessOn(ctx, s.pool, product)
}

func (s *Service) NativeCreditsReadinessOn(ctx context.Context, q accounts.Querier, product string) (NativeCreditReadiness, error) {
	p, e := s.ProfileOn(ctx, q, product)
	if e != nil {
		return NativeCreditReadiness{}, e
	}
	if product != "superwagie" || p.Version < 1 || !p.Usage.Enabled || !slices.Contains(p.Usage.FundingModes, FundingCredits) {
		return NativeCreditReadiness{Reason: "native_credits_disabled"}, nil
	}
	if p.NativeCreditConversion == nil || !validNativeCreditConversion(*p.NativeCreditConversion) {
		return NativeCreditReadiness{Reason: "native_credit_conversion_unconfigured"}, nil
	}
	conversion := *p.NativeCreditConversion
	return NativeCreditReadiness{Ready: true, Conversion: &conversion}, nil
}

// Product policy versions are append-only; a tariff identifier cannot acquire a
// different ratio even after an intervening version or temporary removal.
func validateNativeCreditHistory(ctx context.Context, q accounts.Querier, p BillingProfile) error {
	if p.NativeCreditConversion == nil {
		return nil
	}
	var changed bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_billing_profiles WHERE product_id=$1 AND snapshot#>>'{native_credit_conversion,version}'=$2 AND snapshot->'native_credit_conversion'<>$3::jsonb)`, p.ProductID, p.NativeCreditConversion.Version, encode(p.NativeCreditConversion)).Scan(&changed)
	if e != nil {
		return e
	}
	if changed {
		return ErrConflict
	}
	return nil
}
