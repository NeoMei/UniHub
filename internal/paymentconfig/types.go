// Package paymentconfig owns encrypted merchant revisions and current payment authority.
package paymentconfig

import (
	"errors"
)

var ErrUnavailable = errors.New("payment_configuration_unavailable")

type Status struct {
	Version    int64            `json:"version"`
	Mode       string           `json:"mode"`
	Managed    bool             `json:"managed"`
	StoreReady bool             `json:"store_ready"`
	Configured bool             `json:"configured"`
	Providers  []ProviderStatus `json:"providers"`
}
type ProviderStatus struct {
	ID                     string   `json:"id"`
	Enabled                bool     `json:"enabled"`
	Configured             bool     `json:"configured"`
	AppID                  string   `json:"app_id"`
	MerchantID             string   `json:"merchant_id"`
	SellerID               string   `json:"seller_id"`
	CertificateSerial      string   `json:"certificate_serial"`
	PlatformKeyIDs         []string `json:"platform_key_ids"`
	NotifyURL              string   `json:"notify_url"`
	ReturnURL              string   `json:"return_url"`
	PrivateKeyConfigured   bool     `json:"private_key_configured"`
	APIv3KeyConfigured     bool     `json:"api_v3_key_configured"`
	PublicKeyConfigured    bool     `json:"public_key_configured"`
	PlatformKeysConfigured bool     `json:"platform_keys_configured"`
	MissingFields          []string `json:"missing_fields"`
}

// Secrets are write-only updates. Public metadata replaces the current metadata.
// Omitted secrets retain their values; supplied empty values are invalid.
type UpdateRequest struct {
	provided           map[string]bool
	ExpectedVersion    *int64            `json:"expected_version"`
	Reason             string            `json:"reason"`
	Enabled            *bool             `json:"enabled"`
	AppID              string            `json:"app_id,omitempty"`
	MerchantID         string            `json:"merchant_id,omitempty"`
	SellerID           string            `json:"seller_id,omitempty"`
	CertificateSerial  string            `json:"certificate_serial,omitempty"`
	PlatformKeyIDs     []string          `json:"platform_key_ids,omitempty"`
	PrivateKeyPEM      *string           `json:"private_key_pem,omitempty"`
	APIv3Key           *string           `json:"api_v3_key,omitempty"`
	PublicKeyPEM       *string           `json:"public_key_pem,omitempty"`
	PlatformPublicKeys map[string]string `json:"platform_public_keys,omitempty"`
}

// credential is serialized only inside authenticated encryption.
type credential struct {
	Enabled            bool              `json:"enabled"`
	AppID              string            `json:"app_id"`
	MerchantID         string            `json:"merchant_id"`
	SellerID           string            `json:"seller_id"`
	CertificateSerial  string            `json:"certificate_serial"`
	PrivateKeyPEM      string            `json:"private_key_pem"`
	APIv3Key           string            `json:"api_v3_key"`
	PublicKeyPEM       string            `json:"public_key_pem"`
	PlatformPublicKeys map[string]string `json:"platform_public_keys"`
}
