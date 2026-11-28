package unihub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled           bool
	InternalURL       string
	ServiceToken      string
	GatewayVersion    string
	Region            string
	Currency          string
	ExecutorID        string
	UpstreamSimulator bool
	UpstreamBaseURL   string
	UpstreamAPIKey    string
	CatalogCacheTTL   time.Duration
}

func ConfigFromEnv() Config {
	ttlMs := 0
	if raw := strings.TrimSpace(os.Getenv("UNIHUB_CATALOG_CACHE_MS")); raw != "" {
		_, _ = fmt.Sscanf(raw, "%d", &ttlMs)
	}
	gwVer := strings.TrimSpace(os.Getenv("UNIHUB_GATEWAY_VERSION"))
	if gwVer == "" {
		gwVer = "1.0"
	}
	region := strings.TrimSpace(os.Getenv("UNIHUB_REGION"))
	if region == "" {
		region = "cn"
	}
	currency := strings.TrimSpace(os.Getenv("UNIHUB_CURRENCY"))
	if currency == "" {
		if region == "us" {
			currency = "USD"
		} else {
			currency = "CNY"
		}
	}
	execID := strings.TrimSpace(os.Getenv("UNIHUB_EXECUTOR_ID"))
	if execID == "" {
		execID = "wanliu"
	}
	return Config{
		Enabled:           strings.TrimSpace(os.Getenv("UNIHUB_V2_BILLING_ENABLED")) == "true",
		InternalURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("UNIHUB_INTERNAL_URL")), "/"),
		ServiceToken:      strings.TrimSpace(os.Getenv("UNIHUB_GATEWAY_SERVICE_TOKEN")),
		GatewayVersion:    gwVer,
		Region:            region,
		Currency:          currency,
		ExecutorID:        execID,
		UpstreamSimulator: strings.TrimSpace(os.Getenv("UNIHUB_UPSTREAM_SIMULATOR")) == "true",
		UpstreamBaseURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("UNIHUB_UPSTREAM_BASE_URL")), "/"),
		UpstreamAPIKey:    strings.TrimSpace(os.Getenv("UNIHUB_UPSTREAM_API_KEY")),
		CatalogCacheTTL:   time.Duration(ttlMs) * time.Millisecond,
	}
}

type RouteRateSpec struct {
	RouteID          string
	ModelID          string
	Operation        string
	ProviderCurrency string
	RateDecimal      string // per 1K tokens for token models, or per 1 unit for image/ocr
	IsPer1KTokens    bool
}

func DefaultRouteSpecs(regionCurrency string) map[string]RouteRateSpec {
	if regionCurrency == "USD" {
		return map[string]RouteRateSpec{
			"ali:text-embedding-v4":  {RouteID: "ali:text-embedding-v4", ModelID: "text-embedding-v4", Operation: "embedding", ProviderCurrency: "USD", RateDecimal: "0.00007", IsPer1KTokens: true},
			"ali:gte-rerank-v2":      {RouteID: "ali:gte-rerank-v2", ModelID: "gte-rerank-v2", Operation: "rerank", ProviderCurrency: "USD", RateDecimal: "0.00014", IsPer1KTokens: true},
			"volc:seedream-4.0":      {RouteID: "volc:seedream-4.0", ModelID: "seedream-4.0", Operation: "image_generation", ProviderCurrency: "USD", RateDecimal: "0.03", IsPer1KTokens: false},
			"ali:qwen3-vl-embedding": {RouteID: "ali:qwen3-vl-embedding", ModelID: "qwen3-vl-embedding", Operation: "multimodal_embedding", ProviderCurrency: "USD", RateDecimal: "0.00028", IsPer1KTokens: true},
			"baidu:paddleocr-vl":     {RouteID: "baidu:paddleocr-vl", ModelID: "paddleocr-vl", Operation: "ocr_parse", ProviderCurrency: "USD", RateDecimal: "0.005", IsPer1KTokens: false},
		}
	}
	return map[string]RouteRateSpec{
		"ali:text-embedding-v4":  {RouteID: "ali:text-embedding-v4", ModelID: "text-embedding-v4", Operation: "embedding", ProviderCurrency: "CNY", RateDecimal: "0.0005", IsPer1KTokens: true},
		"ali:gte-rerank-v2":      {RouteID: "ali:gte-rerank-v2", ModelID: "gte-rerank-v2", Operation: "rerank", ProviderCurrency: "CNY", RateDecimal: "0.001", IsPer1KTokens: true},
		"volc:seedream-4.0":      {RouteID: "volc:seedream-4.0", ModelID: "seedream-4.0", Operation: "image_generation", ProviderCurrency: "CNY", RateDecimal: "0.20", IsPer1KTokens: false},
		"ali:qwen3-vl-embedding": {RouteID: "ali:qwen3-vl-embedding", ModelID: "qwen3-vl-embedding", Operation: "multimodal_embedding", ProviderCurrency: "CNY", RateDecimal: "0.002", IsPer1KTokens: true},
		"baidu:paddleocr-vl":     {RouteID: "baidu:paddleocr-vl", ModelID: "paddleocr-vl", Operation: "ocr_parse", ProviderCurrency: "CNY", RateDecimal: "0.03", IsPer1KTokens: false},
		"deepseek:deepseek-v4-flash": {
			RouteID: "deepseek:deepseek-v4-flash", ModelID: "deepseek-v4-flash", Operation: "chat",
			ProviderCurrency: "CNY", RateDecimal: "0.002", IsPer1KTokens: true,
		},
		"deepseek:deepseek-v4-pro": {
			RouteID: "deepseek:deepseek-v4-pro", ModelID: "deepseek-v4-pro", Operation: "chat",
			ProviderCurrency: "CNY", RateDecimal: "0.008", IsPer1KTokens: true,
		},
		"glm:glm-5.2": {
			RouteID: "glm:glm-5.2", ModelID: "glm-5.2", Operation: "chat",
			ProviderCurrency: "CNY", RateDecimal: "0.005", IsPer1KTokens: true,
		},
		"kimi:k3": {
			RouteID: "kimi:k3", ModelID: "k3", Operation: "chat",
			ProviderCurrency: "CNY", RateDecimal: "0.006", IsPer1KTokens: true,
		},
		"telecom:Text-Embedding-V4-A": {
			RouteID: "telecom:Text-Embedding-V4-A", ModelID: "Text-Embedding-V4-A", Operation: "embedding",
			ProviderCurrency: "CNY", RateDecimal: "0.0005", IsPer1KTokens: true,
		},
	}
}

type CatalogRoute struct {
	RouteID           string `json:"routeId"`
	Operation         string `json:"operation"`
	Unit              string `json:"unit"`
	MeterVersion      string `json:"meterVersion"`
	RouterRateVersion string `json:"routerRateVersion"`
	UnitLedgerUnits   string `json:"unitLedgerUnits"`
	MinLedgerUnits    string `json:"minLedgerUnits"`
}

type CatalogModel struct {
	ModelID    string          `json:"modelId"`
	Operations []string        `json:"operations"`
	Pricing    json.RawMessage `json:"pricing"`
	Routes     []CatalogRoute  `json:"routes"`
}

type CatalogResponse struct {
	App struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"app"`
	AccountKind        string         `json:"accountKind"`
	Region             string         `json:"region"`
	Currency           string         `json:"currency"`
	HasAppSeat         bool           `json:"hasAppSeat"`
	PricingVersionID   string         `json:"pricingVersionId"`
	PricingVersionCode string         `json:"pricingVersionCode"`
	CreditsPerCurrency int            `json:"creditsPerCurrency"`
	Models             []CatalogModel `json:"models"`
}

type HoldRequest struct {
	APIKey              string `json:"apiKey"`
	RequestID           string `json:"requestId"`
	RouteID             string `json:"routeId"`
	MeterVersion        string `json:"meterVersion"`
	RouterRateVersion   string `json:"routerRateVersion"`
	BaseAmount          string `json:"baseAmount"`
	ProviderCurrency    string `json:"providerCurrency"`
	OfficialAmount      any    `json:"officialAmount,omitempty"`
	TTLSeconds          int    `json:"ttlSeconds,omitempty"`
	ReconcileTTLSeconds int    `json:"reconcileTtlSeconds,omitempty"`
}

type HoldRecord struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	AuthorizedUnits string `json:"authorizedUnits"`
	SettledUnits    string `json:"settledUnits"`
	ReleasedUnits   string `json:"releasedUnits"`
}

type HoldResponse struct {
	Hold      HoldRecord `json:"hold"`
	Duplicate bool       `json:"duplicate"`
}

type SettleRequest struct {
	BaseAmount       string `json:"baseAmount"`
	ProviderCurrency string `json:"providerCurrency"`
	OfficialAmount   any    `json:"officialAmount,omitempty"`
	ActualUsage      any    `json:"actualUsage,omitempty"`
	UpstreamRef      string `json:"upstreamRef,omitempty"`
}

type SettleResponse struct {
	Hold                    HoldRecord `json:"hold"`
	Duplicate               bool       `json:"duplicate"`
	PlatformDifferenceUnits string     `json:"platformDifferenceUnits"`
}

type HTTPError struct {
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("unihub HTTP %d: %s", e.Status, string(e.Body))
}

type catalogCacheEntry struct {
	resp      *CatalogResponse
	expiresAt time.Time
}

type Client struct {
	cfg            Config
	httpClient     *http.Client
	upstreamClient *http.Client
	routes         map[string]RouteRateSpec
	mu             sync.RWMutex
	cache          map[string]catalogCacheEntry
	syncCount      int64
}

func NewClient(cfg Config) *Client {
	// Credentials are scoped to the configured origins. Surface redirects as errors.
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		cfg:            cfg,
		httpClient:     &http.Client{Timeout: 20 * time.Second, CheckRedirect: noRedirect},
		upstreamClient: &http.Client{Timeout: 180 * time.Second, CheckRedirect: noRedirect},
		routes:         DefaultRouteSpecs(cfg.Currency),
		cache:          make(map[string]catalogCacheEntry),
	}
}

func (c *Client) ValidServiceToken(provided string) bool {
	if c.cfg.ServiceToken == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(c.cfg.ServiceToken)) == 1
}

func (c *Client) InvalidateCatalogCache() {
	c.mu.Lock()
	c.cache = make(map[string]catalogCacheEntry)
	c.syncCount++
	c.mu.Unlock()
}

func (c *Client) ActiveRouteIDs() []string {
	out := make([]string, 0, len(c.routes))
	for id := range c.routes {
		out = append(out, id)
	}
	return out
}

func ResolveRouteSpec(client *Client, catalog *CatalogResponse, modelID string, route *CatalogRoute) (RouteRateSpec, bool) {
	if spec, ok := client.routes[route.RouteID]; ok {
		return spec, true
	}
	if strings.TrimSpace(route.UnitLedgerUnits) == "" {
		return RouteRateSpec{}, false
	}
	unitsRat, ok := new(big.Rat).SetString(strings.TrimSpace(route.UnitLedgerUnits))
	if !ok || unitsRat.Sign() <= 0 {
		return RouteRateSpec{}, false
	}
	creditsPerCurrency := 200
	if catalog != nil && catalog.CreditsPerCurrency > 0 {
		creditsPerCurrency = catalog.CreditsPerCurrency
	}
	denom := new(big.Rat).SetInt64(int64(creditsPerCurrency) * 1000000)
	rateRat := new(big.Rat).Quo(unitsRat, denom)
	rateStr := strings.TrimRight(strings.TrimRight(rateRat.FloatString(12), "0"), ".")
	if rateStr == "" || rateStr == "0" {
		return RouteRateSpec{}, false
	}
	currency := client.cfg.Currency
	if catalog != nil && strings.TrimSpace(catalog.Currency) != "" {
		currency = strings.TrimSpace(catalog.Currency)
	}
	if currency == "" {
		currency = "CNY"
	}
	isPer1K := strings.Contains(strings.ToLower(route.Unit), "1k")
	return RouteRateSpec{
		RouteID:          route.RouteID,
		ModelID:          modelID,
		Operation:        route.Operation,
		ProviderCurrency: currency,
		RateDecimal:      rateStr,
		IsPer1KTokens:    isPer1K,
	}, true
}

func (c *Client) doInternalJSON(ctx context.Context, method, path string, reqBody any, out any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.InternalURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-UniHub-Service-Token", c.cfg.ServiceToken)
	req.Header.Set("X-Gateway-Version", c.cfg.GatewayVersion)
	req.Header.Set("X-UniHub-Region", c.cfg.Region)
	req.Header.Set("X-UniHub-Currency", c.cfg.Currency)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode, Body: respBytes}
	}
	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Catalog(ctx context.Context, apiKey string, bypassCache bool) (*CatalogResponse, error) {
	if !bypassCache && c.cfg.UpstreamBaseURL == "" && c.cfg.CatalogCacheTTL > 0 {
		c.mu.RLock()
		entry, ok := c.cache[apiKey]
		c.mu.RUnlock()
		if ok && time.Now().Before(entry.expiresAt) {
			return entry.resp, nil
		}
	}
	payload := map[string]any{
		"apiKey": apiKey,
	}
	if c.cfg.UpstreamBaseURL == "" {
		payload["activeRouterRouteIds"] = c.ActiveRouteIDs()
	}
	var res CatalogResponse
	err := c.doInternalJSON(ctx, http.MethodPost, "/internal/billing/v1/catalog", payload, &res)
	if err != nil {
		return nil, err
	}
	if c.cfg.UpstreamBaseURL != "" {
		enabled, err := c.relayModels(ctx)
		if err != nil {
			return nil, err
		}
		visible := make([]CatalogModel, 0, len(res.Models))
		for _, m := range res.Models {
			if !enabled[m.ModelID] {
				continue
			}
			if !c.cfg.UpstreamSimulator {
				if m.ModelID != deepSeekPro || res.Region != "cn" || res.Currency != "CNY" {
					continue
				}
				var supported []CatalogRoute
				for _, route := range m.Routes {
					if route.RouteID == proRoute && route.Operation == "chat" && route.Unit == "1K tokens" &&
						route.MeterVersion == proMeter && route.RouterRateVersion == proRate {
						supported = append(supported, route)
					}
				}
				if len(supported) == 0 {
					continue
				}
				m.Routes = supported
				m.Operations = []string{"chat"}
			}
			visible = append(visible, m)
		}
		res.Models = visible
	} else if !c.cfg.UpstreamSimulator {
		res.Models = []CatalogModel{}
	} else if c.cfg.CatalogCacheTTL > 0 {
		c.mu.Lock()
		c.cache[apiKey] = catalogCacheEntry{resp: &res, expiresAt: time.Now().Add(c.cfg.CatalogCacheTTL)}
		c.mu.Unlock()
	}
	return &res, nil
}

// relayModels reads the same model list the native relay credential can actually call.
func (c *Client) relayModels(ctx context.Context) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.UpstreamBaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.UpstreamAPIKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay catalog unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay catalog HTTP %d", res.StatusCode)
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&listing)
	if err != nil || listing.Data == nil {
		return nil, fmt.Errorf("invalid relay catalog")
	}
	enabled := make(map[string]bool, len(listing.Data))
	for _, model := range listing.Data {
		enabled[model.ID] = true
	}
	return enabled, nil
}

func (c *Client) CreateHold(ctx context.Context, req HoldRequest) (*HoldResponse, error) {
	var res HoldResponse
	if err := c.doInternalJSON(ctx, http.MethodPost, "/internal/billing/v1/holds", req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) CommitHold(ctx context.Context, holdID, upstreamRef, state string) error {
	return c.doInternalJSON(ctx, http.MethodPost, "/internal/billing/v1/holds/"+holdID+"/commit", map[string]any{
		"executorId":  c.cfg.ExecutorID,
		"attempt":     1,
		"upstreamRef": upstreamRef,
		"state":       state,
	}, nil)
}

func (c *Client) SettleHold(ctx context.Context, holdID string, req SettleRequest) (*SettleResponse, error) {
	var res SettleResponse
	if err := c.doInternalJSON(ctx, http.MethodPost, "/internal/billing/v1/holds/"+holdID+"/settle", req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) ReleaseHold(ctx context.Context, holdID string) error {
	return c.doInternalJSON(ctx, http.MethodPost, "/internal/billing/v1/holds/"+holdID+"/release", map[string]any{}, nil)
}

// ComputeBaseAmount calculates exact decimal baseAmount using math/big.Rat so
// e.g. 40 tokens * 0.0005 / 1K tokens produces "0.00002" without floating drift.
func ComputeBaseAmount(spec RouteRateSpec, qty int64) (string, error) {
	if qty <= 0 {
		qty = 1
	}
	rateRat, ok := new(big.Rat).SetString(spec.RateDecimal)
	if !ok {
		return "", fmt.Errorf("invalid rate decimal %q", spec.RateDecimal)
	}
	qtyRat := big.NewRat(qty, 1)
	if spec.IsPer1KTokens {
		qtyRat.Quo(qtyRat, big.NewRat(1000, 1))
	}
	total := new(big.Rat).Mul(rateRat, qtyRat)
	s := total.FloatString(10)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		s = "0"
	}
	return s, nil
}
