package billing

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unihub/internal/metering"
)

// MonthAnchor always derives from the original UTC anchor, preserving its day.
func MonthAnchor(anchor time.Time, offset int) time.Time {
	a := anchor.UTC()
	first := time.Date(a.Year(), a.Month()+time.Month(offset), 1, a.Hour(), a.Minute(), a.Second(), a.Nanosecond(), time.UTC)
	day := a.Day()
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, a.Hour(), a.Minute(), a.Second(), a.Nanosecond(), time.UTC)
}
func CurrentPeriod(anchor, paidThrough, now time.Time) (time.Time, time.Time, bool) {
	anchor = anchor.UTC()
	now = now.UTC()
	paidThrough = paidThrough.UTC()
	if now.Before(anchor) || !now.Before(paidThrough) {
		return time.Time{}, time.Time{}, false
	}
	m := (now.Year()-anchor.Year())*12 + int(now.Month()-anchor.Month())
	if now.Before(MonthAnchor(anchor, m)) {
		m--
	}
	start := MonthAnchor(anchor, m)
	end := MonthAnchor(anchor, m+1)
	if end.After(paidThrough) {
		end = paidThrough
	}
	return start, end, start.Before(end)
}
func integer(v string, positive bool) (int64, error) {
	if v == "" || strings.TrimSpace(v) != v {
		return 0, ErrInvalid
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 0 || (positive && n == 0) || strconv.FormatInt(n, 10) != v {
		return 0, ErrInvalid
	}
	return n, nil
}
func str(n int64) string { return strconv.FormatInt(n, 10) }

// CalculateCharge requires mutually exclusive counters, sums exact fractions,
// then rounds up once. Intermediate arithmetic never uses floats or int64 sums.
func CalculateCharge(r Rate, u Usage) (string, error) {
	if err := validateRate(r); err != nil {
		return "", err
	}
	if r.Semantics == "metered_v2" {
		if u.Input != "" || u.Output != "" || u.CacheRead != "" || u.CacheWrite != "" || u.Reasoning != "" {
			return "", ErrInvalid
		}
		if r.Metered == nil || u.Metered == nil {
			return "", ErrInvalid
		}
		v, err := metering.Calculate(*r.Metered, *u.Metered)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		q, rem := new(big.Int), new(big.Int)
		q.QuoRem(v.Num(), v.Denom(), rem)
		if rem.Sign() > 0 {
			q.Add(q, big.NewInt(1))
		}
		if !q.IsInt64() {
			return "", ErrInvalid
		}
		return q.String(), nil
	}
	if (normalizedAsset(r.Asset) != CreditAsset && normalizedAsset(r.Asset) != CashAsset) || r.Semantics != "exclusive_v1" {
		return "", ErrInvalid
	}
	if u.Metered != nil {
		return "", ErrInvalid
	}
	total := new(big.Rat)
	counts := []string{u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Reasoning}
	rates := []Rational{r.Input, r.Output, r.CacheRead, r.CacheWrite, r.Reasoning}
	for i, c := range counts {
		n, e := integer(c, false)
		if e != nil {
			return "", e
		}
		a, e := integer(rates[i].Numerator, false)
		if e != nil {
			return "", e
		}
		b, e := integer(rates[i].Denominator, true)
		if e != nil {
			return "", e
		}
		term := new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(n), big.NewInt(a)), big.NewInt(b))
		total.Add(total, term)
	}
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(total.Num(), total.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return "", ErrInvalid
	}
	return q.String(), nil
}
func prorate(delta int64, remaining, total time.Duration) (string, error) {
	if delta < 0 || remaining <= 0 || total <= 0 {
		return "", ErrInvalid
	}
	n := new(big.Int).Mul(big.NewInt(delta), big.NewInt(int64(remaining)))
	d := big.NewInt(int64(total))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n, d, r)
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return "", ErrInvalid
	}
	return q.String(), nil
}

// validateRate rejects ambiguous mixed contracts before publication or settlement.
func validateRate(r Rate) error {
	if normalizedAsset(r.Asset) != CreditAsset && normalizedAsset(r.Asset) != CashAsset {
		return ErrInvalid
	}
	if r.Semantics == "metered_v2" {
		if r.Metered == nil || r.Input != (Rational{}) || r.Output != (Rational{}) || r.CacheRead != (Rational{}) || r.CacheWrite != (Rational{}) || r.Reasoning != (Rational{}) {
			return ErrInvalid
		}
		if err := metering.ValidatePrice(*r.Metered); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return nil
	}
	if r.Semantics != "exclusive_v1" || r.Metered != nil {
		return ErrInvalid
	}
	for _, rate := range []Rational{r.Input, r.Output, r.CacheRead, r.CacheWrite, r.Reasoning} {
		if _, err := integer(rate.Numerator, false); err != nil {
			return err
		}
		if _, err := integer(rate.Denominator, true); err != nil {
			return err
		}
	}
	return nil
}
