package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

// updateMobileVectors regenerates mobile-protocol/vectors-v1.json:
//
//	go test -run TestMobileProtocolVectors -update .
var updateMobileVectors = flag.Bool("update", false, "rewrite mobile-protocol/vectors-v1.json from the implementation")

// mobileVectorsPath is relative to this package directory (wails-gui).
const mobileVectorsPath = "../mobile-protocol/vectors-v1.json"

const (
	mobileVectorHubID     = "6f1c2e4a-7b3d-4c5e-9a1b-2d3e4f5a6b7c"
	mobileVectorDeviceID  = "0b9d6e2c-3f4a-4e5b-8c7d-1a2b3c4d5e6f"
	mobileVectorPairingID = "a7e3c1d2-5b4a-4f6e-8d9c-0e1f2a3b4c5d"
	mobileVectorRequestID = "c4b8f1e0-2d3a-4b5c-9e8f-7a6b5c4d3e2f"
	mobileVectorExpiresAt = "2027-01-07T07:00:00Z"
	mobileVectorNoticeID  = "5c1e7a2b-9d4f-4a3e-8b6c-7f0e1d2c3b4a"
)

// Field order and names follow PROTOCOL.md section 11.
type mobileVectorMessage struct {
	Plaintext string `json:"plaintext"`
	CT        string `json:"ct"`
}

type mobileVectorEnvelope struct {
	Dir       string `json:"dir"`
	Seq       uint64 `json:"seq"`
	Kind      string `json:"kind"`
	Plaintext string `json:"plaintext"`
	CT        string `json:"ct"`
}

type mobileVectorsFile struct {
	Version       int                    `json:"version"`
	HubPrivate    string                 `json:"hubPrivate"`
	HubPublic     string                 `json:"hubPublic"`
	DevicePrivate string                 `json:"devicePrivate"`
	DevicePublic  string                 `json:"devicePublic"`
	HubID         string                 `json:"hubId"`
	DeviceID      string                 `json:"deviceId"`
	PairingID     string                 `json:"pairingId"`
	Secret        string                 `json:"secret"`
	KPair         string                 `json:"kPair"`
	ClaimToken    string                 `json:"claimToken"`
	ConfirmCode   string                 `json:"confirmCode"`
	KD2H          string                 `json:"kD2H"`
	KH2D          string                 `json:"kH2D"`
	Claim         mobileVectorMessage    `json:"claim"`
	Approval      mobileVectorMessage    `json:"approval"`
	Envelopes     []mobileVectorEnvelope `json:"envelopes"`
	// NoticeSecret is the X25519 session secret; KNotice derives from it
	// (PROTOCOL.md section 14.1). Notice is one sealed moment notice.
	NoticeSecret     string                       `json:"noticeSecret"`
	KNotice          string                       `json:"kNotice"`
	Notice           mobileVectorNotice           `json:"notice"`
	RemoteInvitation mobileVectorRemoteInvitation `json:"remoteInvitation"`
	PushGatewayAuth  json.RawMessage              `json:"pushGatewayAuth"`
}

// mobileVectorNotice is one sealed notice: the plaintext as sealed (padded), the
// nonce used for this vector only, and the ciphertext (nonce || sealed, base64url).
type mobileVectorNotice struct {
	ID        string `json:"id"`
	Nonce     string `json:"nonce"`
	Plaintext string `json:"plaintext"`
	CT        string `json:"ct"`
}

type mobileVectorRemoteInvitation struct {
	Plaintext string `json:"plaintext"`
	Link      string `json:"link"`
}

// mobileVectorNoticeValue is the notice the vector seals.
func mobileVectorNoticeValue() mobileNotice {
	created, _ := time.Parse(time.RFC3339, "2026-10-09T18:00:00Z")
	expires, _ := time.Parse(time.RFC3339, "2026-10-10T18:00:00Z")
	return mobileNotice{
		ID:        mobileVectorNoticeID,
		Kind:      "decision_due",
		Title:     "Approve sending the Q3 reply to Sam?",
		Body:      "Sam has waited since 09:12.",
		Category:  "YANSU_DECISION",
		SubjectID: mobileVectorRequestID,
		Level:     "time-sensitive",
		Actions: []mobileNoticeAction{
			{ID: "approve", Label: "Approve", Auth: true},
			{ID: "decline", Label: "Decline"},
			{ID: "open", Label: "Open"},
		},
		CreatedAt: created,
		ExpiresAt: expires,
	}
}

// mobileVectorSeed derives every fixed key and secret from a label, so the
// vectors are reproducible without storing private material by hand.
func mobileVectorSeed(label string) []byte {
	digest := sha256.Sum256([]byte("yansu mobile protocol vectors v1 " + label))
	return digest[:]
}

func mobileVectorText(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func buildMobileVectors(t *testing.T) mobileVectorsFile {
	t.Helper()
	hubPrivate, err := ecdh.X25519().NewPrivateKey(mobileVectorSeed("hub private key"))
	if err != nil {
		t.Fatal(err)
	}
	devicePrivate, err := ecdh.X25519().NewPrivateKey(mobileVectorSeed("device private key"))
	if err != nil {
		t.Fatal(err)
	}
	secret := mobileVectorSeed("invitation secret")
	kPair, err := mobilePairKey(devicePrivate, hubPrivate.PublicKey(), secret, mobileVectorHubID, mobileVectorPairingID)
	if err != nil {
		t.Fatal(err)
	}
	code, err := mobileConfirmationCode(kPair)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := mobileDeriveSessionKeys(hubPrivate, devicePrivate.PublicKey(), mobileVectorHubID, mobileVectorDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	// PROTOCOL.md section 6: digests are SHA-256 of the exact base64url text.
	bearerDigest := sha256.Sum256([]byte(mobileVectorText(mobileVectorSeed("device bearer"))))

	claimPlaintext, err := json.Marshal(mobileClaimPlaintext{
		DeviceID: mobileVectorDeviceID, DeviceName: "Alex's iPhone", DevicePub: mobileVectorText(devicePrivate.PublicKey().Bytes()),
		BearerHash: hex.EncodeToString(bearerDigest[:]), Platform: "ios", AppVersion: "1.0 (12)",
		Scopes:          []string{mobileScopeBriefRead, mobileScopeBriefRefresh, mobileScopePresenceRead, mobileScopePush},
		PushEnvironment: "development",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimCT, err := sealMobilePairingMessage(kPair, mobilePairingClaimNonceByte, mobileClaimAAD(mobileVectorHubID, mobileVectorPairingID), claimPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	approvalPlaintext, err := json.Marshal(mobileApprovalPlaintext{
		DeviceID: mobileVectorDeviceID, HubName: "Alex's MacBook Pro",
		Scopes:    []string{mobileScopeBriefRead, mobileScopeBriefRefresh, mobileScopePresenceRead, mobileScopePush},
		ExpiresAt: mobileVectorExpiresAt, HubPub: mobileVectorText(hubPrivate.PublicKey().Bytes()),
		// Since B12 the approval names the brief the phone gets right after pairing.
		BriefStatus: &mobileBriefStatus{
			Current: &mobileBriefStatusCurrent{ID: "2026-10-09-morning", Version: strings.Repeat("ab", 32), Kind: "morning", MadeAt: "2026-10-09T07:00:12Z", Audio: true},
			Next:    &mobileBriefStatusNext{Kind: "evening", Slot: "evening", DueAt: "2026-10-09T18:00:00Z"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	approvalCT, err := sealMobilePairingMessage(kPair, mobilePairingApprovalNonceByte, mobileApprovalAAD(mobileVectorHubID, mobileVectorPairingID), approvalPlaintext)
	if err != nil {
		t.Fatal(err)
	}

	requestPlaintext, err := json.Marshal(mobileRequestPlaintext{ID: mobileVectorRequestID, Method: "GET", Path: "/brief/latest"})
	if err != nil {
		t.Fatal(err)
	}
	responsePlaintext, err := json.Marshal(mobileResponsePlaintext{ID: mobileVectorRequestID, Status: 200, Body: `{"ok":true,"data":{}}`})
	if err != nil {
		t.Fatal(err)
	}
	request, err := sealMobileEnvelope(keys.D2H, mobileVectorHubID, mobileVectorDeviceID, mobileDirD2H, 1, mobileKindRequest, requestPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	response, err := sealMobileEnvelope(keys.H2D, mobileVectorHubID, mobileVectorDeviceID, mobileDirH2D, 1, mobileKindResponse, responsePlaintext)
	if err != nil {
		t.Fatal(err)
	}
	chatItems := make([]mobileChatItemView, 0, 2)
	for _, row := range mobileProactiveTestRows() {
		view, ok := mobileChatItemFromRow(row)
		if !ok {
			t.Fatal("structured chat vector did not project")
		}
		chatItems = append(chatItems, view)
	}
	chatBody, err := json.Marshal(map[string]any{
		"ok": true,
		"data": map[string]any{
			"chat":  map[string]string{"id": "main-chat", "title": "Isoform", "state": "idle"},
			"items": chatItems, "cursor": 2, "hasMore": false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chatPlaintext, err := json.Marshal(mobileResponsePlaintext{ID: mobileVectorRequestID, Status: 200, Body: string(chatBody)})
	if err != nil {
		t.Fatal(err)
	}
	chatResponse, err := sealMobileEnvelope(keys.H2D, mobileVectorHubID, mobileVectorDeviceID, mobileDirH2D, 2, mobileKindResponse, chatPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	// Forget everything on the phone (PROTOCOL.md 28): its own h2d kind.
	forgetPlaintext, err := json.Marshal(mobileForgetAllPlaintext{V: 1, JobID: mobileVectorRequestID, At: "2026-11-16T08:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	forgetAll, err := sealMobileEnvelope(keys.H2D, mobileVectorHubID, mobileVectorDeviceID, mobileDirH2D, 3, mobileKindForgetAll, forgetPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	noticeSecret, err := hubPrivate.ECDH(devicePrivate.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	noticeKey := mobileNoticeKey(mobileVectorHubID, mobileVectorDeviceID, noticeSecret)
	notice := mobileVectorNoticeValue()
	noticeNonce := mobileVectorSeed("notice nonce")[:mobileNoticeNonceSize]
	noticePlaintext, err := mobileNoticeEncode(notice)
	if err != nil {
		t.Fatal(err)
	}
	noticeCT, err := sealMobileNoticeWithNonce(noticeKey, mobileVectorHubID, mobileVectorDeviceID, notice, noticeNonce)
	if err != nil {
		t.Fatal(err)
	}
	remoteInvitation := mobileInvitation{
		V: 1, Relay: "http://127.0.0.1:8791", Hub: mobileVectorHubID,
		Pairing: mobileVectorPairingID, Secret: mobileVectorText(secret),
		HubPub: mobileVectorText(hubPrivate.PublicKey().Bytes()), Exp: 1760000000,
		Iroh: "endpointabcdefghijklmnopqrstuvwxyz",
	}
	remoteInvitationPlaintext, err := json.Marshal(remoteInvitation)
	if err != nil {
		t.Fatal(err)
	}
	remoteInvitationLink, err := encodeMobileInvitation(remoteInvitation)
	if err != nil {
		t.Fatal(err)
	}

	return mobileVectorsFile{
		Version:       1,
		HubPrivate:    mobileVectorText(hubPrivate.Bytes()),
		HubPublic:     mobileVectorText(hubPrivate.PublicKey().Bytes()),
		DevicePrivate: mobileVectorText(devicePrivate.Bytes()),
		DevicePublic:  mobileVectorText(devicePrivate.PublicKey().Bytes()),
		HubID:         mobileVectorHubID,
		DeviceID:      mobileVectorDeviceID,
		PairingID:     mobileVectorPairingID,
		Secret:        mobileVectorText(secret),
		KPair:         mobileVectorText(kPair),
		ClaimToken:    mobileClaimToken(secret, mobileVectorPairingID),
		ConfirmCode:   code,
		KD2H:          mobileVectorText(keys.D2H),
		KH2D:          mobileVectorText(keys.H2D),
		Claim:         mobileVectorMessage{Plaintext: string(claimPlaintext), CT: mobileVectorText(claimCT)},
		Approval:      mobileVectorMessage{Plaintext: string(approvalPlaintext), CT: mobileVectorText(approvalCT)},
		Envelopes: []mobileVectorEnvelope{
			{Dir: mobileDirD2H, Seq: 1, Kind: mobileKindRequest, Plaintext: string(requestPlaintext), CT: request.CT},
			{Dir: mobileDirH2D, Seq: 1, Kind: mobileKindResponse, Plaintext: string(responsePlaintext), CT: response.CT},
			{Dir: mobileDirH2D, Seq: 2, Kind: mobileKindResponse, Plaintext: string(chatPlaintext), CT: chatResponse.CT},
			{Dir: mobileDirH2D, Seq: 3, Kind: mobileKindForgetAll, Plaintext: string(forgetPlaintext), CT: forgetAll.CT},
		},
		NoticeSecret: mobileVectorText(noticeSecret),
		KNotice:      mobileVectorText(noticeKey),
		Notice: mobileVectorNotice{
			ID:        notice.ID,
			Nonce:     mobileVectorText(noticeNonce),
			Plaintext: string(noticePlaintext),
			CT:        noticeCT,
		},
		RemoteInvitation: mobileVectorRemoteInvitation{
			Plaintext: string(remoteInvitationPlaintext),
			Link:      remoteInvitationLink,
		},
	}
}

// TestMobileProtocolVectors writes the vectors with -update. Without the flag it
// loads the committed file and recomputes every derived value and every
// ciphertext from the private keys, so the file cannot drift from the code.
func TestMobileProtocolVectors(t *testing.T) {
	generated := buildMobileVectors(t)
	if *updateMobileVectors {
		if current, err := os.ReadFile(mobileVectorsPath); err == nil {
			var preserved struct {
				PushGatewayAuth json.RawMessage `json:"pushGatewayAuth"`
			}
			if json.Unmarshal(current, &preserved) == nil {
				generated.PushGatewayAuth = preserved.PushGatewayAuth
			}
		}
		raw, err := json.MarshalIndent(generated, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mobileVectorsPath, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", mobileVectorsPath)
		return
	}
	raw, err := os.ReadFile(mobileVectorsPath)
	if err != nil {
		t.Fatalf("vectors file is missing; run go test -run TestMobileProtocolVectors -update . (%v)", err)
	}
	var file mobileVectorsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	compare := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: file has %q, the implementation derives %q", name, got, want)
		}
	}
	if file.Version != 1 {
		t.Fatalf("vectors version %d, want 1", file.Version)
	}
	compare("hubId", file.HubID, generated.HubID)
	compare("deviceId", file.DeviceID, generated.DeviceID)
	compare("pairingId", file.PairingID, generated.PairingID)
	compare("hubPrivate", file.HubPrivate, generated.HubPrivate)
	compare("hubPublic", file.HubPublic, generated.HubPublic)
	compare("devicePrivate", file.DevicePrivate, generated.DevicePrivate)
	compare("devicePublic", file.DevicePublic, generated.DevicePublic)
	compare("secret", file.Secret, generated.Secret)
	compare("kPair", file.KPair, generated.KPair)
	compare("claimToken", file.ClaimToken, generated.ClaimToken)
	compare("confirmCode", file.ConfirmCode, generated.ConfirmCode)
	compare("kD2H", file.KD2H, generated.KD2H)
	compare("kH2D", file.KH2D, generated.KH2D)
	compare("remote invitation plaintext", file.RemoteInvitation.Plaintext, generated.RemoteInvitation.Plaintext)
	if !strings.HasPrefix(generated.RemoteInvitation.Link, mobileInvitationPrefix) {
		t.Fatalf("remote invitation link does not use %s", mobileInvitationPrefix)
	}
	compare("remote invitation link", file.RemoteInvitation.Link, generated.RemoteInvitation.Link)
	var remoteInvitation mobileInvitation
	if err := mobileDecodeStrict([]byte(file.RemoteInvitation.Plaintext), &remoteInvitation); err != nil {
		t.Fatal(err)
	}
	if remoteInvitation.Iroh != "endpointabcdefghijklmnopqrstuvwxyz" || remoteInvitation.Exp != 1760000000 {
		t.Fatalf("remote invitation vector has incorrect ticket or expiry: %+v", remoteInvitation)
	}
	if link, err := encodeMobileInvitation(remoteInvitation); err != nil || link != file.RemoteInvitation.Link {
		t.Fatalf("remote invitation link does not encode its plaintext: %v", err)
	}

	// Recompute the public keys and the keys from the private material in the file.
	hubPrivateRaw, err := base64.RawURLEncoding.DecodeString(file.HubPrivate)
	if err != nil {
		t.Fatal(err)
	}
	hubPrivate, err := ecdh.X25519().NewPrivateKey(hubPrivateRaw)
	if err != nil {
		t.Fatal(err)
	}
	devicePrivateRaw, err := base64.RawURLEncoding.DecodeString(file.DevicePrivate)
	if err != nil {
		t.Fatal(err)
	}
	devicePrivate, err := ecdh.X25519().NewPrivateKey(devicePrivateRaw)
	if err != nil {
		t.Fatal(err)
	}
	compare("hubPublic (recomputed)", file.HubPublic, mobileVectorText(hubPrivate.PublicKey().Bytes()))
	compare("devicePublic (recomputed)", file.DevicePublic, mobileVectorText(devicePrivate.PublicKey().Bytes()))
	secret, _ := base64.RawURLEncoding.DecodeString(file.Secret)
	kPair, err := mobilePairKey(devicePrivate, hubPrivate.PublicKey(), secret, file.HubID, file.PairingID)
	if err != nil {
		t.Fatal(err)
	}
	compare("kPair (recomputed)", file.KPair, mobileVectorText(kPair))
	compare("claimToken (recomputed)", file.ClaimToken, mobileClaimToken(secret, file.PairingID))
	code, err := mobileConfirmationCode(kPair)
	if err != nil {
		t.Fatal(err)
	}
	compare("confirmCode (recomputed)", file.ConfirmCode, code)
	keys, err := mobileDeriveSessionKeys(devicePrivate, hubPrivate.PublicKey(), file.HubID, file.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	compare("kD2H (recomputed)", file.KD2H, mobileVectorText(keys.D2H))
	compare("kH2D (recomputed)", file.KH2D, mobileVectorText(keys.H2D))

	// Each ciphertext must open to its plaintext and must reseal to the same bytes.
	checkPairing := func(name string, message mobileVectorMessage, last byte, aad string) {
		t.Helper()
		ciphertext, err := base64.RawURLEncoding.DecodeString(message.CT)
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := openMobilePairingMessage(kPair, last, aad, ciphertext)
		if err != nil || string(plaintext) != message.Plaintext {
			t.Fatalf("%s does not open to its plaintext: %v", name, err)
		}
		resealed, err := sealMobilePairingMessage(kPair, last, aad, plaintext)
		if err != nil || !bytes.Equal(resealed, ciphertext) {
			t.Fatalf("%s does not reseal to the same ciphertext", name)
		}
	}
	checkPairing("claim", file.Claim, mobilePairingClaimNonceByte, mobileClaimAAD(file.HubID, file.PairingID))
	checkPairing("approval", file.Approval, mobilePairingApprovalNonceByte, mobileApprovalAAD(file.HubID, file.PairingID))
	var claim mobileClaimPlaintext
	if err := mobileDecodeStrict([]byte(file.Claim.Plaintext), &claim); err != nil {
		t.Fatal(err)
	}
	compare("claim devicePub", claim.DevicePub, file.DevicePublic)
	if _, err := decodeMobileClaim([]byte(file.Claim.Plaintext), file.DeviceID, file.DevicePublic); err != nil {
		t.Fatalf("the claim vector is not a valid claim: %v", err)
	}

	if len(file.Envelopes) != len(generated.Envelopes) {
		t.Fatalf("expected %d envelope vectors, found %d", len(generated.Envelopes), len(file.Envelopes))
	}
	for i, vector := range file.Envelopes {
		want := generated.Envelopes[i]
		if vector.Dir != want.Dir || vector.Seq != want.Seq || vector.Kind != want.Kind {
			t.Fatalf("envelope %d header differs from the implementation", i)
		}
		compare("envelope plaintext", vector.Plaintext, want.Plaintext)
		compare("envelope ciphertext", vector.CT, want.CT)
		key := keys.D2H
		if vector.Dir == mobileDirH2D {
			key = keys.H2D
		}
		env := mobileEnvelope{V: 1, Hub: file.HubID, Dev: file.DeviceID, Dir: vector.Dir, Seq: vector.Seq, Kind: vector.Kind, CT: vector.CT}
		plaintext, err := openMobileEnvelope(key, env, file.HubID, file.DeviceID, vector.Dir)
		if err != nil || string(plaintext) != vector.Plaintext {
			t.Fatalf("%s envelope %d does not open to its plaintext: %v", vector.Dir, vector.Seq, err)
		}
		resealed, err := sealMobileEnvelope(key, file.HubID, file.DeviceID, vector.Dir, vector.Seq, vector.Kind, plaintext)
		if err != nil || resealed.CT != vector.CT {
			t.Fatalf("%s envelope %d does not reseal to the same ciphertext", vector.Dir, vector.Seq)
		}
		if vector.Dir == mobileDirH2D && vector.Seq == 2 {
			var response mobileResponsePlaintext
			if err := mobileDecodeStrict(plaintext, &response); err != nil {
				t.Fatal(err)
			}
			var body struct {
				Data mobileTestChatDetail `json:"data"`
			}
			if err := json.Unmarshal([]byte(response.Body), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Data.Items) != 2 || body.Data.Items[0].Proactive == nil || body.Data.Items[1].Status == nil {
				t.Fatalf("structured chat vector did not decode: %+v", body.Data)
			}
			for j, row := range mobileProactiveTestRows() {
				compare("chat fallback text", body.Data.Items[j].Text, row.Content)
			}
		}
	}

	// The notice: the secret and key derive from the file's private keys; the
	// sealed plaintext opens to the expected notice; the ciphertext reseals.
	compare("notice nonce", file.Notice.Nonce, generated.Notice.Nonce)
	compare("notice id", file.Notice.ID, mobileVectorNoticeID)
	noticeSecret, err := hubPrivate.ECDH(devicePrivate.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	compare("noticeSecret (recomputed)", file.NoticeSecret, mobileVectorText(noticeSecret))
	noticeKey := mobileNoticeKey(file.HubID, file.DeviceID, noticeSecret)
	compare("kNotice (recomputed)", file.KNotice, mobileVectorText(noticeKey))
	expectedPlaintext, err := mobileNoticeEncode(mobileVectorNoticeValue())
	if err != nil {
		t.Fatal(err)
	}
	compare("notice plaintext (recomputed)", file.Notice.Plaintext, string(expectedPlaintext))
	padded, err := openMobileNoticePlaintext(noticeKey, file.HubID, file.DeviceID, file.Notice.ID, file.Notice.CT)
	if err != nil || string(padded) != file.Notice.Plaintext {
		t.Fatalf("notice does not open to its plaintext: %v", err)
	}
	if opened, err := openMobileNotice(noticeKey, file.HubID, file.DeviceID, file.Notice.ID, file.Notice.CT); err != nil || opened.Title != mobileVectorNoticeValue().Title {
		t.Fatalf("notice does not decode: %+v %v", opened, err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(file.Notice.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	resealedNotice, err := sealMobileNoticeWithNonce(noticeKey, file.HubID, file.DeviceID, mobileVectorNoticeValue(), nonce)
	if err != nil || resealedNotice != file.Notice.CT {
		t.Fatalf("notice does not reseal to the same ciphertext: %v", err)
	}
}

func TestMobilePushGatewayAuthVector(t *testing.T) {
	raw, err := os.ReadFile(mobileVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		PushGatewayAuth struct {
			GatewayPublic string `json:"gatewayPublic"`
			HubPrivate    string `json:"hubPrivate"`
			HubPublic     string `json:"hubPublic"`
			HubKey        string `json:"hubKey"`
			HubID         string `json:"hubId"`
		} `json:"pushGatewayAuth"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	decodedPrivate, err := base64.RawURLEncoding.DecodeString(vectors.PushGatewayAuth.HubPrivate)
	if err != nil {
		t.Fatal(err)
	}
	hubPrivate, err := ecdh.X25519().NewPrivateKey(decodedPrivate)
	if err != nil {
		t.Fatal(err)
	}
	hubPublic, hubKey, err := mobilePushGatewayHubConfig(hubPrivate, vectors.PushGatewayAuth.GatewayPublic)
	if err != nil {
		t.Fatal(err)
	}
	if hubPublic != vectors.PushGatewayAuth.HubPublic || hubKey != vectors.PushGatewayAuth.HubKey {
		t.Fatalf("desktop gateway vector = %q %q, want %q %q", hubPublic, hubKey,
			vectors.PushGatewayAuth.HubPublic, vectors.PushGatewayAuth.HubKey)
	}
	if vectors.PushGatewayAuth.HubID != mobileVectorHubID {
		t.Fatalf("gateway vector hub ID = %q, want %q", vectors.PushGatewayAuth.HubID, mobileVectorHubID)
	}
}
