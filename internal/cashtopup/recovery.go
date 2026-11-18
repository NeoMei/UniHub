package cashtopup

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Recover checks unpaid orders independently of callback delivery. Claim an
// attempt before merchant I/O so concurrent workers share a bounded workload.
func (s *Service) Recover(ctx context.Context) error {
	var failures error
	for range 16 {
		now := s.now().UTC()
		var snapshot []byte
		err := s.pool.QueryRow(ctx, `WITH due AS (
			SELECT id FROM cash_checkouts
			WHERE snapshot->>'payment_status' = 'CREATED'
			AND (last_recovery_attempt IS NULL OR last_recovery_attempt <=
				CASE WHEN (snapshot->>'expires_at')::timestamptz <= $1
				THEN $1 - interval '5 minutes' ELSE $1 - interval '15 seconds' END)
			ORDER BY last_recovery_attempt NULLS FIRST, created_at, id
			LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE cash_checkouts c SET last_recovery_attempt=$1
		FROM due WHERE c.id=due.id RETURNING c.snapshot`, now).Scan(&snapshot)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return errors.Join(failures, err)
		}
		var rec record
		if err := json.Unmarshal(snapshot, &rec); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		// A slow merchant must not keep the same order at the head of the
		// next recovery cycle or block the wallet's posting worker.
		run, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = s.refreshCheckout(run, rec, true)
		cancel()
		if err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}
