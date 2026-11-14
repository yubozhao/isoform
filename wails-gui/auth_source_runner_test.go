package main

import "testing"

// GetAuthSource and HasLocalCredentials follow the model runner. They used to
// read a probe flag that is never set while UseYansuGateway is on (forced for
// every install), so both reported no verified credentials for everyone.
func TestAuthSourceFollowsTheModelRunner(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	app := &App{}
	if got := app.GetAuthSource(); got.Source != "pending" || got.Verified {
		t.Fatalf("before the runner: %+v, want pending and unverified", got)
	}
	if app.HasLocalCredentials() {
		t.Fatal("HasLocalCredentials before the runner is up")
	}
	app.runner = &purposeExtractionFakeRunner{}
	app.ready = true
	if got := app.GetAuthSource(); got.Source != "native" || !got.Verified {
		t.Fatalf("with the runner up: %+v, want native and verified", got)
	}
	if !app.HasLocalCredentials() {
		t.Fatal("HasLocalCredentials false with the runner up")
	}
}
