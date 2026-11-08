package paymentconfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// The deployment owns key provisioning and backup. Runtime never creates or rotates it.
func readKey(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnavailable
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "payment-configuration-key")
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
