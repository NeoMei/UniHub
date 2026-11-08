package httpapi

import (
	"context"
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/paymentconfig"
	"unihub/internal/payments"
)

type PaymentConfiguration interface {
	Status(context.Context, string) (paymentconfig.Status, error)
	RuntimeStatus(context.Context) (paymentconfig.Status, error)
	Update(context.Context, string, string, paymentconfig.UpdateRequest) (paymentconfig.Status, error)
}

func (s *api) paymentStatus(ctx context.Context) (paymentconfig.Status, error) {
	if s.options.PaymentConfiguration != nil {
		return s.options.PaymentConfiguration.RuntimeStatus(ctx)
	}
	mode := "disabled"
	if s.options.Sandbox {
		mode = "sandbox"
	}
	active := []string{}
	if s.options.Billing != nil {
		var e error
		active, e = s.options.Billing.PaymentProvidersContext(ctx)
		if e != nil {
			return paymentconfig.Status{}, e
		}
	}
	if len(active) > 0 {
		mode = "direct"
	}
	status := paymentconfig.Status{Mode: mode, StoreReady: true, Configured: len(active) > 0, Providers: []paymentconfig.ProviderStatus{}}
	for _, id := range []string{"wechat", "alipay"} {
		p := paymentconfig.ProviderStatus{ID: id, PlatformKeyIDs: []string{}, MissingFields: []string{}}
		for _, a := range active {
			if a == id {
				p.Enabled = true
				p.Configured = true
			}
		}
		metadataAvailable := false
		for _, provider := range s.options.PaymentProviders {
			if provider == nil {
				continue
			}
			if provider.ID() != id {
				continue
			}
			if metadata, ok := provider.(interface {
				ConfigurationMetadata() payments.ConfigurationMetadata
			}); ok {
				metadataAvailable = true
				v := metadata.ConfigurationMetadata()
				p.AppID = v.AppID
				p.MerchantID = v.MerchantID
				p.SellerID = v.SellerID
				p.CertificateSerial = v.CertificateSerial
				p.NotifyURL = v.NotifyURL
				p.ReturnURL = v.ReturnURL
				p.PlatformKeyIDs = v.PlatformKeyIDs
				p.PrivateKeyConfigured = v.PrivateKeyConfigured
				p.APIv3KeyConfigured = v.APIv3KeyConfigured
				p.PublicKeyConfigured = v.PublicKeyConfigured
				p.PlatformKeysConfigured = v.PlatformKeysConfigured
			}
		}
		if p.Configured && !metadataAvailable {
			p.MissingFields = append(p.MissingFields, "static_metadata_unavailable")
		}
		status.Providers = append(status.Providers, p)
	}
	return status, nil
}
func (s *api) adminPaymentRoutes(add func(string, action)) {
	add("GET /admin/v1/payment-providers", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := s.options.PlatformAdmin.Require(r.Context(), u.ID, "finance:read"); e != nil {
			return e
		}
		var status paymentconfig.Status
		var e error
		if s.options.PaymentConfiguration != nil {
			status, e = s.options.PaymentConfiguration.Status(r.Context(), u.ID)
		} else {
			status, e = s.paymentStatus(r.Context())
		}
		if e != nil {
			return e
		}
		return respond(w, 200, status)
	})
	add("PUT /admin/v1/payment-providers/{provider}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := s.options.PlatformAdmin.Require(r.Context(), u.ID, "finance:write"); e != nil {
			return e
		}
		if s.options.PaymentConfiguration == nil {
			return paymentconfig.ErrUnavailable
		}
		var body paymentconfig.UpdateRequest
		if e := decode(w, r, &body); e != nil {
			return e
		}
		status, e := s.options.PaymentConfiguration.Update(r.Context(), u.ID, r.PathValue("provider"), body)
		if e != nil {
			return e
		}
		return respond(w, 200, status)
	})
}
