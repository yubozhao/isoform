package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/remoteaccess"
)

// MobileConnectionStatus is the settings view of the iPhone connection. It never
// carries device keys or bearers; the connection code is shown only while open.
type MobileConnectionStatus struct {
	Enabled               bool                    `json:"enabled"`
	RelayURL              string                  `json:"relayUrl"`
	SavedRelayURL         string                  `json:"savedRelayUrl"`
	RelayState            string                  `json:"relayState"`
	RelayLastSeen         string                  `json:"relayLastSeenAt"`
	Warning               string                  `json:"warning,omitempty"`
	WarningCode           string                  `json:"warningCode,omitempty"`
	WarningHost           string                  `json:"warningHost,omitempty"`
	RemoteAccessOffReason string                  `json:"remoteAccessOffReason,omitempty"`
	HubID                 string                  `json:"hubId"`
	HubName               string                  `json:"hubName"`
	ConnectionCode        string                  `json:"connectionCode"`
	ExpiresAt             string                  `json:"expiresAt"`
	PendingPairing        *MobilePendingPairing   `json:"pendingPairing"`
	Devices               []MobileDeviceView      `json:"devices"`
	RemoteAccess          *MobileRemoteAccessView `json:"remoteAccess,omitempty"`
	// InstantPush is true when this Mac's relay has a push gateway (APNs), so
	// phones get notices at once. Without it, notices can come a little late.
	InstantPush bool `json:"instantPush"`
}

type MobileRemoteAccessView struct {
	State     remoteaccess.State `json:"state"`
	LastError string             `json:"lastError,omitempty"`
}

// MobilePendingPairing is a claim that waits for the user to match the code.
type MobilePendingPairing struct {
	PairingID  string `json:"pairingId"`
	DeviceName string `json:"deviceName"`
	Code       string `json:"code"`
}

// MobileDeviceView is the public record of a paired iPhone.
type MobileDeviceView struct {
	DeviceID   string   `json:"deviceId"`
	DeviceName string   `json:"deviceName"`
	CreatedAt  string   `json:"createdAt"`
	ExpiresAt  string   `json:"expiresAt"`
	LastSeenAt string   `json:"lastSeenAt"`
	Scopes     []string `json:"scopes"`
	Simulator  bool     `json:"simulator,omitempty"`
}

var (
	errMobileConnectionOff     = errors.New("turn on the phone connection before generating a code")
	errMobilePairingNotWaiting = errors.New("that phone request is no longer waiting for confirmation")
	errMobileApprovalOffline   = errors.New("the phone connection is offline; confirm again when it reconnects")
	errMobileRemoteAccessOff   = errors.New("remote access is not enabled for this connection")
)

func (record *mobileDeviceRecord) publicView() MobileDeviceView {
	view := record.MobileDeviceView
	view.Scopes = append([]string{}, record.Scopes...)
	return view
}

// mobileDisabledStatus is the status before any connection state exists.
func mobileDisabledStatus() MobileConnectionStatus {
	origin, _ := mobileEffectiveRelayURL("")
	return MobileConnectionStatus{
		RelayURL: origin, SavedRelayURL: "", RelayState: mobileRelayDisabled,
		RemoteAccessOffReason: mobileRemoteAccessOffReason(""),
		HubName:               mobileHubName(), Devices: []MobileDeviceView{},
		InstantPush: mobileInstantPushConfigured(),
	}
}

// mobileBridgeForUse returns the bridge, or nil when the connection never ran.
func (a *App) mobileBridgeForUse() *mobileBridge {
	a.mobileBridgeMu.Lock()
	defer a.mobileBridgeMu.Unlock()
	return a.mobileBridge
}

func openPairingFrame(pairing *mobileOpenPairing) map[string]any {
	return map[string]any{
		"t": "pairing_open", "pairing": pairing.ID,
		"claimTokenHash": pairing.ClaimTokenHash, "expiresAt": mobileTimestamp(pairing.ExpiresAt),
	}
}

// closePairingFrame ends a pairing. A claimed pairing is declined, which lets the
// relay answer the waiting phone.
func closePairingFrame(pairing *mobileOpenPairing) map[string]any {
	if pairing.Claim != nil {
		return declinePairingFrame(pairing.ID)
	}
	return map[string]any{"t": "pairing_close", "pairing": pairing.ID}
}

func declinePairingFrame(pairingID string) map[string]any {
	return map[string]any{"t": "pairing_decline", "pairing": pairingID}
}

func (bridge *mobileBridge) connectionStatus() MobileConnectionStatus {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	now := time.Now()
	if pairing := bridge.pairing; pairing != nil && !now.Before(pairing.ExpiresAt) {
		bridge.pairing = nil
	}
	origin, _ := mobileEffectiveRelayURL(bridge.relay.URL)
	var remoteSnapshot *remoteaccess.Snapshot
	if mobileRemoteAccessMode(bridge.relay.URL) {
		if bridge.remoteAccess != nil {
			snapshot := bridge.remoteAccess.Snapshot()
			if snapshot.RelayURL != "" {
				origin = snapshot.RelayURL
			}
			remoteSnapshot = &snapshot
		} else {
			remoteSnapshot = &remoteaccess.Snapshot{State: remoteaccess.StateOff}
		}
	}
	status := MobileConnectionStatus{
		Enabled: bridge.relay.Enabled, RelayURL: origin, SavedRelayURL: bridge.relay.URL,
		RemoteAccessOffReason: mobileRemoteAccessOffReason(bridge.relay.URL),
		RelayState:            mobileRelayDisabled, HubID: bridge.hubIDLocked(), HubName: mobileHubName(),
		Devices: []MobileDeviceView{}, InstantPush: mobileInstantPushConfigured(),
	}
	if remoteSnapshot != nil {
		status.RemoteAccess = &MobileRemoteAccessView{
			State: remoteSnapshot.State, LastError: remoteSnapshot.LastError,
		}
	}
	status.Warning, status.WarningCode, status.WarningHost = bridge.loopbackWarningLocked(origin, now)
	if bridge.client != nil {
		state, connectedAt := bridge.client.snapshot()
		status.RelayState = state
		if !connectedAt.IsZero() {
			status.RelayLastSeen = mobileTimestamp(connectedAt)
		}
	}
	if pairing := bridge.pairing; pairing != nil {
		status.ConnectionCode = pairing.Invitation
		status.ExpiresAt = mobileTimestamp(pairing.ExpiresAt)
		if claim := pairing.Claim; claim != nil {
			status.PendingPairing = &MobilePendingPairing{PairingID: pairing.ID, DeviceName: claim.DeviceName, Code: claim.Code}
		}
	}
	for _, record := range bridge.devices {
		if expires, ok := mobileParseTimestamp(record.ExpiresAt); ok && now.Before(expires) {
			status.Devices = append(status.Devices, record.publicView())
		}
	}
	sort.Slice(status.Devices, func(i, j int) bool {
		if status.Devices[i].CreatedAt != status.Devices[j].CreatedAt {
			return status.Devices[i].CreatedAt < status.Devices[j].CreatedAt
		}
		return status.Devices[i].DeviceID < status.Devices[j].DeviceID
	})
	return status
}

// mobileSimulatorMarker supports older clients that only identify a Simulator
// in their device name or platform. New clients can send simulator explicitly.
// This is advisory metadata and never changes pairing authority or scopes.
func mobileSimulatorMarker(value string) bool {
	for _, word := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r < 'a' || r > 'z'
	}) {
		if word == "simulator" {
			return true
		}
	}
	return false
}

// loopbackWarningLocked warns about a physical phone without blocking local
// Simulator pairing. The caller holds bridge.mu. The message is retained for
// older wire consumers; desktop UI translates the code and host with i18next.
func (bridge *mobileBridge) loopbackWarningLocked(origin string, now time.Time) (string, string, string) {
	if mobileRemoteAccessMode(bridge.relay.URL) {
		return "", "", ""
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", "", ""
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return "", "", ""
	}
	physical := false
	for _, device := range bridge.devices {
		expires, valid := mobileParseTimestamp(device.ExpiresAt)
		if valid && now.Before(expires) && !device.Simulator && !mobileSimulatorMarker(device.DeviceName) {
			physical = true
			break
		}
	}
	if pairing := bridge.pairing; pairing != nil && now.Before(pairing.ExpiresAt) && pairing.Claim != nil {
		physical = physical || (!pairing.Claim.Simulator && !mobileSimulatorMarker(pairing.Claim.DeviceName))
	}
	if !physical {
		return "", "", ""
	}
	switch bridge.app.currentUILanguage() {
	case "zh-CN":
		return fmt.Sprintf("其他网络上的手机无法访问 %s。", host), "relay_loopback", host
	default:
		return fmt.Sprintf("A phone on another network cannot reach %s.", host), "relay_loopback", host
	}
}

// openPairing replaces any open invitation with a new one (PROTOCOL.md 3.5).
func (bridge *mobileBridge) openPairing() error {
	return bridge.openPairingWithRelay("")
}

// An override permits protocol tests to use a public invitation origin while
// their relay transport remains local.
func (bridge *mobileBridge) openPairingWithRelay(override string) error {
	bridge.mu.Lock()
	if bridge.stopped || !bridge.relay.Enabled || bridge.identity == nil {
		bridge.mu.Unlock()
		return errMobileConnectionOff
	}
	if bridge.client == nil {
		bridge.mu.Unlock()
		return errMobileRemoteAccessNotReady
	}
	if state, _ := bridge.client.snapshot(); state != mobileRelayConnected {
		bridge.mu.Unlock()
		return errMobileRemoteAccessNotReady
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		bridge.mu.Unlock()
		return err
	}
	pairingID := uuid.NewString()
	expires := time.Now().Add(mobilePairingLifetime).Truncate(time.Second)
	hash := mobileClaimTokenHash(mobileClaimToken(secret, pairingID))
	iroh := ""
	relayOrigin := bridge.client.origin
	if override != "" {
		relayOrigin = override
	}
	if mobileRemoteAccessMode(bridge.relay.URL) {
		if bridge.remoteAccess == nil {
			bridge.mu.Unlock()
			return errMobileRemoteAccessNotReady
		}
		snapshot := bridge.remoteAccess.Snapshot()
		iroh = snapshot.Ticket
		if snapshot.State != remoteaccess.StateReady || iroh == "" {
			bridge.mu.Unlock()
			return errMobileRemoteAccessNotReady
		}
		relayOrigin = "" // The ticket is the transport; never publish the local listener.
	} else if !mobileInvitationRelayReachable(relayOrigin) {
		bridge.mu.Unlock()
		return errMobileRemoteAccessNotReady
	}
	warning, warningCode, warningHost := bridge.loopbackWarningLocked(relayOrigin, time.Now())
	invitation, err := encodeMobileInvitation(mobileInvitation{
		V: 1, Relay: relayOrigin, Hub: bridge.identity.HubID, Pairing: pairingID,
		Secret: base64.RawURLEncoding.EncodeToString(secret),
		HubPub: base64.RawURLEncoding.EncodeToString(bridge.identity.Public.Bytes()),
		Exp:    expires.Unix(), Iroh: iroh,
		Warning: warning, WarningCode: warningCode, WarningHost: warningHost,
	})
	if err != nil {
		bridge.mu.Unlock()
		return err
	}
	var superseded map[string]any
	if previous := bridge.pairing; previous != nil {
		superseded = closePairingFrame(previous)
	}
	pairing := &mobileOpenPairing{ID: pairingID, Secret: secret, ClaimTokenHash: hash, ExpiresAt: expires, Invitation: invitation}
	bridge.pairing = pairing
	client := bridge.client
	open := openPairingFrame(pairing)
	bridge.mu.Unlock()

	if superseded != nil {
		bridge.sendControl(superseded)
	}
	// A failed send is harmless: the open invitation is announced again on the
	// next registration.
	_ = client.send(open)
	bridge.notify()
	return nil
}

// mobileInvitationRelayReachable rejects addresses that cannot be reached by a
// phone away from this computer. DNS and service availability can still change.
func mobileInvitationRelayReachable(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !(&net.IPNet{IP: net.ParseIP("100.64.0.0"), Mask: net.CIDRMask(10, 32)}).Contains(ip)
	}
	return strings.Contains(host, ".")
}

// handlePairingClaim authenticates a claim and holds it for the user's decision.
// Three failed claims consume the invitation (PROTOCOL.md section 3.5, step 6).
func (bridge *mobileBridge) handlePairingClaim(raw []byte) {
	var frame struct {
		T         string `json:"t"`
		Pairing   string `json:"pairing"`
		DeviceID  string `json:"deviceId"`
		DevicePub string `json:"devicePub"`
		CT        string `json:"ct"`
	}
	if err := mobileDecodeStrict(raw, &frame); err != nil {
		log.Printf("[mobile-bridge] ignored a malformed pairing claim")
		return
	}
	bridge.mu.Lock()
	if pairing := bridge.pairing; pairing != nil && !time.Now().Before(pairing.ExpiresAt) {
		bridge.pairing = nil
	}
	pairing, identity := bridge.pairing, bridge.identity
	if pairing == nil || identity == nil || pairing.ID != frame.Pairing {
		bridge.mu.Unlock()
		log.Printf("[mobile-bridge] ignored a claim for an invitation that is not open")
		return
	}
	if pairing.Claim != nil || pairing.Resolving {
		// Redelivered after a reconnect; the first claim stays authoritative.
		bridge.mu.Unlock()
		return
	}
	secret := pairing.Secret
	bridge.mu.Unlock()

	claim, kPair, err := decryptMobileClaim(identity, pairing.ID, secret, frame.DeviceID, frame.DevicePub, frame.CT)
	var code string
	if err == nil {
		code, err = mobileConfirmationCode(kPair)
	}

	bridge.mu.Lock()
	if bridge.pairing != pairing || pairing.Claim != nil {
		bridge.mu.Unlock()
		return
	}
	if err != nil {
		pairing.Failures++
		consumed := pairing.Failures >= mobileMaxPairingFailures
		if consumed {
			bridge.pairing = nil
		}
		bridge.mu.Unlock()
		log.Printf("[mobile-bridge] rejected a pairing claim (failures: %d)", pairing.Failures)
		if consumed {
			bridge.sendControl(declinePairingFrame(pairing.ID))
		}
		bridge.notify()
		return
	}
	pairing.Claim = &mobilePendingClaim{
		DeviceID: claim.DeviceID, DeviceName: claim.DeviceName, DevicePub: frame.DevicePub,
		BearerHash: claim.BearerHash, Scopes: claim.Scopes, KPair: kPair, Code: code,
		Simulator: claim.Simulator || mobileSimulatorMarker(claim.Platform) || mobileSimulatorMarker(claim.DeviceName),
	}
	bridge.mu.Unlock()
	log.Printf("[mobile-bridge] an iPhone is waiting for confirmation")
	bridge.notify()
}

// decryptMobileClaim derives kPair from the clear device key and opens the claim.
func decryptMobileClaim(identity *mobileHubIdentity, pairingID string, secret []byte, deviceID, devicePub, ct string) (mobileClaimPlaintext, []byte, error) {
	devicePublic, err := mobileParsePublicKey(devicePub)
	if err != nil || !mobileValidUUID(deviceID) {
		return mobileClaimPlaintext{}, nil, errMobilePairingRejected
	}
	kPair, err := mobilePairKey(identity.Private, devicePublic, secret, identity.HubID, pairingID)
	if err != nil {
		return mobileClaimPlaintext{}, nil, errMobilePairingRejected
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(ct)
	if err != nil {
		return mobileClaimPlaintext{}, nil, errMobilePairingRejected
	}
	plaintext, err := openMobilePairingMessage(kPair, mobilePairingClaimNonceByte, mobileClaimAAD(identity.HubID, pairingID), ciphertext)
	if err != nil {
		return mobileClaimPlaintext{}, nil, err
	}
	claim, err := decodeMobileClaim(plaintext, deviceID, devicePub)
	if err != nil {
		return mobileClaimPlaintext{}, nil, err
	}
	return claim, kPair, nil
}

// confirmPairing approves or declines the held claim. Approval stores the device
// and sends the sealed approval. The device is rolled back if the relay send fails.
func (bridge *mobileBridge) confirmPairing(pairingID string, approve bool) error {
	// Read before taking mu: Today's snapshot holds it, so the Approve
	// button waits for no brief read.
	var briefStatus *mobileBriefStatus
	if approve {
		briefStatus = bridge.cachedBriefStatus()
	}
	bridge.mu.Lock()
	if pairing := bridge.pairing; pairing != nil && !time.Now().Before(pairing.ExpiresAt) {
		bridge.pairing = nil
	}
	pairing := bridge.pairing
	if pairing == nil || pairing.ID != pairingID || pairing.Claim == nil || pairing.Resolving {
		bridge.mu.Unlock()
		return errMobilePairingNotWaiting
	}
	if !approve {
		bridge.pairing = nil
		bridge.mu.Unlock()
		bridge.sendControl(declinePairingFrame(pairingID))
		bridge.notify()
		return nil
	}
	identity, client, claim := bridge.identity, bridge.client, pairing.Claim
	if identity == nil || client == nil {
		bridge.mu.Unlock()
		return errMobileConnectionOff
	}
	now := time.Now()
	record := &mobileDeviceRecord{
		MobileDeviceView: MobileDeviceView{
			DeviceID: claim.DeviceID, DeviceName: claim.DeviceName, CreatedAt: mobileTimestamp(now),
			ExpiresAt: mobileTimestamp(now.Add(mobileDeviceLifetime)), Scopes: append([]string{}, claim.Scopes...),
			Simulator: claim.Simulator,
		},
		DevicePub: claim.DevicePub,
	}
	if bridge.devices[record.DeviceID] != nil {
		bridge.mu.Unlock()
		return errMobilePairingRejected
	}
	approval, err := json.Marshal(mobileApprovalPlaintext{
		DeviceID: record.DeviceID, HubName: mobileHubName(), Scopes: record.Scopes, ExpiresAt: record.ExpiresAt,
		HubPub:      base64.RawURLEncoding.EncodeToString(identity.Public.Bytes()),
		BriefStatus: briefStatus,
	})
	if err != nil {
		bridge.mu.Unlock()
		return err
	}
	ciphertext, err := sealMobilePairingMessage(claim.KPair, mobilePairingApprovalNonceByte, mobileApprovalAAD(identity.HubID, pairingID), approval)
	if err != nil {
		bridge.mu.Unlock()
		return err
	}
	// Reconciliation must not drop this record before the relay has received the approval.
	record.approving = true
	bridge.devices[record.DeviceID] = record
	if err := bridge.persistDevicesLocked(); err != nil {
		delete(bridge.devices, record.DeviceID)
		bridge.mu.Unlock()
		return err
	}
	pairing.Resolving = true
	bridge.mu.Unlock()

	sendErr := client.send(map[string]any{
		"t": "pairing_approve", "pairing": pairingID, "deviceId": record.DeviceID,
		"bearerHash": claim.BearerHash, "scopes": record.Scopes, "expiresAt": record.ExpiresAt,
		"ct": base64.RawURLEncoding.EncodeToString(ciphertext),
	})

	bridge.mu.Lock()
	pairing.Resolving = false
	record.approving = false
	if sendErr != nil {
		delete(bridge.devices, record.DeviceID)
		if err := bridge.persistDevicesLocked(); err != nil {
			log.Printf("[mobile-bridge] could not roll back an unsent approval: %v", err)
		}
		bridge.mu.Unlock()
		return errMobileApprovalOffline
	}
	if bridge.pairing == pairing {
		bridge.pairing = nil
	}
	bridge.mu.Unlock()
	log.Printf("[mobile-bridge] an iPhone was approved")
	bridge.syncKeepAwakePhones()
	bridge.notify()
	if bridge.app != nil && bridge.trackBackground() {
		deviceID := record.DeviceID
		go func() {
			defer bridge.background.Done()
			// The first thing after pairing is the latest brief (phone-setup rule 7).
			bridge.sendCurrentBriefTo(deviceID)
			bridge.app.maybePostFirstReads(bridge.ctx)
		}()
	}
	return nil
}

// revokeDevice removes a device here and tells the relay to delete it there.
func (bridge *mobileBridge) revokeDevice(id string) error {
	if err := bridge.dropDevice(id); err != nil {
		return err
	}
	bridge.syncKeepAwakePhones()
	bridge.sendControl(map[string]any{"t": "revoke", "deviceId": id})
	bridge.notify()
	return nil
}

// GetMobileConnectionStatus reads the connection state without creating files.
func (a *App) GetMobileConnectionStatus() (perfValue MobileConnectionStatus, perfErr error) {
	defer logPageBinding("GetMobileConnectionStatus", time.Now(), &perfValue)
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return mobileDisabledStatus(), nil
	}
	return bridge.connectionStatus(), nil
}

// EnableMobileConnection turns the relay connection on. An empty relayURL keeps
// the saved one.
func (a *App) EnableMobileConnection(relayURL string) (MobileConnectionStatus, error) {
	return a.enableMobileConnection(relayURL, false)
}

// EnableMobileConnectionUsingRelay turns the connection on using the supplied
// saved relay. An empty relay URL clears the saved address.
func (a *App) EnableMobileConnectionUsingRelay(relayURL string) (MobileConnectionStatus, error) {
	return a.enableMobileConnection(relayURL, true)
}

func (a *App) enableMobileConnection(relayURL string, replace bool) (MobileConnectionStatus, error) {
	a.mobileBridgeMu.Lock()
	defer a.mobileBridgeMu.Unlock()
	if a.mobileBridgeStopping {
		return MobileConnectionStatus{}, errMobileBridgeStopping
	}
	bridge := a.mobileBridge
	if bridge == nil {
		dir, err := mobileStateDirectory()
		if err != nil {
			return MobileConnectionStatus{}, err
		}
		created, err := newMobileBridge(a, dir)
		if err != nil {
			return MobileConnectionStatus{}, err
		}
		a.mobileBridge = created
		bridge = created
		bridge.noteTodayChanged()
	}
	if err := bridge.enableRelay(relayURL, replace); err != nil {
		return MobileConnectionStatus{}, err
	}
	return bridge.connectionStatus(), nil
}

// DisableMobileConnection stops the relay client. Paired devices are kept.
func (a *App) DisableMobileConnection() (MobileConnectionStatus, error) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return mobileDisabledStatus(), nil
	}
	if err := bridge.disableRelay(); err != nil {
		return MobileConnectionStatus{}, err
	}
	return bridge.connectionStatus(), nil
}

// GenerateMobileConnectionCode opens a new ten-minute invitation.
func (a *App) GenerateMobileConnectionCode() (MobileConnectionStatus, error) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return MobileConnectionStatus{}, errMobileConnectionOff
	}
	if err := bridge.openPairing(); err != nil {
		return MobileConnectionStatus{}, err
	}
	return bridge.connectionStatus(), nil
}

// ConfirmMobilePairing approves or declines the claim waiting for the user.
func (a *App) ConfirmMobilePairing(pairingID string, approve bool) (MobileConnectionStatus, error) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return MobileConnectionStatus{}, errMobilePairingNotWaiting
	}
	if err := bridge.confirmPairing(strings.TrimSpace(pairingID), approve); err != nil {
		return MobileConnectionStatus{}, err
	}
	if approve && a.telemetry != nil {
		a.telemetry.Capture("iphone.paired", nil)
	}
	return bridge.connectionStatus(), nil
}

// RevokeMobileDevice removes a paired iPhone from this Mac and the relay.
func (a *App) RevokeMobileDevice(deviceID string) (MobileConnectionStatus, error) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return MobileConnectionStatus{}, errMobileDeviceNotFound
	}
	if err := bridge.revokeDevice(strings.TrimSpace(deviceID)); err != nil {
		return MobileConnectionStatus{}, err
	}
	return bridge.connectionStatus(), nil
}

func (a *App) ResetMobileRemoteAccess() (MobileConnectionStatus, error) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return MobileConnectionStatus{}, errMobileRemoteAccessOff
	}
	bridge.mu.Lock()
	if !mobileRemoteAccessMode(bridge.relay.URL) {
		bridge.mu.Unlock()
		return bridge.connectionStatus(), errMobileRemoteAccessOff
	}
	supervisor, client, pairing := bridge.remoteAccess, bridge.client, bridge.pairing
	bridge.pairing = nil
	deviceIDs := make([]string, 0, len(bridge.devices))
	for id := range bridge.devices {
		deviceIDs = append(deviceIDs, id)
	}
	bridge.mu.Unlock()
	if supervisor == nil {
		return bridge.connectionStatus(), errMobileRemoteAccessNotReady
	}
	if client != nil && pairing != nil {
		_ = client.send(closePairingFrame(pairing))
	}
	ctx, cancel := context.WithTimeout(bridge.ctx, 20*time.Second)
	defer cancel()
	if _, err := supervisor.Reset(ctx); err != nil {
		bridge.notify()
		return bridge.connectionStatus(), err
	}
	for _, id := range deviceIDs {
		if err := bridge.revokeDevice(id); err != nil {
			return bridge.connectionStatus(), err
		}
	}
	bridge.notify()
	return bridge.connectionStatus(), nil
}
