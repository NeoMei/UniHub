package unihub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestComputeBaseAmountExact(t *testing.T) {
	spec := DefaultRouteSpecs("CNY")["ali:text-embedding-v4"]
	amt, err := ComputeBaseAmount(spec, 40)
	require.NoError(t, err)
	require.Equal(t, "0.00002", amt)

	specImg := DefaultRouteSpecs("CNY")["volc:seedream-4.0"]
	amtImg, err := ComputeBaseAmount(specImg, 2)
	require.NoError(t, err)
	require.Equal(t, "0.4", amtImg)
}

func TestResolveRouteSpecDynamicFallback(t *testing.T) {
	c := NewClient(Config{Currency: "CNY"})
	cat := &CatalogResponse{Currency: "CNY", CreditsPerCurrency: 200}
	route := &CatalogRoute{
		RouteID:         "custom:model-x",
		Operation:       "chat",
		Unit:            "1K tokens",
		UnitLedgerUnits: "1000000", // 1 credit = 0.005 CNY per 1K tokens
	}
	spec, ok := ResolveRouteSpec(c, cat, "model-x", route)
	require.True(t, ok)
	require.Equal(t, "0.005", spec.RateDecimal)
	require.Equal(t, "CNY", spec.ProviderCurrency)
	require.True(t, spec.IsPer1KTokens)
	amt, err := ComputeBaseAmount(spec, 500)
	require.NoError(t, err)
	require.Equal(t, "0.0025", amt)
}

func setupTestRouter(t *testing.T, handler http.HandlerFunc) (*gin.Engine, *httptest.Server, *Client) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled:           true,
		InternalURL:       srv.URL,
		ServiceToken:      "test-svc-token",
		GatewayVersion:    "1.0",
		Region:            "cn",
		Currency:          "CNY",
		ExecutorID:        "test-gw",
		UpstreamSimulator: true,
	}
	client := NewClient(cfg)
	r := gin.New()
	Register(r, client)
	r.GET("/v1/models", func(c *gin.Context) { c.JSON(200, gin.H{"legacy": true}) })
	r.POST("/v1/embeddings", func(c *gin.Context) { c.JSON(200, gin.H{"legacy": true}) })
	r.POST("/v1/rerank", func(c *gin.Context) { c.JSON(200, gin.H{"legacy": true}) })
	r.POST("/v1/chat/completions", func(c *gin.Context) { c.JSON(200, gin.H{"legacy": true}) })
	return r, srv, client
}

func TestModelsHoldCommitSettleAndErrorPaths(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	holdMode := "ok"
	r, _, _ := setupTestRouter(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "test-svc-token", req.Header.Get("X-UniHub-Service-Token"))
		require.Equal(t, "1.0", req.Header.Get("X-Gateway-Version"))
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/internal/billing/v1/catalog":
			if holdMode == "no_seat" {
				_, _ = w.Write([]byte(`{"hasAppSeat":false,"models":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"app":{"id":"app1","name":"SuperWagie"},
				"accountKind":"personal","region":"cn","currency":"CNY","hasAppSeat":true,
				"pricingVersionId":"pv1","pricingVersionCode":"v1","creditsPerCurrency":200,
				"models":[
					{"modelId":"text-embedding-v4","operations":["embedding"],"pricing":[],"routes":[
						{"routeId":"ali:text-embedding-v4","operation":"embedding","unit":"1K tokens","meterVersion":"m_ali_emb_v4","routerRateVersion":"r_ali_v1","unitLedgerUnits":"100000","minLedgerUnits":"4000"}
					]},
					{"modelId":"gte-rerank-v2","operations":["rerank"],"pricing":[],"routes":[
						{"routeId":"ali:gte-rerank-v2","operation":"rerank","unit":"1K tokens","meterVersion":"m_ali_rerank_v2","routerRateVersion":"r_ali_v1","unitLedgerUnits":"220000","minLedgerUnits":"22000"}
					]}
				]
			}`))
		case "/internal/billing/v1/holds":
			if holdMode == "402" {
				w.WriteHeader(402)
				_, _ = w.Write([]byte(`{"error":"insufficient_credits"}`))
				return
			}
			var body HoldRequest
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, "0.00002", body.BaseAmount)
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"hold":{"id":"11111111-1111-1111-1111-111111111111","status":"reserved","authorizedUnits":"4000"},"duplicate":false}`))
		case "/internal/billing/v1/holds/11111111-1111-1111-1111-111111111111/commit":
			_, _ = w.Write([]byte(`{"hold":{"id":"11111111-1111-1111-1111-111111111111","status":"dispatching"}}`))
		case "/internal/billing/v1/holds/11111111-1111-1111-1111-111111111111/settle":
			_, _ = w.Write([]byte(`{"hold":{"id":"11111111-1111-1111-1111-111111111111","status":"settled","settledUnits":"4000"},"duplicate":false,"platformDifferenceUnits":"0"}`))
		case "/internal/billing/v1/holds/11111111-1111-1111-1111-111111111111/release":
			_, _ = w.Write([]byte(`{"hold":{"id":"11111111-1111-1111-1111-111111111111","status":"released"},"duplicate":false}`))
		default:
			w.WriteHeader(404)
		}
	})

	// 1. Non-uh_live_ request passes through untouched
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-legacy-key")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"legacy":true`)

	// 2. GET /v1/models with uh_live_ returns catalog
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer uh_live_abc")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"text-embedding-v4"`)

	// 3. POST /v1/embeddings performs hold -> commit -> upstream -> settle
	mu.Lock()
	calls = nil
	mu.Unlock()
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-v4","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer uh_live_abc")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "11111111-1111-1111-1111-111111111111", w.Header().Get("X-UniHub-Hold-Id"))
	require.Equal(t, []string{
		"POST /internal/billing/v1/catalog",
		"POST /internal/billing/v1/holds",
		"POST /internal/billing/v1/holds/11111111-1111-1111-1111-111111111111/commit",
		"POST /internal/billing/v1/holds/11111111-1111-1111-1111-111111111111/settle",
	}, calls)

	// 4. Simulated upstream error triggers release hold rollback
	mu.Lock()
	calls = nil
	mu.Unlock()
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-v4","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer uh_live_abc")
	req.Header.Set("X-Simulate-Upstream-Error", "502")
	r.ServeHTTP(w, req)
	require.Equal(t, 502, w.Code)
	require.Equal(t, []string{
		"POST /internal/billing/v1/catalog",
		"POST /internal/billing/v1/holds",
		"POST /internal/billing/v1/holds/11111111-1111-1111-1111-111111111111/commit",
		"POST /internal/billing/v1/holds/11111111-1111-1111-1111-111111111111/release",
	}, calls)

	// 5. 402 insufficient_credits passes through
	holdMode = "402"
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-v4","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer uh_live_abc")
	r.ServeHTTP(w, req)
	require.Equal(t, 402, w.Code)
	require.Contains(t, w.Body.String(), "insufficient_credits")

	// 6. Enterprise revoked seat -> 403 app_seat_required
	holdMode = "no_seat"
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-v4","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer uh_live_abc")
	r.ServeHTTP(w, req)
	require.Equal(t, 403, w.Code)
	require.Contains(t, w.Body.String(), "app_seat_required")

	// 7. Key sync endpoint auth
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/internal/unihub/keys/sync", bytes.NewBufferString(`{"action":"revoke"}`))
	r.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/internal/unihub/keys/sync", bytes.NewBufferString(`{"action":"revoke"}`))
	req.Header.Set("X-UniHub-Service-Token", "test-svc-token")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
}

func TestUpstreamUsageExtractionAndStreamingChat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"text-embedding-v4"},{"id":"deepseek-v4-flash"}]}`))
		case "/v1/embeddings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1]}],"usage":{"total_tokens":120}}`))
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if stream, _ := body["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				_, _ = w.Write([]byte("data: {\"id\":\"chat1\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n"))
				_, _ = w.Write([]byte("data: {\"id\":\"chat1\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat1","choices":[{"message":{"content":"OK"}}],"usage":{"total_tokens":50}}`))
		}
	}))
	defer upstream.Close()

	var settledBase string
	var settledQty any
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/internal/billing/v1/catalog":
			_, _ = w.Write([]byte(`{"currency":"CNY","creditsPerCurrency":200,"hasAppSeat":true,"models":[
				{"modelId":"text-embedding-v4","operations":["embedding"],"routes":[{"routeId":"ali:text-embedding-v4","operation":"embedding","unit":"1K tokens","meterVersion":"m_ali_emb_v4","routerRateVersion":"r_ali_v1"}]},
				{"modelId":"deepseek-v4-flash","operations":["chat"],"routes":[{"routeId":"deepseek:deepseek-v4-flash","operation":"chat","unit":"1K tokens","meterVersion":"m_ds_v1","routerRateVersion":"r_ds_v1","unitLedgerUnits":"400000"}]}
			]}`))
		case "/internal/billing/v1/holds":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"hold":{"id":"22222222-2222-2222-2222-222222222222","status":"reserved","authorizedUnits":"4000"}}`))
		case "/internal/billing/v1/holds/22222222-2222-2222-2222-222222222222/commit":
			_, _ = w.Write([]byte(`{"hold":{"id":"22222222-2222-2222-2222-222222222222","status":"dispatching"}}`))
		case "/internal/billing/v1/holds/22222222-2222-2222-2222-222222222222/settle":
			var s SettleRequest
			require.NoError(t, json.NewDecoder(req.Body).Decode(&s))
			settledBase = s.BaseAmount
			if u, ok := s.ActualUsage.(map[string]any); ok {
				settledQty = u["qty"]
			}
			_, _ = w.Write([]byte(`{"hold":{"id":"22222222-2222-2222-2222-222222222222","status":"settled","settledUnits":"12000"}}`))
		}
	}))
	defer billing.Close()

	client := NewClient(Config{
		Enabled:           true,
		InternalURL:       billing.URL,
		ServiceToken:      "tok",
		GatewayVersion:    "1.0",
		Region:            "cn",
		Currency:          "CNY",
		ExecutorID:        "test",
		UpstreamSimulator: true, // exercises the staging estimators
		UpstreamBaseURL:   upstream.URL,
	})
	r := gin.New()
	Register(r, client)
	r.POST("/v1/embeddings", func(c *gin.Context) { c.JSON(500, gin.H{"err": true}) })
	r.POST("/v1/chat/completions", func(c *gin.Context) { c.JSON(500, gin.H{"err": true}) })

	// Non-streaming embeddings
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-v4","input":"short"}`))
	req.Header.Set("Authorization", "Bearer uh_live_test")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "0.00006", settledBase)
	require.Equal(t, float64(120), settledQty)

	// Non-streaming chat
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer uh_live_test")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, float64(50), settledQty)
	require.Equal(t, "0.0001", settledBase)

	// Streaming SSE chat
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer uh_live_test")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "data: [DONE]")
	require.Equal(t, float64(15), settledQty)
}

func TestProductionCatalogReflectsAvailableRelayModels(t *testing.T) {
	var listed []string
	available := true
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/v1/models", req.URL.Path)
		require.Equal(t, "Bearer relay-test", req.Header.Get("Authorization"))
		if !available {
			w.WriteHeader(503)
			return
		}
		data := []map[string]string{}
		for _, id := range listed {
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer relay.Close()
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct{ APIKey string }
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, "uh_live_fixture", body.APIKey)
		_ = json.NewEncoder(w).Encode(CatalogResponse{Region: "cn", Currency: "CNY", HasAppSeat: true, Models: []CatalogModel{
			{ModelID: deepSeekPro, Routes: []CatalogRoute{
				{RouteID: proRoute, Operation: "chat", Unit: "1K tokens", MeterVersion: proMeter, RouterRateVersion: proRate},
				{RouteID: "unverified", Operation: "chat"},
			}}, {ModelID: "gte-rerank-v2"},
		}})
	}))
	defer billing.Close()
	client := NewClient(Config{InternalURL: billing.URL, UpstreamBaseURL: relay.URL, UpstreamAPIKey: "relay-test"})
	result, err := client.Catalog(t.Context(), "uh_live_fixture", false)
	require.NoError(t, err)
	require.Empty(t, result.Models)
	listed = []string{deepSeekPro, "gte-rerank-v2", "unpublished"}
	result, err = client.Catalog(t.Context(), "uh_live_fixture", false)
	require.NoError(t, err)
	require.Len(t, result.Models, 1)
	require.Equal(t, deepSeekPro, result.Models[0].ModelID)
	require.Len(t, result.Models[0].Routes, 1)
	available = false
	_, err = client.Catalog(t.Context(), "uh_live_fixture", false)
	require.Error(t, err)
}
