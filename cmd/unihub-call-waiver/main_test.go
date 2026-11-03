package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApprovalFileRejectsPublicOrSymlinkInput(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "approval.json")
	if e := os.WriteFile(p, []byte(`{}`), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := readPrivateApproval(p); e == nil {
		t.Fatal("public approval accepted")
	}
	if e := os.Chmod(p, 0600); e != nil {
		t.Fatal(e)
	}
	if b, e := readPrivateApproval(p); e != nil || string(b) != "{}" {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "symlink")
	if e := os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	if _, e := readPrivateApproval(link); e == nil {
		t.Fatal("symlink approval accepted")
	}
}
func TestWaiverCLIRequiresExplicitActionAndRoot(t *testing.T) {
	if e := run(nil, func(string) string { return "" }, 1000); e == nil {
		t.Fatal("nonroot caller accepted")
	}
	if e := run([]string{"--apply", "--install-schema"}, func(string) string { return "" }, 0); e == nil {
		t.Fatal("multiple actions accepted")
	}
}
