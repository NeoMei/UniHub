package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
)

type creditAcceptanceCatalog struct{}

func (creditAcceptanceCatalog) Discover(_ context.Context, r modelaccess.ProviderCatalogRequest) (modelaccess.ProviderCatalogResponse, error) {
	return modelaccess.ProviderCatalogResponse{ProviderCatalogRequest: r, SchemaVersion: 1, CatalogVersion: "points-http-test", ModelScope: modelaccess.ScopeLLMRouterAvailable, PricingSource: modelaccess.PricingLLMRouterNative, NativeCreditProtocol: billing.NativeCreditPricingVersion, Models: []modelaccess.Model{{ID: "native-model", Operations: []string{"chat"}}}}, nil
}

func TestNativeCreditHTTPKeyCatalogAndSettlement(t *testing.T) {
	f := newBillingModesFixture(t, creditAcceptanceCatalog{})
	p := f.profile("hybrid")
	conversion := billing.NativeConversion{Version: "test-http-points-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
	p.NativeCreditConversion = &conversion
	p.Usage.RequireSubscription = false
	p.Usage.RequireSubscriptionForCredits = true
	p.Usage.FundingModes = []string{billing.FundingCredits, billing.FundingPrepaidCash}
	p.Usage.DefaultFundingMode = billing.FundingPrepaidCash
	f.request("PUT", "/ops/v1/products/superwagie/billing-profile", "alice", p, "", 200, nil)
	f.rate("native-credit-sale-fixture", billing.CreditAsset, "1")
	f.recharge("superwagie", "1000000", "points-membership")
	f.recharge("llmrouter", "2000000", "old-cash")
	f.offer("points-http-membership", "month", "100")
	f.request("POST", "/v1/accounts/"+f.account+"/api-keys", "product-alice", modelaccess.APIKeyRequest{Name: "No membership", ModelScope: modelaccess.ScopeLLMRouterAvailable, FundingMode: billing.FundingCredits}, "", http.StatusForbidden, nil)
	f.request("POST", "/v1/accounts/"+f.account+"/model-discovery", "product-alice", map[string]string{"model_scope": modelaccess.ScopeLLMRouterAvailable, "funding_mode": billing.FundingCredits}, "", http.StatusForbidden, nil)
	f.purchase("points-http-membership", "points-http-buy")
	path := "/v1/accounts/" + f.account
	var key modelaccess.APIKey
	f.request("POST", path+"/api-keys", "product-alice", modelaccess.APIKeyRequest{Name: "Explicit native points", ModelScope: modelaccess.ScopeLLMRouterAvailable, FundingMode: billing.FundingCredits, MaxTotal: "60", MaxPerCall: "50"}, "", http.StatusCreated, &key)
	if key.Asset != billing.CreditAsset || key.PricingSource != billing.NativeCreditPricingSource {
		t.Fatalf("key %+v", key)
	}
	var grant modelaccess.Claims
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, map[string]string{"token": key.Token, "model": "native-model", "instance_id": "default", "wire_operation": "chat.completions.create"}, "", 200, &grant)
	if grant.NativeCreditConversion == nil || *grant.NativeCreditConversion != conversion {
		t.Fatalf("grant %+v", grant)
	}
	var catalog modelaccess.Discovery
	f.request("POST", path+"/model-discovery", "product-alice", map[string]string{"model_scope": modelaccess.ScopeLLMRouterAvailable, "funding_mode": billing.FundingCredits}, "", 200, &catalog)
	if catalog.PricingSource != billing.NativeCreditPricingSource || catalog.NativeCreditConversion == nil {
		t.Fatalf("catalog %+v", catalog)
	}
	quote := billing.NativeQuote{PricingVersion: billing.NativeCreditPricingVersion, PricingDigest: strings.Repeat("a", 64), BillingModel: "native-model", WireOperation: "chat.completions.create", RouteID: "0", ChannelID: "1", Group: "default", NativeQuotaMax: "11", Conversion: conversion, Asset: billing.CreditAsset, MaxAmount: "17"}
	body := map[string]any{"credential": key.Token, "instance_id": "default", "task_id": "points-task", "call_id": "points-http-call", "request_digest": "points-http-digest", "model": "native-model", "wire_operation": quote.WireOperation, "quote": quote}
	var call billing.Call
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, body, "points-http-call", 200, &call)
	if call.Held != "17" || call.ChargeAsset != billing.CreditAsset || call.CashHoldAddress != "" || len(call.Allocations) != 1 {
		t.Fatalf("reserve %+v", call)
	}
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, body, "points-http-call", 200, &call)
	settle := billing.NativeSettlementRequest{PricingDigest: quote.PricingDigest, NativeQuota: "3", Amount: "5", ProviderCost: "7"}
	f.request("POST", "/internal/v1/calls/points-http-call/native-settle", billingModesServiceToken, settle, "points-http-call", 200, &call)
	if call.Charged != "5" || call.ProviderCost != "7" {
		t.Fatalf("settle %+v", call)
	}
	var batches []billing.Batch
	f.request("GET", path+"/credits", "product-alice", nil, "", 200, &batches)
	if len(batches) != 1 || batches[0].Available != "95" || batches[0].Consumed != "5" || batches[0].Reserved != "0" {
		t.Fatalf("credits %+v", batches)
	}
	f.balance("llmrouter", 2000000)
}
