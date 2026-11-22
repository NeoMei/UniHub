package httpapi

import (
	"slices"
	"testing"
)

func TestPublicPaymentStateMatchesRuntimeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sandbox   bool
		providers []string
		mode      string
		real      bool
		advertise []string
	}{
		{name: "unconfigured", mode: "unconfigured", advertise: []string{}},
		{name: "direct", providers: []string{"wechat", "alipay"}, mode: "direct", real: true, advertise: []string{"wechat", "alipay"}},
		{name: "sandbox", sandbox: true, providers: []string{"wechat", "alipay"}, mode: "sandbox", advertise: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, real := publicPaymentState(tc.sandbox, tc.providers)
			if mode != tc.mode || real != tc.real {
				t.Fatalf("state = %q, %t, want %q, %t", mode, real, tc.mode, tc.real)
			}
			if got := publicPaymentProviders(tc.sandbox, tc.providers); !slices.Equal(got, tc.advertise) {
				t.Fatalf("providers = %v, want %v", got, tc.advertise)
			}
		})
	}
}
