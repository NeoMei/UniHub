package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/metering"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"

	"github.com/stretchr/testify/require"
)

func TestSuperWagieUsageReportsAtProductionVolume(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	require.NoError(t, database.Migrate(ctx, p))
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	require.NoError(t, err)
	accts, err := a.ListAccounts(ctx, u.ID)
	require.NoError(t, err)
	account := accts[0].ID

	const (
		total        = 39083
		creditNative = 27307
		central      = 10940
		released     = 770
		unknown      = 12
		days         = 12
	)
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -days)
	common := billing.Call{
		AccountID: account, ActorID: u.ID, OriginProductID: "superwagie", InstanceID: "default",
		Status: "SETTLED", Held: "11", Charged: "11", ChargeAsset: billing.CreditAsset,
		Rate:          billing.Rate{Asset: billing.CreditAsset, Model: "fixture", Semantics: "llmrouter_native"},
		NativePricing: &billing.NativeQuote{PricingVersion: billing.NativeCreditPricingVersion},
		Usage: &billing.Usage{Metered: &metering.Usage{
			Version: "metered_v2", State: "confirmed", Source: "provider", Operation: "chat",
			Measurements: []metering.Measurement{
				{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "7"}},
				{Key: "output", Unit: "token", Quantity: metering.Quantity{Value: "3"}},
			},
		}},
	}
	centralCall := common
	centralCall.Charged = "1"
	centralCall.Rate.Semantics = "metered_v2"
	centralCall.Rate.Metered = &metering.Price{
		Version: "metered_v2", Operation: "chat", Policy: "sum",
		Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "1", Denominator: "1"}}},
	}
	centralCall.Usage = &billing.Usage{Metered: &metering.Usage{
		Version: "metered_v2", State: "confirmed", Source: "provider", Operation: "chat",
		Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1"}}},
	}}
	centralCall.NativePricing = nil
	legacy := common
	legacy.Charged = "2"
	legacy.Rate.Semantics = "llmrouter_native"
	legacy.NativePricing = &billing.NativeQuote{PricingVersion: "lr-native-v1"}
	legacy.Usage = &billing.Usage{Input: "5", Output: "2", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}

	encode := func(c billing.Call) json.RawMessage {
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		return raw
	}
	// Spread realistic snapshot sizes across 12 days. The extra fixture data
	// makes JSON and TOAST work measurable without copying production data.
	_, err = p.Exec(ctx, `
		INSERT INTO billing_calls (id,account_id,request_digest,snapshot,created_at)
		SELECT 'call-'||i,$1,'fixture',
		  (CASE WHEN i<=$4 THEN $5::jsonb WHEN i<=$4+$6 THEN $7::jsonb ELSE $8::jsonb END)
		  || jsonb_build_object('call_id','call-'||i,'fixture_payload',(
		    SELECT string_agg(md5(i::text||':'||j), '') FROM generate_series(1,64) j
		  ))
		  || CASE WHEN i<=$9 THEN '{"status":"RELEASED","charged":"0"}'::jsonb ELSE '{}'::jsonb END,
		  $2::timestamptz + ((i-1)%12) * interval '1 day' + i * interval '1 second'
		FROM generate_series(1,$3::int) i`, account, start, total, creditNative, encode(common), central, encode(centralCall), encode(legacy), released)
	require.NoError(t, err)
	_, err = p.Exec(ctx, `
		INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status)
		SELECT 'command-'||i,$1,$2,'release','fixture-'||i,'fixture','{}',jsonb_build_object('call',jsonb_build_object('call_id','call-'||i)),
		       CASE WHEN i<=$3 THEN 'APPLIED' ELSE 'UNKNOWN' END
		FROM generate_series(1, $3::int+$4::int) i`, account, u.ID, released, unknown)
	require.NoError(t, err)
	_, err = p.Exec(ctx, `
		INSERT INTO billing_platform_fault_waivers(case_id,call_id,account_id,operator_id,approval_sha256,approval_raw,original_central_row,original_commands,release_command_id,provider_state)
		SELECT 'waiver-'||i,'call-'||i,$1,$2,repeat('0',64),'fixture','{}','[]','command-'||i,'PROVIDER_UNRESOLVED'
		FROM generate_series(1,$3::int) i`, account, u.ID, released)
	require.NoError(t, err)
	_, err = p.Exec(ctx, "ANALYZE billing_calls; ANALYZE billing_commands; ANALYZE billing_platform_fault_waivers")
	require.NoError(t, err)

	client := products.Client{ProductID: "superwagie", InstanceID: "default", ClientID: "sw", Scopes: []string{"usage:read"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{
		Billing: b, ProductVerifiers: map[string]auth.Verifier{"sw": productVerifier{}}, ProductAccess: map[string]products.Client{"sw": client},
	})
	read := func(path string, out any) {
		t.Helper()
		// Leave headroom within the client's 15-second upstream deadline.
		requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		r := httptest.NewRequestWithContext(requestCtx, "GET", "/v1/accounts/"+account+path, nil)
		r.Header.Set("Authorization", "Bearer product-alice")
		w := httptest.NewRecorder()
		t0 := time.Now()
		h.ServeHTTP(w, r)
		t.Logf("%s: %s", path, time.Since(t0))
		require.Equal(t, 200, w.Code, w.Body.String())
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), out))
	}

	var summary billing.UsageSummary
	read("/usage/summary", &summary)
	require.Equal(t, strconv.Itoa(total), summary.CallsTotal)
	require.Equal(t, strconv.Itoa(total-released-unknown), summary.SettledCalls)
	require.Equal(t, strconv.Itoa(unknown), summary.UnknownCalls)
	require.Equal(t, strconv.Itoa(released), summary.ReleasedCalls)
	require.False(t, summary.UsageComplete)
	require.Equal(t, strconv.Itoa(total-creditNative-central), summary.UsageCoverage.LegacyTokenCalls)
	require.Equal(t, strconv.Itoa(creditNative+central-released-unknown), summary.UsageCoverage.NativeMeteredCalls)
	require.Equal(t, strconv.Itoa((creditNative-released)*11+central+(total-creditNative-central)*2), summary.ChargesByAsset[billing.CreditAsset])
	require.Equal(t, strconv.Itoa(5*(total-creditNative-central)), summary.Usage.Input)
	require.Len(t, summary.NativeUsage, 1)
	require.Equal(t, "chat", summary.NativeUsage[0].Operation)
	require.Len(t, summary.NativeUsage[0].Measurements, 3)
	require.Equal(t, strconv.Itoa(7*(creditNative-released-unknown)), summary.NativeUsage[0].Measurements[0].Quantity.Value)
	require.Equal(t, strconv.Itoa(3*(creditNative-released-unknown)), summary.NativeUsage[0].Measurements[1].Quantity.Value)
	require.Equal(t, strconv.Itoa(central), summary.NativeUsage[0].Measurements[2].Quantity.Value)

	var page billing.CallPage
	read("/calls?limit=20", &page)
	require.Len(t, page.Items, 20)
	require.Equal(t, strconv.Itoa(total), page.Total)
	require.True(t, page.HasMore)
	require.NotEmpty(t, page.NextCursor)
	var dailyTotal int
	for day := 0; day < days; day++ {
		from := start.AddDate(0, 0, day)
		q := url.Values{"limit": {"1"}, "from": {from.Format(time.RFC3339)}, "to": {from.AddDate(0, 0, 1).Format(time.RFC3339)}}
		var observation billing.CallPage
		read("/calls?"+q.Encode(), &observation)
		require.Len(t, observation.Items, 1)
		n, err := strconv.Atoi(observation.Total)
		require.NoError(t, err)
		require.Equal(t, (total+days-1-day)/days, n)
		dailyTotal += n
	}
	require.Equal(t, total, dailyTotal)
}
