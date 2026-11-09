package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentServerAuthenticationAndRefusals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Isoform-Hub") != "test-hub" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authentication headers")
		}
		switch r.URL.Path {
		case "/v1/owner":
			_, _ = w.Write([]byte(`{"state":"on"}`))
		case "/paused":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"paused"}`))
		case "/refused":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"token"}`))
		}
	}))
	defer server.Close()
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.do(context.Background(), http.MethodGet, "/v1/owner", nil, "test-hub", "test-token")
	if err != nil || string(body) != `{"state":"on"}` {
		t.Fatalf("response %s, %v", body, err)
	}
	for _, test := range []struct{ path, kind, code string }{{"/paused", "paused", "paused"}, {"/refused", "refused", "token"}} {
		_, err := client.do(context.Background(), http.MethodGet, test.path, nil, "test-hub", "test-token")
		var refusal *agentServerError
		if !errors.As(err, &refusal) || refusal.Kind != test.kind || refusal.Code != test.code {
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
	server.Close()
	if _, err := client.do(context.Background(), http.MethodGet, "/v1/owner", nil, "test-hub", "test-token"); !errors.Is(err, errAgentServerOffline) {
		t.Fatalf("expected offline, got %v", err)
	}
}

func TestAgentServerRejectsUnsafeURLAndRedirect(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path"} {
		if _, err := newAgentServerClient(base); err == nil {
			t.Fatal("accepted unsafe URL")
		}
	}
	var redirected bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer server.Close()
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.do(context.Background(), http.MethodGet, "/v1/owner", nil, "test-hub", "test-token"); err == nil || redirected {
		t.Fatal("followed redirect")
	}
}
