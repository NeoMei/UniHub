package platformadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Self-service recovery must issue the same mail as the admin path, and it must
// look identical from outside whether or not the address belongs to an account.
func TestSelfServiceRecoverySendsOnlyForVerifiedKnownEmail(t *testing.T) {
	ctx := context.Background()
	s, _, p := identityRecoveryFixture(t)
	const email = "owner@example.test"

	sent, e := s.SelfServiceRecovery(ctx, "  OWNER@Example.Test ")
	if e != nil || !sent || p.deliveries != 1 {
		t.Fatalf("known email: sent=%v deliveries=%d err=%v", sent, p.deliveries, e)
	}
	if sent, e = s.SelfServiceRecovery(ctx, "stranger@example.test"); e != nil || sent || p.deliveries != 1 {
		t.Fatalf("unknown email answered differently: sent=%v deliveries=%d err=%v", sent, p.deliveries, e)
	}
	p.id.EmailVerified = false
	if sent, e = s.SelfServiceRecovery(ctx, email); e != nil || sent || p.deliveries != 1 {
		t.Fatalf("unverified email sent mail: sent=%v deliveries=%d err=%v", sent, p.deliveries, e)
	}
	p.id.EmailVerified = true
	v, e := s.RedeemRecovery(ctx, p.secret, "CorrectHorse9Battery!", 1)
	if e != nil || v.State != "succeeded" || p.writes != 1 {
		t.Fatalf("self-service credential not redeemable: %+v %v", v, e)
	}
}

func TestSelfServiceRecoveryHTTPAnswersEveryAcceptedRequestTheSame(t *testing.T) {
	s, _, p := identityRecoveryFixture(t)
	const origin = "https://hub.test"
	h := s.SelfServiceRecoveryHandler(origin)

	call := func(email, requestOrigin string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"email": email})
		req := httptest.NewRequest(http.MethodPost, "/auth/recovery/request", strings.NewReader(string(body)))
		req.Header.Set("Origin", requestOrigin)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}

	if res := call("owner@example.test", origin); res.Code != http.StatusAccepted || p.deliveries != 1 {
		t.Fatalf("known email status %d deliveries %d", res.Code, p.deliveries)
	}
	if res := call("stranger@example.test", origin); res.Code != http.StatusAccepted || p.deliveries != 1 {
		t.Fatalf("unknown email status %d deliveries %d", res.Code, p.deliveries)
	}
	if res := call("owner@example.test", "https://evil.test"); res.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status %d", res.Code)
	}
	for i := 0; i < 5; i++ {
		call("owner@example.test", origin)
	}
	if res := call("owner@example.test", origin); res.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit not enforced, status %d", res.Code)
	}
}
