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
			Credential           string              `json:"credential"`
			InstanceID           string              `json:"instance_id"`
			TaskID               string              `json:"task_id"`
			CallID               string              `json:"call_id"`
			RequestDigest        string              `json:"request_digest"`
			Model                string              `json:"model"`
			WireOperation        string              `json:"wire_operation"`
			ManagedOperation     string              `json:"managed_operation,omitempty"`
			Quote                billing.NativeQuote `json:"quote"`
			CreditScope          string              `json:"credit_scope"`
			ProductAuthorization string              `json:"product_authorization"`
			MinimumCreditAmount  string              `json:"minimum_credit_amount"`
		}
		if e := decode(w, r, &body); e != nil {
			return e
		}
		// Product reserve rechecks membership inside billing's account lock,
		// after replay lookup, so an expired membership cannot mask a prior hold.
		productReserve := body.CreditScope == billing.ProductCreditScopeV1
		grant, e := s.options.ModelAccess.VerifyWireForInstance(r.Context(), body.Credential, body.Model, body.InstanceID, body.WireOperation, productReserve)
		if e != nil {
			return e
		}
		if grant.CredentialKind != "api_key" || grant.ModelScope != modelaccess.ScopeLLMRouterAvailable || (grant.PricingSource != modelaccess.PricingLLMRouterNative && grant.PricingSource != modelaccess.PricingLLMRouterNativeCredit) {
			return accounts.ErrForbidden
		}
		if grant.CreditScope != body.CreditScope ||
			(grant.CreditScope != billing.ProductCreditScopeV1 && body.MinimumCreditAmount != "") {
			return accounts.ErrForbidden
		}
		if e = s.authorizeProductModelGrant(&grant, body.ProductAuthorization); e != nil {
			return e
		}
		out, e := s.options.Billing.ReserveNative(r.Context(), "llmrouter", billing.NativeReserveRequest{
			ReserveRequest: billing.ReserveRequest{CredentialID: grant.ID, CallID: body.CallID, ActorID: grant.ActorID, AccountID: grant.AccountID, OriginProductID: grant.OriginProductID, RequestDigest: body.RequestDigest},
			Model:          body.Model, WireOperation: body.WireOperation, ManagedOperation: body.ManagedOperation, InstanceID: grant.InstanceID,
			TaskID: body.TaskID, CredentialVersion: grant.CredentialVersion, Quote: body.Quote,
			CreditScope: grant.CreditScope, SourceVerified: grant.SourceVerified, MinimumCreditAmount: body.MinimumCreditAmount,
		})
		if errors.Is(e, billing.ErrNativeReserveBudgetRejected) {
			fail(w, http.StatusPaymentRequired, "usage_budget_exceeded")
			return nil
		}
		if errors.Is(e, billing.ErrSubscriptionRequired) && grant.CreditScope == billing.ProductCreditScopeV1 {
			fail(w, http.StatusForbidden, "product_membership_expired_no_hold")
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
