package modelaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"unihub/internal/billing"
)

var ErrProviderCatalog = errors.New("provider model catalog unavailable")

type ProviderCatalogRequest struct {
	Purpose         string `json:"purpose,omitempty"`
	OriginProductID string `json:"origin_product_id"`
	InstanceID      string `json:"instance_id"`
	ActorID         string `json:"actor_id"`
	AccountID       string `json:"account_id"`
}

type ProviderCatalogResponse struct {
	NativeCreditProtocol string `json:"native_credit_protocol,omitempty"`
	ProviderCatalogRequest
	SchemaVersion  int     `json:"schema_version"`
	CatalogVersion string  `json:"catalog_version"`
	ModelScope     string  `json:"model_scope"`
	PricingSource  string  `json:"pricing_source"`
	Models         []Model `json:"models"`
}

type ProviderCatalog interface {
	Discover(context.Context, ProviderCatalogRequest) (ProviderCatalogResponse, error)
}

type providerCatalogClient struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewProviderCatalog pins the server endpoint and credential. Request fields
// cannot select a destination, route group or credential.
func NewProviderCatalog(endpoint, token string) (ProviderCatalog, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/internal/v1/provider-catalog" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) || len(token) < 32 || strings.ContainsAny(token, " \r\n\t") {
		return nil, billing.ErrInvalid
	}
	return &providerCatalogClient{endpoint: endpoint, token: token, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *providerCatalogClient) Discover(ctx context.Context, r ProviderCatalogRequest) (ProviderCatalogResponse, error) {
	raw, _ := json.Marshal(r)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if e != nil {
		return ProviderCatalogResponse{}, ErrProviderCatalog
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, e := c.client.Do(req)
	if e != nil {
		return ProviderCatalogResponse{}, ErrProviderCatalog
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ProviderCatalogResponse{}, ErrProviderCatalog
	}
	raw, e = io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if e != nil || len(raw) > 2<<20 {
		return ProviderCatalogResponse{}, ErrProviderCatalog
	}
	var out ProviderCatalogResponse
	if e = json.Unmarshal(raw, &out); e != nil || out.ProviderCatalogRequest != r || out.SchemaVersion != 1 || out.CatalogVersion == "" || len(out.CatalogVersion) > 256 || out.ModelScope != ScopeLLMRouterAvailable || out.PricingSource != PricingLLMRouterNative || out.Models == nil {
		return ProviderCatalogResponse{}, ErrProviderCatalog
	}
	seen := map[string]bool{}
	for _, model := range out.Models {
		if strings.TrimSpace(model.ID) == "" || len(model.ID) > 256 || seen[model.ID] {
			return ProviderCatalogResponse{}, ErrProviderCatalog
		}
		seen[model.ID] = true
	}
	return out, nil
}

func (s *Service) ConfigureProviderCatalog(c ProviderCatalog) { s.providerCatalog = c }

func (s *Service) nativeDiscovery(ctx context.Context, actor string, out Discovery) (Discovery, error) {
	if s.providerCatalog == nil {
		return Discovery{}, ErrProviderCatalog
	}
	catalog, e := s.providerCatalog.Discover(ctx, ProviderCatalogRequest{OriginProductID: out.OriginProductID, InstanceID: out.InstanceID, ActorID: actor, AccountID: out.AccountID})
	if e != nil {
		return Discovery{}, e
	}
	if out.PricingSource == PricingLLMRouterNativeCredit {
		if catalog.NativeCreditProtocol != billing.NativeCreditPricingVersion {
			return Discovery{}, ErrProviderCatalog
		}
		out.NativeCreditConversion, e = s.nativeCreditConversionOn(ctx, s.pool, out.OriginProductID)
		if e != nil {
			return Discovery{}, e
		}
	}
	out.Models = catalog.Models
	out.SchemaVersion = catalog.SchemaVersion
	out.CatalogVersion = catalog.CatalogVersion
	out.CatalogSource = "llmrouter"
	out.ModelScope = ScopeLLMRouterAvailable
	if out.PricingSource == "" {
		out.PricingSource = PricingLLMRouterNative
	}
	return out, nil
}

func (s *Service) DiscoverAccountWithScope(ctx context.Context, actor, account, product, instance, scope string) (Discovery, error) {
	return s.DiscoverAccountWithFundingScope(ctx, actor, account, product, instance, scope, "")
}

func (s *Service) DiscoverAccountWithFundingScope(ctx context.Context, actor, account, product, instance, scope, funding string) (Discovery, error) {
	if scope == "" || scope == ScopeSelected {
		if funding != "" {
			return Discovery{}, billing.ErrInvalid
		}
		return s.DiscoverAccount(ctx, actor, account, product, instance)
	}
	if scope != ScopeLLMRouterAvailable {
		return Discovery{}, billing.ErrInvalid
	}
	auth, e := s.nativeAuthorityOn(ctx, s.pool, actor, account, product, instance, funding)
	if e != nil {
		return Discovery{}, e
	}

	return s.nativeDiscovery(ctx, actor, Discovery{PricingSource: nativePricingSource(auth.Asset), AccountID: account, InstanceID: instance, OriginProductID: product, ExpiresAt: auth.ExpiresAt})
}

// NativeCreditProtocol is a service-authenticated runtime negotiation. No
// account or model credential is fabricated for this deployment check.
func (c *providerCatalogClient) NativeCreditProtocol(ctx context.Context, product string) (string, error) {
	out, e := c.Discover(ctx, ProviderCatalogRequest{OriginProductID: product, Purpose: "native_credit_readiness"})
	return out.NativeCreditProtocol, e
}
