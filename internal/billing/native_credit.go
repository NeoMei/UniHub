package billing

import (
	"context"
	"errors"
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

// Product quotes use already-rounded microcredits. A second currency
// conversion would round again or change the frozen public tariff.
func exactProductCreditConversion(c *NativeConversion) bool {
	return c != nil && c.Numerator == "1" && c.Denominator == "1"
}

// NativeCreditReadiness describes server policy, not provider reachability,
// account membership, balance, or a production merchant acceptance result.
type NativeCreditReadiness struct {
	CreditScope string            `json:"credit_scope,omitempty"`
	Ready       bool              `json:"ready"`
	Reason      string            `json:"reason,omitempty"`
	Conversion  *NativeConversion `json:"conversion,omitempty"`
}

func (s *Service) NativeCreditsReadiness(ctx context.Context, product string) (NativeCreditReadiness, error) {
	return s.NativeCreditsReadinessOn(ctx, s.pool, product)
}

func (s *Service) NativeCreditsReadinessOn(ctx context.Context, q accounts.Querier, product string) (NativeCreditReadiness, error) {
	p, e := s.ProfileOn(ctx, q, product)
	if e != nil {
		return NativeCreditReadiness{}, e
	}
	return s.nativeCreditReadinessForProfile(ctx, q, p, p.CreditScope)
}

func (s *Service) NativeCreditsReadinessForScopeOn(ctx context.Context, q accounts.Querier, product, scope string) (NativeCreditReadiness, error) {
	p, e := s.ProfileOn(ctx, q, product)
	if e != nil {
		return NativeCreditReadiness{}, e
	}
	return s.nativeCreditReadinessForProfile(ctx, q, p, scope)
}

func (s *Service) nativeCreditReadinessForProfile(ctx context.Context, q accounts.Querier, p BillingProfile, scope string) (NativeCreditReadiness, error) {
	validScope := scope == "" && p.ProductID == "superwagie" || scope == ProductCreditScopeV1 && p.CreditScope == ProductCreditScopeV1
	if !validScope || p.Version < 1 || !p.Usage.Enabled || !slices.Contains(p.Usage.FundingModes, FundingCredits) {
		return NativeCreditReadiness{Reason: "native_credits_disabled"}, nil
	}
	conversion := p.NativeCreditConversion
	if scope == "" && p.CreditScope == ProductCreditScopeV1 {
		// Enrollment in the product wallet leaves the latest legacy tariff on
		// its own lane. Existing native-credit Keys never switch to microcredits.
		legacy, e := load[BillingProfile](ctx, q, `SELECT snapshot FROM product_billing_profiles WHERE product_id=$1 AND COALESCE(snapshot->>'credit_scope','')='' ORDER BY version DESC LIMIT 1`, p.ProductID)
		if e != nil && !errors.Is(e, accounts.ErrNotFound) {
			return NativeCreditReadiness{}, e
		}
		conversion = legacy.NativeCreditConversion
	}
	if conversion == nil || !validNativeCreditConversion(*conversion) || scope == ProductCreditScopeV1 && !exactProductCreditConversion(conversion) {
		return NativeCreditReadiness{Reason: "native_credit_conversion_unconfigured"}, nil
	}
	return NativeCreditReadiness{Ready: true, CreditScope: scope, Conversion: conversion}, nil
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
