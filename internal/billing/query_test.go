package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

// A query must not mistake the reservation for the final charge, include a
// different product/account, or expose internal observations in its public DTO.
func TestUsageQueriesUseFinalAmountsAndHideInternalObservations(t *testing.T) {
	f := setup(t) // isolated real PostgreSQL and real Formance ledger
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-month-1", "usage-purchase")
	rateFixture(t, f)
	if _, err := f.s.Reserve(ctx, "router", ReserveRequest{"usage-settled", f.actor, f.account, "superwagie", "private-digest", "r", "100", ""}); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.s.QueryCall(ctx, f.actor, f.account, "usage-settled", "superwagie")
	if err != nil || fresh.CreatedAt == nil || fresh.TimeSource != "reserve_received" || fresh.CreatedAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("new timestamp %+v %v", fresh, err)
	}
	if _, err := f.s.Settle(ctx, "router", SettlementRequest{"usage-settled", &Usage{Input: "150", Output: "2", CacheRead: "3", CacheWrite: "4", Reasoning: "5"}, "900"}); err != nil {
		t.Fatal(err)
	}
	when := dt("2026-10-20T12:00:00Z")
	if _, err := f.s.pool.Exec(ctx, `UPDATE billing_calls SET created_at=$1 WHERE id='usage-settled'`, when); err != nil {
		t.Fatal(err)
	}
	queryInsert(t, f.s, Call{CallID: "usage-missing", AccountID: f.account, ActorID: f.actor, OriginProductID: "superwagie", Rate: Rate{Model: "test"}, Status: "MISSING_USAGE", Held: "500", Charged: "0"}, &when)
	queryInsert(t, f.s, Call{CallID: "usage-unknown", AccountID: f.account, ActorID: f.actor, OriginProductID: "superwagie", Rate: Rate{Model: "test"}, Status: "UNKNOWN", Held: "600", Charged: "0"}, &when)
	// A real ambiguous ledger command leaves the last applied call snapshot
	// unchanged. Reporting that snapshot as RESERVED would hide uncertainty.
	if _, err := f.s.Reserve(ctx, "router", ReserveRequest{"usage-pending-command", f.actor, f.account, "superwagie", "d", "r", "20", ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.pool.Exec(ctx, `UPDATE billing_calls SET created_at=$1 WHERE id='usage-pending-command'`, when); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.pool.Exec(ctx, `UPDATE billing_commands SET status='UNKNOWN' WHERE kind='reserve' AND request_key='usage-pending-command'`); err != nil {
		t.Fatal(err)
	}
	queryInsert(t, f.s, Call{CallID: "other-origin", AccountID: f.account, ActorID: f.actor, OriginProductID: "agentwiki", Rate: Rate{Model: "test"}, Status: "SETTLED", Charged: "999999", Usage: &Usage{Input: "999999", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}}, &when)
	filter := CallFilter{From: when.Add(-time.Hour), To: when.Add(time.Hour), OriginProductID: "superwagie"}
	summary, err := f.s.QueryUsageSummary(ctx, f.actor, f.account, filter)
	if err != nil || summary.CallsTotal != "4" || summary.SettledCalls != "1" || summary.MissingUsageCalls != "1" || summary.UnknownCalls != "2" || summary.Charged != "100" || summary.Usage.Input != "150" || summary.Usage.Output != "2" || summary.Usage.CacheRead != "3" || summary.Usage.CacheWrite != "4" || summary.Usage.Reasoning != "5" || summary.UsageComplete || summary.Scope.Visibility != "account" {
		t.Fatalf("summary %+v, err=%v", summary, err)
	}
	page, err := f.s.QueryCalls(ctx, f.actor, f.account, filter, CallPageRequest{Limit: 2})
	if err != nil || page.Total != "4" || len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page %+v err=%v", page, err)
	}
	view, err := f.s.QueryCall(ctx, f.actor, f.account, "usage-settled", "superwagie")
	if err != nil || view.Charged != "100" || view.Model != "fixture" || view.CreatedAt == nil || !view.FinancialDetails {
		t.Fatalf("view %+v err=%v", view, err)
	}
	uncertain, err := f.s.QueryCall(ctx, f.actor, f.account, "usage-pending-command", "superwagie")
	if err != nil || uncertain.Status != "UNKNOWN" {
		t.Fatalf("uncertain call %+v %v", uncertain, err)
	}
	wire, _ := json.Marshal(view)
	for _, sensitive := range []string{"provider_cost", "platform_difference", "allocations", "credential_id", "request_digest", "final_digest", "private-digest"} {
		if strings.Contains(string(wire), sensitive) {
			t.Fatalf("public DTO exposes %q: %s", sensitive, wire)
		}
	}
	bs, err := f.s.Batches(ctx, f.actor, f.account)
	if err != nil || len(bs) != 1 || bs[0].Consumed != "100" {
		t.Fatalf("query changed real ledger: %+v %v", bs, err)
	}
}

// This catches offset pagination, totals accidentally computed after the cursor,
// and a member reading another actor's financial or model-usage history.
func TestUsageQueriesKeysetScopeAndCursorBinding(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	owner, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://usage-test", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://usage-test", Subject: "member"})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://usage-test", Subject: "outsider"})
	if err != nil {
		t.Fatal(err)
	}
	team, err := a.CreateTeam(ctx, owner.ID, "Usage team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.Invite(ctx, owner.ID, team.ID, member.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptInvitation(ctx, member.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	s := New(p, a, nil) // all methods below are metadata-only queries
	when := dt("2026-10-20T12:00:00Z")
	personal, err := a.ListAccounts(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var otherAccount string
	for _, account := range personal {
		if account.Kind == "personal" {
			otherAccount = account.ID
		}
	}
	queryInsert(t, s, Call{CallID: "personal-call", AccountID: otherAccount, ActorID: owner.ID, OriginProductID: "superwagie", Rate: Rate{Model: "model-a"}, Status: "SETTLED", Charged: "999999", Usage: &Usage{Input: "999999", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}}, &when)
	for i := range 205 {
		queryInsert(t, s, Call{CallID: fmt.Sprintf("page-%03d", i), AccountID: team.ID, ActorID: owner.ID, OriginProductID: "superwagie", Rate: Rate{Model: "model-a"}, Status: "SETTLED", Held: "5", Charged: "3", Usage: &Usage{Input: "2", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}}, &when)
	}
	queryInsert(t, s, Call{CallID: "member-call", AccountID: team.ID, ActorID: member.ID, OriginProductID: "superwagie", Rate: Rate{Model: "model-b"}, Status: "SETTLED", Held: "50", Charged: "30", Usage: &Usage{Input: "20", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}}, &when)
	queryInsert(t, s, Call{CallID: "legacy-unknown", AccountID: team.ID, ActorID: owner.ID, OriginProductID: "superwagie", Rate: Rate{Model: "model-a"}, Status: "PENDING", Held: "0", Charged: "0"}, nil)
	filter := CallFilter{OriginProductID: "superwagie", Model: "model-a"}
	seen, cursor := map[string]bool{}, ""
	for {
		page, err := s.QueryCalls(ctx, owner.ID, team.ID, filter, CallPageRequest{Limit: 100, Cursor: cursor})
		if err != nil || page.Total != "206" || page.UnknownTime != "1" {
			t.Fatalf("page total %+v %v", page, err)
		}
		for _, item := range page.Items {
			if seen[item.CallID] {
				t.Fatalf("duplicate %s", item.CallID)
			}
			seen[item.CallID] = true
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 206 {
		t.Fatalf("only %d calls", len(seen))
	}
	for _, id := range []string{"null-a", "null-b", "null-c"} {
		queryInsert(t, s, Call{CallID: id, AccountID: team.ID, ActorID: owner.ID, OriginProductID: "agentwiki", Rate: Rate{Model: "legacy"}, Status: "RELEASED", Charged: "0"}, nil)
	}
	nullCursor := ""
	nullSeen := map[string]bool{}
	for {
		page, err := s.QueryCalls(ctx, owner.ID, team.ID, CallFilter{OriginProductID: "agentwiki", Model: "legacy"}, CallPageRequest{Limit: 1, Cursor: nullCursor})
		if err != nil || page.Total != "3" || page.UnknownTime != "3" || len(page.Items) != 1 || page.Items[0].CreatedAt != nil {
			t.Fatalf("null-time page %+v %v", page, err)
		}
		if nullSeen[page.Items[0].CallID] {
			t.Fatal("null-time cursor repeated row")
		}
		nullSeen[page.Items[0].CallID] = true
		if !page.HasMore {
			break
		}
		nullCursor = page.NextCursor
	}
	if len(nullSeen) != 3 {
		t.Fatalf("null-time traversal %d", len(nullSeen))
	}
	first, err := s.QueryCalls(ctx, owner.ID, team.ID, filter, CallPageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreCallFilter(first.NextCursor)
	if err != nil || restored.OriginProductID != "superwagie" || restored.Model != "model-a" || !restored.From.IsZero() {
		t.Fatalf("restored %+v %v", restored, err)
	}
	if _, err := RestoreCallFilter(first.NextCursor + "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered restored filter %v", err)
	}
	if _, err := s.QueryCalls(ctx, owner.ID, otherAccount, filter, CallPageRequest{Limit: 2, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-account cursor %v", err)
	}
	if _, err := s.QueryCall(ctx, owner.ID, team.ID, "personal-call", "superwagie"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("cross-account lookup %v", err)
	}
	for _, change := range []CallFilter{{OriginProductID: "agentwiki", Model: "model-a"}, {OriginProductID: "superwagie", Model: "model-b"}, {OriginProductID: "superwagie", Model: "model-a", From: when}} {
		if _, err := s.QueryCalls(ctx, owner.ID, team.ID, change, CallPageRequest{Limit: 2, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("changed filter accepted %v", err)
		}
	}
	if _, err := s.QueryCalls(ctx, owner.ID, team.ID, filter, CallPageRequest{Limit: 2, Cursor: first.NextCursor + "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered cursor %v", err)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err := s.QueryCalls(ctx, owner.ID, team.ID, filter, CallPageRequest{Limit: limit}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	if _, err := s.QueryUsageSummary(ctx, outsider.ID, team.ID, filter); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("outsider %v", err)
	}
	if _, err := s.QueryCall(ctx, owner.ID, team.ID, "member-call", "agentwiki"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("other origin %v", err)
	}
	memberPage, err := s.QueryCalls(ctx, member.ID, team.ID, CallFilter{OriginProductID: "superwagie"}, CallPageRequest{Limit: 100})
	if err != nil || memberPage.Total != "1" || len(memberPage.Items) != 1 || memberPage.Items[0].Charged != "" || memberPage.Items[0].Held != "" || memberPage.FinancialDetails || memberPage.Scope.Visibility != "actor" || memberPage.Scope.ActorID != member.ID {
		t.Fatalf("member %+v %v", memberPage, err)
	}
	memberSummary, err := s.QueryUsageSummary(ctx, member.ID, team.ID, CallFilter{OriginProductID: "superwagie"})
	if err != nil || memberSummary.CallsTotal != "1" || memberSummary.Usage.Input != "20" || memberSummary.Charged != "" || memberSummary.FinancialDetails {
		t.Fatalf("member summary %+v %v", memberSummary, err)
	}
	if _, err := s.QueryCall(ctx, member.ID, team.ID, "page-001", "superwagie"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("member other actor %v", err)
	}
	if err := a.ChangeRole(ctx, owner.ID, team.ID, member.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	admin, err := s.QueryUsageSummary(ctx, member.ID, team.ID, CallFilter{OriginProductID: "superwagie"})
	if err != nil || admin.CallsTotal != "1" || admin.FinancialDetails || admin.Scope.Visibility != "actor" {
		t.Fatalf("team admin gained finance %+v %v", admin, err)
	}
	if err := a.ChangeRole(ctx, owner.ID, team.ID, member.ID, "billing_admin"); err != nil {
		t.Fatal(err)
	}
	finance, err := s.QueryUsageSummary(ctx, member.ID, team.ID, CallFilter{OriginProductID: "superwagie", Model: "model-a"})
	if err != nil || finance.CallsTotal != "206" || finance.Charged != "615" || !finance.FinancialDetails || finance.Scope.Visibility != "account" {
		t.Fatalf("billing admin %+v %v", finance, err)
	}
	bounded, err := s.QueryCalls(ctx, owner.ID, team.ID, CallFilter{From: when.Add(-time.Hour), To: when.Add(time.Hour), OriginProductID: "superwagie", Model: "model-a"}, CallPageRequest{Limit: 100})
	if err != nil || bounded.Total != "205" || bounded.UnknownTime != "1" {
		t.Fatalf("bounded %+v %v", bounded, err)
	}
	if err := a.RemoveMember(ctx, owner.ID, team.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryCalls(ctx, member.ID, team.ID, CallFilter{}, CallPageRequest{Limit: 1}); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("revoked %v", err)
	}
}

func TestUsageSummaryExactIntegerTotalsAndMalformedStoredValues(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://integer-usage", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := New(p, a, nil)
	when := dt("2026-10-20T12:00:00Z")
	for _, id := range []string{"big-a", "big-b"} {
		queryInsert(t, s, Call{CallID: id, AccountID: as[0].ID, ActorID: u.ID, OriginProductID: "superwagie", Status: "SETTLED", Charged: "9223372036854775807", Usage: &Usage{Input: "9223372036854775807", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}}, &when)
	}
	summary, err := s.QueryUsageSummary(ctx, u.ID, as[0].ID, CallFilter{})
	if err != nil || summary.Charged != "18446744073709551614" || summary.Usage.Input != "18446744073709551614" || !summary.UsageComplete || summary.Asset != "CREDIT/6" {
		t.Fatalf("exact total %+v %v", summary, err)
	}
	if _, err = p.Exec(ctx, `UPDATE billing_calls SET snapshot=jsonb_set(snapshot,'{usage,input}','"1.5"') WHERE id='big-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueryUsageSummary(ctx, u.ID, as[0].ID, CallFilter{}); err == nil {
		t.Fatal("malformed fractional usage accepted")
	}
}

func TestUsageMigrationPreservesUnknownLegacyCallTime(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://legacy-usage", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `ALTER TABLE billing_calls DROP COLUMN created_at, DROP COLUMN time_source; DROP INDEX billing_commands_unknown_call; DELETE FROM schema_migrations WHERE version='migrations/008_usage_queries.sql'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"legacy-known", "legacy-no-evidence"} {
		call := Call{CallID: id, AccountID: as[0].ID, ActorID: u.ID, OriginProductID: "superwagie", Rate: Rate{Model: "legacy"}, Status: "SETTLED", Charged: "0"}
		if _, err = p.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'d',$3)`, id, as[0].ID, encode(call)); err != nil {
			t.Fatal(err)
		}
	}
	known := dt("2025-02-01T01:02:03Z")
	if _, err = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status,created_at) VALUES('legacy-command',$1,$2,'reserve','legacy-known','d','{}','{}','APPLIED',$3)`, as[0].ID, u.ID, known); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	s := New(p, a, nil)
	view, err := s.QueryCall(ctx, u.ID, as[0].ID, "legacy-known", "superwagie")
	if err != nil || view.CreatedAt == nil || !view.CreatedAt.Equal(known) || view.TimeSource != "reserve_command" {
		t.Fatalf("known %+v %v", view, err)
	}
	view, err = s.QueryCall(ctx, u.ID, as[0].ID, "legacy-no-evidence", "superwagie")
	if err != nil || view.CreatedAt != nil || view.TimeSource != "unknown" {
		t.Fatalf("unknown %+v %v", view, err)
	}
	summary, err := s.QueryUsageSummary(ctx, u.ID, as[0].ID, CallFilter{From: known.Add(-time.Hour), To: known.Add(time.Hour)})
	if err != nil || summary.CallsTotal != "1" || summary.UnknownTime != "1" {
		t.Fatalf("legacy summary %+v %v", summary, err)
	}
}

func queryInsert(t *testing.T, s *Service, c Call, when *time.Time) {
	t.Helper()
	source := "reserve_received"
	if when == nil {
		source = "unknown"
	}
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO billing_calls(id,account_id,request_digest,snapshot,created_at,time_source) VALUES($1,$2,'query-fixture',$3,$4,$5)`, c.CallID, c.AccountID, encode(c), when, source); err != nil {
		t.Fatal(err)
	}
}
