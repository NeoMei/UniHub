package billing

import (
	"testing"
	"time"
)

func dt(s string) time.Time {
	x, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return x
}
func TestAnchoredCalendarClampsAndRestoresOriginalDay(t *testing.T) {
	anchor := dt("2024-01-31T13:20:00Z")
	if got := MonthAnchor(anchor, 1); !got.Equal(dt("2024-02-29T13:20:00Z")) {
		t.Fatalf("feb %v", got)
	}
	if got := MonthAnchor(anchor, 2); !got.Equal(dt("2024-03-31T13:20:00Z")) {
		t.Fatalf("march %v", got)
	}
	start, end, ok := CurrentPeriod(anchor, dt("2025-01-31T13:20:00Z"), dt("2024-12-15T00:00:00Z"))
	if !ok || !start.Equal(dt("2024-11-30T13:20:00Z")) || !end.Equal(dt("2024-12-31T13:20:00Z")) {
		t.Fatalf("%v %v %v", start, end, ok)
	}
	_, _, ok = CurrentPeriod(anchor, MonthAnchor(anchor, 12), MonthAnchor(anchor, 12))
	if ok {
		t.Fatal("grant at paid-through")
	}
}

func TestProductBillingCalendarUsesBeijingActivationDay(t *testing.T) {
	offer := Offer{CreditScope: ProductCreditScopeV1}
	anchor := dt("2024-01-30T18:30:00Z") // Jan 31 at 02:30 in Beijing.
	february := dt("2024-02-28T18:30:00Z")
	march := dt("2024-03-30T18:30:00Z")
	annualEnd := dt("2025-01-30T18:30:00Z")
	for _, sample := range []struct {
		months int
		want   time.Time
	}{{1, february}, {2, march}, {12, annualEnd}} {
		if got := offer.monthAnchor(anchor, sample.months); !got.Equal(sample.want) {
			t.Fatalf("period %d ends at %v, want %v", sample.months, got, sample.want)
		}
	}
	start, end, ok := offer.currentPeriod(anchor, annualEnd, february)
	if !ok || !start.Equal(february) || !end.Equal(march) {
		t.Fatalf("wrong February boundary: %v %v %v", start, end, ok)
	}
	if _, _, active := offer.currentPeriod(anchor, annualEnd, annualEnd); active {
		t.Fatal("the annual boundary is exclusive")
	}
}
func TestRateSumsFractionsBeforeCeiling(t *testing.T) {
	r := Rate{Semantics: "exclusive_v1", Input: Rational{"1", "3"}, Output: Rational{"1", "3"}, CacheRead: Rational{"0", "1"}, CacheWrite: Rational{"0", "1"}, Reasoning: Rational{"0", "1"}}
	got, e := CalculateCharge(r, Usage{Input: "1", Output: "1", CacheRead: "0", CacheWrite: "0", Reasoning: "0"})
	if e != nil || got != "1" {
		t.Fatalf("charge %s %v", got, e)
	}
	_, e = CalculateCharge(r, Usage{Input: "-1", Output: "1", CacheRead: "0", CacheWrite: "0", Reasoning: "0"})
	if e == nil {
		t.Fatal("negative usage accepted")
	}
	_, e = CalculateCharge(r, Usage{Input: "9223372036854775807", Output: "9223372036854775807", CacheRead: "0", CacheWrite: "0", Reasoning: "0"})
	if e != nil {
		t.Fatal(e)
	}
	r.Semantics = "inclusive"
	if _, e = CalculateCharge(r, Usage{}); e == nil {
		t.Fatal("ambiguous counts accepted")
	}
}
