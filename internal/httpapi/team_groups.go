package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
)

func (s *api) teamGroupRoutes(add func(string, action)) {
	const groups = "/v1/accounts/{account}/groups"
	add("GET "+groups, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		directory, err := s.accounts.TeamGroups(r.Context(), u.ID, r.PathValue("account"))
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, directory)
	})
	add("GET "+groups+"/memberships", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		memberships, err := s.accounts.TeamGroupMemberships(r.Context(), u.ID, r.PathValue("account"))
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, memberships)
	})

	type groupName struct {
		Name string `json:"name"`
	}
	add("POST "+groups, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body groupName
		if err := decode(w, r, &body); err != nil {
			return err
		}
		group, err := s.accounts.CreateTeamGroup(r.Context(), u.ID, r.PathValue("account"), body.Name)
		if err != nil {
			return err
		}
		return respond(w, http.StatusCreated, group)
	})
	add("PATCH "+groups+"/{group}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var body groupName
		if err := decode(w, r, &body); err != nil {
			return err
		}
		group, err := s.accounts.RenameTeamGroup(r.Context(), u.ID, r.PathValue("account"), r.PathValue("group"), body.Name)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, group)
	})
	add("DELETE "+groups+"/{group}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		if err := s.accounts.DeleteTeamGroup(r.Context(), u.ID, r.PathValue("account"), r.PathValue("group")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	add("PUT "+groups+"/{group}/members/{member}", s.teamGroupMembershipAction(true))
	add("DELETE "+groups+"/{group}/members/{member}", s.teamGroupMembershipAction(false))
}

func (s *api) teamGroupMembershipAction(assigned bool) action {
	return func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		err := s.accounts.SetTeamGroupMembership(r.Context(), u.ID, r.PathValue("account"), r.PathValue("group"), r.PathValue("member"), assigned)
		if err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
