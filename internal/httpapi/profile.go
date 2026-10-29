package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/identityapi"
)

type avatarService interface {
	PutAvatar(context.Context, string, string, []byte) (identityapi.Profile, error)
	Avatar(context.Context, string) (string, []byte, error)
}

func profileError(w http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, identityapi.ErrManagedByProduct):
		fail(w, 409, "credentials_managed_by_product")
	case errors.Is(err, identityapi.ErrInvalidInput):
		fail(w, 400, "invalid_request")
	case errors.Is(err, identityapi.ErrInvalidPassword):
		fail(w, 400, "invalid_password")
	case errors.Is(err, identityapi.ErrRateLimited):
		fail(w, 429, "rate_limited")
	default:
		fail(w, 503, "identity_unavailable")
	}
	return nil
}

func (s *api) profileRoutes(add func(string, action)) {
	s.mfaRoutes(add)
	add("GET /v1/me/profile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, err := s.options.Identity.Profile(r.Context(), u.ID)
		if err != nil {
			return profileError(w, err)
		}
		return respond(w, 200, v)
	})
	add("PATCH /v1/me/profile", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			DisplayName *string `json:"display_name"`
		}
		if err := decode(w, r, &body); err != nil {
			return err
		}
		if body.DisplayName == nil {
			fail(w, 400, "invalid_request")
			return nil
		}
		v, err := s.options.Identity.UpdateDisplayName(r.Context(), u.ID, *body.DisplayName)
		if err != nil {
			return profileError(w, err)
		}
		return respond(w, 200, v)
	})
	add("POST /v1/me/password", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := decode(w, r, &body); err != nil {
			return err
		}
		if err := s.options.Identity.ChangePassword(r.Context(), u.ID, body.CurrentPassword, body.NewPassword); err != nil {
			return profileError(w, err)
		}
		w.WriteHeader(204)
		return nil
	})
	if a, ok := s.options.Identity.(avatarService); ok {
		add("GET /v1/me/avatar", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			contentType, data, err := a.Avatar(r.Context(), u.ID)
			if err != nil {
				return profileError(w, err)
			}
			if len(data) == 0 {
				fail(w, 404, "not_found")
				return nil
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Disposition", "inline")
			_, err = w.Write(data)
			return err
		})
		add("PUT /v1/me/avatar", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			var body struct {
				ContentType string `json:"content_type"`
				DataBase64  string `json:"data_base64"`
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
			d.DisallowUnknownFields()
			err := d.Decode(&body)
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(w, 413, "request_too_large")
				return nil
			}
			if err != nil || d.Decode(&struct{}{}) != io.EOF {
				fail(w, 400, "invalid_request")
				return nil
			}
			data, err := base64.StdEncoding.DecodeString(body.DataBase64)
			if err != nil || !validAvatar(body.ContentType, data) {
				fail(w, 400, "invalid_request")
				return nil
			}
			contentType := body.ContentType
			v, err := a.PutAvatar(r.Context(), u.ID, contentType, data)
			if err != nil {
				return profileError(w, err)
			}
			return respond(w, 200, v)
		})
	}
}

func validAvatar(contentType string, data []byte) bool {
	if len(data) < 12 {
		return false
	}
	switch contentType {
	case "image/png":
		return string(data[:8]) == "\x89PNG\r\n\x1a\n"
	case "image/jpeg":
		return data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	default:
		return false
	}
}
