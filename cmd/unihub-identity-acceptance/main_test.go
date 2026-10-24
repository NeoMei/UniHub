package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateOutputRefusesExistingFilesAndSymlinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "markers.json")
	if err = privateOutput(path, []byte("synthetic-marker")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private output permissions")
	}
	if err = privateOutput(path, []byte("replacement")); err == nil {
		t.Fatal("overwrote existing private evidence")
	}
	before, _ := os.ReadFile(path)
	if string(before) != "synthetic-marker" {
		t.Fatal("existing content changed")
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err = privateOutput(link, []byte("replacement")); err == nil {
		t.Fatal("followed symlink")
	}
	folder := filepath.Join(dir, "linked-directory")
	if err = os.Symlink(dir, folder); err != nil {
		t.Fatal(err)
	}
	if err = privateOutput(filepath.Join(folder, "new"), []byte("replacement")); err == nil {
		t.Fatal("followed parent symlink")
	}
}
