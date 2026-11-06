package modelaccess

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func TestNativeScopeUpgradePreservesKeyAndDoesNotUseCentralModels(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://native-scope.test", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := billing.New(p, a, nil)
	b.ConfigureOperators([]string{u.ID})
	profile, _ := billing.BillingPreset("superwagie", "prepaid")
	profile.Usage.Models = []string{"old-model"}
	if _, err = b.SaveProfile(ctx, u.ID, "superwagie", profile); err != nil {
		t.Fatal(err)
	}
	rate := billing.Rate{ID: "old-rate", Model: "old-model", Asset: billing.CashAsset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "1"}, Output: billing.Rational{Numerator: "0", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}
	if _, err = b.PublishRate(ctx, u.ID, rate); err != nil {
		t.Fatal(err)
	}
	s := New(p, a, b)
	s.ConfigureProductUsers(productusers.New(p, productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}}, "https://native-scope.test", nil))
	k, err := s.CreateAPIKey(ctx, u.ID, as[0].ID, APIKeyRequest{Name: "Existing", OriginProductID: "superwagie", InstanceID: "default", Models: []string{"old-model"}, MaxTotal: "100", MaxPerCall: "20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.VerifyForInstance(ctx, k.Token, "new-native-model", "default"); !errors.Is(err, ErrCredential) {
		t.Fatalf("selected key admitted new model: %v", err)
	}
	up, err := s.UpgradeAPIKeyScope(ctx, u.ID, as[0].ID, k.ID, APIKeyScopeRequest{OriginProductID: "superwagie", InstanceID: "default", ModelScope: ScopeLLMRouterAvailable})
	if err != nil {
		t.Fatal(err)
	}
	if up.ID != k.ID || up.ActorID != k.ActorID || up.AccountID != k.AccountID || up.MaxTotal != "100" || up.MaxPerCall != "20" || up.Token != "" || up.CredentialVersion != 2 || len(up.Models) != 1 {
		t.Fatalf("upgrade changed key contract: %+v", up)
	}
	again, err := s.UpgradeAPIKeyScope(ctx, u.ID, as[0].ID, k.ID, APIKeyScopeRequest{OriginProductID: "superwagie", InstanceID: "default", ModelScope: ScopeLLMRouterAvailable})
	if err != nil || again.CredentialVersion != 2 {
		t.Fatalf("replay: %+v %v", again, err)
	}
	grant, err := s.VerifyWireForInstance(ctx, k.Token, "new-native-model", "default", "ocr.document.create", false)
	if err != nil || grant.ModelScope != ScopeLLMRouterAvailable || grant.PricingSource != PricingLLMRouterNative {
		t.Fatalf("dynamic grant: %+v %v", grant, err)
	}
	var events int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE operation_id=$1 AND action='model_credential.scope_upgraded'`, k.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("audit count=%d err=%v", events, err)
	}
	if _, err = s.CreateAPIKey(ctx, u.ID, as[0].ID, APIKeyRequest{Name: "All", OriginProductID: "superwagie", InstanceID: "default", ModelScope: ScopeLLMRouterAvailable}); err != nil {
		t.Fatalf("empty history dynamic creation: %v", err)
	}
	if err = s.RevokeAPIKey(ctx, u.ID, as[0].ID, k.ID, "superwagie", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.VerifyForInstance(ctx, k.Token, "new-native-model", "default"); !errors.Is(err, ErrCredential) {
		t.Fatalf("revoked key admitted: %v", err)
	}
}
