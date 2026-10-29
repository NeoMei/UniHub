package billing

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
)

func BatchAddress(batch, bucket string) string { return "credits:batch:" + batch + ":" + bucket }
func (s *Service) Batches(ctx context.Context, actor, account string) ([]Batch, error) {
	if e := s.member(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	bs, e := list[Batch](ctx, s.pool, `SELECT snapshot FROM billing_batches WHERE account_id=$1 ORDER BY (snapshot->>'expires_at')::timestamptz,(snapshot->>'created_at')::timestamptz,id`, account)
	if e != nil {
		return nil, e
	}
	for i := range bs {
		if e = s.quantities(ctx, &bs[i]); e != nil {
			return nil, e
		}
	}
	return bs, nil
}
func (s *Service) quantities(ctx context.Context, b *Batch) error {
	dest := []*string{&b.Available, &b.Reserved, &b.Consumed, &b.Expired, &b.Withdrawn}
	for i, bucket := range []string{"available", "reserved", "consumed", "expired", "withdrawn"} {
		n, e := s.ledger.Balance(ctx, BatchAddress(b.ID, bucket), CreditAsset)
		if e != nil {
			return e
		}
		*dest[i] = str(n)
	}
	return nil
}
func (s *Service) allocate(ctx context.Context, c *pgxpool.Conn, account string, n int64, now time.Time) ([]Allocation, error) {
	bs, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND NOT (snapshot->>'frozen')::boolean AND (snapshot->>'starts_at')::timestamptz <= $2 AND (snapshot->>'expires_at')::timestamptz > $2 ORDER BY (snapshot->>'expires_at')::timestamptz,(snapshot->>'created_at')::timestamptz,CASE WHEN snapshot->>'kind'='addon' THEN 1 ELSE 0 END,id`, account, now)
	if e != nil {
		return nil, e
	}
	out := []Allocation{}
	for _, b := range bs {
		balance, e := s.ledger.Balance(ctx, BatchAddress(b.ID, "available"), CreditAsset)
		if e != nil {
			return nil, e
		}
		take := balance
		if take > n {
			take = n
		}
		if take > 0 {
			out = append(out, Allocation{b.ID, str(take)})
			n -= take
		}
		if n == 0 {
			return out, nil
		}
	}
	return nil, ErrInsufficientCredits
}
func (s *Service) GetCall(ctx context.Context, service, callID string) (Call, error) {
	if e := s.trust(service); e != nil {
		return Call{}, e
	}
	call, e := load[Call](ctx, s.pool, `SELECT snapshot FROM billing_calls WHERE id=$1`, callID)
	if e != nil {
		return call, e
	}
	if call.ServiceID != service {
		return Call{}, accounts.ErrForbidden
	}
	return call, nil
}
func (s *Service) Reserve(ctx context.Context, service string, r ReserveRequest) (Call, error) {
	return s.ReserveWithScope(ctx, service, r, "", "")
}

func reserveRequestHash(service string, r ReserveRequest, instance, task string) string {
	if instance == "" && task == "" {
		// Preserve the exact immutable digest shape of historical task calls.
		return hash(struct {
			S string
			R ReserveRequest
		}{service, r})
	}
	return hash(struct {
		S                  string
		R                  ReserveRequest
		InstanceID, TaskID string
	}{service, r, instance, task})
}

func (s *Service) ReserveWithScope(ctx context.Context, service string, r ReserveRequest, instance, task string) (Call, error) {
	return s.ReserveWithWire(ctx, service, r, instance, task, "")
}

func (s *Service) ReserveWithWire(ctx context.Context, service string, r ReserveRequest, instance, task, wire string) (Call, error) {
	requestHash := reserveRequestHash(service, r, instance, task)
	if wire != "" {
		requestHash = hash(struct{ Base, Wire string }{requestHash, wire})
	}
	var out Call
	if e := s.trust(service); e != nil {
		return out, e
	}
	n, e := integer(r.Amount, true)
	if e != nil || !keyOK(r.CallID) || !keyOK(r.RequestDigest) {
		return out, ErrInvalid
	}
	e = s.locked(ctx, r.AccountID, func(c *pgxpool.Conn) error {
		old, e := load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, r.CallID)
		if e == nil {
			var h string
			if e = c.QueryRow(ctx, `SELECT request_digest FROM billing_calls WHERE id=$1`, r.CallID).Scan(&h); e != nil {
				return e
			}
			if h != requestHash {
				return ErrConflict
			}
			out = old
			return s.resumeCallCommand(ctx, c, "reserve", r.CallID, &out)
		}
		if !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		now := s.now().UTC()
		if e = s.clear(ctx, c, r.AccountID); e != nil {
			return e
		}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('unihub:billing-rate-catalog',0))`); e != nil {
			return e
		}
		rate, e := load[Rate](ctx, c, `SELECT snapshot FROM billing_rates WHERE id=$1 AND status='published'`, r.RateID)
		if e != nil {
			return e
		}
		if wire != "" && !RateMatchesWire(rate, wire) {
			return accounts.ErrForbidden
		}
		auth, e := s.usageFundingOn(ctx, c, r.CredentialID, r.ActorID, r.AccountID, r.OriginProductID, rate.Model, now)
		if e != nil {
			return e
		}
		rate.Asset = normalizedAsset(rate.Asset)
		if rate.Asset != auth.Asset {
			return accounts.ErrForbidden
		}
		if r.CredentialID != "" {
			rates, err := list[Rate](ctx, c, `SELECT snapshot FROM billing_rates WHERE status='published' AND snapshot->>'model'=$1 AND COALESCE(NULLIF(snapshot->>'asset',''),'CREDIT/6')=$2`, rate.Model, auth.Asset)
			if err != nil {
				return err
			}
			if !ModelRatesUnambiguous(rates, rate.Model, auth.Asset) {
				return accounts.ErrForbidden
			}
		}
		budgetCall := Call{CredentialID: r.CredentialID, ActorID: r.ActorID, AccountID: r.AccountID, OriginProductID: r.OriginProductID, InstanceID: instance, FundingMode: auth.FundingMode, ChargeAsset: auth.Asset}
		if e = s.checkUsageBudget(ctx, c, budgetCall, n, n); e != nil {
			return e
		}
		alloc := []Allocation{}
		posts := []ledger.Posting{}
		if auth.Asset == CashAsset {
			posts, e = s.cashReservePosts(ctx, r.AccountID, CashHoldAddress(r.AccountID, r.CallID), n)
		} else {
			alloc, e = s.allocate(ctx, c, r.AccountID, n, now)
			for _, a := range alloc {
				n, _ := integer(a.Amount, true)
				posts = append(posts, ledger.Posting{Source: BatchAddress(a.BatchID, "available"), Destination: BatchAddress(a.BatchID, "reserved"), Asset: CreditAsset, Amount: n})
			}
		}
		if e != nil {
			return e
		}
		out = Call{WireOperation: wire, ServiceID: service, CredentialID: r.CredentialID, CallID: r.CallID, ActorID: r.ActorID, AccountID: r.AccountID, OriginProductID: r.OriginProductID, RequestDigest: r.RequestDigest, Rate: rate, Held: r.Amount, Charged: "0", PlatformDifference: "0", Status: "PENDING", Allocations: alloc, ChargeAsset: auth.Asset, FundingMode: auth.FundingMode, FundingProductID: "llmrouter", BillingProfileVersion: auth.ProfileVersion, InstanceID: instance, TaskID: task}
		if auth.Asset == CashAsset {
			out.CashHoldAddress = CashHoldAddress(r.AccountID, r.CallID)
		}
		held := out
		held.Status = "RESERVED"
		_, e = tx.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,$3,$4)`, out.CallID, out.AccountID, requestHash, encode(out))
		if e != nil {
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
func (s *Service) resumeCallCommand(ctx context.Context, c *pgxpool.Conn, kind, key string, out *Call) error {
	var cmdID string
	e := c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind=$2 AND request_key=$3`, out.AccountID, kind, key).Scan(&cmdID)
	if e != nil {
		return e
	}
	e = s.runLocked(ctx, c, cmdID)
	saved, re := load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, out.CallID)
	if re == nil {
		*out = saved
	}
	if e != nil {
		return e
	}
	return re
}
func (s *Service) callCommand(ctx context.Context, c *pgxpool.Conn, call Call, kind, key string, posts []ledger.Posting) (Call, error) {
	tx, e := c.Begin(ctx)
	if e != nil {
		return call, e
	}
	defer database.Rollback(tx)
	cmd, e := s.createCommand(ctx, tx, call.AccountID, call.ActorID, kind, key, posts, effect{Call: &call})
	if e != nil {
		return call, e
	}
	if e = tx.Commit(ctx); e != nil {
		return call, e
	}
	e = s.runLocked(ctx, c, cmd.ID)
	saved, re := load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, call.CallID)
	if re == nil {
		call = saved
	}
	if e != nil {
		return call, e
	}
	return call, re
}
func (s *Service) TopUp(ctx context.Context, service, callID, amount, key string) (Call, error) {
	out, e := s.GetCall(ctx, service, callID)
	if e != nil {
		return out, e
	}
	n, e := integer(amount, true)
	if e != nil || !keyOK(key) {
		return out, ErrInvalid
	}
	e = s.locked(ctx, out.AccountID, func(c *pgxpool.Conn) error {
		call, e := load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, callID)
		if e != nil {
			return e
		}
		var old []byte
		e = c.QueryRow(ctx, `SELECT effect FROM billing_commands WHERE account_id=$1 AND kind='topup' AND request_key=$2`, call.AccountID, callID+":"+key).Scan(&old)
		if e == nil {
			var eff effect
			if e = json.Unmarshal(old, &eff); e != nil {
				return e
			}
			var oldPosts []byte
			if e = c.QueryRow(ctx, `SELECT command FROM billing_commands WHERE account_id=$1 AND kind='topup' AND request_key=$2`, call.AccountID, callID+":"+key).Scan(&oldPosts); e != nil {
				return e
			}
			var command ledger.Command
			if e = json.Unmarshal(oldPosts, &command); e != nil {
				return e
			}
			sum := int64(0)
			for _, p := range command.Postings {
				sum += p.Amount
			}
			if sum != n {
				return ErrConflict
			}
			out = call
			return s.resumeCallCommand(ctx, c, "topup", callID+":"+key, &out)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if call.Status != "RESERVED" {
			return ErrConflict
		}
		now := s.now().UTC()
		if e = s.callFundingAllowed(ctx, c, call, now); e != nil {
			return e
		}
		if e = s.clear(ctx, c, call.AccountID); e != nil {
			return e
		}
		held, _ := integer(call.Held, true)
		if n > int64(^uint64(0)>>1)-held {
			return ErrInvalid
		}
		if e = s.checkUsageBudget(ctx, c, call, n, held+n); e != nil {
			return e
		}
		alloc := []Allocation{}
		posts := []ledger.Posting{}
		if callAsset(call) == CashAsset {
			posts, e = s.cashReservePosts(ctx, call.AccountID, cashHoldForCall(call), n)
		} else {
			alloc, e = s.allocate(ctx, c, call.AccountID, n, now)
			for _, a := range alloc {
				x, _ := integer(a.Amount, true)
				posts = append(posts, ledger.Posting{Source: BatchAddress(a.BatchID, "available"), Destination: BatchAddress(a.BatchID, "reserved"), Asset: CreditAsset, Amount: x})
			}
		}
		if e != nil {
			return e
		}
		call.Allocations = append(call.Allocations, alloc...)
		call.Held = str(held + n)
		out, e = s.callCommand(ctx, c, call, "topup", callID+":"+key, posts)
		return e
	})
	return out, e
}
func (s *Service) Settle(ctx context.Context, service string, r SettlementRequest) (Call, error) {
	out, e := s.GetCall(ctx, service, r.CallID)
	if e != nil {
		return out, e
	}
	// Provider cost is an independent optional observation, never the charge.
	// Preserve unknown as empty; known zero remains distinct in the final digest.
	if r.ProviderCost != "" {
		if _, e = integer(r.ProviderCost, false); e != nil {
			return out, e
		}
	}
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
		if r.Usage == nil {
			if e = s.clear(ctx, c, call.AccountID); e != nil {
				return e
			}
			if call.Status != "RESERVED" && call.Status != "MISSING_USAGE" {
				return ErrConflict
			}
			call.Status = "MISSING_USAGE"
			call.ProviderCost = r.ProviderCost
			tx, e := c.Begin(ctx)
			if e != nil {
				return e
			}
			defer database.Rollback(tx)
			if e = saveCall(ctx, tx, call); e != nil {
				return e
			}
			if e = event(ctx, tx, call.AccountID, service, call.CallID, "call.missing_usage", r); e != nil {
				return e
			}
			out = call
			return tx.Commit(ctx)
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
		if call.Status != "RESERVED" && call.Status != "MISSING_USAGE" {
			return ErrConflict
		}
		if e = s.clear(ctx, c, call.AccountID); e != nil {
			return e
		}
		if e = validateRate(call.Rate); e != nil {
			return e
		}
		charge, e := CalculateCharge(call.Rate, *r.Usage)
		if e != nil {
			return e
		}
		cost, _ := integer(charge, false)
		held, _ := integer(call.Held, false)
		difference := int64(0)
		if cost > held {
			difference = cost - held
			cost = held
		}
		posts, e := s.finalPosts(ctx, c, call, cost, s.now().UTC())
		if e != nil {
			return e
		}
		call.Status = "SETTLED"
		call.Charged = str(cost)
		call.PlatformDifference = str(difference)
		call.FinalDigest = digest
		call.ProviderCost = r.ProviderCost
		call.Usage = r.Usage
		out, e = s.callCommand(ctx, c, call, "settle", call.CallID, posts)
		return e
	})
	return out, e
}
func (s *Service) finalPosts(ctx context.Context, c *pgxpool.Conn, call Call, charge int64, now time.Time) ([]ledger.Posting, error) {
	if callAsset(call) == CashAsset {
		return cashFinalPosts(call, charge)
	}
	posts := []ledger.Posting{}
	for _, a := range call.Allocations {
		held, _ := integer(a.Amount, true)
		used := held
		if used > charge {
			used = charge
		}
		if used > 0 {
			posts = append(posts, ledger.Posting{Source: BatchAddress(a.BatchID, "reserved"), Destination: BatchAddress(a.BatchID, "consumed"), Asset: CreditAsset, Amount: used})
			charge -= used
		}
		if held > used {
			b, e := load[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE id=$1`, a.BatchID)
			if e != nil {
				return nil, e
			}
			bucket := "available"
			if !now.Before(b.ExpiresAt) {
				bucket = "expired"
			}
			posts = append(posts, ledger.Posting{Source: BatchAddress(a.BatchID, "reserved"), Destination: BatchAddress(a.BatchID, bucket), Asset: CreditAsset, Amount: held - used})
		}
	}
	return posts, nil
}
func (s *Service) Release(ctx context.Context, service, callID, reason string) (Call, error) {
	out, e := s.GetCall(ctx, service, callID)
	if e != nil {
		return out, e
	}
	if !keyOK(reason) {
		return out, ErrInvalid
	}
	e = s.locked(ctx, out.AccountID, func(c *pgxpool.Conn) error {
		call, e := load[Call](ctx, c, `SELECT snapshot FROM billing_calls WHERE id=$1`, callID)
		if e != nil {
			return e
		}
		digest := hash([]string{"release", reason})
		if call.Status == "RELEASED" {
			if call.FinalDigest != digest {
				return ErrConflict
			}
			out = call
			return nil
		}
		var old string
		e = c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='release' AND request_key=$2`, call.AccountID, callID).Scan(&old)
		if e == nil {
			cmd, e := readCommand(ctx, c, old)
			if e != nil {
				return e
			}
			if cmd.Effect.Call.FinalDigest != digest {
				return ErrConflict
			}
			out = call
			return s.resumeCallCommand(ctx, c, "release", callID, &out)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if call.Status != "RESERVED" && call.Status != "MISSING_USAGE" {
			return ErrConflict
		}
		if e = s.clear(ctx, c, call.AccountID); e != nil {
			return e
		}
		posts, e := s.finalPosts(ctx, c, call, 0, s.now().UTC())
		if e != nil {
			return e
		}
		call.Status = "RELEASED"
		call.FinalDigest = digest
		out, e = s.callCommand(ctx, c, call, "release", callID, posts)
		return e
	})
	return out, e
}
func (s *Service) expireLocked(ctx context.Context, c *pgxpool.Conn, b Batch, now time.Time) error {
	if now.Before(b.ExpiresAt) {
		return nil
	}
	if e := s.clear(ctx, c, b.AccountID); e != nil {
		return e
	}
	n, e := s.ledger.Balance(ctx, BatchAddress(b.ID, "available"), CreditAsset)
	if e != nil || n == 0 {
		return e
	}
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	cmd, e := s.createCommand(ctx, tx, b.AccountID, "worker", "expire", b.ID, []ledger.Posting{{Source: BatchAddress(b.ID, "available"), Destination: BatchAddress(b.ID, "expired"), Asset: CreditAsset, Amount: n}}, effect{})
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return s.runLocked(ctx, c, cmd.ID)
}
