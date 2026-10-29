package productauth

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
	"time"
)

func TestBrowserSourceProofTokenHasOpaqueServerOnlyShape(t *testing.T) {
	proof := newBrowserSourceProofToken()
	if len(proof) != len("ubsp_")+64 || !strings.HasPrefix(proof, "ubsp_") {
		t.Fatalf("source proof has wrong shape: %q", proof)
	}
	for _, c := range proof[len("ubsp_"):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("source proof is not lowercase hex: %q", proof)
		}
	}
}

type sourceSessionRow struct {
	expiry time.Time
	err    error
}

func (r sourceSessionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*time.Time)) = r.expiry
	return nil
}

type sourceSessionQuerier struct {
	query string
	args  []any
	row   sourceSessionRow
}

func (q *sourceSessionQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.query, q.args = sql, args
	return q.row
}

func TestCurrentBrowserSourceSessionUsesCentralSessionRevocationGates(t *testing.T) {
	q := &sourceSessionQuerier{row: sourceSessionRow{expiry: time.Now().Add(time.Minute)}}
	_, err := currentBrowserSourceSession(context.Background(), q, strings.Repeat("a", 64), "central-user")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"s.created_at>=u.session_not_before", "u.active", "s.expires_at>now()", "NOT EXISTS (SELECT 1 FROM identity_revocations"} {
		if !strings.Contains(q.query, required) {
			t.Fatalf("source session predicate missing %q", required)
		}
	}
	if len(q.args) != 2 || q.args[0] != strings.Repeat("a", 64) || q.args[1] != "central-user" {
		t.Fatalf("unexpected query args: %#v", q.args)
	}
}

func TestCurrentBrowserSourceSessionFailsClosed(t *testing.T) {
	want := pgx.ErrNoRows
	q := &sourceSessionQuerier{row: sourceSessionRow{err: want}}
	if _, err := currentBrowserSourceSession(context.Background(), q, strings.Repeat("a", 64), "central-user"); err != want {
		t.Fatalf("got %v want %v", err, want)
	}
}
