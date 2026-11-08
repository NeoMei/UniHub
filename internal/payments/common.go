package payments

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maxBody = 1 << 20

var integer = regexp.MustCompile(`^[1-9][0-9]*$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
var refundIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var yuanPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$`)

func toFen(s string) (int64, error) {
	if !integer.MatchString(s) {
		return 0, ErrInvalid
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n%10000 != 0 {
		return 0, ErrInvalid
	}
	return n / 10000, nil
}
func fenYuan(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }
func fenAmount(n int64) (string, error) {
	if n <= 0 || n > math.MaxInt64/10000 {
		return "", ErrVerification
	}
	return strconv.FormatInt(n*10000, 10), nil
}
func yuanAmount(s string) (string, error) {
	if !yuanPattern.MatchString(s) {
		return "", ErrVerification
	}
	parts := strings.SplitN(s, ".", 2)
	frac := "00"
	if len(parts) == 2 {
		frac = (parts[1] + "0")[:2]
	}
	n, err := strconv.ParseInt(parts[0]+frac, 10, 64)
	if err != nil {
		return "", ErrVerification
	}
	return fenAmount(n)
}
func validRequest(r Request) error {
	if !identifier.MatchString(r.OrderID) || strings.TrimSpace(r.Description) == "" || len(r.Description) > 127 || !r.ExpiresAt.After(time.Now()) {
		return ErrInvalid
	}
	_, err := toFen(r.Amount)
	return err
}
func validRefund(r RefundRequest) error {
	if !identifier.MatchString(r.OrderID) || r.TransactionID == "" || len(r.TransactionID) > 64 || !refundIdentifier.MatchString(r.RefundID) || len(r.Reason) > 80 {
		return ErrInvalid
	}
	a, e := toFen(r.Amount)
	b, f := toFen(r.TotalAmount)
	if e != nil || f != nil || a > b {
		return ErrInvalid
	}
	return nil
}
func trustedHTTPS(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443")
}
func validPublicKey(k *rsa.PublicKey) bool {
	return k != nil && k.N != nil && k.N.BitLen() >= 2048 && k.E >= 3
}
func validPrivateKey(k *rsa.PrivateKey) bool {
	return k != nil && validPublicKey(&k.PublicKey) && k.Validate() == nil
}
func ParsePrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalid
	}
	var key *rsa.PrivateKey
	if k, e := x509.ParsePKCS8PrivateKey(block.Bytes); e == nil {
		key, _ = k.(*rsa.PrivateKey)
	} else {
		key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if !validPrivateKey(key) {
		return nil, ErrInvalid
	}
	return key, nil
}
func ParsePublicKeyPEM(data []byte) (*rsa.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalid
	}
	var key *rsa.PublicKey
	if cert, e := x509.ParseCertificate(block.Bytes); e == nil {
		if time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
			return nil, ErrInvalid
		}
		key, _ = cert.PublicKey.(*rsa.PublicKey)
	} else if k, e := x509.ParsePKIXPublicKey(block.Bytes); e == nil {
		key, _ = k.(*rsa.PublicKey)
	} else {
		key, _ = x509.ParsePKCS1PublicKey(block.Bytes)
	}
	if !validPublicKey(key) {
		return nil, ErrInvalid
	}
	return key, nil
}
func signRSA(key *rsa.PrivateKey, body []byte) (string, error) {
	sum := sha256.Sum256(body)
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if e != nil {
		return "", ErrUnavailable
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}
func verifyRSA(key *rsa.PublicKey, body []byte, signature string) error {
	if !validPublicKey(key) {
		return ErrVerification
	}
	sig, e := base64.StdEncoding.DecodeString(signature)
	if e != nil {
		return ErrVerification
	}
	sum := sha256.Sum256(body)
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return ErrVerification
	}
	return nil
}
func client(c *http.Client) *http.Client {
	result := http.Client{Timeout: 20 * time.Second}
	if c != nil {
		result = *c
		if result.Timeout <= 0 {
			result.Timeout = 20 * time.Second
		}
	}
	result.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &result
}
func readBody(r *http.Response) ([]byte, error) {
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if e != nil || len(b) > maxBody {
		return nil, ErrUnavailable
	}
	return b, nil
}

// strictJSON rejects duplicate names at every depth and trailing data before
// decoding. Signatures authenticate bytes, but must not permit ambiguous fields.
func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var walk func() error
	walk = func() error {
		tok, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				t, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := t.(string)
				k = strings.Map(func(r rune) rune {
					smallest := r
					for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
						if next < smallest {
							smallest = next
						}
					}
					return smallest
				}, k)
				if !ok || seen[k] {
					return ErrVerification
				}
				seen[k] = true
				if e = walk(); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return ErrVerification
			}
		case '[':
			for d.More() {
				if e := walk(); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return ErrVerification
			}
		default:
			return ErrVerification
		}
		return nil
	}
	if e := walk(); e != nil {
		return ErrVerification
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrVerification
	}
	if e := json.Unmarshal(b, v); e != nil {
		return ErrVerification
	}
	return nil
}
