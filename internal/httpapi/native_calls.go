package httpapi

import (
	"errors"
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
)

func (s *api) nativeCallRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /internal/v1/calls/native-reserve", s.serviceAuthenticated(func(w http.ResponseWriter, r *http.Request) error {
		var body struct {
			Credential    string              `json:"credential"`
			InstanceID    string              `json:"instance_id"`
			TaskID        string              `json:"task_id"`
			CallID        string              `json:"call_id"`
			RequestDigest string              `json:"request_digest"`
			Model         string              `json:"model"`
			WireOperation string              `json:"wire_operation"`
			Quote         billing.NativeQuote `json:"quote"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		// Introspection checks the current Key and payer. LLMRouter owns model
		// and route admission; billing checks the immutable request on replay.
		grant, e := s.options.ModelAccess.VerifyWireForInstance(r.Context(), body.Credential, body.Model, body.InstanceID, body.WireOperation, false)
		if e != nil {
			return e
		}
		if grant.CredentialKind != "api_key" || grant.ModelScope != modelaccess.ScopeLLMRouterAvailable || (grant.PricingSource != modelaccess.PricingLLMRouterNative && grant.PricingSource != modelaccess.PricingLLMRouterNativeCredit) {
			return accounts.ErrForbidden
		}
		out, e := s.options.Billing.ReserveNative(r.Context(), "llmrouter", billing.NativeReserveRequest{ReserveRequest: billing.ReserveRequest{CredentialID: grant.ID, CallID: body.CallID, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, RequestDigest: body.RequestDigest}, Model: body.Model, WireOperation: body.WireOperation, InstanceID: grant.InstanceID, TaskID: body.TaskID, CredentialVersion: grant.CredentialVersion, Quote: body.Quote})
		if errors.Is(e, billing.ErrNativeReserveBudgetRejected) {
			fail(w, http.StatusPaymentRequired, "usage_budget_exceeded")
			return nil
		}
		if e != nil {
			return e
		}
		return respond(w, http.StatusOK, out)
	}))
	m.HandleFunc("POST /internal/v1/calls/{call}/native-settle", s.serviceAuthenticated(func(w http.ResponseWriter, r *http.Request) error {
		var body billing.NativeSettlementRequest
		if e := decode(w, r, &body); e != nil {
			return e
		}
		if body.CallID != "" && body.CallID != r.PathValue("call") {
			return billing.ErrInvalid
		}
		body.CallID = r.PathValue("call")
		out, e := s.options.Billing.SettleNative(r.Context(), "llmrouter", body)
		if e != nil {
			return e
		}
		return respond(w, http.StatusOK, out)
	}))
}
