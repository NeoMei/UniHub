package productauth

import (
	"context"

	"github.com/jackc/pgx/v5"

	"unihub/internal/database"
)

type runtimeMutationGuardKey struct{}

// WithRuntimeMutationGuard carries the credential that admitted a managed runtime
// request into the transaction that writes its results.
func WithRuntimeMutationGuard(ctx context.Context, guard func(context.Context, pgx.Tx) error) context.Context {
	return context.WithValue(ctx, runtimeMutationGuardKey{}, guard)
}

func (s *Service) beginRuntimeMutation(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if guard, ok := ctx.Value(runtimeMutationGuardKey{}).(func(context.Context, pgx.Tx) error); ok {
		if err := guard(ctx, tx); err != nil {
			database.Rollback(tx)
			return nil, err
		}
	}
	return tx, nil
}
