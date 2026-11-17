package apnspush

import (
	"context"
	"crypto/elliptic"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sideshow/apns2"
	"isoform-relay/internal/push"
)

// momentClaim is a queued moment as the store hands it to the worker.
func momentClaim(c Config, moment, level string) push.Claim {
	claim := testClaim(c)
	claim.Kind = push.KindMoment
	claim.EventID = "event:9"
	claim.Notice = push.Notice{
		ID:       "6f1c2e4a-7b3d-4c5e-9a1b-2d3e4f5a6b7c",
		Moment:   moment,
		CT:       strings.Repeat("Q", 64),
		Category: push.CategoryDecision,
		Collapse: "decision:req-9",
		Level:    level,
	}
	return claim
}

func TestMomentPayloadCarriesRoutingKeysAndSealedTextOnly(t *testing.T) {
	c, _ := testConfig(t, elliptic.P256())
	claim := momentClaim(c, "decision_due", push.LevelTimeSensitive)
	payload, err := notificationFor(claim)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"aps":{"alert":{"title":"Isoform","body":"Isoform needs a decision."},"sound":"default","mutable-content":1,` +
		`"category":"YANSU_DECISION","thread-id":"` + claim.HubID + `","interruption-level":"time-sensitive"},` +
		`"yansu":{"kind":"decision_due","hub":"` + claim.HubID + `","id":"` + claim.Notice.ID + `","dev":"` + claim.DeviceID + `","ct":"` + claim.Notice.CT + `"}}`
	var got, expected any
	if err = json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("moment payload %s, want %s", body, want)
	}
	if strings.Contains(string(body), `"eid"`) {
		t.Fatal("a moment payload carries the release-one event id")
	}
}

func TestMomentLevelDrivesSoundInterruptionPriorityAndCollapse(t *testing.T) {
	c, _ := testConfig(t, elliptic.P256())
	tests := []struct {
		level, sound string
		priority     int
	}{
		{push.LevelPassive, "", apns2.PriorityLow},
		{push.LevelActive, "default", apns2.PriorityHigh},
		{push.LevelTimeSensitive, "default", apns2.PriorityHigh},
	}
	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			claim := momentClaim(c, "work_finished", tc.level)
			payload, err := notificationFor(claim)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Aps.Sound != tc.sound || payload.Aps.InterruptionLevel != tc.level {
				t.Fatalf("aps %+v", payload.Aps)
			}
			if got := priorityFor(claim); got != tc.priority {
				t.Fatalf("apns-priority %d, want %d", got, tc.priority)
			}
			if got := collapseFor(claim); got != "decision:req-9" {
				t.Fatalf("collapse %q, want the hub's collapse id", got)
			}
		})
	}
	// Brief and needs_you keep their release-two headers.
	brief := testClaim(c)
	if priorityFor(brief) != apns2.PriorityHigh || collapseFor(brief) != brief.NotificationID {
		t.Fatal("brief headers changed")
	}
}

func TestMomentFallbackTextNamesNoContentPerKind(t *testing.T) {
	c, _ := testConfig(t, elliptic.P256())
	tests := map[string]string{
		"decision_due":        "Isoform needs a decision.",
		"commitment_slipping": "A commitment needs attention.",
		"meeting_ended":       "Your meeting notes are ready.",
		"person_reappeared":   "Someone you know came up.",
		"goal_check_in":       "A quick check on a goal.",
		"work_finished":       "Work finished.",
		"work_blocked":        "Work is waiting on you.",
		"learned_to_confirm":  "Isoform learned something.",
		"capture_paused":      "Isoform stopped watching for a while.",
		"brief_missed":        "Your brief did not arrive.",
		"weekly_recap":        "Your week is ready.",
		"chat_answer":         "Isoform answered your messages.",
		"schedule_missed":     "A schedule did not run.",
		"suggestion":          "Isoform has a suggestion.",
		"waiting_on_reply":    "Someone is waiting on your reply.",
		"playbook_suggested":  "Isoform can take a routine off your hands.",
		"playbook_result":     "A playbook finished.",
		"topic_finds":         "Something new on a topic you follow.",
		"something_new":       "Isoform has something for you.",
	}
	for moment, want := range tests {
		payload, err := notificationFor(momentClaim(c, moment, push.LevelActive))
		if err != nil {
			t.Fatalf("%s: %v", moment, err)
		}
		if payload.Aps.Alert.Body != want {
			t.Fatalf("%s fallback %q, want %q", moment, payload.Aps.Alert.Body, want)
		}
	}
}

func TestIncompleteMomentIsPermanentAndNeverSent(t *testing.T) {
	c, _ := testConfig(t, elliptic.P256())
	client, err := NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	missing := momentClaim(c, "decision_due", push.LevelActive)
	missing.Notice.CT = ""
	if outcome, err := client.Send(context.Background(), missing); outcome != push.Permanent || err != errIncompleteNotice {
		t.Fatalf("incomplete moment gave %v (%v)", outcome, err)
	}
	// Bypassing Prepare cannot exceed the APNs limit: the payload is refused first.
	oversized := momentClaim(c, "decision_due", push.LevelActive)
	oversized.Notice.CT = strings.Repeat("Q", 4000)
	if outcome, err := client.Send(context.Background(), oversized); outcome != push.Permanent || err != errPayloadTooLarge {
		t.Fatalf("oversized moment gave %v (%v)", outcome, err)
	}
}

func TestMomentRequestHeadersAndBodyFollowTheNotice(t *testing.T) {
	c, _ := testConfig(t, elliptic.P256())
	claim := momentClaim(c, "goal_check_in", push.LevelPassive)
	requests := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.ProtoMajor != 2 || r.URL.Path != "/3/device/"+claim.Token {
			t.Error("wrong APNs request transport or path")
		}
		for header, want := range map[string]string{
			"apns-topic":       c.Topic,
			"apns-id":          claim.NotificationID,
			"apns-collapse-id": "decision:req-9",
			"apns-push-type":   "alert",
			"apns-priority":    "5",
		} {
			if r.Header.Get(header) != want {
				t.Errorf("header %s is %q, want %q", header, r.Header.Get(header), want)
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) > push.MaxPayloadBytes {
			t.Error("invalid payload bound")
		}
		if strings.Contains(string(body), claim.Token) {
			t.Error("device token travels in the payload")
		}
		if !strings.Contains(string(body), `"ct":"`+claim.Notice.CT+`"`) || strings.Contains(string(body), `"sound"`) {
			t.Errorf("passive payload %s", body)
		}
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
		t.Fatalf("moment send: %v %v", outcome, err)
	}
	if requests != 1 {
		t.Fatal("request not observed")
	}
}

// A withdraw is a background push with only the ids (PROTOCOL.md 9.4).
func TestWithdrawPayloadIsBackgroundWithTheIDs(t *testing.T) {
	ids := "7c9e6679-7425-40de-944b-e07fc1f90ae7 brief:2026-11-15-evening"
	n, err := withdrawPayloadFor(push.Claim{Kind: push.KindWithdraw, HubID: "7c9e6679-7425-40de-944b-e07fc1f90ae8", Notice: push.Notice{CT: ids}})
	if err != nil || n.Aps.ContentAvailable != 1 || n.Yansu.Kind != "withdraw" || len(n.Yansu.IDs) != 2 || n.Yansu.IDs[1] != "brief:2026-11-15-evening" {
		t.Fatalf("withdraw = %+v %v", n, err)
	}
	if _, err := withdrawPayloadFor(push.Claim{Kind: push.KindWithdraw, Notice: push.Notice{CT: "nope nope"}}); err == nil {
		t.Fatal("bad ids made a payload")
	}
}
