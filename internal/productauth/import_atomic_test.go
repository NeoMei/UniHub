package productauth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

const importProduct = "llmrouter"
const importInstance = "import-atomic"

func reviewedImportFixture(t *testing.T) (*Service, Import) {
	t.Helper()
	pool := testutil.Database(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{
		"product_id":"llmrouter","instance_id":"import-atomic","name":"Router",
		"entry_url":"https://router.test","version":1,"default_roles":["member"],
		"roles":[
			{"id":"member","name":"Member","permissions":["product:use"]},
			{"id":"admin","name":"Admin","product_role":10,"native_baseline":true,
			 "privileged":true,"permissions":["user:write","user:read"]}
		]
	}]`))
	if err != nil {
		t.Fatal(err)
	}
	service := New(pool, accounts.New(pool), productusers.New(pool, registry, "https://hub.test", nil), "https://hub.test")
	request := Import{
		Registration:    Registration{LocalUserID: "17", OperationID: "register-native-17", Username: "legacy", AuthVersion: 3},
		ReviewedBatchID: "reviewed-batch-17",
		SnapshotDigest:  strings.Repeat("a", 64),
		ProductRole:     10,
		Active:          true,
		PermissionOverrides: map[string]map[string]bool{
			"user": {"write": false},
		},
	}
	return service, request
}

func importCanonicalUser(t *testing.T, s *Service) accounts.User {
	t.Helper()
	u, err := s.accounts.EnsureIdentity(context.Background(), accounts.Identity{Issuer: "https://oidc.test", Subject: "canonical"})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type importEffects struct {
	users, identities, personalAccounts, members, bindings, assignments, subjects, events int
}

func readImportEffects(t *testing.T, s *Service) importEffects {
	t.Helper()
	var got importEffects
	err := s.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM users),
		(SELECT count(*) FROM identities),
		(SELECT count(*) FROM accounts WHERE kind='personal'),
		(SELECT count(*) FROM account_members),
		(SELECT count(*) FROM product_runtime_bindings),
		(SELECT count(*) FROM product_user_access),
		(SELECT count(*) FROM product_auth_subjects),
		(SELECT count(*) FROM product_auth_events)`).Scan(
		&got.users, &got.identities, &got.personalAccounts, &got.members,
		&got.bindings, &got.assignments, &got.subjects, &got.events,
	)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReviewedImportRejectsInvalidInputWithoutEffects(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Import)
	}{
		{"invalid snapshot", func(in *Import) { in.SnapshotDigest = strings.Repeat("z", 64) }},
		{"invalid registration in snapshot", func(in *Import) { in.AuthVersion = 0 }},
		{"unknown permission", func(in *Import) { in.PermissionOverrides["user"]["unknown"] = false }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, request := reviewedImportFixture(t)
			request.CentralUserID = importCanonicalUser(t, s).ID
			before := readImportEffects(t, s)
			tt.change(&request)
			if _, err := s.Import(context.Background(), importProduct, importInstance, request); !errors.Is(err, accounts.ErrInvalid) {
				t.Fatalf("invalid import returned %v", err)
			}
			if got := readImportEffects(t, s); got != before {
				t.Fatalf("invalid import changed database state: got %+v, want %+v", got, before)
			}
		})
	}
}

func TestReviewedImportRollsBackWhenEventCannotBeWritten(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "new canonical user"
		if explicit {
			name = "existing canonical user"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, request := reviewedImportFixture(t)
			if explicit {
				request.CentralUserID = importCanonicalUser(t, s).ID
			}
			before := readImportEffects(t, s)
			if _, err := s.pool.Exec(ctx, `ALTER TABLE product_auth_events ADD CONSTRAINT reject_import_event CHECK (event <> 'import')`); err != nil {
				t.Fatal(err)
			}
			_, err := s.Import(ctx, importProduct, importInstance, request)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.ConstraintName != "reject_import_event" {
				t.Fatalf("expected event constraint failure, got %v", err)
			}
			if got := readImportEffects(t, s); got != before {
				t.Fatalf("failed import changed database state: got %+v, want %+v", got, before)
			}
			if _, err := s.pool.Exec(ctx, `ALTER TABLE product_auth_events DROP CONSTRAINT reject_import_event`); err != nil {
				t.Fatal(err)
			}
			out, err := s.Import(ctx, importProduct, importInstance, request)
			if err != nil {
				t.Fatal(err)
			}
			if !out.Access.Active || out.ProductRole != 10 || out.AuthVersion != 3 || out.Access.Version != 3 ||
				!slices.Equal(out.Access.Permissions, []string{"product:use", "user:read"}) {
				t.Fatalf("retry lost reviewed authority: %+v", out)
			}
			if explicit && out.Binding.UserID != request.CentralUserID {
				t.Fatalf("bound to %q, want %q", out.Binding.UserID, request.CentralUserID)
			}
			want := importEffects{1, 1, 1, 1, 1, 1, 1, 1}
			if explicit {
				want.identities = 2
			}
			if got := readImportEffects(t, s); got != want {
				t.Fatalf("successful import effects: got %+v, want %+v", got, want)
			}
			if _, err = s.SyncLifecycle(ctx, importProduct, importInstance, Lifecycle{
				LocalUserID: request.LocalUserID, OperationID: "changed-password", PreviousAuthVersion: 3, NextAuthVersion: 4,
			}); err != nil {
				t.Fatal(err)
			}
			replay, err := s.Import(ctx, importProduct, importInstance, request)
			if err != nil || replay.AuthVersion != 4 || replay.Access.Version != out.Access.Version || replay.Binding != out.Binding {
				t.Fatalf("replay changed imported identity: %+v, %v", replay, err)
			}
			request.SnapshotDigest = strings.Repeat("b", 64)
			if _, err = s.Import(ctx, importProduct, importInstance, request); !errors.Is(err, accounts.ErrConflict) {
				t.Fatalf("changed snapshot replay returned %v", err)
			}
			want.events = 2
			if got := readImportEffects(t, s); got != want {
				t.Fatalf("replay changed database state: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestReviewedImportDoesNotReplaceExistingProductAuthority(t *testing.T) {
	ctx := context.Background()
	s, request := reviewedImportFixture(t)
	request.CentralUserID = importCanonicalUser(t, s).ID
	if _, err := s.pool.Exec(ctx, `INSERT INTO product_user_access
		(user_id,product_id,instance_id,roles,active,version,permission_overrides)
		VALUES($1,$2,$3,'["member"]',false,1,'{"user":{"read":false}}')`, request.CentralUserID, importProduct, importInstance); err != nil {
		t.Fatal(err)
	}
	before := readImportEffects(t, s)
	if _, err := s.Import(ctx, importProduct, importInstance, request); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("import over an established assignment returned %v", err)
	}
	var untouched bool
	if err := s.pool.QueryRow(ctx, `SELECT NOT active AND version=1 AND product_role=1 AND permission_overrides='{"user":{"read":false}}'
		FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`,
		request.CentralUserID, importProduct, importInstance).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("established assignment was changed: %v", err)
	}
	if got := readImportEffects(t, s); got != before {
		t.Fatalf("conflicting import changed database state: got %+v, want %+v", got, before)
	}
}

func TestReviewedImportPreservesMatchingExplicitAssignment(t *testing.T) {
	ctx := context.Background()
	s, request := reviewedImportFixture(t)
	request.CentralUserID = importCanonicalUser(t, s).ID
	if _, err := s.pool.Exec(ctx, `INSERT INTO product_user_access
		(user_id,product_id,instance_id,roles,active,version,product_role,permission_overrides,updated_at)
		VALUES($1,$2,$3,'["member"]',true,7,10,'{"user":{"write":false}}','2026-01-01T00:00:00Z')`,
		request.CentralUserID, importProduct, importInstance); err != nil {
		t.Fatal(err)
	}
	before := readImportEffects(t, s)
	if _, err := s.pool.Exec(ctx, `ALTER TABLE product_auth_events ADD CONSTRAINT reject_matching_import CHECK(event<>'import')`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Import(ctx, importProduct, importInstance, request)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "reject_matching_import" {
		t.Fatalf("matching assignment should reach the import event: %v", err)
	}
	if got := readImportEffects(t, s); got != before {
		t.Fatalf("failed import changed database state: got %+v, want %+v", got, before)
	}
	if _, err := s.pool.Exec(ctx, `ALTER TABLE product_auth_events DROP CONSTRAINT reject_matching_import`); err != nil {
		t.Fatal(err)
	}

	imported, err := s.Import(ctx, importProduct, importInstance, request)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Binding.UserID != request.CentralUserID || imported.Binding.LocalUserID != request.LocalUserID ||
		imported.AuthVersion != request.AuthVersion || imported.ProductRole != 10 || !imported.Access.Active ||
		imported.Access.Version != 8 || !slices.Equal(imported.Access.Permissions, []string{"product:use", "user:read"}) {
		t.Fatalf("import lost the established assignment or native identity: %+v", imported)
	}
	replay, err := s.Import(ctx, importProduct, importInstance, request)
	if err != nil || replay.Binding != imported.Binding || replay.Access.Version != imported.Access.Version {
		t.Fatalf("import replay changed identity: %+v, %v", replay, err)
	}
	var untouched bool
	if err = s.pool.QueryRow(ctx, `SELECT roles='["member"]' AND active AND version=7 AND product_role=10
		AND permission_overrides='{"user":{"write":false}}' AND updated_at='2026-01-01T00:00:00Z'
		FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`,
		request.CentralUserID, importProduct, importInstance).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("established assignment was changed: %v", err)
	}
	want := before
	want.identities++
	want.bindings++
	want.subjects++
	want.events++
	if got := readImportEffects(t, s); got != want {
		t.Fatalf("unexpected binding effects: got %+v, want %+v", got, want)
	}
}

func TestReviewedImportRequiresExactExplicitAssignment(t *testing.T) {
	for _, tt := range []struct {
		name, roles, overrides string
		active                 bool
		role                   int
	}{
		{"roles differ", `[]`, `{"user":{"write":false}}`, true, 10},
		{"active differs", `["member"]`, `{"user":{"write":false}}`, false, 10},
		{"product role differs", `["member"]`, `{"user":{"write":false}}`, true, 1},
		{"overrides differ", `["member"]`, `{}`, true, 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, request := reviewedImportFixture(t)
			request.CentralUserID = importCanonicalUser(t, s).ID
			if _, err := s.pool.Exec(ctx, `INSERT INTO product_user_access
				(user_id,product_id,instance_id,roles,active,version,product_role,permission_overrides)
				VALUES($1,$2,$3,$4,$5,7,$6,$7)`, request.CentralUserID, importProduct, importInstance,
				tt.roles, tt.active, tt.role, tt.overrides); err != nil {
				t.Fatal(err)
			}
			before := readImportEffects(t, s)
			if _, err := s.Import(ctx, importProduct, importInstance, request); !errors.Is(err, accounts.ErrConflict) {
				t.Fatalf("distinct assignment returned %v", err)
			}
			if got := readImportEffects(t, s); got != before {
				t.Fatalf("conflicting import changed database state: got %+v, want %+v", got, before)
			}
		})
	}
}

func TestReviewedImportDoesNotAdoptExistingBindingWithoutRegistration(t *testing.T) {
	ctx := context.Background()
	s, request := reviewedImportFixture(t)
	u := importCanonicalUser(t, s)
	request.CentralUserID = u.ID
	identity := accounts.Identity{Issuer: "https://hub.test/product-auth/llmrouter/import-atomic", Subject: request.LocalUserID}
	if _, err := s.pool.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3)`, identity.Issuer, identity.Subject, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.users.EnsureRuntimeBinding(ctx, importProduct, importInstance, request.LocalUserID, "existing-binding", identity, u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO product_user_access
		(user_id,product_id,instance_id,roles,active,version,product_role,permission_overrides)
		VALUES($1,$2,$3,'["member"]',true,7,10,'{"user":{"write":false}}')`, u.ID, importProduct, importInstance); err != nil {
		t.Fatal(err)
	}
	before := readImportEffects(t, s)
	if _, err := s.Import(ctx, importProduct, importInstance, request); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("import over an existing unregistered binding returned %v", err)
	}
	if got := readImportEffects(t, s); got != before {
		t.Fatalf("conflicting import changed database state: got %+v, want %+v", got, before)
	}
}

func TestReviewedImportPreservesCentrallyAssignedDefaultRole(t *testing.T) {
	ctx := context.Background()
	s, request := reviewedImportFixture(t)
	u, err := s.accounts.EnsureIdentity(ctx, accounts.Identity{
		Issuer: "https://hub.test/product-auth/llmrouter/import-atomic", Subject: request.LocalUserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := productusers.Registry{importProduct + "/" + importInstance: s.users.Products()[0]}
	s.users = productusers.New(s.pool, registry, "https://hub.test", []string{u.ID})
	if _, err := s.users.SetAccess(ctx, u.ID, u.ID, importProduct, importInstance, []string{"member"}, true, 0); err != nil {
		t.Fatal(err)
	}
	registered, err := s.Register(ctx, importProduct, importInstance, request.Registration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, importProduct, importInstance, request); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("import over centrally assigned defaults returned %v", err)
	}
	out, err := s.Lookup(ctx, importProduct, importInstance, request.LocalUserID)
	if err != nil || out.ProductRole != 1 || out.Access.Version != registered.Access.Version {
		t.Fatalf("centrally assigned defaults changed: %+v, %v", out, err)
	}
}

func TestReviewedImportCompletesUnchangedRegistration(t *testing.T) {
	ctx := context.Background()
	s, request := reviewedImportFixture(t)
	registered, err := s.Register(ctx, importProduct, importInstance, request.Registration)
	if err != nil {
		t.Fatal(err)
	}
	request.Active = false
	imported, err := s.Import(ctx, importProduct, importInstance, request)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Binding != registered.Binding || imported.Access.Active || imported.ProductRole != 10 || imported.AuthVersion != 3 {
		t.Fatalf("import lost the reviewed inactive identity: %+v", imported)
	}
	if got := readImportEffects(t, s); got != (importEffects{1, 1, 1, 1, 1, 1, 1, 1}) {
		t.Fatalf("import duplicated registered data: %+v", got)
	}
}

func TestReviewedImportWaitsForProductAuthorityWriter(t *testing.T) {
	s, request := reviewedImportFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registered, err := s.Register(ctx, importProduct, importInstance, request.Registration)
	if err != nil {
		t.Fatal(err)
	}

	// User administration locks the target before inspecting or changing access.
	admin, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(admin)
	var blocker int
	if err = admin.QueryRow(ctx, `SELECT pg_backend_pid() FROM users WHERE id=$1 FOR UPDATE`, registered.Binding.UserID).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Import(ctx, importProduct, importInstance, request)
		done <- err
	}()
	waitForImportBlock(t, ctx, s, blocker, done)
	if _, err = admin.Exec(ctx, `UPDATE product_user_access SET active=false,version=2 WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`,
		registered.Binding.UserID, importProduct, importInstance); err != nil {
		t.Fatal(err)
	}
	if err = admin.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("import competing with a product authority writer returned %v", err)
	}
}

func TestReviewedImportWaitsForRuntimeBindingLockBeforePublishing(t *testing.T) {
	s, request := reviewedImportFixture(t)
	request.CentralUserID = importCanonicalUser(t, s).ID
	before := readImportEffects(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(lock)
	var blocker int
	if err = lock.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(hashtextextended($1,0))`,
		"runtime-binding:llmrouter/import-atomic").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Import(ctx, importProduct, importInstance, request)
		done <- err
	}()
	waitForImportBlock(t, ctx, s, blocker, done)
	if got := readImportEffects(t, s); got != before {
		t.Errorf("import published effects before acquiring the runtime binding lock: got %+v, want %+v", got, before)
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func waitForImportBlock(t *testing.T, ctx context.Context, s *Service, blocker int, done <-chan error) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_locks WHERE NOT granted AND $1=ANY(pg_blocking_pids(pid)))`,
			blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("import finished while the writer lock was held: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}
