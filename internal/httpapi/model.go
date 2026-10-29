package httpapi

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
	"unihub/internal/productusers"
)

func (s *api) modelRoutes(m *http.ServeMux, add func(string, action)) {
	s.apiKeyRoutes(add)
	a := s.options.ModelAccess
	add("GET /v1/products/{product}/model-consistency", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product := r.PathValue("product")
		if origin := originProduct(r); origin != "" && product != origin {
			return accounts.ErrForbidden
		}
		query, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil {
			return billing.ErrInvalid
		}
		if query.Has("account_id") || query.Has("funding_mode") {
			if len(query) != 2 || len(query["account_id"]) != 1 || len(query["funding_mode"]) != 1 || strings.TrimSpace(query.Get("account_id")) == "" || (query.Get("funding_mode") != billing.FundingCredits && query.Get("funding_mode") != billing.FundingPrepaidCash) {
				return billing.ErrInvalid
			}
			_, instance, e := apiKeyScope(r, product, "")
			if e != nil {
				return e
			}
			out, e := a.ConsistencyForAccount(r.Context(), u.ID, query.Get("account_id"), product, instance, query.Get("funding_mode"))
			if e != nil {
				return e
			}
			return respond(w, 200, out)
		}
		profile, e := s.options.Billing.Profile(r.Context(), product)
		if e != nil {
			return e
		}
		asset := billing.CreditAsset
		if profile.Usage.DefaultFundingMode == billing.FundingPrepaidCash {
			asset = billing.CashAsset
		}
		out, e := a.Consistency(r.Context(), product, asset)
		if e != nil {
			return e
		}
		return respond(w, 200, out)
	})
	b := s.options.Billing
	add("POST /v1/accounts/{account}/model-credentials", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body modelaccess.Request
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if product := originProduct(r); product != "" {
			if body.OriginProductID != "" && body.OriginProductID != product {
				return accounts.ErrForbidden
			}
			body.OriginProductID = product
		}
		v, e := a.Issue(r.Context(), u.ID, r.PathValue("account"), body)
		if e != nil {
			return e
		}
		return respond(w, 201, v)
	})
	add("DELETE /v1/accounts/{account}/model-credentials/{credential}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if e := a.RevokeForProduct(r.Context(), u.ID, r.PathValue("account"), r.PathValue("credential"), originProduct(r)); e != nil {
			return e
		}
		w.WriteHeader(204)
		return nil
	})
	m.HandleFunc("POST /internal/v1/model-credentials/verify", s.serviceAuthenticated(func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			WireOperation string `json:"wire_operation"`
			Token         string `json:"token"`
			Model         string `json:"model"`
			Purpose       string `json:"purpose"`
			InstanceID    string `json:"instance_id"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		var v modelaccess.Claims
		var e error
		if body.Purpose == "result_read" {
			v, e = a.VerifyResultReadForInstance(r.Context(), body.Token, body.Model, body.InstanceID, body.WireOperation)
			if e == nil {
				e = s.authorizeResultReadActor(r, v)
			}
		} else if body.Purpose != "" && body.Purpose != "receipt" {
			return modelaccess.ErrCredential
		} else if body.WireOperation != "" {
			v, e = a.VerifyWireForInstance(r.Context(), body.Token, body.Model, body.InstanceID, body.WireOperation, body.Purpose == "receipt")
		} else if body.Purpose == "receipt" {
			v, e = a.VerifyReceiptForInstance(r.Context(), body.Token, body.Model, body.InstanceID)
		} else {
			v, e = a.VerifyForInstance(r.Context(), body.Token, body.Model, body.InstanceID)
		}
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	}))
	m.HandleFunc("POST /internal/v1/calls/reserve", s.serviceAuthenticated(func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			WireOperation string `json:"wire_operation"`
			Credential    string `json:"credential"`
			CallID        string `json:"call_id"`
			RequestDigest string `json:"request_digest"`
			RateID        string `json:"rate_id"`
			Amount        string `json:"amount"`
			TaskID        string `json:"task_id"`
			InstanceID    string `json:"instance_id"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		// Existing video reservations keep their immutable idempotency contract.
		// Receipt authority proves the credential scope without authorizing a new
		// dispatch; ReserveWithWire then checks the stored original request hash.
		if body.WireOperation == "" || body.WireOperation == "videos.create" {
			old, oldErr := b.GetCall(r.Context(), "llmrouter", body.CallID)
			if oldErr == nil && old.Rate.Semantics == "metered_v2" && old.Rate.Metered != nil && old.Rate.Metered.Operation == "video" && old.WireOperation == body.WireOperation {
				var grant modelaccess.Claims
				var verifyErr error
				if body.WireOperation == "" {
					grant, verifyErr = a.VerifyReceiptForInstance(r.Context(), body.Credential, old.Rate.Model, body.InstanceID)
				} else {
					grant, verifyErr = a.VerifyWireForInstance(r.Context(), body.Credential, old.Rate.Model, body.InstanceID, body.WireOperation, true)
				}
				if verifyErr != nil {
					return verifyErr
				}
				if grant.ID != old.CredentialID || grant.ActorID != old.ActorID || grant.AccountID != old.AccountID || grant.OriginProductID != old.OriginProductID || old.Rate.ID != body.RateID || (old.InstanceID != "" && old.InstanceID != grant.InstanceID) || (old.TaskID != "" && old.TaskID != body.TaskID) || (grant.CredentialKind != "api_key" && grant.TaskID != body.TaskID) || (grant.CredentialKind == "api_key" && (body.TaskID == "" || len(body.TaskID) > 128)) {
					return accounts.ErrForbidden
				}
				request := billing.ReserveRequest{CredentialID: grant.ID, CallID: body.CallID, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, RequestDigest: body.RequestDigest, RateID: body.RateID, Amount: body.Amount}
				var result billing.Call
				var replayErr error
				if grant.CredentialKind == "api_key" || body.WireOperation != "" {
					result, replayErr = b.ReserveWithWire(r.Context(), "llmrouter", request, grant.InstanceID, body.TaskID, body.WireOperation)
				} else {
					result, replayErr = b.Reserve(r.Context(), "llmrouter", request)
				}
				if replayErr != nil {
					return replayErr
				}
				return respond(w, http.StatusOK, result)
			}
		}
		rates, e := b.Rates(r.Context())
		if e != nil {
			return e
		}
		model := ""
		for _, rate := range rates {
			if rate.ID == body.RateID {
				model = rate.Model
				break
			}
		}
		if model == "" {
			return billing.ErrInvalid
		}
		var grant modelaccess.Claims
		if body.WireOperation != "" {
			grant, e = a.VerifyWireForInstance(r.Context(), body.Credential, model, body.InstanceID, body.WireOperation, false)
		} else {
			grant, e = a.VerifyForInstance(r.Context(), body.Credential, model, body.InstanceID)
		}
		if e != nil {
			return e
		}
		if (grant.CredentialKind != "api_key" && grant.TaskID != body.TaskID) || (grant.CredentialKind == "api_key" && (body.TaskID == "" || len(body.TaskID) > 128)) {
			return accounts.ErrForbidden
		}
		request := billing.ReserveRequest{CredentialID: grant.ID, CallID: body.CallID, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, RequestDigest: body.RequestDigest, RateID: body.RateID, Amount: body.Amount}
		var v billing.Call
		if grant.CredentialKind == "api_key" || body.WireOperation != "" {
			v, e = b.ReserveWithWire(r.Context(), "llmrouter", request, grant.InstanceID, body.TaskID, body.WireOperation)
		} else {
			v, e = b.Reserve(r.Context(), "llmrouter", request)
		}
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	}))
}

// authorizeResultReadActor is a point-in-time prerequisite, not a substitute
// for linearizing result delivery with account or login revocation.
func (s *api) authorizeResultReadActor(r *http.Request, grant modelaccess.Claims) error {
	raw, ok := runtimeBearer(r, "X-UniHub-Actor-Authorization")
	if !ok || strings.HasPrefix(raw, "upa_") || strings.HasPrefix(raw, "ubp_") || s.options.Identity == nil || s.options.ProductUsers == nil {
		return errRuntimeActor
	}
	var claims auth.Claims
	matches := 0
	for clientID, verifier := range s.options.ProductVerifiers {
		policy, ok := s.options.ProductAccess[clientID]
		if !ok || policy.ClientID != clientID || policy.ProductID != grant.OriginProductID || productusers.Instance(policy.InstanceID) != grant.InstanceID || !slices.Contains(policy.Scopes, "access:read") || verifier == nil {
			continue
		}
		candidate, err := verifier.Verify(r.Context(), raw)
		if err == nil && candidate.ExpiresAt > time.Now().Unix() && candidate.Issuer != "" && candidate.Subject != "" {
			claims = candidate
			matches++
		}
	}
	if matches != 1 || !s.options.Identity.SessionAllowed(r.Context(), raw) {
		return errRuntimeActor
	}
	if s.options.PlatformAdmin != nil {
		if err := s.options.PlatformAdmin.CredentialAllowed(r.Context(), grant.ActorID, claims.IssuedAt); err != nil {
			return accounts.ErrForbidden
		}
	}
	if err := s.options.ModelAccess.RequireResultReadIdentity(r.Context(), grant, claims.Issuer, claims.Subject); err != nil {
		return err
	}
	if r.Context().Err() != nil || claims.ExpiresAt <= time.Now().Unix() {
		return errRuntimeActor
	}
	return nil
}
