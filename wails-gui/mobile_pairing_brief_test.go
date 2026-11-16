package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// The first thing after pairing is the computer's latest brief (phone-setup
// v1.0.0 rule 7, B12): the approval names it, and its package follows at once
// for the new phone only, without a push, kept in the outbox until received.
func TestMobilePairingSendsTheLatestBrief(t *testing.T) {
	bridge, relay, conn, invitation := mobileTestOpenInvitation(t)
	relay.nextFrame(t, "pairing_open")
	brief := MobileBriefV1{Version: 1, BriefID: "2026-10-09-morning", Slot: "morning", LocalDate: "2026-10-09",
		AsOf: "2026-10-09T07:00:12Z", HubName: "Mac", NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "The plan shipped."}}}
	bridge.app.mobileBriefSvc = &mobileTestBriefSource{brief: &brief}

	phone := newMobileTestPhone(t)
	frame, kPair := phone.claimFrame(t, invitation, nil)
	conn.send(t, frame)
	mobileTestWaitFor(t, "a pending claim", func() bool { return bridge.connectionStatus().PendingPairing != nil })
	if err := bridge.confirmPairing(invitation.Pairing, true); err != nil {
		t.Fatal(err)
	}

	approve := relay.nextFrame(t, "pairing_approve")
	var approval struct {
		CT string `json:"ct"`
	}
	if err := json.Unmarshal(approve.raw, &approval); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(approval.CT)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := openMobilePairingMessage(kPair, mobilePairingApprovalNonceByte, mobileApprovalAAD(bridge.identity.HubID, invitation.Pairing), ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var sealed mobileApprovalPlaintext
	if err := mobileDecodeStrict(plaintext, &sealed); err != nil {
		t.Fatal(err)
	}
	if sealed.BriefStatus == nil || sealed.BriefStatus.Current == nil || sealed.BriefStatus.Current.ID != brief.BriefID ||
		sealed.BriefStatus.Current.MadeAt != brief.AsOf || sealed.BriefStatus.Next == nil {
		t.Fatalf("the approval does not name the brief that follows it: %+v", sealed.BriefStatus)
	}

	sent := relay.nextFrame(t, "brief")
	var briefFrame mobileBriefFrame
	if err := json.Unmarshal(sent.raw, &briefFrame); err != nil {
		t.Fatal(err)
	}
	if briefFrame.Notify || briefFrame.Env.Dev != phone.deviceID || briefFrame.AsOf != brief.AsOf {
		t.Fatalf("the brief after pairing is wrong: notify=%v dev=%s asOf=%s", briefFrame.Notify, briefFrame.Env.Dev, briefFrame.AsOf)
	}
	var received MobileBriefV1
	if err := json.Unmarshal(phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, briefFrame.Env), &received); err != nil || received.BriefID != brief.BriefID {
		t.Fatalf("the new phone cannot open the latest brief: %v", err)
	}
	if entry, ok := bridge.briefOutbox.entry(phone.deviceID); !ok || entry.BriefID != brief.BriefID || entry.Notify {
		t.Fatalf("the brief sent after pairing is not kept until received: %+v %v", entry, ok)
	}
	relay.expectNoFrame(t, "brief", 200*time.Millisecond)
}

// With no brief made yet the approval still names the first one's time, and
// nothing is sent.
func TestMobilePairingBeforeTheFirstBriefNamesItsTime(t *testing.T) {
	bridge, relay, conn, invitation := mobileTestOpenInvitation(t)
	relay.nextFrame(t, "pairing_open")
	bridge.app.mobileBriefSvc = &mobileTestBriefSource{}
	phone := newMobileTestPhone(t)
	frame, kPair := phone.claimFrame(t, invitation, nil)
	conn.send(t, frame)
	mobileTestWaitFor(t, "a pending claim", func() bool { return bridge.connectionStatus().PendingPairing != nil })
	if err := bridge.confirmPairing(invitation.Pairing, true); err != nil {
		t.Fatal(err)
	}
	approve := relay.nextFrame(t, "pairing_approve")
	var approval struct {
		CT string `json:"ct"`
	}
	if err := json.Unmarshal(approve.raw, &approval); err != nil {
		t.Fatal(err)
	}
	ciphertext, _ := base64.RawURLEncoding.DecodeString(approval.CT)
	plaintext, err := openMobilePairingMessage(kPair, mobilePairingApprovalNonceByte, mobileApprovalAAD(bridge.identity.HubID, invitation.Pairing), ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var sealed mobileApprovalPlaintext
	if err := mobileDecodeStrict(plaintext, &sealed); err != nil {
		t.Fatal(err)
	}
	if sealed.BriefStatus == nil || sealed.BriefStatus.Current != nil || sealed.BriefStatus.Next == nil || sealed.BriefStatus.Next.DueAt == "" {
		t.Fatalf("the approval does not say when the first brief comes: %+v", sealed.BriefStatus)
	}
	relay.expectNoFrame(t, "brief", 200*time.Millisecond)
}
