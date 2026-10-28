package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unihub/internal/accounts"
)

type identityFixtureProvider struct {
	mu                 sync.Mutex
	id                 ProviderIdentity
	secret             string
	writes, deliveries int
	failWrite          bool
	failCreate         bool
	creates            int
}

func (p *identityFixtureProvider) Capabilities(context.Context) IdentityCapabilities {
	return IdentityCapabilities{Directory: true, DisplayName: true, CreatePending: true, Activation: true, Recovery: true, RevokeSessions: true, Issuer: "fixture", Organization: "unihub", ProductionVerified: true}
}
func (p *identityFixtureProvider) Directory(context.Context, ListOptions) (Page[ProviderIdentity], error) {
	return Page[ProviderIdentity]{Items: []ProviderIdentity{p.id}, Total: 1, AsOf: time.Now()}, nil
}
func (p *identityFixtureProvider) ReadIdentity(_ context.Context, issuer, subject string) (ProviderIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if issuer != p.id.Issuer || subject != p.id.Subject {
		return ProviderIdentity{}, accounts.ErrNotFound
	}
	return p.id, nil
}
func (p *identityFixtureProvider) CreatePendingIdentity(context.Context, string, CreateIdentityInput) (ProviderIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	if p.failCreate {
		return ProviderIdentity{}, ErrIdentityUnknown
	}
	return p.id, nil
}
func (p *identityFixtureProvider) SetIdentityDisplayName(_ context.Context, _, _, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id.DisplayName = name
	return nil
}
func (p *identityFixtureProvider) DeliverRecovery(_ context.Context, _ ProviderIdentity, secret, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secret = secret
	p.deliveries++
	return nil
}
func (p *identityFixtureProvider) SetRecoveryPassword(context.Context, ProviderIdentity, string, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes++
	if p.failWrite {
		return ErrIdentityUnknown
	}
	return nil
}
func (p *identityFixtureProvider) RevokeIdentitySessions(context.Context, ProviderIdentity, string) error {
	return nil
}
func identityRecoveryFixture(t *testing.T) (*Store, []string, *identityFixtureProvider) {
	s, ids := coreFixture(t)
	p := &identityFixtureProvider{id: ProviderIdentity{Issuer: "fixture", Subject: "1", Email: "owner@example.test", EmailVerified: true, Manageable: true}}
	s.SetIdentityProvider(p)
	return s, ids, p
}
func TestRecoverySingleUseConcurrentAndNoAdminSecret(t *testing.T) {
	ctx := context.Background()
	s, ids, p := identityRecoveryFixture(t)
	input := RecoveryInput{OperationID: "request-recovery-1", Reason: "test verified recovery"}
	op, e := s.RequestRecovery(ctx, ids[0], ids[1], input)
	if e != nil || op.State != "succeeded" {
		t.Fatal(op, e)
	}
	raw, _ := json.Marshal(op)
	if strings.Contains(string(raw), p.secret) || strings.Contains(string(raw), p.id.Email) {
		t.Fatal("admin receives recovery secret or channel")
	}
	if _, e = s.RequestRecovery(ctx, ids[0], ids[1], input); e != nil || p.deliveries != 1 {
		t.Fatalf("duplicate delivery: %d %v", p.deliveries, e)
	}
	var wg sync.WaitGroup
	success := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.RedeemRecovery(ctx, p.secret, "CorrectHorse9Battery!", 1)
			success <- e == nil && v.State == "succeeded"
		}()
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 1 || p.writes != 1 {
		t.Fatalf("redeemed %d times, provider writes %d", n, p.writes)
	}
	var pending bool
	if e = s.pool.QueryRow(ctx, `SELECT pending FROM identity_revocations WHERE id=$1`, "recovery-"+op.ID).Scan(&pending); e != nil || pending {
		t.Fatal("successful reset remained blocked", e)
	}
}
func TestRecoveryUnknownNeverRepeatsPasswordWrite(t *testing.T) {
	ctx := context.Background()
	s, ids, p := identityRecoveryFixture(t)
	p.failWrite = true
	op, e := s.RequestRecovery(ctx, ids[0], ids[1], RecoveryInput{OperationID: "unknown-password-write", Reason: "test ambiguity"})
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.RedeemRecovery(ctx, p.secret, "CorrectHorse9Battery!", 1)
	if e != nil || v.State != "unknown" {
		t.Fatal(v, e)
	}
	if _, e = s.RedeemRecovery(ctx, p.secret, "DifferentPassword12!", 1); !errors.Is(e, ErrRecoveryInvalid) {
		t.Fatal("ambiguous reset repeated", e)
	}
	if p.writes != 1 {
		t.Fatal("provider repeated write")
	}
	var pending bool
	if e = s.pool.QueryRow(ctx, `SELECT pending FROM identity_revocations WHERE id=$1`, "recovery-"+op.ID).Scan(&pending); e != nil || !pending {
		t.Fatal("unknown reset not fail closed", e)
	}
}
func TestRecoveryUnverifiedAndProtectedTargetDenied(t *testing.T) {
	ctx := context.Background()
	s, ids, p := identityRecoveryFixture(t)
	p.id.EmailVerified = false
	if _, e := s.RequestRecovery(ctx, ids[0], ids[1], RecoveryInput{OperationID: "unverified-target", Reason: "test"}); !errors.Is(e, ErrRecoveryUnsupported) {
		t.Fatal(e)
	}
	if _, e := s.SetRoles(ctx, ids[0], ids[2], []string{"user_admin"}, 0, "test role"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RequestRecovery(ctx, ids[2], ids[0], RecoveryInput{OperationID: "protected-target", Reason: "test"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal(e)
	}
}
func TestIdentityCreateUnknownIsDurableAcrossRetry(t *testing.T) {
	ctx := context.Background()
	s, ids, p := identityRecoveryFixture(t)
	p.failCreate = true
	input := CreateIdentityInput{OperationID: "unknown-create-identity", Username: "new-user", Reason: "test"}
	v, e := s.CreateIdentity(ctx, ids[0], input)
	if e != nil || v.State != "unknown" {
		t.Fatal(v, e)
	}
	v, e = s.CreateIdentity(ctx, ids[0], input)
	if e != nil || v.State != "unknown" || p.creates != 1 {
		t.Fatalf("duplicate provider creation: %v %v creates=%d", v, e, p.creates)
	}
}
func TestIdentityMaterializationExactSubjectAndDisabled(t *testing.T) {
	ctx := context.Background()
	s, ids, p := identityRecoveryFixture(t)
	p.id.Subject = "not-yet-materialized"
	p.id.Disabled = true
	u, e := s.MaterializeIdentity(ctx, ids[0], MaterializeIdentityInput{Issuer: "fixture", Subject: p.id.Subject, Reason: "explicit selection"})
	if e != nil {
		t.Fatal(e)
	}
	var active bool
	if e = s.pool.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, u.ID).Scan(&active); e != nil || active {
		t.Fatal("disabled provider user enabled", e)
	}
	u2, e := s.MaterializeIdentity(ctx, ids[0], MaterializeIdentityInput{Issuer: "fixture", Subject: p.id.Subject, Reason: "retry selection"})
	if e != nil || u2.ID != u.ID {
		t.Fatal("materialization duplicated", e)
	}
}
func TestRecoveryHandlerOriginAndFragmentDelivery(t *testing.T) {
	s, _, _ := identityRecoveryFixture(t)
	h := s.RecoveryHandler("https://console.example.test")
	r := httptest.NewRequest("POST", "https://console.example.test/auth/recovery", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
	link := RecoveryURL("https://console.example.test", "abc-secret")
	if link != "https://console.example.test/auth/recovery#token=abc-secret" {
		t.Fatal(link)
	}
}

func (p *identityFixtureProvider) DeliverActivation(ctx context.Context, id ProviderIdentity, secret, request string) error {
	return p.DeliverRecovery(ctx, id, secret, request)
}
func (p *identityFixtureProvider) ActivateIdentity(context.Context, ProviderIdentity, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id.Disabled = false
	p.id.Pending = false
	p.id.EmailVerified = true
	return nil
}
func TestPendingActivationVerifiesUserChannelAndEnablesAfterSingleUse(t *testing.T) {
	ctx := context.Background()
	s, ids, p := identityRecoveryFixture(t)
	p.id.Disabled = true
	p.id.Pending = true
	p.id.EmailVerified = false
	if _, e := s.pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, ids[1]); e != nil {
		t.Fatal(e)
	}
	op, e := s.RequestActivation(ctx, ids[0], ids[1], RecoveryInput{OperationID: "activate-pending-user", Reason: "user requested activation"})
	if e != nil || op.State != "succeeded" {
		t.Fatal(op, e)
	}
	op, e = s.RedeemRecovery(ctx, p.secret, "CorrectHorse9Battery!", 1)
	if e != nil || op.State != "succeeded" {
		t.Fatal(op, e)
	}
	var active bool
	if e = s.pool.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, ids[1]).Scan(&active); e != nil || !active {
		t.Fatal("activated identity remains disabled", e)
	}
	if !p.id.EmailVerified || p.id.Pending || p.writes != 1 {
		t.Fatal("incorrect activation", p.id, p.writes)
	}
	if _, e = s.RedeemRecovery(ctx, p.secret, "CorrectHorse9Battery!", 1); !errors.Is(e, ErrRecoveryInvalid) {
		t.Fatal("activation credential reused", e)
	}
}
func TestBroadRevocationReportsUpstreamUnavailableAndKeepsLocalCutoff(t *testing.T) {
	ctx := context.Background()
	s, ids, _ := identityRecoveryFixture(t)
	s.SetIdentityProvider(nil)
	v, e := s.RevokeIdentitySessions(ctx, ids[0], ids[1], SessionRevocationInput{OperationID: "revoke-upstream-unavailable", ExpectedVersion: 1, Reason: "all device logout"})
	if e != nil || v.State != "unsupported" {
		t.Fatal(v, e)
	}
	if !strings.Contains(string(v.Result), "unsupported") || !strings.Contains(string(v.Result), "partial") {
		t.Fatal("falsely complete upstream logout", string(v.Result))
	}
	var cutoff time.Time
	if e = s.pool.QueryRow(ctx, `SELECT session_not_before FROM users WHERE id=$1`, ids[1]).Scan(&cutoff); e != nil || cutoff.Before(time.Now().Add(-time.Minute)) {
		t.Fatal("local cutoff not advanced", e)
	}
}
