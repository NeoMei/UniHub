package billing_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"sync"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/platformadmin"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestPlatformFaultWaiverDecisionSchemaIsDurable(t *testing.T) {
	p := testutil.Database(t)
	if err := database.Migrate(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := p.QueryRow(context.Background(), `SELECT to_regclass('billing_platform_fault_waivers') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("platform fault waiver has no durable immutable evidence table")
	}
}
func TestPlatformFaultWaiverRejectsUnboundApproval(t *testing.T) {
	s := billing.New(nil, nil, nil)
	if _, err := s.AdminPlatformFaultWaiver(context.Background(), nil, "operator", []byte(`{}`), "bad"); err == nil {
		t.Fatal("unbound approval accepted")
	}
}

type waiverLedger struct {
	mu       sync.Mutex
	balances map[string]int64
	txs      map[string]ledger.Transaction
	loss     bool
	posts    int
}

func (l *waiverLedger) Post(_ context.Context, c ledger.Command) (ledger.Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.txs[c.Reference]; ok {
		return old, nil
	}
	for _, p := range c.Postings {
		if l.balances[p.Source] < p.Amount {
			return ledger.Transaction{}, ledger.ErrInsufficientFunds
		}
	}
	for _, p := range c.Postings {
		l.balances[p.Source] -= p.Amount
		l.balances[p.Destination] += p.Amount
	}
	tr := ledger.Transaction{ID: fmt.Sprint(len(l.txs) + 1), Reference: c.Reference, Postings: c.Postings}
	l.txs[c.Reference] = tr
	l.posts++
	if l.loss {
		l.loss = false
		return ledger.Transaction{}, ledger.ErrUnknown
	}
	return tr, nil
}
func (l *waiverLedger) Find(_ context.Context, ref string) (*ledger.Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.txs[ref]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (l *waiverLedger) Balance(_ context.Context, address, asset string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if asset != billing.CashAsset {
		return 0, ledger.ErrConflict
	}
	return l.balances[address], nil
}

type waiverFixture struct {
	s         *billing.Service
	p         *pgxpool.Pool
	authority *platformadmin.Store
	l         *waiverLedger
	approval  billing.PlatformFaultWaiverApproval
	call      billing.Call
	operator  string
}

func waiverSetup(t *testing.T) waiverFixture {
	t.Helper()
	return waiverSetupOrigin(t, "llmrouter")
}

func waiverSetupOrigin(t *testing.T, origin string) waiverFixture {
	t.Helper()
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	store := accounts.New(p)
	u, e := store.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://waiver.test", Subject: "customer"})
	if e != nil {
		t.Fatal(e)
	}
	op, e := store.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://waiver.test", Subject: "operator"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := store.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := as[0].ID
	if _, e = p.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,'["finance_admin"]','test',$1)`, op.ID); e != nil {
		t.Fatal(e)
	}
	call := billing.Call{CallID: "approved-case-call", ServiceID: "llmrouter", ActorID: u.ID, AccountID: account, OriginProductID: origin, InstanceID: "default", ChargeAsset: billing.CashAsset, FundingMode: billing.FundingPrepaidCash, FundingProductID: "llmrouter", Held: "2048", Charged: "0", Status: "RESERVED", RequestDigest: "request-digest", CredentialID: "credential", TaskID: "task", WireOperation: "embeddings.create", Rate: billing.Rate{ID: "rate", Model: "model", Asset: billing.CashAsset}, CashHoldAddress: billing.CashHoldAddress(account, "approved-case-call")}
	raw, _ := json.Marshal(call)
	if _, e = p.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'reserve-request',$3)`, call.CallID, account, raw); e != nil {
		t.Fatal(e)
	}
	reserve := ledger.Command{Reference: "unihub:billing:original-hold", Postings: []ledger.Posting{{Source: wallet.Address(account, "llmrouter"), Destination: call.CashHoldAddress, Asset: billing.CashAsset, Amount: 2048}}}
	rc, _ := json.Marshal(reserve)
	effect, _ := json.Marshal(map[string]any{"call": call})
	if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status,ledger_transaction_id) VALUES('original-hold',$1,$2,'reserve',$3,'reserve-hash',$4,$5,'APPLIED','original-ledger-id')`, account, u.ID, call.CallID, rc, effect); e != nil {
		t.Fatal(e)
	}
	var row json.RawMessage
	if e = p.QueryRow(ctx, `SELECT to_jsonb(c) FROM billing_calls c WHERE id=$1`, call.CallID).Scan(&row); e != nil {
		t.Fatal(e)
	}
	rateRaw, _ := json.Marshal(call.Rate)
	gateway, _ := json.Marshal(map[string]any{"Digest": call.RequestDigest, "CredentialID": call.CredentialID, "TaskID": call.TaskID, "WireOperation": call.WireOperation, "Model": call.Rate.Model, "RateID": call.Rate.ID, "RateSnapshot": string(rateRaw), "BillingProfileVersion": call.BillingProfileVersion, "CallID": call.CallID, "ActorID": u.ID, "AccountID": account, "OriginProductID": origin, "InstanceID": "default", "Asset": billing.CashAsset, "FundingMode": billing.FundingPrepaidCash, "FundingProductID": "llmrouter", "Amount": "2048", "Status": "UNKNOWN", "Evidence": "", "Intent": "", "Charged": "", "EvidenceConflict": false})
	attempts, _ := json.Marshal([]map[string]any{{"ID": 57, "ChannelID": 102, "CallID": call.CallID, "Status": "DISPATCHED"}})
	a := billing.PlatformFaultWaiverApproval{ApprovalStatement: "Explicitly approved full customer hold return for platform fault; supplier unresolved", ApprovalSourceSHA256: fmt.Sprintf("%064d", 3), Schema: 1, CaseID: "incident-approved", OperatorID: op.ID, Reason: "Customer waiver approved for platform failure; supplier result unresolved", CallID: call.CallID, ActorID: u.ID, AccountID: account, OriginProductID: origin, InstanceID: "default", Asset: billing.CashAsset, Held: "2048", ProviderState: "PROVIDER_UNRESOLVED", OriginalEvidenceSHA256: fmt.Sprintf("%064d", 1), FollowupEvidenceSHA256: fmt.Sprintf("%064d", 2), ExpectedCentralRow: row, GatewayCall: gateway, GatewayAttempts: attempts}
	l := &waiverLedger{balances: map[string]int64{call.CashHoldAddress: 2048}, txs: map[string]ledger.Transaction{reserve.Reference: {ID: "original-ledger-id", Reference: reserve.Reference, Postings: reserve.Postings}}}
	return waiverFixture{s: billing.New(p, store, l), p: p, authority: platformadmin.New(p, nil), l: l, approval: a, call: call, operator: op.ID}
}
func waiverBytes(a billing.PlatformFaultWaiverApproval) ([]byte, string) {
	b, _ := json.Marshal(a)
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:])
}
func (f waiverFixture) apply() (billing.Call, error) {
	b, h := waiverBytes(f.approval)
	return f.s.AdminPlatformFaultWaiver(context.Background(), f.authority, f.operator, b, h)
}

func TestPlatformFaultWaiverReleasesExactHoldAndPreservesEvidence(t *testing.T) {
	for _, origin := range []string{"llmrouter", "superwagie"} {
		t.Run(origin, func(t *testing.T) {
			f := waiverSetupOrigin(t, origin)
			ctx := context.Background()
			got, e := f.apply()
			if e != nil {
				t.Fatal(e)
			}
			if got.OriginProductID != origin || got.FundingProductID != "llmrouter" || got.Status != "RELEASED" || got.Charged != "0" || got.ProviderCost != "" || got.Usage != nil {
				t.Fatalf("false settlement: %+v", got)
			}
			n, _ := f.l.Balance(ctx, wallet.Address(f.call.AccountID, "llmrouter"), billing.CashAsset)
			if n != 2048 {
				t.Fatal(n)
			}
			hold, _ := f.l.Balance(ctx, f.call.CashHoldAddress, billing.CashAsset)
			if hold != 0 {
				t.Fatal(hold)
			}
			var raw []byte
			var state string
			var before json.RawMessage
			if e = f.p.QueryRow(ctx, `SELECT approval_raw,provider_state,original_central_row FROM billing_platform_fault_waivers`).Scan(&raw, &state, &before); e != nil {
				t.Fatal(e)
			}
			want, _ := waiverBytes(f.approval)
			if string(raw) != string(want) || state != "PROVIDER_UNRESOLVED" {
				t.Fatal("approval evidence changed")
			}
			var originalRow map[string]any
			var archivedRow map[string]any
			_ = json.Unmarshal(f.approval.ExpectedCentralRow, &originalRow)
			_ = json.Unmarshal(before, &archivedRow)
			if !reflect.DeepEqual(originalRow, archivedRow) {
				t.Fatal("original row overwritten")
			}
			for _, sql := range []string{`UPDATE billing_platform_fault_waivers SET provider_state='PROVIDER_UNRESOLVED'`, `DELETE FROM billing_platform_fault_waivers`} {
				if _, e = f.p.Exec(ctx, sql); e == nil {
					t.Fatal("immutable evidence rewrite accepted")
				}
			}
			if _, e = f.apply(); e != nil {
				t.Fatal("identical stale-snapshot replay failed", e)
			}
			if f.l.posts != 1 {
				t.Fatal("replay moved money twice", f.l.posts)
			}
			f.approval.Reason = "changed approval"
			if _, e = f.apply(); !errors.Is(e, billing.ErrConflict) {
				t.Fatal("changed decision accepted", e)
			}
		})
	}
}

func TestPlatformFaultWaiverRejectsChangedScopeStateAndAuthority(t *testing.T) {
	for _, name := range []string{"account", "amount", "state", "snapshot", "operator", "authority", "hold", "supplier-evidence", "request-binding"} {
		t.Run(name, func(t *testing.T) {
			f := waiverSetup(t)
			ctx := context.Background()
			switch name {
			case "account":
				f.approval.AccountID = "other"
			case "amount":
				f.approval.Held = "2049"
			case "state":
				_, _ = f.p.Exec(ctx, `UPDATE billing_calls SET snapshot=jsonb_set(snapshot,'{status}','"SETTLED"')`)
			case "snapshot":
				_, _ = f.p.Exec(ctx, `UPDATE billing_calls SET request_digest='changed'`)
			case "operator":
				f.operator = f.call.ActorID
			case "authority":
				_, _ = f.p.Exec(ctx, `UPDATE platform_admin_roles SET roles='[]'`)
			case "hold":
				f.l.balances[f.call.CashHoldAddress] = 2047
			case "request-binding":
				var gateway map[string]any
				_ = json.Unmarshal(f.approval.GatewayCall, &gateway)
				gateway["Digest"] = "wrong-request"
				f.approval.GatewayCall, _ = json.Marshal(gateway)
			case "supplier-evidence":
				f.approval.GatewayCall = []byte(`{"CallID":"other"}`)
			}
			if _, e := f.apply(); e == nil {
				t.Fatal("changed scope accepted")
			}
			if f.l.posts != 0 {
				t.Fatal("rejection moved money")
			}
			var count int
			if e := f.p.QueryRow(ctx, `SELECT count(*) FROM billing_platform_fault_waivers`).Scan(&count); e != nil || count != 0 {
				t.Fatalf("rejection persisted case %d %v", count, e)
			}
		})
	}
}
func TestPlatformFaultWaiverResponseLossResumesOneLedgerReference(t *testing.T) {
	f := waiverSetup(t)
	f.l.loss = true
	if _, e := f.apply(); !errors.Is(e, billing.ErrPending) {
		t.Fatal("response loss forced success", e)
	}
	if _, e := f.s.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	if got, e := f.apply(); e != nil || got.Status != "RELEASED" {
		t.Fatal(got, e)
	}
	if f.l.posts != 1 {
		t.Fatal("recovery duplicated refund", f.l.posts)
	}
}
func TestPlatformFaultWaiverConcurrentReplayCreatesOneCommand(t *testing.T) {
	f := waiverSetup(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() { <-start; _, e := f.apply(); errs <- e }()
	}
	close(start)
	for range 2 {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := f.p.QueryRow(context.Background(), `SELECT count(*) FROM billing_commands WHERE kind='release'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("commands %d %v", count, e)
	}
	if f.l.posts != 1 {
		t.Fatal("concurrency duplicated refund", f.l.posts)
	}
}
func TestPlatformFaultWaiverAuditFailureRollsBackCommand(t *testing.T) {
	f := waiverSetup(t)
	ctx := context.Background()
	if _, e := f.p.Exec(ctx, `CREATE FUNCTION fail_waiver_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER fail_waiver_audit BEFORE INSERT ON platform_admin_audit FOR EACH ROW EXECUTE FUNCTION fail_waiver_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.apply(); e == nil {
		t.Fatal("audit failure ignored")
	}
	if f.l.posts != 0 {
		t.Fatal("audit failure moved money")
	}
	var count int
	if e := f.p.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE kind='release'`).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed audit retained release", count, e)
	}
}

func TestPlatformFaultWaiverApprovalRejectsDuplicateAndUnknownFields(t *testing.T) {
	f := waiverSetup(t)
	good, _ := waiverBytes(f.approval)
	for _, extra := range []string{`,"schema":1`, `,"unrecognized":true`} {
		raw := append(append([]byte{}, good[:len(good)-1]...), []byte(extra+"}")...)
		sum := sha256.Sum256(raw)
		if _, err := billing.ParsePlatformFaultWaiverApproval(raw, hex.EncodeToString(sum[:])); err == nil {
			t.Fatal("ambiguous approval accepted")
		}
	}
}
func TestPlatformFaultWaiverSchemaInstallerDoesNotBootstrapDatabase(t *testing.T) {
	p := testutil.Database(t)
	if err := database.InstallPlatformFaultWaiverSchema(context.Background(), p); err == nil {
		t.Fatal("schema installer bootstrapped empty database")
	}
	var n int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unrequested schema changes %d %v", n, err)
	}
}
func TestPlatformFaultWaiverSchemaInstallerIsIdempotent(t *testing.T) {
	f := waiverSetup(t)
	for range 2 {
		if err := database.InstallPlatformFaultWaiverSchema(context.Background(), f.p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlatformFaultWaiverPreviewChecksLiveAuthorityAndMovesNoMoney(t *testing.T) {
	f := waiverSetup(t)
	ctx := context.Background()
	raw, sha := waiverBytes(f.approval)
	got, e := f.s.PreviewAdminPlatformFaultWaiver(ctx, f.authority, f.operator, raw, sha)
	if e != nil || got.Status != "RESERVED" {
		t.Fatal(got, e)
	}
	if f.l.posts != 0 {
		t.Fatal("preview moved money")
	}
	var decisions, commands, audits int
	if e = f.p.QueryRow(ctx, `SELECT (SELECT count(*) FROM billing_platform_fault_waivers),(SELECT count(*) FROM billing_commands WHERE kind='release'),(SELECT count(*) FROM platform_admin_audit)`).Scan(&decisions, &commands, &audits); e != nil || decisions != 0 || commands != 0 || audits != 0 {
		t.Fatal("preview wrote state", decisions, commands, audits, e)
	}
	if _, e = f.p.Exec(ctx, `UPDATE platform_admin_roles SET roles='[]'`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PreviewAdminPlatformFaultWaiver(ctx, f.authority, f.operator, raw, sha); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("preview ignored live authority", e)
	}
}

func TestPlatformFaultWaiverApprovalRequiresUserDecisionEvidence(t *testing.T) {
	f := waiverSetup(t)
	raw, _ := waiverBytes(f.approval)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	delete(body, "approval_statement")
	delete(body, "approval_source_sha256")
	raw, _ = json.Marshal(body)
	sum := sha256.Sum256(raw)
	if _, err := billing.ParsePlatformFaultWaiverApproval(raw, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("approval missing the actual user decision evidence was accepted")
	}
}

func TestPlatformFaultWaiverRejectsUnknownProduct(t *testing.T) {
	f := waiverSetupOrigin(t, "unknown-product")
	raw, digest := waiverBytes(f.approval)
	if _, err := billing.ParsePlatformFaultWaiverApproval(raw, digest); !errors.Is(err, billing.ErrInvalid) {
		t.Fatalf("unexpected product accepted: %v", err)
	}
	if f.l.posts != 0 {
		t.Fatal("invalid product moved money")
	}
}

func TestPlatformFaultWaiverNativePricingPreservesFrozenQuoteAndFunding(t *testing.T) {
	for _, change := range []string{"", "quote", "quote_duplicate", "quote_unknown", "price", "credential", "origin", "funding"} {
		t.Run("native_"+change, func(t *testing.T) {
			f := waiverSetupOrigin(t, "superwagie")
			price := `{"native_price":"frozen"}`
			sum := sha256.Sum256([]byte(price))
			quote := billing.NativeQuote{PricingVersion: "lr-native-v1", PricingDigest: hex.EncodeToString(sum[:]), BillingModel: "model", WireOperation: f.call.WireOperation, RouteID: "12", ChannelID: "102", Group: "default", NativeQuotaMax: "2048", Conversion: billing.NativeConversion{Version: "lr-quota-usd-cny-v1", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}, Asset: billing.CashAsset, MaxAmount: "2048"}
			f.call.NativePricing = &quote
			f.call.CredentialVersion = 2
			f.call.Rate = billing.Rate{Model: "model", Asset: billing.CashAsset, Semantics: "llmrouter_native"}
			snapshot, e := json.Marshal(f.call)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.p.Exec(context.Background(), `UPDATE billing_calls SET snapshot=$2 WHERE id=$1`, f.call.CallID, snapshot); e != nil {
				t.Fatal(e)
			}
			if _, e = f.p.Exec(context.Background(), `UPDATE billing_commands SET effect=jsonb_set(effect,'{call}',$2::jsonb) WHERE request_key=$1`, f.call.CallID, snapshot); e != nil {
				t.Fatal(e)
			}
			if e = f.p.QueryRow(context.Background(), `SELECT to_jsonb(c) FROM billing_calls c WHERE id=$1`, f.call.CallID).Scan(&f.approval.ExpectedCentralRow); e != nil {
				t.Fatal(e)
			}
			var gateway map[string]any
			if e = json.Unmarshal(f.approval.GatewayCall, &gateway); e != nil {
				t.Fatal(e)
			}
			q, e := json.Marshal(quote)
			if e != nil {
				t.Fatal(e)
			}
			gateway["RateID"], gateway["RateSnapshot"] = "", ""
			gateway["PricingSource"], gateway["NativeQuoteJSON"], gateway["NativePriceSnapshot"], gateway["CredentialVersion"] = "llmrouter_native", string(q), price, 2
			switch change {
			case "quote":
				gateway["NativeQuoteJSON"] = string(q[:len(q)-1]) + `,"max_amount":"4096"}`
			case "quote_duplicate":
				gateway["NativeQuoteJSON"] = string(q[:len(q)-1]) + `,"max_amount":"2048"}`
			case "quote_unknown":
				gateway["NativeQuoteJSON"] = string(q[:len(q)-1]) + `,"unknown":"value"}`
			case "price":
				gateway["NativePriceSnapshot"] = `{"native_price":"changed"}`
			case "credential":
				gateway["CredentialVersion"] = 3
			case "origin":
				gateway["OriginProductID"] = "llmrouter"
			case "funding":
				gateway["FundingProductID"] = "superwagie"
			}
			f.approval.GatewayCall, e = json.Marshal(gateway)
			if e != nil {
				t.Fatal(e)
			}
			raw, digest := waiverBytes(f.approval)
			_, previewErr := f.s.PreviewAdminPlatformFaultWaiver(context.Background(), f.authority, f.operator, raw, digest)
			if f.l.posts != 0 {
				t.Fatal("preview moved funds")
			}
			if change == "" && previewErr != nil {
				t.Fatal(previewErr)
			}
			if change != "" && previewErr == nil {
				t.Fatal("preview accepted changed native proof")
			}
			got, e := f.apply()
			if change != "" {
				if e == nil || f.l.posts != 0 {
					t.Fatalf("changed %s accepted or moved funds: %v", change, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if got.Status != "RELEASED" || got.Charged != "0" || got.OriginProductID != "superwagie" || got.FundingProductID != "llmrouter" || !reflect.DeepEqual(got.NativePricing, &quote) || got.CredentialVersion != 2 {
				t.Fatalf("changed native accounting scope: %+v", got)
			}
			if f.l.balances[f.call.CashHoldAddress] != 0 || f.l.balances[wallet.Address(f.call.AccountID, "llmrouter")] != 2048 {
				t.Fatal("hold was not returned to original product wallet")
			}
			if _, e = f.apply(); e != nil || f.l.posts != 1 {
				t.Fatalf("waiver replay moved funds twice: %v", e)
			}
		})
	}
}
