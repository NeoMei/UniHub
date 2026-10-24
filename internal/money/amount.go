package money

import (
	"errors"
	"strconv"
)

const MaxAmount int64 = 1_000_000_000_000_000

var ErrInvalidAmount = errors.New("amount must be a positive integer string of at most 1000000000000000 CNY/6 units")

// Parse accepts integer micro-yuan strings, without rounding or floating point.
func Parse(s string) (int64, error) {
	if s == "" {
		return 0, ErrInvalidAmount
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, ErrInvalidAmount
		}
	}
	amount, err := strconv.ParseInt(s, 10, 64)
	if err != nil || amount <= 0 || amount > MaxAmount {
		return 0, ErrInvalidAmount
	}
	return amount, nil
}

// ParseRecharge additionally requires a whole number of fen (10,000 micro-yuan).
func ParseRecharge(s string) (int64, error) {
	amount, err := Parse(s)
	if err != nil {
		return 0, err
	}
	if amount%10_000 != 0 {
		return 0, errors.New("recharge amount must be a whole number of cents")
	}
	return amount, nil
}
