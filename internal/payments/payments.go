// Package payments implements direct official merchant payment protocols. Amounts
// use integer CNY/6 strings; callers retain durable payment and refund state.
package payments

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid payment input")
	ErrUnconfigured = errors.New("payment provider unconfigured")
	ErrVerification = errors.New("payment verification failed")
	ErrUnavailable  = errors.New("payment provider unavailable")
)

type Request struct {
	OrderID, Amount, Description string
	ExpiresAt                    time.Time
}
type Session struct {
	Kind      string    `json:"kind"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Result struct {
	OrderID, TransactionID, Amount, Currency, Status string
	PaidAt                                           time.Time
}
type RefundRequest struct{ OrderID, TransactionID, RefundID, Amount, TotalAmount, Reason string }
type RefundResult struct{ RefundID, TransactionID, Amount, Status string }
type Provider interface {
	ID() string
	MerchantID() string
	Create(context.Context, Request) (Session, error)
	Query(context.Context, string) (Result, error)
	Close(context.Context, string) error
	VerifyNotification(http.Header, []byte) (Result, error)
	Refund(context.Context, RefundRequest) (RefundResult, error)
	QueryRefund(context.Context, RefundRequest) (RefundResult, error)
}
