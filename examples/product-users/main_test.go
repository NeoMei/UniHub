package main

import (
	"context"
	"errors"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sampleAccess() userkit.Access {
	return userkit.Access{Issuer: "http://127.0.0.1:58118", Subject: "user-a", ProductID: "notes", InstanceID: "default", User: userkit.User{ID: "user-a", Name: "A"}, Roles: []string{"member"}, Permissions: []string{"product:use", "article:create"}, Version: 1, Active: true, ExpiresAt: time.Now().Add(time.Hour).Unix()}
}

type fixedProvider struct{ a userkit.Access }

func (f fixedProvider) Resolve(context.Context, string) (userkit.Access, error) { return f.a, nil }
func TestProvisioningIsDurableAtomicAndConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, e := s.Ensure(context.Background(), sampleAccess())
			if e != nil {
				t.Error(e)
				return
			}
			ids <- u.ID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate identity")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.Ensure(context.Background(), sampleAccess())
	if err != nil || u.ID != first {
		t.Fatal("identity not durable")
	}
	record, err := s.profile(u.ID)
	if err != nil || record.Notebook != "Personal notebook" || record.Initializations != 1 {
		t.Fatalf("default resources changed: %+v", record)
	}
	other := sampleAccess()
	other.Subject = "user-b"
	other.User.ID = "user-b"
	v, err := s.Ensure(context.Background(), other)
	if err != nil || v.ID == u.ID {
		t.Fatal("identity collision")
	}
}
func TestResourcePolicyCannotOverrideCentralDeny(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := sampleAccess()
	svc, _ := userkit.NewService(fixedProvider{a}, s, s)
	p, err := svc.Authenticate(context.Background(), "proof")
	if err != nil {
		t.Fatal(err)
	}
	if err = p.RequirePermission(context.Background(), "article:create", p.LocalUser.ID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.RequirePermission(context.Background(), "article:create", "another-user"), userkit.ErrDenied) {
		t.Fatal("cross-user resource allowed")
	}
	a.Permissions = nil
	svc, _ = userkit.NewService(fixedProvider{a}, s, s)
	p, _ = svc.Authenticate(context.Background(), "proof")
	if !errors.Is(p.RequirePermission(context.Background(), "article:create", p.LocalUser.ID), userkit.ErrDenied) {
		t.Fatal("central deny overridden")
	}
}
func TestExplicitConfigRequired(t *testing.T) {
	t.Setenv("PRODUCT_CONFIG", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("implicit config accepted")
	}
}

func TestFailedInitializationPublishesNothing(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := s.path
	s.path = filepath.Join(path, "missing", "product.db")
	if _, err = s.Ensure(context.Background(), sampleAccess()); err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	if len(s.rows) != 0 {
		t.Fatal("failed transaction published identity")
	}
	s.path = path
	u, err := s.Ensure(context.Background(), sampleAccess())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := s.profile(u.ID)
	if record.Initializations != 1 {
		t.Fatal("defaults were not atomic")
	}
}
