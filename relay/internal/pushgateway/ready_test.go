package pushgateway

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"isoform-relay/internal/apnspush"
	"isoform-relay/internal/push"
	"isoform-relay/internal/pushgatewayauth"
)

// The gateway sends a ready wake (PROTOCOL.md 9.5) through with no notice:
// a hub cannot attach brief content to it.
func TestGatewayPassesAReadyWakeWithNoNotice(t *testing.T) {
	gatewayKey := testPrivateKey(t)
	hubKey := testPrivateKey(t)
	hubPub := base64.RawURLEncoding.EncodeToString(hubKey.PublicKey().Bytes())
	macKey, _, err := pushgatewayauth.DeriveHubKeyFromPrivate(hubKey, gatewayKey.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	grantKey, err := pushgatewayauth.DeriveGrantKey(gatewayKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{outcome: push.Accepted}
	server := httptest.NewServer(New("development", sender, gatewayKey))
	defer server.Close()
	claim := validClaim()
	claim.Kind = push.KindReady
	claim.GatewayGrant, err = pushgatewayauth.NewGrant(grantKey, claim.Environment, claim.Token, claim.HubID, hubPub, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	client := apnspush.NewGatewayClient(apnspush.GatewayConfig{
		URL: server.URL, Environment: "development", HubID: claim.HubID, HubPub: hubPub,
		HubKey: base64.RawURLEncoding.EncodeToString(macKey),
	})
	if outcome, err := client.Send(context.Background(), claim); err != nil || outcome != push.Accepted {
		t.Fatalf("ready = %q, %v", outcome, err)
	}
	if sender.claim.Kind != push.KindReady || sender.claim.Notice != (push.Notice{}) {
		t.Fatalf("handler received %+v", sender.claim)
	}
	sender.claim = push.Claim{}
	claim.Notice = push.Notice{CT: "sealed-brief-text"}
	if outcome, err := client.Send(context.Background(), claim); err == nil && outcome == push.Accepted {
		t.Fatal("a ready wake with a notice was sent")
	}
	if sender.claim.Kind != "" {
		t.Fatal("a ready wake with a notice reached the sender")
	}
}
