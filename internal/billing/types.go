// Package billing owns subscriptions and model-credit metadata. Ledger quantities
// are authoritative; values crossing the API boundary are decimal integer strings.
package billing

import (
	"errors"
	"time"
)

var (
	ErrInvalid             = errors.New("billing: invalid input")
	ErrConflict            = errors.New("billing: idempotency or version conflict")
	ErrPending             = errors.New("billing: unresolved account command")
	ErrInsufficientCredits = errors.New("billing: insufficient model credits")
	ErrUnsupported         = errors.New("billing: cross-period changes are unsupported")
	ErrManualReview        = errors.New("billing: refund requires manual review")
)

const CreditAsset = "CREDIT/6"

type Offer struct {
	ID             string            `json:"id"`
	ProductID      string            `json:"product_id"`
	Name           string            `json:"name"`
	AccountKind    string            `json:"account_kind"` // personal, team, or both
	Kind           string            `json:"kind"`         // membership or addon
	Period         string            `json:"period"`       // month or year; empty for addon
	Price          string            `json:"price"`
	MonthlyCredit  string            `json:"monthly_credit"` // addon amount for addon
	Entitlements   map[string]string `json:"entitlements"`
	Rank           int               `json:"rank"`
	ValidityMonths int               `json:"validity_months"`
	UpgradeRule    string            `json:"upgrade_rule"` // remaining_term_prorata_v1
	Status         string            `json:"status"`       // draft or published
	Sandbox        bool              `json:"sandbox"`
	RefundRule     string            `json:"refund_rule"` // empty (manual) or unused_only_v1
}
type Subscription struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	ProductID   string    `json:"product_id"`
	Offer       Offer     `json:"offer"`
	Version     int64     `json:"version"`
	Anchor      time.Time `json:"anchor"`
	PaidThrough time.Time `json:"paid_through"`
	Status      string    `json:"status"`
}
type Order struct {
	ID                          string    `json:"id"`
	AccountID                   string    `json:"account_id"`
	ActorID                     string    `json:"actor_id"`
	ProductID                   string    `json:"product_id"`
	Kind                        string    `json:"kind"`
	Offer                       Offer     `json:"offer"`
	Amount                      string    `json:"amount"`
	ProductFunding              string    `json:"product_funding"`
	UnifiedFunding              string    `json:"unified_funding"`
	PaymentStatus               string    `json:"payment_status"`
	FulfillmentStatus           string    `json:"fulfillment_status"`
	SubscriptionID              string    `json:"subscription_id,omitempty"`
	Start                       time.Time `json:"start"`
	End                         time.Time `json:"end"`
	PeriodStart                 time.Time `json:"period_start"`
	PeriodEnd                   time.Time `json:"period_end"`
	GrantAmount                 string    `json:"grant_amount"`
	CreatedAt                   time.Time `json:"created_at"`
	RefundStatus                string    `json:"refund_status,omitempty"`
	RefundActorID               string    `json:"refund_actor_id,omitempty"`
	RefundKey                   string    `json:"refund_key,omitempty"`
	RefundReason                string    `json:"refund_reason,omitempty"`
	FundingAuthorizationVersion int64     `json:"funding_authorization_version,omitempty"`
	RenewalAuthorizationVersion int64     `json:"renewal_authorization_version,omitempty"`
}
type PurchaseRequest struct {
	OfferID string `json:"offer_id"`
	Key     string `json:"key"`
}
type Quote struct {
	ID                  string    `json:"id"`
	AccountID           string    `json:"account_id"`
	SubscriptionID      string    `json:"subscription_id"`
	SubscriptionVersion int64     `json:"subscription_version"`
	Offer               Offer     `json:"offer"`
	Amount              string    `json:"amount"`
	GrantAmount         string    `json:"grant_amount"`
	PeriodStart         time.Time `json:"period_start"`
	ExpiresAt           time.Time `json:"expires_at"`
	CreatedAt           time.Time `json:"created_at"`
}
type Authorization struct {
	ProductID       string    `json:"product_id"`
	Purpose         string    `json:"purpose"` // funding or renewal
	Enabled         bool      `json:"enabled"`
	Version         int64     `json:"version"`
	MaxPerOperation string    `json:"max_per_operation"`
	MaxTotal        string    `json:"max_total"`
	Used            string    `json:"used"`
	ExpiresAt       time.Time `json:"expires_at"`
	ActorID         string    `json:"actor_id"`
}
type Batch struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"account_id"`
	ProductID      string    `json:"product_id"`
	OrderID        string    `json:"order_id"`
	SubscriptionID string    `json:"subscription_id,omitempty"`
	SourceKey      string    `json:"source_key"`
	Kind           string    `json:"kind"`
	Original       string    `json:"original"`
	Available      string    `json:"available"`
	Reserved       string    `json:"reserved"`
	Consumed       string    `json:"consumed"`
	Expired        string    `json:"expired"`
	Withdrawn      string    `json:"withdrawn"`
	StartsAt       time.Time `json:"starts_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
	Frozen         bool      `json:"frozen"`
}
type Rational struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}
type Rate struct {
	ID         string   `json:"id"`
	Model      string   `json:"model"`
	Status     string   `json:"status"`
	Semantics  string   `json:"semantics"` // exclusive_v1: each count excludes every other count
	Input      Rational `json:"input"`
	Output     Rational `json:"output"`
	CacheRead  Rational `json:"cache_read"`
	CacheWrite Rational `json:"cache_write"`
	Reasoning  Rational `json:"reasoning"`
}
type Usage struct {
	Input      string `json:"input"`
	Output     string `json:"output"`
	CacheRead  string `json:"cache_read"`
	CacheWrite string `json:"cache_write"`
	Reasoning  string `json:"reasoning"`
}
type ReserveRequest struct {
	CallID          string `json:"call_id"`
	ActorID         string `json:"actor_id"`
	AccountID       string `json:"account_id"`
	OriginProductID string `json:"origin_product_id"`
	RequestDigest   string `json:"request_digest"`
	RateID          string `json:"rate_id"`
	Amount          string `json:"amount"`
	CredentialID    string `json:"credential_id,omitempty"`
}
type Allocation struct {
	BatchID string `json:"batch_id"`
	Amount  string `json:"amount"`
}
type Call struct {
	ServiceID          string       `json:"service_id"`
	CallID             string       `json:"call_id"`
	ActorID            string       `json:"actor_id"`
	AccountID          string       `json:"account_id"`
	OriginProductID    string       `json:"origin_product_id"`
	RequestDigest      string       `json:"request_digest"`
	Rate               Rate         `json:"rate"`
	Held               string       `json:"held"`
	Charged            string       `json:"charged"`
	PlatformDifference string       `json:"platform_difference"`
	Status             string       `json:"status"`
	Allocations        []Allocation `json:"allocations"`
	Usage              *Usage       `json:"usage,omitempty"`
	FinalDigest        string       `json:"final_digest,omitempty"`
	ProviderCost       string       `json:"provider_cost,omitempty"`
	CredentialID       string       `json:"credential_id,omitempty"`
}
type SettlementRequest struct {
	CallID       string `json:"call_id"`
	Usage        *Usage `json:"usage"`
	ProviderCost string `json:"provider_cost"`
}
type MaintenanceResult struct {
	Processed int `json:"processed"`
	Pending   int `json:"pending"`
}
