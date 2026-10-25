package identityapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrUnavailable     = errors.New("identity profile unavailable")
	ErrInvalidInput    = errors.New("identity profile invalid input")
	ErrInvalidPassword = errors.New("identity profile invalid password")
	ErrRateLimited     = errors.New("identity profile rate limited")
)

type Profile struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	Email       string `json:"email"`
	Version     string `json:"version"`
}

func (s *Server) ProfileEnabled() bool { return s.client != nil && !s.opts.Production }

type providerUser struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	Avatar          string `json:"avatar"`
	PermanentAvatar string `json:"permanentAvatar"`
	Email           string `json:"email"`
	UpdatedTime     string `json:"updatedTime"`
}

type profileIdentity struct {
	Issuer  string
	Subject string
}

func (s *Server) profileIdentity(ctx context.Context, userID string) (profileIdentity, error) {
	var out profileIdentity
	err := s.pool.QueryRow(ctx, `SELECT issuer,subject FROM identities WHERE user_id=$1`, userID).Scan(&out.Issuer, &out.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrUnavailable
	}
	if err != nil {
		return out, err
	}
	if out.Issuer != s.opts.Issuer || out.Subject == "" {
		return profileIdentity{}, ErrUnavailable
	}
	return out, nil
}

func (s *Server) providerUser(ctx context.Context, id profileIdentity) (providerUser, error) {
	var env envelope
	path := "/api/get-user?userId=" + url.QueryEscape(id.Subject) + "&owner=" + url.QueryEscape("unihub")
	if err := s.providerJSON(ctx, "GET", path, nil, true, &env); err != nil || env.Status != "ok" || len(env.Data) == 0 || string(env.Data) == "null" {
		return providerUser{}, ErrUnavailable
	}
	var user providerUser
	if err := json.Unmarshal(env.Data, &user); err != nil || user.ID != id.Subject || user.Name == "" {
		return providerUser{}, ErrUnavailable
	}
	return user, nil
}

func (s *Server) localAvatar(ctx context.Context, userID string) (string, string, error) {
	var contentType string
	var updated time.Time
	err := s.pool.QueryRow(ctx, `SELECT content_type,updated_at FROM identity_avatars WHERE user_id=$1`, userID).Scan(&contentType, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return "/v1/me/avatar", contentType + ":" + updated.UTC().Format(time.RFC3339Nano), nil
}

func (s *Server) Profile(ctx context.Context, userID string) (Profile, error) {
	id, err := s.profileIdentity(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	user, err := s.providerUser(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	avatarURL, avatarVersion, err := s.localAvatar(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	versionData, _ := json.Marshal([]string{user.UpdatedTime, user.Name, user.DisplayName, user.Email, avatarVersion})
	versionHash := sha256.Sum256(versionData)
	version := hex.EncodeToString(versionHash[:])
	if _, err = s.pool.Exec(ctx, `UPDATE users SET name=$2,email=$3 WHERE id=$1`, userID, user.DisplayName, user.Email); err != nil {
		return Profile{}, err
	}
	return Profile{ID: userID, Username: user.Name, DisplayName: user.DisplayName, AvatarURL: avatarURL, Email: user.Email, Version: version}, nil
}

func (s *Server) UpdateDisplayName(ctx context.Context, userID, displayName string) (Profile, error) {
	if !utf8.ValidString(displayName) || utf8.RuneCountInString(displayName) > 80 {
		return Profile{}, ErrInvalidInput
	}
	id, err := s.profileIdentity(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	user, err := s.providerUser(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	body := map[string]string{"displayName": displayName}
	var env envelope
	path := "/api/update-user?id=" + url.QueryEscape("unihub/"+user.Name) + "&columns=displayName"
	if displayName == "" {
		path += "&allowEmpty=true"
	}
	if err = s.providerJSON(ctx, "POST", path, body, true, &env); err != nil || env.Status != "ok" {
		return Profile{}, ErrUnavailable
	}
	profile, err := s.Profile(ctx, userID)
	if err != nil || profile.DisplayName != displayName {
		return Profile{}, ErrUnavailable
	}
	return profile, nil
}

func (s *Server) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if currentPassword == "" || newPassword == "" || len(currentPassword) > 512 || len(newPassword) > 512 {
		return ErrInvalidInput
	}
	var attempts int
	err := s.pool.QueryRow(ctx, `INSERT INTO identity_rate_limits(key_hash,window_start,hits) VALUES($1,date_bin(interval '15 minutes',now(),timestamptz '2000-01-01'),1) ON CONFLICT(key_hash,window_start) DO UPDATE SET hits=identity_rate_limits.hits+1 RETURNING hits`, s.mac("profile_password", userID)).Scan(&attempts)
	if err != nil {
		return ErrUnavailable
	}
	if attempts > 5 {
		return ErrRateLimited
	}
	id, err := s.profileIdentity(ctx, userID)
	if err != nil {
		return err
	}
	user, err := s.providerUser(ctx, id)
	if err != nil {
		return err
	}
	org, err := s.organization(ctx)
	if err != nil {
		return ErrUnavailable
	}
	if !validPassword(newPassword, user.Name, user.Email, org.PasswordOptions) {
		return ErrInvalidPassword
	}
	// Commit the cutoff before sending a mutation that may succeed without a
	// reliable response. Each attempt owns one row, so a rejected concurrent
	// attempt cannot remove another password change's revocation.
	attemptID := randomID()
	if _, err = s.pool.Exec(ctx, `INSERT INTO identity_revocations(id,issuer,subject,revoked_after) VALUES($1,$2,$3,clock_timestamp())`, attemptID, id.Issuer, id.Subject); err != nil {
		return ErrUnavailable
	}
	form := url.Values{"userOwner": {"unihub"}, "userName": {user.Name}, "oldPassword": {currentPassword}, "newPassword": {newPassword}}
	var env envelope
	if err = s.providerForm(ctx, "/api/set-password", form, &env); err != nil {
		// Casdoor may have changed the password before its response was lost.
		// Keep the pending row while the provider may still be running. It blocks
		// all bearer and browser API use until explicit controlled recovery.
		_ = s.revokeSessions(id)
		return ErrUnavailable
	}
	if env.Status != "ok" {
		if providerPasswordRejected(env) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, err = s.pool.Exec(cleanup, `DELETE FROM identity_revocations WHERE id=$1`, attemptID); err != nil {
				return ErrUnavailable
			}
			return ErrInvalidPassword
		}
		// Any other provider error is ambiguous. The provider may have persisted
		// the password before returning its error envelope. Move the cutoff after
		// the attempt and revoke local sessions before returning an unknown result.
		// A received handler error has completed the provider operation. If
		// finalizing the cutoff fails, keep the pending row, which blocks all use.
		if s.revokeSessions(id) == nil {
			// A failed finalization leaves pending=true and remains fail-closed.
			_ = s.markRevocation(id, attemptID)
		}
		return ErrUnavailable
	}
	if err = s.revokeSessions(id); err != nil {
		// The provider accepted the mutation, but local session cleanup is unknown.
		return ErrUnavailable
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err = s.pool.Exec(cleanup, `DELETE FROM identity_revocations WHERE id=$1`, attemptID); err != nil {
		return ErrUnavailable
	}
	return nil
}

// SessionAllowed is called only after the bearer signature, issuer, audience and
// token purpose have passed the API's verifier. The local cutoff also rejects
// still-active provider tokens after an ambiguous password mutation.
func (s *Server) SessionAllowed(ctx context.Context, token string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(s.opts.Issuer, "/")+"/api/get-account", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var env envelope
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&env) != nil || env.Status != "ok" {
		return false
	}
	var user providerUser
	if json.Unmarshal(env.Data, &user) != nil || user.ID == "" {
		return false
	}
	var revoked time.Time
	var pending bool
	err = s.pool.QueryRow(ctx, `SELECT max(revoked_after),bool_or(pending) FROM identity_revocations WHERE issuer=$1 AND subject=$2 GROUP BY issuer,subject`, s.opts.Issuer, user.ID).Scan(&revoked, &pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	if err != nil || pending {
		return false
	}
	issued, subject, ok := tokenClaims(token)
	return ok && subject == user.ID && issued.After(revoked)
}

func (s *Server) BrowserSessionAllowed(ctx context.Context, userID string) bool {
	var pending bool
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(bool_or(r.pending),false) FROM identity_revocations r JOIN identities i ON i.issuer=r.issuer AND i.subject=r.subject WHERE i.user_id=$1`, userID).Scan(&pending)
	return err == nil && !pending
}

func tokenClaims(token string) (time.Time, string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, "", false
	}
	var claims struct {
		IssuedAt float64 `json:"iat"`
		Subject  string  `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.IssuedAt <= 0 || claims.Subject == "" {
		return time.Time{}, "", false
	}
	sec, nanos := int64(claims.IssuedAt), int64((claims.IssuedAt-float64(int64(claims.IssuedAt)))*1e9)
	return time.Unix(sec, nanos), claims.Subject, true
}

// Pinned Casdoor CheckPassword returns this English message before any
// password mutation. Other provider errors may follow a persisted mutation.
var passwordRejected = regexp.MustCompile(`^password or code is incorrect(?:, you have [0-9]+ remaining chances)?$`)

func providerPasswordRejected(env envelope) bool {
	return passwordRejected.MatchString(env.Msg)
}

func (s *Server) markRevocation(id profileIdentity, attemptID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `UPDATE identity_revocations SET revoked_after=clock_timestamp(),pending=false WHERE id=$1 AND issuer=$2 AND subject=$3`, attemptID, id.Issuer, id.Subject)
	return err
}

func (s *Server) revokeSessions(id profileIdentity) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `DELETE FROM browser_sessions WHERE issuer=$1 AND subject=$2`, id.Issuer, id.Subject)
	return err
}

func (s *Server) PutAvatar(ctx context.Context, userID, contentType string, data []byte) (Profile, error) {
	if len(data) == 0 || len(data) > 512<<10 || (contentType != "image/png" && contentType != "image/jpeg") {
		return Profile{}, ErrInvalidInput
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 1024 || config.Height > 1024 || contentType != "image/"+format {
		return Profile{}, ErrInvalidInput
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Profile{}, ErrInvalidInput
	}
	var normalized bytes.Buffer
	if png.Encode(&normalized, decoded) != nil || normalized.Len() > 512<<10 {
		return Profile{}, ErrInvalidInput
	}
	data = normalized.Bytes()
	contentType = "image/png"
	if _, err := s.profileIdentity(ctx, userID); err != nil {
		return Profile{}, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO identity_avatars(user_id,content_type,data,updated_at) VALUES($1,$2,$3,clock_timestamp()) ON CONFLICT(user_id) DO UPDATE SET content_type=excluded.content_type,data=excluded.data,updated_at=excluded.updated_at`, userID, contentType, data); err != nil {
		return Profile{}, err
	}
	return s.Profile(ctx, userID)
}

func (s *Server) Avatar(ctx context.Context, userID string) (string, []byte, error) {
	if _, err := s.profileIdentity(ctx, userID); err != nil {
		return "", nil, err
	}
	var contentType string
	var data []byte
	if err := s.pool.QueryRow(ctx, `SELECT content_type,data FROM identity_avatars WHERE user_id=$1`, userID).Scan(&contentType, &data); errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	} else if err != nil {
		return "", nil, err
	}
	return contentType, data, nil
}

func (s *Server) providerForm(ctx context.Context, path string, form url.Values, out any) error {
	// Keep password material inside the request body and the caller's stack.
	return s.providerFormRaw(ctx, path, strings.NewReader(form.Encode()), out)
}

func (s *Server) providerFormRaw(ctx context.Context, path string, body io.Reader, out any) error {
	// The implementation is kept separate from providerJSON so JSON provider
	// requests cannot accidentally receive form credentials.
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(s.opts.Issuer, "/")+path, body)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Language", "en")
	req.SetBasicAuth(s.opts.ManagementClientID, s.opts.ManagementClientSecret)
	res, err := s.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 || res.StatusCode != 200 || json.Unmarshal(b, out) != nil {
		return ErrUnavailable
	}
	return nil
}
