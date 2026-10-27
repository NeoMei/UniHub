package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"strconv"
	"strings"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/productusers"
)

const RuntimeAsset = "LR_QUOTA/0"
const RuntimeUnitVersion = "lr-quota-v1"

type RuntimeScope struct {
	ProductID  string `json:"product_id"`
	InstanceID string `json:"instance_id"`
}
type RuntimeReserveRequest struct {
	CallID        string `json:"call_id"`
	LocalUserID   string `json:"local_user_id"`
	LocalKeyID    string `json:"local_key_id"`
	AccountID     string `json:"account_id"`
	RequestDigest string `json:"request_digest"`
	Model         string `json:"model"`
	Operation     string `json:"operation"`
	Asset         string `json:"asset"`
	UnitVersion   string `json:"unit_version"`
	PriceVersion  string `json:"price_version"`
	PricingDigest string `json:"pricing_digest"`
	Amount        string `json:"amount"`
	ParentCallID  string `json:"parent_call_id,omitempty"`
}
type RuntimeSettlementRequest struct {
	Amount        string          `json:"amount"`
	PricingDigest string          `json:"pricing_digest"`
	Usage         json.RawMessage `json:"usage"`
}
type RuntimeTopUpRequest struct {
	RequestKey   string `json:"request_key"`
	Amount       string `json:"amount"`
	AcceptedCall bool   `json:"accepted_call,omitempty"`
}
type RuntimeReleaseRequest struct {
	Reason string `json:"reason"`
}
type RuntimeCall struct {
	RuntimeReserveRequest
	RuntimeScope
	ID          string          `json:"id"`
	ActorID     string          `json:"actor_id"`
	Held        string          `json:"held"`
	Charged     string          `json:"charged"`
	Refunded    string          `json:"refunded,omitempty"`
	Status      string          `json:"status"`
	FinalDigest string          `json:"final_digest,omitempty"`
	Usage       json.RawMessage `json:"usage,omitempty"`
	Attempt     int             `json:"attempt,omitempty"`
}
type RuntimeBalance struct {
	RuntimeScope
	AccountID   string `json:"account_id"`
	Asset       string `json:"asset"`
	UnitVersion string `json:"unit_version"`
	Available   string `json:"available"`
	Held        string `json:"held"`
	Consumed    string `json:"consumed"`
}
type RuntimeAdjustmentRequest struct {
	RequestKey        string  `json:"request_key"`
	LocalUserID       string  `json:"local_user_id"`
	AccountID         string  `json:"account_id"`
	Amount            string  `json:"amount"`
	Reason            string  `json:"reason"`
	SetAvailable      *string `json:"set_available,omitempty"`
	ExpectedAvailable string  `json:"expected_available,omitempty"`
	Asset             string  `json:"asset"`
	UnitVersion       string  `json:"unit_version"`
}
type RuntimeImportRequest struct {
	BatchID        string          `json:"batch_id"`
	LegacyUserID   string          `json:"legacy_user_id"`
	SnapshotDigest string          `json:"snapshot_digest"`
	LocalUserID    string          `json:"local_user_id"`
	AccountID      string          `json:"account_id"`
	Amount         string          `json:"amount"`
	Asset          string          `json:"asset"`
	UnitVersion    string          `json:"unit_version"`
	SourceUnit     string          `json:"source_unit"`
	Conversion     string          `json:"conversion"`
	SourceEvidence json.RawMessage `json:"source_evidence,omitempty"`
}
type RuntimeAwardRequest struct {
	ReceiptID     string          `json:"receipt_id"`
	SourceKind    string          `json:"source_kind"`
	ReceiptDigest string          `json:"receipt_digest"`
	Reason        string          `json:"reason"`
	LocalUserID   string          `json:"local_user_id"`
	AccountID     string          `json:"account_id"`
	Amount        string          `json:"amount"`
	Asset         string          `json:"asset"`
	UnitVersion   string          `json:"unit_version"`
	SourcePayload json.RawMessage `json:"source_payload,omitempty"`
}

// RuntimeAward is called only by a receipt-generating trusted service operation.
// It never accepts an arbitrary central subject, role or spendable cash asset.
func (s *Service) RuntimeAward(ctx context.Context, scope RuntimeScope, r RuntimeAwardRequest) (RuntimeAdjustment, error) {
	n, e := runtimeAmount(r.Amount)
	if e != nil || !runtimeUnits(r.Asset, r.UnitVersion) || !runtimeText(r.ReceiptID) || !runtimeText(r.ReceiptDigest) || !runtimeText(r.Reason) {
		return RuntimeAdjustment{}, ErrInvalid
	}
	switch r.SourceKind {
	case "payment", "redemption", "checkin", "referral", "signup":
	default:
		return RuntimeAdjustment{}, ErrInvalid
	}
	if len(r.SourcePayload) > 64<<10 || (len(r.SourcePayload) > 0 && !json.Valid(r.SourcePayload)) {
		return RuntimeAdjustment{}, ErrInvalid
	}
	return s.runtimeAdjustment(ctx, scope, "", r.LocalUserID, r.AccountID, r.SourceKind, r.ReceiptID, r.Amount, hash(r), n, nil, "", r)
}

type RuntimeAdjustment struct {
	RuntimeScope
	ID            string          `json:"id"`
	AccountID     string          `json:"account_id"`
	Amount        string          `json:"amount"`
	Status        string          `json:"status"`
	RequestDigest string          `json:"request_digest"`
	Source        json.RawMessage `json:"source,omitempty"`
}

type RuntimeBudgetRequest struct {
	LocalUserID string `json:"local_user_id"`
	AccountID   string `json:"account_id"`
	Version     int64  `json:"version"`
	MaxTotal    string `json:"max_total"`
	MaxPerCall  string `json:"max_per_call"`
}

func (s *Service) SetRuntimeBudget(ctx context.Context, scope RuntimeScope, actor string, r RuntimeBudgetRequest) (out UsageBudget, e error) {
	if !validRuntimeScope(scope) || r.Version < 0 {
		return out, ErrInvalid
	}
	if e = ValidateUsageLimits(r.MaxTotal, r.MaxPerCall); e != nil {
		return
	}
	e = s.locked(ctx, r.AccountID, func(c *pgxpool.Conn) error {
		if err := s.finance(ctx, c, actor, r.AccountID); err != nil {
			return err
		}
		if _, err := s.runtimeAuthority(ctx, c, scope, r.LocalUserID, r.AccountID); err != nil {
			return err
		}
		var current int64
		err := c.QueryRow(ctx, `SELECT version FROM usage_budgets WHERE account_id=$1 AND origin_product_id=$2 AND instance_id=$3 AND asset=$4`, r.AccountID, scope.ProductID, scope.InstanceID, RuntimeAsset).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current != r.Version {
			return ErrConflict
		}
		out = UsageBudget{AccountID: r.AccountID, OriginProductID: scope.ProductID, InstanceID: scope.InstanceID, Asset: RuntimeAsset, FundingMode: "product_quota", Version: current + 1, MaxTotal: r.MaxTotal, MaxPerCall: r.MaxPerCall}
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer database.Rollback(tx)
		if _, err = tx.Exec(ctx, `INSERT INTO usage_budgets(account_id,origin_product_id,instance_id,asset,version,max_total,max_per_call,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id,origin_product_id,instance_id,asset) DO UPDATE SET version=excluded.version,max_total=excluded.max_total,max_per_call=excluded.max_per_call,updated_by=excluded.updated_by,updated_at=now()`, r.AccountID, scope.ProductID, scope.InstanceID, RuntimeAsset, out.Version, r.MaxTotal, r.MaxPerCall, actor); err != nil {
			return err
		}
		if err = event(ctx, tx, r.AccountID, actor, scope.ProductID, "runtime_budget.updated", out); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return
}

func (s *Service) GetRuntimeBudget(ctx context.Context, scope RuntimeScope, local, account string) (out UsageBudget, e error) {
	if !validRuntimeScope(scope) || s.productUsers == nil {
		return out, accounts.ErrForbidden
	}
	b, e := s.productUsers.RuntimeBinding(ctx, scope.ProductID, scope.InstanceID, local)
	if e != nil {
		return out, e
	}
	if e = s.member(ctx, s.pool, b.UserID, account); e != nil {
		return out, e
	}
	out = UsageBudget{AccountID: account, OriginProductID: scope.ProductID, InstanceID: scope.InstanceID, Asset: RuntimeAsset, FundingMode: "product_quota", Held: "0", Used: "0"}
	e = s.pool.QueryRow(ctx, `SELECT version,max_total,max_per_call FROM usage_budgets WHERE account_id=$1 AND origin_product_id=$2 AND instance_id=$3 AND asset=$4`, account, scope.ProductID, scope.InstanceID, RuntimeAsset).Scan(&out.Version, &out.MaxTotal, &out.MaxPerCall)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	e = s.pool.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN snapshot->>'status' IN ('PENDING','RESERVED') THEN (snapshot->>'held')::numeric ELSE 0 END),0)::text,COALESCE(sum(CASE WHEN snapshot->>'status'='SETTLED' THEN (snapshot->>'charged')::numeric-COALESCE((snapshot->>'refunded')::numeric,0) ELSE 0 END),0)::text FROM product_runtime_calls WHERE account_id=$1 AND snapshot->>'product_id'=$2 AND snapshot->>'instance_id'=$3`, account, scope.ProductID, scope.InstanceID).Scan(&out.Held, &out.Used)
	out.Remaining = UsageRemaining(out.MaxTotal, out.Held, out.Used)
	return
}

func runtimeID(scope RuntimeScope, kind, key string) string {
	b, _ := json.Marshal([]string{scope.ProductID, scope.InstanceID, kind, key})
	sum := sha256.Sum256(b)
	return "rt_" + hex.EncodeToString(sum[:])
}

// RuntimeAddress is deliberately disjoint from cash and shared-credit wallets.
// The same final ledger is authoritative; SQL stores receipts, never balances.
func RuntimeAddress(scope RuntimeScope, account, bucket string) string {
	return "productruntime:" + runtimeID(scope, "account", account) + ":" + bucket
}
func validRuntimeScope(scope RuntimeScope) bool {
	return scope.ProductID == "llmrouter" && usageInstanceID.MatchString(scope.InstanceID)
}
func runtimeUnits(asset, version string) bool {
	return asset == RuntimeAsset && version == RuntimeUnitVersion
}
func runtimeText(v string) bool {
	return v != "" && len(v) <= 512 && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "\x00\r\n")
}
func runtimeAmount(v string) (int64, error) {
	if v == "0" {
		return 0, nil
	}
	if !exactLimit.MatchString(v) {
		return 0, ErrInvalid
	}
	return integer(v, false)
}
func (s *Service) runtimeAuthority(ctx context.Context, q accounts.Querier, scope RuntimeScope, local, account string) (productusers.RuntimeBinding, error) {
	if !validRuntimeScope(scope) || s.productUsers == nil || !s.productUsers.Managed(scope.ProductID, scope.InstanceID) {
		return productusers.RuntimeBinding{}, accounts.ErrForbidden
	}
	b, e := s.productUsers.RuntimeBindingOn(ctx, q, scope.ProductID, scope.InstanceID, local)
	if e != nil {
		return b, e
	}
	if e = s.productUsers.RequirePermissionOn(ctx, q, b.UserID, scope.ProductID, scope.InstanceID, "product:use"); e != nil {
		return b, e
	}
	if account != "" {
		if e = s.member(ctx, q, b.UserID, account); e != nil {
			return b, e
		}
	}
	return b, nil
}
func saveRuntimeCall(ctx context.Context, tx pgx.Tx, call RuntimeCall) error {
	_, e := tx.Exec(ctx, `UPDATE product_runtime_calls SET snapshot=$2 WHERE id=$1`, call.ID, encode(call))
	return e
}
func saveRuntimeAdjustment(ctx context.Context, tx pgx.Tx, a RuntimeAdjustment) error {
	_, e := tx.Exec(ctx, `UPDATE product_runtime_adjustments SET snapshot=$2 WHERE id=$1`, a.ID, encode(a))
	return e
}
func (s *Service) RuntimeBalance(ctx context.Context, scope RuntimeScope, local, account string) (out RuntimeBalance, e error) {
	if !validRuntimeScope(scope) || s.productUsers == nil {
		return out, accounts.ErrForbidden
	}
	b, e := s.productUsers.RuntimeBinding(ctx, scope.ProductID, scope.InstanceID, local)
	if e != nil {
		return out, e
	}
	if e = s.member(ctx, s.pool, b.UserID, account); e != nil {
		return out, e
	}
	out = RuntimeBalance{RuntimeScope: scope, AccountID: account, Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}
	values := []*string{&out.Available, &out.Held, &out.Consumed}
	for i, bucket := range []string{"available", "reserved", "consumed"} {
		n, err := s.ledger.Balance(ctx, RuntimeAddress(scope, account, bucket), RuntimeAsset)
		if err != nil {
			return out, err
		}
		*values[i] = str(n)
	}
	return
}
func (s *Service) runtimeCallOn(ctx context.Context, q accounts.Querier, scope RuntimeScope, callID string) (RuntimeCall, error) {
	return load[RuntimeCall](ctx, q, `SELECT snapshot FROM product_runtime_calls WHERE id=$1`, runtimeID(scope, "call", callID))
}
func (s *Service) RuntimeGetCall(ctx context.Context, scope RuntimeScope, callID string) (out RuntimeCall, e error) {
	if !validRuntimeScope(scope) {
		return out, accounts.ErrForbidden
	}
	out, e = s.runtimeCallOn(ctx, s.pool, scope, callID)
	if e != nil {
		return
	}
	e = s.locked(ctx, out.AccountID, func(c *pgxpool.Conn) error {
		if e := s.runtimeResume(ctx, c, out.ID); e != nil {
			return e
		}
		var e error
		out, e = s.runtimeCallOn(ctx, c, scope, callID)
		return e
	})
	return
}
func (s *Service) runtimeResume(ctx context.Context, c *pgxpool.Conn, entity string) error {
	rows, e := c.Query(ctx, `SELECT id FROM billing_commands WHERE effect->'runtime_call'->>'id'=$1 OR effect->'runtime_adjustment'->>'id'=$1 ORDER BY created_at,id`, entity)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.runLocked(ctx, c, id); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) runtimeCheckBudget(ctx context.Context, q accounts.Querier, scope RuntimeScope, account string, additional, total int64) error {
	var maxTotal, maxCall string
	e := q.QueryRow(ctx, `SELECT max_total,max_per_call FROM usage_budgets WHERE account_id=$1 AND origin_product_id=$2 AND instance_id=$3 AND asset=$4`, account, scope.ProductID, scope.InstanceID, RuntimeAsset).Scan(&maxTotal, &maxCall)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var held, used string
	e = q.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN snapshot->>'status' IN ('PENDING','RESERVED') THEN (snapshot->>'held')::numeric ELSE 0 END),0)::text,COALESCE(sum(CASE WHEN snapshot->>'status'='SETTLED' THEN (snapshot->>'charged')::numeric-COALESCE((snapshot->>'refunded')::numeric,0) ELSE 0 END),0)::text FROM product_runtime_calls WHERE account_id=$1 AND snapshot->>'product_id'=$2 AND snapshot->>'instance_id'=$3`, account, scope.ProductID, scope.InstanceID).Scan(&held, &used)
	if e != nil {
		return e
	}
	return enforceUsageLimit(maxTotal, maxCall, held, used, additional, total)
}
func (s *Service) RuntimeReserve(ctx context.Context, scope RuntimeScope, r RuntimeReserveRequest) (out RuntimeCall, e error) {
	n, e := runtimeAmount(r.Amount)
	if e != nil || !validRuntimeScope(scope) || !runtimeUnits(r.Asset, r.UnitVersion) {
		return out, ErrInvalid
	}
	for _, v := range []string{r.CallID, r.LocalUserID, r.AccountID, r.RequestDigest, r.Model, r.Operation, r.PriceVersion, r.PricingDigest} {
		if !runtimeText(v) {
			return out, ErrInvalid
		}
	}
	id := runtimeID(scope, "call", r.CallID)
	digest := hash(struct {
		Scope   RuntimeScope
		Request RuntimeReserveRequest
	}{scope, r})
	e = s.locked(ctx, r.AccountID, func(c *pgxpool.Conn) error {
		old, err := s.runtimeCallOn(ctx, c, scope, r.CallID)
		retryAttempt := 0
		if err == nil {
			var d string
			if err = c.QueryRow(ctx, `SELECT request_digest FROM product_runtime_calls WHERE id=$1`, id).Scan(&d); err != nil {
				return err
			}
			if d != digest {
				return ErrConflict
			}
			if err = s.runtimeResume(ctx, c, id); err != nil {
				return err
			}
			out, err = s.runtimeCallOn(ctx, c, scope, r.CallID)
			if err != nil {
				return err
			}
			if r.ParentCallID == "" || out.Status != "REJECTED" {
				return nil
			}
			if err = s.runtimeRetryableParentCharge(ctx, c, out); err != nil {
				return err
			}
			retryAttempt = old.Attempt + 1
		}
		if err != nil && !errors.Is(err, accounts.ErrNotFound) {
			return err
		}
		if err = s.clear(ctx, c, r.AccountID); err != nil {
			return err
		}
		var actor string
		if r.ParentCallID != "" {
			parent, err := s.runtimeAcceptedParent(ctx, c, scope, r)
			if err != nil {
				return err
			}
			actor = parent.ActorID
		} else {
			b, err := s.runtimeAuthority(ctx, c, scope, r.LocalUserID, r.AccountID)
			if err != nil {
				return err
			}
			actor = b.UserID
			if err = s.runtimeCheckBudget(ctx, c, scope, r.AccountID, n, n); err != nil {
				return err
			}
		}
		balance, err := s.ledger.Balance(ctx, RuntimeAddress(scope, r.AccountID, "available"), RuntimeAsset)
		if err != nil {
			return err
		}
		if n > balance {
			return ledger.ErrInsufficientFunds
		}
		out = RuntimeCall{Attempt: retryAttempt, RuntimeReserveRequest: r, RuntimeScope: scope, ID: id, ActorID: actor, Held: r.Amount, Charged: "0", Status: "PENDING"}
		applied := out
		applied.Status = "RESERVED"
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer database.Rollback(tx)
		if retryAttempt > 0 {
			tag, e := tx.Exec(ctx, `UPDATE product_runtime_calls SET snapshot=$2 WHERE id=$1 AND snapshot->>'status'='REJECTED'`, id, encode(out))
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		} else if _, err = tx.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,$3,$4)`, id, r.AccountID, digest, encode(out)); err != nil {
			return err
		}
		posts := []ledger.Posting{}
		if n > 0 {
			posts = append(posts, ledger.Posting{Source: RuntimeAddress(scope, r.AccountID, "available"), Destination: RuntimeAddress(scope, r.AccountID, "reserved"), Asset: RuntimeAsset, Amount: n})
		}
		commandKey := id
		if retryAttempt > 0 {
			commandKey = id + ":retry:" + strconv.Itoa(retryAttempt)
		}
		cmd, err := s.createCommand(ctx, tx, r.AccountID, actor, "runtime_reserve", commandKey, posts, effect{RuntimeCall: &applied})
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		if err = s.runLocked(ctx, c, cmd.ID); err != nil {
			return err
		}
		out, err = s.runtimeCallOn(ctx, c, scope, r.CallID)
		return err
	})
	return
}
func (s *Service) RuntimeTopUp(ctx context.Context, scope RuntimeScope, callID string, r RuntimeTopUpRequest) (out RuntimeCall, e error) {
	n, e := runtimeAmount(r.Amount)
	if e != nil || n == 0 || !runtimeText(r.RequestKey) {
		return out, ErrInvalid
	}
	out, e = s.runtimeCallOn(ctx, s.pool, scope, callID)
	if e != nil {
		return
	}
	requestDigest := hash(r)
	key := runtimeID(scope, "topup", callID+"\x00"+r.RequestKey)
	e = s.locked(ctx, out.AccountID, func(c *pgxpool.Conn) error {
		var cmdID string
		attempt := 0
		commandKey := key
		err := c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='runtime_topup' AND (request_key=$2 OR request_key LIKE $2||':retry:%') ORDER BY COALESCE((effect->>'runtime_attempt')::int,0) DESC LIMIT 1`, out.AccountID, key).Scan(&cmdID)
		if err == nil {
			x, err := readCommand(ctx, c, cmdID)
			if err != nil {
				return err
			}
			if x.Effect.RuntimeRequestDigest != requestDigest {
				return ErrConflict
			}
			if err = s.runLocked(ctx, c, cmdID); err != nil {
				return err
			}
			terminal, err := readCommand(ctx, c, cmdID)
			if err != nil {
				return err
			}
			if terminal.Status == "REJECTED" && r.AcceptedCall {
				if err = s.runtimeRetryableCommand(ctx, c, terminal); err != nil {
					return err
				}
				attempt = terminal.Effect.RuntimeAttempt + 1
				commandKey = key + ":retry:" + strconv.Itoa(attempt)
			} else {
				if terminal.Status == "CANCELED" || terminal.Status == "REJECTED" {
					return accounts.ErrForbidden
				}
				out, err = s.runtimeCallOn(ctx, c, scope, callID)
				return err
			}
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = s.clear(ctx, c, out.AccountID); err != nil {
			return err
		}
		out, err = s.runtimeCallOn(ctx, c, scope, callID)
		if err != nil {
			return err
		}
		if out.Status != "RESERVED" {
			return ErrConflict
		}
		if !r.AcceptedCall {
			if _, err = s.runtimeAuthority(ctx, c, scope, out.LocalUserID, out.AccountID); err != nil {
				return err
			}
		}
		held, _ := runtimeAmount(out.Held)
		if n > math.MaxInt64-held {
			return ErrInvalid
		}
		if !r.AcceptedCall {
			if err = s.runtimeCheckBudget(ctx, c, scope, out.AccountID, n, held+n); err != nil {
				return err
			}
		}
		balance, err := s.ledger.Balance(ctx, RuntimeAddress(scope, out.AccountID, "available"), RuntimeAsset)
		if err != nil {
			return err
		}
		if n > balance {
			return ledger.ErrInsufficientFunds
		}
		out.Held = str(held + n)
		posts := []ledger.Posting{{Source: RuntimeAddress(scope, out.AccountID, "available"), Destination: RuntimeAddress(scope, out.AccountID, "reserved"), Asset: RuntimeAsset, Amount: n}}
		out, err = s.runtimeCallCommand(ctx, c, out, "runtime_topup", commandKey, requestDigest, posts, runtimeCommandOption{Accepted: r.AcceptedCall, Attempt: attempt})
		return err
	})
	return
}

type runtimeCommandOption struct {
	Accepted bool
	Attempt  int
}

func (s *Service) runtimeCallCommand(ctx context.Context, c *pgxpool.Conn, out RuntimeCall, kind, key, digest string, posts []ledger.Posting, options ...runtimeCommandOption) (RuntimeCall, error) {
	tx, e := c.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer database.Rollback(tx)
	option := runtimeCommandOption{}
	if len(options) == 1 {
		option = options[0]
	}
	cmd, e := s.createCommand(ctx, tx, out.AccountID, out.ActorID, kind, key, posts, effect{RuntimeCall: &out, RuntimeRequestDigest: digest, RuntimeAcceptedCall: option.Accepted, RuntimeAttempt: option.Attempt})
	if e != nil {
		return out, e
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	if e = s.runLocked(ctx, c, cmd.ID); e != nil {
		return out, e
	}
	terminal, e := readCommand(ctx, c, cmd.ID)
	if e != nil {
		return out, e
	}
	if terminal.Status == "CANCELED" || terminal.Status == "REJECTED" {
		return out, accounts.ErrForbidden
	}
	return s.runtimeCallOn(ctx, c, out.RuntimeScope, out.CallID)
}
func (s *Service) RuntimeSettle(ctx context.Context, scope RuntimeScope, callID string, r RuntimeSettlementRequest) (out RuntimeCall, e error) {
	amount, e := runtimeAmount(r.Amount)
	if e != nil || !json.Valid(r.Usage) || len(r.Usage) > 64<<10 {
		return out, ErrInvalid
	}
	out, e = s.runtimeCallOn(ctx, s.pool, scope, callID)
	if e != nil {
		return
	}
	if r.PricingDigest != out.PricingDigest {
		return out, ErrConflict
	}
	digest := hash(r)
	e = s.locked(ctx, out.AccountID, func(c *pgxpool.Conn) error {
		if err := s.runtimeResume(ctx, c, out.ID); err != nil {
			return err
		}
		var err error
		out, err = s.runtimeCallOn(ctx, c, scope, callID)
		if err != nil {
			return err
		}
		if out.Status == "SETTLED" || out.Status == "RELEASED" {
			if out.FinalDigest != digest {
				return ErrConflict
			}
			return nil
		}
		if out.Status != "RESERVED" {
			return ErrConflict
		}
		if err = s.clear(ctx, c, out.AccountID); err != nil {
			return err
		}
		held, _ := runtimeAmount(out.Held)
		posts := []ledger.Posting{}
		if amount > held {
			return ErrConflict
		} // Caller must explicitly reserve an extension first.
		if amount > 0 {
			posts = append(posts, ledger.Posting{Source: RuntimeAddress(scope, out.AccountID, "reserved"), Destination: RuntimeAddress(scope, out.AccountID, "consumed"), Asset: RuntimeAsset, Amount: amount})
		}
		if held > amount {
			posts = append(posts, ledger.Posting{Source: RuntimeAddress(scope, out.AccountID, "reserved"), Destination: RuntimeAddress(scope, out.AccountID, "available"), Asset: RuntimeAsset, Amount: held - amount})
		}
		out.Status = "SETTLED"
		out.Charged = r.Amount
		out.FinalDigest = digest
		out.Usage = r.Usage
		out, err = s.runtimeCallCommand(ctx, c, out, "runtime_settle", out.ID, digest, posts)
		return err
	})
	return
}
func (s *Service) RuntimeRelease(ctx context.Context, scope RuntimeScope, callID string, r RuntimeReleaseRequest) (out RuntimeCall, e error) {
	if !runtimeText(r.Reason) {
		return out, ErrInvalid
	}
	out, e = s.runtimeCallOn(ctx, s.pool, scope, callID)
	if e != nil {
		return
	}
	digest := hash(r)
	e = s.locked(ctx, out.AccountID, func(c *pgxpool.Conn) error {
		if err := s.runtimeResume(ctx, c, out.ID); err != nil {
			return err
		}
		var err error
		out, err = s.runtimeCallOn(ctx, c, scope, callID)
		if err != nil {
			return err
		}
		if out.Status == "SETTLED" || out.Status == "RELEASED" {
			if out.FinalDigest != digest {
				return ErrConflict
			}
			return nil
		}
		if out.Status != "RESERVED" {
			return ErrConflict
		}
		if err = s.clear(ctx, c, out.AccountID); err != nil {
			return err
		}
		held, _ := runtimeAmount(out.Held)
		posts := []ledger.Posting{}
		if held > 0 {
			posts = append(posts, ledger.Posting{Source: RuntimeAddress(scope, out.AccountID, "reserved"), Destination: RuntimeAddress(scope, out.AccountID, "available"), Asset: RuntimeAsset, Amount: held})
		}
		out.Status = "RELEASED"
		out.Charged = "0"
		out.FinalDigest = digest
		out, err = s.runtimeCallCommand(ctx, c, out, "runtime_release", out.ID, digest, posts)
		return err
	})
	return
}
func (s *Service) RuntimeAdjustCredit(ctx context.Context, scope RuntimeScope, actor string, r RuntimeAdjustmentRequest) (RuntimeAdjustment, error) {
	if !runtimeUnits(r.Asset, r.UnitVersion) || !runtimeText(r.Reason) || !runtimeText(r.RequestKey) {
		return RuntimeAdjustment{}, ErrInvalid
	}
	var n int64
	if r.SetAvailable != nil {
		if r.Amount != "" {
			return RuntimeAdjustment{}, ErrInvalid
		}
		if _, e := runtimeAmount(*r.SetAvailable); e != nil {
			return RuntimeAdjustment{}, e
		}
		if _, e := runtimeAmount(r.ExpectedAvailable); e != nil {
			return RuntimeAdjustment{}, e
		}
	} else {
		var e error
		n, e = strconv.ParseInt(r.Amount, 10, 64)
		if e != nil || n == math.MinInt64 || str(n) != r.Amount || r.ExpectedAvailable != "" {
			return RuntimeAdjustment{}, ErrInvalid
		}
	}
	return s.runtimeAdjustment(ctx, scope, actor, r.LocalUserID, r.AccountID, "gift", r.RequestKey, r.Amount, hash(r), n, r.SetAvailable, r.ExpectedAvailable, r)
}
func (s *Service) RuntimeImportBalance(ctx context.Context, scope RuntimeScope, r RuntimeImportRequest) (RuntimeAdjustment, error) {
	n, e := runtimeAmount(r.Amount)
	if e != nil || !runtimeUnits(r.Asset, r.UnitVersion) || r.SourceUnit != RuntimeUnitVersion || r.Conversion != "identity" || r.LegacyUserID != r.LocalUserID || !runtimeText(r.BatchID) || !runtimeText(r.SnapshotDigest) {
		return RuntimeAdjustment{}, ErrInvalid
	}
	// The source key excludes batch ID: retrying another batch cannot import the
	// same legacy user's remaining balance a second time.
	return s.runtimeAdjustment(ctx, scope, "", r.LocalUserID, r.AccountID, "migration", r.LegacyUserID, r.Amount, hash(r), n, nil, "", r)
}
func (s *Service) runtimeAdjustment(ctx context.Context, scope RuntimeScope, actor, local, account, kind, key, amount, digest string, n int64, setAvailable *string, expectedAvailable string, source any) (out RuntimeAdjustment, e error) {
	if !validRuntimeScope(scope) {
		return out, ErrInvalid
	}
	entity := runtimeID(scope, kind, key)
	e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
		old, err := load[RuntimeAdjustment](ctx, c, `SELECT snapshot FROM product_runtime_adjustments WHERE id=$1`, entity)
		if err == nil {
			if old.RequestDigest != digest {
				return ErrConflict
			}
			if err = s.runtimeResume(ctx, c, entity); err != nil {
				return err
			}
			out, err = load[RuntimeAdjustment](ctx, c, `SELECT snapshot FROM product_runtime_adjustments WHERE id=$1`, entity)
			if err == nil && (out.Status == "CANCELED" || out.Status == "REJECTED") {
				return accounts.ErrForbidden
			}
			return err
		}
		if !errors.Is(err, accounts.ErrNotFound) {
			return err
		}
		if err = s.clear(ctx, c, account); err != nil {
			return err
		}
		if s.productUsers == nil || !s.productUsers.Managed(scope.ProductID, scope.InstanceID) {
			return accounts.ErrForbidden
		}
		b, err := s.productUsers.RuntimeBindingOn(ctx, c, scope.ProductID, scope.InstanceID, local)
		if err != nil {
			return err
		}
		// Gifts/imports retain the old user's asset even while consumption is
		// disabled. Only a later reserve requires current product:use authority.
		if account != b.PersonalAccountID {
			return accounts.ErrForbidden
		}
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer database.Rollback(tx)
		if kind == "gift" {
			if err = s.productUsers.RequireRuntimeCreditOn(ctx, tx, actor, scope.ProductID, scope.InstanceID, account); err != nil {
				return err
			}
		} else {
			actor = b.UserID
		}
		if setAvailable != nil {
			available, err := s.ledger.Balance(ctx, RuntimeAddress(scope, account, "available"), RuntimeAsset)
			if err != nil {
				return err
			}
			if str(available) != expectedAvailable {
				return ErrConflict
			}
			target, _ := runtimeAmount(*setAvailable)
			n = target - available
			amount = str(n)
		}
		posts := []ledger.Posting{}
		if n > 0 {
			available, err := s.ledger.Balance(ctx, RuntimeAddress(scope, account, "available"), RuntimeAsset)
			if err != nil {
				return err
			}
			reserved, err := s.ledger.Balance(ctx, RuntimeAddress(scope, account, "reserved"), RuntimeAsset)
			if err != nil {
				return err
			}
			if n > 2147483646 || available > 2147483646-n || reserved > 2147483646-n-available {
				return ErrConflict
			}
			posts = append(posts, ledger.Posting{Source: "world", Destination: RuntimeAddress(scope, account, "available"), Asset: RuntimeAsset, Amount: n})
		}
		if n < 0 {
			balance, err := s.ledger.Balance(ctx, RuntimeAddress(scope, account, "available"), RuntimeAsset)
			if err != nil {
				return err
			}
			if -n > balance {
				return ledger.ErrInsufficientFunds
			}
			posts = append(posts, ledger.Posting{Source: RuntimeAddress(scope, account, "available"), Destination: RuntimeAddress(scope, account, "withdrawn"), Asset: RuntimeAsset, Amount: -n})
		}
		out = RuntimeAdjustment{RuntimeScope: scope, ID: entity, AccountID: account, Amount: amount, Status: "PENDING", RequestDigest: digest, Source: encode(source)}
		applied := out
		applied.Status = "APPLIED"
		if _, err = tx.Exec(ctx, `INSERT INTO product_runtime_adjustments(id,account_id,product_id,instance_id,source_kind,source_key,request_digest,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, entity, account, scope.ProductID, scope.InstanceID, kind, key, digest, encode(out)); err != nil {
			return err
		}
		cmd, err := s.createCommand(ctx, tx, account, actor, "runtime_"+kind, entity, posts, effect{RuntimeAdjustment: &applied})
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		if err = s.runLocked(ctx, c, cmd.ID); err != nil {
			return err
		}
		out, err = load[RuntimeAdjustment](ctx, c, `SELECT snapshot FROM product_runtime_adjustments WHERE id=$1`, entity)
		if err == nil && (out.Status == "CANCELED" || out.Status == "REJECTED") {
			return accounts.ErrForbidden
		}
		return err
	})
	return
}
