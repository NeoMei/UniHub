package productauth

import (
	"net/http"
	"strings"
	"testing"
)

func TestNativeBrowserHandoffCannotCompleteAcrossRolloutGeneration(t *testing.T) {
	s, handler, clients, _ := setupBrowserHandoffUI(t)
	client := clients["wiki"]
	s.ConfigureBrowserClients(clients)
	actor := nativeBrowserTestActor(t, s, "pilot-generation-source")
	legacy, legacyCookie := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki", "")

	client.NativeHandoffGeneration = "pilot-1"
	clients["wiki"] = client
	s.ConfigureBrowserClients(clients)
	session := strings.Repeat("a", 64)
	first, firstCookie := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki", session)
	second, secondCookie := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki", session)

	client.NativeHandoffGeneration = "disabled"
	clients["wiki"] = client
	s.ConfigureBrowserClients(clients)
	response := handoffUIRequest(t, handler, http.StatusUnauthorized, http.MethodGet, first.RequestURI(), nil, firstCookie)
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("a paused handoff changed browser cookies")
	}

	client.NativeHandoffGeneration = "pilot-2"
	clients["wiki"] = client
	s.ConfigureBrowserClients(clients)
	response = handoffUIRequest(t, handler, http.StatusUnauthorized, http.MethodGet, second.RequestURI(), nil, secondCookie)
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("an earlier generation changed browser cookies")
	}
	response = handoffUIRequest(t, handler, http.StatusUnauthorized, http.MethodGet, legacy.RequestURI(), nil, legacyCookie)
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("a legacy untagged approval changed browser cookies")
	}
	fresh, freshCookie := approveNativeBrowserTestHandoff(t, s, handler, actor, "wiki", session)
	response = handoffUIRequest(t, handler, http.StatusSeeOther, http.MethodGet, fresh.RequestURI(), nil, freshCookie)
	handoffUICookie(t, response, "unihub_session")
}
