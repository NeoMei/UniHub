package paymentconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unihub/internal/accounts"
)

func TestKeyFileRejectsSymlinkDotDotAndPreservesCanonicalBytes(t *testing.T) {
	dir := paymentTestDir(t)
	checked := filepath.Join(dir, "checked")
	followed := filepath.Join(dir, "followed")
	nested := filepath.Join(followed, "nested")
	for _, p := range []string{checked, nested, filepath.Join(checked, "child")} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	first, second := bytes.Repeat([]byte("a"), 32), bytes.Repeat([]byte("b"), 32)
	checkedKey, followedKey := filepath.Join(checked, "key"), filepath.Join(followed, "key")
	if e := os.WriteFile(checkedKey, first, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(followedKey, second, 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(checked, "link")
	if e := os.Symlink(nested, link); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		path  string
		bytes []byte
	}{{checkedKey, first}, {followedKey, second}} {
		got, e := readKey(tc.path)
		if e != nil || !bytes.Equal(got, tc.bytes) {
			t.Fatal("canonical key did not return its checked bytes", e)
		}
	}
	beforeChecked, e := os.Stat(checkedKey)
	if e != nil {
		t.Fatal(e)
	}
	beforeFollowed, e := os.Stat(followedKey)
	if e != nil {
		t.Fatal(e)
	}
	// Build literal traversal components: filepath.Join would remove the exploit.
	for _, bad := range []string{checked + "/link/../key", checked + "/./key", checked + "//key", checked + "/child/../key"} {
		got, e := readKey(bad)
		if !errors.Is(e, ErrUnavailable) || got != nil {
			t.Errorf("ambiguous key path accepted: error=%v, returned_bytes=%d", e, len(got))
		}
		if ValidManagedConfiguration(bad, "https://hub.example") {
			t.Error("startup validator accepted ambiguous key path")
		}
	}
	for _, tc := range []struct {
		path   string
		bytes  []byte
		before os.FileInfo
	}{{checkedKey, first, beforeChecked}, {followedKey, second, beforeFollowed}} {
		after, e := os.Stat(tc.path)
		if e != nil {
			t.Fatal(e)
		}
		got, e := os.ReadFile(tc.path)
		if e != nil || !os.SameFile(tc.before, after) || after.Mode() != tc.before.Mode() || !bytes.Equal(got, tc.bytes) {
			t.Fatal("rejected key path mutated original fixture", e)
		}
	}
	if target, e := os.Readlink(link); e != nil || target != nested {
		t.Fatal("rejected path mutated symlink", e)
	}
}

func TestMerchantDecodeRejectsNestedCredentialDuplicates(t *testing.T) {
	for _, value := range []string{
		`{"PUB_KEY_ID_1":"first","PUB_KEY_ID_1":"second"}`,
		`{"PUB_KEY_ID_1":"first","PUB_KEY_ID_\u0031":"second"}`,
	} {
		r := UpdateRequest{Reason: "unchanged"}
		e := json.Unmarshal([]byte(`{"expected_version":0,"enabled":true,"reason":"save","platform_public_keys":`+value+`}`), &r)
		if !errors.Is(e, accounts.ErrInvalid) {
			t.Errorf("nested duplicate accepted: %v", e)
		}
		if r.Reason != "unchanged" {
			t.Error("rejected decode mutated request")
		}
	}
	var r UpdateRequest
	if e := json.Unmarshal([]byte(`{"expected_version":0,"enabled":true,"reason":"save","platform_public_keys":{"PUB_KEY_ID_A":"first","PUB_KEY_ID_a":"second"}}`), &r); e != nil || len(r.PlatformPublicKeys) != 2 {
		t.Fatal("case-sensitive platform key IDs collapsed", e)
	}
}
