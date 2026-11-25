package apnspush

import (
	"context"
	"crypto/elliptic"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"isoform-relay/internal/push"
)

// A ready wake (PROTOCOL.md 9.5) is a background push, priority 5, never
// collapsed, and its payload carries the hub id and nothing about the brief:
// no id, title, slot, time, alert or sound.
func TestReadyWakeIsABackgroundPushWithNoBriefContent(t *testing.T) {
	c, _ := testConfig(t, elliptic.P256())
	claim := testClaim(c)
	claim.Kind = push.KindReady
	requests := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		for header, want := range map[string]string{"apns-push-type": "background", "apns-priority": "5", "apns-collapse-id": ""} {
			if r.Header.Get(header) != want {
				t.Errorf("%s = %q, want %q", header, r.Header.Get(header), want)
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var got map[string]map[string]any
		if err = json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		keys := func(m map[string]any) []string {
			out := []string{}
			for k := range m {
				out = append(out, k)
			}
			return out
		}
		if len(got) != 2 || got["aps"] == nil || got["yansu"] == nil {
			t.Errorf("top-level keys: %s", body)
		}
		if !reflect.DeepEqual(got["aps"], map[string]any{"content-available": float64(1)}) {
			t.Errorf("aps = %v", keys(got["aps"]))
		}
		if !reflect.DeepEqual(got["yansu"], map[string]any{"kind": "ready", "hub": claim.HubID}) {
			t.Errorf("yansu = %v", keys(got["yansu"]))
		}
		w.Header().Set("apns-id", claim.NotificationID)
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client, err := NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.client.Host = server.URL
	client.client.HTTPClient.Transport = &boundedTransport{base: server.Client().Transport}
	if outcome, err := client.Send(context.Background(), claim); err != nil || outcome != push.Accepted {
		t.Fatalf("ready send: %v %v", outcome, err)
	}
	if requests != 1 {
		t.Fatal("request not observed")
	}
}
