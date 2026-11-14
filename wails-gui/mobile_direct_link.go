package main

// The direct path beside a pinned relay (PROTOCOL.md section 21.4).
//
// A desktop pinned to an HTTPS relay (relay.json, ISOFORM_RELAY_URL or a build
// stamp) used to serve phones only through that relay. When the relay's public
// address stopped working for a phone, nothing else could reach this computer.
// The desktop now also runs its own relay sidecar behind dumbpipe, exactly as
// remote-access mode does, and keeps a second hub connection there. A paired
// phone asks for direct-path credentials over the pinned relay (POST
// /remote-access): it gets the iroh ticket and a bearer that only the local
// relay knows. The hub then mirrors every frame a phone needs (responses,
// events, briefs, acks, revokes, goodbyes) to the local relay, so the phone can
// use either path and fall back between them.
//
// Pairing still happens on the pinned relay. Pushes are queued only there, so
// a phone never gets the same notification twice.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"wails-gui/internal/remoteaccess"
)

const mobileDirectGrantsFile = "direct-grants.json"

var errMobileDirectPathNotReady = errors.New("the direct path is not ready")

// mobileDirectGrant is one phone's bearer at the local relay. Only the digest
// is kept; the bearer itself travels once, sealed, in the /remote-access reply.
type mobileDirectGrant struct {
	DeviceID   string `json:"deviceId"`
	BearerHash string `json:"bearerHash"`
	ExpiresAt  string `json:"expiresAt"`
}

type mobileDirectGrantFile struct {
	Version int                 `json:"version"`
	Grants  []mobileDirectGrant `json:"grants"`
}

// MobileRemoteAccessGrant is the reply to POST /remote-access.
type MobileRemoteAccessGrant struct {
	Ticket    string `json:"ticket"`
	Bearer    string `json:"bearer"`
	ExpiresAt string `json:"expiresAt"`
}

// loadMobileDirectGrants reads direct-grants.json. A missing or unreadable
// file means no grants: phones ask again, which costs one request.
func loadMobileDirectGrants(dir string) map[string]mobileDirectGrant {
	grants := make(map[string]mobileDirectGrant)
	raw, err := os.ReadFile(filepath.Join(dir, mobileDirectGrantsFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[mobile-direct] could not read the direct-path grants: %v", err)
		}
		return grants
	}
	var file mobileDirectGrantFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 {
		log.Printf("[mobile-direct] ignored an unreadable direct-path grants file")
		return grants
	}
	for _, grant := range file.Grants {
		if mobileValidUUID(grant.DeviceID) && grant.BearerHash != "" {
			grants[grant.DeviceID] = grant
		}
	}
	return grants
}

// persistDirectGrantsLocked writes direct-grants.json. The caller holds directMu.
func (bridge *mobileBridge) persistDirectGrantsLocked() error {
	grants := make([]mobileDirectGrant, 0, len(bridge.directGrants))
	for _, grant := range bridge.directGrants {
		grants = append(grants, grant)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].DeviceID < grants[j].DeviceID })
	if err := ensureMobileStateDirectory(bridge.stateDir); err != nil {
		return err
	}
	return writeMobilePrivateJSON(filepath.Join(bridge.stateDir, mobileDirectGrantsFile),
		mobileDirectGrantFile{Version: 1, Grants: grants})
}

func (bridge *mobileBridge) directGrant(deviceID string) (mobileDirectGrant, bool) {
	bridge.directMu.Lock()
	defer bridge.directMu.Unlock()
	grant, ok := bridge.directGrants[deviceID]
	return grant, ok
}

// dropDirectGrant forgets a removed phone's direct-path bearer. The revoke the
// caller sends is mirrored to the local relay whether or not a grant existed.
func (bridge *mobileBridge) dropDirectGrant(deviceID string) {
	bridge.directMu.Lock()
	defer bridge.directMu.Unlock()
	if _, ok := bridge.directGrants[deviceID]; !ok {
		return
	}
	delete(bridge.directGrants, deviceID)
	if err := bridge.persistDirectGrantsLocked(); err != nil {
		log.Printf("[mobile-direct] could not save the direct-path grants: %v", err)
	}
}

// directPathWantedLocked reports whether the direct path should run beside the
// pinned relay. In remote-access mode the primary connection already is the
// direct path. The caller holds bridge.mu.
func (bridge *mobileBridge) directPathWantedLocked() bool {
	return !bridge.stopped && bridge.relay.Enabled && bridge.identity != nil &&
		!mobileRemoteAccessMode(bridge.relay.URL) && mobileRemoteAccessEnabled()
}

// startDirectLink starts the sidecar and the second hub connection in the
// background. It returns at once and is safe to call repeatedly.
func (bridge *mobileBridge) startDirectLink() {
	bridge.mu.Lock()
	if !bridge.directPathWantedLocked() || bridge.directStarting || bridge.direct.Load() != nil {
		bridge.mu.Unlock()
		return
	}
	if bridge.app == nil || bridge.app.remoteAccessFactory == nil {
		// Without the bundled binaries (a test binary, a build without them)
		// there is no direct path to offer; nothing is retried.
		if _, err := LocateBundledRelay(); err != nil {
			bridge.mu.Unlock()
			log.Printf("[mobile-direct] the direct path is not offered: relay %v", err)
			return
		}
		if _, err := LocateBundledDumbpipe(); err != nil {
			bridge.mu.Unlock()
			log.Printf("[mobile-direct] the direct path is not offered: dumbpipe %v", err)
			return
		}
	}
	bridge.directStarting = true
	bridge.mu.Unlock()
	go bridge.runDirectLink()
}

func (bridge *mobileBridge) runDirectLink() {
	defer func() {
		bridge.mu.Lock()
		bridge.directStarting = false
		bridge.mu.Unlock()
	}()
	for attempt := 1; ; attempt++ {
		bridge.mu.Lock()
		wanted := bridge.directPathWantedLocked()
		bridge.mu.Unlock()
		if !wanted {
			return
		}
		snapshot, err := bridge.ensureRemoteAccess(bridge.ctx)
		if err == nil && snapshot.RelayURL == "" {
			err = errMobileDirectPathNotReady
		}
		if err == nil {
			bridge.mu.Lock()
			if !bridge.directPathWantedLocked() || bridge.direct.Load() != nil {
				bridge.mu.Unlock()
				return
			}
			client, clientErr := newMobileRelayClient(snapshot.RelayURL, *bridge.identity, mobileHubName(), &mobileDirectHandler{bridge: bridge})
			if clientErr == nil {
				client.logPrefix = "[mobile-direct]"
				bridge.direct.Store(client)
				client.start(bridge.ctx)
				bridge.mu.Unlock()
				log.Printf("[mobile-direct] the direct path runs beside the pinned relay")
				bridge.notify()
				return
			}
			bridge.mu.Unlock()
			err = clientErr
		}
		if errors.Is(err, errMobileBridgeStopping) {
			return
		}
		log.Printf("[mobile-direct] the direct path is not available yet: %v", err)
		if !mobileSleep(bridge.ctx, mobileRemoteAccessRetryDelay(attempt)) {
			return
		}
	}
}

// takeDirectLinkLocked detaches the direct connection. The caller holds
// bridge.mu and stops the returned client after unlocking.
func (bridge *mobileBridge) takeDirectLinkLocked() *mobileRelayClient {
	return bridge.direct.Swap(nil)
}

// mirrorToDirect copies one hub frame bound for the pinned relay to the local
// relay. It runs inside mobileRelayClient.send, possibly with bridge.mu held,
// so it takes only directMu.
func (bridge *mobileBridge) mirrorToDirect(raw []byte) {
	direct := bridge.direct.Load()
	if direct == nil {
		return
	}
	frame, ok := bridge.directCopy(raw)
	if !ok {
		return
	}
	if err := direct.send(frame); err != nil && !errors.Is(err, errMobileRelayOffline) {
		log.Printf("[mobile-direct] a frame was not copied to the direct path: %v", err)
	}
}

// directCopy returns the local relay's copy of a frame, or false when the frame
// belongs to the pinned relay alone: pairing, text messages, and anything for a
// phone that has no direct-path bearer. Push fields are dropped.
func (bridge *mobileBridge) directCopy(raw []byte) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false
	}
	var kind string
	if err := json.Unmarshal(fields["t"], &kind); err != nil {
		return nil, false
	}
	keep := func(names ...string) map[string]json.RawMessage {
		out := make(map[string]json.RawMessage, len(names))
		for _, name := range names {
			if value, ok := fields[name]; ok {
				out[name] = value
			}
		}
		return out
	}
	envelopeDevice := func() string {
		var env struct {
			Dev string `json:"dev"`
		}
		_ = json.Unmarshal(fields["env"], &env)
		return env.Dev
	}
	granted := func(deviceID string) bool {
		_, ok := bridge.directGrant(deviceID)
		return ok
	}
	switch kind {
	case "out":
		if !granted(envelopeDevice()) {
			return nil, false
		}
		return keep("t", "env"), true
	case "brief":
		if !granted(envelopeDevice()) {
			return nil, false
		}
		out := keep("t", "env", "asOf", "deliverAt")
		out["notify"] = json.RawMessage("false")
		return out, true
	case "ack":
		var dev string
		_ = json.Unmarshal(fields["dev"], &dev)
		if !granted(dev) {
			return nil, false
		}
		return fields, true
	case "revoke", "goodbye":
		return fields, true
	default:
		return nil, false
	}
}

// serveRemoteAccess answers POST /remote-access: the iroh ticket and a new
// bearer for the local relay. Each call replaces the phone's previous direct
// bearer. A desktop without a direct path answers 503 and the phone keeps
// using the pinned relay.
func (bridge *mobileBridge) serveRemoteAccess(w http.ResponseWriter, device mobileDeviceRecord) {
	grant, ticket, err := bridge.issueDirectGrant(device)
	if err != nil {
		status := http.StatusServiceUnavailable
		if !errors.Is(err, errMobileDirectPathNotReady) {
			status = http.StatusInternalServerError
		}
		mobileJSON(w, status, nil, err.Error())
		return
	}
	mobileJSON(w, http.StatusOK, MobileRemoteAccessGrant{Ticket: ticket, Bearer: grant.bearer, ExpiresAt: grant.ExpiresAt}, "")
}

type mobileIssuedDirectGrant struct {
	mobileDirectGrant
	bearer string
}

func (bridge *mobileBridge) issueDirectGrant(device mobileDeviceRecord) (mobileIssuedDirectGrant, string, error) {
	direct := bridge.direct.Load()
	bridge.mu.Lock()
	supervisor := bridge.remoteAccess
	record := bridge.devices[device.DeviceID]
	var scopes []string
	if record != nil {
		scopes = append([]string{}, record.Scopes...)
	}
	bridge.mu.Unlock()
	if direct == nil || supervisor == nil || record == nil {
		return mobileIssuedDirectGrant{}, "", errMobileDirectPathNotReady
	}
	if state, _ := direct.snapshot(); state != mobileRelayConnected {
		return mobileIssuedDirectGrant{}, "", errMobileDirectPathNotReady
	}
	snapshot := supervisor.Snapshot()
	if snapshot.State != remoteaccess.StateReady || snapshot.Ticket == "" {
		return mobileIssuedDirectGrant{}, "", errMobileDirectPathNotReady
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return mobileIssuedDirectGrant{}, "", err
	}
	bearer := base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(bearer))
	grant := mobileDirectGrant{DeviceID: device.DeviceID, BearerHash: hex.EncodeToString(sum[:]), ExpiresAt: device.ExpiresAt}
	bridge.directMu.Lock()
	previous, had := bridge.directGrants[grant.DeviceID]
	bridge.directGrants[grant.DeviceID] = grant
	if err := bridge.persistDirectGrantsLocked(); err != nil {
		if had {
			bridge.directGrants[grant.DeviceID] = previous
		} else {
			delete(bridge.directGrants, grant.DeviceID)
		}
		bridge.directMu.Unlock()
		return mobileIssuedDirectGrant{}, "", err
	}
	bridge.directMu.Unlock()
	if err := direct.send(mobileDirectGrantFrame(grant, scopes)); err != nil {
		// The grant is saved and is sent again at the next registration.
		return mobileIssuedDirectGrant{}, "", errMobileDirectPathNotReady
	}
	return mobileIssuedDirectGrant{mobileDirectGrant: grant, bearer: bearer}, snapshot.Ticket, nil
}

func mobileDirectGrantFrame(grant mobileDirectGrant, scopes []string) map[string]any {
	return map[string]any{
		"t": "grant", "deviceId": grant.DeviceID, "bearerHash": grant.BearerHash,
		"scopes": scopes, "expiresAt": grant.ExpiresAt,
	}
}

// mobileDirectHandler receives the local relay's frames. Requests join the
// same per-device queue as requests from the pinned relay, so sequence checks
// and replay rules are shared.
type mobileDirectHandler struct {
	bridge *mobileBridge
}

// relayRegistered re-sends every grant (the local relay's state may be new),
// revokes phones this computer no longer serves, and opens delivery.
func (h *mobileDirectHandler) relayRegistered(client *mobileRelayClient, presence []mobileRelayPresence, listed bool) {
	bridge := h.bridge
	bridge.mu.Lock()
	if bridge.stopped || bridge.direct.Load() != client {
		bridge.mu.Unlock()
		return
	}
	now := time.Now()
	var frames []map[string]any
	bridge.directMu.Lock()
	changed := false
	for id, grant := range bridge.directGrants {
		record := bridge.devices[id]
		expires, ok := mobileParseTimestamp(grant.ExpiresAt)
		if record == nil || !ok || !now.Before(expires) {
			delete(bridge.directGrants, id)
			changed = true
			continue
		}
		frames = append(frames, mobileDirectGrantFrame(grant, append([]string{}, record.Scopes...)))
	}
	if changed {
		if err := bridge.persistDirectGrantsLocked(); err != nil {
			log.Printf("[mobile-direct] could not save the direct-path grants: %v", err)
		}
	}
	if listed {
		for _, entry := range presence {
			if _, ok := bridge.directGrants[entry.DeviceID]; !ok {
				frames = append(frames, map[string]any{"t": "revoke", "deviceId": entry.DeviceID})
			}
		}
	}
	bridge.directMu.Unlock()
	for _, entry := range presence {
		record := bridge.devices[entry.DeviceID]
		if record == nil {
			continue
		}
		if seen, ok := mobileParseTimestamp(entry.LastSeenAt); ok {
			if current, known := mobileParseTimestamp(record.LastSeenAt); !known || seen.After(current) {
				record.LastSeenAt = mobileTimestamp(seen)
			}
		}
	}
	client.mu.Lock()
	if client.conn == nil || client.connContext == nil || client.connContext.Err() != nil {
		client.mu.Unlock()
		bridge.mu.Unlock()
		return
	}
	client.ready = true
	client.mu.Unlock()
	bridge.mu.Unlock()
	sort.Slice(frames, func(i, j int) bool { return frames[i]["t"].(string) < frames[j]["t"].(string) })
	for _, frame := range frames {
		if err := client.send(frame); err != nil {
			log.Printf("[mobile-direct] could not update the direct path: %v", err)
			return
		}
	}
}

func (h *mobileDirectHandler) relayFrame(kind string, raw []byte) {
	switch kind {
	case "in":
		h.bridge.handleInboundFrame(raw)
	case "device_unpaired":
		// The phone removed itself through the direct path. Remove it here and
		// at the pinned relay too.
		var frame struct {
			T        string `json:"t"`
			DeviceID string `json:"deviceId"`
		}
		if err := mobileDecodeStrict(raw, &frame); err != nil {
			log.Printf("[mobile-direct] ignored a malformed unpair frame")
			return
		}
		if err := h.bridge.revokeDevice(frame.DeviceID); err != nil && !errors.Is(err, errMobileDeviceNotFound) {
			log.Printf("[mobile-direct] could not remove an unpaired device: %v", err)
		}
	case "error":
		var frame struct {
			Ref   string `json:"ref"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &frame)
		log.Printf("[mobile-direct] warning: the local relay rejected a %q frame: %s", frame.Ref, mobileTruncateUTF8(frame.Error, 200))
	default:
		// Pairing claims, receipts and text messages belong to the pinned relay.
	}
}

func (h *mobileDirectHandler) relayStateChanged() {
	h.bridge.notify()
}

// mobileMirroredUploader puts an attachment on the pinned relay and on the
// direct path together, so a phone that reads from either finds it. The upload
// succeeds when either relay took it.
type mobileMirroredUploader struct {
	primary mobileAttachmentUploader
	direct  mobileAttachmentUploader
}

func (u mobileMirroredUploader) uploadAttachment(ctx context.Context, attachmentID string, sealed []byte) error {
	directDone := make(chan error, 1)
	go func() { directDone <- u.direct.uploadAttachment(ctx, attachmentID, sealed) }()
	primaryErr := u.primary.uploadAttachment(ctx, attachmentID, sealed)
	directErr := <-directDone
	switch {
	case primaryErr != nil && directErr != nil:
		return primaryErr
	case primaryErr != nil:
		log.Printf("[mobile-direct] an attachment reached only the direct path: %v", primaryErr)
	case directErr != nil:
		log.Printf("[mobile-direct] an attachment did not reach the direct path: %v", directErr)
	}
	return nil
}

// attachmentUploader returns where an attachment goes: the pinned relay, and
// the direct path too while it is connected and some phone holds a grant.
func (bridge *mobileBridge) attachmentUploader(primary *mobileRelayClient) mobileAttachmentUploader {
	direct := bridge.direct.Load()
	if direct == nil {
		return primary
	}
	if state, _ := direct.snapshot(); state != mobileRelayConnected {
		return primary
	}
	bridge.directMu.Lock()
	granted := len(bridge.directGrants) > 0
	bridge.directMu.Unlock()
	if !granted {
		return primary
	}
	return mobileMirroredUploader{primary: primary, direct: direct}
}
