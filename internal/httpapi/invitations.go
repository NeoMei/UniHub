package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
)

func (s *api) invitationQueryRoutes(add func(string, action)) {
	add("GET /v1/invitations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		values, e := s.accounts.ListInvitations(r.Context(), u.ID, "")
		if e != nil {
			return e
		}
		return respond(w, 200, values)
	})
	add("GET /v1/accounts/{account}/invitations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		values, e := s.accounts.ListInvitations(r.Context(), u.ID, r.PathValue("account"))
		if e != nil {
			return e
		}
		return respond(w, 200, values)
	})
}
