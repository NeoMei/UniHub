package payments

import "slices"

// ConfigurationMetadata contains only administration-safe installation metadata.
// It excludes credentials, key fingerprints and private filesystem locations.
type ConfigurationMetadata struct {
	AppID, MerchantID, SellerID, CertificateSerial, NotifyURL, ReturnURL                  string
	PlatformKeyIDs                                                                        []string
	PrivateKeyConfigured, APIv3KeyConfigured, PublicKeyConfigured, PlatformKeysConfigured bool
}

func (w *WeChat) ConfigurationMetadata() ConfigurationMetadata {
	ids := []string{}
	for id := range w.cfg.PlatformKeys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ConfigurationMetadata{AppID: w.cfg.AppID, MerchantID: w.cfg.MerchantID, CertificateSerial: w.cfg.CertificateSerial, NotifyURL: w.cfg.NotifyURL, PlatformKeyIDs: ids, PrivateKeyConfigured: w.cfg.PrivateKey != nil, APIv3KeyConfigured: len(w.cfg.APIv3Key) > 0, PlatformKeysConfigured: len(ids) > 0}
}
func (a *Alipay) ConfigurationMetadata() ConfigurationMetadata {
	return ConfigurationMetadata{AppID: a.cfg.AppID, SellerID: a.cfg.SellerID, NotifyURL: a.cfg.NotifyURL, ReturnURL: a.cfg.ReturnURL, PlatformKeyIDs: []string{}, PrivateKeyConfigured: a.cfg.PrivateKey != nil, PublicKeyConfigured: a.cfg.PublicKey != nil}
}
