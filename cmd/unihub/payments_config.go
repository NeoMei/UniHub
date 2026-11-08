package main

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unihub/internal/payments"
)

type merchantFile struct {
	WeChat *weChatFile `json:"wechat"`
	Alipay *alipayFile `json:"alipay"`
}
type weChatFile struct {
	AppID             string            `json:"app_id"`
	MerchantID        string            `json:"merchant_id"`
	CertificateSerial string            `json:"certificate_serial"`
	PrivateKeyFile    string            `json:"private_key_file"`
	APIv3KeyFile      string            `json:"api_v3_key_file"`
	PlatformKeyFiles  map[string]string `json:"platform_key_files"`
	NotifyURL         string            `json:"notify_url"`
}
type alipayFile struct {
	AppID          string `json:"app_id"`
	SellerID       string `json:"seller_id"`
	PrivateKeyFile string `json:"private_key_file"`
	PublicKeyFile  string `json:"public_key_file"`
	NotifyURL      string `json:"notify_url"`
	ReturnURL      string `json:"return_url"`
}

// Payment key paths are trusted deployment configuration. Do not return raw
// filesystem/PEM errors: startup logs must never disclose credentials or paths.
func paymentFile(path string, secret bool) ([]byte, error) {
	invalid := errors.New("payment key configuration is unavailable or unsafe")
	if !filepath.IsAbs(path) {
		return nil, invalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || secret && info.Mode().Perm()&0077 != 0 {
		return nil, invalid
	}
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return nil, invalid
	}
	return b, nil
}
func paymentPrivateKey(path string) (*rsa.PrivateKey, error) {
	b, err := paymentFile(path, true)
	if err != nil {
		return nil, err
	}
	k, err := payments.ParsePrivateKeyPEM(b)
	if err != nil {
		return nil, errors.New("invalid payment private key configuration")
	}
	return k, nil
}
func paymentPublicKey(path string) (*rsa.PublicKey, error) {
	b, err := paymentFile(path, false)
	if err != nil {
		return nil, err
	}
	k, err := payments.ParsePublicKeyPEM(b)
	if err != nil {
		return nil, errors.New("invalid payment public key configuration")
	}
	return k, nil
}
func readPaymentProviders(path string) ([]payments.Provider, error) {
	invalid := errors.New("invalid direct payment configuration")
	if !filepath.IsAbs(path) {
		return nil, invalid
	}
	raw, err := readBoundedConfig(path)
	if err != nil {
		return nil, invalid
	}
	var cfg merchantFile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(&struct{}{}) != io.EOF || cfg.WeChat == nil && cfg.Alipay == nil {
		return nil, invalid
	}
	out := []payments.Provider{}
	if c := cfg.WeChat; c != nil {
		private, err := paymentPrivateKey(c.PrivateKeyFile)
		if err != nil {
			return nil, err
		}
		apiKey, err := paymentFile(c.APIv3KeyFile, true)
		if err != nil {
			return nil, err
		}
		apiKey = []byte(strings.TrimSuffix(strings.TrimSuffix(string(apiKey), "\n"), "\r"))
		keys := make(map[string]*rsa.PublicKey, len(c.PlatformKeyFiles))
		for id, path := range c.PlatformKeyFiles {
			key, err := paymentPublicKey(path)
			if err != nil {
				return nil, err
			}
			keys[id] = key
		}
		p, err := payments.NewWeChat(payments.WeChatConfig{AppID: c.AppID, MerchantID: c.MerchantID, CertificateSerial: c.CertificateSerial, NotifyURL: c.NotifyURL, PrivateKey: private, APIv3Key: apiKey, PlatformKeys: keys})
		if err != nil {
			return nil, invalid
		}
		out = append(out, p)
	}
	if c := cfg.Alipay; c != nil {
		private, err := paymentPrivateKey(c.PrivateKeyFile)
		if err != nil {
			return nil, err
		}
		public, err := paymentPublicKey(c.PublicKeyFile)
		if err != nil {
			return nil, err
		}
		p, err := payments.NewAlipay(payments.AlipayConfig{AppID: c.AppID, SellerID: c.SellerID, NotifyURL: c.NotifyURL, ReturnURL: c.ReturnURL, PrivateKey: private, PublicKey: public})
		if err != nil {
			return nil, invalid
		}
		out = append(out, p)
	}
	return out, nil
}
