package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const transactionResponse = `{"data":{"id":0,"reference":"transfer:42","postings":[{"source":"wallets:alice","destination":"wallets:bob","asset":"CNY/6","amount":80000000}],"metadata":{},"timestamp":"2026-10-24T10:00:00Z","insertedAt":"2026-10-24T10:00:00Z","reverted":false}}`

func transfer() Command {
	return Command{Reference: "transfer:42", Postings: []Posting{{Source: "wallets:alice", Destination: "wallets:bob", Asset: "CNY/6", Amount: 80000000}}}
}

func TestPostUsesPinnedProtocolAndStableReplay(t *testing.T) {
	var bodies [][]byte
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v2/unihub/transactions" {
			t.Errorf("wrong endpoint %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing JSON content type")
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		var request struct {
			Reference string `json:"reference"`
			Postings  []struct {
				Source      string      `json:"source"`
				Destination string      `json:"destination"`
				Asset       string      `json:"asset"`
				Amount      json.Number `json:"amount"`
			} `json:"postings"`
			Force bool `json:"force"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		if request.Reference != "transfer:42" || request.Force || len(request.Postings) != 1 {
			t.Errorf("unsafe request: %s", body)
		} else if p := request.Postings[0]; p.Source != "wallets:alice" || p.Destination != "wallets:bob" || p.Asset != "CNY/6" || p.Amount.String() != "80000000" {
			t.Errorf("wrong posting: %s", body)
		}
		if len(bodies) > 1 {
			w.Header().Set("Idempotency-Hit", "true")
		}
		io.WriteString(w, transactionResponse)
	}))
	defer server.Close()
	f := NewFormance(server.URL, "unihub", server.Client())
	for range 2 {
		tx, err := f.Post(context.Background(), transfer())
		if err != nil || tx.ID != "0" || tx.Reference != "transfer:42" || !reflect.DeepEqual(tx.Postings, transfer().Postings) {
			t.Fatalf("Post = %#v, %v", tx, err)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("unstable replay: keys=%v bodies=%q", keys, bodies)
	}
}

func TestPostClassifiesErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"funds", 400, `{"errorCode":"INSUFFICIENT_FUND","errorMessage":"no funds"}`, ErrInsufficientFunds},
		{"reference conflict", 409, `{"errorCode":"CONFLICT","errorMessage":"duplicate reference"}`, ErrConflict},
		{"idempotency mismatch", 400, `{"errorCode":"VALIDATION","errorMessage":"invalid idempotency hash"}`, ErrConflict},
		{"server failure", 500, `{"errorCode":"INTERNAL","errorMessage":"commit failed"}`, ErrUnknown},
		{"malformed rejection", 502, `gateway error`, ErrUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer s.Close()
			_, err := NewFormance(s.URL, "unihub", s.Client()).Post(context.Background(), transfer())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPostUnknownTransportOutcomeDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer s.Close()
	_, err := NewFormance(s.URL, "unihub", s.Client()).Post(context.Background(), transfer())
	if !errors.Is(err, ErrUnknown) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestPostDoesNotImplicitlyRetryOnReusedConnection(t *testing.T) {
	var posts atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.Method == "GET" {
			io.WriteString(w, `{"data":{"CNY/6":100000000}}`)
			return
		}
		if posts.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		io.WriteString(w, transactionResponse)
	}))
	defer s.Close()
	f := NewFormance(s.URL, "unihub", s.Client())
	if _, err := f.Balance(context.Background(), "wallets:alice", "CNY/6"); err != nil {
		t.Fatal(err)
	}
	_, err := f.Post(context.Background(), transfer())
	if !errors.Is(err, ErrUnknown) || posts.Load() != 1 {
		t.Fatalf("implicit replay occurred: posts=%d err=%v", posts.Load(), err)
	}
}

func TestPostRejectsCorruptSuccess(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{"reference":"transfer:42","postings":[]}}`, strings.Replace(transactionResponse, `"id":0`, `"id":1.2`, 1), strings.Replace(transactionResponse, `"reference":"transfer:42"`, `"reference":"other"`, 1), strings.Replace(transactionResponse, `80000000`, `80000001`, 1), strings.Replace(transactionResponse, `80000000`, `8e7`, 1), strings.Replace(transactionResponse, `80000000`, `80000000.0`, 1), transactionResponse + `{}`, `{"data":{"id":1}}`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer s.Close()
			tx, err := NewFormance(s.URL, "unihub", s.Client()).Post(context.Background(), transfer())
			if !errors.Is(err, ErrUnknown) {
				t.Fatalf("corrupt success accepted: %#v, %v", tx, err)
			}
		})
	}
}

func TestFindReturnsImmutablePostingsAndExactLargeID(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v2/unihub/transactions" {
			t.Errorf("bad route: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("query") != `{"$match":{"reference":"transfer:42"}}` {
			t.Errorf("wrong query: %s", r.URL.RawQuery)
		}
		io.WriteString(w, `{"cursor":{"pageSize":2,"hasMore":false,"data":[{"id":9007199254740993,"reference":"transfer:42","postings":[{"source":"wallets:alice","destination":"wallets:bob","asset":"CNY/6","amount":"80000000"}],"metadata":{},"timestamp":"2026-10-24T10:00:00Z","reverted":false}]}}`)
	}))
	defer s.Close()
	tx, err := NewFormance(s.URL, "unihub", s.Client()).Find(context.Background(), "transfer:42")
	if err != nil || tx == nil || tx.ID != "9007199254740993" || !reflect.DeepEqual(tx.Postings, transfer().Postings) {
		t.Fatalf("Find = %#v, %v", tx, err)
	}
}

func TestFindEmptyAndInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		body  string
		empty bool
	}{
		{`{"cursor":{"pageSize":2,"hasMore":false,"data":[]}}`, true},
		{`{}`, false}, {`{"cursor":{}}`, false}, {`{"cursor":{"data":null}}`, false},
		{`{"cursor":{"data":[{"id":0,"reference":"other","postings":[]}]}}`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) }))
			defer s.Close()
			tx, err := NewFormance(s.URL, "unihub", s.Client()).Find(context.Background(), "transfer:42")
			if tc.empty {
				if err != nil || tx != nil {
					t.Fatalf("expected absent: %#v,%v", tx, err)
				}
			} else if !errors.Is(err, ErrUnknown) {
				t.Fatalf("bad query response accepted: %#v,%v", tx, err)
			}
		})
	}
}

func TestBalanceDecodesExactInteger(t *testing.T) {
	for _, tc := range []struct {
		body    string
		want    int64
		invalid bool
	}{
		{`{"data":{"CNY/6":9007199254740993}}`, 9007199254740993, false},
		{`{"data":{"CNY/6":"9007199254740993"}}`, 9007199254740993, false},
		{`{"data":{}}`, 0, false}, {`{}`, 0, true}, {`{"data":null}`, 0, true},
		{`{"data":{"CNY/6":1.5}}`, 0, true}, {`{"data":{"CNY/6":1e6}}`, 0, true},
		{`{"data":{"CNY/6":9223372036854775808}}`, 0, true},
		{`{"data":{"CNY/6":null}}`, 0, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v2/unihub/aggregate/balances" || r.URL.Query().Get("query") != `{"$match":{"address":"wallets:alice"}}` {
					t.Errorf("bad balance request: %s %s", r.Method, r.URL.String())
				}
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			balance, err := NewFormance(s.URL, "unihub", s.Client()).Balance(context.Background(), "wallets:alice", "CNY/6")
			if tc.invalid {
				if !errors.Is(err, ErrUnknown) {
					t.Fatalf("invalid balance accepted: %d,%v", balance, err)
				}
			} else if err != nil || balance != tc.want {
				t.Fatalf("Balance=%d,%v; want %d", balance, err, tc.want)
			}
		})
	}
}

func TestEnsureCreatesLedgerAndVerifiesExisting(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			gets := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/unihub" {
					t.Errorf("wrong ledger path %s", r.URL.Path)
				}
				if r.Method == "POST" {
					if existing {
						w.WriteHeader(400)
						io.WriteString(w, `{"errorCode":"LEDGER_ALREADY_EXISTS"}`)
					} else {
						w.WriteHeader(204)
					}
				} else if r.Method == "GET" {
					gets++
					io.WriteString(w, `{"data":{"name":"unihub","bucket":"_default","metadata":{}}}`)
				} else {
					t.Error("unexpected method")
				}
			}))
			defer s.Close()
			if err := NewFormance(s.URL, "unihub", s.Client()).Ensure(context.Background()); err != nil {
				t.Fatal(err)
			}
			if existing && gets != 1 {
				t.Errorf("existing ledger was not read back: %d", gets)
			}
		})
	}
}

func TestPostHonorsContextDeadline(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer s.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewFormance(s.URL, "unihub", s.Client()).Post(ctx, transfer())
	if !errors.Is(err, ErrUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected uncertain deadline: %v", err)
	}
}

func TestPostRejectsMissingTransactionIDWithReason(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Replace(transactionResponse, `"id":0,`, "", 1))
	}))
	defer s.Close()
	_, err := NewFormance(s.URL, "unihub", s.Client()).Post(context.Background(), transfer())
	if !errors.Is(err, ErrUnknown) || !strings.Contains(err.Error(), "transaction ID") {
		t.Fatalf("missing ID must remain uncertain with a useful reason: %v", err)
	}
}

func TestPostDoesNotFollowRedirect(t *testing.T) {
	redirected := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected = true
			io.WriteString(w, transactionResponse)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	_, err := NewFormance(s.URL, "unihub", s.Client()).Post(context.Background(), transfer())
	if !errors.Is(err, ErrUnknown) || redirected {
		t.Fatalf("redirected=%v err=%v", redirected, err)
	}
}

func TestFindRejectsMultipleMatchesAndCorruptTransaction(t *testing.T) {
	tx := strings.TrimSuffix(strings.TrimPrefix(transactionResponse, `{"data":`), `}`)
	for _, body := range []string{
		`{"cursor":{"data":[` + tx + `,` + tx + `],"hasMore":false}}`,
		`{"cursor":{"data":[` + tx + `],"hasMore":true}}`,
		`{"cursor":{"data":[` + strings.Replace(tx, `80000000`, `9223372036854775808`, 1) + `],"hasMore":false}}`,
		`{"cursor":{"data":[` + strings.Replace(tx, `"reverted":false`, `"reverted":true`, 1) + `],"hasMore":false}}`,
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		found, err := NewFormance(s.URL, "unihub", s.Client()).Find(context.Background(), "transfer:42")
		s.Close()
		if found != nil || !errors.Is(err, ErrUnknown) {
			t.Fatalf("ambiguous lookup accepted: %#v, %v", found, err)
		}
	}
}

func TestEnsureRejectsCorruptExistingLedgerReadback(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(400)
			io.WriteString(w, `{"errorCode":"LEDGER_ALREADY_EXISTS"}`)
			return
		}
		io.WriteString(w, `{"data":{"name":"other"}}`)
	}))
	defer s.Close()
	if err := NewFormance(s.URL, "unihub", s.Client()).Ensure(context.Background()); !errors.Is(err, ErrUnknown) {
		t.Fatalf("accepted mismatched ledger: %v", err)
	}
}

func TestProvidedTimeoutIsHonoredWithoutMutatingClient(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer s.Close()
	defer close(release)
	client := s.Client()
	client.Timeout = 20 * time.Millisecond
	_, err := NewFormance(s.URL, "unihub", client).Post(context.Background(), transfer())
	if !errors.Is(err, ErrUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected client timeout: %v", err)
	}
	if client.CheckRedirect != nil || client.Timeout != 20*time.Millisecond {
		t.Fatal("constructor mutated provided client")
	}
}
