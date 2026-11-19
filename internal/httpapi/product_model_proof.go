package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
	"unihub/internal/productusers"
)

const productModelProofTTL = 2 * time.Minute
const productModelProofOperation = "model:authorize"
const productModelProofPrefix = "ump_"

func singleProductModelProof(r *http.Request) (string, bool) {
	values := r.Header.Values("X-UniHub-Product-Authorization")
	if len(values) != 1 {
		return "", false
	}
	return values[0], values[0] != ""
}

type productModelPrincipal struct {
	Product    string `json:"product_id"`
	Instance   string `json:"instance_id"`
	Actor      string `json:"actor_id"`
	Account    string `json:"account_id"`
	Key        string `json:"key_id"`
	KeyVersion int64  `json:"key_version"`
}

type productModelProof struct {
	productModelPrincipal
	ExpiresAt int64 `json:"expires_at"`
}

func (p productModelPrincipal) valid() bool {
	return p.Product != "" && p.Instance != "" && p.Actor != "" &&
		p.Account != "" && p.Key != "" && p.KeyVersion > 0
}

func productModelProofMAC(key []byte, runtimeCredential, payload string) []byte {
	signature := hmac.New(sha256.New, key)
	signature.Write([]byte("unihub-model-authorization-llmrouter-v1:"))
	credentialDigest := sha256.Sum256([]byte(runtimeCredential))
	signature.Write(credentialDigest[:])
	signature.Write([]byte(payload))
	return signature.Sum(nil)
}

func signProductModelProof(key []byte, runtimeCredential string, principal productModelPrincipal, now time.Time) (string, error) {
	if len(key) < 32 || len(runtimeCredential) < 32 || !principal.valid() {
		return "", accounts.ErrForbidden
	}
	proof := productModelProof{productModelPrincipal: principal, ExpiresAt: now.Add(productModelProofTTL).Unix()}
	body, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return productModelProofPrefix + payload + "." + base64.RawURLEncoding.EncodeToString(productModelProofMAC(key, runtimeCredential, payload)), nil
}

func validProductModelProof(token string, key []byte, services map[string]RuntimeService, principal productModelPrincipal, now time.Time) bool {
	if len(token) > 2048 || len(key) < 32 || !principal.valid() {
		return false
	}
	raw, ok := strings.CutPrefix(token, productModelProofPrefix)
	if !ok {
		return false
	}
	payload, signature, ok := strings.Cut(raw, ".")
	if !ok {
		return false
	}
	mac, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || len(mac) != sha256.Size {
		return false
	}
	for _, service := range services {
		if len(service.Token) < 32 || service.ProductID != principal.Product ||
			productusers.Instance(service.InstanceID) != principal.Instance ||
			!slices.Contains(service.AllowedOps, productModelProofOperation) ||
			!hmac.Equal(mac, productModelProofMAC(key, service.Token, payload)) {
			continue
		}
		body, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil {
			return false
		}
		var proof productModelProof
		if json.Unmarshal(body, &proof) != nil {
			return false
		}
		return proof.productModelPrincipal == principal && proof.ExpiresAt > now.Unix() &&
			proof.ExpiresAt <= now.Add(productModelProofTTL).Unix()
	}
	return false
}

func modelPrincipal(grant modelaccess.Claims) productModelPrincipal {
	return productModelPrincipal{
		Product: grant.OriginProductID, Instance: grant.InstanceID,
		Actor: grant.ActorID, Account: grant.AccountID,
		Key: grant.ID, KeyVersion: grant.CredentialVersion,
	}
}

func (s *api) hasProductModelRuntime(product, instance string) bool {
	if len(s.options.ProductModelSigningKey) < 32 || s.options.ModelAccess == nil {
		return false
	}
	for _, service := range s.options.ProductRuntime {
		if service.ProductID == product && productusers.Instance(service.InstanceID) == productusers.Instance(instance) &&
			len(service.Token) >= 32 && slices.Contains(service.AllowedOps, productModelProofOperation) {
			return true
		}
	}
	return false
}

func (s *api) authorizeProductModelGrant(grant *modelaccess.Claims, proof string) error {
	if grant.CreditScope != billing.ProductCreditScopeV1 {
		return nil
	}
	if grant.FundingProductID != grant.OriginProductID ||
		!validProductModelProof(proof, s.options.ProductModelSigningKey, s.options.ProductRuntime, modelPrincipal(*grant), time.Now()) {
		return accounts.ErrForbidden
	}
	grant.SourceVerified = true
	return nil
}

func (s *api) createProductModelProof(w http.ResponseWriter, r *http.Request, service RuntimeService) error {
	if s.options.ModelAccess == nil {
		return accounts.ErrForbidden
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	actor, _, err := s.runtimeActor(r, service)
	if err != nil {
		return err
	}
	grant, err := s.options.ModelAccess.VerifyReceiptForInstance(r.Context(), in.Token, "", service.InstanceID)
	if errors.Is(err, billing.ErrSubscriptionRequired) {
		fail(w, http.StatusForbidden, "product_membership_expired")
		return nil
	}
	if err != nil {
		return err
	}
	if grant.CreditScope != billing.ProductCreditScopeV1 || grant.OriginProductID != service.ProductID ||
		grant.FundingProductID != service.ProductID || grant.ActorID != actor.ID {
		return accounts.ErrForbidden
	}
	now := time.Now()
	token, err := signProductModelProof(s.options.ProductModelSigningKey, service.Token, modelPrincipal(grant), now)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return respond(w, http.StatusCreated, map[string]any{"token": token, "expires_at": now.Add(productModelProofTTL).Unix()})
}
