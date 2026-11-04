package httpapi

import (
	"net/http/httptest"
	"testing"
	"unihub/internal/billing"
)

func TestCallQueryAllowsExplicitRuntimeAsset(t *testing.T) {
	for _, asset := range []string{billing.CreditAsset, billing.CashAsset, billing.RuntimeAsset} {
		r := httptest.NewRequest("GET", "/v1/accounts/account/calls?asset="+asset, nil)
		filter, _, e := callQuery(r, true)
		if e != nil || filter.Asset != asset {
			t.Fatalf("asset %s: %+v %v", asset, filter, e)
		}
	}
	if _, _, e := callQuery(httptest.NewRequest("GET", "/v1/accounts/account/calls?asset=USD/2", nil), true); e == nil {
		t.Fatal("unsupported asset accepted")
	}
}
