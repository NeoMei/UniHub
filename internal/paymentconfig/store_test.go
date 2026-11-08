package paymentconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/platformadmin"
	"unihub/internal/testutil"
)

func TestManagedConfigurationDurabilityAndRecovery(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "fixture", Subject: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	admin := platformadmin.New(pool, nil)
	if e = admin.Bootstrap(ctx, "fixture", u.ID, "bootstrap fixture"); e != nil {
		t.Fatal(e)
	}
	keyPath := filepath.Join(paymentTestDir(t), "key")
	key := make([]byte, 32)
	rand.Read(key)
	os.WriteFile(keyPath, key, 0600)
	m, e := New(pool, admin, keyPath, "https://hub.example")
	if e != nil {
		t.Fatal(e)
	}
	status, e := m.Status(ctx, u.ID)
	if e != nil || status.Version != 0 || len(status.Providers) != 2 || status.Configured {
		t.Fatal(status, e)
	}
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	pub, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	v := int64(0)
	enabled := true
	r := UpdateRequest{ExpectedVersion: &v, Enabled: &enabled, Reason: "fixture merchant", AppID: "app1", SellerID: "seller1", PrivateKeyPEM: &private, PublicKeyPEM: &public}
	status, e = m.Update(ctx, u.ID, "alipay", r)
	if e != nil || status.Version != 1 || !status.Configured {
		t.Fatal(status, e)
	}
	raw, _ := json.Marshal(status)
	if bytes.Contains(raw, []byte("BEGIN")) || bytes.Contains(raw, []byte(private)) {
		t.Fatal("secret in status")
	}
	var stored []byte
	if e = pool.QueryRow(ctx, `SELECT encrypted_payload FROM payment_provider_revisions LIMIT 1`).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(stored, []byte("PRIVATE KEY")) || bytes.Contains(stored, []byte("seller1")) {
		t.Fatal("plaintext persisted")
	}
	if _, e = m.Update(ctx, u.ID, "alipay", r); !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("stale update", e)
	}
	v = 1
	r.PrivateKeyPEM = nil
	r.PublicKeyPEM = nil
	r.AppID = "app2"
	status, e = m.Update(ctx, u.ID, "alipay", r)
	if e != nil {
		t.Fatal(e)
	}
	second, e := New(pool, admin, keyPath, "https://hub.example")
	if e != nil {
		t.Fatal(e)
	}
	active, recovery, e := second.Providers(ctx)
	if e != nil || len(active) != 1 || len(recovery) != 2 {
		t.Fatal(len(active), len(recovery), e)
	}
	v = 2
	enabled = false
	status, e = m.Update(ctx, u.ID, "alipay", r)
	if e != nil || status.Configured || !status.Providers[1].Configured {
		t.Fatal(status, e)
	}
	active, recovery, e = second.Providers(ctx)
	if e != nil || len(active) != 0 || len(recovery) != 2 {
		t.Fatal("disabled recovery lost", len(active), len(recovery), e)
	}
	v = 3
	empty := ""
	r.PrivateKeyPEM = &empty
	if _, e = m.Update(ctx, u.ID, "alipay", r); !errors.Is(e, accounts.ErrInvalid) {
		t.Fatal("empty accepted", e)
	}
	r.PrivateKeyPEM = nil
	if _, e = m.Update(ctx, "ordinary", "alipay", r); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("authority", e)
	}
	if _, e = pool.Exec(ctx, `CREATE FUNCTION reject_payment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit failure'; END $$; CREATE TRIGGER reject_payment_audit BEFORE INSERT ON platform_admin_audit FOR EACH ROW EXECUTE FUNCTION reject_payment_audit()`); e != nil {
		t.Fatal(e)
	}
	enabled = true
	if _, e = m.Update(ctx, u.ID, "alipay", r); e == nil {
		t.Fatal("audit failure committed")
	}
	status, e = m.Status(ctx, u.ID)
	if e != nil || status.Version != 3 || status.Configured {
		t.Fatal(status, e)
	}
	os.Remove(keyPath)
	if _, _, e = second.Providers(ctx); !errors.Is(e, ErrUnavailable) {
		t.Fatal("key loss failed open", e)
	}
	if _, e = os.Stat(keyPath); !os.IsNotExist(e) {
		t.Fatal("key regenerated")
	}
	os.WriteFile(keyPath, make([]byte, 32), 0600)
	if _, _, e = second.Providers(ctx); !errors.Is(e, ErrUnavailable) {
		t.Fatal("wrong key accepted", e)
	}
}

func TestKeyFileRejectsUnsafeInputs(t *testing.T) {
	dir := paymentTestDir(t)
	path := filepath.Join(dir, "key")
	os.WriteFile(path, make([]byte, 32), 0600)
	if _, e := readKey(path); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	os.Symlink(path, link)
	if _, e := readKey(link); e == nil {
		t.Fatal("symlink")
	}
	parentLink := filepath.Join(paymentTestDir(t), "parent")
	if e := os.Symlink(dir, parentLink); e != nil {
		t.Fatal(e)
	}
	if _, e := readKey(filepath.Join(parentLink, "key")); e == nil {
		t.Fatal("parent symlink")
	}
	hardlink := filepath.Join(dir, "hardlink")
	if e := os.Link(path, hardlink); e != nil {
		t.Fatal(e)
	}
	if _, e := readKey(path); e == nil {
		t.Fatal("hard link")
	}
	os.Remove(hardlink)
	os.Chmod(path, 0644)
	if _, e := readKey(path); e == nil {
		t.Fatal("permissions")
	}
	os.Chmod(path, 0600)
	os.WriteFile(path, make([]byte, 33), 0600)
	if _, e := readKey(path); e == nil {
		t.Fatal("length")
	}
}

func TestManagedWeChatRotationConcurrentCASAndAuditRedaction(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	u, e := accounts.New(pool).EnsureIdentity(ctx, accounts.Identity{Issuer: "fixture", Subject: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	admin := platformadmin.New(pool, nil)
	if e = admin.Bootstrap(ctx, "fixture", u.ID, "bootstrap"); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(paymentTestDir(t), "key")
	if e = os.WriteFile(path, make([]byte, 32), 0600); e != nil {
		t.Fatal(e)
	}
	m, e := New(pool, admin, path, "https://hub.example")
	if e != nil {
		t.Fatal(e)
	}
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	version := int64(0)
	enabled := true
	apiKey := "0123456789abcdef0123456789abcdef"
	r := UpdateRequest{ExpectedVersion: &version, Enabled: &enabled, Reason: "WeChat fixture", AppID: "wxapp", MerchantID: "merchant", CertificateSerial: "serial", PlatformKeyIDs: []string{"PUB_KEY_ID_1"}, PrivateKeyPEM: &private, APIv3Key: &apiKey, PlatformPublicKeys: map[string]string{"PUB_KEY_ID_1": public}}
	status, e := m.Update(ctx, u.ID, "wechat", r)
	if e != nil || !status.Providers[0].Configured || !status.Providers[0].APIv3KeyConfigured {
		t.Fatal(status, e)
	}
	active, _, e := m.Providers(ctx)
	if e != nil {
		t.Fatal(e)
	}
	identity := active[0].(interface{ ConfigurationID() string }).ConfigurationID()
	version = 1
	r.PrivateKeyPEM = nil
	r.APIv3Key = nil
	r.PlatformPublicKeys = nil
	r.CertificateSerial = "rotated_serial"
	if _, e = m.Update(ctx, u.ID, "wechat", r); e != nil {
		t.Fatal("metadata rotation retained secrets", e)
	}
	other, e := New(pool, admin, path, "https://hub.example")
	if e != nil {
		t.Fatal(e)
	}
	active, recovery, e := other.Providers(ctx)
	if e != nil || len(recovery) != 1 || active[0].(interface{ ConfigurationID() string }).ConfigurationID() != identity {
		t.Fatal("rotation changed identity", e)
	}
	var audit string
	if e = pool.QueryRow(ctx, `SELECT string_agg(detail::text,'') FROM platform_admin_audit WHERE action='finance.payment-provider'`).Scan(&audit); e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{private, public, apiKey, "PRIVATE KEY"} {
		if strings.Contains(audit, secret) {
			t.Fatal("credential leaked to audit")
		}
	}
	version = 2
	bad := "invalid-private-credential-fixture"
	r.PrivateKeyPEM = &bad
	if _, e = m.Update(ctx, u.ID, "wechat", r); !errors.Is(e, accounts.ErrInvalid) || strings.Contains(e.Error(), bad) {
		t.Fatal("credential validation/redaction", e)
	}
	r.PrivateKeyPEM = nil
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, manager := range []*Manager{m, other} {
		go func(manager *Manager) { <-start; _, e := manager.Update(ctx, u.ID, "wechat", r); errs <- e }(manager)
	}
	close(start)
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-errs
		if e == nil {
			successes++
		} else if errors.Is(e, accounts.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("CAS accepted concurrent writers", successes, conflicts)
	}
	// Version is authenticated: moving ciphertext to a different revision fails closed.
	if _, e = pool.Exec(ctx, `UPDATE payment_provider_revisions SET encrypted_payload=(SELECT encrypted_payload FROM payment_provider_revisions WHERE version=1) WHERE version=3`); e != nil {
		t.Fatal(e)
	}
	if _, e = other.RuntimeStatus(ctx); !errors.Is(e, ErrUnavailable) {
		t.Fatal("AAD mismatch accepted", e)
	}
}

func paymentTestDir(t *testing.T) string {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return dir
}
