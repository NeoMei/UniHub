package unihub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type routeMapping struct {
	DefaultModel string
	Operation    string
}

var pathMappings = map[string]routeMapping{
	"/v1/chat/completions":           {DefaultModel: "deepseek-v4-flash", Operation: "chat"},
	"/v1/completions":                {DefaultModel: "deepseek-v4-flash", Operation: "chat"},
	"/v1/responses":                  {DefaultModel: "deepseek-v4-flash", Operation: "responses"},
	"/v1/embeddings":                 {DefaultModel: "text-embedding-v4", Operation: "embedding"},
	"/compatible-mode/v1/embeddings": {DefaultModel: "text-embedding-v4", Operation: "embedding"},
	"/v1/rerank":                     {DefaultModel: "gte-rerank-v2", Operation: "rerank"},
	"/api/v1/services/rerank/text-rerank/text-rerank":                       {DefaultModel: "gte-rerank-v2", Operation: "rerank"},
	"/v1/images/generations":                                                {DefaultModel: "seedream-4.0", Operation: "image_generation"},
	"/v1/multimodal/embeddings":                                             {DefaultModel: "qwen3-vl-embedding", Operation: "multimodal_embedding"},
	"/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding": {DefaultModel: "qwen3-vl-embedding", Operation: "multimodal_embedding"},
	"/v1/ocr/parse": {DefaultModel: "paddleocr-vl", Operation: "ocr_parse"},
	"/v1/ocr":       {DefaultModel: "paddleocr-vl", Operation: "ocr_parse"},
}

func Register(engine *gin.Engine, client *Client) {
	if client == nil || !client.cfg.Enabled {
		return
	}
	engine.Use(Middleware(client))
	engine.POST("/v1/ocr/parse", func(c *gin.Context) {
		handleUniHubRequest(c, client)
	})
	engine.POST("/api/internal/unihub/keys/sync", HandleKeysSync(client))
}

func HandleKeysSync(client *Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client == nil || !client.cfg.Enabled {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "unihub_billing_disabled"})
			return
		}
		token := strings.TrimSpace(c.GetHeader("X-UniHub-Service-Token"))
		if !client.ValidServiceToken(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_service_token"})
			return
		}
		var payload map[string]any
		_ = c.ShouldBindJSON(&payload)
		client.InvalidateCatalogCache()
		action, _ := payload["action"].(string)
		c.AbortWithStatusJSON(http.StatusOK, gin.H{"ok": true, "action": action})
	}
}

func extractUniHubBearer(c *gin.Context) (string, bool) {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if !strings.HasPrefix(tok, "uh_live_") {
		return "", false
	}
	return tok, true
}

func Middleware(client *Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client == nil || !client.cfg.Enabled {
			c.Next()
			return
		}
		if c.Request.Method == http.MethodPost && c.Request.URL.Path == "/api/internal/unihub/keys/sync" {
			HandleKeysSync(client)(c)
			return
		}
		if _, ok := extractUniHubBearer(c); !ok {
			c.Next()
			return
		}
		handleUniHubRequest(c, client)
	}
}

func handleUniHubRequest(c *gin.Context, client *Client) {
	apiKey, ok := extractUniHubBearer(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_api_key"})
		return
	}
	path := c.Request.URL.Path
	if c.Request.Method == http.MethodGet && path == "/v1/models" {
		handleCatalogModels(c, client, apiKey)
		return
	}
	mapping, supported := pathMappings[path]
	if !supported || c.Request.Method != http.MethodPost {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "unsupported_billable_endpoint"})
		return
	}
	handleBillableModelCall(c, client, apiKey, mapping)
}

func writeUpstreamError(c *gin.Context, err error) {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		contentType := "application/json"
		if len(httpErr.Body) > 0 && json.Valid(httpErr.Body) {
			c.Data(httpErr.Status, contentType, httpErr.Body)
		} else {
			c.JSON(httpErr.Status, gin.H{"error": strings.TrimSpace(string(httpErr.Body))})
		}
		c.Abort()
		return
	}
	c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "unihub_unreachable", "detail": err.Error()})
}

func handleCatalogModels(c *gin.Context, client *Client, apiKey string) {
	catalog, err := client.Catalog(c.Request.Context(), apiKey, true)
	if err != nil {
		writeUpstreamError(c, err)
		return
	}
	if !catalog.HasAppSeat {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "app_seat_required"})
		return
	}
	data := make([]gin.H, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		data = append(data, gin.H{
			"id":         m.ModelID,
			"object":     "model",
			"created":    time.Now().Unix(),
			"owned_by":   "unihub",
			"operations": m.Operations,
			"pricing":    m.Pricing,
			"routes":     m.Routes,
		})
	}
	c.AbortWithStatusJSON(http.StatusOK, gin.H{
		"object":             "list",
		"data":               data,
		"app":                catalog.App,
		"accountKind":        catalog.AccountKind,
		"region":             catalog.Region,
		"currency":           catalog.Currency,
		"pricingVersionId":   catalog.PricingVersionID,
		"pricingVersionCode": catalog.PricingVersionCode,
	})
}

func randomHexID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func countTextRunes(v any) int {
	switch t := v.(type) {
	case string:
		return len([]rune(t))
	case []any:
		total := 0
		for _, item := range t {
			total += countTextRunes(item)
		}
		return total
	case map[string]any:
		total := 0
		for _, k := range []string{"text", "content", "input"} {
			if sub, ok := t[k]; ok {
				total += countTextRunes(sub)
			}
		}
		return total
	default:
		return 0
	}
}

func extractUsageCount(c *gin.Context, client *Client, rawBody []byte, op string) int64 {
	if client.cfg.UpstreamSimulator {
		if h := strings.TrimSpace(c.GetHeader("X-Test-Usage-Tokens")); h != "" {
			if n, err := strconv.ParseInt(h, 10, 64); err == nil && n > 0 {
				return n
			}
		}
		if h := strings.TrimSpace(c.GetHeader("X-Test-Usage-Count")); h != "" {
			if n, err := strconv.ParseInt(h, 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	var body map[string]any
	_ = common.Unmarshal(rawBody, &body)
	switch op {
	case "image_generation":
		if n, ok := asInt64(body["n"]); ok && n > 0 {
			return n
		}
		return 1
	case "ocr_parse":
		if n, ok := asInt64(body["pages"]); ok && n > 0 {
			return n
		}
		return 1
	case "chat", "responses":
		totalChars := 0
		if msgs, ok := body["messages"].([]any); ok {
			for _, m := range msgs {
				totalChars += countTextRunes(m)
			}
		}
		if inp, ok := body["input"]; ok {
			totalChars += countTextRunes(inp)
		}
		if prm, ok := body["prompt"]; ok {
			totalChars += countTextRunes(prm)
		}
		est := int64((totalChars+2)/3) + 64
		if maxTok, ok := asInt64(body["max_tokens"]); ok && maxTok > 0 {
			est += maxTok
		} else if maxOut, ok := asInt64(body["max_output_tokens"]); ok && maxOut > 0 {
			est += maxOut
		}
		if est < 64 {
			return 64
		}
		return est
	case "rerank":
		totalChars := 0
		if q, ok := body["query"].(string); ok {
			totalChars += len([]rune(q))
		}
		if docs, ok := body["documents"].([]any); ok {
			for _, d := range docs {
				if s, ok := d.(string); ok {
					totalChars += len([]rune(s))
				}
			}
		}
		if inp, ok := body["input"].(map[string]any); ok {
			if q, ok := inp["query"].(string); ok {
				totalChars += len([]rune(q))
			}
			if docs, ok := inp["documents"].([]any); ok {
				for _, d := range docs {
					if s, ok := d.(string); ok {
						totalChars += len([]rune(s))
					}
				}
			}
		}
		if totalChars <= 0 {
			return 100
		}
		est := int64((totalChars + 2) / 3)
		if est < 100 {
			return 100
		}
		return est
	default:
		// embedding / multimodal_embedding
		totalChars := 0
		switch inp := body["input"].(type) {
		case string:
			totalChars += len([]rune(inp))
		case []any:
			for _, item := range inp {
				if s, ok := item.(string); ok {
					totalChars += len([]rune(s))
				}
			}
		case map[string]any:
			if contents, ok := inp["contents"].([]any); ok {
				for _, item := range contents {
					if m, ok := item.(map[string]any); ok {
						if t, ok := m["text"].(string); ok {
							totalChars += len([]rune(t))
						} else {
							totalChars += 120
						}
					}
				}
			}
		}
		if totalChars <= 0 {
			return 40
		}
		est := int64((totalChars + 2) / 3)
		if est < 40 {
			return 40
		}
		return est
	}
}

func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

func resolveModelRoute(catalog *CatalogResponse, requestedModel string, mapping routeMapping) (*CatalogModel, *CatalogRoute) {
	targetModel := strings.TrimSpace(requestedModel)
	if targetModel == "" {
		targetModel = mapping.DefaultModel
	}
	for i := range catalog.Models {
		m := &catalog.Models[i]
		if m.ModelID != targetModel || !slices.Contains(m.Operations, mapping.Operation) {
			continue
		}
		for j := range m.Routes {
			r := &m.Routes[j]
			if r.Operation == mapping.Operation {
				return m, r
			}
		}
	}
	return nil, nil
}

func handleBillableModelCall(c *gin.Context, client *Client, apiKey string, mapping routeMapping) {
	rawBody, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
		return
	}
	var bodyMap map[string]any
	if len(rawBody) > 0 {
		if err := common.Unmarshal(rawBody, &bodyMap); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
	}
	requestedModel, _ := bodyMap["model"].(string)
	if !client.cfg.UpstreamSimulator && (c.Request.URL.Path != "/v1/chat/completions" || requestedModel != deepSeekPro) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "unsupported_billable_route"})
		return
	}
	bypass := client.cfg.CatalogCacheTTL <= 0
	catalog, err := client.Catalog(c.Request.Context(), apiKey, bypass)
	if err != nil {
		writeUpstreamError(c, err)
		return
	}
	if !catalog.HasAppSeat {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "app_seat_required"})
		return
	}
	modelEntry, route := resolveModelRoute(catalog, requestedModel, mapping)
	if modelEntry == nil || route == nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "model_not_allowed", "model": requestedModel})
		return
	}
	if !client.cfg.UpstreamSimulator {
		handleDeepSeekChat(c, client, apiKey, catalog, route, rawBody)
		return
	}
	spec, ok := ResolveRouteSpec(client, catalog, modelEntry.ModelID, route)
	if !ok {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "router_rate_missing", "routeId": route.RouteID})
		return
	}
	usageCount := extractUsageCount(c, client, rawBody, mapping.Operation)
	estBaseAmount, err := ComputeBaseAmount(spec, usageCount)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "rate_compute_failed"})
		return
	}
	requestID := strings.TrimSpace(c.GetHeader("X-Request-Id"))
	if requestID == "" {
		requestID = randomHexID("req")
	}
	holdResp, err := client.CreateHold(c.Request.Context(), HoldRequest{
		APIKey:            apiKey,
		RequestID:         requestID,
		RouteID:           route.RouteID,
		MeterVersion:      route.MeterVersion,
		RouterRateVersion: route.RouterRateVersion,
		BaseAmount:        estBaseAmount,
		ProviderCurrency:  spec.ProviderCurrency,
		OfficialAmount: map[string]any{
			"estimatedQty": usageCount,
			"unit":         route.Unit,
			"rateDecimal":  spec.RateDecimal,
			"currency":     spec.ProviderCurrency,
		},
		TTLSeconds:          300,
		ReconcileTTLSeconds: 3600,
	})
	if err != nil {
		writeUpstreamError(c, err)
		return
	}
	if !newReservation(c, holdResp) {
		return
	}
	holdID := holdResp.Hold.ID
	upstreamRef := randomHexID("up")
	if err := client.CommitHold(c.Request.Context(), holdID, upstreamRef, "dispatching"); err != nil {
		pendingMeter(c, holdID, upstreamRef, "commit_unconfirmed")
		return
	}
	upStatus, upBody, actualUsageQty, streamed, upErr := dispatchUpstream(c, client, rawBody, modelEntry.ModelID, mapping.Operation, usageCount, upstreamRef, holdID, route.RouteID)
	if upErr != nil || upStatus < 200 || upStatus >= 300 {
		_ = client.ReleaseHold(context.Background(), holdID)
		if streamed {
			c.Abort()
			return
		}
		if upStatus < 400 {
			upStatus = http.StatusBadGateway
		}
		if len(upBody) > 0 && json.Valid(upBody) {
			c.Data(upStatus, "application/json", upBody)
		} else {
			msg := "upstream_failed"
			if upErr != nil {
				msg = upErr.Error()
			}
			c.JSON(upStatus, gin.H{"error": msg, "holdId": holdID})
		}
		c.Abort()
		return
	}
	actualBaseAmount, err := ComputeBaseAmount(spec, actualUsageQty)
	if err != nil {
		actualBaseAmount = estBaseAmount
	}
	settleResp, err := client.SettleHold(context.Background(), holdID, SettleRequest{
		BaseAmount:       actualBaseAmount,
		ProviderCurrency: spec.ProviderCurrency,
		OfficialAmount: map[string]any{
			"actualQty":   actualUsageQty,
			"unit":        route.Unit,
			"rateDecimal": spec.RateDecimal,
			"currency":    spec.ProviderCurrency,
		},
		ActualUsage: map[string]any{
			"qty":     actualUsageQty,
			"unit":    route.Unit,
			"modelId": modelEntry.ModelID,
		},
		UpstreamRef: upstreamRef,
	})
	if err != nil {
		if streamed {
			c.Abort()
			return
		}
		writeUpstreamError(c, err)
		return
	}
	if streamed {
		c.Abort()
		return
	}
	c.Header("X-UniHub-Hold-Id", holdID)
	c.Header("X-UniHub-Settled-Units", settleResp.Hold.SettledUnits)
	c.Header("X-UniHub-Route-Id", route.RouteID)
	c.Data(upStatus, "application/json", upBody)
	c.Abort()
}

func extractUsageFromParsedMap(parsed map[string]any) (int64, bool) {
	if usage, ok := parsed["usage"].(map[string]any); ok {
		if n, ok := asInt64(usage["total_tokens"]); ok && n > 0 {
			return n, true
		}
		inTok, hasIn := asInt64(usage["prompt_tokens"])
		if !hasIn {
			inTok, hasIn = asInt64(usage["input_tokens"])
		}
		outTok, hasOut := asInt64(usage["completion_tokens"])
		if !hasOut {
			outTok, hasOut = asInt64(usage["output_tokens"])
		}
		if (hasIn || hasOut) && inTok+outTok > 0 {
			return inTok + outTok, true
		}
	}
	return 0, false
}

func extractActualUsageFromResponse(op string, respBody []byte, fallback int64) int64 {
	var parsed map[string]any
	if len(respBody) == 0 || common.Unmarshal(respBody, &parsed) != nil {
		return fallback
	}
	switch op {
	case "image_generation":
		if data, ok := parsed["data"].([]any); ok && len(data) > 0 {
			return int64(len(data))
		}
	case "ocr_parse":
		if usage, ok := parsed["usage"].(map[string]any); ok {
			if pages, ok := asInt64(usage["pages"]); ok && pages > 0 {
				return pages
			}
		}
		if pages, ok := asInt64(parsed["pages"]); ok && pages > 0 {
			return pages
		}
	default:
		if n, ok := extractUsageFromParsedMap(parsed); ok {
			return n
		}
		if respObj, ok := parsed["response"].(map[string]any); ok {
			if n, ok := extractUsageFromParsedMap(respObj); ok {
				return n
			}
		}
	}
	return fallback
}

func dispatchUpstream(c *gin.Context, client *Client, rawBody []byte, modelID, op string, estQty int64, upstreamRef, holdID, routeID string) (int, []byte, int64, bool, error) {
	if simErr := strings.TrimSpace(c.GetHeader("X-Simulate-Upstream-Error")); simErr != "" && client.cfg.UpstreamSimulator {
		code := http.StatusBadGateway
		if n, err := strconv.Atoi(simErr); err == nil && n >= 400 && n < 600 {
			code = n
		}
		return code, []byte(fmt.Sprintf(`{"error":"simulated_upstream_error","status":%d}`, code)), 0, false, fmt.Errorf("simulated upstream status %d", code)
	}
	if client.cfg.UpstreamBaseURL != "" {
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, client.cfg.UpstreamBaseURL+c.Request.URL.Path, bytes.NewReader(rawBody))
		if err != nil {
			return http.StatusBadGateway, nil, 0, false, err
		}
		req.Header.Set("Content-Type", "application/json")
		if client.cfg.UpstreamAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+client.cfg.UpstreamAPIKey)
		}
		httpCli := client.upstreamClient
		if httpCli == nil {
			httpCli = client.httpClient
		}
		resp, err := httpCli.Do(req)
		if err != nil {
			return http.StatusBadGateway, nil, 0, false, err
		}
		defer resp.Body.Close()

		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.Contains(contentType, "text/event-stream") {
			c.Header("Content-Type", resp.Header.Get("Content-Type"))
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-UniHub-Hold-Id", holdID)
			c.Header("X-UniHub-Route-Id", routeID)
			c.Status(resp.StatusCode)

			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 64*1024), 4<<20)
			var streamUsage int64
			streamDeltaChars := 0
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data:") {
					payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					if payload != "" && payload != "[DONE]" {
						var parsed map[string]any
						if common.Unmarshal([]byte(payload), &parsed) == nil {
							if u, ok := extractUsageFromParsedMap(parsed); ok {
								streamUsage = u
							} else if respObj, ok := parsed["response"].(map[string]any); ok {
								if u, ok := extractUsageFromParsedMap(respObj); ok {
									streamUsage = u
								}
							}
							if choices, ok := parsed["choices"].([]any); ok {
								for _, ch := range choices {
									if chMap, ok := ch.(map[string]any); ok {
										streamDeltaChars += countTextRunes(chMap["delta"])
									}
								}
							}
							if d, ok := parsed["delta"]; ok {
								streamDeltaChars += countTextRunes(d)
							}
						}
					}
				}
				if _, writeErr := c.Writer.WriteString(line + "\n"); writeErr != nil {
					break
				}
				c.Writer.Flush()
			}
			if scanErr := scanner.Err(); scanErr != nil && !errors.Is(scanErr, context.Canceled) {
				if streamUsage > 0 {
					return resp.StatusCode, nil, streamUsage, true, nil
				}
				return resp.StatusCode, nil, 0, true, scanErr
			}
			actual := streamUsage
			if actual <= 0 {
				actual = int64((streamDeltaChars+2)/3) + 64
				if estQty > 0 && estQty < actual {
					// keep estimated delta-based tokens
				} else if estQty > 0 && streamDeltaChars == 0 {
					actual = estQty
				}
			}
			return resp.StatusCode, nil, actual, true, nil
		}

		out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return http.StatusBadGateway, nil, 0, false, err
		}
		actualQty := extractActualUsageFromResponse(op, out, estQty)
		return resp.StatusCode, out, actualQty, false, nil
	}
	if !client.cfg.UpstreamSimulator {
		return http.StatusServiceUnavailable, []byte(`{"error":"upstream_not_configured"}`), 0, false, errors.New("upstream not configured")
	}
	var payload any
	switch op {
	case "chat", "responses":
		payload = gin.H{
			"id":      upstreamRef,
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   modelID,
			"choices": []gin.H{
				{"index": 0, "message": gin.H{"role": "assistant", "content": "OK"}, "finish_reason": "stop"},
			},
			"usage": gin.H{"prompt_tokens": estQty, "completion_tokens": 2, "total_tokens": estQty + 2},
		}
		estQty += 2
	case "embedding":
		payload = gin.H{
			"object": "list",
			"model":  modelID,
			"data": []gin.H{
				{"object": "embedding", "index": 0, "embedding": []float64{0.012, -0.034, 0.056, 0.078}},
			},
			"usage": gin.H{"prompt_tokens": estQty, "total_tokens": estQty},
		}
	case "rerank":
		payload = gin.H{
			"id":      upstreamRef,
			"model":   modelID,
			"results": []gin.H{{"index": 0, "relevance_score": 0.96}},
			"usage":   gin.H{"total_tokens": estQty},
		}
	case "image_generation":
		payload = gin.H{
			"created": time.Now().Unix(),
			"model":   modelID,
			"data": []gin.H{
				{"url": "https://cdn.example.invalid/seedream/" + upstreamRef + ".png"},
			},
		}
	case "multimodal_embedding":
		payload = gin.H{
			"object": "list",
			"model":  modelID,
			"data": []gin.H{
				{"object": "embedding", "index": 0, "embedding": []float64{0.021, 0.043, -0.015, 0.089}},
			},
			"usage": gin.H{"input_tokens": estQty, "total_tokens": estQty},
		}
	case "ocr_parse":
		payload = gin.H{
			"id":       upstreamRef,
			"model":    modelID,
			"pages":    estQty,
			"markdown": "# OCR Document Output\n\nParsed via " + modelID + ".",
			"usage":    gin.H{"pages": estQty},
		}
	default:
		return http.StatusBadRequest, []byte(`{"error":"unsupported_operation"}`), 0, false, errors.New("unsupported operation")
	}
	out, err := common.Marshal(payload)
	if err != nil {
		return http.StatusInternalServerError, nil, 0, false, err
	}
	return http.StatusOK, out, estQty, false, nil
}
