package paymentconfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The deployment owns key provisioning and backup. Runtime never creates or rotates it.
func readKey(path string) ([]byte, error) {
	f, e := openKeyFile(path)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0400 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() != 32 {
		return nil, ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return nil, ErrUnavailable
	}
	key, e := io.ReadAll(io.LimitReader(f, 33))
	if e != nil || len(key) != 32 {
		return nil, ErrUnavailable
	}
	return key, nil
}

// Traverse one canonical component at a time using pinned directory descriptors.
// Root can resolve relative symlinks internally, so no-follow Lstat checks alone
// are insufficient: verify each opened descriptor is the exact checked inode.
// No key bytes are read until this identity check and the file checks pass.
func openKeyFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrUnavailable
	}
	root, e := os.OpenRoot("/")
	if e != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(path[1:], "/")
	for _, part := range parts[:len(parts)-1] {
		checked, e := root.Lstat(part)
		if e != nil || !checked.IsDir() {
			return nil, ErrUnavailable
		}
		child, e := root.OpenRoot(part)
		if e != nil {
			return nil, ErrUnavailable
		}
		opened, e := child.Stat(".")
		if e != nil || !os.SameFile(checked, opened) {
			_ = child.Close()
			return nil, ErrUnavailable
		}
		_ = root.Close()
		root = child
	}
	name := parts[len(parts)-1]
	checked, e := root.Lstat(name)
	if e != nil || !checked.Mode().IsRegular() {
		return nil, ErrUnavailable
	}
	file, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrUnavailable
	}
	opened, e := file.Stat()
	if e != nil || !os.SameFile(checked, opened) {
		_ = file.Close()
		return nil, ErrUnavailable
	}
	return file, nil
}

func aead(key []byte) (cipher.AEAD, error) {
	c, e := aes.NewCipher(key)
	if e != nil {
		return nil, ErrUnavailable
	}
	return cipher.NewGCM(c)
}
func seal(key []byte, id string, version int64, raw []byte) ([]byte, error) {
	a, e := aead(key)
	if e != nil {
		return nil, ErrUnavailable
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, ErrUnavailable
	}
	return a.Seal(nonce, nonce, raw, []byte(fmt.Sprintf("unihub:payment-config:v1:%s:%d", id, version))), nil
}
func unseal(key []byte, id string, version int64, encrypted []byte) ([]byte, error) {
	a, e := aead(key)
	if e != nil || len(encrypted) < a.NonceSize() {
		return nil, ErrUnavailable
	}
	raw, e := a.Open(nil, encrypted[:a.NonceSize()], encrypted[a.NonceSize():], []byte(fmt.Sprintf("unihub:payment-config:v1:%s:%d", id, version)))
	if e != nil {
		return nil, ErrUnavailable
	}
	return raw, nil
}

// ValidManagedConfiguration checks trusted startup inputs without reading the database.
func ValidManagedConfiguration(keyFile, origin string) bool {
	if !validOrigin(origin) {
		return false
	}
	_, e := readKey(keyFile)
	return e == nil
}
