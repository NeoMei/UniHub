package payments

import (
	"crypto/sha256"
	"encoding/hex"
)

// ConfigurationID freezes the commercial recipient and application. Keys and
// notification addresses may rotate without changing the recipient of funds.
func (w *WeChat) ConfigurationID() string {
	sum := sha256.Sum256([]byte("wechat\x00" + w.cfg.MerchantID + "\x00" + w.cfg.AppID))
	return hex.EncodeToString(sum[:])
}
func (a *Alipay) ConfigurationID() string {
	sum := sha256.Sum256([]byte("alipay\x00" + a.cfg.SellerID + "\x00" + a.cfg.AppID))
	return hex.EncodeToString(sum[:])
}
