package productusers

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestRuntimeBindingsNeverLinkByEmailOrReplaceLocalIdentity(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	s := New(p, nil, "https://central.test", nil)
	identity := accounts.Identity{Issuer: "https://issuer.test", Subject: "one", Email: "same@example.test"}
	u, e := a.EnsureIdentity(ctx, identity)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.EnsureRuntimeBinding(ctx, "llmrouter", "default", "42", "link-42", identity, u)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.EnsureRuntimeBinding(ctx, "llmrouter", "default", "42", "link-42", identity, u)
	if e != nil || replay.ID != b.ID {
		t.Fatalf("replay %v %v", replay, e)
	}
	identity.Subject = "two"
	other, e := a.EnsureIdentity(ctx, identity)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnsureRuntimeBinding(ctx, "llmrouter", "default", "42", "replace-42", identity, other); !errors.Is(e, accounts.ErrConflict) {
		t.Fatalf("replaced local user: %v", e)
	}
	if _, e = s.EnsureRuntimeBinding(ctx, "llmrouter", "default", "43", "duplicate-central", accounts.Identity{Issuer: "https://issuer.test", Subject: "one"}, u); !errors.Is(e, accounts.ErrConflict) {
		t.Fatalf("duplicated central user: %v", e)
	}
	missing, e := s.RuntimeBinding(ctx, "llmrouter", "other", "42")
	if !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("scope leak %+v %v", missing, e)
	}
}
