package console

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"unihub/internal/billing"
	"unihub/internal/httpapi"
)

// The composed console must let the real payment handler reject unsigned
// notifications. A static 405 prevents every provider from delivering receipts.
func TestPaymentCallbacksReachRealAPIWithoutBrowserSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("console-index"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{StaticDir: dir}}
	api := httpapi.New(nil, nil, nil, httpapi.Options{Billing: billing.New(nil, nil, nil)})
	h := s.Handler(api)
	for _, provider := range []string{"wechat", "alipay"} {
		t.Run(provider, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/payments/"+provider+"/notify", strings.NewReader("unsigned fixture"))
			r.Header.Set("Origin", "https://provider.example.test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"error":"invalid_notification"`) {
				t.Fatalf("notification boundary=%d %s", w.Code, w.Body)
			}
		})
	}
	// Only the two exact POST paths bypass the static contract.
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/payments/wechat/notify", 200, "console-index"},
		{"PUT", "/payments/alipay/notify", 405, "Method Not Allowed"},
		{"POST", "/payments/wechat/notify/", 405, "Method Not Allowed"},
		{"POST", "/payments/alipay/notify/extra", 405, "Method Not Allowed"},
		{"POST", "/payments/other/notify", 405, "Method Not Allowed"},
		{"POST", "/payments/wechat/notify-adjacent", 405, "Method Not Allowed"},
	} {
		w := request(h, tc.method, tc.path, nil, "", "")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s %s=%d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}
