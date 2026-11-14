package hub

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"isoform-relay/internal/notify"
	"isoform-relay/internal/push"
	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// Frames sent by the hub. Each struct is decoded strictly; unknown fields are
// rejected. Validation runs before any storage call.
type registerFrame struct {
	T       string `json:"t"`
	Proto   int    `json:"proto"`
	HubName string `json:"hubName"`
}

type heartbeatFrame struct {
	T string `json:"t"`
}

type goodbyeFrame struct {
	T     string `json:"t"`
	Cause string `json:"cause"`
	// Reason is optional: why the computer is going to sleep, so the phone can say it.
	Reason string `json:"reason,omitempty"`
}

type pairingOpenFrame struct {
	T              string `json:"t"`
	Pairing        string `json:"pairing"`
	ClaimTokenHash string `json:"claimTokenHash"`
	ExpiresAt      string `json:"expiresAt"`
}

type pairingRefFrame struct {
	T       string `json:"t"`
	Pairing string `json:"pairing"`
}

type pairingApproveFrame struct {
	T          string   `json:"t"`
	Pairing    string   `json:"pairing"`
	DeviceID   string   `json:"deviceId"`
	BearerHash string   `json:"bearerHash"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  string   `json:"expiresAt"`
	CT         string   `json:"ct"`
}

// outFrame carries a hub envelope for one device. Notify asks for a push, which
// only an event may carry. Release 2 accepts the needs_you push kind alone.
type outFrame struct {
	T      string          `json:"t"`
	Env    json.RawMessage `json:"env"`
	Notify bool            `json:"notify"`
	Push   string          `json:"push"`
	// The fields below describe a moment notice and are valid only with
	// push "moment" (PROTOCOL.md section 14).
	NoticeID string `json:"noticeId"`
	Kind     string `json:"kind"`
	CT       string `json:"ct"`
	Category string `json:"category"`
	Collapse string `json:"collapse"`
	Level    string `json:"level"`
}

// hasNotice reports whether any moment field is set.
func (f outFrame) hasNotice() bool {
	return f.NoticeID != "" || f.Kind != "" || f.CT != "" || f.Category != "" || f.Collapse != "" || f.Level != ""
}

type briefFrame struct {
	T      string          `json:"t"`
	Env    json.RawMessage `json:"env"`
	AsOf   string          `json:"asOf"`
	Notify bool            `json:"notify"`
	// DeliverAt, optional, is when a brief made ahead of its slot becomes
	// visible to the phone and its push is due (RFC 3339, UTC).
	DeliverAt string `json:"deliverAt,omitempty"`
}

// maxBriefDelay bounds how far ahead a brief may be held, so a held brief always
// comes due within a day of its frame.
const maxBriefDelay = 24 * time.Hour

// grantFrame registers a phone the hub paired through another relay, so the
// phone can reach the hub here as well.
type grantFrame struct {
	T          string   `json:"t"`
	DeviceID   string   `json:"deviceId"`
	BearerHash string   `json:"bearerHash"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  string   `json:"expiresAt"`
}

type revokeFrame struct {
	T        string `json:"t"`
	DeviceID string `json:"deviceId"`
}

// smsAckFrame acknowledges one text reply; the relay deletes it.
type smsAckFrame struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

type ackFrame struct {
	T        string  `json:"t"`
	DeviceID string  `json:"dev"`
	UpTo     *uint64 `json:"upTo"`
}

// Frames sent by the relay.
type deviceView struct {
	DeviceID   string  `json:"deviceId"`
	LastSeenAt *string `json:"lastSeenAt"`
}

type registeredFrameOut struct {
	T          string       `json:"t"`
	ServerTime string       `json:"serverTime"`
	Devices    []deviceView `json:"devices"`
}

type claimFrameOut struct {
	T         string `json:"t"`
	Pairing   string `json:"pairing"`
	DeviceID  string `json:"deviceId"`
	DevicePub string `json:"devicePub"`
	CT        string `json:"ct"`
}

type envelopeFrameOut struct {
	T   string          `json:"t"`
	Env json.RawMessage `json:"env"`
}

// receiptFrameOut hands the hub one device's report about a notice. The relay
// authenticated the device and stores the report; it is not sealed, because the
// relay must read state to store it (PROTOCOL.md section 14.5).
type receiptFrameOut struct {
	T        string `json:"t"`
	DeviceID string `json:"dev"`
	NoticeID string `json:"noticeId"`
	State    string `json:"state"`
	Action   string `json:"action"`
	At       string `json:"at"`
}

// smsReplyFrameOut hands the hub one text the user sent to the Isoform number
// (PROTOCOL.md section 18.5). It is not sealed: the text reached the relay in
// the clear from the phone network, so sealing it here would protect nothing.
type smsReplyFrameOut struct {
	T    string `json:"t"`
	ID   string `json:"id"`
	Body string `json:"body"`
	At   string `json:"at"`
}

// smsStateFrameOut tells the hub its text subscription changed.
type smsStateFrameOut struct {
	T     string `json:"t"`
	State string `json:"state"`
}

type unpairedFrameOut struct {
	T        string `json:"t"`
	DeviceID string `json:"deviceId"`
}

type errorFrameOut struct {
	T     string `json:"t"`
	Ref   string `json:"ref"`
	Error string `json:"error"`
}

// reject reports a refused frame to the hub. The connection stays open.
func (s *session) reject(ref, message string) {
	s.enqueue(marshal(errorFrameOut{T: "error", Ref: ref, Error: message}))
}

// dispatch routes one hub frame. A refused frame gets an error frame and the
// connection continues.
func (s *session) dispatch(data []byte) {
	var head struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		s.reject("unknown", "frame is not a JSON object")
		return
	}
	ref := refOf(head.T)
	if len(data) > wire.MaxFrameBytes && head.T != "brief" {
		s.reject(ref, "frame exceeds 256 KiB")
		return
	}
	if message := s.apply(head.T, data); message != "" {
		s.reject(ref, message)
	}
}

// apply handles one decoded frame and returns a static refusal message, or
// the empty string on success.
func (s *session) apply(t string, data []byte) string {
	switch t {
	case "heartbeat":
		return s.heartbeat(data)
	case "goodbye":
		return s.goodbye(data)
	case "pairing_open":
		return s.pairingOpen(data)
	case "pairing_close":
		return s.pairingClose(data)
	case "pairing_approve":
		return s.pairingApprove(data)
	case "pairing_decline":
		return s.pairingDecline(data)
	case "out":
		return s.outbound(data)
	case "brief":
		return s.brief(data)
	case "revoke":
		return s.revoke(data)
	case "grant":
		return s.grant(data)
	case "ack":
		return s.ack(data)
	case "sms_ack":
		return s.smsAck(data)
	case "register":
		return "register is only valid as the first frame"
	default:
		return "unknown frame type"
	}
}

func refOf(t string) string {
	switch t {
	case "register", "heartbeat", "goodbye", "pairing_open", "pairing_close", "pairing_approve", "pairing_decline", "out", "brief", "revoke", "grant", "ack", "sms_ack":
		return t
	default:
		return "unknown"
	}
}

func (s *session) goodbye(data []byte) string {
	var frame goodbyeFrame
	if !decodeFrame(data, &frame) {
		return "invalid goodbye frame"
	}
	switch frame.Cause {
	case "sleeping", "shutting_down", "closing":
	default:
		return "invalid goodbye cause"
	}
	switch frame.Reason {
	case "", "battery_floor", "low_power", "lid_closed", "user_sleep", "unknown":
	default:
		return "invalid goodbye reason"
	}
	if err := s.m.goodbye(s, frame.Cause, frame.Reason); err != nil {
		return "goodbye could not be recorded"
	}
	return ""
}

func (s *session) heartbeat(data []byte) string {
	var f heartbeatFrame
	if !decodeFrame(data, &f) {
		return "invalid heartbeat frame"
	}
	ctx, cancel := storeContext()
	defer cancel()
	if err := s.m.Store.TouchHub(ctx, s.hubID, s.m.Now()); err != nil {
		return "heartbeat could not be recorded"
	}
	return ""
}

func (s *session) pairingOpen(data []byte) string {
	var f pairingOpenFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.Pairing) || !wire.ValidDigest(f.ClaimTokenHash) {
		return "invalid pairing_open frame"
	}
	expires, err := wire.ParseTime(f.ExpiresAt)
	if err != nil {
		return "invalid pairing_open frame"
	}
	now := s.m.Now()
	if !expires.After(now) || expires.After(now.Add(maxInvitation)) {
		return "pairing_open expiry is outside the allowed window"
	}
	ctx, cancel := storeContext()
	defer cancel()
	if err = s.m.Store.OpenPairing(ctx, s.hubID, f.Pairing, f.ClaimTokenHash, expires, now); err != nil {
		return "pairing could not be opened"
	}
	return ""
}

func (s *session) pairingClose(data []byte) string {
	var f pairingRefFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.Pairing) {
		return "invalid pairing_close frame"
	}
	ctx, cancel := storeContext()
	defer cancel()
	if err := s.m.Store.ClosePairing(ctx, s.hubID, f.Pairing); err != nil {
		return "pairing could not be closed"
	}
	return ""
}

func (s *session) pairingApprove(data []byte) string {
	var f pairingApproveFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.Pairing) || !wire.ValidUUID(f.DeviceID) ||
		!wire.ValidDigest(f.BearerHash) || !wire.ValidScopes(f.Scopes) || !wire.ValidCiphertext(f.CT, wire.MaxPairingPlaintext) {
		return "invalid pairing_approve frame"
	}
	expires, err := wire.ParseTime(f.ExpiresAt)
	if err != nil {
		return "invalid pairing_approve frame"
	}
	now := s.m.Now()
	if !expires.After(now) || expires.After(now.Add(maxDeviceLife)) {
		return "pairing_approve expiry is outside the allowed window"
	}
	approval := store.Approval{PairingID: f.Pairing, DeviceID: f.DeviceID, BearerHash: f.BearerHash, Scopes: f.Scopes, ExpiresAt: expires, CT: f.CT}
	ctx, cancel := storeContext()
	defer cancel()
	switch err = s.m.Store.ApprovePairing(ctx, s.hubID, approval, now); {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return "unknown pairing"
	case errors.Is(err, store.ErrExpired):
		return "pairing has expired"
	case errors.Is(err, store.ErrConflict):
		return "pairing cannot be approved in its current state"
	default:
		return "pairing could not be approved"
	}
	s.m.Notify.Notify(notify.PairingKey(f.Pairing))
	return ""
}

func (s *session) pairingDecline(data []byte) string {
	var f pairingRefFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.Pairing) {
		return "invalid pairing_decline frame"
	}
	ctx, cancel := storeContext()
	defer cancel()
	switch err := s.m.Store.DeclinePairing(ctx, s.hubID, f.Pairing); {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return "unknown pairing"
	case errors.Is(err, store.ErrConflict):
		return "pairing is not awaiting approval"
	default:
		return "pairing could not be declined"
	}
	s.m.Notify.Notify(notify.PairingKey(f.Pairing))
	return ""
}

func (s *session) outbound(data []byte) string {
	var f outFrame
	if !decodeFrame(data, &f) || len(f.Env) == 0 {
		return "invalid out frame"
	}
	env, err := wire.ParseEnvelope(f.Env)
	if err != nil || env.Hub != s.hubID || (env.Kind != "res" && env.Kind != "event") {
		return "invalid out envelope"
	}
	pushKind := ""
	var notice *push.Notice
	if f.Notify {
		if env.Kind != "event" {
			return "notify requires an event envelope"
		}
		switch f.Push {
		case push.KindNeedsYou:
			if f.hasNotice() {
				return "notice fields require a moment push"
			}
			pushKind = push.KindNeedsYou
		case push.KindMoment:
			prepared, err := push.Notice{ID: f.NoticeID, Moment: f.Kind, CT: f.CT, Category: f.Category, Collapse: f.Collapse, Level: f.Level}.Prepare()
			if err != nil {
				return "invalid notice"
			}
			notice = &prepared
		default:
			return "unknown push kind"
		}
	} else if f.Push != "" || f.hasNotice() {
		return "push kind requires notify"
	}
	body, err := env.Canonical()
	if err != nil {
		return "invalid out envelope"
	}
	ctx, cancel := storeContext()
	defer cancel()
	if notice != nil {
		err = s.m.Store.AddNoticeResponse(ctx, s.hubID, env, body, *notice, s.m.Now())
	} else {
		err = s.m.Store.AddResponseWithPush(ctx, s.hubID, env, body, pushKind, s.m.Now())
	}
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return "unknown device"
	case errors.Is(err, store.ErrConflict):
		return "sequence conflicts with a stored envelope"
	default:
		return "envelope could not be stored"
	}
	s.m.Notify.Notify(notify.DeviceKey(env.Dev))
	return ""
}

func (s *session) brief(data []byte) string {
	var f briefFrame
	if !decodeFrame(data, &f) || len(f.Env) == 0 {
		return "invalid brief frame"
	}
	asOf, err := wire.ParseTime(f.AsOf)
	if err != nil {
		return "invalid brief frame"
	}
	env, err := wire.ParseEnvelope(f.Env)
	if err != nil || env.Hub != s.hubID || env.Kind != "brief" {
		return "invalid brief envelope"
	}
	body, err := env.Canonical()
	if err != nil {
		return "invalid brief envelope"
	}
	ctx, cancel := storeContext()
	defer cancel()
	now := s.m.Now()
	var deliverAt time.Time
	if f.DeliverAt != "" {
		deliverAt, err = wire.ParseTime(f.DeliverAt)
		if err != nil || deliverAt.After(now.Add(maxBriefDelay)) {
			return "invalid brief frame"
		}
	}
	if err = s.m.Store.PutBriefDueAt(ctx, s.hubID, env, body, wire.FormatTime(asOf), f.Notify, now, deliverAt); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "unknown device"
		}
		return "brief could not be stored"
	}
	return ""
}

func (s *session) grant(data []byte) string {
	var f grantFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.DeviceID) || !wire.ValidDigest(f.BearerHash) || !wire.ValidScopes(f.Scopes) {
		return "invalid grant frame"
	}
	expires, err := wire.ParseTime(f.ExpiresAt)
	if err != nil {
		return "invalid grant frame"
	}
	now := s.m.Now()
	if !expires.After(now) || expires.After(now.Add(maxDeviceLife)) {
		return "grant expiry is outside the allowed window"
	}
	ctx, cancel := storeContext()
	defer cancel()
	device := store.Device{ID: f.DeviceID, BearerHash: f.BearerHash, Scopes: f.Scopes, ExpiresAt: expires}
	switch err = s.m.Store.GrantDevice(ctx, s.hubID, device, now); {
	case err == nil:
	case errors.Is(err, store.ErrConflict):
		return "device cannot be granted"
	default:
		return "device could not be granted"
	}
	// A bearer replaced here ends any wait the old one held.
	s.m.Notify.Notify(notify.DeviceKey(f.DeviceID))
	return ""
}

func (s *session) revoke(data []byte) string {
	var f revokeFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.DeviceID) {
		return "invalid revoke frame"
	}
	ctx, cancel := storeContext()
	defer cancel()
	removed, err := s.m.Store.RevokeDevice(ctx, s.hubID, f.DeviceID)
	if err != nil {
		return "device could not be revoked"
	}
	if removed {
		s.m.Notify.Notify(notify.DeviceKey(f.DeviceID))
	}
	return ""
}

// ack deletes the device's requests the hub has consumed. An unknown device is
// not an error: it may have removed itself while the frame was in flight.
func (s *session) ack(data []byte) string {
	var f ackFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.DeviceID) || f.UpTo == nil || *f.UpTo > wire.MaxSeq {
		return "invalid ack frame"
	}
	ctx, cancel := storeContext()
	defer cancel()
	if err := s.m.Store.AckRequests(ctx, s.hubID, f.DeviceID, *f.UpTo); err != nil && !errors.Is(err, store.ErrNotFound) {
		return "acknowledgement could not be stored"
	}
	return ""
}

// smsAck deletes a text reply the hub has taken. An id that is not queued for
// this hub is not an error: it may have expired or been acked already.
func (s *session) smsAck(data []byte) string {
	var f smsAckFrame
	if !decodeFrame(data, &f) || !wire.ValidUUID(f.ID) {
		return "invalid sms_ack frame"
	}
	if s.m.SMS == nil {
		return "text messages are not configured"
	}
	ctx, cancel := storeContext()
	defer cancel()
	if err := s.m.SMS.Ack(ctx, s.hubID, f.ID); err != nil {
		return "acknowledgement could not be stored"
	}
	s.mu.Lock()
	delete(s.texts, f.ID)
	s.mu.Unlock()
	return ""
}

func storeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), storeTimeout)
}
