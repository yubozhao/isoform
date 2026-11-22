package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/sms"
)

const smsNumber = "+14155550123"

// smsRelay is a relay with the fake text provider wired as main.go wires the
// real one.
type smsRelay struct {
	*relay
	fake  *sms.Fake
	texts *sms.Service
	seq   int
}

func newSMSRelay(t *testing.T) *smsRelay {
	t.Helper()
	r := newRelay(t, "")
	vault, err := sms.NewVault(sms.VaultKeys{Seal: bytes.Repeat([]byte{9}, sms.KeySize)})
	if err != nil {
		t.Fatal(err)
	}
	fake := sms.NewFake([]byte("webhook-secret"))
	cfg := sms.Config{Provider: "fake", DailyCap: sms.DefaultDailyCap, MonthlyCap: sms.DefaultMonthlyCap, HourlyCap: sms.DefaultHourlyCap, CodesPerHour: sms.DefaultCodesPerHour, KeywordAnswersPerHour: sms.DefaultKeywordAnswersPerHour, Countries: []string{"+1"}, HelpContact: "help@example.com"}
	texts := sms.NewService(r.store, fake, vault, cfg)
	texts.Now = r.now
	texts.OnReply = r.hubs.Kick
	texts.OnState = r.hubs.SMSChanged
	r.hubs.SMS = texts
	r.h.SMS = texts
	return &smsRelay{relay: r, fake: fake, texts: texts}
}

func hubHeader(hubID, credential string) http.Header {
	header := bearer(credential)
	header.Set("X-Yansu-Hub", hubID)
	return header
}

func (r *smsRelay) hubCall(method, path, hubID, credential string, value any) response {
	r.t.Helper()
	var body []byte
	if value != nil {
		var err error
		if body, err = json.Marshal(value); err != nil {
			r.t.Fatal(err)
		}
	}
	return r.call(method, path, hubHeader(hubID, credential), body)
}

type smsStatusData struct {
	State         string  `json:"state"`
	CodeExpiresAt *string `json:"codeExpiresAt"`
	SentToday     int     `json:"sentToday"`
	SentThisMonth int     `json:"sentThisMonth"`
	DailyCap      int     `json:"dailyCap"`
	Code          string  `json:"code"`
}

func smsData(t *testing.T, resp response) smsStatusData {
	t.Helper()
	var data smsStatusData
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

var smsCode = regexp.MustCompile(`[0-9]{6}`)

// enabledHub connects a hub and turns text messages on for it, as the Mac
// does before it asks for a code.
func (r *smsRelay) enabledHub(hubID, credential string) *hubClient {
	r.t.Helper()
	hc := r.connectHub(hubID, credential)
	resp := r.hubCall(http.MethodPost, "/v1/hub/sms/enable", hubID, credential, nil)
	r.expectStatus(resp, http.StatusOK, "enable")
	return hc
}

// subscribe connects nothing; the hub must exist. It verifies smsNumber.
func (r *smsRelay) subscribe(hubID, credential string) {
	r.t.Helper()
	resp := r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": smsNumber, "lang": "en"})
	r.expectStatus(resp, http.StatusOK, "start verification")
	texts := r.fake.SentTo(smsNumber)
	code := smsCode.FindString(texts[len(texts)-1].Body)
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription/confirm", hubID, credential, map[string]string{"code": code})
	r.expectStatus(resp, http.StatusOK, "confirm")
	if data := smsData(r.t, resp); data.State != "active" {
		r.t.Fatalf("state after confirm %q", data.State)
	}
}

// webhook posts a signed inbound text. A blank signature sends none.
func (r *smsRelay) webhook(from, body, messageID, signature string) (int, []byte) {
	r.t.Helper()
	form := url.Values{"From": {from}, "To": {"+18005550100"}, "Body": {body}, "MessageId": {messageID}}
	req, err := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/sms/inbound", strings.NewReader(form.Encode()))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	switch signature {
	case "":
	case "valid":
		req.Header.Set("X-Fake-Signature", r.fake.Sign(form))
	default:
		req.Header.Set("X-Fake-Signature", signature)
	}
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Cache-Control") != "no-store" {
		r.t.Fatal("webhook answer is cacheable")
	}
	return resp.StatusCode, raw
}

func (r *smsRelay) text(body string) int {
	r.t.Helper()
	r.seq++
	status, _ := r.webhook(smsNumber, body, fmt.Sprintf("msg-%d", r.seq), "valid")
	return status
}

func TestSMSRoutesNeedAProvider(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	r.connectHub(hubID, credential)
	resp := r.call(http.MethodGet, "/v1/hub/sms/subscription", hubHeader(hubID, credential), nil)
	r.expectStatus(resp, http.StatusServiceUnavailable, "status without a provider")
	resp = r.call(http.MethodPost, "/v1/sms/inbound", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, []byte("Body=STOP"))
	r.expectStatus(resp, http.StatusNotFound, "webhook without a provider")
}

func TestSMSHubRoutesAreAuthenticated(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	r.enabledHub(hubID, credential)
	cases := []struct {
		name   string
		header http.Header
	}{
		{"no credential", http.Header{"X-Yansu-Hub": {hubID}}},
		{"wrong credential", hubHeader(hubID, randomToken(t))},
		{"unknown hub", hubHeader(uuid.NewString(), randomToken(t))},
		{"malformed hub id", hubHeader("not-a-uuid", credential)},
	}
	paths := []struct{ method, path string }{
		{http.MethodGet, "/v1/hub/sms/subscription"},
		{http.MethodPost, "/v1/hub/sms/subscription"},
		{http.MethodDelete, "/v1/hub/sms/subscription"},
		{http.MethodPost, "/v1/hub/sms/subscription/confirm"},
		{http.MethodPost, "/v1/hub/sms/send"},
		{http.MethodPost, "/v1/hub/sms/enable"},
	}
	for _, c := range cases {
		for _, p := range paths {
			resp := r.call(p.method, p.path, c.header, []byte(`{"number":"+14155550123","code":"123456","body":"hi"}`))
			r.expectStatus(resp, http.StatusUnauthorized, c.name+" "+p.method+" "+p.path)
		}
	}
	if len(r.fake.Sent()) != 0 {
		t.Fatal("an unauthenticated request sent a text")
	}
	// A device bearer is not a hub credential.
	hc := r.enabledHub(uuid.NewString(), randomToken(t))
	dev := r.pairDevice(hc, []string{"brief:read", "push"})
	resp := r.call(http.MethodPost, "/v1/hub/sms/send", hubHeader(dev.hubID, dev.bearer), []byte(`{"body":"hi"}`))
	r.expectStatus(resp, http.StatusUnauthorized, "device bearer on a hub route")
}

func TestSMSSubscriptionSendAndReplyFlow(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.enabledHub(hubID, credential)

	resp := r.hubCall(http.MethodGet, "/v1/hub/sms/subscription", hubID, credential, nil)
	r.expectStatus(resp, http.StatusOK, "status")
	if data := smsData(t, resp); data.State != "none" || data.DailyCap != 10 {
		t.Fatalf("initial status %+v", data)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": "4155550123"})
	r.expectStatus(resp, http.StatusBadRequest, "number without country code")
	if smsData(t, resp).Code != "invalid_number" {
		t.Fatalf("refusal code %s", resp.Raw)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": "+442079460958"})
	r.expectStatus(resp, http.StatusBadRequest, "number outside the allowed countries")
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": smsNumber, "lang": "fr"})
	r.expectStatus(resp, http.StatusBadRequest, "unsupported language")
	if smsData(t, resp).Code != "invalid_request" {
		t.Fatalf("refusal code %s", resp.Raw)
	}
	resp = r.call(http.MethodPut, "/v1/hub/sms/subscription", hubHeader(hubID, credential), []byte(`{}`))
	r.expectStatus(resp, http.StatusMethodNotAllowed, "PUT on the subscription")

	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": "+1 415 555 0123", "lang": "en"})
	r.expectStatus(resp, http.StatusOK, "start")
	if data := smsData(t, resp); data.State != "pending_verification" || data.CodeExpiresAt == nil {
		t.Fatalf("pending status %+v", data)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": "Isoform: too soon"})
	r.expectStatus(resp, http.StatusConflict, "send while pending")
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription/confirm", hubID, credential, map[string]string{"code": "12345"})
	r.expectStatus(resp, http.StatusBadRequest, "malformed code")
	code := smsCode.FindString(r.fake.SentTo(smsNumber)[0].Body)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription/confirm", hubID, credential, map[string]string{"code": wrong})
	r.expectStatus(resp, http.StatusForbidden, "wrong code")
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription/confirm", hubID, credential, map[string]string{"code": code})
	r.expectStatus(resp, http.StatusOK, "confirm")

	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": "Isoform: \"Deck\" is ready to review. Reply R for the summary."})
	r.expectStatus(resp, http.StatusOK, "send")
	if data := smsData(t, resp); data.SentToday != 1 {
		t.Fatalf("after send %+v", data)
	}
	sent := r.fake.SentTo(smsNumber)
	if sent[len(sent)-1].Body != "Isoform: \"Deck\" is ready to review. Reply R for the summary." {
		t.Fatalf("sent %q", sent[len(sent)-1].Body)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": strings.Repeat("x", sms.MaxBodyRunes+1)})
	r.expectStatus(resp, http.StatusBadRequest, "long body")
	resp = r.call(http.MethodPost, "/v1/hub/sms/send", hubHeader(hubID, credential), []byte(`{"body":"hi","to":"+15555550100"}`))
	r.expectStatus(resp, http.StatusBadRequest, "send naming another number")

	// A reply reaches the connected hub, once, and stays until acked.
	if status := r.text("Y"); status != http.StatusOK {
		t.Fatalf("webhook status %d", status)
	}
	frame := hc.recvType("sms_reply")
	if frame["body"] != "Y" || !wireUUID(frame["id"]) {
		t.Fatalf("reply frame %v", frame)
	}
	hc.send(map[string]any{"t": "sms_ack", "id": frame["id"]})
	hc.barrier()
	if replies, _ := r.texts.Replies(t.Context(), hubID, 10); len(replies) != 0 {
		t.Fatal("an acked reply is still queued")
	}

	// STOP turns the subscription off and tells the hub.
	if status := r.text("stop"); status != http.StatusOK {
		t.Fatalf("STOP status %d", status)
	}
	if frame = hc.recvType("sms_state"); frame["state"] != "stopped" {
		t.Fatalf("state frame %v", frame)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": "Isoform: hi"})
	r.expectStatus(resp, http.StatusConflict, "send after STOP")
	if smsData(t, resp).Code != "opted_out" {
		t.Fatalf("refusal %s", resp.Raw)
	}
	if status := r.text("START"); status != http.StatusOK {
		t.Fatalf("START status %d", status)
	}
	if frame = hc.recvType("sms_state"); frame["state"] != "active" {
		t.Fatalf("state frame %v", frame)
	}

	resp = r.hubCall(http.MethodDelete, "/v1/hub/sms/subscription", hubID, credential, nil)
	r.expectStatus(resp, http.StatusOK, "unsubscribe")
	if data := smsData(t, resp); data.State != "none" {
		t.Fatalf("after delete %+v", data)
	}
}

func wireUUID(v any) bool {
	s, ok := v.(string)
	return ok && len(s) == 36
}

func TestSMSWebhookSignatureAndReplay(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.enabledHub(hubID, credential)
	r.subscribe(hubID, credential)
	for _, signature := range []string{"", "deadbeef"} {
		status, _ := r.webhook(smsNumber, "STOP", "forged", signature)
		if status != http.StatusForbidden {
			t.Fatalf("signature %q: status %d", signature, status)
		}
	}
	resp := r.hubCall(http.MethodGet, "/v1/hub/sms/subscription", hubID, credential, nil)
	if data := smsData(t, resp); data.State != "active" {
		t.Fatalf("a forged STOP changed the state to %q", data.State)
	}
	for i := 0; i < 3; i++ {
		status, body := r.webhook(smsNumber, "N", "same-message", "valid")
		if status != http.StatusOK || string(body) != "ok" {
			t.Fatalf("webhook %d: %d %s", i, status, body)
		}
	}
	first := hc.recvType("sms_reply")
	hc.barrier()
	for _, frame := range hc.pending {
		if frame["t"] == "sms_reply" {
			t.Fatalf("a replayed webhook was forwarded twice: %v and %v", first, frame)
		}
	}
}

func TestSMSRepliesWaitForTheHubAndReturnUntilAcked(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.enabledHub(hubID, credential)
	r.subscribe(hubID, credential)
	_ = hc.conn.Close()
	r.waitOffline(hubID)

	if status := r.text("1"); status != http.StatusOK {
		t.Fatalf("webhook status %d", status)
	}
	hc = r.enabledHub(hubID, credential)
	frame := hc.recvType("sms_reply")
	if frame["body"] != "1" {
		t.Fatalf("queued reply %v", frame)
	}
	// Not acked: the next connection gets it again.
	_ = hc.conn.Close()
	r.waitOffline(hubID)
	hc = r.enabledHub(hubID, credential)
	again := hc.recvType("sms_reply")
	if again["id"] != frame["id"] {
		t.Fatalf("redelivered %v, want %v", again, frame)
	}
	hc.send(map[string]any{"t": "sms_ack", "id": "not-a-uuid"})
	if refused := hc.recvType("error"); refused["ref"] != "sms_ack" {
		t.Fatalf("bad ack %v", refused)
	}
	// Acked: the next connection does not get it again.
	hc.send(map[string]any{"t": "sms_ack", "id": again["id"]})
	hc.barrier()
	_ = hc.conn.Close()
	r.waitOffline(hubID)
	hc = r.enabledHub(hubID, credential)
	if status := r.text("2"); status != http.StatusOK {
		t.Fatalf("webhook status %d", status)
	}
	if next := hc.recvType("sms_reply"); next["body"] != "2" {
		t.Fatalf("an acked reply came back: %v", next)
	}
}

func TestSMSStateFollowsTheNumberToAnotherHub(t *testing.T) {
	r := newSMSRelay(t)
	first, firstCredential := uuid.NewString(), randomToken(t)
	hc := r.enabledHub(first, firstCredential)
	r.subscribe(first, firstCredential)
	second, secondCredential := uuid.NewString(), randomToken(t)
	r.enabledHub(second, secondCredential)
	r.subscribe(second, secondCredential)
	if frame := hc.recvType("sms_state"); frame["state"] != "none" {
		t.Fatalf("displaced hub told %v", frame)
	}
	resp := r.hubCall(http.MethodGet, "/v1/hub/sms/subscription", first, firstCredential, nil)
	if data := smsData(t, resp); data.State != "none" {
		t.Fatalf("displaced hub reads %q", data.State)
	}
}

func TestSMSExpiredCodeIsNotPending(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	r.enabledHub(hubID, credential)
	resp := r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": smsNumber})
	r.expectStatus(resp, http.StatusOK, "start")
	r.advance(sms.CodeTTL + time.Second)
	resp = r.hubCall(http.MethodGet, "/v1/hub/sms/subscription", hubID, credential, nil)
	if data := smsData(t, resp); data.State != "none" || data.CodeExpiresAt != nil {
		t.Fatalf("expired code status %+v", data)
	}
	code := smsCode.FindString(r.fake.SentTo(smsNumber)[0].Body)
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription/confirm", hubID, credential, map[string]string{"code": code})
	r.expectStatus(resp, http.StatusGone, "expired code")
}

func (r *smsRelay) waitOffline(hubID string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.hubs.Online(hubID) {
		if time.Now().After(deadline) {
			r.t.Fatal("hub never went offline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSMSQuotaAndIsolationBetweenHubs(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	r.enabledHub(hubID, credential)
	r.subscribe(hubID, credential)
	for i := 0; i < sms.DefaultDailyCap; i++ {
		resp := r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": fmt.Sprintf("Isoform: %d", i)})
		r.expectStatus(resp, http.StatusOK, "send under the cap")
	}
	var resp response
	send := func() int {
		resp = r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": "Isoform: over"})
		return resp.Status
	}
	expectLimitLog(t, "sms_daily", "/v1/hub/sms/send", sms.DefaultDailyCap, sms.DefaultDailyCap, send)
	r.texts.Config.MonthlyCap = 1
	expectLimitLog(t, "sms_monthly", "/v1/hub/sms/send", sms.DefaultDailyCap, 1, send)
	r.expectStatus(resp, http.StatusTooManyRequests, "send over the cap")
	if smsData(t, resp).Code != "quota" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("quota refusal %s %v", resp.Raw, resp.Header)
	}

	other, otherCredential := uuid.NewString(), randomToken(t)
	r.enabledHub(other, otherCredential)
	resp = r.hubCall(http.MethodGet, "/v1/hub/sms/subscription", other, otherCredential, nil)
	if data := smsData(t, resp); data.State != "none" {
		t.Fatalf("another hub sees state %q", data.State)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/send", other, otherCredential, map[string]string{"body": "Isoform: not mine"})
	r.expectStatus(resp, http.StatusConflict, "another hub sending")
	resp = r.hubCall(http.MethodDelete, "/v1/hub/sms/subscription", other, otherCredential, nil)
	r.expectStatus(resp, http.StatusOK, "another hub deleting its own nothing")
	resp = r.hubCall(http.MethodGet, "/v1/hub/sms/subscription", hubID, credential, nil)
	if data := smsData(t, resp); data.State != "active" {
		t.Fatalf("another hub's delete reached this hub: %q", data.State)
	}
}

func TestSMSVerificationLimitedPerAddress(t *testing.T) {
	r := newSMSRelay(t)
	sent := 0
	for i := 0; i < smsVerificationsPerAddress+2; i++ {
		hubID, credential := uuid.NewString(), randomToken(t)
		r.enabledHub(hubID, credential)
		resp := r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": fmt.Sprintf("+1415555%04d", i)})
		if resp.Status == http.StatusOK {
			sent++
		} else {
			r.expectStatus(resp, http.StatusTooManyRequests, "verification over the address limit")
		}
	}
	if sent != smsVerificationsPerAddress {
		t.Fatalf("%d verification texts from one address, limit %d", sent, smsVerificationsPerAddress)
	}
}

func TestSMSNeedsAnEnabledHubOrAPairedPhone(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	r.connectHub(hubID, credential)
	resp := r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": smsNumber})
	r.expectStatus(resp, http.StatusForbidden, "code for a hub that never enabled texts")
	if smsData(t, resp).Code != "not_entitled" {
		t.Fatalf("refusal %s", resp.Raw)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": "Isoform: hi"})
	r.expectStatus(resp, http.StatusForbidden, "send for a hub that never enabled texts")
	if len(r.fake.Sent()) != 0 {
		t.Fatal("a hub without the entitlement got a text out")
	}
	for i := 0; i < 2; i++ {
		resp = r.hubCall(http.MethodPost, "/v1/hub/sms/enable", hubID, credential, nil)
		r.expectStatus(resp, http.StatusOK, "enable")
	}
	if data := smsData(t, resp); data.State != "none" || data.DailyCap != sms.DefaultDailyCap {
		t.Fatalf("status after enable %+v", data)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/sms/subscription", hubID, credential, map[string]string{"number": smsNumber})
	r.expectStatus(resp, http.StatusOK, "code after enable")

	// A paired phone stands in for the flag.
	paired := r.connectHub(uuid.NewString(), randomToken(t))
	r.pairDevice(paired, []string{"brief:read", "push"})
	if ok, err := r.store.SMSEntitled(t.Context(), paired.hubID, r.now()); err != nil || !ok {
		t.Fatalf("a hub with a paired phone is not entitled: %v %v", ok, err)
	}
}

func TestSMSSendRefusesLinksAndPhoneNumbers(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	r.enabledHub(hubID, credential)
	r.subscribe(hubID, credential)
	before := len(r.fake.Sent())
	for _, body := range []string{
		"\"Deck\" is ready to review.",
		"Isoform: sign in at https://example.com/login",
		"Isoform: see bit.ly/abc",
		"Isoform: call 415-555-0199 now",
	} {
		resp := r.hubCall(http.MethodPost, "/v1/hub/sms/send", hubID, credential, map[string]string{"body": body})
		r.expectStatus(resp, http.StatusBadRequest, body)
		if smsData(t, resp).Code != "invalid_body" {
			t.Fatalf("refusal for %q: %s", body, resp.Raw)
		}
	}
	if len(r.fake.Sent()) != before {
		t.Fatal("a refused body was texted")
	}
}

func TestSMSWebhookCountsOnlyBadSignatures(t *testing.T) {
	r := newSMSRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.enabledHub(hubID, credential)
	r.subscribe(hubID, credential)
	limited := false
	for i := 0; i < smsBadWebhooksPerAddress+5; i++ {
		status, _ := r.webhook(smsNumber, "STOP", fmt.Sprintf("forged-%d", i), "deadbeef")
		switch status {
		case http.StatusForbidden:
		case http.StatusTooManyRequests:
			limited = true
		default:
			t.Fatalf("forged webhook %d: status %d", i, status)
		}
	}
	if !limited {
		t.Fatal("bad signatures from one address were never limited")
	}
	// The same address still gets signed texts through.
	for i := 0; i < smsBadWebhooksPerAddress+5; i++ {
		if status := r.text(fmt.Sprintf("reply %d", i)); status != http.StatusOK {
			t.Fatalf("signed webhook %d after forged ones: status %d", i, status)
		}
	}
	if frame := hc.recvType("sms_reply"); frame["body"] != "reply 0" {
		t.Fatalf("first reply %v", frame)
	}
}
