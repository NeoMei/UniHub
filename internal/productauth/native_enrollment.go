package productauth

import (
	"context"
	"errors"
	"strings"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productusers"
)

type NativeEnrollment struct {
	ClientID       string         `json:"client_id"`
	DisplayName    string         `json:"display_name"`
	Email          string         `json:"email"`
	Authentication Authentication `json:"authentication"`
	Automatic      bool           `json:"automatic,omitempty"`
}

// EnrollNativeIdentity resolves an existing mapping or provisions a new identity
// after the original product authenticates its user. Automatic enrollment uses
// profile email only to defer ambiguous provisioning to the explicit link flow.
func (s *Service) EnrollNativeIdentity(ctx context.Context, product, instance string, in NativeEnrollment) (Identity, error) {
	instance = productusers.Instance(instance)
	client, err := s.identityLinkClient(in.ClientID, "")
	if err != nil || client.ProductID != product || productusers.Instance(client.InstanceID) != instance {
		return Identity{}, accounts.ErrForbidden
	}
	if validateAuthentication(in.Authentication, time.Now()) != nil || (in.Authentication.Method == "session" && !in.Automatic) ||
		len(in.DisplayName) > 256 || len(in.Email) > 320 {
		return Identity{}, accounts.ErrInvalid
	}

	issuer := s.issuer + "/product-auth/" + product + "/" + instance
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Identity{}, err
	}
	defer database.Rollback(tx)
	if err = accounts.LockIdentity(ctx, tx, issuer, in.Authentication.LocalUserID); err != nil {
		return Identity{}, err
	}
	binding, err := s.users.RuntimeBindingOn(ctx, tx, product, instance, in.Authentication.LocalUserID)
	if errors.Is(err, accounts.ErrNotFound) {
		if in.Automatic {
			email := strings.ToLower(strings.TrimSpace(in.Email))
			if email == "" {
				return Identity{}, accounts.ErrInvalid
			}
			if err = accounts.LockIdentity(ctx, tx, s.issuer+"/automatic-enrollment-email", email); err != nil {
				return Identity{}, err
			}
			var collision bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(btrim(email))=$1)`, email).Scan(&collision); err != nil {
				return Identity{}, err
			}
			if collision {
				return Identity{}, accounts.ErrConflict
			}
		}
		user, createErr := s.accounts.EnsureIdentityOn(ctx, tx, accounts.Identity{
			Issuer: issuer, Subject: in.Authentication.LocalUserID, Name: in.DisplayName, Email: in.Email,
		})
		if createErr != nil {
			return Identity{}, createErr
		}
		binding.UserID = user.ID
		err = s.bindLinkedIdentity(ctx, tx, client, user.ID, in.Authentication, "native:"+in.Authentication.AuthenticationID, digest(in))
	}
	if err != nil {
		return Identity{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Identity{}, err
	}
	out, err := s.IdentityLinkByCanonicalUser(ctx, product, instance, binding.UserID)
	if err != nil {
		return Identity{}, err
	}
	if out.AuthVersion != in.Authentication.AuthVersion {
		return Identity{}, accounts.ErrConflict
	}
	return out, nil
}
