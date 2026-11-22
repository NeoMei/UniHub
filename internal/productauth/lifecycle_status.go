package productauth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/productusers"
)

type SourceLifecycleStatus struct {
	LocalUserID string      `json:"local_user_id"`
	AuthVersion int64       `json:"auth_version"`
	State       SourceState `json:"state"`
}

// ReadSourceLifecycle reports the exact native state to its own product, even
// when an account is locked and ordinary login lookup would reject it.
func (s *Service) ReadSourceLifecycle(ctx context.Context, product, instance, local string) (SourceLifecycleStatus, error) {
	if !localSubject.MatchString(local) || !s.users.Managed(product, instance) {
		return SourceLifecycleStatus{}, accounts.ErrInvalid
	}
	status := SourceLifecycleStatus{LocalUserID: local, State: SourceStateActive}
	var locked, deleted bool
	err := s.pool.QueryRow(ctx, `SELECT a.auth_version,a.native_locked,a.deleted FROM product_auth_subjects a
		JOIN product_runtime_bindings b ON b.id=a.binding_id WHERE b.product_id=$1 AND b.instance_id=$2 AND b.local_user_id=$3`,
		product, productusers.Instance(instance), local).Scan(&status.AuthVersion, &locked, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceLifecycleStatus{}, accounts.ErrNotFound
	}
	if err != nil {
		return SourceLifecycleStatus{}, err
	}
	if deleted {
		status.State = SourceStateDeleted
	} else if locked {
		status.State = SourceStateLocked
	}
	return status, nil
}
