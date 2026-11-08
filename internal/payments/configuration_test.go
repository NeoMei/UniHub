package payments

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigurationMetadataContainsOnlyPublicInstallationFields(t *testing.T) {
	key, platform := testKey(t), testKey(t)
	wc, e := NewWeChat(wcConfig(key, platform, nil))
	if e != nil {
		t.Fatal(e)
	}
	ali, e := NewAlipay(aliConfig(key, platform, nil))
	if e != nil {
		t.Fatal(e)
	}
	w := wc.ConfigurationMetadata()
	a := ali.ConfigurationMetadata()
	if !w.PrivateKeyConfigured || !w.APIv3KeyConfigured || !w.PlatformKeysConfigured || len(w.PlatformKeyIDs) != 1 || w.AppID == "" || w.MerchantID == "" || w.CertificateSerial == "" || w.NotifyURL == "" {
		t.Fatal("incomplete WeChat metadata")
	}
	if !a.PrivateKeyConfigured || !a.PublicKeyConfigured || a.SellerID == "" || a.AppID == "" || a.NotifyURL == "" || a.ReturnURL == "" {
		t.Fatal("incomplete Alipay metadata")
	}
	raw, _ := json.Marshal([]ConfigurationMetadata{w, a})
	for _, secret := range []string{key.D.String(), platform.D.String(), string(wc.cfg.APIv3Key), "PRIVATE KEY"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret exposed")
		}
	}
	w.PlatformKeyIDs[0] = "changed"
	if wc.ConfigurationMetadata().PlatformKeyIDs[0] == "changed" {
		t.Fatal("mutable configuration metadata")
	}
}
