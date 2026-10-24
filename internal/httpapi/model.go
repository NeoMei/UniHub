package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
)

func (s *api) modelRoutes(m *http.ServeMux, add func(string, action)) {
	a := s.options.ModelAccess
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
			Token string `json:"token"`
			Model string `json:"model"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		v, e := a.Verify(r.Context(), body.Token, body.Model)
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	}))
	m.HandleFunc("POST /internal/v1/calls/reserve", s.serviceAuthenticated(func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			Credential    string `json:"credential"`
			CallID        string `json:"call_id"`
			RequestDigest string `json:"request_digest"`
			RateID        string `json:"rate_id"`
			Amount        string `json:"amount"`
			TaskID        string `json:"task_id"`
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
		grant, e := a.Verify(r.Context(), body.Credential, model)
		if e != nil {
			return e
		}
		if grant.TaskID != body.TaskID {
			return accounts.ErrForbidden
		}
		v, e := b.Reserve(r.Context(), "llmrouter", billing.ReserveRequest{CredentialID: grant.ID, CallID: body.CallID, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, RequestDigest: body.RequestDigest, RateID: body.RateID, Amount: body.Amount})
		if e != nil {
			return e
		}
		return respond(w, 200, v)
	}))
}
