package httpapi

import (
	"testing"
	"time"
)

func TestProductModelProofBindsPrincipalAndExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	service := RuntimeService{
		Token:     "test-only-product-proof-signing-key",
		ProductID: "superwagie", InstanceID: "default",
		AllowedOps: []string{"model:authorize"},
	}
	claim := productModelPrincipal{
		Product: "superwagie", Instance: "default", Actor: "user1",
		Account: "team1", Key: "key1", KeyVersion: 3,
	}
	key := []byte("test-only-unihub-signing-key-32-bytes")
	proof, err := signProductModelProof(key, service.Token, claim, now)
	if err != nil {
		t.Fatal(err)
	}
	valid := func(candidate RuntimeService, target productModelPrincipal, at time.Time) bool {
		return validProductModelProof(proof, key, map[string]RuntimeService{"registered": candidate}, target, at)
	}
	if !valid(service, claim, now.Add(time.Second)) {
		t.Fatal("valid proof rejected")
	}
	secondPayer := claim
	secondPayer.Account = "team2"
	if valid(service, secondPayer, now) {
		t.Fatal("proof crossed a payer boundary")
	}
	secondKey := claim
	secondKey.KeyVersion++
	if valid(service, secondKey, now) {
		t.Fatal("proof survived Key rotation")
	}
	secondRuntime := service
	secondRuntime.ProductID = "agentwiki"
	if valid(secondRuntime, claim, now) {
		t.Fatal("proof crossed a product runtime boundary")
	}
	revoked := service
	revoked.AllowedOps = nil
	if valid(revoked, claim, now) {
		t.Fatal("revoked runtime can still authorize model calls")
	}
	if valid(service, claim, now.Add(productModelProofTTL)) {
		t.Fatal("expired product proof accepted")
	}
}
