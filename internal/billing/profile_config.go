package billing

import (
	"bytes"
	"encoding/json"
	"io"
)

// ParseBillingProfiles accepts either a preset or a complete component policy.
// It is independent of identity clients and contains no runtime credentials.
func ParseBillingProfiles(raw []byte) ([]BillingProfile, error) {
	type entry struct {
		ProductID       string            `json:"product_id"`
		Version         int64             `json:"version"`
		Preset          string            `json:"preset,omitempty"`
		Subscription    *BillingComponent `json:"subscription,omitempty"`
		IncludedCredits *BillingComponent `json:"included_credits,omitempty"`
		Addons          *BillingComponent `json:"addons,omitempty"`
		Usage           *UsagePolicy      `json:"usage,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var entries []entry
	if len(raw) > 1<<20 || dec.Decode(&entries) != nil || entries == nil || len(entries) > 100 || dec.Decode(&struct{}{}) != io.EOF {
		return nil, ErrInvalid
	}
	out := make([]BillingProfile, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ProductID] || e.Version < 1 {
			return nil, ErrInvalid
		}
		seen[e.ProductID] = true
		p := BillingProfile{ProductID: e.ProductID, Version: e.Version}
		if e.Preset != "" {
			if e.Subscription != nil || e.IncludedCredits != nil || e.Addons != nil || e.Usage != nil {
				return nil, ErrInvalid
			}
			var err error
			p, err = BillingPreset(e.ProductID, e.Preset)
			if err != nil {
				return nil, err
			}
			p.Version = e.Version
		} else {
			if e.Subscription == nil || e.IncludedCredits == nil || e.Addons == nil || e.Usage == nil {
				return nil, ErrInvalid
			}
			if e.Subscription != nil {
				p.Subscription = *e.Subscription
			}
			if e.IncludedCredits != nil {
				p.IncludedCredits = *e.IncludedCredits
			}
			if e.Addons != nil {
				p.Addons = *e.Addons
			}
			if e.Usage != nil {
				p.Usage = *e.Usage
			}
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
