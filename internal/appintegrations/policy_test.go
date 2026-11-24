package appintegrations

import "testing"

func TestNativeIdentityPolicy(t *testing.T) {
	form := Input{ProductID: "example-wiki", Name: "Example Wiki", Origin: "https://wiki.example.org",
		EntryURL: "https://wiki.example.org/auth/start", CallbackURL: "https://wiki.example.org/auth/return",
		NativeLoginURL: "https://wiki.example.org/login"}
	app, err := normalize(form, "https://hub.example.org", false)
	if err != nil {
		t.Fatal(err)
	}
	if app.InstanceID != "default" || len(app.Manifest().DefaultRoles) != 0 || len(app.Client().Scopes) != 0 {
		t.Fatalf("unsafe identity defaults: %+v", app)
	}
	for _, change := range []func(*Input){
		func(i *Input) { i.CallbackURL = "https://other.example.org/auth/return" },
		func(i *Input) { i.CallbackURL += "?untrusted=1" },
		func(i *Input) { i.EntryURL = "https://wiki.example.org:443-evil.org/auth/start" },
		func(i *Input) { i.Origin = "http://127.0.0.1:3000" },
		func(i *Input) { i.Origin = "https://hub.example.org" },
		func(i *Input) { i.NativeLoginURL = "https://attacker@wiki.example.org/login" },
	} {
		in := form
		change(&in)
		if _, err := normalize(in, "https://hub.example.org", false); err == nil {
			t.Errorf("accepted unsafe configuration %+v", in)
		}
	}
}

func TestStateTransitionsRequireProductionEvidence(t *testing.T) {
	app := Application{State: "draft"}
	if app.allows("pilot") {
		t.Fatal("credential must exist before pilot")
	}
	app.CredentialConfigured = true
	if !app.allows("pilot") || app.allows("active") {
		t.Fatal("draft can only progress to pilot")
	}
	app.State, app.InboundVerified = "pilot", true
	if app.allows("active") {
		t.Fatal("the second login direction is required")
	}
	app.OutboundVerified = true
	if !app.allows("active") || !app.allows("paused") {
		t.Fatal("fully verified pilot can publish or pause")
	}
	app.State = "paused"
	if app.allows("active") || !app.allows("pilot") {
		t.Fatal("resuming requires pilot first")
	}
}
