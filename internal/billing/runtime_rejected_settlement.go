package billing

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runtimeRejectedSettlement is private operational evidence, never accepted
// usage or a financial command. Keep the first valid overheld report per call;
// retries (including conflicting reports) cannot replace that original proof.
// The request usage has already passed RuntimeSettle's 64 KiB JSON validation.
type runtimeRejectedSettlement struct {
	RuntimeScope
	RuntimeSettlementRequest
	CallID        string    `json:"call_id"`
	AccountID     string    `json:"account_id"`
	Asset         string    `json:"asset"`
	UnitVersion   string    `json:"unit_version"`
	RequestDigest string    `json:"request_digest"`
	Held          string    `json:"held"`
	Digest        string    `json:"digest"`
	Reason        string    `json:"reason"`
	ObservedAt    time.Time `json:"observed_at"`
}

// Called only under the account lock, after reloading the current RESERVED
// call. Autocommit must succeed before returning the ordinary overheld conflict;
// a storage failure is returned as such. The separate column survives every
// command recovery, extension, settlement and release snapshot replacement.
func retainRejectedRuntimeSettlement(ctx context.Context, c *pgxpool.Conn, call RuntimeCall, request RuntimeSettlementRequest, digest string) error {
	proof := runtimeRejectedSettlement{
		RuntimeScope: call.RuntimeScope, RuntimeSettlementRequest: request,
		CallID: call.CallID, AccountID: call.AccountID, Asset: call.Asset,
		UnitVersion: call.UnitVersion, RequestDigest: call.RequestDigest,
		Held: call.Held, Digest: digest, Reason: "amount_exceeds_held", ObservedAt: time.Now().UTC(),
	}
	_, err := c.Exec(ctx, `UPDATE product_runtime_calls SET rejected_settlement=$2 WHERE id=$1 AND rejected_settlement IS NULL`, call.ID, encode(proof))
	return err
}
