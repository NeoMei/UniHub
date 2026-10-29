package identityapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// providerLogin carries the pinned login envelope plus the provider session
// cookie established by an accepted first factor.
type providerLogin struct {
	envelope
	session string
}

// providerLoginJSON posts the pinned login form and returns the provider's
// session cookie so a pending second factor can continue the same login.
func (s *Server) providerLoginJSON(ctx context.Context, path string, body any, cookieName string, out *providerLogin) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(s.opts.Issuer, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en")
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 || res.StatusCode != 200 {
		return errors.New("identity provider unavailable")
	}
	if json.Unmarshal(b, &out.envelope) != nil {
		return errors.New("identity provider invalid response")
	}
	for _, cookie := range res.Cookies() {
		if cookie.Name == cookieName && cookie.Value != "" {
			out.session = cookie.Name + "=" + cookie.Value
		}
	}
	return nil
}

// Casdoor remains the only second-factor authority. UniHub drives the pinned

// Casdoor remains the only second-factor authority. UniHub drives the pinned
// v4.23.0 MFA management API and stores no TOTP secret or recovery code: both
// only transit through a single response to the enrolled user.
const mfaTypeTotp = "totp"

var (
	ErrMfaUnsupported = errors.New("identity mfa unavailable")
	ErrMfaState       = errors.New("identity mfa invalid state")
	ErrMfaRateLimited = errors.New("identity mfa rate limited")
	ErrMfaInvalidCode = errors.New("identity mfa invalid code")
	ErrMfaInvalidPass = errors.New("identity mfa invalid password")
)

type MfaSetup struct {
	Secret        string   `json:"secret"`
	URL           string   `json:"otpauth_url"`
	RecoveryCodes []string `json:"recovery_codes"`
}

type MfaStatus struct {
	Enabled bool `json:"enabled"`
}

type MfaEnrollRequest struct {
	Passcode     string `json:"passcode"`
	Secret       string `json:"secret"`
	RecoveryCode string `json:"recovery_code"`
}

type MfaDisableRequest struct {
	CurrentPassword string `json:"current_password"`
	Passcode        string `json:"passcode"`
}

type mfaAuthorizeRequest struct {
	ClientID string `json:"client_id"`
	Token    string `json:"mfa_token"`
	Passcode string `json:"passcode"`
}

// mfaIdentity resolves only provider-managed identities. Product-owned
// credentials keep their original product as the second-factor authority.
func (s *Server) mfaIdentity(ctx context.Context, userID string) (profileIdentity, providerUser, error) {
	id, err := s.profileIdentity(ctx, userID)
	if errors.Is(err, ErrUnavailable) {
		if _, nativeErr := s.nativeProfile(ctx, userID); nativeErr == nil {
			return profileIdentity{}, providerUser{}, ErrManagedByProduct
		}
		return profileIdentity{}, providerUser{}, ErrUnavailable
	}
	if err != nil {
		return profileIdentity{}, providerUser{}, err
	}
	user, err := s.providerUser(ctx, id)
	if err != nil {
		return profileIdentity{}, providerUser{}, err
	}
	return id, user, nil
}

func mfaSetupRequest(ctx context.Context, s *Server, path string, user providerUser, fields map[string]string) (envelope, error) {
	form := url.Values{"owner": {"unihub"}, "name": {user.Name}, "mfaType": {mfaTypeTotp}}
	for k, v := range fields {
		form.Set(k, v)
	}
	var env envelope
	err := s.providerForm(ctx, path, form, &env)
	return env, err
}

func (s *Server) MfaStatus(ctx context.Context, userID string) (MfaStatus, error) {
	_, user, err := s.mfaIdentity(ctx, userID)
	if err != nil {
		return MfaStatus{}, err
	}
	return MfaStatus{Enabled: user.TotpSecret != ""}, nil
}

func (s *Server) MfaEnroll(ctx context.Context, userID string) (MfaSetup, MfaStatus, error) {
	_, user, err := s.mfaIdentity(ctx, userID)
	if err != nil {
		return MfaSetup{}, MfaStatus{}, err
	}
	if user.TotpSecret != "" {
		return MfaSetup{}, MfaStatus{}, ErrMfaState
	}
	env, err := mfaSetupRequest(ctx, s, "/api/mfa/setup/initiate", user, nil)
	if err == nil && env.Status != "ok" {
		err = ErrMfaUnsupported
	}
	if err != nil {
		return MfaSetup{}, MfaStatus{}, ErrUnavailable
	}
	var props struct {
		Secret        string   `json:"secret"`
		URL           string   `json:"url"`
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	if json.Unmarshal(env.Data, &props) != nil || props.Secret == "" || props.URL == "" || len(props.RecoveryCodes) != 1 || props.RecoveryCodes[0] == "" {
		return MfaSetup{}, MfaStatus{}, ErrUnavailable
	}
	// Only the pending secret transits to the enrolling user; it is never
	// persisted in UniHub, and an already-enabled provider stays untouched.
	status, err := s.MfaStatus(ctx, userID)
	if err != nil || status.Enabled {
		return MfaSetup{}, MfaStatus{}, ErrUnavailable
	}
	return MfaSetup{Secret: props.Secret, URL: props.URL, RecoveryCodes: props.RecoveryCodes}, status, nil
}

func (s *Server) MfaConfirm(ctx context.Context, userID, secret, passcode, recoveryCode string) (MfaStatus, error) {
	if secret == "" || passcode == "" || recoveryCode == "" || len(secret) > 256 || len(passcode) > 16 || len(recoveryCode) > 128 {
		return MfaStatus{}, ErrInvalidInput
	}
	if err := s.limitProfile(ctx, "mfa_confirm", userID); err != nil {
		return MfaStatus{}, err
	}
	_, user, err := s.mfaIdentity(ctx, userID)
	if err != nil {
		return MfaStatus{}, err
	}
	if user.TotpSecret != "" {
		return MfaStatus{}, ErrMfaState
	}
	env, err := mfaSetupRequest(ctx, s, "/api/mfa/setup/verify", user, map[string]string{"secret": secret, "passcode": passcode})
	if err == nil && env.Status != "ok" {
		err = ErrMfaInvalidCode
	}
	if err != nil {
		if errors.Is(err, ErrMfaInvalidCode) {
			return MfaStatus{}, err
		}
		return MfaStatus{}, ErrUnavailable
	}
	env, err = mfaSetupRequest(ctx, s, "/api/mfa/setup/enable", user, map[string]string{"secret": secret, "recoveryCodes": recoveryCode})
	if err == nil && env.Status != "ok" {
		err = ErrMfaUnsupported
	}
	if err != nil {
		return MfaStatus{}, ErrUnavailable
	}
	status, err := s.MfaStatus(ctx, userID)
	if err != nil || !status.Enabled {
		return MfaStatus{}, ErrUnavailable
	}
	if err := s.recordAudit(ctx, userID, "identity.mfa.enabled"); err != nil {
		return MfaStatus{}, ErrUnavailable
	}
	return status, nil
}

func (s *Server) MfaDisable(ctx context.Context, userID, currentPassword, passcode string) (MfaStatus, error) {
	if currentPassword == "" || passcode == "" || len(currentPassword) > 512 {
		return MfaStatus{}, ErrInvalidInput
	}
	if err := s.limitProfile(ctx, "mfa_disable", userID); err != nil {
		return MfaStatus{}, err
	}
	_, user, err := s.mfaIdentity(ctx, userID)
	if err != nil {
		return MfaStatus{}, err
	}
	if user.TotpSecret == "" {
		return MfaStatus{}, ErrMfaState
	}
	// The pinned provider verifies credentials without any mutation first.
	var check envelope
	checkBody := map[string]string{"owner": "unihub", "name": user.Name, "password": currentPassword}
	defer clear(checkBody)
	if err := s.providerJSON(ctx, "POST", "/api/check-user-password", checkBody, true, &check); err != nil {
		return MfaStatus{}, ErrUnavailable
	}
	if check.Status != "ok" {
		return MfaStatus{}, ErrMfaInvalidPass
	}
	env, err := mfaSetupRequest(ctx, s, "/api/mfa/setup/verify", user, map[string]string{"secret": user.TotpSecret, "passcode": passcode})
	if err == nil && env.Status != "ok" {
		err = ErrMfaInvalidCode
	}
	if err != nil {
		if errors.Is(err, ErrMfaInvalidCode) {
			return MfaStatus{}, err
		}
		return MfaStatus{}, ErrUnavailable
	}
	env, err = mfaSetupRequest(ctx, s, "/api/delete-mfa", user, nil)
	if err == nil && env.Status != "ok" {
		err = ErrMfaUnsupported
	}
	if err != nil {
		return MfaStatus{}, ErrUnavailable
	}
	status, err := s.MfaStatus(ctx, userID)
	if err != nil || status.Enabled {
		return MfaStatus{}, ErrUnavailable
	}
	if err := s.recordAudit(ctx, userID, "identity.mfa.disabled"); err != nil {
		return MfaStatus{}, ErrUnavailable
	}
	return status, nil
}

// limitProfile reuses the durable per-user attempt windows of the password
// profile flow so TOTP guessing cannot run faster than password changes.
func (s *Server) limitProfile(ctx context.Context, kind, userID string) error {
	if s.pool == nil {
		return ErrUnavailable
	}
	var attempts int
	err := s.pool.QueryRow(ctx, `INSERT INTO identity_rate_limits(key_hash,window_start,hits) VALUES($1,date_bin(interval '15 minutes',now(),timestamptz '2000-01-01'),1) ON CONFLICT(key_hash,window_start) DO UPDATE SET hits=identity_rate_limits.hits+1 RETURNING hits`, s.mac("profile_password", kind, userID)).Scan(&attempts)
	if err != nil {
		return ErrUnavailable
	}
	if attempts > 5 {
		return ErrMfaRateLimited
	}
	return nil
}

// recordAudit writes the personal-account audit trail without credentials.
func (s *Server) recordAudit(ctx context.Context, userID, action string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES((SELECT a.id FROM accounts a WHERE a.kind='personal' AND a.personal_user_id=$1),$1,$2,$2||'.'||$3)`, userID, randomID(), action)
	return err
}
