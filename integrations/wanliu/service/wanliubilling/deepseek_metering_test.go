package unihub

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProCalendarAndPrices(t *testing.T) {
	t.Setenv("UNIHUB_DEEPSEEK_CN_CALENDAR_JSON", "")
	cal, err := readProCalendar()
	require.NoError(t, err)
	require.Equal(t, "cn-2026-2027", cal.Version)
	for _, tc := range []struct{ start, end, tier string }{
		{"2026-12-01 08:59", "2026-12-01 09:00", ""},
		{"2026-12-01 09:00", "2026-12-01 11:59", "peak"},
		{"2026-12-01 12:00", "2026-12-01 13:59", "off_peak"},
		{"2026-12-01 14:00", "2026-12-01 17:59", "peak"},
		{"2026-12-01 18:00", "2026-12-02 08:59", "off_peak"},
		{"2026-10-01 10:00", "2026-10-01 10:01", "off_peak"},
		{"2027-10-04 10:00", "2027-10-04 10:01", "off_peak"},
		{"2026-10-10 10:00", "2026-10-10 10:01", "off_peak"}, // Saturday, including make-up workdays
		{"2028-01-04 10:00", "2028-01-04 10:01", ""},
	} {
		start, err := time.ParseInLocation("2006-01-02 15:04", tc.start, beijing)
		require.NoError(t, err)
		end, err := time.ParseInLocation("2006-01-02 15:04", tc.end, beijing)
		require.NoError(t, err)
		require.Equal(t, tc.tier, cal.interval(start.UTC(), end.UTC()), tc.start)
	}
	require.Equal(t, "0.01155", proCNY(2000, 1000, 500, "off_peak"))
	require.Equal(t, "0.0231", proCNY(2000, 1000, 500, "peak"))
	t.Setenv("UNIHUB_DEEPSEEK_CN_CALENDAR_JSON", `{"days":{"2028-01-04":"workday"}}`)
	_, err = readProCalendar()
	require.Error(t, err)
}

func TestProRejectsUnboundedAndConflictingUsage(t *testing.T) {
	for _, param := range []string{`"max_tokens":0`, `"max_tokens":-1`, `"max_tokens":1.1`, `"max_tokens":8193`, `"max_tokens":18446744073709551615`,
		`"max_tokens":4,"max_completion_tokens":5`, `"n":2`, `"tools":[{}]`, `"max_output_tokens":900000`} {
		_, err := parseProChat([]byte(`{"messages":[{"role":"user","content":"你好"}],` + param + `}`))
		require.Error(t, err, param)
	}
	chat, err := parseProChat([]byte(`{"messages":[{"role":"user","content":"你好"}],"stream":true,"stream_options":{"include_usage":false},"max_completion_tokens":8}`))
	require.NoError(t, err)
	require.Equal(t, 106, chat.inputBound)
	require.Equal(t, 8, chat.maxTokens)
	require.Contains(t, string(chat.body), `"include_usage":true`)
	require.NotContains(t, string(chat.body), "max_completion_tokens")
	for _, usage := range []string{
		`{"prompt_tokens":10,"completion_tokens":2}`,
		`{"prompt_tokens":107,"completion_tokens":1,"prompt_cache_hit_tokens":0}`,
		`{"prompt_tokens":10,"completion_tokens":9,"prompt_cache_hit_tokens":0}`,
		`{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":11}`,
		`{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_miss_tokens":-1}`,
		`{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":3,"prompt_cache_miss_tokens":9}`,
		`{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":3,"prompt_tokens_details":{"cached_tokens":0}}`,
		`{"prompt_tokens":10,"completion_tokens":2,"total_tokens":11,"prompt_cache_hit_tokens":0}`,
		`{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"input_tokens":0,"output_tokens":0,"input_tokens_details":null}`,
	} {
		_, err := readProFrame([]byte(`{"model":"deepseek-v4-pro","usage":`+usage+`}`), chat)
		require.Error(t, err, usage)
	}
}

func TestProHTTPMetering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const holdID = "33333333-3333-3333-3333-333333333333"
	const nativeID = "native-test-id"
	const official = `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6}`
	const normalized = `{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}`
	// Freeze the applicable tariff without depending on today's weekday or time.
	now := time.Now().In(beijing)
	t.Setenv("UNIHUB_DEEPSEEK_CN_CALENDAR_JSON", fmt.Sprintf(`{"version":"test-v1","days":{"%s":"holiday","%s":"holiday"}}`, now.Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly)))
	for _, mode := range []string{"json", "sse", "duplicate", "invalid_commit", "no_id", "unknown_usage", "truncated_sse", "settle_failure", "late_released", "redirect", "native_429", "wrong_version", "legacy_completions"} {
		t.Run(mode, func(t *testing.T) {
			stream := mode == "sse" || mode == "truncated_sse" || mode == "settle_failure"
			var hold HoldRequest
			var settled SettleRequest
			var calls []string
			var forwarded []byte
			var nativeCalls, reservations int
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				require.Equal(t, "Bearer native-test-token", r.Header.Get("Authorization"))
				if r.URL.Path == "/v1/models" {
					fmt.Fprint(w, `{"data":[{"id":"deepseek-v4-pro"}]}`)
					return
				}
				require.Equal(t, "/v1/chat/completions", r.URL.Path)
				require.Empty(t, r.URL.RawQuery)
				require.Empty(t, r.Header.Get("X-Test-Usage-Tokens"))
				nativeCalls++
				forwarded, _ = io.ReadAll(r.Body)
				if mode != "no_id" {
					w.Header().Set(common.RequestIdKey, nativeID)
				}
				if mode == "redirect" {
					w.Header().Set("Location", "/v1/chat/completions")
					w.WriteHeader(307)
					return
				}
				if mode == "native_429" {
					w.WriteHeader(429)
					fmt.Fprint(w, `{"error":"rate_limited"}`)
					return
				}
				usage := official
				if mode == "unknown_usage" {
					usage = `{"total_tokens":12}`
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
					fmt.Fprint(w, "data: {\"model\":\"deepseek-v4-pro\",\"choices\":[{\"delta\":{\"content\":\"native-data\"}}]}\r\n\r\n")
					w.(http.Flusher).Flush()
					fmt.Fprintf(w, "data: {\"model\":\"deepseek-v4-pro\",\"choices\":[],\"usage\":%s}\n\n", normalized)
					if mode != "truncated_sse" {
						fmt.Fprint(w, "data: [DONE]\n\n")
					}
					return
				}
				fmt.Fprintf(w, `{"model":"deepseek-v4-pro","choices":[{"message":{"content":"native-data"}}],"usage":%s}`, usage)
			}))
			defer native.Close()
			billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				parts := strings.Split(r.URL.Path, "/")
				action := parts[len(parts)-1]
				calls = append(calls, action)
				switch action {
				case "catalog":
					version := proRate
					if mode == "wrong_version" {
						version = "old"
					}
					fmt.Fprintf(w, `{"hasAppSeat":true,"region":"cn","currency":"CNY","models":[{"modelId":"deepseek-v4-pro","operations":["chat"],"routes":[{"routeId":"deepseek:deepseek-v4-pro","operation":"chat","unit":"1K tokens","meterVersion":"deepseek-v4-pro-chat-v1","routerRateVersion":"%s"}]}]}`, version)
				case "holds":
					reservations++
					require.NoError(t, common.DecodeJson(r.Body, &hold))
					fmt.Fprintf(w, `{"hold":{"id":"%s","status":"reserved","authorizedUnits":"99"},"duplicate":%t}`, holdID, mode == "duplicate" || reservations > 1)
				case "commit":
					if mode == "invalid_commit" {
						fmt.Fprint(w, "{}")
						return
					}
					var in map[string]any
					require.NoError(t, common.DecodeJson(r.Body, &in))
					fmt.Fprintf(w, `{"hold":{"id":"%s","status":"in_flight","upstreamRef":"%s","committedBy":"smoke","commitAttempt":1}}`, holdID, in["upstreamRef"])
				case "settle":
					require.NoError(t, common.DecodeJson(r.Body, &settled))
					if mode == "settle_failure" {
						w.WriteHeader(503)
						return
					}
					state := "settled"
					if mode == "late_released" {
						state = "released"
					}
					fmt.Fprintf(w, `{"hold":{"id":"%s","status":"%s","settledUnits":"11"}}`, holdID, state)
				default:
					t.Errorf("unexpected billing operation %s", action)
				}
			}))
			defer billing.Close()
			r := gin.New()
			Register(r, NewClient(Config{Enabled: true, InternalURL: billing.URL, ServiceToken: "test", Region: "cn", Currency: "CNY", ExecutorID: "smoke", UpstreamBaseURL: native.URL, UpstreamAPIKey: "native-test-token"}))
			r.POST("/v1/chat/completions", func(c *gin.Context) { t.Error("dispatched to local relay") })
			path := "/v1/chat/completions"
			if mode == "legacy_completions" {
				path = "/v1/completions"
			}
			req := httptest.NewRequest("POST", path+"?key=ignored", bytes.NewBufferString(fmt.Sprintf(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"你好"}],"max_tokens":8,"temperature":0.3,"stream":%t}`, stream)))
			req.Header.Set("Authorization", "Bearer uh_live_test")
			req.Header.Set("X-Request-Id", "same-request")
			req.Header.Set("X-Test-Usage-Tokens", "1000000")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.NotContains(t, calls, "release")
			if mode == "legacy_completions" || mode == "wrong_version" {
				require.Equal(t, 403, w.Code)
				require.NotContains(t, calls, "holds")
				require.Zero(t, nativeCalls)
				return
			}
			require.Equal(t, "CNY", hold.ProviderCurrency)
			require.Equal(t, "0.00117", hold.BaseAmount) // (106*9 + 8*27) / 1M
			require.Equal(t, "same-request", hold.RequestID)
			if mode == "duplicate" {
				require.Equal(t, 409, w.Code)
				require.NotContains(t, calls, "commit")
				require.Zero(t, nativeCalls)
				return
			}
			if mode == "invalid_commit" {
				require.Equal(t, 502, w.Code)
				require.Zero(t, nativeCalls)
				return
			}
			require.Equal(t, 1, nativeCalls)
			require.Contains(t, string(forwarded), `"temperature":0.3`)
			if stream {
				require.Contains(t, string(forwarded), `"include_usage":true`)
			}
			if mode == "json" || mode == "sse" || mode == "settle_failure" || mode == "late_released" {
				require.Equal(t, "CNY", settled.ProviderCurrency)
				require.Equal(t, "0.0000546", settled.BaseAmount)
				require.Equal(t, nativeID, settled.UpstreamRef)
				u := settled.ActualUsage.(map[string]any)
				require.Equal(t, nativeID, u["nativeRequestId"])
				require.Equal(t, float64(4), u["cacheHitTokens"])
				require.Equal(t, float64(6), u["cacheMissTokens"])
			} else {
				require.NotContains(t, calls, "settle")
			}
			switch mode {
			case "json", "sse":
				require.Equal(t, 200, w.Code)
				require.Contains(t, w.Body.String(), "native-data")
				require.Equal(t, nativeID, w.Header().Get(common.RequestIdKey))
				retry := httptest.NewRequest("POST", path, bytes.NewReader(forwarded))
				retry.Header = req.Header.Clone()
				replayed := httptest.NewRecorder()
				r.ServeHTTP(replayed, retry)
				require.Equal(t, http.StatusConflict, replayed.Code)
				require.Equal(t, 1, nativeCalls)
				require.Equal(t, []string{"catalog", "holds", "commit", "settle", "catalog", "holds"}, calls)
				if stream {
					require.True(t, w.Flushed)
					require.True(t, strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n"))
				}
			case "settle_failure", "truncated_sse":
				require.Contains(t, w.Body.String(), "billing_reconciliation_required")
				require.NotContains(t, w.Body.String(), "[DONE]")
			case "native_429":
				require.Equal(t, 429, w.Code)
				require.Equal(t, "reconciliation_required", w.Header().Get("X-UniHub-Billing-State"))
			default:
				require.Equal(t, 502, w.Code)
				require.Contains(t, w.Body.String(), "billing_reconciliation_required")
			}
		})
	}
}

func TestProStreamForwardsContentBeforeUsage(t *testing.T) {
	first := "data: {\"model\":\"deepseek-v4-pro\",\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"
	relay, vendor := io.Pipe()
	release := make(chan struct{})
	go func() {
		defer vendor.Close()
		_, _ = io.WriteString(vendor, first)
		<-release
		_, _ = io.WriteString(vendor, "data: {\"model\":\"deepseek-v4-pro\",\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"prompt_cache_miss_tokens\":5}}\n\ndata: [DONE]\n\n")
	}()
	counts := make(chan proCounts, 1)
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		actual, err := readProStream(c, relay, proChat{stream: true, inputBound: 100, maxTokens: 8})
		if err != nil {
			t.Errorf("stream read: %v", err)
		}
		counts <- actual
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	// Release the producer on failed assertions as well.
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(srv.URL+"/v1/chat/completions", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	payload := make([]byte, len(first))
	_, err = io.ReadFull(resp.Body, payload)
	require.NoError(t, err)
	require.Equal(t, first, string(payload))
	resume()
	tail, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(tail), "prompt_cache_miss_tokens")
	require.NotContains(t, string(tail), "[DONE]")
	require.Equal(t, proCounts{input: 5, output: 1, miss: 5}, <-counts)
}
