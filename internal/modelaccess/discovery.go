package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/billing"
)

// Model is the public, capability-only model projection used by product
// clients. It deliberately contains no credential or provider configuration.
type Model struct {
	CapabilityMetadata json.RawMessage `json:"capability_metadata,omitempty"`
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Operations         []string        `json:"operations"`
	InputModalities    []string        `json:"input_modalities"`
	OutputModalities   []string        `json:"output_modalities"`
	Context            int64           `json:"context"`
	MaxOutput          int64           `json:"max_output"`
	Probe              bool            `json:"probe"`
	RateID             string          `json:"rate_id"`
}

// Discovery is safe to return to a trusted product client. Token material is
// intentionally absent; callers may persist this response as public state.
type Discovery struct {
	NativeCreditConversion *billing.NativeConversion `json:"native_credit_conversion,omitempty"`
	SchemaVersion          int                       `json:"schema_version,omitempty"`
	CatalogVersion         string                    `json:"catalog_version,omitempty"`
	CatalogSource          string                    `json:"catalog_source,omitempty"`
	ModelScope             string                    `json:"model_scope,omitempty"`
	PricingSource          string                    `json:"pricing_source,omitempty"`
	CredentialVersion      int64                     `json:"credential_version,omitempty"`
	AccountID              string                    `json:"account_id"`
	InstanceID             string                    `json:"instance_id"`
	OriginProductID        string                    `json:"origin_product_id"`
	ExpiresAt              time.Time                 `json:"expires_at"`
	Models                 []Model                   `json:"models"`
}

// ProjectCatalog projects an allowlisted model set against the published rate
// catalog. A model is returned only when it has an unambiguous published rate
// for the authorized asset. The first rate is selected deterministically.
func ProjectCatalog(allowlist []string, rates []billing.Rate) []Model {
	if len(allowlist) == 0 {
		return []Model{}
	}
	allowed := make(map[string]bool, len(allowlist))
	for _, model := range allowlist {
		if strings.TrimSpace(model) != "" {
			allowed[model] = true
		}
	}
	byModel := map[string]Model{}
	for _, rate := range rates {
		asset := rate.Asset
		if asset == "" {
			asset = billing.CreditAsset
		}
		if rate.Status != "published" || !allowed[rate.Model] || rate.Model == "" || rate.ID == "" || asset != billing.CreditAsset && asset != billing.CashAsset {
			continue
		}
		if !billing.ModelRatesUnambiguous(rates, rate.Model, asset) {
			continue
		}
		operations, input, output, probe := operationCapabilities(rate)
		projected, exists := byModel[rate.Model]
		if !exists {
			byModel[rate.Model] = Model{ID: rate.Model, Name: rate.Model, Operations: operations, InputModalities: input, OutputModalities: output, Probe: probe, RateID: rate.ID}
			continue
		}
		projected.Operations = appendUnique(projected.Operations, operations...)
		projected.InputModalities = appendUnique(projected.InputModalities, input...)
		projected.OutputModalities = appendUnique(projected.OutputModalities, output...)
		projected.Probe = projected.Probe || probe
		byModel[rate.Model] = projected
	}
	out := make([]Model, 0, len(byModel))
	for _, model := range allowlist {
		if projected, ok := byModel[model]; ok {
			out = append(out, projected)
		}
	}
	return out
}

func operationCapabilities(rate billing.Rate) ([]string, []string, []string, bool) {
	if rate.Metered == nil {
		return []string{"chat"}, []string{"text"}, []string{"text"}, true
	}
	op := rate.Metered.Operation
	input, output := []string{"text"}, []string{"text"}
	probe := true
	switch op {
	case "embedding":
		output = []string{"embedding"}
	case "rerank":
		output = []string{"relevance"}
	case "speech":
		input, output, probe = []string{"text"}, []string{"audio"}, false
	case "transcription":
		input, output, probe = []string{"audio"}, []string{"text"}, false
	case "image_generation", "image_edit":
		input, output, probe = []string{"text"}, []string{"image"}, false
	case "ocr":
		input, output, probe = []string{"image"}, []string{"text"}, false
	case "video":
		input, output, probe = []string{"text"}, []string{"video"}, false
	}
	return []string{op}, input, output, probe
}

func validKeyToken(token string) bool {
	return len(token) == 71 && strings.HasPrefix(token, "uh_key_") && !strings.ContainsAny(token, " \r\n\t")
}

// DiscoverAPIKey validates the complete immutable scope of an API key before
// returning any model metadata. expectedProduct and expectedInstance, when
// supplied, are hard checks used by product gateways.
func (s *Service) DiscoverAPIKey(ctx context.Context, token, expectedProduct, expectedInstance string) (Discovery, error) {
	if !validKeyToken(token) {
		return Discovery{}, ErrCredential
	}
	var out Discovery
	var data []byte
	var funding, asset string
	var actor string
	e := s.pool.QueryRow(ctx, `SELECT actor_id,account_id,origin_product_id,instance_id,models,expires_at,funding_mode,asset,model_scope,pricing_source,credential_version FROM model_credentials WHERE token_hash=$1 AND credential_kind='api_key' AND revoked_at IS NULL AND expires_at>now()`, digest(token)).Scan(&actor, &out.AccountID, &out.OriginProductID, &out.InstanceID, &data, &out.ExpiresAt, &funding, &asset, &out.ModelScope, &out.PricingSource, &out.CredentialVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		return Discovery{}, ErrCredential
	}
	if e != nil {
		return Discovery{}, e
	}
	out.ExpiresAt = out.ExpiresAt.UTC()
	if expectedProduct != "" && expectedProduct != out.OriginProductID || expectedInstance != "" && expectedInstance != out.InstanceID {
		return Discovery{}, ErrCredential
	}
	if e = s.activeOn(ctx, s.pool, actor); e != nil {
		return Discovery{}, ErrCredential
	}
	if _, e = s.accounts.Role(ctx, actor, out.AccountID); e != nil {
		return Discovery{}, ErrCredential
	}
	if e = s.apiKeyProductActive(ctx, s.pool, actor, out.OriginProductID, out.InstanceID); e != nil {
		return Discovery{}, ErrCredential
	}
	auth, e := s.authorize(ctx, actor, out.AccountID, out.OriginProductID, funding)
	if e != nil || auth.Asset != asset {
		return Discovery{}, ErrCredential
	}
	if out.ModelScope == ScopeLLMRouterAvailable {
		if _, e := s.nativeAuthorityOn(ctx, s.pool, actor, out.AccountID, out.OriginProductID, out.InstanceID, funding); e != nil || !nativeFundingMatches(funding, asset, out.PricingSource) {
			return Discovery{}, ErrCredential
		}
		return s.nativeDiscovery(ctx, actor, out)
	}
	var keyModels []string
	if e = json.Unmarshal(data, &keyModels); e != nil {
		return Discovery{}, ErrCredential
	}
	allowlist, e := s.productModels(ctx, s.pool, out.OriginProductID)
	if e != nil {
		return Discovery{}, ErrCredential
	}
	rates, e := s.eligibility.Rates(ctx)
	if e != nil {
		return Discovery{}, e
	}
	filtered := make([]string, 0, len(keyModels))
	for _, model := range keyModels {
		if slices.Contains(allowlist, model) && billing.ModelRatesUnambiguous(rates, model, asset) {
			filtered = append(filtered, model)
		}
	}
	out.Models = ProjectCatalogForAsset(filtered, rates, asset)
	return out, nil
}

func appendUnique(dst []string, values ...string) []string {
	for _, value := range values {
		if !slices.Contains(dst, value) {
			dst = append(dst, value)
		}
	}
	return dst
}

// DiscoverAccount reads the same catalog for a signed-in product client. The
// caller must already be bound to the product instance by the HTTP policy.
func (s *Service) DiscoverAccount(ctx context.Context, actor, account, product, instance string) (Discovery, error) {
	if product == "" || instance == "" || s.productUsers == nil || !s.productUsers.Managed(product, instance) {
		return Discovery{}, accounts.ErrForbidden
	}
	if e := s.activeOn(ctx, s.pool, actor); e != nil {
		return Discovery{}, e
	}
	if _, e := s.accounts.Role(ctx, actor, account); e != nil {
		return Discovery{}, e
	}
	if e := s.apiKeyProductActive(ctx, s.pool, actor, product, instance); e != nil {
		return Discovery{}, e
	}
	auth, e := s.authorize(ctx, actor, account, product, "")
	if e != nil {
		return Discovery{}, e
	}
	allowlist, e := s.productModels(ctx, s.pool, product)
	if e != nil {
		return Discovery{}, e
	}
	rates, e := s.eligibility.Rates(ctx)
	if e != nil {
		return Discovery{}, e
	}
	return Discovery{AccountID: account, InstanceID: instance, OriginProductID: product, ExpiresAt: auth.ExpiresAt, Models: ProjectCatalogForAsset(allowlist, rates, auth.Asset)}, nil
}

func ProjectCatalogForAsset(allowlist []string, rates []billing.Rate, asset string) []Model {
	filtered := make([]billing.Rate, 0, len(rates))
	for _, rate := range rates {
		rateAsset := rate.Asset
		if rateAsset == "" {
			rateAsset = billing.CreditAsset
		}
		if rateAsset == asset {
			filtered = append(filtered, rate)
		}
	}
	return ProjectCatalog(allowlist, filtered)
}
