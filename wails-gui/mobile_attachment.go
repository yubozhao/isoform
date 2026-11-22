package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
)

// Attachments (PROTOCOL.md section 17). A file the phone should have, today
// the spoken brief, is sealed once with a random per-file key and uploaded to
// the relay with the hub credential. The key travels inside the brief
// document, which is already sealed end to end for each device, so the relay
// holds ciphertext only. This mirrors the notice sealing in mobile_notice.go:
// ChaCha20-Poly1305 with the id bound as associated data.

const (
	mobileAttachmentNonceSize   = chacha20poly1305.NonceSize
	mobileAttachmentMaxBytes    = 16 << 20
	mobileAttachmentUploadWait  = 90 * time.Second
	mobileAttachmentContentType = "application/octet-stream"
	// mobileAttachmentTextType is the same sealed bytes as base64 text (PROTOCOL.md 17.2).
	mobileAttachmentTextType = "text/plain"
)

var (
	errMobileAttachmentTooLarge = errors.New("the attachment is too large to send")
	errMobileAttachmentKey      = errors.New("the attachment key is not valid")
	errMobileAttachmentRejected = errors.New("the relay did not accept the attachment")
)

func init() {
	mobileBriefAudioSealer = uploadMobileBriefAudio
}

// mobileAttachmentAAD binds a sealed attachment to its hub and id.
func mobileAttachmentAAD(hubID, attachmentID string) []byte {
	return []byte("yansu-attachment-v1|" + hubID + "|" + attachmentID)
}

// sealMobileAttachment seals plaintext under key and nonce for the attachment id.
func sealMobileAttachment(key, nonce []byte, hubID, attachmentID string, plaintext []byte) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize || len(nonce) != mobileAttachmentNonceSize {
		return nil, errMobileAttachmentKey
	}
	if len(plaintext) == 0 || len(plaintext) > mobileAttachmentMaxBytes-chacha20poly1305.Overhead {
		return nil, errMobileAttachmentTooLarge
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plaintext, mobileAttachmentAAD(hubID, attachmentID)), nil
}

// openMobileAttachment authenticates and decrypts a sealed attachment. The hub
// never needs to open one it sent; this keeps the phone's decoder checkable.
func openMobileAttachment(key, nonce []byte, hubID, attachmentID string, sealed []byte) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize || len(nonce) != mobileAttachmentNonceSize {
		return nil, errMobileAttachmentKey
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, sealed, mobileAttachmentAAD(hubID, attachmentID))
	if err != nil {
		return nil, errMobileAttachmentRejected
	}
	return plaintext, nil
}

// newMobileAttachmentSecret draws a fresh key and nonce for one attachment.
func newMobileAttachmentSecret() (key, nonce []byte, err error) {
	key = make([]byte, chacha20poly1305.KeySize)
	nonce = make([]byte, mobileAttachmentNonceSize)
	if _, err = rand.Read(key); err != nil {
		return nil, nil, err
	}
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return key, nonce, nil
}

// mobileAttachmentUploader puts one sealed attachment on the relay. The bridge
// implements it over its relay origin and hub credential; tests use a stub.
type mobileAttachmentUploader interface {
	uploadAttachment(ctx context.Context, attachmentID string, sealed []byte) error
}

// sealAndUploadAttachment seals a file and uploads it, returning the record
// the brief carries. The caller decides whether anyone can receive it.
func sealAndUploadAttachment(ctx context.Context, uploader mobileAttachmentUploader, hubID, path string) (*MobileBriefAudioAttachment, error) {
	plaintext, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return sealAndUploadAttachmentBytes(ctx, uploader, hubID, plaintext)
}

// sealAndUploadAttachmentBytes seals an already verified immutable snapshot.
func sealAndUploadAttachmentBytes(ctx context.Context, uploader mobileAttachmentUploader, hubID string, plaintext []byte) (*MobileBriefAudioAttachment, error) {
	key, nonce, err := newMobileAttachmentSecret()
	if err != nil {
		return nil, err
	}
	attachmentID := uuid.NewString()
	sealed, err := sealMobileAttachment(key, nonce, hubID, attachmentID, plaintext)
	if err != nil {
		return nil, err
	}
	if err := uploader.uploadAttachment(ctx, attachmentID, sealed); err != nil {
		return nil, err
	}
	return &MobileBriefAudioAttachment{
		ID:    attachmentID,
		Key:   base64.RawURLEncoding.EncodeToString(key),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	}, nil
}

// uploadMobileBriefAudio is the brief service's sealer: it sends the spoken
// brief when a paired phone can read briefs and the relay is reachable, and
// reports errMobileAttachmentOffline otherwise so the audio stays on the Mac.
func uploadMobileBriefAudio(ctx context.Context, a *App, _ string, path string) (*MobileBriefAudioAttachment, error) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return nil, errMobileAttachmentOffline
	}
	bridge.mu.Lock()
	client, identity := bridge.client, bridge.identity
	readers := 0
	for _, record := range bridge.devices {
		if record != nil && mobileHasScope(record.Scopes, mobileScopeBriefRead) {
			readers++
		}
	}
	bridge.mu.Unlock()
	if client == nil || identity == nil || readers == 0 {
		return nil, errMobileAttachmentOffline
	}
	return sealAndUploadAttachment(ctx, bridge.attachmentUploader(client), identity.HubID, path)
}

// uploadAttachment puts the sealed bytes on the relay with the hub credential
// (PUT /v1/hub/attachments/{id}).
//
//isoform:destination relay
func (c *mobileRelayClient) uploadAttachment(ctx context.Context, attachmentID string, sealed []byte) error {
	if len(sealed) == 0 || len(sealed) > mobileAttachmentMaxBytes {
		return errMobileAttachmentTooLarge
	}
	ctx, cancel := context.WithTimeout(ctx, mobileAttachmentUploadWait)
	defer cancel()
	status, relays, err := c.putAttachment(ctx, attachmentID, sealed, mobileAttachmentContentType)
	if err != nil {
		return err
	}
	if status == http.StatusForbidden && !relays {
		// A 403 the relay did not write: a tunnel in front of it refused the bytes by what
		// they happen to be, and would refuse them every time (2026-11-21: one sealed body
		// in forty). As base64 text they hold nothing such a rule matches. A relay that
		// takes no text answers 415 and the upload fails as it did.
		text := make([]byte, base64.StdEncoding.EncodedLen(len(sealed)))
		base64.StdEncoding.Encode(text, sealed)
		if status, _, err = c.putAttachment(ctx, attachmentID, text, mobileAttachmentTextType); err != nil {
			return err
		}
	}
	if status != http.StatusCreated {
		return fmt.Errorf("%w (status %d)", errMobileAttachmentRejected, status)
	}
	return nil
}

// putAttachment sends one body and says the status and whether the answer is
// the relay's own: every answer of the relay is JSON with "ok".
//
//isoform:destination relay
func (c *mobileRelayClient) putAttachment(ctx context.Context, attachmentID string, body []byte, contentType string) (int, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.hubOrigin()+"/v1/hub/attachments/"+attachmentID, bytes.NewReader(body))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("X-Yansu-Hub", c.hubID)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(body))
	resp, err := mobileAttachmentHTTPClient.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("could not reach the relay: %w", err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var shape struct {
		OK *bool `json:"ok"`
	}
	relays := json.Unmarshal(answer, &shape) == nil && shape.OK != nil
	return resp.StatusCode, relays, nil
}

// mobileAttachmentHTTPClient uploads attachments. It follows the proxy
// settings the relay WebSocket uses and never follows redirects, so the
// credential goes to the configured relay only.
//
//isoform:destination relay
var mobileAttachmentHTTPClient = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
