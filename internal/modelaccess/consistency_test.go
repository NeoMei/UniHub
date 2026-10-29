package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/metering"
	"unihub/internal/testutil"
)

// Removing a product allowlist, duplicate check or enabled check must reject
// issuance without leaving either a capability or its audit event behind.
func TestProductModelRatesFailClosedBeforeCredentialInsert(t *testing.T) {
	for _, tc := range []struct {
		name, models string
		rates        []billing.Rate
		disabled     bool
		want         bool
	}{
		{name: "valid", models: `["model"]`, rates: []billing.Rate{{ID: "r1", Model: "model", Asset: billing.CashAsset, Status: "published"}}, want: true},
		{name: "missing", models: `["model"]`},
		{name: "duplicate", models: `["model"]`, rates: []billing.Rate{{ID: "r1", Model: "model", Asset: billing.CashAsset, Status: "published"}, {ID: "r2", Model: "model", Asset: billing.CashAsset, Status: "published"}}},
		{name: "distinct operations", models: `["model"]`, rates: []billing.Rate{{ID: "gen", Model: "model", Asset: billing.CashAsset, Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Operation: "image_generation"}}, {ID: "edit", Model: "model", Asset: billing.CashAsset, Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Operation: "image_edit"}}}, want: true},
		{name: "wrong asset", models: `["model"]`, rates: []billing.Rate{{ID: "r1", Model: "model", Asset: billing.CreditAsset, Status: "published"}}},
		{name: "cross product model", models: `["other-model"]`, rates: []billing.Rate{{ID: "r1", Model: "model", Asset: billing.CashAsset, Status: "published"}}},
		{name: "empty explicit allowlist", models: `[]`, rates: []billing.Rate{{ID: "r1", Model: "model", Asset: billing.CashAsset, Status: "published"}}},
		{name: "disabled", models: `["model"]`, rates: []billing.Rate{{ID: "r1", Model: "model", Asset: billing.CashAsset, Status: "published"}}, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p := testutil.Database(t)
			if err := database.Migrate(ctx, p); err != nil {
				t.Fatal(err)
			}
			a := accounts.New(p)
			u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://consistency", Subject: "actor"})
			if err != nil {
				t.Fatal(err)
			}
			as, err := a.ListAccounts(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			b := billing.New(p, a, nil)
			raw := []byte(`{"product_id":"llmrouter","version":1,"subscription":{"enabled":false},"included_credits":{"enabled":false},"addons":{"enabled":false},"usage":{"enabled":true,"funding_modes":["prepaid_cash"],"default_funding_mode":"prepaid_cash","models":` + tc.models + `}}`)
			if _, err = p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) VALUES('llmrouter',1,$1,$2)`, u.ID, raw); err != nil {
				t.Fatal(err)
			}
			for _, r := range tc.rates {
				v, _ := json.Marshal(r)
				if _, err = p.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, r.ID, v); err != nil {
					t.Fatal(err)
				}
			}
			if tc.disabled {
				if _, err = p.Exec(ctx, `UPDATE products SET enabled=false WHERE id='llmrouter'`); err != nil {
					t.Fatal(err)
				}
			}
			g, err := New(p, a, b).Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "llmrouter", Models: []string{"model"}, TaskID: "task"})
			if tc.want {
				if err != nil || g.Token == "" {
					t.Fatalf("valid issuance: %v", err)
				}
				return
			}
			if !errors.Is(err, accounts.ErrForbidden) {
				t.Errorf("expected stable authorization denial, got %v", err)
			}
			var count int
			if err = p.QueryRow(ctx, `SELECT (SELECT count(*) FROM model_credentials)+(SELECT count(*) FROM audit_events WHERE action='model_credential.issued')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("issuance side effects %d: %v", count, err)
			}
		})
	}
}

func TestIssueWaitsForRateCatalogMutation(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://rate-issuance", Subject: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	accountsList, err := a.ListAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := billing.New(p, a, nil)
	profile, _ := billing.BillingPreset("llmrouter", "prepaid")
	profile.Version = 1
	profile.Usage.Models = []string{"model"}
	raw, _ := json.Marshal(profile)
	if _, err := p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) VALUES('llmrouter',1,'deployment',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	rate, _ := json.Marshal(billing.Rate{ID: "one", Model: "model", Asset: billing.CashAsset, Status: "published"})
	if _, err := p.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES('one','published',$1)`, rate); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:billing-rate-catalog',0))`); err != nil {
		t.Fatal(err)
	}
	s := New(p, a, b)
	req := Request{OriginProductID: "llmrouter", Models: []string{"model"}, TaskID: "task"}
	blocked, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	_, err = s.Issue(blocked, u.ID, accountsList[0].ID, req)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("issuance crossed catalog mutation: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Issue(ctx, u.ID, accountsList[0].ID, req); err != nil {
		t.Fatal(err)
	}
}

func TestExistingCredentialRejectsNewAmbiguousRate(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://rates", Subject: "actor"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, nil)
	profile, _ := billing.BillingPreset("llmrouter", "prepaid")
	profile.Version = 1
	profile.Usage.Models = []string{"model"}
	raw, _ := json.Marshal(profile)
	if _, e = p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) VALUES('llmrouter',1,'deployment',$1)`, raw); e != nil {
		t.Fatal(e)
	}
	add := func(id string) {
		r := billing.Rate{ID: id, Model: "model", Asset: billing.CashAsset, Status: "published"}
		v, _ := json.Marshal(r)
		if _, e := p.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, id, v); e != nil {
			t.Fatal(e)
		}
	}
	add("one")
	s := New(p, a, b)
	g, e := s.Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "llmrouter", Models: []string{"model"}, TaskID: "task"})
	if e != nil {
		t.Fatal(e)
	}
	add("two")
	if _, e = s.VerifyReceipt(ctx, g.Token, "model"); e != nil {
		t.Fatalf("historical receipt denied by rate ambiguity: %v", e)
	}
	if _, e = s.Verify(ctx, g.Token, "model"); !errors.Is(e, ErrCredential) {
		t.Fatalf("old grant bypassed new ambiguity: %v", e)
	}
}
