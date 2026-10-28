package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestTrendBucketsCover14UTCDays(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 30, 0, 0, time.UTC)
	days := trendBuckets(now)
	if len(days) != trendDays {
		t.Fatalf("bucket count %d", len(days))
	}
	if days[0].Date != "2026-10-16" || days[13].Date != "2026-10-29" {
		t.Fatalf("bucket range %s..%s", days[0].Date, days[13].Date)
	}
	if trendBuckets(now.In(time.FixedZone("CST", 8*3600)))[13].Date != "2026-10-29" {
		t.Fatal("non-UTC input must normalize to UTC")
	}
	from, to := dayWindow(now)
	if !from.Equal(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("window %s..%s", from, to)
	}
}

func TestRankFailedModelsOrdersAndCaps(t *testing.T) {
	rows := []FailedModel{
		{Model: "zero-fail", Calls: 10, FailedCalls: 0},
		{Model: "", Calls: 5, FailedCalls: 5},
		{Model: "b", Calls: 4, FailedCalls: 2},
		{Model: "a", Calls: 4, FailedCalls: 2},
		{Model: "c", Calls: 100, FailedCalls: 10},
		{Model: "d", Calls: 1, FailedCalls: 1},
		{Model: "e", Calls: 8, FailedCalls: 1},
		{Model: "f", Calls: 3, FailedCalls: 1},
	}
	got := rankFailedModels(rows)
	if len(got) != maxFailedModels {
		t.Fatalf("kept %d", len(got))
	}
	wantOrder := []string{"d", "a", "b", "f", "e"}
	for i, model := range wantOrder {
		if got[i].Model != model {
			t.Fatalf("rank %d = %s, want %s", i, got[i].Model, model)
		}
	}
	wantRate := []float64{1, 0.5, 0.5, 0.3333, 0.125}
	for i, rate := range wantRate {
		if got[i].FailureRate != rate {
			t.Fatalf("rank %d rate %v, want %v", i, got[i].FailureRate, rate)
		}
	}
}

func TestOperationTimeseriesAggregatesWindows(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	admin, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://timeseries.test", Subject: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	plain, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://timeseries.test", Subject: "plain"})
	if e != nil {
		t.Fatal(e)
	}
	roles, _ := json.Marshal([]string{"super_admin"})
	if _, e = p.Exec(ctx, "INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,$2,'test',$1)", admin.ID, roles); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	// admin and plain were created today; one user lands in the window, one
	// forty days back stays outside it.
	if _, e = p.Exec(ctx, "INSERT INTO users(id,name,email,created_at) VALUES('ts-old','old','old@test',$1)", today.AddDate(0, 0, -40)); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, "INSERT INTO users(id,name,email,created_at) VALUES('ts-new','new','new@test',$1)", yesterday.Add(-time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, "INSERT INTO accounts(id,kind,name) VALUES('ts-team','team','TS Team')"); e != nil {
		t.Fatal(e)
	}
	insertCall := func(id, model, status string, at time.Time) {
		t.Helper()
		call := billing.Call{CallID: id, AccountID: "ts-team", ActorID: admin.ID, Rate: billing.Rate{Model: model}, Status: status, ChargeAsset: "CREDIT/6"}
		raw, _ := json.Marshal(call)
		if _, e := p.Exec(ctx, "INSERT INTO billing_calls(id,account_id,request_digest,snapshot,created_at) VALUES($1,'ts-team','ts',$2,$3)", id, raw, at); e != nil {
			t.Fatal(e)
		}
	}
	insertCall("ts-c1", "m1", "SETTLED", today.Add(-time.Hour))
	insertCall("ts-c2", "m1", "REJECTED", today.Add(-2*time.Hour))
	insertCall("ts-c3", "m2", "SETTLED", today.Add(-3*time.Hour))
	insertCall("ts-c4", "m2", "REJECTED", yesterday.Add(-time.Hour))
	insertCall("ts-c5", "m1", "SETTLED", yesterday.Add(-2*time.Hour))
	insertOrder := func(id, refundStatus string) {
		t.Helper()
		snapshot := fmt.Sprintf("{\"refund_status\":%q}", refundStatus)
		if _, e := p.Exec(ctx, "INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,'ts-team',$2,$3,'ts',$4)", id, admin.ID, id, snapshot); e != nil {
			t.Fatal(e)
		}
	}
	insertOrder("ts-o1", "MANUAL_REVIEW")
	insertOrder("ts-o2", "MANUAL_REVIEW")
	insertOrder("ts-o3", "REJECTED")
	s := New(p, nil)
	v, e := s.OperationTimeseries(ctx, admin.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Days) != trendDays || v.Days[trendDays-1].Date != today.Format("2006-01-02") {
		t.Fatalf("days %d ending %s", len(v.Days), v.Days[trendDays-1].Date)
	}
	last := v.Days[trendDays-1]
	prev := v.Days[trendDays-2]
	if last.Calls != 3 || last.FailedCalls != 1 || prev.Calls != 2 || prev.FailedCalls != 1 {
		t.Fatalf("day buckets %+v / %+v", prev, last)
	}
	totalNew := int64(0)
	for _, d := range v.Days {
		totalNew += d.NewUsers
	}
	if totalNew != 3 {
		t.Fatalf("new users %d", totalNew)
	}
	if !v.FinanceScope || v.PendingRefunds != 2 {
		t.Fatalf("finance scope %v refunds %d", v.FinanceScope, v.PendingRefunds)
	}
	if len(v.TopFailedModels) != 2 || v.TopFailedModels[0].Model != "m2" || v.TopFailedModels[0].FailureRate != 1 || v.TopFailedModels[1].Model != "m1" || v.TopFailedModels[1].FailureRate != 0.3333 {
		t.Fatalf("models %+v", v.TopFailedModels)
	}
	if _, e = s.OperationTimeseries(ctx, plain.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("plain user got %v", e)
	}
}
