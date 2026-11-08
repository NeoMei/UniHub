package paymentconfig

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"slices"
	"strings"
	"unihub/internal/accounts"
	"unihub/internal/payments"
)

func validOrigin(origin string) bool {
	u, e := url.Parse(origin)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && (u.Port() == "" || u.Port() == "443") && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(origin, "#")
}
func (m *Manager) build(id string, c credential) (payments.Provider, error) {
	private, e := payments.ParsePrivateKeyPEM([]byte(c.PrivateKeyPEM))
	if e != nil {
		return nil, accounts.ErrInvalid
	}
	if id == "wechat" {
		keys := map[string]*rsa.PublicKey{}
		for id, raw := range c.PlatformPublicKeys {
			k, e := payments.ParsePublicKeyPEM([]byte(raw))
			if e != nil {
				return nil, accounts.ErrInvalid
			}
			keys[id] = k
		}
		p, e := payments.NewWeChat(payments.WeChatConfig{AppID: c.AppID, MerchantID: c.MerchantID, CertificateSerial: c.CertificateSerial, NotifyURL: m.origin + "/payments/wechat/notify", PrivateKey: private, APIv3Key: []byte(c.APIv3Key), PlatformKeys: keys})
		if e != nil {
			return nil, accounts.ErrInvalid
		}
		return p, nil
	}
	public, e := payments.ParsePublicKeyPEM([]byte(c.PublicKeyPEM))
	if e != nil {
		return nil, accounts.ErrInvalid
	}
	p, e := payments.NewAlipay(payments.AlipayConfig{AppID: c.AppID, SellerID: c.SellerID, NotifyURL: m.origin + "/payments/alipay/notify", ReturnURL: m.origin + "/", PrivateKey: private, PublicKey: public})
	if e != nil {
		return nil, accounts.ErrInvalid
	}
	return p, nil
}
func merge(id string, old credential, r UpdateRequest) (credential, error) {
	if r.ExpectedVersion == nil || *r.ExpectedVersion < 0 || r.Enabled == nil || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 2000 {
		return credential{}, accounts.ErrInvalid
	}
	if id != "wechat" && id != "alipay" {
		return credential{}, accounts.ErrInvalid
	}
	if id == "wechat" && (r.SellerID != "" || r.PublicKeyPEM != nil) || id == "alipay" && (r.MerchantID != "" || r.CertificateSerial != "" || r.APIv3Key != nil || len(r.PlatformKeyIDs) > 0 || len(r.PlatformPublicKeys) > 0) {
		return credential{}, accounts.ErrInvalid
	}
	if id == "wechat" && r.provided["seller_id"] {
		return credential{}, accounts.ErrInvalid
	}
	if id == "alipay" {
		for _, field := range []string{"merchant_id", "certificate_serial", "platform_key_ids", "platform_public_keys"} {
			if r.provided[field] {
				return credential{}, accounts.ErrInvalid
			}
		}
	}
	c := old
	c.Enabled = *r.Enabled
	c.AppID = r.AppID
	c.MerchantID = r.MerchantID
	c.SellerID = r.SellerID
	c.CertificateSerial = r.CertificateSerial
	for _, item := range []struct {
		input  *string
		target *string
	}{{r.PrivateKeyPEM, &c.PrivateKeyPEM}, {r.APIv3Key, &c.APIv3Key}, {r.PublicKeyPEM, &c.PublicKeyPEM}} {
		if item.input != nil {
			if strings.TrimSpace(*item.input) == "" || len(*item.input) > 64<<10 {
				return credential{}, accounts.ErrInvalid
			}
			*item.target = *item.input
		}
	}
	if id == "wechat" {
		if len(r.PlatformKeyIDs) > 16 || len(r.PlatformPublicKeys) > 16 {
			return credential{}, accounts.ErrInvalid
		}
		keys := map[string]string{}
		for _, id := range r.PlatformKeyIDs {
			if id == "" || len(id) > 128 {
				return credential{}, accounts.ErrInvalid
			}
			if _, ok := keys[id]; ok {
				return credential{}, accounts.ErrInvalid
			}
			keys[id] = old.PlatformPublicKeys[id]
		}
		for id, raw := range r.PlatformPublicKeys {
			if _, ok := keys[id]; !ok || strings.TrimSpace(raw) == "" || len(raw) > 64<<10 {
				return credential{}, accounts.ErrInvalid
			}
			keys[id] = raw
		}
		c.PlatformPublicKeys = keys
	}
	return c, nil
}
func (m *Manager) providerStatus(id string, c credential, configured bool) ProviderStatus {
	s := ProviderStatus{ID: id, Enabled: c.Enabled, Configured: configured, AppID: c.AppID, MerchantID: c.MerchantID, SellerID: c.SellerID, CertificateSerial: c.CertificateSerial, PlatformKeyIDs: []string{}, NotifyURL: m.origin + "/payments/" + id + "/notify", PrivateKeyConfigured: c.PrivateKeyPEM != "", APIv3KeyConfigured: c.APIv3Key != "", PublicKeyConfigured: c.PublicKeyPEM != "", PlatformKeysConfigured: len(c.PlatformPublicKeys) > 0, MissingFields: []string{}}
	for id := range c.PlatformPublicKeys {
		s.PlatformKeyIDs = append(s.PlatformKeyIDs, id)
	}
	slices.Sort(s.PlatformKeyIDs)
	if id == "alipay" {
		s.ReturnURL = m.origin + "/"
	}
	for _, v := range []struct {
		name    string
		present bool
	}{{"app_id", c.AppID != ""}, {"private_key_pem", s.PrivateKeyConfigured}} {
		if !v.present {
			s.MissingFields = append(s.MissingFields, v.name)
		}
	}
	if id == "wechat" {
		for _, v := range []struct {
			name    string
			present bool
		}{{"merchant_id", c.MerchantID != ""}, {"certificate_serial", c.CertificateSerial != ""}, {"api_v3_key", s.APIv3KeyConfigured}, {"platform_public_keys", s.PlatformKeysConfigured}} {
			if !v.present {
				s.MissingFields = append(s.MissingFields, v.name)
			}
		}
	} else {
		if c.SellerID == "" {
			s.MissingFields = append(s.MissingFields, "seller_id")
		}
		if !s.PublicKeyConfigured {
			s.MissingFields = append(s.MissingFields, "public_key_pem")
		}
	}
	return s
}

// Preserve the trusted RSA key rather than a time-limited certificate wrapper.
// Constructors have already validated the supplied certificate and key.
func normalizePublicKeys(c *credential) error {
	normalize := func(raw string) (string, error) {
		key, e := payments.ParsePublicKeyPEM([]byte(raw))
		if e != nil {
			return "", accounts.ErrInvalid
		}
		der, e := x509.MarshalPKIXPublicKey(key)
		if e != nil {
			return "", accounts.ErrInvalid
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
	}
	if c.PublicKeyPEM != "" {
		raw, e := normalize(c.PublicKeyPEM)
		if e != nil {
			return e
		}
		c.PublicKeyPEM = raw
	}
	for id, raw := range c.PlatformPublicKeys {
		value, e := normalize(raw)
		if e != nil {
			return e
		}
		c.PlatformPublicKeys[id] = value
	}
	return nil
}
