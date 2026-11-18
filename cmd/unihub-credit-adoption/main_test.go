package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

func TestAdoptionCommandGuardsProductionAndMakesPreviewReadOnly(t *testing.T) {
	config := map[string]string{
		"UNIHUB_ADOPTION_DATABASE_URL": "postgresql://127.0.0.1/postgres?sslmode=disable",
		"UNIHUB_ADOPTION_LEDGER_URL":   "http://127.0.0.1:53478",
		"UNIHUB_ADOPTION_LEDGER_NAME":  "legacy-credit-fixture-proof",
	}
	getenv := func(key string) string { return config[key] }
	if _, _, _, e := openOptions(getenv, true); e == nil {
		t.Fatal("write configuration accepted without a disposable schema")
	}
	preview, _, _, e := openOptions(getenv, false)
	if e != nil || preview.ConnConfig.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatalf("read-only configuration: %v", e)
	}
	config["UNIHUB_ADOPTION_DATABASE_URL"] += "&search_path=legacy_credit_fixture_proof"
	if _, _, _, e = openOptions(getenv, true); e != nil {
		t.Fatalf("isolated fixture rejected: %v", e)
	}
	config["UNIHUB_ADOPTION_LEDGER_URL"] = "https://example.invalid"
	if _, _, _, e = openOptions(getenv, false); e == nil {
		t.Fatal("remote ledger accepted")
	}
	config["UNIHUB_ADOPTION_LEDGER_URL"] = "http://127.0.0.1:53478"
	config["UNIHUB_ADOPTION_DATABASE_URL"] = "postgresql://127.0.0.1,example.invalid/postgres?sslmode=disable"
	if _, _, _, e = openOptions(getenv, false); e == nil {
		t.Fatal("remote PostgreSQL fallback accepted")
	}
}

func TestAdoptionCommandDryRunApplyReplayAndVerify(t *testing.T) {
	rawDSN, target := os.Getenv("UNIHUB_TEST_DATABASE_URL"), os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if rawDSN == "" || target == "" {
		t.Skip("local PostgreSQL and Formance fixtures required")
	}
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	schema, ledgerName := "legacy_credit_fixture_"+suffix, "legacy-credit-fixture-"+suffix
	dsn, e := url.Parse(rawDSN)
	if e != nil {
		t.Fatal(e)
	}
	query := dsn.Query()
	query.Set("search_path", schema)
	dsn.RawQuery = query.Encode()
	cfg := map[string]string{
		"UNIHUB_ADOPTION_DATABASE_URL": dsn.String(),
		"UNIHUB_ADOPTION_LEDGER_URL":   target,
		"UNIHUB_ADOPTION_LEDGER_NAME":  ledgerName,
	}
	getenv := func(key string) string { return cfg[key] }
	db, _, _, e := openOptions(getenv, true)
	if e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.NewWithConfig(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = pool.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	if e = database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	ledgerClient := ledger.NewFormance(target, ledgerName, nil)
	if e = ledgerClient.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	store := accounts.New(pool)
	actor, e := store.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://fixture.invalid", Subject: "legacy-adoption"})
	if e != nil {
		t.Fatal(e)
	}
	payers, e := store.ListAccounts(ctx, actor.ID)
	if e != nil || len(payers) != 1 {
		t.Fatalf("fixture payer: %+v %v", payers, e)
	}
	account := payers[0].ID
	if _, e = ledgerClient.Post(ctx, ledger.Command{Reference: "test-paid-funding", Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(account, "superwagie"), Asset: billing.CashAsset, Amount: 3000000}}}); e != nil {
		t.Fatal(e)
	}
	b := billing.New(pool, store, ledgerClient)
	b.ConfigureOperators([]string{actor.ID})
	b.ConfigureSandbox(true)
	if _, e = b.SeedSandbox(ctx); e != nil {
		t.Fatal(e)
	}
	rate := billing.Rate{ID: "legacy-rate", Model: "legacy-model", Semantics: "exclusive_v1"}
	zero := billing.Rational{Numerator: "0", Denominator: "1"}
	rate.Input, rate.Output, rate.CacheRead, rate.CacheWrite, rate.Reasoning = zero, zero, zero, zero, zero
	if _, e = b.PublishRate(ctx, actor.ID, rate); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Purchase(ctx, actor.ID, account, billing.PurchaseRequest{OfferID: "sandbox-sw-month-1", Key: "legacy-original"}); e != nil {
		t.Fatal(e)
	}
	profile, e := b.Profile(ctx, "superwagie")
	if e != nil {
		t.Fatal(e)
	}
	profile.CreditScope = billing.ProductCreditScopeV1
	profile.NativeCreditConversion = &billing.NativeConversion{Version: "exact-fixture", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
	if _, e = b.SaveProfile(ctx, actor.ID, "superwagie", profile); e != nil {
		t.Fatal(e)
	}
	before, e := b.Batches(ctx, actor.ID, account)
	if e != nil || len(before) != 1 {
		t.Fatalf("fixture grant: %+v %v", before, e)
	}
	args := []string{"--actor", actor.ID, "--account", account, "--product", "superwagie", "--batch", before[0].ID}
	var out bytes.Buffer
	if e = run(args, getenv, &out); e != nil {
		t.Fatal(e)
	}
	var plan billing.LegacyCreditAdoptionPlan
	if e = json.Unmarshal(out.Bytes(), &plan); e != nil || !plan.CanApply || plan.Cash.Product != "2000000" {
		t.Fatalf("dry run: %+v %v", plan, e)
	}
	for _, action := range []string{"--apply-digest", "--apply-digest", "--verify-digest"} {
		out.Reset()
		if e = run(append(args, action, plan.Digest), getenv, &out); e != nil {
			t.Fatal(e)
		}
		var result billing.LegacyCreditAdoptionVerification
		if e := json.Unmarshal(out.Bytes(), &result); e != nil || result.Cash != plan.Cash || len(result.Current) != 1 ||
			result.Current[0].CreditScope != billing.ProductCreditScopeV1 || !result.Current[0].ExpiresAt.Equal(before[0].ExpiresAt) {
			t.Fatalf("%s: %+v %v", action, result, e)
		}
	}
}
