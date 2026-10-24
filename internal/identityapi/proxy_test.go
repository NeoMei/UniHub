package identityapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
)

func TestRateAddressTrustsOnlyExplicitLoopbackProxy(t *testing.T) {
	for _, tc := range []struct {
		name, remote string
		values       []string
		trust        bool
		want         string
	}{
		{"direct", "192.0.2.9:9000", []string{"198.51.100.1"}, true, "192.0.2.9"},
		{"disabled", "127.0.0.1:9000", []string{"198.51.100.1"}, false, "127.0.0.1"},
		{"proxy a", "127.0.0.1:9000", []string{"198.51.100.1"}, true, "198.51.100.1"},
		{"proxy b", "[::1]:9000", []string{"198.51.100.2"}, true, "198.51.100.2"},
		{"list rejected", "127.0.0.1:9000", []string{"198.51.100.1, 192.0.2.1"}, true, "127.0.0.1"},
		{"duplicates rejected", "127.0.0.1:9000", []string{"198.51.100.1", "198.51.100.2"}, true, "127.0.0.1"},
		{"port rejected", "127.0.0.1:9000", []string{"198.51.100.1:9000"}, true, "127.0.0.1"},
		{"invalid", "not an ip", []string{"198.51.100.1"}, true, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.values {
				r.Header.Add("X-Real-IP", v)
			}
			s := &Server{opts: Options{TrustLoopbackProxy: tc.trust}}
			if got := s.rateAddress(r); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestDeniedProxyIPCannotGrowUsernameBuckets(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := &Server{pool: pool, opts: Options{SigningKey: []byte("independent-identity-signing-key-for-test"), TrustLoopbackProxy: true}}
	c := products.Client{ClientID: "native"}
	r := httptest.NewRequest("POST", authorizePath, nil)
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("X-Real-IP", "198.51.100.1")
	ipKey := s.mac("limit", "authorize", "ip", c.ClientID, "198.51.100.1")
	window := time.Now().UTC().Truncate(time.Minute)
	// Seed both adjacent windows so the assertion stays stable at minute edges.
	for _, at := range []time.Time{window, window.Add(time.Minute)} {
		if _, err := pool.Exec(ctx, `INSERT INTO identity_rate_limits(key_hash,window_start,hits) VALUES($1,$2,10)`, ipKey, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 20 {
		if s.limit(ctx, r, c, fmt.Sprintf("new-username-%d", i), "authorize") {
			t.Fatal("over-limit proxy IP was allowed")
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity_rate_limits`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("denied requests created rate buckets: rows=%d err=%v", rows, err)
	}
	r.Header.Set("X-Real-IP", "198.51.100.2")
	if !s.limit(ctx, r, c, "different-customer", "authorize") {
		t.Fatal("independent customer inherited the other proxy client's IP limit")
	}
}
