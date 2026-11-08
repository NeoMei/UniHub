package modelaccess

import (
	"context"
	"unihub/internal/accounts"
	"unihub/internal/billing"
)

type nativeCreditEligibility interface {
	NativeCreditsReadinessOn(context.Context, accounts.Querier, string) (billing.NativeCreditReadiness, error)
}

func nativePricingSource(asset string) string {
	if asset == billing.CreditAsset {
		return PricingLLMRouterNativeCredit
	}
	return PricingLLMRouterNative
}

func nativeFundingMatches(mode, asset, pricing string) bool {
	return mode == billing.FundingPrepaidCash && asset == billing.CashAsset && pricing == PricingLLMRouterNative || mode == billing.FundingCredits && asset == billing.CreditAsset && pricing == PricingLLMRouterNativeCredit
}

func (s *Service) nativeCreditConversionOn(ctx context.Context, q accounts.Querier, product string) (*billing.NativeConversion, error) {
	policy, ok := s.eligibility.(nativeCreditEligibility)
	if !ok {
		return nil, accounts.ErrForbidden
	}
	ready, e := policy.NativeCreditsReadinessOn(ctx, q, product)
	if e != nil {
		return nil, e
	}
	if !ready.Ready || ready.Conversion == nil {
		return nil, accounts.ErrForbidden
	}
	return ready.Conversion, nil
}

// NativeCreditsReadiness combines authoritative product policy and the actual
// provider catalog's advertised protocol. It deliberately does not require an
// existing subscription: the first membership must be purchasable.
func (s *Service) NativeCreditsReadiness(ctx context.Context, actor, account, product, instance string) (billing.NativeCreditReadiness, error) {
	policy, ok := s.eligibility.(nativeCreditEligibility)
	if !ok {
		return billing.NativeCreditReadiness{Reason: "native_credits_disabled"}, nil
	}
	ready, e := policy.NativeCreditsReadinessOn(ctx, s.pool, product)
	if e != nil || !ready.Ready {
		return ready, e
	}
	if s.providerCatalog == nil {
		return billing.NativeCreditReadiness{Reason: "native_credit_provider_unavailable"}, nil
	}
	catalog, e := s.providerCatalog.Discover(ctx, ProviderCatalogRequest{ActorID: actor, AccountID: account, OriginProductID: product, InstanceID: instance})
	if e != nil {
		return billing.NativeCreditReadiness{Reason: "native_credit_provider_unavailable"}, nil
	}
	if catalog.NativeCreditProtocol != billing.NativeCreditPricingVersion {
		return billing.NativeCreditReadiness{Reason: "native_credit_protocol_unsupported"}, nil
	}
	return ready, nil
}

// NativeCreditsRuntimeReadiness is the startup/checkout runtime gate. The
// commercial caller owns finance authorization and rechecks policy under lock.
func (s *Service) NativeCreditsRuntimeReadiness(ctx context.Context, product string) (billing.NativeCreditReadiness, error) {
	policy, ok := s.eligibility.(nativeCreditEligibility)
	if !ok {
		return billing.NativeCreditReadiness{Reason: "native_credits_disabled"}, nil
	}
	ready, e := policy.NativeCreditsReadinessOn(ctx, s.pool, product)
	if e != nil || !ready.Ready {
		return ready, e
	}
	provider, ok := s.providerCatalog.(interface {
		NativeCreditProtocol(context.Context, string) (string, error)
	})
	if !ok {
		return billing.NativeCreditReadiness{Reason: "native_credit_provider_unavailable"}, nil
	}
	protocol, e := provider.NativeCreditProtocol(ctx, product)
	if e != nil {
		return billing.NativeCreditReadiness{Reason: "native_credit_provider_unavailable"}, nil
	}
	if protocol != billing.NativeCreditPricingVersion {
		return billing.NativeCreditReadiness{Reason: "native_credit_protocol_unsupported"}, nil
	}
	return ready, nil
}
