package billing

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
)

// ErrNativeReserveBudgetRejected proves a new native reservation has no hold.
// Existing-call replays return before the budget check that emits this error.
var ErrNativeReserveBudgetRejected = fmt.Errorf("%w: native reserve rejected before persistence", errUsageBudgetExceeded)

type NativeConversion struct {
	Version     string `json:"version"`
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	Rounding    string `json:"rounding"`
}

type NativeQuote struct {
	PricingVersion string           `json:"pricing_version"`
	PricingDigest  string           `json:"pricing_digest"`
	BillingModel   string           `json:"billing_model"`
	WireOperation  string           `json:"wire_operation"`
	RouteID        string           `json:"route_id"`
	ChannelID      string           `json:"channel_id"`
	Group          string           `json:"group"`
	NativeQuotaMax string           `json:"native_quota_max"`
	Conversion     NativeConversion `json:"conversion"`
	Asset          string           `json:"asset"`
	MaxAmount      string           `json:"max_amount"`
}

type NativeReserveRequest struct {
	ReserveRequest
	Model             string      `json:"model"`
	WireOperation     string      `json:"wire_operation"`
	InstanceID        string      `json:"instance_id"`
	TaskID            string      `json:"task_id"`
	CredentialVersion int64       `json:"credential_version"`
	Quote             NativeQuote `json:"quote"`
}

type NativeSettlementRequest struct {
	CallID        string `json:"call_id"`
	PricingDigest string `json:"pricing_digest"`
	NativeQuota   string `json:"native_quota"`
	Amount        string `json:"amount"`
	Usage         *Usage `json:"usage,omitempty"`
	ProviderCost  string `json:"provider_cost,omitempty"`
}

var nativeDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func nativeAmount(quota string, c NativeConversion) (string, error) {
	if c.Rounding != "lr-cumulative-ceil-v1" || (c.Version != "lr-quota-usd-cny-v1" && !validNativeCreditConversion(c)) {
		return "", ErrInvalid
	}
	q, e := integer(quota, false)
	if e != nil {
		return "", e
	}
	n, e := integer(c.Numerator, true)
	if e != nil {
		return "", e
	}
	d, e := integer(c.Denominator, true)
	if e != nil {
		return "", e
	}
	x := new(big.Int).Mul(big.NewInt(q), big.NewInt(n))
	x.Add(x, big.NewInt(d-1))
	x.Quo(x, big.NewInt(d))
	if !x.IsInt64() {
		return "", ErrInvalid
	}
	return x.String(), nil
}

func validateNativeQuote(q NativeQuote, wire string) error {
	validPricing := q.PricingVersion == "lr-native-v1" && q.Asset == CashAsset && q.Conversion.Version == "lr-quota-usd-cny-v1" || q.PricingVersion == NativeCreditPricingVersion && q.Asset == CreditAsset && validNativeCreditConversion(q.Conversion)
	if !validPricing || !nativeDigest.MatchString(q.PricingDigest) || q.WireOperation != wire || strings.TrimSpace(wire) == "" || len(wire) > 128 || strings.TrimSpace(q.BillingModel) == "" || len(q.BillingModel) > 256 || strings.TrimSpace(q.Group) == "" || len(q.Group) > 128 {
		return ErrInvalid
	}
	if _, e := integer(q.RouteID, false); e != nil {
		return e
	}
	if _, e := integer(q.ChannelID, true); e != nil {
		return e
	}
	amount, e := nativeAmount(q.NativeQuotaMax, q.Conversion)
	if e != nil || amount != q.MaxAmount {
		return ErrInvalid
	}
	return nil
}

// Optional usage is an observation. Charge calculation uses only native quota
// and the frozen conversion. Validate observations before storing audit data.
func validateNativeUsage(u *Usage) error {
	if u == nil {
		return nil
	}
	for _, v := range []string{u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Reasoning} {
		if v != "" {
			if _, e := integer(v, false); e != nil {
				return e
			}
			if u.Metered != nil {
				return ErrInvalid
			}
		}
	}
	if u.Metered == nil {
		return nil
	}
	m := u.Metered
	if m.Version != "metered_v2" || m.State != "confirmed" || strings.TrimSpace(m.Operation) == "" || len(m.Operation) > 64 || len(m.Source) > 128 || len(m.ProviderRequestID) > 256 || len(m.Measurements) > 64 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, x := range m.Measurements {
		if x.Key == "" || len(x.Key) > 64 || seen[x.Key] || x.Unit == "" || len(x.Unit) > 64 || len(x.ProviderUnit) > 128 || x.Quantity.Scale < 0 || x.Quantity.Scale > 9 || len(x.Quantity.Value) > 38 || !nativeQuantity.MatchString(x.Quantity.Value) {
			return ErrInvalid
		}
		if (x.Unit == "token" || x.Unit == "character" || x.Unit == "image" || x.Unit == "request") && x.Quantity.Scale != 0 {
			return ErrInvalid
		}
		seen[x.Key] = true
	}
	return nil
}

var nativeQuantity = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func (s *Service) nativeUsageFundingOn(ctx context.Context, q accounts.Querier, call Call, now time.Time) (UsageAuthorization, error) {
	var mode, asset, pricing string
	var profileVersion int64
	if call.CredentialID == "" || call.CredentialVersion < 1 || call.OriginProductID != "superwagie" || call.InstanceID == "" {
		return UsageAuthorization{}, accounts.ErrForbidden
	}
	e := q.QueryRow(ctx, `SELECT funding_mode,asset,billing_profile_version,pricing_source FROM model_credentials WHERE id=$1 AND actor_id=$2 AND account_id=$3 AND origin_product_id=$4 AND instance_id=$5 AND credential_kind='api_key' AND model_scope='llmrouter_available' AND credential_version=$6 AND expires_at>$7 AND revoked_at IS NULL`, call.CredentialID, call.ActorID, call.AccountID, call.OriginProductID, call.InstanceID, call.CredentialVersion, now).Scan(&mode, &asset, &profileVersion, &pricing)
	if errors.Is(e, pgx.ErrNoRows) {
		return UsageAuthorization{}, accounts.ErrForbidden
	}
	if e != nil {
		return UsageAuthorization{}, e
	}
	if asset != callAsset(call) || mode != callFundingMode(call) || !((mode == FundingPrepaidCash && asset == CashAsset && pricing == "llmrouter_native") || (mode == FundingCredits && asset == CreditAsset && pricing == NativeCreditPricingSource)) {
		return UsageAuthorization{}, accounts.ErrForbidden
	}
	if s.productUsers == nil || !s.productUsers.Managed(call.OriginProductID, call.InstanceID) {
		return UsageAuthorization{}, accounts.ErrForbidden
	}
	if e = s.productUsers.RequirePermissionOn(ctx, q, call.ActorID, call.OriginProductID, call.InstanceID, "product:use"); e != nil {
		return UsageAuthorization{}, e
	}
	auth, e := s.authorizeUsageOn(ctx, q, call.ActorID, call.AccountID, call.OriginProductID, mode, now)
	if e != nil {
		return auth, e
	}
	if auth.Asset != asset {
		return UsageAuthorization{}, accounts.ErrForbidden
	}
	auth.ProfileVersion = profileVersion
	return auth, nil
}

// ReserveNative accepts LLMRouter's frozen native quote, retaining managed-Key
// ownership, limits and product-wallet funding in the existing call ledger.
func (s *Service) ReserveNative(ctx context.Context, service string, r NativeReserveRequest) (Call, error) {
	var out Call
	if service != "llmrouter" {
		return out, accounts.ErrForbidden
	}
	if e := s.trust(service); e != nil {
		return out, e
	}
	if e := validateNativeQuote(r.Quote, r.WireOperation); e != nil {
		return out, e
	}
	if !keyOK(r.CallID) || !keyOK(r.RequestDigest) || strings.TrimSpace(r.Model) == "" || len(r.Model) > 256 || len(r.TaskID) > 128 || r.RateID != "" || r.Amount != "" {
		return out, ErrInvalid
	}
	n, _ := integer(r.Quote.MaxAmount, false)
	requestHash := hash(struct {
		Service string
		Request NativeReserveRequest
	}{service, r})
	e := s.nativeLocked(ctx, r.AccountID, r.OriginProductID, r.InstanceID, func(c *pgxpool.Conn) error {
		old, e := load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, r.CallID)
		if e == nil {
			var savedHash string
			if e = c.QueryRow(ctx, `SELECT request_digest FROM billing_calls WHERE id=$1`, r.CallID).Scan(&savedHash); e != nil {
				return e
			}
			if savedHash != requestHash {
				return ErrConflict
			}
			out = old
			return s.resumeCallCommand(ctx, c, "reserve", r.CallID, &out)
		}
		if !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		if e = s.clear(ctx, c, r.AccountID); e != nil {
			return e
		}
		out = Call{ServiceID: service, CredentialID: r.CredentialID, CredentialVersion: r.CredentialVersion, CallID: r.CallID, ActorID: r.ActorID, AccountID: r.AccountID, OriginProductID: r.OriginProductID, RequestDigest: r.RequestDigest, InstanceID: r.InstanceID, TaskID: r.TaskID, WireOperation: r.WireOperation, Rate: Rate{Model: r.Model, Asset: r.Quote.Asset, Semantics: "llmrouter_native"}, NativePricing: &r.Quote, Held: r.Quote.MaxAmount, Charged: "0", PlatformDifference: "0", Status: "PENDING", ChargeAsset: r.Quote.Asset, FundingMode: FundingPrepaidCash, FundingProductID: "llmrouter", CashHoldAddress: CashHoldAddress(r.AccountID, r.CallID), Allocations: []Allocation{}}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if r.Quote.Asset == CreditAsset {
			out.FundingMode = FundingCredits
			out.CashHoldAddress = ""
			var productID string
			if e = tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR SHARE`, r.OriginProductID).Scan(&productID); e != nil {
				return e
			}
			ready, e := s.NativeCreditsReadinessOn(ctx, tx, r.OriginProductID)
			if e != nil {
				return e
			}
			if !ready.Ready || *ready.Conversion != r.Quote.Conversion {
				return accounts.ErrForbidden
			}
		}
		auth, e := s.nativeUsageFundingOn(ctx, c, out, s.now().UTC())
		if e != nil {
			return e
		}
		out.BillingProfileVersion = auth.ProfileVersion
		if e = s.checkUsageBudget(ctx, c, out, n, n); e != nil {
			if errors.Is(e, errUsageBudgetExceeded) {
				return ErrNativeReserveBudgetRejected
			}
			return e
		}
		posts := []ledger.Posting{}
		if n > 0 {
			if out.ChargeAsset == CashAsset {
				posts, e = s.cashReservePosts(ctx, r.AccountID, out.CashHoldAddress, n)
			} else {
				out.Allocations, e = s.allocate(ctx, c, r.AccountID, n, s.now().UTC())
				for _, a := range out.Allocations {
					amount, _ := integer(a.Amount, true)
					posts = append(posts, ledger.Posting{Source: BatchAddress(a.BatchID, "available"), Destination: BatchAddress(a.BatchID, "reserved"), Asset: CreditAsset, Amount: amount})
				}
			}
			if e != nil {
				return e
			}
		}
		held := out
		held.Status = "RESERVED"
		if _, e = tx.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,$3,$4)`, out.CallID, out.AccountID, requestHash, encode(out)); e != nil {
			return e
		}
		cmd, e := s.createCommand(ctx, tx, out.AccountID, out.ActorID, "reserve", out.CallID, posts, effect{Call: &held})
		if e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		e = s.runLocked(ctx, c, cmd.ID)
		out, _ = load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, r.CallID)
		return e
	})
	return out, e
}

func (s *Service) SettleNative(ctx context.Context, service string, r NativeSettlementRequest) (Call, error) {
	out, e := s.GetCall(ctx, service, r.CallID)
	if e != nil {
		return out, e
	}
	if service != "llmrouter" || out.NativePricing == nil {
		return out, accounts.ErrForbidden
	}
	if e = validateNativeUsage(r.Usage); e != nil {
		return out, e
	}
	if r.ProviderCost != "" {
		if _, e = integer(r.ProviderCost, false); e != nil {
			return out, e
		}
	}
	amount, e := nativeAmount(r.NativeQuota, out.NativePricing.Conversion)
	if e != nil || r.Amount != amount {
		return out, ErrInvalid
	}
	if r.PricingDigest != out.NativePricing.PricingDigest {
		return out, ErrConflict
	}
	cost, _ := integer(r.Amount, false)
	e = s.locked(ctx, out.AccountID, func(c *pgxpool.Conn) error {
		call, e := load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, r.CallID)
		if e != nil {
			return e
		}
		digest := hash(r)
		if call.Status == "SETTLED" || call.Status == "RELEASED" {
			if call.FinalDigest != digest {
				return ErrConflict
			}
			out = call
			return nil
		}
		var cmdID string
		e = c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='settle' AND request_key=$2`, call.AccountID, call.CallID).Scan(&cmdID)
		if e == nil {
			cmd, e := readCommand(ctx, c, cmdID)
			if e != nil {
				return e
			}
			if cmd.Effect.Call.FinalDigest != digest {
				return ErrConflict
			}
			out = call
			return s.resumeCallCommand(ctx, c, "settle", call.CallID, &out)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if call.Status != "RESERVED" {
			return ErrConflict
		}
		if e = s.clear(ctx, c, call.AccountID); e != nil {
			return e
		}
		held, e := integer(call.Held, false)
		if e != nil {
			return e
		}
		if cost > held {
			return ErrPending
		}
		posts, e := s.finalPosts(ctx, c, call, cost, s.now().UTC())
		if e != nil {
			return e
		}
		call.Status = "SETTLED"
		call.Charged = r.Amount
		call.PlatformDifference = "0"
		call.FinalDigest = digest
		call.ProviderCost = r.ProviderCost
		call.Usage = r.Usage
		call.NativeQuota = r.NativeQuota
		out, e = s.callCommand(ctx, c, call, "settle", call.CallID, posts)
		return e
	})
	return out, e
}
