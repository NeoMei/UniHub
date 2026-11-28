package unihub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	deepSeekPro = "deepseek-v4-pro"
	proRoute    = "deepseek:deepseek-v4-pro"
	proMeter    = "deepseek-v4-pro-chat-v1"
	proRate     = "deepseek-v4-pro-cn-20261017-v1"
	// Bound the accepted plain-text chat envelope before reserving it.
	proInputLimit    = 1 << 20
	proOutputLimit   = 8192
	proOutputDefault = 256
)

// Chinese official prices, in fen per million tokens. Native rc.51 pricing
// is independently denominated in USD. Neither amount is an FX conversion.
// https://api-docs.deepseek.com/zh-cn/quick_start/pricing
func proCNY(hit, miss, output int, tier string) string {
	fen := int64(hit)*15 + int64(miss)*450 + int64(output)*1350
	if tier == "peak" {
		fen *= 2
	}
	return decimal.New(fen, -8).String()
}

type proCalendar struct {
	Version string            `json:"version"`
	Days    map[string]string `json:"days"`
}

func readProCalendar() (cal proCalendar, err error) {
	raw := os.Getenv("UNIHUB_DEEPSEEK_CN_CALENDAR_JSON")
	if strings.TrimSpace(raw) == "" {
		return defaultProCalendar, nil
	}
	if common.UnmarshalJsonStr(raw, &cal) != nil || strings.TrimSpace(cal.Version) == "" || len(cal.Days) == 0 {
		return cal, errors.New("invalid_deepseek_calendar")
	}
	for day, kind := range cal.Days {
		if _, err := time.Parse(time.DateOnly, day); err != nil || (kind != "workday" && kind != "holiday") {
			return cal, errors.New("invalid_deepseek_calendar")
		}
	}
	return cal, nil
}

var beijing = time.FixedZone("UTC+8", 8*3600)

func (c proCalendar) at(t time.Time) string {
	t = t.In(beijing)
	h := t.Hour()
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday || h < 9 || (h >= 12 && h < 14) || h >= 18 {
		return "off_peak"
	}
	switch c.Days[t.Format(time.DateOnly)] {
	case "workday":
		return "peak"
	case "holiday":
		return "off_peak"
	default:
		return ""
	}
}

func (c proCalendar) interval(start, end time.Time) string {
	tier := c.at(start)
	if tier == "" || end.Before(start) || end.Sub(start) > 24*time.Hour {
		return ""
	}
	for t := start.Truncate(time.Hour).Add(time.Hour); !t.After(end); t = t.Add(time.Hour) {
		if c.at(t) != tier {
			return ""
		}
	}
	return tier
}

type proChat struct {
	body       []byte
	inputBound int
	maxTokens  int
	stream     bool
}

func parseProChat(raw []byte) (chat proChat, err error) {
	var fields map[string]json.RawMessage
	invalid := errors.New("unsupported_chat_parameters")
	if common.Unmarshal(raw, &fields) != nil {
		return chat, invalid
	}
	for k := range fields {
		switch k {
		case "model", "messages", "max_tokens", "max_completion_tokens", "stream", "stream_options", "n", "temperature", "top_p", "stop", "thinking", "reasoning_effort", "presence_penalty", "frequency_penalty", "logprobs", "top_logprobs", "seed", "user", "response_format":
		default:
			return chat, invalid
		}
	}
	if b, ok := fields["response_format"]; ok {
		var format map[string]string
		if common.Unmarshal(b, &format) != nil || len(format) != 1 || (format["type"] != "text" && format["type"] != "json_object") {
			return chat, invalid
		}
	}
	var messages []map[string]json.RawMessage
	if common.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 {
		return chat, invalid
	}
	// Byte-level tokenization uses at most one token per UTF-8 byte. Reserve
	// 32 special/template tokens per message and 64 framing tokens per chat.
	// Any actual usage outside this envelope requires reconciliation.
	chat.inputBound = 64
	for _, msg := range messages {
		if len(msg) < 2 || len(msg) > 3 {
			return chat, invalid
		}
		var role string
		if common.Unmarshal(msg["role"], &role) != nil {
			return chat, invalid
		}
		switch role {
		case "system", "developer", "user", "assistant":
		default:
			return chat, invalid
		}
		chat.inputBound += 32 + len(role)
		if len(msg) == 3 {
			var name string
			if common.Unmarshal(msg["name"], &name) != nil || name == "" {
				return chat, invalid
			}
			chat.inputBound += len(name) + 8
		}
		content := msg["content"]
		switch common.GetJsonType(content) {
		case "string":
			var value string
			if common.Unmarshal(content, &value) != nil || value == "" {
				return chat, invalid
			}
			chat.inputBound += len(value)
		case "array":
			var parts []map[string]string
			if common.Unmarshal(content, &parts) != nil || len(parts) == 0 {
				return chat, invalid
			}
			for _, part := range parts {
				if len(part) != 2 || part["type"] != "text" || part["text"] == "" {
					return chat, invalid
				}
				chat.inputBound += 8 + len(part["text"])
			}
			msg["content"], err = common.Marshal(parts)
			if err != nil {
				return chat, err
			}
		default:
			return chat, invalid
		}
	}
	fields["messages"], err = common.Marshal(messages)
	if err != nil {
		return chat, err
	}
	if chat.inputBound > proInputLimit {
		return chat, errors.New("input_too_large")
	}
	if n, ok := fields["n"]; ok && string(n) != "1" {
		return chat, invalid
	}
	chat.maxTokens = proOutputDefault
	capField, ok := fields["max_tokens"]
	if alt, hasAlt := fields["max_completion_tokens"]; hasAlt {
		if ok {
			return chat, invalid
		}
		capField, ok = alt, true
		delete(fields, "max_completion_tokens")
	}
	if ok {
		n, err := strconv.Atoi(string(capField))
		if err != nil || n < 1 || n > proOutputLimit {
			return chat, errors.New("invalid_max_tokens")
		}
		chat.maxTokens = n
	}
	fields["max_tokens"] = json.RawMessage(strconv.Itoa(chat.maxTokens))
	if value, ok := fields["stream"]; ok {
		switch string(value) {
		case "true":
			chat.stream = true
		case "false":
		default:
			return chat, invalid
		}
	}
	if chat.stream {
		opts := map[string]json.RawMessage{}
		if b, ok := fields["stream_options"]; ok && (common.Unmarshal(b, &opts) != nil || opts == nil) {
			return chat, invalid
		}
		opts["include_usage"] = json.RawMessage("true")
		fields["stream_options"], err = common.Marshal(opts)
		if err != nil {
			return chat, err
		}
	} else {
		delete(fields, "stream_options")
	}
	chat.body, err = common.Marshal(fields)
	return chat, err
}

func newReservation(c *gin.Context, resp *HoldResponse) bool {
	if resp.Duplicate {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "duplicate_request", "holdId": resp.Hold.ID, "status": resp.Hold.Status})
		return false
	}
	if _, err := uuid.Parse(resp.Hold.ID); err != nil || resp.Hold.Status != "reserved" {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "unconfirmed_reservation"})
		return false
	}
	return true
}

// A client may already have received SSE data. Terminate with an error event;
// the native [DONE] event is held back until UniHub acknowledges settlement.
func pendingMeter(c *gin.Context, hold, ref, reason string) {
	log.Printf("UniHub reconciliation required hold=%s upstreamRef=%s reason=%s", hold, ref, reason)
	data := gin.H{"error": "billing_reconciliation_required", "reason": reason, "holdId": hold, "upstreamRef": ref}
	if c.Writer.Written() {
		raw, _ := common.Marshal(data)
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", raw)
		c.Writer.Flush()
		c.Abort()
		return
	}
	c.Writer.Header().Del("Trailer")
	c.Header("Content-Type", "application/json")
	c.Header("X-UniHub-Billing-State", "reconciliation_required")
	c.AbortWithStatusJSON(http.StatusBadGateway, data)
}

func handleDeepSeekChat(c *gin.Context, client *Client, apiKey string, catalog *CatalogResponse, route *CatalogRoute, raw []byte) {
	if route.RouteID != proRoute || route.Operation != "chat" || route.Unit != "1K tokens" ||
		route.MeterVersion != proMeter || route.RouterRateVersion != proRate || catalog.Region != "cn" || catalog.Currency != "CNY" ||
		client.cfg.Region != "cn" || client.cfg.Currency != "CNY" {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "unsupported_deepseek_pricing_version"})
		return
	}
	if client.cfg.UpstreamBaseURL == "" || client.cfg.UpstreamAPIKey == "" || strings.HasPrefix(client.cfg.UpstreamAPIKey, "uh_live_") {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "native_endpoint_not_configured"})
		return
	}
	cal, err := readProCalendar()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	chat, err := parseProChat(raw)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	requestID := strings.TrimSpace(c.GetHeader("X-Request-Id"))
	if requestID == "" {
		requestID = randomHexID("req")
	}
	c.Header("X-Request-Id", requestID)
	ref := randomHexID("up")
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, client.cfg.UpstreamBaseURL+"/v1/chat/completions", bytes.NewReader(chat.body))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "invalid_native_endpoint"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+client.cfg.UpstreamAPIKey)
	req.Header.Set("X-Request-Id", ref)
	hold, err := client.CreateHold(c.Request.Context(), HoldRequest{
		APIKey: apiKey, RequestID: requestID, RouteID: proRoute, MeterVersion: proMeter, RouterRateVersion: proRate,
		ProviderCurrency: "CNY", BaseAmount: proCNY(0, chat.inputBound, chat.maxTokens, "peak"),
		OfficialAmount: gin.H{"currency": "CNY", "tier": "peak", "maxCacheMissTokens": chat.inputBound, "maxOutputTokens": chat.maxTokens, "calendarVersion": cal.Version},
		TTLSeconds:     300, ReconcileTTLSeconds: 3600,
	})
	if err != nil {
		writeUpstreamError(c, err)
		return
	}
	if !newReservation(c, hold) {
		return
	}
	id := hold.Hold.ID
	c.Header("X-UniHub-Hold-Id", id)
	c.Header("X-UniHub-Route-Id", proRoute)
	if !positiveUnits.MatchString(hold.Hold.AuthorizedUnits) {
		pendingMeter(c, id, ref, "reservation_unconfirmed")
		return
	}
	if !confirmProDispatch(c.Request.Context(), client, id, ref) {
		pendingMeter(c, id, ref, "commit_unconfirmed")
		return
	}

	start := time.Now()
	resp, err := client.upstreamClient.Do(req)
	if err != nil {
		pendingMeter(c, id, ref, "native_transport_unconfirmed")
		return
	}
	defer resp.Body.Close()
	nativeID := resp.Header.Get(common.RequestIdKey)
	if len(resp.Header.Values(common.RequestIdKey)) != 1 || !nativeIDPattern.MatchString(nativeID) {
		pendingMeter(c, id, ref, "native_request_id_missing")
		return
	}
	c.Header(common.RequestIdKey, nativeID)
	// Log the join before reading a possibly long response. The committed
	// dispatch reference remains available if settlement never reaches UniHub.
	log.Printf("UniHub native dispatch hold=%s upstreamRef=%s nativeRequestId=%s", id, ref, nativeID)
	if resp.StatusCode != http.StatusOK {
		log.Printf("UniHub reconciliation required hold=%s nativeRequestId=%s httpStatus=%d", id, nativeID, resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode <= 599 {
			body, err := boundedNativeBody(resp.Body)
			if err == nil && json.Valid(body) {
				c.Header("X-UniHub-Billing-State", "reconciliation_required")
				c.Data(resp.StatusCode, "application/json", body)
				c.Abort()
				return
			}
		}
		pendingMeter(c, id, nativeID, "native_http_failure")
		return
	}
	var usage proCounts
	var body []byte
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch {
	case chat.stream && mediaType == "text/event-stream":
		usage, err = readProStream(c, resp.Body, chat)
	case !chat.stream && mediaType == "application/json":
		body, err = boundedNativeBody(resp.Body)
		if err == nil {
			usage, err = readProFrame(body, chat)
		}
	default:
		err = errProEvidence
	}
	if err != nil || usage.input == 0 {
		pendingMeter(c, id, nativeID, "native_usage_unconfirmed")
		return
	}
	end := time.Now()
	tier := cal.interval(start, end)
	if tier == "" {
		log.Printf("UniHub tariff unresolved hold=%s nativeRequestId=%s start=%s end=%s calendar=%q hit=%d miss=%d output=%d", id, nativeID, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano), cal.Version, usage.hit, usage.miss, usage.output)
		pendingMeter(c, id, nativeID, "tariff_period_unconfirmed")
		return
	}
	amount := proCNY(usage.hit, usage.miss, usage.output, tier)
	s, err := client.SettleHold(context.WithoutCancel(c.Request.Context()), id, SettleRequest{
		BaseAmount: amount, ProviderCurrency: "CNY", UpstreamRef: nativeID,
		OfficialAmount: gin.H{"currency": "CNY", "amount": amount, "tier": tier, "calendarVersion": cal.Version, "routerRateVersion": proRate},
		ActualUsage: gin.H{"modelId": deepSeekPro, "qty": usage.input + usage.output, "unit": "tokens", "promptTokens": usage.input,
			"cacheHitTokens": usage.hit, "cacheMissTokens": usage.miss, "completionTokens": usage.output, "stream": chat.stream,
			"nativeRequestId": nativeID, "dispatchRef": ref, "dispatchAt": start.UTC(), "completedAt": end.UTC()},
	})
	if err != nil || s == nil || s.Duplicate || s.Hold.ID != id || s.Hold.Status != "settled" || !positiveUnits.MatchString(s.Hold.SettledUnits) || (s.PlatformDifferenceUnits != "" && s.PlatformDifferenceUnits != "0") {
		pendingMeter(c, id, nativeID, "settlement_unconfirmed")
		return
	}
	c.Header("X-UniHub-Settled-Units", s.Hold.SettledUnits)
	if chat.stream {
		_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
		c.Writer.Flush()
	} else {
		c.Data(http.StatusOK, "application/json", body)
	}
	c.Abort()
}

// The production commit endpoint returns the updated row. Confirm that this
// executor owns the in-flight attempt before making the one native POST.
func confirmProDispatch(ctx context.Context, client *Client, id, ref string) bool {
	var reply struct {
		Hold struct {
			ID, Status, CommittedBy, UpstreamRef string
			CommitAttempt                        int
		}
	}
	err := client.doInternalJSON(ctx, http.MethodPost, "/internal/billing/v1/holds/"+id+"/commit",
		gin.H{"executorId": client.cfg.ExecutorID, "attempt": 1, "upstreamRef": ref, "state": "in_flight"}, &reply)
	h := reply.Hold
	return err == nil && h.ID == id && h.Status == "in_flight" && h.CommitAttempt == 1 && h.CommittedBy == client.cfg.ExecutorID && h.UpstreamRef == ref
}

var positiveUnits = regexp.MustCompile(`^[1-9][0-9]*$`)
var nativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
