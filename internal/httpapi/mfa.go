package httpapi

import (
	"errors"
	"net/http"

	"unihub/internal/accounts"
	"unihub/internal/identityapi"
)

type mfaStatus = identityapi.MfaStatus
type mfaSetup = identityapi.MfaSetup

func mfaError(w http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, identityapi.ErrManagedByProduct):
		fail(w, 409, "credentials_managed_by_product")
	case errors.Is(err, identityapi.ErrInvalidInput):
		fail(w, 400, "invalid_request")
	case errors.Is(err, identityapi.ErrMfaState):
		fail(w, 409, "conflict")
	case errors.Is(err, identityapi.ErrMfaRateLimited):
		fail(w, 429, "rate_limited")
	case errors.Is(err, identityapi.ErrMfaInvalidCode):
		fail(w, 401, "invalid_mfa_code")
	case errors.Is(err, identityapi.ErrMfaInvalidPass):
		fail(w, 401, "invalid_password")
	default:
		fail(w, 503, "identity_unavailable")
	}
	return nil
}

func (s *api) mfaRoutes(add func(string, action)) {
	add("GET /v1/me/mfa", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		status, err := s.options.Identity.MfaStatus(r.Context(), u.ID)
		if err != nil {
			return mfaError(w, err)
		}
		return respond(w, 200, status)
	})
	add("POST /v1/me/mfa/enroll", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		setup, status, err := s.options.Identity.MfaEnroll(r.Context(), u.ID)
		if err != nil {
			return mfaError(w, err)
		}
		return respond(w, 200, map[string]any{"secret": setup.Secret, "otpauth_url": setup.URL, "recovery_code": setup.RecoveryCodes[0], "enabled": status.Enabled})
	})
	add("POST /v1/me/mfa/confirm", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body identityapi.MfaEnrollRequest
		if err := decode(w, r, &body); err != nil {
			return err
		}
		status, err := s.options.Identity.MfaConfirm(r.Context(), u.ID, body.Secret, body.Passcode, body.RecoveryCode)
		if err != nil {
			return mfaError(w, err)
		}
		return respond(w, 200, status)
	})
	add("POST /v1/me/mfa/disable", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body identityapi.MfaDisableRequest
		if err := decode(w, r, &body); err != nil {
			return err
		}
		status, err := s.options.Identity.MfaDisable(r.Context(), u.ID, body.CurrentPassword, body.Passcode)
		if err != nil {
			return mfaError(w, err)
		}
		return respond(w, 200, status)
	})
}
