package main

// End-to-end check through the real relay binary (relay/ module): the real hub
// code pairs with a Go device that runs the production crypto, answers an
// encrypted request, and delivers a brief the device asked for. The test
// builds and spawns the relay, so it only runs with YANSU_MOBILE_E2E=1.
//
// The automated device connects directly to the local relay. Its invitation
// uses a public test origin because loopback invitations are never offered.
// Manual simulator pairing needs a reachable relay or a real remote ticket.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

func TestMobileEndToEndThroughRelayBinary(t *testing.T) {
	if os.Getenv("YANSU_MOBILE_E2E") != "1" {
		t.Skip("set YANSU_MOBILE_E2E=1 to build and run the relay end-to-end check")
	}
	relayURL := startMobileE2ERelay(t)
	dir := t.TempDir()
	t.Setenv("YANSU_MOBILE_STATE_DIR", dir)
	t.Setenv("ISOFORM_RELAY_URL", "")
	app := &App{}
	store, err := storage.New(filepath.Join(dir, "yansu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app.store = store
	app.activeTags = map[string]bool{}
	mobileE2EInstallChatSeams(t)
	if mobileBriefServiceFactory == nil {
		t.Fatal("the brief service factory is not registered")
	}
	svc := mobileBriefServiceFactory(app)
	app.mobileBriefSvc = svc
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	t.Cleanup(func() {
		cancel()
		svc.Stop()
		if bridge := app.mobileBridgeForUse(); bridge != nil {
			bridge.Stop()
		}
	})

	phone := pairMobileE2EPhone(t, app, relayURL)

	code, body := phone.request(t, "GET", "/status", "")
	if code != http.StatusOK || !strings.Contains(body, `"hubName"`) {
		t.Fatalf("status route: %d %s", code, body)
	}
	code, body = phone.request(t, "POST", "/brief/refresh", "{}")
	// A catch-up generation may already be running when the phone asks, so both
	// answers mean a brief is on its way.
	if code != http.StatusOK || !(strings.Contains(body, `"making"`) || strings.Contains(body, `"queued"`)) {
		t.Fatalf("brief refresh: %d %s", code, body)
	}
	brief := phone.awaitBrief(t, 90*time.Second)
	if brief.Version != 1 || brief.Slot == "" || brief.BriefID == "" || brief.AsOf == "" {
		t.Fatalf("delivered brief: %+v", brief)
	}
	t.Logf("delivered brief %s (%s, narrative %s)", brief.BriefID, brief.Slot, brief.NarrativeStatus)
	mobileE2ENote("", "AUTOMATED_OK "+brief.BriefID)

	// The Desk routes of release 2 were removed with the Desk.
	code, body = phone.request(t, "GET", "/desk", "")
	if code != http.StatusNotFound {
		t.Fatalf("removed desk route: %d %s", code, body)
	}

	// Release 3: a chat turn, its events, a cancel and a memory correction.
	mobileE2EChatFlow(t, phone)
	mobileE2ENote("", "AUTOMATED_CHAT_OK")

	// Brief on the phone, one package (PROTOCOL.md section 8.1). The chat
	// flow spent this minute's device requests on the relay (60 a minute).
	time.Sleep(61 * time.Second)
	mobileE2EBriefPackageGuard(t, app, svc.(*mobileBriefService), phone)
	mobileE2ENote("", "AUTOMATED_PACKAGE_OK")

	hold, _ := time.ParseDuration(os.Getenv("YANSU_MOBILE_E2E_HOLD"))
	if hold <= 0 {
		return
	}
	mobileE2EHold(t, app, hold)
}

func pairMobileE2EPhone(t *testing.T, app *App, relayURL string) *mobileE2EPhone {
	t.Helper()
	status, err := app.EnableMobileConnection(relayURL)
	if err != nil || !status.Enabled {
		t.Fatalf("enable: %+v %v", status, err)
	}
	mobileTestWaitFor(t, "a connected relay", func() bool {
		current, _ := app.GetMobileConnectionStatus()
		return current.RelayState == mobileRelayConnected
	})

	status, err = mobileE2EOpenInvitation(app)
	if err != nil || status.ConnectionCode == "" {
		t.Fatalf("generate: %+v %v", status, err)
	}
	phone := newMobileE2EPhone(t, relayURL, mobileTestDecodeInvitation(t, status.ConnectionCode))
	phone.claim(t)
	mobileTestWaitFor(t, "a pending claim on the hub", func() bool {
		current, _ := app.GetMobileConnectionStatus()
		return current.PendingPairing != nil
	})
	pending, _ := app.GetMobileConnectionStatus()
	if pending.PendingPairing.Code != phone.code || pending.PendingPairing.DeviceName != "E2E iPhone" {
		t.Fatalf("pending pairing does not match the phone: %+v vs %s", pending.PendingPairing, phone.code)
	}
	if _, err := app.ConfirmMobilePairing(pending.PendingPairing.PairingID, true); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	approval := phone.awaitApproval(t)
	if approval.DeviceID != phone.deviceID || len(approval.Scopes) != len(mobileE2EPhoneScopes) || approval.HubName == "" {
		t.Fatalf("approval: %+v", approval)
	}

	return phone
}

// The test phone connects to relayURL directly; this origin is only used in
// the encoded fixture and is never dialed or offered to a physical phone.
func mobileE2EOpenInvitation(app *App) (MobileConnectionStatus, error) {
	if err := app.mobileBridgeForUse().openPairingWithRelay("https://relay.example.com"); err != nil {
		return MobileConnectionStatus{}, err
	}
	return app.GetMobileConnectionStatus()
}

// mobileE2EHold keeps the hub reachable for manual simulator pairing.
func mobileE2EHold(t *testing.T, app *App, hold time.Duration) {
	t.Helper()
	status, err := app.GenerateMobileConnectionCode()
	if err != nil {
		t.Fatalf("hold invitation: %v", err)
	}
	fmt.Printf("\nMOBILE_E2E_INVITATION %s\n\n", status.ConnectionCode)
	mobileE2ENote(status.ConnectionCode, "INVITATION "+status.ConnectionCode)
	known := map[string]bool{}
	for _, device := range status.Devices {
		known[device.DeviceID] = true
	}
	deadline := time.Now().Add(hold)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		current, err := app.GetMobileConnectionStatus()
		if err != nil {
			continue
		}
		if current.PendingPairing != nil {
			fmt.Printf("MOBILE_E2E_CLAIM device=%q code=%s (approving)\n", current.PendingPairing.DeviceName, current.PendingPairing.Code)
			mobileE2ENote("", "CLAIM "+current.PendingPairing.DeviceName+" code="+current.PendingPairing.Code)
			if _, err := app.ConfirmMobilePairing(current.PendingPairing.PairingID, true); err != nil {
				fmt.Printf("MOBILE_E2E_APPROVE_FAILED %v\n", err)
			}
			continue
		}
		for _, device := range current.Devices {
			if known[device.DeviceID] {
				continue
			}
			known[device.DeviceID] = true
			brief := mobileE2ESampleBrief(current.HubName)
			if err := app.publishMobileBrief(brief, false); err != nil {
				fmt.Printf("MOBILE_E2E_PUBLISH_FAILED %v\n", err)
			} else {
				fmt.Printf("MOBILE_E2E_PUBLISHED sample brief to %s\n", device.DeviceName)
				mobileE2ENote("", "PUBLISHED "+device.DeviceName)
			}
		}
		if current.ConnectionCode == "" && current.PendingPairing == nil {
			if fresh, err := app.GenerateMobileConnectionCode(); err == nil && fresh.ConnectionCode != "" {
				fmt.Printf("\nMOBILE_E2E_INVITATION %s\n\n", fresh.ConnectionCode)
				mobileE2ENote(fresh.ConnectionCode, "INVITATION "+fresh.ConnectionCode)
			}
		}
	}
}

// startMobileE2ERelay builds relay/ and runs it on a loopback port.
func startMobileE2ERelay(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "isoform-relay")
	build := exec.Command("go", "build", "-o", binary, "./cmd/isoform-relay")
	build.Dir = filepath.Join("..", "relay")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build relay: %v\n%s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	run := exec.Command(binary)
	run.Env = append(os.Environ(), "ISOFORM_RELAY_ADDR="+address, "ISOFORM_RELAY_STATE_DIR="+t.TempDir())
	run.Stdout, run.Stderr = os.Stderr, os.Stderr
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = run.Process.Kill()
		_ = run.Wait()
	})
	url := "http://" + address
	mobileTestWaitFor(t, "a healthy relay", func() bool {
		response, err := http.Get(url + "/v1/health")
		if err != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	return url
}

// mobileE2EPhone runs the device side of PROTOCOL.md over real HTTP.
type mobileE2EPhone struct {
	relay        string
	invitation   mobileInvitation
	hubPublic    *ecdh.PublicKey
	deviceID     string
	private      *ecdh.PrivateKey
	bearer       string
	bearerHash   string
	claimToken   string
	kPair        []byte
	code         string
	keys         mobileSessionKeys
	sendSeq      uint64
	lastMessage  uint64
	lastBrief    uint64
	client       *http.Client
	approvalDone bool
	events       []map[string]any
}

type mobileE2EReply struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

func newMobileE2EPhone(t *testing.T, relay string, invitation mobileInvitation) *mobileE2EPhone {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bearerBytes := make([]byte, 32)
	if _, err := rand.Read(bearerBytes); err != nil {
		t.Fatal(err)
	}
	bearer := base64.RawURLEncoding.EncodeToString(bearerBytes)
	digest := sha256.Sum256([]byte(bearer))
	hubPublic, err := mobileParsePublicKey(invitation.HubPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base64.RawURLEncoding.DecodeString(invitation.Secret)
	if err != nil {
		t.Fatal(err)
	}
	kPair, err := mobilePairKey(private, hubPublic, secret, invitation.Hub, invitation.Pairing)
	if err != nil {
		t.Fatal(err)
	}
	code, err := mobileConfirmationCode(kPair)
	if err != nil {
		t.Fatal(err)
	}
	phone := &mobileE2EPhone{
		relay: relay, invitation: invitation, hubPublic: hubPublic, deviceID: uuid.NewString(), private: private,
		bearer: bearer, bearerHash: hex.EncodeToString(digest[:]), claimToken: mobileClaimToken(secret, invitation.Pairing),
		kPair: kPair, code: code, client: &http.Client{Timeout: 40 * time.Second},
	}
	phone.keys, err = mobileDeriveSessionKeys(private, hubPublic, invitation.Hub, phone.deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return phone
}

func (p *mobileE2EPhone) call(t *testing.T, method, path string, body any, headers map[string]string) (int, mobileE2EReply) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, p.relay+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	var reply mobileE2EReply
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = json.Unmarshal(raw, &reply)
	return response.StatusCode, reply
}

func (p *mobileE2EPhone) authed() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.bearer}
}

// mobileE2EPhoneScopes are the scopes the current iPhone app asks for.
var mobileE2EPhoneScopes = []string{
	mobileScopeBriefRead, mobileScopeBriefRefresh, mobileScopePresenceRead, mobileScopePush,
	mobileScopeChatRead, mobileScopeChatWrite, mobileScopeMemoryFix, mobileScopeHandoff,
}

func (p *mobileE2EPhone) claim(t *testing.T) {
	t.Helper()
	plaintext, err := json.Marshal(mobileClaimPlaintext{
		DeviceID: p.deviceID, DeviceName: "E2E iPhone", DevicePub: base64.RawURLEncoding.EncodeToString(p.private.PublicKey().Bytes()),
		BearerHash: p.bearerHash, Platform: "ios", AppVersion: "e2e", PushEnvironment: "development",
		Scopes: mobileE2EPhoneScopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := sealMobilePairingMessage(p.kPair, mobilePairingClaimNonceByte, mobileClaimAAD(p.invitation.Hub, p.invitation.Pairing), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	status, reply := p.call(t, "POST", "/v1/pairings/"+p.invitation.Pairing+"/claim", map[string]string{
		"hub": p.invitation.Hub, "claimToken": p.claimToken, "deviceId": p.deviceID,
		"devicePub": base64.RawURLEncoding.EncodeToString(p.private.PublicKey().Bytes()),
		"ct":        base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil)
	if status != http.StatusAccepted {
		t.Fatalf("claim: %d %s", status, reply.Error)
	}
}

func (p *mobileE2EPhone) awaitApproval(t *testing.T) mobileApprovalPlaintext {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		status, reply := p.call(t, "GET", "/v1/pairings/"+p.invitation.Pairing+"?wait=5", nil, map[string]string{"X-Yansu-Claim": p.claimToken})
		if status == http.StatusConflict {
			time.Sleep(time.Second)
			continue
		}
		if status != http.StatusOK {
			t.Fatalf("pairing poll: %d %s", status, reply.Error)
		}
		var data struct {
			State    string `json:"state"`
			Approval *struct {
				CT string `json:"ct"`
			} `json:"approval"`
		}
		if err := json.Unmarshal(reply.Data, &data); err != nil {
			t.Fatal(err)
		}
		switch data.State {
		case "approved":
			if data.Approval == nil {
				t.Fatal("approved without an approval payload")
			}
			ciphertext, err := base64.RawURLEncoding.DecodeString(data.Approval.CT)
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err := openMobilePairingMessage(p.kPair, mobilePairingApprovalNonceByte, mobileApprovalAAD(p.invitation.Hub, p.invitation.Pairing), ciphertext)
			if err != nil {
				t.Fatalf("open approval: %v", err)
			}
			var approval mobileApprovalPlaintext
			if err := json.Unmarshal(plaintext, &approval); err != nil {
				t.Fatal(err)
			}
			p.approvalDone = true
			return approval
		case "declined", "expired":
			t.Fatalf("pairing ended as %s", data.State)
		}
	}
	t.Fatal("the approval never arrived")
	return mobileApprovalPlaintext{}
}

// request sends one hub request through the relay and waits for its response.
func (p *mobileE2EPhone) request(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	return p.requestWithQuery(t, method, path, "", body)
}

// requestWithQuery is request with a query string; the query travels inside the
// encrypted request, as PROTOCOL.md section 7 requires for every hub route.
func (p *mobileE2EPhone) requestWithQuery(t *testing.T, method, path, query, body string) (int, string) {
	t.Helper()
	id := p.postRequest(t, method, path, query, body)
	return p.awaitResponse(t, id)
}

// postRequest seals and queues one request on the relay and returns its id. The
// relay answers 202 whether or not the hub is connected (PROTOCOL.md section 23).
func (p *mobileE2EPhone) postRequest(t *testing.T, method, path, query, body string) string {
	t.Helper()
	id := uuid.NewString()
	plaintext, err := json.Marshal(map[string]string{"id": id, "method": method, "path": path, "query": query, "body": body})
	if err != nil {
		t.Fatal(err)
	}
	p.sendSeq++
	env, err := sealMobileEnvelope(p.keys.D2H, p.invitation.Hub, p.deviceID, mobileDirD2H, p.sendSeq, mobileKindRequest, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if status, reply := p.call(t, "POST", "/v1/requests", env, p.authed()); status != http.StatusAccepted {
		t.Fatalf("send request: %d %s", status, reply.Error)
	}
	return id
}

// awaitResponse long-polls until the hub answers the request id.
func (p *mobileE2EPhone) awaitResponse(t *testing.T, id string) (int, string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, item := range p.poll(t, 10) {
			if item.kind == mobileKindEvent {
				p.noteEvent(t, item.plaintext)
				continue
			}
			var response struct {
				ID     string `json:"id"`
				Status int    `json:"status"`
				Body   string `json:"body"`
			}
			if err := json.Unmarshal(item.plaintext, &response); err != nil {
				t.Fatal(err)
			}
			if response.ID == id {
				return response.Status, response.Body
			}
		}
	}
	t.Fatalf("no response for request %s", id)
	return 0, ""
}

// mobileE2EOpened is one h2d envelope the phone authenticated and decrypted.
type mobileE2EOpened struct {
	kind      string
	plaintext []byte
}

// poll long-polls the responses route once, opens every envelope and acks it.
func (p *mobileE2EPhone) poll(t *testing.T, wait int) []mobileE2EOpened {
	t.Helper()
	status, reply := p.call(t, "GET", "/v1/responses?after="+strconv.FormatUint(p.lastMessage, 10)+"&wait="+strconv.Itoa(wait), nil, p.authed())
	if status != http.StatusOK {
		t.Fatalf("poll responses: %d %s", status, reply.Error)
	}
	var data struct {
		Envelopes []mobileEnvelope `json:"envelopes"`
	}
	if err := json.Unmarshal(reply.Data, &data); err != nil {
		t.Fatal(err)
	}
	opened := make([]mobileE2EOpened, 0, len(data.Envelopes))
	for _, env := range data.Envelopes {
		plaintext, err := openMobileEnvelope(p.keys.H2D, env, p.invitation.Hub, p.deviceID, mobileDirH2D)
		if err != nil {
			t.Fatalf("open response: %v", err)
		}
		if env.Seq > p.lastMessage {
			p.lastMessage = env.Seq
		}
		p.call(t, "POST", "/v1/responses/ack", map[string]uint64{"upTo": env.Seq}, p.authed())
		opened = append(opened, mobileE2EOpened{kind: env.Kind, plaintext: plaintext})
	}
	return opened
}

// noteEvent keeps an event envelope the phone received, for awaitEvent.
func (p *mobileE2EPhone) noteEvent(t *testing.T, plaintext []byte) {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal(plaintext, &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	p.events = append(p.events, event)
}

// awaitEvent waits for an h2d event that satisfies match. Events are content-free
// (PROTOCOL.md section 15.5), so the phone matches on identifiers and state.
func (p *mobileE2EPhone) awaitEvent(t *testing.T, match func(map[string]any) bool, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, event := range p.events {
			if match(event) {
				return event
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the expected event never arrived")
		}
		for _, item := range p.poll(t, 5) {
			if item.kind == mobileKindEvent {
				p.noteEvent(t, item.plaintext)
			}
		}
	}
}

func (p *mobileE2EPhone) awaitBrief(t *testing.T, timeout time.Duration) MobileBriefV1 {
	t.Helper()
	brief, _ := p.awaitBriefDocument(t, timeout)
	return brief
}

// awaitBriefDocument returns the next pushed brief with its exact bytes.
func (p *mobileE2EPhone) awaitBriefDocument(t *testing.T, timeout time.Duration) (MobileBriefV1, []byte) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, reply := p.call(t, "GET", "/v1/brief/latest", nil, p.authed())
		if status == http.StatusNotFound {
			time.Sleep(time.Second)
			continue
		}
		if status != http.StatusOK {
			t.Fatalf("latest brief: %d %s", status, reply.Error)
		}
		var data struct {
			Version uint64         `json:"version"`
			AsOf    string         `json:"asOf"`
			Env     mobileEnvelope `json:"env"`
		}
		if err := json.Unmarshal(reply.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Env.Seq <= p.lastBrief {
			time.Sleep(time.Second)
			continue
		}
		// openMobileEnvelope already strips the brief padding.
		document, err := openMobileEnvelope(p.keys.H2D, data.Env, p.invitation.Hub, p.deviceID, mobileDirH2D)
		if err != nil {
			t.Fatalf("open brief: %v", err)
		}
		var brief MobileBriefV1
		if err := json.Unmarshal(document, &brief); err != nil {
			t.Fatalf("decode brief: %v\n%s", err, document)
		}
		p.lastBrief = data.Env.Seq
		return brief, document
	}
	t.Fatal("no brief arrived at the relay")
	return MobileBriefV1{}, nil
}

// mobileE2EBriefPackageGuard is the brief-on-the-phone guard: the Mac makes a
// written brief and reads it aloud, and the phone's copy holds its full text
// and the matching recording, under the id and version Today names.
func mobileE2EBriefPackageGuard(t *testing.T, app *App, svc *mobileBriefService, phone *mobileE2EPhone) {
	t.Helper()
	// The read gates need open stores: an activity store and a memory folder
	// of the test's own.
	t.Setenv("HOME", t.TempDir())
	app.activity = &activityMonitorState{store: newTestStore(t)}
	writePrivacyMemory(t, "onboarding-notes", "# Onboarding flow notes\n\nA manually written note.\n")
	now := time.Now().Truncate(time.Second)
	brief := buildMobileBriefV1(mobileBriefInput{
		AsOf: now, WindowStart: now.Add(-time.Hour), Slot: mobileBriefSlotOnRequest, Location: time.Local,
		Learned: []mobileBriefCandidate{{SourceKind: "memory", SourceID: "onboarding-notes", Title: "Onboarding flow notes", At: now.Add(-time.Minute)}},
	})
	refs := []string{brief.Evidence[0].Ref}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{
		{Text: "Good evening.", Refs: refs},
		{Text: "Most of the afternoon went to the onboarding flow, with two threads about the launch date in between.", Refs: refs},
		{Text: "The handoff notes for Thursday's review are drafted from the same files.", Refs: refs},
	}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	var spoken []string
	speaker := fakeSpeaker("en", nil, nil)
	speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
		spoken = append(spoken, text)
		return testWAV(1), nil
	}
	svc.speaker = speaker
	if delivered, withheld := app.mobileBriefDelivery(brief); len(delivered.Narrative) != len(brief.Narrative) {
		t.Fatalf("the read gates withheld the fixture: %+v", withheld)
	}
	svc.addAudio(context.Background(), brief, now)
	stored, ok := svc.store.load(brief.BriefID)
	if !ok || stored.Audio == nil || stored.Audio.Attachment == nil || len(stored.Audio.Paragraphs) != 3 {
		t.Fatalf("the Mac did not read the brief aloud for the phone: %+v (spoken %q)", stored.Audio, spoken)
	}

	// The push: the full text, the greeting joined to the first sentence.
	var pushed MobileBriefV1
	var document []byte
	for deadline := time.Now().Add(60 * time.Second); ; {
		pushed, document = phone.awaitBriefDocument(t, time.Until(deadline))
		if pushed.BriefID == brief.BriefID && pushed.Audio != nil && pushed.Audio.Attachment != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the brief with its recording never reached the phone: %+v", pushed.Audio)
		}
	}
	version := mobileBriefPackageVersion(document)
	if len(pushed.Narrative) != 2 || !strings.HasPrefix(pushed.Narrative[0].Text, "Good evening. Most of the afternoon") ||
		pushed.Narrative[1].Text != brief.Narrative[2].Text || pushed.Kind != mobileBriefSlotOnRequest {
		t.Fatalf("the phone holds another text: %+v", pushed.Narrative)
	}
	if len(pushed.Audio.Paragraphs) != 2 || pushed.Audio.Paragraphs[0].Index != 0 || pushed.Audio.Paragraphs[1].Index != 1 {
		t.Fatalf("paragraph starts do not follow the text: %+v", pushed.Audio.Paragraphs)
	}

	// The by-ID route serves the same bytes.
	code, body := phone.request(t, "GET", "/briefs/"+brief.BriefID, "")
	var byID struct {
		Data json.RawMessage `json:"data"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &byID) != nil || !bytes.Equal(byID.Data, document) {
		t.Fatalf("GET /briefs/{id} serves other bytes: %d %.300s", code, body)
	}

	checkMobileE2ERecording(t, phone, pushed.Audio)

	// Today names this brief and version.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(2 * time.Second) {
		code, body := phone.request(t, "GET", "/today", "")
		var today struct {
			Data struct {
				CurrentBrief *mobileTodayBriefRef `json:"currentBrief"`
			} `json:"data"`
		}
		if code == http.StatusOK && json.Unmarshal([]byte(body), &today) == nil && today.Data.CurrentBrief != nil &&
			today.Data.CurrentBrief.ID == brief.BriefID && today.Data.CurrentBrief.Version == version {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Today names %+v, the phone holds %s %s", today.Data.CurrentBrief, brief.BriefID, version)
		}
	}

	// The phone says it saved the package; the outbox lets it go.
	code, body = phone.request(t, "POST", "/briefs/"+brief.BriefID+"/received", `{"version":"`+version+`"}`)
	if code != http.StatusOK {
		t.Fatalf("received: %d %s", code, body)
	}
	if _, queued := app.mobileBridgeForUse().briefOutbox.entry(phone.deviceID); queued {
		t.Fatal("the outbox kept a brief the phone saved")
	}
}

func checkMobileE2ERecording(t *testing.T, phone *mobileE2EPhone, recording *MobileBriefAudio) {
	t.Helper()
	// The recording on the relay is the one the brief names.
	if recording == nil || recording.Attachment == nil {
		t.Fatal("phone package has no recording attachment")
	}
	attachment := recording.Attachment
	request, _ := http.NewRequest(http.MethodGet, phone.relay+"/v1/attachments/"+attachment.ID, nil)
	request.Header.Set("Authorization", "Bearer "+phone.bearer)
	response, err := phone.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := io.ReadAll(response.Body)
	response.Body.Close()
	key, _ := base64.RawURLEncoding.DecodeString(attachment.Key)
	nonce, _ := base64.RawURLEncoding.DecodeString(attachment.Nonce)
	audio, err := openMobileAttachment(key, nonce, phone.invitation.Hub, attachment.ID, sealed)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("the recording could not be opened: %d %v", response.StatusCode, err)
	}
	if digest := sha256.Sum256(audio); hex.EncodeToString(digest[:]) != recording.SHA256 || len(audio) != recording.Bytes {
		t.Fatal("the recording does not match the brief")
	}
}

// mobileE2EInstallChatSeams answers chat turns with a canned reply and removes
// memories without touching the user's memory folder, so no model is called.
func mobileE2EInstallChatSeams(t *testing.T) {
	t.Helper()
	turns := &mobileTestTurns{}
	mobileChatRunnerOverride = mobileTestMessageOverride(turns)
	mobileMemoryRejectOverride = func(memoryID, reason string) error {
		fmt.Printf("MOBILE_E2E_MEMORY_REJECTED %s\n", memoryID)
		return nil
	}
	t.Cleanup(func() { mobileChatRunnerOverride, mobileMemoryRejectOverride = nil, nil })
}

// mobileE2EChatFlow drives the chat routes the way the phone does: create a
// chat, send, replay the send, see the completion event, read the reply with
// the cursor, cancel a held turn, and correct a memory.
func mobileE2EChatFlow(t *testing.T, phone *mobileE2EPhone) {
	t.Helper()
	code, body := phone.request(t, "POST", "/chats", `{"idempotencyKey":"e2e-chat-create","title":"E2E design chat"}`)
	if code != http.StatusCreated {
		t.Fatalf("create chat: %d %s", code, body)
	}
	chatID := mobileE2EData(t, body)["id"]
	if chatID == "" {
		t.Fatalf("create chat returned no id: %s", body)
	}

	code, body = phone.request(t, "POST", "/chats/"+chatID+"/messages", `{"idempotencyKey":"e2e-send-1","text":"Summarize the design notes"}`)
	if code != http.StatusAccepted {
		t.Fatalf("send: %d %s", code, body)
	}
	accepted := mobileE2EData(t, body)
	turnID := accepted["turnId"]
	if seq := accepted["seq"]; seq == "" || seq == "0" {
		t.Fatalf("the 202 must name the stored item's seq: %s", body)
	}
	code, replay := phone.request(t, "POST", "/chats/"+chatID+"/messages", `{"idempotencyKey":"e2e-send-1","text":"Summarize the design notes"}`)
	if code != http.StatusAccepted || replay != body {
		t.Fatalf("replayed send: %d %s, want %s", code, replay, body)
	}
	finished := phone.awaitEvent(t, func(event map[string]any) bool {
		return event["event"] == mobileEventChatTurn && event["chatId"] == chatID && event["state"] == mobileChatEventDone && event["turnId"] == turnID
	}, 20*time.Second)
	if _, hasContent := finished["text"]; hasContent {
		t.Fatalf("a chat event carried content: %+v", finished)
	}

	code, body = phone.requestWithQuery(t, "GET", "/chats/"+chatID, "after=0", "")
	if code != http.StatusOK || !strings.Contains(body, "Canned reply.") || !strings.Contains(body, "Summarize the design notes") {
		t.Fatalf("read the chat: %d %s", code, body)
	}
	code, body = phone.requestWithQuery(t, "GET", "/chats", "limit=20", "")
	if code != http.StatusOK || !strings.Contains(body, chatID) || !strings.Contains(body, `"state":"idle"`) {
		t.Fatalf("chat list after the turn: %d %s", code, body)
	}

	code, body = phone.request(t, "POST", "/chats/"+chatID+"/messages", `{"idempotencyKey":"e2e-send-2","text":"hold"}`)
	if code != http.StatusAccepted {
		t.Fatalf("held send: %d %s", code, body)
	}
	heldTurn := mobileE2EData(t, body)["turnId"]
	code, body = phone.request(t, "GET", "/chats/"+chatID, "")
	if code != http.StatusOK || !strings.Contains(body, `"state":"running"`) {
		t.Fatalf("the held turn is not running: %d %s", code, body)
	}
	code, body = phone.request(t, "POST", "/chats/"+chatID+"/cancel", `{"idempotencyKey":"e2e-cancel-1"}`)
	if code != http.StatusOK || !strings.Contains(body, `"cancelled":true`) {
		t.Fatalf("cancel: %d %s", code, body)
	}
	phone.awaitEvent(t, func(event map[string]any) bool {
		return event["event"] == mobileEventChatTurn && event["chatId"] == chatID && event["turnId"] == heldTurn && event["state"] == mobileChatEventDone
	}, 20*time.Second)

	code, body = phone.request(t, "POST", "/memory/e2e-memory-1/reject-attribution", `{"idempotencyKey":"e2e-memory-1","reason":"Wrong about my team"}`)
	if code != http.StatusOK || !strings.Contains(body, `"removed":true`) {
		t.Fatalf("memory correction: %d %s", code, body)
	}
}

// mobileE2EData returns the string fields of a response document's data.
func mobileE2EData(t *testing.T, body string) map[string]string {
	t.Helper()
	var reply mobileE2EReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	// Values are strings, except numbers such as the 202's seq, which come back in decimal.
	var raw map[string]any
	if err := json.Unmarshal(reply.Data, &raw); err != nil {
		t.Fatalf("decode data %q: %v", reply.Data, err)
	}
	fields := make(map[string]string, len(raw))
	for key, value := range raw {
		fields[key] = fmt.Sprint(value)
	}
	return fields
}

// mobileE2ESampleBrief is a realistic document for the simulator check.
func mobileE2ESampleBrief(hubName string) MobileBriefV1 {
	now := time.Now()
	at := func(hoursAgo int) string {
		return now.Add(-time.Duration(hoursAgo) * time.Hour).UTC().Format(time.RFC3339)
	}
	day := now.Format("2006-01-02")
	hours := []MobileBriefHour{}
	for hour, minutes := range map[int]int{9: 38, 10: 52, 11: 44, 13: 31, 14: 57, 15: 49, 16: 36, 17: 22} {
		hours = append(hours, MobileBriefHour{Hour: hour, ActiveMinutes: minutes})
	}
	return MobileBriefV1{
		Version: 1, BriefID: day + "-evening", Slot: "evening", LocalDate: day, Timezone: now.Location().String(),
		WindowStart: at(11), AsOf: now.UTC().Format(time.RFC3339), HubName: hubName, NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{
			{Text: "Most of the afternoon went to the onboarding flow in Figma, with two Slack threads about the launch date in between.", Refs: []string{"e1", "e5"}},
			{Text: "Yansu drafted the handoff notes for Thursday's design review from the same files; its result matched yours. Why the launch date moved is unknown.", Refs: []string{"e2"}},
		},
		NeedsYouCount: 2,
		Sections: MobileBriefSections{
			Moved: []MobileBriefItem{
				{ID: "m1", Title: "Drafted the design review handoff notes", Detail: "Matched the version you edited later.", Status: "verified", At: at(3), Kind: "history", Refs: []string{"e2"}},
				{ID: "m2", Title: "Filed the release checklist in Notion", Detail: "Receipt only; the page has not been opened since.", Status: "receipt only", At: at(6), Kind: "history", Refs: []string{"e3"}},
			},
			Doing:    []MobileBriefItem{{ID: "d1", Title: "Collecting pricing examples for the Pro plan page", Detail: "Waiting on a reply from Mia Chen.", Status: "waiting", At: at(1), Kind: "initiative", Refs: []string{"e4"}}},
			Watching: []MobileBriefItem{{ID: "w1", Title: "Launch date thread in #product", Detail: "Watching for a decision.", Status: "watching", At: at(2), Kind: "watching", Refs: []string{"e5"}}},
			Meetings: []MobileBriefItem{{ID: "mt1", Title: "Zoom", Detail: "10:00 to 10:45", Status: "notes ready", At: at(8), Kind: "meeting", Refs: []string{"e6"}}},
			People: []MobileBriefItem{
				{ID: "p1", Title: "Mia Chen", Detail: "Slack, 4 messages", Status: "seen", At: at(2), Kind: "principal", Refs: []string{"e7"}},
				{ID: "p2", Title: "Daniel Ortega", Detail: "Mail, 1 thread", Status: "seen", At: at(5), Kind: "principal", Refs: []string{"e8"}},
			},
			OpenLoops: []MobileBriefItem{
				{ID: "o1", Title: "Send the onboarding spec to Mia by Thursday", Detail: "", Status: "commitment", At: at(4), Kind: "purpose", Refs: []string{"e7"}},
				{ID: "o2", Title: "Ship the Pro plan page", Detail: "", Status: "goal", At: at(30), Kind: "purpose", Refs: []string{"e4"}},
				{ID: "o3", Title: "Analytics events are missing for the new flow", Detail: "", Status: "concern", At: at(26), Kind: "purpose", Refs: []string{"e1"}},
			},
			Learned:      []MobileBriefItem{{ID: "l1", Title: "Design reviews start from the Figma file, not the doc", Detail: "Supported by 3 compared tasks.", Status: "supported", At: at(3), Kind: "learning", Refs: []string{"e2"}}},
			Deliverables: []MobileBriefItem{{ID: "dl1", Title: "handoff-notes.md", Detail: "", Status: "ready", At: at(3), Kind: "deliverable", Refs: []string{"e2"}}},
			Schedules:    []MobileBriefItem{{ID: "s1", Title: "Daily news digest", Detail: "Ran at 07:00", Status: "completed", At: at(10), Kind: "schedule_run", Refs: []string{"e3"}}},
		},
		DayShape: &MobileBriefDay{ActiveMinutes: 329, Hours: hours, TopApps: []MobileBriefAppTime{{Name: "Figma", ActiveMinutes: 95}, {Name: "Chrome", ActiveMinutes: 60}, {Name: "Slack", ActiveMinutes: 40}, {Name: "Notion", ActiveMinutes: 25}}},
		Counts:   MobileBriefCounts{OpenLoopsByKind: map[string]int{"commitment": 1, "goal": 1, "concern": 1}, SelfUpdatesPending: 1, LearningItems: 1, NeedsYou: 2},
		Evidence: []MobileBriefRef{
			{Ref: "e1", SourceKind: "summary", SourceID: day, Label: "Activity on " + day, ObservedAt: at(1)},
			{Ref: "e2", SourceKind: "history", SourceID: "history-1", Label: "Design review handoff notes", ObservedAt: at(3)},
			{Ref: "e3", SourceKind: "schedule_run", SourceID: "run-7", Label: "Daily news digest", ObservedAt: at(10)},
			{Ref: "e4", SourceKind: "initiative", SourceID: "initiative-2", Label: "Pro plan page pricing examples", ObservedAt: at(1)},
			{Ref: "e5", SourceKind: "watching", SourceID: "watch-3", Label: "Launch date thread", ObservedAt: at(2)},
			{Ref: "e6", SourceKind: "meeting", SourceID: "session-91", Label: "Zoom meeting 10:00", ObservedAt: at(8)},
			{Ref: "e7", SourceKind: "principal", SourceID: "principal-mia", Label: "Mia Chen", ObservedAt: at(2)},
			{Ref: "e8", SourceKind: "principal", SourceID: "principal-daniel", Label: "Daniel Ortega", ObservedAt: at(5)},
		},
		Limits: MobileBriefLimits{ItemsPerSection: 8, EvidenceRefs: 60, TruncatedSections: []string{}},
	}
}

// mobileE2ENote mirrors hold-mode progress into files named by
// YANSU_MOBILE_E2E_INVITATION_FILE (the current invitation) and its ".events"
// sibling, because test output is buffered until the run ends.
func mobileE2ENote(invitation, event string) {
	path := os.Getenv("YANSU_MOBILE_E2E_INVITATION_FILE")
	if path == "" {
		return
	}
	if invitation != "" {
		_ = os.WriteFile(path, []byte(invitation+"\n"), 0o600)
	}
	if file, err := os.OpenFile(path+".events", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = file.WriteString(time.Now().Format(time.RFC3339) + " " + event + "\n")
		file.Close()
	}
}
