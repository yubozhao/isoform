package main

import (
	"bytes"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// Moment notices (PROTOCOL.md section 14). A notice is sealed for one device
// with a key derived from that device's session secret, so the relay and APNs
// see only ciphertext and a fixed fallback line. The hub sends each notice twice
// through the relay: as an h2d event envelope for the device's poll, and as the
// out frame's sealed ct for the push.

const (
	// mobileEventNotice is the plaintext event for a notice envelope.
	mobileEventNotice = "notice"
	// mobilePushMoment is the out frame's push kind for a sealed notice.
	mobilePushMoment = "moment"

	mobileNoticeVersion     = 1
	mobileNoticeKeyInfo     = "notice"
	mobileNoticeNonceSize   = chacha20poly1305.NonceSize
	mobileNoticePadBlock    = 64
	mobileNoticeMaxWire     = 3500
	mobileNoticeMaxTitle    = 120
	mobileNoticeMaxBody     = 300
	mobileNoticeMaxSubject  = 128
	mobileNoticeMaxActions  = 4
	mobileNoticeMaxLabel    = 40
	mobileNoticeMaxCollapse = 64
)

var (
	errMobileNoticeTooLarge    = errors.New("the notice is too large to push")
	errMobileNoticeRejected    = errors.New("the notice could not be authenticated")
	errMobileNoticeInvalid     = errors.New("the notice is not valid")
	errMobileNoticeKey         = errors.New("the notice key could not be derived")
	errMobileNoticeNoRecipient = errors.New("no paired device can receive notices")
)

var (
	mobileNoticeKindPattern     = regexp.MustCompile(`^[a-z][a-z_]{0,39}$`)
	mobileNoticeActionPattern   = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
	mobileNoticeCollapsePattern = regexp.MustCompile(`^[!-~]{1,64}$`)
)

// mobileNoticeReceiptHook receives each receipt the relay forwards (section 14.6).
// The Engagement service sets it once at startup. It must not block, and it must
// tolerate a repeated receipt, because the relay may replay one after a reconnect.
var mobileNoticeReceiptHook func(deviceID string, noticeID, state, action string, at time.Time)

// mobileNotice is one moment to tell the user about. Level and Collapse travel
// in the out frame, not in the sealed text.
type mobileNotice struct {
	ID              string
	Kind            string
	Title           string
	Body            string
	Category        string
	SubjectID       string
	MomentID        string
	DestinationKind string
	DestinationID   string
	// WorkItemID names the Your work item a notice's answer actions land on
	// (PROTOCOL.md 9.2).
	WorkItemID string
	Collapse   string
	Level      string
	Actions    []mobileNoticeAction
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// mobileNoticeAction is one button the notification offers. Auth asks the phone
// for the owner's authentication before the action runs.
type mobileNoticeAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Auth  bool   `json:"auth"`
}

// mobileNoticePlaintext is the sealed JSON. The field order is part of the
// protocol, so the vectors fix it.
type mobileNoticePlaintext struct {
	V               int                  `json:"v"`
	ID              string               `json:"id"`
	Kind            string               `json:"kind"`
	Title           string               `json:"title"`
	Body            string               `json:"body"`
	Category        string               `json:"category"`
	Actions         []mobileNoticeAction `json:"actions"`
	SubjectID       string               `json:"subjectId"`
	MomentID        string               `json:"momentId,omitempty"`
	DestinationKind string               `json:"destinationKind,omitempty"`
	DestinationID   string               `json:"destinationId,omitempty"`
	WorkItemID      string               `json:"workItemId,omitempty"`
	CreatedAt       string               `json:"createdAt"`
	ExpiresAt       string               `json:"expiresAt"`
}

// mobileNoticeEvent is the plaintext of the h2d event envelope that accompanies
// a notice. The phone reads it on poll; the sealed text travels in the push.
type mobileNoticeEvent struct {
	Event    string `json:"event"`
	NoticeID string `json:"noticeId"`
	Kind     string `json:"kind"`
	At       string `json:"at"`
}

// mobileNoticeFrame is the out frame that publishes one notice to the relay.
type mobileNoticeFrame struct {
	T        string         `json:"t"`
	Env      mobileEnvelope `json:"env"`
	Notify   bool           `json:"notify"`
	Push     string         `json:"push"`
	NoticeID string         `json:"noticeId"`
	Kind     string         `json:"kind"`
	CT       string         `json:"ct"`
	Category string         `json:"category"`
	Collapse string         `json:"collapse"`
	Level    string         `json:"level"`
}

// mobileNoticeKey derives the notice key from the X25519 session secret (the
// value the envelope keys derive from). The salt is the envelope salt, so no new
// state is needed. HKDF with a 32-byte output and SHA-256 cannot fail, so a nil
// result is reported to callers as errMobileNoticeKey.
func mobileNoticeKey(hubID, deviceID string, secret []byte) []byte {
	key, err := hkdf.Key(sha256.New, secret, []byte("yansu-mobile-v1|"+hubID+"|"+deviceID), mobileNoticeKeyInfo, chacha20poly1305.KeySize)
	if err != nil {
		return nil
	}
	return key
}

// noticeKeyLocked derives one paired device's notice key. The caller holds
// bridge.mu.
func (bridge *mobileBridge) noticeKeyLocked(record *mobileDeviceRecord) ([]byte, error) {
	if bridge.identity == nil {
		return nil, errMobileRelayOffline
	}
	devicePub, err := mobileParsePublicKey(record.DevicePub)
	if err != nil {
		return nil, err
	}
	secret, err := bridge.identity.Private.ECDH(devicePub)
	if err != nil {
		return nil, err
	}
	key := mobileNoticeKey(bridge.identity.HubID, record.DeviceID, secret)
	if key == nil {
		return nil, errMobileNoticeKey
	}
	return key, nil
}

func mobileNoticeAAD(hubID, deviceID, noticeID string) []byte {
	return []byte("yansu-notice-v1|" + hubID + "|" + deviceID + "|" + noticeID)
}

// validateSealed checks the fields that the sealed text carries. The sealer
// calls it before sealing and the opener calls it after opening.
func (n mobileNotice) validateSealed() error {
	if !mobileValidUUID(n.ID) || !mobileNoticeKindPattern.MatchString(n.Kind) {
		return errMobileNoticeInvalid
	}
	if n.Title == "" || len(n.Title) > mobileNoticeMaxTitle || len(n.Body) > mobileNoticeMaxBody {
		return errMobileNoticeInvalid
	}
	if !mobileNoticeCategoryValid(n.Category) || len(n.SubjectID) > mobileNoticeMaxSubject {
		return errMobileNoticeInvalid
	}
	if n.DestinationKind != "" || n.DestinationID != "" {
		switch n.DestinationKind {
		case "brief", "task", "chat", mobileNoticeKindAgentJoin:
		default:
			return errMobileNoticeInvalid
		}
		if !mobileNoticeDestinationIDValid(n.DestinationID) {
			return errMobileNoticeInvalid
		}
	}
	if len(n.Actions) > mobileNoticeMaxActions || n.CreatedAt.IsZero() {
		return errMobileNoticeInvalid
	}
	if !n.ExpiresAt.IsZero() && !n.ExpiresAt.After(n.CreatedAt) {
		return errMobileNoticeInvalid
	}
	for _, action := range n.Actions {
		if !mobileNoticeActionPattern.MatchString(action.ID) || action.Label == "" || len(action.Label) > mobileNoticeMaxLabel {
			return errMobileNoticeInvalid
		}
	}
	return nil
}

func mobileNoticeDestinationIDValid(id string) bool {
	return mobileValidID(id) && !strings.ContainsAny(id, "?#%")
}

// validate checks a notice for publishing: the sealed fields and the out frame
// fields that the relay enforces.
func (n mobileNotice) validate() error {
	if err := n.validateSealed(); err != nil {
		return err
	}
	if !mobileNoticeLevelValid(n.Level) {
		return errMobileNoticeInvalid
	}
	if n.Collapse != "" && !mobileNoticeCollapsePattern.MatchString(n.Collapse) {
		return errMobileNoticeInvalid
	}
	return nil
}

func mobileNoticeCategoryValid(category string) bool {
	switch category {
	case "YANSU_DECISION", "YANSU_MOMENT", "YANSU_BRIEF":
		return true
	}
	return false
}

func mobileNoticeLevelValid(level string) bool {
	switch level {
	case "passive", "active", "time-sensitive":
		return true
	}
	return false
}

// mobileNoticeEncode returns the plaintext JSON, padded with spaces to a multiple
// of mobileNoticePadBlock. JSON permits trailing whitespace, so the padding
// decodes as-is and openMobileNotice removes it.
func mobileNoticeEncode(n mobileNotice) ([]byte, error) {
	actions := make([]mobileNoticeAction, 0, len(n.Actions))
	actions = append(actions, n.Actions...)
	raw, err := json.Marshal(mobileNoticePlaintext{
		V: mobileNoticeVersion, ID: n.ID, Kind: n.Kind, Title: n.Title, Body: n.Body, Category: n.Category,
		Actions: actions, SubjectID: n.SubjectID, MomentID: n.MomentID, DestinationKind: n.DestinationKind, DestinationID: n.DestinationID,
		WorkItemID: n.WorkItemID, CreatedAt: mobileTimestamp(n.CreatedAt), ExpiresAt: mobileOptionalTimestamp(n.ExpiresAt),
	})
	if err != nil {
		return nil, err
	}
	padded := (len(raw) + mobileNoticePadBlock - 1) / mobileNoticePadBlock * mobileNoticePadBlock
	return append(raw, bytes.Repeat([]byte{' '}, padded-len(raw))...), nil
}

func mobileOptionalTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return mobileTimestamp(value)
}

// sealMobileNotice seals a notice with a fresh random nonce.
func sealMobileNotice(key []byte, hubID, deviceID string, n mobileNotice) (string, error) {
	nonce := make([]byte, mobileNoticeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sealMobileNoticeWithNonce(key, hubID, deviceID, n, nonce)
}

// sealMobileNoticeWithNonce seals a notice with the given nonce. Production
// uses sealMobileNotice; the protocol vectors use this with a fixed nonce.
func sealMobileNoticeWithNonce(key []byte, hubID, deviceID string, n mobileNotice, nonce []byte) (string, error) {
	if err := n.validateSealed(); err != nil {
		return "", err
	}
	if len(key) != chacha20poly1305.KeySize || len(nonce) != mobileNoticeNonceSize {
		return "", errMobileNoticeKey
	}
	plaintext, err := mobileNoticeEncode(n)
	if err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plaintext, mobileNoticeAAD(hubID, deviceID, n.ID))
	wire := make([]byte, 0, len(nonce)+len(sealed))
	wire = append(wire, nonce...)
	wire = append(wire, sealed...)
	ct := base64.RawURLEncoding.EncodeToString(wire)
	if len(ct) > mobileNoticeMaxWire {
		return "", errMobileNoticeTooLarge
	}
	return ct, nil
}

// openMobileNoticePlaintext authenticates and decrypts a notice and returns the
// padded plaintext exactly as it was sealed.
func openMobileNoticePlaintext(key []byte, hubID, deviceID, noticeID, ct string) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, errMobileNoticeKey
	}
	if len(ct) > mobileNoticeMaxWire {
		return nil, errMobileNoticeRejected
	}
	wire, err := base64.RawURLEncoding.DecodeString(ct)
	if err != nil || len(wire) <= mobileNoticeNonceSize+chacha20poly1305.Overhead {
		return nil, errMobileNoticeRejected
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, wire[:mobileNoticeNonceSize], wire[mobileNoticeNonceSize:], mobileNoticeAAD(hubID, deviceID, noticeID))
	if err != nil {
		return nil, errMobileNoticeRejected
	}
	if len(plaintext) == 0 || len(plaintext)%mobileNoticePadBlock != 0 {
		return nil, errMobileNoticeRejected
	}
	return plaintext, nil
}

// openMobileNotice opens and decodes a notice. The hub never needs to open a
// notice it sent; this keeps the vectors and the phone's decoder symmetric.
func openMobileNotice(key []byte, hubID, deviceID, noticeID, ct string) (mobileNotice, error) {
	padded, err := openMobileNoticePlaintext(key, hubID, deviceID, noticeID, ct)
	if err != nil {
		return mobileNotice{}, err
	}
	var decoded mobileNoticePlaintext
	if err = mobileDecodeStrict(bytes.TrimRight(padded, " "), &decoded); err != nil {
		return mobileNotice{}, errMobileNoticeInvalid
	}
	if decoded.V != mobileNoticeVersion || decoded.ID != noticeID {
		return mobileNotice{}, errMobileNoticeInvalid
	}
	createdAt, ok := mobileParseTimestamp(decoded.CreatedAt)
	if !ok {
		return mobileNotice{}, errMobileNoticeInvalid
	}
	var expiresAt time.Time
	if decoded.ExpiresAt != "" {
		if expiresAt, ok = mobileParseTimestamp(decoded.ExpiresAt); !ok {
			return mobileNotice{}, errMobileNoticeInvalid
		}
	}
	n := mobileNotice{
		ID: decoded.ID, Kind: decoded.Kind, Title: decoded.Title, Body: decoded.Body, Category: decoded.Category,
		SubjectID: decoded.SubjectID, MomentID: decoded.MomentID, DestinationKind: decoded.DestinationKind, DestinationID: decoded.DestinationID, Actions: decoded.Actions,
		CreatedAt: createdAt, ExpiresAt: expiresAt,
	}
	if err = n.validateSealed(); err != nil {
		return mobileNotice{}, err
	}
	return n, nil
}

// sealNoticeLocked builds one device's event envelope and out frame. It advances
// the device's send counter only when both seals succeed. The caller holds
// bridge.mu.
func (bridge *mobileBridge) sealNoticeLocked(record *mobileDeviceRecord, n mobileNotice, event []byte) (mobileNoticeFrame, error) {
	keys, err := bridge.sessionKeysLocked(record)
	if err != nil {
		return mobileNoticeFrame{}, err
	}
	noticeKey, err := bridge.noticeKeyLocked(record)
	if err != nil {
		return mobileNoticeFrame{}, err
	}
	hubID := bridge.identity.HubID
	ct, err := sealMobileNotice(noticeKey, hubID, record.DeviceID, n)
	if err != nil {
		return mobileNoticeFrame{}, err
	}
	record.SendSeq++
	env, err := sealMobileEnvelope(keys.H2D, hubID, record.DeviceID, mobileDirH2D, record.SendSeq, mobileKindEvent, event)
	if err != nil {
		record.SendSeq--
		return mobileNoticeFrame{}, err
	}
	collapse := n.Collapse
	if collapse == "" {
		collapse = n.ID
	}
	return mobileNoticeFrame{
		T: "out", Env: env, Notify: true, Push: mobilePushMoment, NoticeID: n.ID, Kind: n.Kind,
		CT: ct, Category: n.Category, Collapse: collapse, Level: n.Level,
	}, nil
}

// publishMobileNotice seals a notice for each listed device that holds the push
// scope and sends one out frame per device (PROTOCOL.md section 14.3). Devices
// that are no longer paired are skipped. It reports errMobileNoticeNoRecipient
// when no device qualifies, so a caller can fall back to another channel.
func (bridge *mobileBridge) publishMobileNotice(n mobileNotice, devices []mobileDeviceRecord) error {
	if err := n.validate(); err != nil {
		return err
	}
	event, err := json.Marshal(mobileNoticeEvent{Event: mobileEventNotice, NoticeID: n.ID, Kind: n.Kind, At: mobileTimestamp(n.CreatedAt)})
	if err != nil {
		return err
	}
	bridge.sendOrder.Lock()
	defer bridge.sendOrder.Unlock()
	bridge.mu.Lock()
	if bridge.stopped || bridge.identity == nil || bridge.client == nil {
		bridge.mu.Unlock()
		return errMobileRelayOffline
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, device := range devices {
		record := bridge.devices[device.DeviceID]
		if record == nil || seen[device.DeviceID] || !mobileHasScope(record.Scopes, mobileScopePush) {
			continue
		}
		seen[device.DeviceID] = true
		ids = append(ids, device.DeviceID)
	}
	if len(ids) == 0 {
		bridge.mu.Unlock()
		return errMobileNoticeNoRecipient
	}
	sort.Strings(ids)
	var errs []error
	frames := make([]mobileNoticeFrame, 0, len(ids))
	for _, id := range ids {
		frame, err := bridge.sealNoticeLocked(bridge.devices[id], n, event)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		frames = append(frames, frame)
	}
	// Counters are saved before any frame that uses them is sent.
	if err := bridge.persistDevicesLocked(); err != nil {
		bridge.mu.Unlock()
		return errors.Join(append(errs, err)...)
	}
	client := bridge.client
	bridge.mu.Unlock()
	for _, frame := range frames {
		if err := client.send(frame); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// handleNoticeReceipt takes a receipt the relay forwarded (PROTOCOL.md section
// 14.6). The relay authenticated the device; the hub checks the shape and that
// the device is still paired, then calls the hook outside the lock.
func (bridge *mobileBridge) handleNoticeReceipt(raw []byte) {
	var frame struct {
		T        string `json:"t"`
		DeviceID string `json:"dev"`
		NoticeID string `json:"noticeId"`
		State    string `json:"state"`
		Action   string `json:"action"`
		At       string `json:"at"`
	}
	if err := mobileDecodeStrict(raw, &frame); err != nil {
		log.Printf("[mobile-notice] ignored a malformed receipt")
		return
	}
	at, ok := mobileParseTimestamp(frame.At)
	if !ok || !mobileValidUUID(frame.DeviceID) || !mobileValidUUID(frame.NoticeID) || !mobileNoticeReceiptValid(frame.State, frame.Action) {
		log.Printf("[mobile-notice] ignored a receipt that is not valid")
		return
	}
	bridge.mu.Lock()
	paired := bridge.devices[frame.DeviceID] != nil
	bridge.mu.Unlock()
	if !paired {
		log.Printf("[mobile-notice] ignored a receipt from a device that is not paired")
		return
	}
	if hook := mobileNoticeReceiptHook; hook != nil {
		hook(frame.DeviceID, frame.NoticeID, frame.State, frame.Action, at)
	}
}

// mobileNoticeReceiptValid accepts the four receipt states. An acted receipt
// names its action; the other states carry an action only when there is one.
func mobileNoticeReceiptValid(state, action string) bool {
	switch state {
	case "delivered", "opened", "dismissed":
	case "acted":
		if action == "" {
			return false
		}
	default:
		return false
	}
	return action == "" || mobileNoticeActionPattern.MatchString(action)
}
