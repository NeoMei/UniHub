package paymentconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unihub/internal/accounts"
)

// Reject duplicate/case-folded members and explicit null updates. They must not
// silently turn a credential replacement into retention of the previous secret.
func (r *UpdateRequest) UnmarshalJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return accounts.ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		t, e := d.Token()
		if e != nil {
			return accounts.ErrInvalid
		}
		key, ok := t.(string)
		if !ok || seen[strings.ToLower(key)] {
			return accounts.ErrInvalid
		}
		seen[strings.ToLower(key)] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return accounts.ErrInvalid
		}
	}
	if _, e = d.Token(); e != nil {
		return accounts.ErrInvalid
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return accounts.ErrInvalid
	}
	type request UpdateRequest
	var v request
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil {
		return accounts.ErrInvalid
	}
	*r = UpdateRequest(v)
	r.provided = seen
	return nil
}
