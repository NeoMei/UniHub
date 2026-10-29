package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
)

func (s *api) modelRoutes(m *http.ServeMux, add func(string, action)) {
	s.apiKeyRoutes(add)
	a := s.options.ModelAccess
	add("GET /v1/products/{product}/model-consistency", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		product := r.PathValue("product")
		if origin := originProduct(r); origin != "" && product != origin {
			return accounts.ErrForbidden
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
		if body.WireOperation != "" {
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
