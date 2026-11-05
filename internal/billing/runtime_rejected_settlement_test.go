package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
)

// Removing the rejected observation write must fail this test while the ledger
// assertions continue to pass. Invalid input must never acquire an observation.
func TestRuntimeRejectedSettlementRetainsFirstEvidence(t *testing.T) {
	for _, asset := range []string{RuntimeAsset, "CNY/6"} {
		t.Run(asset, func(t *testing.T) {
			s, scope, b := runtimeFixture(t)
			ctx := context.Background()
			unit := RuntimeUnitVersion
			if asset == "CNY/6" {
				unit = "lr-cny-v1"
			}
			address := runtimeCallAddress(scope, b.PersonalAccountID, asset, "available")
			if _, err := s.ledger.Post(ctx, ledger.Command{Reference: "rejected-evidence-funding", Postings: []ledger.Posting{{Source: "world", Destination: address, Asset: asset, Amount: 100}}}); err != nil {
				t.Fatal(err)
			}
			in := RuntimeReserveRequest{CallID: "rejected-evidence", LocalUserID: "42", AccountID: b.PersonalAccountID, RequestDigest: "request", Model: "native-model", Operation: "native-operation", Asset: asset, UnitVersion: unit, PriceVersion: "v1", PricingDigest: "pricing", Amount: "10"}
			call, err := s.RuntimeReserve(ctx, scope, in)
			if err != nil {
				t.Fatal(err)
			}
			readEvidence := func() json.RawMessage {
				t.Helper()
				var raw json.RawMessage
				// to_jsonb also works on the pre-fix schema: missing evidence is a proper
				// red assertion, rather than an undefined-column harness error.
				if err := s.pool.QueryRow(ctx, `SELECT to_jsonb(c)->'rejected_settlement' FROM product_runtime_calls c WHERE id=$1`, call.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			request := RuntimeSettlementRequest{Amount: "11", PricingDigest: "pricing", Usage: json.RawMessage(`{"requests":1,"provider_observation":"private-evidence"}`)}
			for _, bad := range []RuntimeSettlementRequest{
				{Amount: "011", PricingDigest: "pricing", Usage: request.Usage},
				{Amount: "-1", PricingDigest: "pricing", Usage: request.Usage},
				{Amount: "11", PricingDigest: "wrong", Usage: request.Usage},
				{Amount: "11", PricingDigest: "pricing", Usage: json.RawMessage(`{`)},
				{Amount: "11", PricingDigest: "pricing", Usage: json.RawMessage(`"` + strings.Repeat("x", 64<<10) + `"`)},
			} {
				if _, err := s.RuntimeSettle(ctx, scope, in.CallID, bad); err == nil {
					t.Fatal("invalid settlement accepted")
				}
				if raw := readEvidence(); len(raw) > 0 && string(raw) != "null" {
					t.Fatal("invalid input acquired evidence")
				}
			}
			if _, err := s.RuntimeSettle(ctx, RuntimeScope{ProductID: scope.ProductID, InstanceID: "other"}, in.CallID, request); !errors.Is(err, accounts.ErrNotFound) {
				t.Fatalf("wrong scope: %v", err)
			}
			if raw := readEvidence(); len(raw) > 0 && string(raw) != "null" {
				t.Fatal("wrong scope acquired evidence")
			}
			if _, err := s.RuntimeSettle(ctx, scope, in.CallID, request); !errors.Is(err, ErrConflict) {
				t.Fatalf("overheld must conflict: %v", err)
			}
			restarted := New(s.pool, accounts.New(s.pool), s.ledger)
			restarted.ConfigureProductUsers(s.productUsers)
			got, err := restarted.RuntimeGetCall(ctx, scope, in.CallID)
			if err != nil {
				t.Fatal(err)
			}
			balance, err := s.ledger.Balance(ctx, address, asset)
			if err != nil {
				t.Fatal(err)
			}
			var commands int
			if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE account_id=$1 AND kind='runtime_settle'`, b.PersonalAccountID).Scan(&commands); err != nil {
				t.Fatal(err)
			}
			t.Logf("restart: asset=%s status=%s held=%s charged=%s available=%d settlement_commands=%d", asset, got.Status, got.Held, got.Charged, balance, commands)
			if got.Status != "RESERVED" || got.Held != "10" || got.Charged != "0" || len(got.Usage) != 0 || got.FinalDigest != "" || balance != 90 || commands != 0 {
				t.Fatalf("rejection changed financial/accepted state: %+v balance=%d commands=%d", got, balance, commands)
			}
			first := readEvidence()
			if len(first) == 0 || string(first) == "null" {
				t.Fatal("valid overheld observation lost after restart")
			}
			// Decode explicitly named transport-independent keys used by operations.
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(first, &fields); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"call_id": in.CallID, "account_id": in.AccountID, "asset": asset, "unit_version": unit, "product_id": scope.ProductID, "instance_id": scope.InstanceID, "pricing_digest": "pricing", "held": "10", "amount": "11", "reason": "amount_exceeds_held", "request_digest": "request"} {
				var got string
				if err = json.Unmarshal(fields[key], &got); err != nil || got != want {
					t.Fatalf("evidence %s=%s want %q: %v", key, fields[key], want, err)
				}
			}
			var usage map[string]any
			if err = json.Unmarshal(fields["usage"], &usage); err != nil || usage["provider_observation"] != "private-evidence" {
				t.Fatal("reported usage lost")
			}
			page, err := restarted.QueryCalls(ctx, b.UserID, b.PersonalAccountID, CallFilter{OriginProductID: scope.ProductID}, CallPageRequest{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			for _, view := range []any{got, page} {
				raw, _ := json.Marshal(view)
				if strings.Contains(string(raw), "private-evidence") || strings.Contains(string(raw), "rejected_settlement") {
					t.Fatal("private observation leaked into runtime response")
				}
			}
			// Two independently constructed services exercise the shared database lock.
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					r := request
					if i%2 == 1 {
						r.Amount = "12"
						r.Usage = json.RawMessage(`{"requests":2}`)
					}
					_, e := restarted.RuntimeSettle(ctx, scope, in.CallID, r)
					errs <- e
				}(i)
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				if !errors.Is(e, ErrConflict) {
					t.Fatalf("concurrent retry: %v", e)
				}
			}
			if string(readEvidence()) != string(first) {
				t.Fatal("duplicate/conflicting retry replaced first evidence")
			}
			got, err = restarted.RuntimeTopUp(ctx, scope, in.CallID, RuntimeTopUpRequest{RequestKey: "explicit-extension", Amount: "1"})
			if err != nil || got.Held != "11" {
				t.Fatalf("extension %+v %v", got, err)
			}
			accepted := request
			accepted.Usage = json.RawMessage(`{"accepted_requests":1}`)
			got, err = restarted.RuntimeSettle(ctx, scope, in.CallID, accepted)
			if err != nil || got.Charged != "11" || got.Status != "SETTLED" || !strings.Contains(string(got.Usage), `"accepted_requests": 1`) || got.FinalDigest == "" {
				t.Fatalf("explicit settlement %+v %v", got, err)
			}
			if string(readEvidence()) != string(first) {
				t.Fatal("later snapshot writes erased rejected evidence")
			}
			balance, err = s.ledger.Balance(ctx, address, asset)
			if err != nil || balance != 89 {
				t.Fatalf("final available=%d %v", balance, err)
			}
			// Release remains explicit and also must preserve the historical observation.
			in.CallID = "release-after-rejection"
			call, err = s.RuntimeReserve(ctx, scope, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.RuntimeSettle(ctx, scope, in.CallID, request); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			beforeRelease := readEvidence()
			got, err = s.RuntimeRelease(ctx, scope, in.CallID, RuntimeReleaseRequest{Reason: "explicit cancellation"})
			if err != nil || got.Status != "RELEASED" || got.Charged != "0" {
				t.Fatalf("release %+v %v", got, err)
			}
			if string(readEvidence()) != string(beforeRelease) {
				t.Fatal("release erased rejected evidence")
			}
		})
	}
}

func TestRuntimeRejectedSettlementStorageFailureIsTruthful(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	if _, err := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "fund", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "fixture", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}); err != nil {
		t.Fatal(err)
	}
	in := RuntimeReserveRequest{CallID: "storage-failure", LocalUserID: "42", AccountID: b.PersonalAccountID, RequestDigest: "request", Model: "m", Operation: "chat", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "v", PricingDigest: "p", Amount: "10"}
	call, err := s.RuntimeReserve(ctx, scope, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `CREATE FUNCTION reject_observation_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF to_jsonb(NEW)->'rejected_settlement' IS NOT NULL AND to_jsonb(NEW)->'rejected_settlement'<>'null'::jsonb THEN RAISE EXCEPTION 'test observation storage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_observation BEFORE UPDATE ON product_runtime_calls FOR EACH ROW EXECUTE FUNCTION reject_observation_write()`); err != nil {
		t.Fatal(err)
	}
	request := RuntimeSettlementRequest{Amount: "11", PricingDigest: "p", Usage: json.RawMessage(`{"requests":1}`)}
	if _, err = s.RuntimeSettle(ctx, scope, in.CallID, request); err == nil || errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "test observation storage failure") {
		t.Fatalf("storage failure hidden as ordinary conflict: %v", err)
	}
	var raw json.RawMessage
	if err = s.pool.QueryRow(ctx, `SELECT to_jsonb(c)->'rejected_settlement' FROM product_runtime_calls c WHERE id=$1`, call.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) > 0 && string(raw) != "null" {
		t.Fatal("failed statement committed an observation")
	}
	got, err := s.RuntimeGetCall(ctx, scope, in.CallID)
	if err != nil || got.Status != "RESERVED" || got.Held != "10" || got.Charged != "0" || got.FinalDigest != "" || len(got.Usage) != 0 {
		t.Fatalf("rollback %+v %v", got, err)
	}
	if _, err = s.pool.Exec(ctx, `DROP TRIGGER reject_observation ON product_runtime_calls`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RuntimeSettle(ctx, scope, in.CallID, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry after storage repair: %v", err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT to_jsonb(c)->'rejected_settlement' FROM product_runtime_calls c WHERE id=$1`, call.ID).Scan(&raw); err != nil || len(raw) == 0 || string(raw) == "null" {
		t.Fatalf("storage repair failed: %s %v", raw, err)
	}
}
