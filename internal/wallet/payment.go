package wallet

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

func SignReceipt(key []byte, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(timestamp))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}
func VerifyReceipt(key []byte, timestamp, signature string, body []byte, now time.Time) error {
	invalid := errors.New("invalid payment signature")
	if len(key) < 32 {
		return invalid
	}
	unix, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil {
		return invalid
	}
	delta := now.Sub(time.Unix(unix, 0))
	if delta > 5*time.Minute || delta < -5*time.Minute {
		return invalid
	}
	got, e := hex.DecodeString(signature)
	if e != nil {
		return invalid
	}
	want, _ := hex.DecodeString(SignReceipt(key, timestamp, body))
	if !hmac.Equal(got, want) {
		return invalid
	}
	return nil
}
