package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"unihub/internal/cashtopup"
)

// A cash checkout that cannot reach a configured payment provider must report
// payment_not_configured, never a generic service_unavailable that makes the
// client claim online payment is unsupported.
func TestCashTopupPaymentUnavailableErrorContract(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, cashtopup.ErrPaymentUnavailable)
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "{\"error\":\"payment_not_configured\"}\n" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
