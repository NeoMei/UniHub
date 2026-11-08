package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func paymentTestEnvironment() map[string]string {
	return map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:3068", "OIDC_ISSUER": "http://localhost:8000", "OIDC_AUDIENCE": "unihub", "PAYMENT_MODE": "direct"}
}
func writePaymentTestConfiguration(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "private.pem")
	if err = os.WriteFile(private, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(dir, "public.pem")
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(public, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}), 0644); err != nil {
		t.Fatal(err)
	}
	apiKey := filepath.Join(dir, "apiv3.key")
	if err = os.WriteFile(apiKey, []byte("0123456789abcdef0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "payments.json")
	config := map[string]any{
		"wechat": map[string]any{"app_id": "wx1234567890abcdef", "merchant_id": "1900001234", "certificate_serial": "0123456789ABCDEF", "private_key_file": private, "api_v3_key_file": apiKey, "platform_key_files": map[string]string{"PUB_KEY_ID_123": public}, "notify_url": "https://billing.example.test/payments/wechat/notify"},
		"alipay": map[string]any{"app_id": "2026110800000001", "seller_id": "2088000000000001", "private_key_file": private, "public_key_file": public, "notify_url": "https://billing.example.test/payments/alipay/notify", "return_url": "https://billing.example.test/payment-return"},
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	return configPath, private
}
func TestDirectPaymentsRequireCompleteExplicitConfiguration(t *testing.T) {
	env := paymentTestEnvironment()
	read := func() (config, error) { return readConfig(func(k string) string { return env[k] }) }
	if _, err := read(); err == nil {
		t.Fatal("direct mode accepted without merchant configuration")
	}
	file, _ := writePaymentTestConfiguration(t)
	env["PAYMENT_PROVIDERS_FILE"] = file
	c, err := read()
	if err != nil {
		t.Fatal(err)
	}
	if c.Sandbox || len(c.PaymentProviders) != 2 {
		t.Fatal("configured direct channels were not enabled independently of sandbox")
	}
	if c.PaymentProviders[0].ID() != "wechat" || c.PaymentProviders[1].ID() != "alipay" {
		t.Fatal("wrong payment adapters")
	}
	for _, mode := range []string{"disabled", "sandbox"} {
		env["PAYMENT_MODE"] = mode
		if _, err = read(); err == nil {
			t.Fatal("merchant configuration accepted outside direct mode")
		}
	}
}
func TestPaymentSecretsRequireProtectedRegularAbsoluteFiles(t *testing.T) {
	file, private := writePaymentTestConfiguration(t)
	env := paymentTestEnvironment()
	env["PAYMENT_PROVIDERS_FILE"] = file
	if err := os.Chmod(private, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := readConfig(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("world-readable payment key accepted")
	}
	if strings.Contains(err.Error(), private) || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatal("configuration error disclosed secret location or material")
	}
	if err = os.Chmod(private, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(private, []byte("invalid private key material"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readConfig(func(k string) string { return env[k] }); err == nil {
		t.Fatal("invalid payment key accepted")
	}
}
func TestPaymentConfigRejectsUnknownFieldsAndEmptyChannels(t *testing.T) {
	for _, body := range []string{`{}`, `{"unexpected":true}`, `{"alipay":{}}`, `{"wechat":{}}`, `{"alipay":{"private_key_file":"relative.pem"}}`} {
		dir := t.TempDir()
		file := filepath.Join(dir, "payments.json")
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		env := paymentTestEnvironment()
		env["PAYMENT_PROVIDERS_FILE"] = file
		if _, err := readConfig(func(k string) string { return env[k] }); err == nil {
			t.Fatal("invalid payment configuration accepted")
		}
	}
}

func TestManagedPaymentsAllowNoMerchantsAndRequireProtectedKey(t *testing.T) {
	env := paymentTestEnvironment()
	env["PUBLIC_URL"] = "https://hub.example"
	env["OIDC_CLIENT_SECRET"] = "fixture-secret"
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "payment-config.key")
	if e := os.WriteFile(path, make([]byte, 32), 0600); e != nil {
		t.Fatal(e)
	}
	env["PAYMENT_CONFIG_KEY_FILE"] = path
	read := func() (config, error) { return readConfig(func(k string) string { return env[k] }) }
	c, e := read()
	if e != nil || c.Sandbox || len(c.PaymentProviders) != 0 || c.PaymentConfigKeyFile != path {
		t.Fatal("empty managed configuration", e)
	}
	env["PAYMENT_PROVIDERS_FILE"] = "/unused"
	if _, e = read(); e == nil {
		t.Fatal("dual authority accepted")
	}
	delete(env, "PAYMENT_PROVIDERS_FILE")
	env["PUBLIC_URL"] = "http://localhost:8088"
	if _, e = read(); e == nil {
		t.Fatal("unsafe callback origin")
	}
	env["PUBLIC_URL"] = "https://hub.example"
	for _, mode := range []string{"disabled", "sandbox"} {
		env["PAYMENT_MODE"] = mode
		if _, e = read(); e == nil {
			t.Fatal("key outside direct")
		}
	}
	env["PAYMENT_MODE"] = "direct"
	os.Remove(path)
	if _, e = read(); e == nil {
		t.Fatal("lost key accepted")
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("key generated")
	}
}
