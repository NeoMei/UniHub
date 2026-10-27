package productauth

import (
	"context"
	"testing"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

func TestBrokerRejectsUnregisteredRedirectAndCrossScope(t *testing.T) {
	registry, _ := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	s := &Service{users: productusers.New(nil, registry, "https://hub.test", nil)}
	s.ConfigureBrowserClients(map[string]products.Client{"router": {ClientID: "router", ProductID: "llmrouter", InstanceID: "default", BrowserSSO: true, RedirectURIs: []string{"https://router.test/api/unihub/callback"}}})
	if _, e := s.BrowserClient("router", "https://evil.test/callback"); e == nil {
		t.Fatal("unregistered redirect accepted")
	}
	if _, e := s.BrowserClient("unknown", "https://router.test/api/unihub/callback"); e == nil {
		t.Fatal("unknown browser client accepted")
	}
	if _, e := s.ApproveBrowserHandoff(context.Background(), "upa_invalid", "other", "default", BrowserApproval{ClientID: "router"}); e == nil {
		t.Fatal("cross-product service authority accepted")
	}
}
