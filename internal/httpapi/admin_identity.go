package httpapi

import (
	"errors"
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/platformadmin"
)

func adminIdentityResult(w http.ResponseWriter, v any, e error) error {
	switch {
	case errors.Is(e, platformadmin.ErrIdentityUnavailable):
		fail(w, 503, "identity_capability_unavailable")
		return nil
	case errors.Is(e, platformadmin.ErrRecoveryUnsupported):
		fail(w, 409, "verified_recovery_channel_required")
		return nil
	case errors.Is(e, platformadmin.ErrIdentityUnknown):
		fail(w, 409, "identity_outcome_unknown")
		return nil
	case e != nil:
		return e
	}
	return respond(w, 200, v)
}
func (s *api) adminIdentityRoutes(add func(string, action)) {
	if s.options.PlatformAdmin == nil {
		return
	}
	a := s.options.PlatformAdmin
	add("GET /admin/v1/identity-capabilities", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := a.IdentityCapabilities(r.Context(), u.ID)
		return adminIdentityResult(w, v, e)
	})
	add("GET /admin/v1/identities", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminListOptions(r)
		if e != nil {
			return e
		}
		v, e := a.ListIdentities(r.Context(), u.ID, o)
		return adminIdentityResult(w, v, e)
	})
	add("POST /admin/v1/identities/materialize", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.MaterializeIdentityInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.MaterializeIdentity(r.Context(), u.ID, b)
		return adminIdentityResult(w, v, e)
	})
	add("POST /admin/v1/identities", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.CreateIdentityInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.CreateIdentity(r.Context(), u.ID, b)
		return adminIdentityResult(w, v, e)
	})
	add("PATCH /admin/v1/users/{user}/display-name", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.DisplayNameInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.UpdateIdentityDisplayName(r.Context(), u.ID, r.PathValue("user"), b)
		return adminIdentityResult(w, v, e)
	})
	add("POST /admin/v1/users/{user}/recovery", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.RecoveryInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.RequestRecovery(r.Context(), u.ID, r.PathValue("user"), b)
		return adminIdentityResult(w, v, e)
	})
	add("POST /admin/v1/users/{user}/activation", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.RecoveryInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.RequestActivation(r.Context(), u.ID, r.PathValue("user"), b)
		return adminIdentityResult(w, v, e)
	})

	add("POST /admin/v1/users/{user}/sessions/revoke", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.SessionRevocationInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.RevokeIdentitySessions(r.Context(), u.ID, r.PathValue("user"), b)
		return adminIdentityResult(w, v, e)
	})

	add("GET /admin/v1/identity-operations/{operation}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := a.IdentityOperation(r.Context(), u.ID, r.PathValue("operation"))
		return adminIdentityResult(w, v, e)
	})
	add("GET /admin/v1/teams/{account}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		v, e := a.TeamDetail(r.Context(), u.ID, r.PathValue("account"))
		return adminIdentityResult(w, v, e)
	})
	add("GET /admin/v1/teams/{account}/members", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminListOptions(r)
		if e != nil {
			return e
		}
		v, e := a.TeamMembers(r.Context(), u.ID, r.PathValue("account"), o)
		return adminIdentityResult(w, v, e)
	})
	add("PATCH /admin/v1/teams/{account}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.TeamNameInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.RenameTeam(r.Context(), u.ID, r.PathValue("account"), b)
		return adminIdentityResult(w, v, e)
	})
	add("PATCH /admin/v1/teams/{account}/members/{user}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.TeamMemberInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.ChangeTeamMember(r.Context(), u.ID, r.PathValue("account"), r.PathValue("user"), b)
		return adminIdentityResult(w, v, e)
	})
	add("POST /admin/v1/teams/{account}/invitations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b platformadmin.TeamInvitationInput
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.CreateTeamInvitation(r.Context(), u.ID, r.PathValue("account"), b)
		return adminIdentityResult(w, v, e)
	})
	add("GET /admin/v1/teams/{account}/invitations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminListOptions(r)
		if e != nil {
			return e
		}
		v, e := a.PlatformInvitations(r.Context(), u.ID, r.PathValue("account"), o)
		return adminIdentityResult(w, v, e)
	})
	add("DELETE /admin/v1/teams/{account}/invitations/{invitation}", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			ExpectedVersion int64  `json:"expected_version"`
			Reason          string `json:"reason"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.RevokeTeamInvitation(r.Context(), u.ID, r.PathValue("account"), r.PathValue("invitation"), b.ExpectedVersion, b.Reason)
		return adminIdentityResult(w, v, e)
	})
}

// These are recipient routes, not administration routes. A pending invitation
// grants no management privileges; acceptance rechecks the originating grant.
func (s *api) platformInvitationRoutes(add func(string, action)) {
	if s.options.PlatformAdmin == nil {
		return
	}
	a := s.options.PlatformAdmin
	add("GET /v1/platform-invitations", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		o, e := adminListOptions(r)
		if e != nil {
			return e
		}
		v, e := a.PlatformInvitations(r.Context(), u.ID, "", o)
		return adminIdentityResult(w, v, e)
	})
	add("POST /v1/platform-invitations/{invitation}/accept", func(w http.ResponseWriter, r *http.Request, u accounts.User) error {
		var b struct {
			ExpectedVersion int64 `json:"expected_version"`
		}
		if e := decode(w, r, &b); e != nil {
			return e
		}
		v, e := a.AcceptPlatformInvitation(r.Context(), u.ID, r.PathValue("invitation"), b.ExpectedVersion)
		return adminIdentityResult(w, v, e)
	})
}
