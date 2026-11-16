package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestForgetAgentOfYouProfileSendsTheDeleteAndWaitsForTheServer(t *testing.T) {
	deletes := 0
	up := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/owner/profile" && r.Header.Get("Authorization") == "Bearer test-token" {
			deletes++
			_, _ = w.Write([]byte(`{"deleted":true}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: yourAgentEnv{stateFile: filepath.Join(dir, "identity.json")}, client: client, cachePath: filepath.Join(dir, "cache.json")}

	// Never used Agent of You: nothing to delete, nothing sent.
	if err := forgetAgentOfYouProfileWith(context.Background(), env); err != nil || deletes != 0 {
		t.Fatalf("no state: err=%v deletes=%d", err, deletes)
	}
	if err := writeYourAgentStateLocked(env.reservation.stateFile, &yourAgentStateFile{Hub: "test-hub", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	up = false
	if err := forgetAgentOfYouProfileWith(context.Background(), env); err == nil {
		t.Fatal("a server that can't take the delete must leave the step failing")
	}
	up = true
	if err := forgetAgentOfYouProfileWith(context.Background(), env); err != nil || deletes != 1 {
		t.Fatalf("delete: err=%v deletes=%d", err, deletes)
	}
}
