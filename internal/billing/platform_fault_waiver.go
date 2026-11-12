package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"reflect"
	"strings"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

// PlatformFaultWaiverApproval is an explicit customer obligation waiver, never a
// statement that provider execution failed or supplier cost was zero.
type PlatformFaultWaiverApproval struct {
	ApprovalStatement      string          `json:"approval_statement"`
	ApprovalSourceSHA256   string          `json:"approval_source_sha256"`
	Schema                 int             `json:"schema"`
	CaseID                 string          `json:"case_id"`
	OperatorID             string          `json:"operator_id"`
	Reason                 string          `json:"reason"`
	CallID                 string          `json:"call_id"`
	ActorID                string          `json:"actor_id"`
	AccountID              string          `json:"account_id"`
	OriginProductID        string          `json:"origin_product_id"`
	InstanceID             string          `json:"instance_id"`
	Asset                  string          `json:"asset"`
	Held                   string          `json:"held"`
	ProviderState          string          `json:"provider_state"`
	OriginalEvidenceSHA256 string          `json:"original_evidence_sha256"`
	FollowupEvidenceSHA256 string          `json:"followup_evidence_sha256"`
	ExpectedCentralRow     json.RawMessage `json:"expected_central_row"`
	GatewayCall            json.RawMessage `json:"gateway_call"`
	GatewayAttempts        json.RawMessage `json:"gateway_attempts"`
}

// ParsePlatformFaultWaiverApproval binds the exact private approval bytes to the
// independently supplied digest. Duplicate keys are rejected at every depth.
func ParsePlatformFaultWaiverApproval(raw []byte, expectedSHA string) (PlatformFaultWaiverApproval, error) {
	var a PlatformFaultWaiverApproval
	sum := sha256.Sum256(raw)
	if len(raw) == 0 || len(raw) > 4<<20 || hex.EncodeToString(sum[:]) != expectedSHA {
		return a, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := waiverUniqueValue(d); err != nil {
		return a, ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return a, ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return a, ErrInvalid
	}
	n, err := integer(a.Held, true)
	if err != nil || str(n) != a.Held || a.Schema != 1 || strings.TrimSpace(a.ApprovalStatement) == "" || len(a.ApprovalStatement) > 16000 || !keyOK(a.CaseID) || !keyOK(a.CallID) || !keyOK(a.OperatorID) || !keyOK(a.ActorID) || !keyOK(a.AccountID) || strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 2000 || (a.OriginProductID != "llmrouter" && a.OriginProductID != "superwagie") || a.InstanceID == "" || (a.Asset != CashAsset && a.Asset != CreditAsset) || a.ProviderState != "PROVIDER_UNRESOLVED" {
		return a, ErrInvalid
	}
	for _, v := range []string{a.ApprovalSourceSHA256, a.OriginalEvidenceSHA256, a.FollowupEvidenceSHA256} {
		b, e := hex.DecodeString(v)
		if e != nil || len(b) != 32 || v != strings.ToLower(v) {
			return a, ErrInvalid
		}
	}
	var central map[string]json.RawMessage
	if json.Unmarshal(a.ExpectedCentralRow, &central) != nil || len(central) == 0 {
		return a, ErrInvalid
	}
	var g map[string]json.RawMessage
	if json.Unmarshal(a.GatewayCall, &g) != nil {
		return a, ErrInvalid
	}
	wantMode := FundingPrepaidCash
	if a.Asset == CreditAsset {
		wantMode = FundingCredits
	}
	required := map[string]string{"CallID": a.CallID, "ActorID": a.ActorID, "AccountID": a.AccountID, "OriginProductID": a.OriginProductID, "InstanceID": a.InstanceID, "Asset": a.Asset, "FundingMode": wantMode, "FundingProductID": "llmrouter", "Amount": a.Held, "Status": "UNKNOWN", "Evidence": "", "Intent": ""}
	for k, w := range required {
		var v string
		if json.Unmarshal(g[k], &v) != nil || v != w {
			return a, ErrInvalid
		}
	}
	var charged string
	if json.Unmarshal(g["Charged"], &charged) != nil || (charged != "" && charged != "0") {
		return a, ErrInvalid
	}
	var conflict bool
	if json.Unmarshal(g["EvidenceConflict"], &conflict) != nil || conflict {
		return a, ErrInvalid
	}
	var attempts []struct {
		ID        int64
		CallID    string
		ChannelID int64
		Status    string
	}
	// DISPATCHED means the request left for the supplier; UNKNOWN means it left and
	// the answer was lost. Both are exactly the unresolved sends a waiver exists for.
	if json.Unmarshal(a.GatewayAttempts, &attempts) != nil || len(attempts) != 1 || attempts[0].ID <= 0 || attempts[0].ChannelID <= 0 || attempts[0].CallID != a.CallID || (attempts[0].Status != "DISPATCHED" && attempts[0].Status != "UNKNOWN") {
		return a, ErrInvalid
	}
	return a, nil
}

func waiverUniqueValue(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		keys := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := key.(string)
			if !ok || keys[s] {
				return ErrInvalid
			}
			keys[s] = true
			if e = waiverUniqueValue(d); e != nil {
				return e
			}
		}
	} else if delim == '[' {
		for d.More() {
			if e = waiverUniqueValue(d); e != nil {
				return e
			}
		}
	} else {
		return ErrInvalid
	}
	_, e = d.Token()
	return e
}

// AdminPlatformFaultWaiver accepts one immutable operator case. It creates the
// normal release obligation atomically with the evidence and reuses recovery.
func (s *Service) AdminPlatformFaultWaiver(ctx context.Context, authority AdminAuthority, actor string, raw []byte, approvalSHA string) (Call, error) {
	return s.adminPlatformFaultWaiver(ctx, authority, actor, raw, approvalSHA, true)
}

// PreviewAdminPlatformFaultWaiver checks current authority, exact snapshots and
// the original hold without persisting a command or decision.
func (s *Service) PreviewAdminPlatformFaultWaiver(ctx context.Context, authority AdminAuthority, actor string, raw []byte, approvalSHA string) (Call, error) {
	return s.adminPlatformFaultWaiver(ctx, authority, actor, raw, approvalSHA, false)
}
func (s *Service) adminPlatformFaultWaiver(ctx context.Context, authority AdminAuthority, actor string, raw []byte, approvalSHA string, apply bool) (out Call, err error) {
	a, err := ParsePlatformFaultWaiverApproval(raw, approvalSHA)
	if err != nil {
		return out, err
	}
	if authority == nil || actor != a.OperatorID {
		return out, accounts.ErrForbidden
	}
	if s.ledger == nil || s.pool == nil {
		return out, ErrUnsupported
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer database.Rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`); err != nil {
		return out, err
	}
	if err = authority.RequireAdminOn(ctx, tx, actor, "finance:write"); err != nil {
		return out, err
	}
	if err = database.LockAccountTx(ctx, tx, a.AccountID); err != nil {
		return out, err
	}
	var oldCase, oldSHA, commandID string
	err = tx.QueryRow(ctx, `SELECT case_id,approval_sha256,release_command_id FROM billing_platform_fault_waivers WHERE case_id=$1 OR call_id=$2`, a.CaseID, a.CallID).Scan(&oldCase, &oldSHA, &commandID)
	if err == nil {
		if oldCase != a.CaseID || oldSHA != approvalSHA {
			return out, ErrConflict
		}
		if !apply {
			return load[Call](ctx, tx, `SELECT snapshot FROM billing_calls WHERE id=$1`, a.CallID)
		}
		if err = tx.Commit(ctx); err != nil {
			return out, err
		}
		err = s.locked(ctx, a.AccountID, func(c *pgxpool.Conn) error { return s.runLocked(ctx, c, commandID) })
		saved, e := load[Call](ctx, s.pool, `SELECT snapshot FROM billing_calls WHERE id=$1`, a.CallID)
		if e != nil {
			return out, e
		}
		return saved, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var snapshot, original []byte
	var equal bool
	err = tx.QueryRow(ctx, `SELECT snapshot,to_jsonb(c),to_jsonb(c)=$2::jsonb FROM billing_calls c WHERE id=$1 FOR UPDATE`, a.CallID, a.ExpectedCentralRow).Scan(&snapshot, &original, &equal)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, accounts.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if !equal {
		return out, ErrConflict
	}
	if json.Unmarshal(snapshot, &out) != nil {
		return out, ErrInvalid
	}
	// Cash and credits hold their funds in different ledgers, so the row must
	// match the approved asset exactly rather than assume cash.
	wantMode := FundingPrepaidCash
	if a.Asset == CreditAsset {
		wantMode = FundingCredits
	}
	if out.CallID != a.CallID || out.ActorID != a.ActorID || out.AccountID != a.AccountID || out.ServiceID != "llmrouter" || out.OriginProductID != a.OriginProductID || out.InstanceID != a.InstanceID || out.ChargeAsset != a.Asset || out.Rate.Asset != a.Asset || out.FundingMode != wantMode || out.FundingProductID != "llmrouter" || out.Held != a.Held || out.Charged != "0" || out.Status != "RESERVED" || out.Usage != nil || out.ProviderCost != "" || out.FinalDigest != "" {
		return out, ErrConflict
	}
	var gateway struct {
		Digest, CredentialID, TaskID, WireOperation, Model, RateID, RateSnapshot string
		PricingSource, NativeQuoteJSON, NativePriceSnapshot                      string
		BillingProfileVersion, CredentialVersion                                 int64
	}
	if json.Unmarshal(a.GatewayCall, &gateway) != nil || gateway.Digest != out.RequestDigest || gateway.CredentialID != out.CredentialID || gateway.TaskID != out.TaskID || gateway.WireOperation != out.WireOperation || gateway.Model != out.Rate.Model || gateway.RateID != out.Rate.ID || gateway.BillingProfileVersion != out.BillingProfileVersion {
		return out, ErrConflict
	}
	if out.NativePricing != nil {
		var quote NativeQuote
		// NativeQuoteJSON is a JSON string inside the approval. The outer
		// duplicate-key guard cannot inspect it, so validate its own shape.
		decoder := json.NewDecoder(strings.NewReader(gateway.NativeQuoteJSON))
		decoder.UseNumber()
		if waiverUniqueValue(decoder) != nil {
			return out, ErrConflict
		}
		if _, err := decoder.Token(); err != io.EOF {
			return out, ErrConflict
		}
		decoder = json.NewDecoder(strings.NewReader(gateway.NativeQuoteJSON))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&quote) != nil {
			return out, ErrConflict
		}
		priceDigest := sha256.Sum256([]byte(gateway.NativePriceSnapshot))
		// Credit-priced managed calls record the credit pricing source; both are
		// the same native quote contract.
		if (gateway.PricingSource != "llmrouter_native" && gateway.PricingSource != "llmrouter_native_credit") || gateway.RateSnapshot != "" || gateway.CredentialVersion != out.CredentialVersion || gateway.CredentialVersion <= 0 || gateway.NativePriceSnapshot == "" || !reflect.DeepEqual(quote, *out.NativePricing) || validateNativeQuote(quote, out.WireOperation) != nil || quote.PricingDigest != hex.EncodeToString(priceDigest[:]) || quote.MaxAmount != out.Held {
			return out, ErrConflict
		}
	} else {
		var gatewayRate Rate
		if gateway.PricingSource == "llmrouter_native" || gateway.PricingSource == "llmrouter_native_credit" || gateway.NativeQuoteJSON != "" || gateway.NativePriceSnapshot != "" || json.Unmarshal([]byte(gateway.RateSnapshot), &gatewayRate) != nil || !reflect.DeepEqual(gatewayRate, out.Rate) {
			return out, ErrConflict
		}
	}
	// clear accepts the same account connection through the caller transaction.
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_commands WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN')) OR EXISTS(SELECT 1 FROM wallet_operations WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN')) OR EXISTS(SELECT 1 FROM billing_orders WHERE account_id=$1 AND ((snapshot->>'payment_status'='PAID' AND snapshot->>'fulfillment_status'='PENDING' AND COALESCE(snapshot->>'refund_status','') IN ('','REJECTED')) OR snapshot->>'refund_status'='FROZEN'))`, a.AccountID).Scan(&pending); err != nil {
		return out, err
	}
	if pending {
		return out, ErrPending
	}
	n, _ := integer(a.Held, true)
	// The hold lives in the cash ledger or in batch addresses, never both.
	holdBalance := func(asset string) (int64, error) {
		if asset == CreditAsset {
			total := int64(0)
			for _, alloc := range out.Allocations {
				amount, e := integer(alloc.Amount, false)
				if e != nil {
					return 0, e
				}
				balance, e := s.ledger.Balance(ctx, BatchAddress(alloc.BatchID, "reserved"), CreditAsset)
				if e != nil {
					return 0, e
				}
				if balance != amount {
					return 0, ErrConflict
				}
				total += amount
			}
			return total, nil
		}
		return s.ledger.Balance(ctx, cashHoldForCall(out), CashAsset)
	}
	balance, e := holdBalance(a.Asset)
	if e != nil {
		return out, e
	}
	if balance != n {
		return out, ErrConflict
	}
	var commands []byte
	if err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(bc) ORDER BY created_at,id),'[]'::jsonb) FROM billing_commands bc WHERE account_id=$1 AND effect#>>'{call,call_id}'=$2`, a.AccountID, a.CallID).Scan(&commands); err != nil {
		return out, err
	}
	var reserve command
	var reserveID, reserveLedgerID string
	if err = tx.QueryRow(ctx, `SELECT id,COALESCE(ledger_transaction_id,'') FROM billing_commands WHERE account_id=$1 AND kind='reserve' AND request_key=$2 AND status='APPLIED'`, a.AccountID, a.CallID).Scan(&reserveID, &reserveLedgerID); err != nil {
		return out, ErrConflict
	}
	reserve, err = readCommand(ctx, tx, reserveID)
	if err != nil {
		return out, err
	}
	originalTx, e := s.ledger.Find(ctx, reserve.Ledger.Reference)
	if e != nil {
		return out, e
	}
	if originalTx == nil || originalTx.Reference != reserve.Ledger.Reference || originalTx.ID == "" || originalTx.ID != reserveLedgerID || !reflect.DeepEqual(originalTx.Postings, reserve.Ledger.Postings) {
		return out, ErrConflict
	}
	if !waiverHoldPostsMatch(out, a, reserve.Ledger.Postings, n) {
		return out, ErrConflict
	}
	if !apply {
		return out, nil
	}
	// A current ledger hold and the exact approved database row are independent checks.
	posts, e := waiverReleasePosts(out, a)
	if e != nil {
		return out, e
	}
	out.Status = "RELEASED"
	out.Charged = "0"
	out.FinalDigest = hash([]string{"release", "platform_fault_waiver:" + approvalSHA})
	cmd, e := s.createCommand(ctx, tx, out.AccountID, out.ActorID, "release", out.CallID, posts, effect{Call: &out})
	if e != nil {
		return out, e
	}
	commandID = cmd.ID
	_, err = tx.Exec(ctx, `INSERT INTO billing_platform_fault_waivers(case_id,call_id,account_id,operator_id,approval_sha256,approval_raw,original_central_row,original_commands,release_command_id,provider_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'PROVIDER_UNRESOLVED')`, a.CaseID, a.CallID, a.AccountID, actor, approvalSHA, raw, original, commands, commandID)
	if err != nil {
		return out, err
	}
	if err = authority.WriteAudit(ctx, tx, actor, "finance.platform_fault_waiver", a.CallID, a.Reason, map[string]any{"case_id": a.CaseID, "approval_sha256": approvalSHA, "release_command_id": commandID, "original_central_row": json.RawMessage(original), "original_commands": json.RawMessage(commands), "provider_state": a.ProviderState, "held": a.Held, "asset": a.Asset}); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	err = s.locked(ctx, a.AccountID, func(c *pgxpool.Conn) error { return s.runLocked(ctx, c, commandID) })
	saved, e := load[Call](ctx, s.pool, `SELECT snapshot FROM billing_calls WHERE id=$1`, a.CallID)
	if e != nil {
		return out, e
	}
	return saved, err
}

// waiverHoldPostsMatch proves the recorded reserve moved exactly the approved
// amount out of the payer, in the asset the approval names. Cash holds sit in a
// per-call address; credits sit in each batch's reserved bucket.
func waiverHoldPostsMatch(call Call, a PlatformFaultWaiverApproval, posts []ledger.Posting, held int64) bool {
	if a.Asset == CreditAsset {
		if len(call.Allocations) == 0 || len(posts) != len(call.Allocations) {
			return false
		}
		total := int64(0)
		for _, p := range posts {
			matched := false
			for _, alloc := range call.Allocations {
				amount, e := integer(alloc.Amount, false)
				if e != nil {
					return false
				}
				if p.Source == BatchAddress(alloc.BatchID, "available") && p.Destination == BatchAddress(alloc.BatchID, "reserved") && p.Amount == amount {
					matched = true
					total += amount
					break
				}
			}
			if !matched || p.Asset != CreditAsset {
				return false
			}
		}
		return total == held
	}
	return len(posts) == 1 && posts[0].Source == wallet.Address(a.AccountID, "llmrouter") && posts[0].Destination == cashHoldForCall(call) && posts[0].Asset == CashAsset && posts[0].Amount == held
}

// waiverReleasePosts returns the postings that give the whole hold back. The
// customer owes nothing for a call whose provider outcome cannot be resolved.
func waiverReleasePosts(call Call, a PlatformFaultWaiverApproval) ([]ledger.Posting, error) {
	if a.Asset == CreditAsset {
		posts := []ledger.Posting{}
		for _, alloc := range call.Allocations {
			amount, e := integer(alloc.Amount, false)
			if e != nil {
				return nil, e
			}
			posts = append(posts, ledger.Posting{Source: BatchAddress(alloc.BatchID, "reserved"), Destination: BatchAddress(alloc.BatchID, "available"), Asset: CreditAsset, Amount: amount})
		}
		if len(posts) == 0 {
			return nil, ErrConflict
		}
		return posts, nil
	}
	return cashFinalPosts(call, 0)
}
