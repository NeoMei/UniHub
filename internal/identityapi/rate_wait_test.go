package identityapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPasswordRateLimitReportsLongestKnownWindow(t *testing.T) {
	s, _, pool := setup(t)
	now := time.Now().UTC()
	for _, entry := range []struct {
		dimension, value string
		window           time.Time
		hits             int
	}{
		{"ip", "192.0.2.1", now.Truncate(time.Minute), 10},
		{"username", "alice", now.Truncate(15 * time.Minute), 5},
	} {
		key := s.mac("limit", "authorize", entry.dimension, "native", entry.value)
		if _, err := pool.Exec(context.Background(), `INSERT INTO identity_rate_limits(key_hash,window_start,hits) VALUES($1,$2,$3)`, key, entry.window, entry.hits); err != nil {
			t.Fatal(err)
		}
	}
	w := request(t, s, authorizePath, authorize(), "")
	var body struct {
		Error      string `json:"error"`
		RetryAfter int    `json:"retry_after"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	sec, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != http.StatusTooManyRequests || err != nil || body.Error != "rate_limited" || body.RetryAfter != sec || sec < 1 || sec > 900 {
		t.Fatalf("status=%d header=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	deadline := time.Now().Add(time.Duration(sec) * time.Second)
	expected := now.Truncate(15 * time.Minute).Add(15 * time.Minute)
	if deadline.Before(expected) || deadline.After(expected.Add(2*time.Second)) {
		t.Fatalf("deadline=%v expected=%v", deadline, expected)
	}
}

func TestPasswordRateStoreFailureIsUnavailable(t *testing.T) {
	s, _, pool := setup(t)
	if _, err := pool.Exec(context.Background(), `ALTER TABLE identity_rate_limits RENAME TO unavailable_rate_limits`); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, authorizePath, authorize(), "")
	if w.Code != 503 || w.Header().Get("Retry-After") != "" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestTokenRateLimitReportsWait(t *testing.T) {
	s, _, pool := setup(t)
	key := s.mac("limit", "token", "token-ip", "native", "192.0.2.1")
	window := time.Now().UTC().Truncate(time.Minute)
	if _, err := pool.Exec(context.Background(), `INSERT INTO identity_rate_limits(key_hash,window_start,hits) VALUES($1,$2,30)`, key, window); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"client_id": "native", "grant_type": "authorization_code", "authorization_id": strings.Repeat("a", 43), "code": "fixture", "redirect_uri": "http://localhost:9000/callback", "code_verifier": verifier}
	w := request(t, s, tokenPath, body, "")
	var result struct {
		Error      string `json:"error"`
		RetryAfter int    `json:"retry_after"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	sec, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != 429 || err != nil || sec < 1 || sec > 60 || result.Error != "rate_limited" || result.RetryAfter != sec {
		t.Fatalf("status=%d header=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
}
