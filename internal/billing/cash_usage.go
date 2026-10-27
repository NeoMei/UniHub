package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

// CashHoldAddress derives a ledger-safe address from an opaque call identifier.
// Caller-provided identifiers never become literal ledger address segments.
func CashHoldAddress(account, callID string) string {
	digest := sha256.Sum256([]byte(callID))
	return "cash:" + account + ":usage_v1:" + hex.EncodeToString(digest[:])
}

// Holds created before address snapshots used the literal call ID. Retain that
// address only for those immutable calls so existing valid holds can finish.
func cashHoldForCall(call Call) string {
	if call.CashHoldAddress != "" {
		return call.CashHoldAddress
	}
	return "cash:" + call.AccountID + ":usage:" + call.CallID
}

// AuthorizeUsage evaluates current policy for a new credential or usage hold.
func (s *Service) AuthorizeUsage(ctx context.Context, actor, account, product, mode string) (UsageAuthorization, error) {
	return s.authorizeUsageOn(ctx, s.pool, actor, account, product, mode, s.now().UTC())
}

// AuthorizeUsageOn reuses the caller's transaction and account lock. In
// particular, credential issuance must never acquire a nested pool connection.
func (s *Service) AuthorizeUsageOn(ctx context.Context, q accounts.Querier, actor, account, product, mode string) (UsageAuthorization, error) {
	return s.authorizeUsageOn(ctx, q, actor, account, product, mode, s.now().UTC())
}
func (s *Service) authorizeUsageOn(ctx context.Context, q accounts.Querier, actor, account, origin, mode string, now time.Time) (UsageAuthorization, error) {
	var out UsageAuthorization
	var active bool
	if e := q.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, actor).Scan(&active); e != nil {
		return out, e
	}
	if !active {
		return out, accounts.ErrForbidden
	}
	if e := s.member(ctx, q, actor, account); e != nil {
		return out, e
	}
	p, e := s.ProfileOn(ctx, q, origin)
	if e != nil {
		return out, e
	}
	if mode == "" {
		mode = p.Usage.DefaultFundingMode
	}
	if !p.Usage.Enabled || !slices.Contains(p.Usage.FundingModes, mode) {
		return out, accounts.ErrForbidden
	}
	out = UsageAuthorization{FundingMode: mode, ProfileVersion: p.Version}
	switch mode {
	case FundingCredits:
		out.Asset = CreditAsset
	case FundingPrepaidCash:
		out.Asset = CashAsset
	default:
		return UsageAuthorization{}, accounts.ErrForbidden
	}
	if p.Usage.RequireSubscription {
		sub, e := load[Subscription](ctx, q, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND product_id=$2`, account, origin)
		if e != nil {
			if errors.Is(e, accounts.ErrNotFound) {
				return UsageAuthorization{}, accounts.ErrForbidden
			}
			return UsageAuthorization{}, e
		}
		if sub.Status != "active" || !now.Before(sub.PaidThrough) {
			return UsageAuthorization{}, accounts.ErrForbidden
		}
		out.ExpiresAt = sub.PaidThrough
	}
	return out, nil
}

func (s *Service) usageFundingOn(ctx context.Context, q accounts.Querier, credential, actor, account, origin, model string, now time.Time) (UsageAuthorization, error) {
	var mode, asset string
	var version int64
	if credential != "" {
		e := q.QueryRow(ctx, `SELECT funding_mode,asset,billing_profile_version FROM model_credentials WHERE id=$1 AND actor_id=$2 AND account_id=$3 AND origin_product_id=$4 AND expires_at>$5 AND revoked_at IS NULL AND models ? $6`, credential, actor, account, origin, now, model).Scan(&mode, &asset, &version)
		if errors.Is(e, pgx.ErrNoRows) {
			return UsageAuthorization{}, accounts.ErrForbidden
		}
		if e != nil {
			return UsageAuthorization{}, e
		}
	}
	auth, e := s.authorizeUsageOn(ctx, q, actor, account, origin, mode, now)
	if e != nil {
		return auth, e
	}
	p, e := s.ProfileOn(ctx, q, origin)
	if e != nil {
		return UsageAuthorization{}, e
	}
	if p.Usage.Models != nil && !slices.Contains(p.Usage.Models, model) {
		return UsageAuthorization{}, accounts.ErrForbidden
	}
	if credential != "" {
		if asset != auth.Asset {
			return UsageAuthorization{}, accounts.ErrForbidden
		}
		auth.ProfileVersion = version
	}
	return auth, nil
}

func callAsset(call Call) string { return normalizedAsset(call.ChargeAsset) }
func callFundingMode(call Call) string {
	if call.FundingMode == "" {
		return FundingCredits
	}
	return call.FundingMode
}
func (s *Service) callFundingAllowed(ctx context.Context, q accounts.Querier, call Call, now time.Time) error {
	auth, e := s.authorizeUsageOn(ctx, q, call.ActorID, call.AccountID, call.OriginProductID, callFundingMode(call), now)
	if e != nil {
		return e
	}
	if auth.Asset != callAsset(call) || normalizedAsset(call.Rate.Asset) != callAsset(call) {
		return accounts.ErrForbidden
	}
	if callAsset(call) == CashAsset && call.FundingProductID != "llmrouter" {
		return accounts.ErrForbidden
	}
	if call.CredentialID != "" {
		auth, e = s.usageFundingOn(ctx, q, call.CredentialID, call.ActorID, call.AccountID, call.OriginProductID, call.Rate.Model, now)
		if e != nil {
			return e
		}
		if auth.Asset != callAsset(call) || auth.FundingMode != callFundingMode(call) {
			return accounts.ErrForbidden
		}
	}
	return nil
}
func (s *Service) cashReservePosts(ctx context.Context, account, holdAddress string, n int64) ([]ledger.Posting, error) {
	available, e := s.ledger.Balance(ctx, wallet.Address(account, "llmrouter"), CashAsset)
	if e != nil {
		return nil, e
	}
	if available < n {
		return nil, ledger.ErrInsufficientFunds
	}
	return []ledger.Posting{{Source: wallet.Address(account, "llmrouter"), Destination: holdAddress, Asset: CashAsset, Amount: n}}, nil
}
func cashFinalPosts(call Call, charge int64) ([]ledger.Posting, error) {
	held, e := integer(call.Held, false)
	if e != nil || charge < 0 || charge > held {
		return nil, ErrInvalid
	}
	posts := []ledger.Posting{}
	if charge > 0 {
		posts = append(posts, ledger.Posting{Source: cashHoldForCall(call), Destination: "commercial:revenue:llmrouter", Asset: CashAsset, Amount: charge})
	}
	if held > charge {
		posts = append(posts, ledger.Posting{Source: cashHoldForCall(call), Destination: wallet.Address(call.AccountID, "llmrouter"), Asset: CashAsset, Amount: held - charge})
	}
	return posts, nil
}
func (s *Service) validateCashAllocation(ctx context.Context, q accounts.Querier, x command) error {
	call := x.Effect.Call
	if call == nil || call.FundingProductID != "llmrouter" || callFundingMode(*call) != FundingPrepaidCash || normalizedAsset(call.Rate.Asset) != CashAsset || len(x.Ledger.Postings) != 1 {
		return ErrInvalid
	}
	p := x.Ledger.Postings[0]
	if p.Source != wallet.Address(x.Account, "llmrouter") || p.Destination != cashHoldForCall(*call) || p.Asset != CashAsset || p.Amount <= 0 {
		return ErrInvalid
	}
	return nil
}
