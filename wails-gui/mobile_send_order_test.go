package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// The relay hands a phone only messages numbered above the one it asked after,
// and the phone keeps a highest number too. A message that leaves this computer
// after a later-numbered one is therefore lost to the phone: it waits for an
// answer that never comes. Senders numbered a message under the bridge's lock
// and sent it after releasing it, so an answer and an event could leave in the
// other order. Numbering and sending are one step now.
func TestMobileMessagesLeaveInTheOrderOfTheirNumbers(t *testing.T) {
	bridge, relay := mobileTestHub(t)
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	relay.acceptRegistration(t, mobileTestListed(phone))
	seqOf := func() uint64 {
		out := relay.nextFrame(t, "out")
		var frame struct {
			Env mobileEnvelope `json:"env"`
		}
		if err := json.Unmarshal(out.raw, &frame); err != nil {
			t.Fatal(err)
		}
		return frame.Env.Seq
	}
	// The connection is ready a moment after it registers: wait for the first answer to go.
	deadline := time.Now().Add(5 * time.Second)
	for bridge.sendMobileResponse(phone.deviceID, mobileTestOtherID, http.StatusOK, `{"ok":true}`) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the connection to the relay did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	last := seqOf()

	const senders = 40
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := bridge.sendMobileResponse(phone.deviceID, mobileTestOtherID, http.StatusOK, `{"ok":true}`); err != nil {
				t.Errorf("an answer was not sent: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			bridge.publishEvent(mobileScopeBriefRead, []byte(`{"event":"today_changed"}`))
		}()
	}
	wg.Wait()

	for i := 0; i < 2*senders; i++ {
		seq := seqOf()
		if seq <= last {
			t.Fatalf("message %d left after message %d", seq, last)
		}
		last = seq
	}
}
