package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

func TestRuntimeCreditUsesCurrentTargetHierarchyIncludingInactiveUsers(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	admin, e := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://runtime-test", Subject: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version) VALUES($1,'llmrouter','default','["member"]',true,10,1)`, admin.ID); e != nil {
		t.Fatal(e)
	}
	in := RuntimeAdjustmentRequest{RequestKey: "hierarchy", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "50", Reason: "original admin credit", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}
	if _, e = s.RuntimeAdjustCredit(ctx, scope, admin.ID, in); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("admin adjusted root: %v", e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, b.UserID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RuntimeAdjustCredit(ctx, scope, admin.ID, in); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("inactive root lost hierarchy: %v", e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE product_user_access SET product_role=1 WHERE user_id=$1`, b.UserID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RuntimeAdjustCredit(ctx, scope, admin.ID, in); e != nil {
		t.Fatalf("superior cannot adjust disabled member: %v", e)
	}
	available, e := s.ledger.Balance(ctx, RuntimeAddress(scope, b.PersonalAccountID, "available"), RuntimeAsset)
	if e != nil || available != 50 {
		t.Fatalf("balance %d: %v", available, e)
	}
}

func TestRuntimeCanceledCreditIsDefiniteOnReplay(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	in := RuntimeAdjustmentRequest{RequestKey: "revoked-credit", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "50", Reason: "credit", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion}
	entity := runtimeID(scope, "gift", in.RequestKey)
	out := RuntimeAdjustment{RuntimeScope: scope, ID: entity, AccountID: b.PersonalAccountID, Amount: "50", Status: "PENDING", RequestDigest: hash(in), Source: encode(in)}
	err := s.locked(ctx, b.PersonalAccountID, func(c *pgxpool.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if _, e = tx.Exec(ctx, `INSERT INTO product_runtime_adjustments(id,account_id,product_id,instance_id,source_kind,source_key,request_digest,snapshot) VALUES($1,$2,$3,$4,'gift',$5,$6,$7)`, entity, b.PersonalAccountID, scope.ProductID, scope.InstanceID, in.RequestKey, hash(in), encode(out)); e != nil {
			return e
		}
		cmd, e := s.createCommand(ctx, tx, b.PersonalAccountID, b.UserID, "runtime_gift", entity, nil, effect{RuntimeAdjustment: &out})
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE product_user_access SET product_role=1 WHERE user_id=$1`, b.UserID); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		return s.runLocked(ctx, c, cmd.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RuntimeAdjustCredit(ctx, scope, b.UserID, in)
	if !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("canceled credit did not terminate: %v", err)
	}
}
