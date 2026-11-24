package httpapi

import (
	"net/http"

	"unihub/internal/accounts"
	"unihub/internal/appintegrations"
)

func (s *api) applicationIntegrationRoutes(add func(string, action)) {
	store := s.options.ApplicationIntegrations
	if store == nil {
		return
	}
	superAdmin := func(pattern string, fn action) {
		add(pattern, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
			if err := s.options.PlatformAdmin.Require(r.Context(), u.ID, "applications:manage"); err != nil {
				return err
			}
			return fn(w, r, u)
		})
	}
	const path = "/admin/v1/application-integrations"
	const item = path + "/{product}/{instance}"
	superAdmin("GET "+path, func(w http.ResponseWriter, r *http.Request, _ accounts.User) error {
		apps, err := store.List(r.Context())
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, apps)
	})
	superAdmin("POST "+path, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var input appintegrations.Input
		if err := decode(w, r, &input); err != nil {
			return err
		}
		app, err := store.Create(r.Context(), u.ID, input)
		if err != nil {
			return err
		}
		return respond(w, http.StatusCreated, app)
	})
	superAdmin("PUT "+item, func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var input appintegrations.Input
		if err := decode(w, r, &input); err != nil {
			return err
		}
		app, err := store.Update(r.Context(), u.ID, r.PathValue("product"), r.PathValue("instance"), input)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, app)
	})
	superAdmin("POST "+item+"/credential", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var input struct {
			Version int64 `json:"version"`
		}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		credential, err := store.Issue(r.Context(), u.ID, r.PathValue("product"), r.PathValue("instance"), input.Version)
		if err != nil {
			return err
		}
		return respond(w, http.StatusCreated, credential)
	})
	superAdmin("POST "+item+"/state", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var input struct {
			Version int64  `json:"version"`
			State   string `json:"state"`
		}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		app, err := store.Transition(r.Context(), u.ID, r.PathValue("product"), r.PathValue("instance"), input.Version, input.State)
		if err != nil {
			return err
		}
		return respond(w, http.StatusOK, app)
	})
}
