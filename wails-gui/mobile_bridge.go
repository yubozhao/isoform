package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mobileAPIPrefix       = "/mobile/v1"
	mobilePairingLifetime = 10 * time.Minute
	mobileDeviceLifetime  = 90 * 24 * time.Hour
	mobileMaxRequestBytes = 64 * 1024

	mobileMaxConcurrentRequests = 4
	mobileDeviceQueueDepth      = 32
	mobileMaxPendingControl     = 256
)

var errMobileBridgeStopping = errors.New("the desktop is stopping")
var errMobileRemoteRetryNoLongerNeeded = errors.New("remote access retry is no longer needed")

// mobileBridge is the desktop side of the iPhone connection. It owns the hub
// identity, the paired device records, the one open invitation, and the relay
// client. It opens no listener: every byte travels over the outbound relay socket
// as end-to-end encrypted envelopes (PROTOCOL.md sections 4 and 6).
type mobileBridge struct {
	app          *App
	stateDir     string
	ctx          context.Context
	cancel       context.CancelFunc
	requestSlots chan struct{}
	// background tracks detached jobs that touch the app's stores (device
	// request serving, settled-turn timers, coalesced publishes). Stop waits
	// for every admitted job, so the stores are never closed underneath one.
	background sync.WaitGroup
	// briefDeliveryMu orders sequence allocation, offline replay and new brief
	// writes. Acquire it before mu, and release mu before network writes.
	briefDeliveryMu sync.Mutex

	mu                 sync.Mutex
	stopped            bool
	identity           *mobileHubIdentity
	relay              mobileRelayConfig
	client             *mobileRelayClient
	remoteAccess       mobileRemoteAccessSupervisor
	remoteRelayURL     string
	remoteRetrying     bool
	remoteRetryBackoff func(attempt int) time.Duration
	// direct is the second hub connection, to this computer's own relay
	// sidecar, kept beside a pinned relay (mobile_direct_link.go). It is an
	// atomic pointer because mirroring reads it inside relay sends.
	direct         atomic.Pointer[mobileRelayClient]
	directStarting bool
	// directMu guards directGrants. Acquire it after mu, never before.
	directMu     sync.Mutex
	directGrants map[string]mobileDirectGrant
	// replayWindows holds each phone's accepted request numbers
	// (mobile_replay_window.go). Guarded by mu.
	replayWindows map[string]*mobileReplayWindow
	devices       map[string]*mobileDeviceRecord
	pairing       *mobileOpenPairing
	pendingBriefs map[string]mobileBriefFrame
	// briefOutbox keeps each phone's newest brief on disk until the phone
	// confirms it (mobile_brief_outbox.go).
	briefOutbox    *mobileBriefOutbox
	pendingControl []map[string]any
	workers        map[string]chan mobileEnvelope
	// chat holds the phone's view of running chats (mobile_chat.go).
	chat  *mobileChatState
	today mobileTodayCache
	// keepAwake holds the idle-sleep assertion while the setting and power
	// inputs allow it and a phone is paired (mobile_keep_awake.go).
	keepAwake *mobileKeepAwake
}

// mobileDeviceRecord is one paired iPhone (devices.json, version 2). Counters are
// persisted before a frame that uses them is sent.
type mobileDeviceRecord struct {
	MobileDeviceView
	DevicePub            string `json:"devicePub"`
	SendSeq              uint64 `json:"sendSeq"`
	LastAcceptedSeq      uint64 `json:"lastAcceptedSeq"`
	LastAcceptedBriefSeq uint64 `json:"lastAcceptedBriefSeq"`
	// approving is set while an approval is in flight. The relay does not know the
	// device yet, so reconciliation must keep it. It is never persisted.
	approving bool
	// heldScopes are scopes in the stored record that this Mac does not know,
	// granted by a newer Mac. They grant nothing here and are written back
	// unchanged, so the phone keeps them when that newer Mac runs again.
	heldScopes []string
	// The offline queue is also in memory. Track its newest sealed brief
	// separately from chat responses so an older recheck cannot overtake it.
	latestBriefSeq uint64
}

type mobileDeviceFile struct {
	Version int                  `json:"version"`
	HubID   string               `json:"hubId"`
	Devices []mobileDeviceRecord `json:"devices"`
}

// mobileBriefFrame is the relay frame that stores a sealed brief.
type mobileBriefFrame struct {
	// DeliverAt, for an early brief, is its slot time (RFC 3339, UTC).
	DeliverAt string `json:"deliverAt,omitempty"`
	// Local dependencies survive offline queuing but never enter relay JSON.
	attendanceSourceSessions map[string][]string
	desktopOffers            []MobileBriefOffer
	weekLead                 string
	weekLeadFacts            string

	T      string         `json:"t"`
	Env    mobileEnvelope `json:"env"`
	AsOf   string         `json:"asOf"`
	Notify bool           `json:"notify"`
}

type mobileRequestPlaintext struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	Body   string `json:"body"`
}

type mobileResponsePlaintext struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type mobileResponse struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// mobileCapturedResponse records what a route handler writes, so the result can
// be sealed into a response envelope.
type mobileCapturedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *mobileCapturedResponse) Header() http.Header { return w.header }

func (w *mobileCapturedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *mobileCapturedResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

// startConfiguredMobileBridge runs at app startup. It reads saved state without
// creating files, and starts the relay client only when relay.json is enabled.
func startConfiguredMobileBridge(app *App) (*mobileBridge, error) {
	dir, err := mobileStateDirectory()
	if err != nil {
		return nil, err
	}
	if !mobileStateExists(dir) {
		return nil, nil
	}
	bridge, err := newMobileBridge(app, dir)
	if err != nil {
		return nil, err
	}
	if bridge.relay.Enabled {
		if mobileRemoteAccessMode(bridge.relay.URL) {
			go func() {
				if err := bridge.enableRelay("", false); err != nil {
					log.Printf("[mobile-bridge] iPhone connection not started: %v", err)
				}
			}()
		} else {
			bridge.mu.Lock()
			err = bridge.startRelayLocked()
			bridge.mu.Unlock()
			if err != nil {
				log.Printf("[mobile-bridge] iPhone connection not started: %v", err)
			} else {
				// A pinned relay gets the direct path beside it at launch too
				// (PROTOCOL.md section 21.4), not only after Settings enables it.
				bridge.startDirectLink()
			}
		}
	}
	bridge.noteTodayChanged()
	return bridge, nil
}

func newMobileBridge(app *App, stateDir string) (*mobileBridge, error) {
	relay, err := loadMobileRelayConfig(stateDir)
	if err != nil {
		return nil, err
	}
	identity, err := loadMobileHubIdentity(stateDir)
	if err != nil {
		return nil, err
	}
	devices, changed, err := loadMobileDeviceStore(stateDir, time.Now())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	bridge := &mobileBridge{
		app:           app,
		stateDir:      stateDir,
		ctx:           ctx,
		cancel:        cancel,
		requestSlots:  make(chan struct{}, mobileMaxConcurrentRequests),
		relay:         relay,
		identity:      identity,
		devices:       devices,
		pendingBriefs: make(map[string]mobileBriefFrame),
		briefOutbox:   loadMobileBriefOutbox(stateDir),
		workers:       make(map[string]chan mobileEnvelope),
		directGrants:  loadMobileDirectGrants(stateDir),
		replayWindows: loadMobileReplayWindows(stateDir, devices),
	}
	if changed {
		bridge.mu.Lock()
		err = bridge.persistDevicesLocked()
		bridge.mu.Unlock()
		if err != nil {
			cancel()
			return nil, err
		}
	}
	power, assertion := newMobileKeepAwakeSystem()
	bridge.keepAwake = newMobileKeepAwake(stateDir, power, assertion)
	bridge.keepAwake.initPairedPhones(len(devices))
	bridge.registerChatListener()
	if app != nil && app.store != nil {
		if removed, pruneErr := app.store.PruneMobileReceipts(ctx, time.Now()); pruneErr != nil {
			log.Printf("[mobile-chat] could not prune receipts: %v", pruneErr)
		} else if removed > 0 {
			log.Printf("[mobile-chat] pruned %d expired receipts", removed)
		}
	}
	// Start the Today loops now: they build the first snapshot at bridge start, so
	// GET /today never has to wait for a build.
	bridge.startTodayCache()
	return bridge, nil
}

// loadMobileDeviceStore reads devices.json. Version 1 (bearer-hash devices from the
// LAN build) is discarded. Expired devices are removed, and the send counters skip
// ahead so a number used before an unflushed write is never reused.
func loadMobileDeviceStore(dir string, now time.Time) (map[string]*mobileDeviceRecord, bool, error) {
	devices := make(map[string]*mobileDeviceRecord)
	raw, err := os.ReadFile(filepath.Join(dir, "devices.json"))
	if errors.Is(err, os.ErrNotExist) {
		return devices, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, false, errors.New("invalid mobile device store")
	}
	switch header.Version {
	case 1:
		log.Printf("[mobile-bridge] discarded a version 1 device store; paired iPhones must pair again")
		return devices, true, nil
	case 2:
	default:
		return nil, false, errors.New("unsupported mobile device store version")
	}
	var saved mobileDeviceFile
	if err := mobileDecodeStrict(raw, &saved); err != nil {
		return nil, false, errors.New("invalid mobile device store")
	}
	changed := false
	held := 0
	for index := range saved.Devices {
		record := saved.Devices[index]
		// A scope from a newer release is ignored, never a reason to drop the
		// phone: the record stays and only the unknown scope grants nothing.
		record.Scopes, record.heldScopes = mobileSplitKnownScopes(record.Scopes)
		held += len(record.heldScopes)
		if err := validateMobileDeviceRecord(record); err != nil {
			log.Printf("[mobile-bridge] skipped an invalid device record")
			changed = true
			continue
		}
		expires, _ := mobileParseTimestamp(record.ExpiresAt)
		if !now.Before(expires) {
			log.Printf("[mobile-bridge] removed an expired device record")
			changed = true
			continue
		}
		record.SendSeq = mobileSkipAhead(record.SendSeq)
		changed = true
		devices[record.DeviceID] = &record
	}
	if held > 0 {
		log.Printf("[mobile-bridge] paired iPhones hold %d permission(s) this Mac does not know; they are ignored here and kept in the device store", held)
	}
	return devices, changed, nil
}

func validateMobileDeviceRecord(record mobileDeviceRecord) error {
	invalid := errors.New("invalid device record")
	if !mobileValidUUID(record.DeviceID) {
		return invalid
	}
	if _, err := mobileParsePublicKey(record.DevicePub); err != nil {
		return invalid
	}
	if _, ok := mobileParseTimestamp(record.CreatedAt); !ok {
		return invalid
	}
	if _, ok := mobileParseTimestamp(record.ExpiresAt); !ok {
		return invalid
	}
	if record.LastSeenAt != "" {
		if _, ok := mobileParseTimestamp(record.LastSeenAt); !ok {
			return invalid
		}
	}
	scopes, err := mobileNormalizeScopes(record.Scopes)
	if err != nil || len(scopes) != len(record.Scopes) {
		return invalid
	}
	return nil
}

// persistDevicesLocked writes devices.json. The caller holds bridge.mu, so
// concurrent writers cannot overwrite each other with stale snapshots.
func (bridge *mobileBridge) persistDevicesLocked() error {
	devices := make([]mobileDeviceRecord, 0, len(bridge.devices))
	for _, record := range bridge.devices {
		stored := *record
		if len(record.heldScopes) > 0 {
			stored.Scopes = uniqueSortedStrings(append(append([]string(nil), record.Scopes...), record.heldScopes...))
		}
		devices = append(devices, stored)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].DeviceID < devices[j].DeviceID })
	if err := ensureMobileStateDirectory(bridge.stateDir); err != nil {
		return err
	}
	return writeMobilePrivateJSON(filepath.Join(bridge.stateDir, "devices.json"), mobileDeviceFile{
		Version: 2, HubID: bridge.hubIDLocked(), Devices: devices,
	})
}

func (bridge *mobileBridge) hubIDLocked() string {
	if bridge.identity == nil {
		return ""
	}
	return bridge.identity.HubID
}

// sessionKeysLocked derives the session keys for one paired device.
func (bridge *mobileBridge) sessionKeysLocked(record *mobileDeviceRecord) (mobileSessionKeys, error) {
	if bridge.identity == nil {
		return mobileSessionKeys{}, errMobileRelayOffline
	}
	devicePub, err := mobileParsePublicKey(record.DevicePub)
	if err != nil {
		return mobileSessionKeys{}, err
	}
	return mobileDeriveSessionKeys(bridge.identity.Private, devicePub, bridge.identity.HubID, record.DeviceID)
}

// mobileEventFrame carries an unsolicited h2d envelope of kind event.
type mobileEventFrame struct {
	T   string         `json:"t"`
	Env mobileEnvelope `json:"env"`
}

// publishEvent seals one event envelope per device that holds scope. Events
// are not queued for later: a phone that was offline re-reads what it needs.
func (bridge *mobileBridge) publishEvent(scope string, payload []byte) {
	bridge.publishEventForDevices(scope, func(mobileDeviceRecord) []byte { return payload })
}

func (bridge *mobileBridge) publishEventForDevices(scope string, payloadFor func(mobileDeviceRecord) []byte) {
	bridge.mu.Lock()
	if bridge.stopped || bridge.identity == nil || bridge.client == nil {
		bridge.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(bridge.devices))
	for id, record := range bridge.devices {
		if mobileHasScope(record.Scopes, scope) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	frames := make([]mobileEventFrame, 0, len(ids))
	for _, id := range ids {
		record := bridge.devices[id]
		payload := payloadFor(*record)
		if len(payload) == 0 {
			continue
		}
		keys, err := bridge.sessionKeysLocked(record)
		if err != nil {
			continue
		}
		record.SendSeq++
		env, err := sealMobileEnvelope(keys.H2D, bridge.identity.HubID, id, mobileDirH2D, record.SendSeq, mobileKindEvent, payload)
		if err != nil {
			record.SendSeq--
			continue
		}
		frames = append(frames, mobileEventFrame{T: "out", Env: env})
	}
	if err := bridge.persistDevicesLocked(); err != nil {
		log.Printf("[mobile] could not save send counters: %v", err)
	}
	client := bridge.client
	bridge.mu.Unlock()
	for _, frame := range frames {
		if err := client.send(frame); err != nil {
			log.Printf("[mobile] event not delivered: %v", err)
		}
	}
}

// startRelayLocked creates the hub identity if needed and starts the relay client.
// The caller holds bridge.mu.
func (bridge *mobileBridge) startRelayLocked() error {
	if bridge.client != nil {
		return nil
	}
	origin, err := mobileEffectiveRelayURL(bridge.relay.URL)
	if err != nil {
		return err
	}
	if origin == "" {
		origin = bridge.remoteRelayURL
		if origin == "" {
			return errMobileRemoteAccessNotReady
		}
	}
	if bridge.identity == nil {
		identity, err := createMobileHubIdentity(bridge.stateDir)
		if err != nil {
			return err
		}
		bridge.identity = &identity
	}
	client, err := newMobileRelayClient(origin, *bridge.identity, mobileHubName(), bridge)
	if err != nil {
		return err
	}
	client.mirror = bridge.mirrorToDirect
	bridge.client = client
	client.start(bridge.ctx)
	return nil
}

// enableRelay saves the relay settings and starts the client. An empty URL keeps
// the saved one unless replace is true. It never waits for the relay goroutines:
// the caller may hold the App lock, which those goroutines need for brief lookups.
func (bridge *mobileBridge) enableRelay(requested string, replace bool) error {
	return bridge.enableRelayWithRemoteRetryGuard(requested, replace, false)
}

func (bridge *mobileBridge) enableRelayForRemoteRetry() error {
	return bridge.enableRelayWithRemoteRetryGuard("", false, true)
}

func (bridge *mobileBridge) enableRelayWithRemoteRetryGuard(requested string, replace, remoteRetry bool) error {
	bridge.mu.Lock()
	if bridge.stopped {
		bridge.mu.Unlock()
		return errMobileBridgeStopping
	}
	if remoteRetry && !bridge.remoteAccessConfiguredLocked() {
		bridge.mu.Unlock()
		return errMobileRemoteRetryNoLongerNeeded
	}
	next := bridge.relay
	if replace {
		next.URL = ""
	}
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		origin, err := validateMobileRelayURL(trimmed)
		if err != nil {
			bridge.mu.Unlock()
			return err
		}
		next.URL = origin
	}
	origin, err := mobileEffectiveRelayURL(next.URL)
	if err != nil {
		bridge.mu.Unlock()
		return err
	}
	if bridge.identity == nil {
		identity, err := createMobileHubIdentity(bridge.stateDir)
		if err != nil {
			bridge.mu.Unlock()
			return err
		}
		bridge.identity = &identity
	}
	next.Enabled = true
	if err := saveMobileRelayConfig(bridge.stateDir, next); err != nil {
		bridge.mu.Unlock()
		return err
	}
	bridge.relay = next
	if bridge.client != nil && bridge.client.origin != origin {
		bridge.client.cancel()
		bridge.client = nil
	}
	remote := mobileRemoteAccessMode(next.URL)
	var stopSupervisor mobileRemoteAccessSupervisor
	var stopDirect *mobileRelayClient
	if remote {
		// The primary connection becomes the direct path itself.
		if direct := bridge.takeDirectLinkLocked(); direct != nil {
			bridge.mu.Unlock()
			direct.stop()
			bridge.mu.Lock()
		}
		bridge.mu.Unlock()
		snapshot, err := bridge.ensureRemoteAccess(bridge.ctx)
		bridge.mu.Lock()
		if bridge.stopped {
			supervisor := bridge.remoteAccess
			bridge.remoteAccess, bridge.remoteRelayURL = nil, ""
			bridge.mu.Unlock()
			if supervisor != nil {
				supervisor.Stop()
			}
			return errMobileBridgeStopping
		}
		if remoteRetry && !bridge.remoteAccessConfiguredLocked() {
			bridge.mu.Unlock()
			return errMobileRemoteRetryNoLongerNeeded
		}
		if err != nil {
			bridge.startRemoteRetryLocked()
			bridge.mu.Unlock()
			bridge.notify()
			return err
		}
		bridge.remoteRelayURL = snapshot.RelayURL
	} else if !bridge.directPathWantedLocked() {
		stopSupervisor = bridge.remoteAccess
		stopDirect = bridge.takeDirectLinkLocked()
		bridge.remoteAccess = nil
		bridge.remoteRelayURL = ""
	} else {
		// The supervisor keeps serving the direct path beside the pinned relay.
		bridge.remoteRelayURL = ""
	}
	err = bridge.startRelayLocked()
	bridge.mu.Unlock()
	if stopDirect != nil {
		stopDirect.stop()
	}
	if stopSupervisor != nil {
		stopSupervisor.Stop()
	}
	if err == nil && !remote {
		bridge.startDirectLink()
	}
	bridge.notify()
	return err
}

func (bridge *mobileBridge) remoteAccessConfiguredLocked() bool {
	return !bridge.stopped && bridge.relay.Enabled && mobileRemoteAccessMode(bridge.relay.URL)
}

func (bridge *mobileBridge) startRemoteRetryLocked() {
	if bridge.remoteRetrying || !bridge.remoteAccessConfiguredLocked() {
		return
	}
	bridge.remoteRetrying = true
	go bridge.retryRemoteAccess()
}

func (bridge *mobileBridge) retryRemoteAccess() {
	defer func() {
		bridge.mu.Lock()
		bridge.remoteRetrying = false
		bridge.mu.Unlock()
	}()

	for attempt := 1; ; attempt++ {
		bridge.mu.Lock()
		backoff := bridge.remoteRetryBackoff
		bridge.mu.Unlock()
		delay := mobileRemoteAccessRetryDelay(attempt)
		if backoff != nil {
			delay = backoff(attempt)
		}

		timer := time.NewTimer(delay)
		select {
		case <-bridge.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		bridge.mu.Lock()
		configured := bridge.remoteAccessConfiguredLocked()
		bridge.mu.Unlock()
		if !configured {
			return
		}
		if err := bridge.enableRelayForRemoteRetry(); err == nil || errors.Is(err, errMobileRemoteRetryNoLongerNeeded) {
			return
		}
	}
}

func mobileRemoteAccessRetryDelay(attempt int) time.Duration {
	delay := 5 * time.Second
	for i := 1; i < attempt && delay < 60*time.Second; i++ {
		delay *= 2
	}
	if delay > 60*time.Second {
		return 60 * time.Second
	}
	return delay
}

// disableRelay stops the client and saves enabled=false. Paired devices stay
// recorded, so they work again when the connection is turned back on.
func (bridge *mobileBridge) disableRelay() error {
	bridge.mu.Lock()
	next := bridge.relay
	next.Enabled = false
	if err := saveMobileRelayConfig(bridge.stateDir, next); err != nil {
		bridge.mu.Unlock()
		return err
	}
	bridge.relay = next
	client, pairing, supervisor := bridge.client, bridge.pairing, bridge.remoteAccess
	direct := bridge.takeDirectLinkLocked()
	bridge.client, bridge.pairing = nil, nil
	bridge.remoteAccess, bridge.remoteRelayURL = nil, ""
	bridge.mu.Unlock()
	if client != nil {
		if pairing != nil {
			_ = client.send(closePairingFrame(pairing))
		}
		client.stop()
	}
	if direct != nil {
		direct.stop()
	}
	if supervisor != nil {
		supervisor.Stop()
	}
	bridge.notify()
	return nil
}

// Stop shuts the bridge down at app exit. It is safe to call more than once.
func (bridge *mobileBridge) Stop() {
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	if bridge.stopped {
		bridge.mu.Unlock()
		return
	}
	bridge.stopped = true
	var detach func()
	if bridge.chat != nil {
		detach = bridge.chat.detach
	}
	client := bridge.client
	supervisor := bridge.remoteAccess
	direct := bridge.takeDirectLinkLocked()
	bridge.client, bridge.pairing = nil, nil
	bridge.remoteAccess, bridge.remoteRelayURL = nil, ""
	workers := bridge.workers
	bridge.workers = make(map[string]chan mobileEnvelope)
	bridge.mu.Unlock()
	bridge.cancel()
	if detach != nil {
		detach()
	}
	for _, queue := range workers {
		close(queue)
	}
	if client != nil {
		client.stop()
	}
	if direct != nil {
		direct.stop()
	}
	if supervisor != nil {
		supervisor.Stop()
	}
	if bridge.keepAwake != nil {
		bridge.keepAwake.Stop()
	}
	// Jobs admitted before the stop must finish before the caller closes the
	// app's stores.
	bridge.background.Wait()
}

// trackBackground admits one detached job unless the bridge is stopping. An
// admitted job calls background.Done when it finishes.
func (bridge *mobileBridge) trackBackground() bool {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.stopped {
		return false
	}
	bridge.background.Add(1)
	return true
}

func (bridge *mobileBridge) notify() {
	if bridge.app != nil {
		bridge.app.emitBrowserEvent("mobile:updated")
	}
}

// relayRegistered runs after each successful registration. The relay's device list
// is the reconciliation source, presence is refreshed, and then whatever was queued
// while offline is replayed: control frames, the open invitation, and the newest
// brief per device.
func (bridge *mobileBridge) relayRegistered(client *mobileRelayClient, presence []mobileRelayPresence, listed bool) {
	bridge.briefDeliveryMu.Lock()
	defer bridge.briefDeliveryMu.Unlock()
	// Recordings received before a quit or a pause are transcribed now.
	defer bridge.resumeRecordingTranscription()
	bridge.mu.Lock()
	if bridge.stopped || bridge.client != client {
		bridge.mu.Unlock()
		return
	}
	if listed {
		bridge.reconcileLocked(presence)
	}
	for _, entry := range presence {
		record := bridge.devices[entry.DeviceID]
		if record == nil {
			continue
		}
		// A null lastSeenAt means the device was never seen; it parses as unknown.
		if seen, ok := mobileParseTimestamp(entry.LastSeenAt); ok {
			if current, known := mobileParseTimestamp(record.LastSeenAt); !known || seen.After(current) {
				record.LastSeenAt = mobileTimestamp(seen)
			}
		}
	}
	// Registration is now reconciled. Replay may use the socket, while a new
	// brief waits for the replay to finish under briefDeliveryMu. Public
	// connected state is published by the client only after this callback.
	client.mu.Lock()
	// Wake may cancel this registration while it waits for briefDeliveryMu.
	// Only a live response from the current connection can reopen delivery.
	if client.conn == nil || client.connContext == nil || client.connContext.Err() != nil {
		client.mu.Unlock()
		bridge.mu.Unlock()
		return
	}
	client.ready = true
	client.mu.Unlock()
	controls := bridge.pendingControl
	bridge.pendingControl = nil
	var open map[string]any
	if pairing := bridge.pairing; pairing != nil && pairing.Claim == nil {
		open = openPairingFrame(pairing)
	}
	briefs := make([]mobileBriefFrame, 0, len(bridge.pendingBriefs))
	for deviceID, frame := range bridge.pendingBriefs {
		if bridge.devices[deviceID] != nil {
			briefs = append(briefs, frame)
		}
	}
	bridge.pendingBriefs = make(map[string]mobileBriefFrame)
	bridge.mu.Unlock()
	bridge.syncKeepAwakePhones()
	bridge.flushControls(client, controls)
	if open != nil {
		_ = client.send(open)
	}
	bridge.flushPendingBriefs(client, briefs)
	bridge.wakeTodayPhones()
}

func (bridge *mobileBridge) relayStateChanged() {
	bridge.notify()
}

// relayFrame dispatches the relay-to-hub frames that are not registration or
// replacement. Unknown frames are ignored.
func (bridge *mobileBridge) relayFrame(kind string, raw []byte) {
	switch kind {
	case "pairing_claim":
		bridge.handlePairingClaim(raw)
	case "in":
		bridge.handleInboundFrame(raw)
	case "receipt":
		bridge.handleNoticeReceipt(raw)
	case "device_unpaired":
		bridge.handleDeviceUnpaired(raw)
	case "sms_reply":
		bridge.handleSMSReply(raw)
	case "sms_state":
		bridge.handleSMSState(raw)
	case "error":
		bridge.logRelayRejection(raw)
	default:
		log.Printf("[mobile-relay] ignored an unknown frame")
	}
}

// logRelayRejection records a hub frame the relay rejected. The socket stays open,
// so the connection is left alone.
func (bridge *mobileBridge) logRelayRejection(raw []byte) {
	var frame struct {
		Ref   string `json:"ref"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &frame)
	log.Printf("[mobile-relay] warning: the relay rejected a %q frame: %s", frame.Ref, mobileTruncateUTF8(frame.Error, 200))
}

func (bridge *mobileBridge) handleDeviceUnpaired(raw []byte) {
	var frame struct {
		T        string `json:"t"`
		DeviceID string `json:"deviceId"`
	}
	if err := mobileDecodeStrict(raw, &frame); err != nil {
		log.Printf("[mobile-relay] ignored a malformed unpair frame")
		return
	}
	if err := bridge.dropDevice(frame.DeviceID); err != nil && !errors.Is(err, errMobileDeviceNotFound) {
		log.Printf("[mobile-bridge] could not remove an unpaired device: %v", err)
	}
	bridge.syncKeepAwakePhones()
	bridge.notify()
}

// dropDevice removes a device record and its queued state. It sends nothing.
func (bridge *mobileBridge) dropDevice(id string) error {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	record := bridge.devices[id]
	if record == nil {
		return errMobileDeviceNotFound
	}
	delete(bridge.devices, id)
	if err := bridge.persistDevicesLocked(); err != nil {
		bridge.devices[id] = record
		return err
	}
	delete(bridge.replayWindows, id)
	delete(bridge.pendingBriefs, id)
	bridge.briefOutbox.drop(id)
	bridge.closeWorkerLocked(id)
	bridge.dropDirectGrant(id)
	return nil
}

// removeDeviceLocked forgets a device and its queued state without saving. The
// caller holds bridge.mu and saves once afterwards.
func (bridge *mobileBridge) removeDeviceLocked(id string) {
	delete(bridge.replayWindows, id)
	delete(bridge.devices, id)
	delete(bridge.pendingBriefs, id)
	bridge.briefOutbox.drop(id)
	bridge.closeWorkerLocked(id)
	bridge.dropDirectGrant(id)
}

// reconcileLocked applies the relay's device list (PROTOCOL.md section 6). A local
// device the relay does not list was unpaired or expired while this Mac was offline,
// so its record is removed. An approval still in flight is kept, because the relay
// has not seen that device yet. The caller holds bridge.mu.
func (bridge *mobileBridge) reconcileLocked(presence []mobileRelayPresence) {
	relayed := make(map[string]bool, len(presence))
	unknown := 0
	for _, entry := range presence {
		relayed[entry.DeviceID] = true
		if bridge.devices[entry.DeviceID] == nil {
			unknown++
		}
	}
	removed := 0
	for id, record := range bridge.devices {
		if relayed[id] || record.approving {
			continue
		}
		bridge.removeDeviceLocked(id)
		removed++
	}
	if unknown > 0 {
		// A device the relay knows but this Mac cannot serve would only ever get
		// undecryptable requests, so revoke it there; the phone then learns it
		// must pair again.
		for _, entry := range presence {
			if bridge.devices[entry.DeviceID] == nil {
				bridge.pendingControl = append(bridge.pendingControl, map[string]any{"t": "revoke", "deviceId": entry.DeviceID})
			}
		}
		log.Printf("[mobile-bridge] the relay listed %d iPhone(s) this Mac has no record of; revoked them at the relay", unknown)
	}
	if removed > 0 {
		log.Printf("[mobile-bridge] the relay no longer lists %d paired iPhone(s); their records were removed", removed)
		if err := bridge.persistDevicesLocked(); err != nil {
			log.Printf("[mobile-bridge] could not save the device list: %v", err)
		}
	}
}

// mobileSkipAhead advances a persisted send counter past any number that may have
// been used after the last durable write, and never beyond the JSON-safe bound.
func mobileSkipAhead(seq uint64) uint64 {
	if seq >= mobileMaxSequence-mobileSendSeqSkip {
		return mobileMaxSequence
	}
	return seq + mobileSendSeqSkip
}

// handleInboundFrame queues one device envelope on that device's worker. It never
// blocks the socket reader.
func (bridge *mobileBridge) handleInboundFrame(raw []byte) {
	bridge.handleInboundFrameFrom(raw, nil)
}

// handleInboundFrameFrom is handleInboundFrame for a frame from a given relay
// connection; nil is the pinned relay.
func (bridge *mobileBridge) handleInboundFrameFrom(raw []byte, from *mobileRelayClient) {
	var frame struct {
		T   string         `json:"t"`
		Env mobileEnvelope `json:"env"`
	}
	if err := mobileDecodeStrict(raw, &frame); err != nil {
		log.Printf("[mobile-relay] ignored a malformed request frame")
		return
	}
	frame.Env.from = from
	bridge.enqueueInbound(frame.Env)
}

func (bridge *mobileBridge) enqueueInbound(env mobileEnvelope) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.stopped {
		return
	}
	queue := bridge.workers[env.Dev]
	if queue == nil {
		queue = make(chan mobileEnvelope, mobileDeviceQueueDepth)
		bridge.workers[env.Dev] = queue
		go bridge.runDeviceWorker(queue)
	}
	select {
	case queue <- env:
	default:
		log.Printf("[mobile-bridge] dropped a request because the device queue is full")
	}
}

func (bridge *mobileBridge) runDeviceWorker(queue <-chan mobileEnvelope) {
	for env := range queue {
		bridge.serveInbound(env)
	}
}

// closeWorkerLocked ends a device's worker. The caller holds bridge.mu, so no
// sender can write to a queue after it is closed.
func (bridge *mobileBridge) closeWorkerLocked(deviceID string) {
	if queue, ok := bridge.workers[deviceID]; ok {
		close(queue)
		delete(bridge.workers, deviceID)
	}
}

// serveInbound handles one d2h request. Only an authenticated, unseen request
// runs, and its sequence number is persisted before the handler starts, so a
// crash cannot serve it twice. Redelivered requests are only acknowledged.
func (bridge *mobileBridge) serveInbound(env mobileEnvelope) {
	if !bridge.trackBackground() {
		return
	}
	defer bridge.background.Done()
	select {
	case bridge.requestSlots <- struct{}{}:
	case <-bridge.ctx.Done():
		return
	}
	defer func() { <-bridge.requestSlots }()

	bridge.mu.Lock()
	record := bridge.devices[env.Dev]
	if record == nil {
		bridge.mu.Unlock()
		log.Printf("[mobile-bridge] dropped a request from a device that is not paired")
		return
	}
	device := *record
	keys, err := bridge.sessionKeysLocked(record)
	hubID := bridge.hubIDLocked()
	bridge.mu.Unlock()
	if err != nil {
		log.Printf("[mobile-bridge] dropped a request: %v", err)
		return
	}
	plaintext, err := openMobileEnvelope(keys.D2H, env, hubID, device.DeviceID, mobileDirD2H)
	if err != nil {
		log.Printf("[mobile-bridge] dropped a request that failed authentication")
		return
	}
	fresh, err := bridge.acceptRequest(env)
	if err != nil {
		log.Printf("[mobile-bridge] could not record a request: %v", err)
		return
	}
	if !fresh {
		bridge.ackInbound(env)
		return
	}
	request, err := decodeMobileRequest(plaintext)
	if err != nil {
		if mobileValidUUID(request.ID) {
			_ = bridge.sendMobileResponse(device.DeviceID, request.ID, http.StatusBadRequest, mobileErrorBody("the request is not valid"))
		}
		bridge.ackInbound(env)
		return
	}
	status, body := bridge.serveRequest(device, request)
	if err := bridge.sendMobileResponse(device.DeviceID, request.ID, status, body); err != nil {
		log.Printf("[mobile-bridge] could not send a response: %v", err)
	}
	bridge.ackInbound(env)
	audit := mobileAuditEntry{TS: mobileTimestamp(time.Now()), DeviceID: device.DeviceID, Method: request.Method, Path: request.Path, Status: status}
	if err := appendMobileAudit(bridge.stateDir, audit); err != nil {
		log.Printf("[mobile-bridge] could not write the audit log: %v", err)
	}
}

// acceptRequest applies the replay check to an authenticated request and persists
// the new sequence number before the request runs.
func (bridge *mobileBridge) acceptRequest(env mobileEnvelope) (bool, error) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	record := bridge.devices[env.Dev]
	if record == nil {
		return false, errMobileDeviceNotFound
	}
	stream := mobileStreamForKind(mobileKindRequest)
	// The replay window, not a high-water mark: a request the relay held can
	// arrive after a newer one from the direct path (mobile_replay_window.go).
	window := bridge.replayWindowLocked(record)
	next := window.clone()
	if !next.accept(env.Seq) {
		return false, nil
	}
	// The window is saved before the request runs and before devices.json, so
	// a crash at any point never lets the same request run twice.
	bridge.replayWindows[record.DeviceID] = next
	if err := bridge.persistReplayWindowsLocked(bridge.replayWindows); err != nil {
		bridge.replayWindows[record.DeviceID] = window
		return false, err
	}
	previous, previousSeen := record.lastAccepted(stream), record.LastSeenAt
	if next.top > previous {
		record.setLastAccepted(stream, next.top)
	}
	record.LastSeenAt = mobileTimestamp(time.Now())
	if err := bridge.persistDevicesLocked(); err != nil {
		// Undo both, so the relay's redelivery runs the request. If the window
		// cannot be written back, the request stays recorded: it is then never
		// run, which is safer than running it twice.
		record.setLastAccepted(stream, previous)
		record.LastSeenAt = previousSeen
		bridge.replayWindows[record.DeviceID] = window
		if rollbackErr := bridge.persistReplayWindowsLocked(bridge.replayWindows); rollbackErr != nil {
			bridge.replayWindows[record.DeviceID] = next
		}
		return false, err
	}
	return true, nil
}

// ackInbound acknowledges a request on the relay that delivered it, and only
// there. An ack deletes every queued request up to its number, so acking on the
// other path could delete a held request with a lower number that relay has not
// delivered yet. Each relay delivers its own queue in order, so acking up to the
// number it just delivered is safe there.
func (bridge *mobileBridge) ackInbound(env mobileEnvelope) {
	deviceID, seq := env.Dev, env.Seq
	bridge.mu.Lock()
	client := bridge.client
	if env.from != nil {
		client = env.from
	}
	exists := bridge.devices[deviceID] != nil
	bridge.mu.Unlock()
	if client == nil || !exists {
		return
	}
	// The device id is part of the ack: sequence numbers are per device, so an ack
	// without it could delete another phone's queued requests.
	if err := client.send(map[string]any{"t": "ack", "dev": deviceID, "upTo": seq}); err != nil {
		log.Printf("%s could not acknowledge a request: %v", client.logPrefix, err)
	}
}

// serveRequest runs an authorized request through the route table and returns the
// status and the standard JSON body.
func (bridge *mobileBridge) serveRequest(device mobileDeviceRecord, request mobileRequestPlaintext) (int, string) {
	if expires, ok := mobileParseTimestamp(device.ExpiresAt); !ok || !time.Now().Before(expires) {
		return http.StatusUnauthorized, mobileErrorBody("this phone's authorization has expired; pair it again")
	}
	target := mobileAPIPrefix + request.Path
	if request.Query != "" {
		target += "?" + request.Query
	}
	httpRequest, err := http.NewRequestWithContext(bridge.ctx, request.Method, target, strings.NewReader(request.Body))
	if err != nil {
		return http.StatusBadRequest, mobileErrorBody("the request is not valid")
	}
	if request.Body != "" {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	captured := &mobileCapturedResponse{header: http.Header{}}
	bridge.handleAuthorized(captured, httpRequest, device)
	status := captured.status
	if status == 0 {
		status = http.StatusOK
	}
	// The JSON encoder adds a newline that is not part of the response document.
	return status, strings.TrimSuffix(captured.body.String(), "\n")
}

// sendMobileResponse seals a response for one device and sends it as an out frame.
func (bridge *mobileBridge) sendMobileResponse(deviceID, requestID string, status int, body string) error {
	plaintext, err := json.Marshal(mobileResponsePlaintext{ID: requestID, Status: status, Body: body})
	if err != nil {
		return err
	}
	if len(plaintext) > mobileMaxMessagePlaintext {
		plaintext, err = json.Marshal(mobileResponsePlaintext{ID: requestID, Status: http.StatusRequestEntityTooLarge,
			Body: mobileErrorBody("the response is too large for the phone connection")})
		if err != nil {
			return err
		}
	}
	bridge.mu.Lock()
	record := bridge.devices[deviceID]
	if record == nil {
		bridge.mu.Unlock()
		return errMobileDeviceNotFound
	}
	keys, err := bridge.sessionKeysLocked(record)
	if err != nil {
		bridge.mu.Unlock()
		return err
	}
	record.SendSeq++
	if err := bridge.persistDevicesLocked(); err != nil {
		record.SendSeq--
		bridge.mu.Unlock()
		return err
	}
	seq, hubID, client := record.SendSeq, bridge.hubIDLocked(), bridge.client
	bridge.mu.Unlock()
	env, err := sealMobileEnvelope(keys.H2D, hubID, deviceID, mobileDirH2D, seq, mobileKindResponse, plaintext)
	if err != nil {
		return err
	}
	if client == nil {
		return errMobileRelayOffline
	}
	return client.send(map[string]any{"t": "out", "env": env})
}

// publishBrief seals one brief per paired device and sends it. When the relay is
// not connected, the newest brief per device waits for the next registration.
func (bridge *mobileBridge) publishBrief(brief MobileBriefV1, notify bool) error {
	if brief.NarrativeStatus == "quiet" {
		return nil
	}
	weekLead, weekLeadFacts := brief.WeekLead, brief.WeekLeadFacts
	attendanceSources := map[string][]string{}
	for _, row := range brief.MeetingAttendance {
		if row.sourceSessionIDs != nil {
			attendanceSources[row.EventID] = append([]string{}, row.sourceSessionIDs...)
		}
	}
	// The envelope carries the brief's package byte for byte, so the version
	// the phone computes is the one GET /today and GET /briefs/{id} name.
	_, desktopOffers := bridge.app.mobileBriefPhoneProjection(brief)
	pkg, ok := bridge.briefPackage(brief.BriefID)
	if !ok {
		var err error
		if pkg, _, err = bridge.app.buildMobileBriefPackage(brief, bridge.briefSettings()); err != nil {
			return err
		}
	}
	padded, err := mobilePadBrief(pkg.Bytes)
	if err != nil {
		return err
	}
	bridge.briefDeliveryMu.Lock()
	defer bridge.briefDeliveryMu.Unlock()
	bridge.mu.Lock()
	if bridge.stopped || bridge.identity == nil || len(bridge.devices) == 0 {
		bridge.mu.Unlock()
		return nil
	}
	ids := make([]string, 0, len(bridge.devices))
	for id := range bridge.devices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	deliverAt := mobileBriefDeliverAtText(brief)
	previous := make(map[string]uint64, len(ids))
	frames := make(map[string]mobileBriefFrame, len(ids))
	revert := func() {
		for id, seq := range previous {
			bridge.devices[id].SendSeq = seq
		}
	}
	for _, id := range ids {
		record := bridge.devices[id]
		keys, err := bridge.sessionKeysLocked(record)
		if err != nil {
			revert()
			bridge.mu.Unlock()
			return err
		}
		previous[id] = record.SendSeq
		record.SendSeq++
		env, err := sealMobileEnvelope(keys.H2D, bridge.identity.HubID, id, mobileDirH2D, record.SendSeq, mobileKindBrief, padded)
		if err != nil {
			revert()
			bridge.mu.Unlock()
			return err
		}
		frames[id] = mobileBriefFrame{
			T: "brief", Env: env, AsOf: brief.AsOf, Notify: notify, DeliverAt: deliverAt,
			attendanceSourceSessions: attendanceSources, desktopOffers: desktopOffers,
			weekLead: weekLead, weekLeadFacts: weekLeadFacts,
		}
	}
	if err := bridge.persistDevicesLocked(); err != nil {
		revert()
		bridge.mu.Unlock()
		return err
	}
	for _, id := range ids {
		bridge.devices[id].latestBriefSeq = frames[id].Env.Seq
	}
	client := bridge.client
	bridge.mu.Unlock()
	now := time.Now()
	for _, id := range ids {
		bridge.briefOutbox.record(mobileBriefOutboxEntry{
			DeviceID: id, BriefID: brief.BriefID, AsOf: brief.AsOf, Version: pkg.Version,
			Notify: notify, QueuedAt: mobileTimestamp(now), DeliverAt: deliverAt,
		})
		if client == nil || client.send(frames[id]) != nil {
			bridge.storePendingBrief(frames[id], true)
			continue
		}
		bridge.briefOutbox.handed(id, brief.BriefID, now)
	}
	return nil
}

// mobileBriefFrameID reads the brief id a queued frame carries.
func (bridge *mobileBridge) mobileBriefFrameID(frame mobileBriefFrame) string {
	bridge.mu.Lock()
	record := bridge.devices[frame.Env.Dev]
	if record == nil || bridge.identity == nil {
		bridge.mu.Unlock()
		return ""
	}
	keys, err := bridge.sessionKeysLocked(record)
	hubID := bridge.hubIDLocked()
	bridge.mu.Unlock()
	if err != nil {
		return ""
	}
	raw, err := openMobileEnvelope(keys.H2D, frame.Env, hubID, frame.Env.Dev, mobileDirH2D)
	if err != nil {
		return ""
	}
	var head struct {
		BriefID string `json:"briefId"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return ""
	}
	return head.BriefID
}

// resendBriefOutbox sends again what the outbox holds for phones that the
// in-memory queue did not just cover: briefs made before a restart, and
// briefs the relay took over an hour ago that the phone never confirmed.
// The caller holds briefDeliveryMu.
func (bridge *mobileBridge) resendBriefOutbox(client *mobileRelayClient, covered map[string]bool) {
	for _, entry := range bridge.briefOutbox.due(time.Now()) {
		bridge.mu.Lock()
		_, queued := bridge.pendingBriefs[entry.DeviceID]
		bridge.mu.Unlock()
		if covered[entry.DeviceID] || queued {
			continue
		}
		pkg, ok := bridge.briefPackage(entry.BriefID)
		if !ok {
			bridge.briefOutbox.drop(entry.DeviceID)
			continue
		}
		padded, err := mobilePadBrief(pkg.Bytes)
		if err != nil {
			continue
		}
		bridge.mu.Lock()
		record := bridge.devices[entry.DeviceID]
		if record == nil || bridge.identity == nil || bridge.stopped {
			bridge.mu.Unlock()
			bridge.briefOutbox.drop(entry.DeviceID)
			continue
		}
		keys, err := bridge.sessionKeysLocked(record)
		if err != nil {
			bridge.mu.Unlock()
			continue
		}
		previous := record.SendSeq
		record.SendSeq++
		env, err := sealMobileEnvelope(keys.H2D, bridge.identity.HubID, entry.DeviceID, mobileDirH2D, record.SendSeq, mobileKindBrief, padded)
		if err == nil {
			err = bridge.persistDevicesLocked()
		}
		if err != nil {
			record.SendSeq = previous
			bridge.mu.Unlock()
			continue
		}
		record.latestBriefSeq = env.Seq
		bridge.mu.Unlock()
		if entry.Version != pkg.Version {
			entry.Version = pkg.Version
			bridge.briefOutbox.record(entry)
		}
		frame := mobileBriefFrame{T: "brief", Env: env, AsOf: entry.AsOf, Notify: entry.Notify, DeliverAt: entry.DeliverAt}
		if client.send(frame) == nil {
			bridge.briefOutbox.handed(entry.DeviceID, entry.BriefID, time.Now())
		}
	}
}

// storePendingBrief keeps one brief per device. A new brief replaces the old one;
// a flushed brief never replaces a newer one that arrived meanwhile.
func (bridge *mobileBridge) storePendingBrief(frame mobileBriefFrame, replace bool) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	deviceID := frame.Env.Dev
	if bridge.devices[deviceID] == nil {
		return
	}
	if _, exists := bridge.pendingBriefs[deviceID]; exists && !replace {
		return
	}
	bridge.pendingBriefs[deviceID] = frame
}

func (bridge *mobileBridge) flushPendingBriefs(client *mobileRelayClient, frames []mobileBriefFrame) {
	// A device whose queued frame this flush handles, even one a newer
	// publication superseded, is not sent its outbox entry as well.
	covered := map[string]bool{}
	for _, frame := range frames {
		covered[frame.Env.Dev] = true
	}
	for _, frame := range frames {
		// The source policy may have changed while the relay was offline.
		// Never replay the old encrypted document without another read check.
		refreshed, err := bridge.refreshPendingBrief(frame)
		if err != nil {
			continue
		}
		frame = refreshed
		if err := client.send(frame); err != nil {
			bridge.storePendingBrief(frame, false)
			continue
		}
		if id := bridge.mobileBriefFrameID(frame); id != "" {
			bridge.briefOutbox.handed(frame.Env.Dev, id, time.Now())
		}
	}
	bridge.resendBriefOutbox(client, covered)
}

// Recheck offline plaintext against current privacy and mail before replay.
// Changed payloads use a fresh persisted sequence; the caller owns briefDeliveryMu.
func (bridge *mobileBridge) refreshPendingBrief(frame mobileBriefFrame) (mobileBriefFrame, error) {
	bridge.mu.Lock()
	record := bridge.devices[frame.Env.Dev]
	if record == nil || bridge.identity == nil || bridge.stopped {
		bridge.mu.Unlock()
		return mobileBriefFrame{}, errMobileDeviceNotFound
	}
	keys, err := bridge.sessionKeysLocked(record)
	hubID := bridge.hubIDLocked()
	bridge.mu.Unlock()
	if err != nil {
		return mobileBriefFrame{}, err
	}
	raw, err := openMobileEnvelope(keys.H2D, frame.Env, hubID, frame.Env.Dev, mobileDirH2D)
	if err != nil {
		return mobileBriefFrame{}, err
	}
	var brief MobileBriefV1
	if err := json.Unmarshal(raw, &brief); err != nil {
		return mobileBriefFrame{}, err
	}
	brief.WeekLead, brief.WeekLeadFacts = frame.weekLead, frame.weekLeadFacts
	// The encrypted wire document deliberately omits session IDs. Reattach
	// only the local dependencies of this queued publication before rechecking.
	for i := range brief.MeetingAttendance {
		brief.MeetingAttendance[i].sourceSessionIDs = frame.attendanceSourceSessions[brief.MeetingAttendance[i].EventID]
	}
	if frame.desktopOffers != nil {
		brief.Offers = append([]MobileBriefOffer{}, frame.desktopOffers...)
	}
	brief, frame.desktopOffers = bridge.app.mobileBriefPhoneProjection(brief)
	brief = mobileBriefPackageFinish(brief, bridge.briefSettings())
	document, err := json.Marshal(brief)
	if err != nil {
		return mobileBriefFrame{}, err
	}
	if bytes.Equal(document, raw) {
		return frame, nil
	}
	padded, err := mobilePadBrief(document)
	if err != nil {
		return mobileBriefFrame{}, err
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.devices[frame.Env.Dev] != record || bridge.stopped || bridge.identity == nil || bridge.identity.HubID != hubID || record.latestBriefSeq != frame.Env.Seq {
		return mobileBriefFrame{}, errMobileDeviceNotFound
	}
	// A fresh sequence is mandatory: the old envelope's nonce must never be
	// reused for a different document. Persist before putting it on the wire.
	previous := record.SendSeq
	record.SendSeq++
	frame.Env, err = sealMobileEnvelope(keys.H2D, hubID, record.DeviceID, mobileDirH2D, record.SendSeq, mobileKindBrief, padded)
	if err == nil {
		err = bridge.persistDevicesLocked()
	}
	if err != nil {
		record.SendSeq = previous
		return mobileBriefFrame{}, err
	}
	record.latestBriefSeq = frame.Env.Seq
	return frame, nil
}

// sendControl sends a control frame now, or queues it until the relay registers.
func (bridge *mobileBridge) sendControl(frame map[string]any) {
	bridge.mu.Lock()
	client := bridge.client
	bridge.mu.Unlock()
	if client != nil && client.send(frame) == nil {
		return
	}
	bridge.mu.Lock()
	bridge.queueControlLocked(frame)
	bridge.mu.Unlock()
}

// queueControlLocked appends a control frame. The caller holds bridge.mu.
func (bridge *mobileBridge) queueControlLocked(frames ...map[string]any) {
	bridge.pendingControl = append(bridge.pendingControl, frames...)
	if excess := len(bridge.pendingControl) - mobileMaxPendingControl; excess > 0 {
		bridge.pendingControl = bridge.pendingControl[excess:]
	}
}

// flushControls sends queued control frames in order. Unsent frames go back to
// the front of the queue, so none is lost when the connection drops mid-flush.
func (bridge *mobileBridge) flushControls(client *mobileRelayClient, frames []map[string]any) {
	for index, frame := range frames {
		if err := client.send(frame); err != nil {
			bridge.mu.Lock()
			unsent := append([]map[string]any{}, frames[index:]...)
			bridge.pendingControl = append(unsent, bridge.pendingControl...)
			if excess := len(bridge.pendingControl) - mobileMaxPendingControl; excess > 0 {
				bridge.pendingControl = bridge.pendingControl[excess:]
			}
			bridge.mu.Unlock()
			return
		}
	}
}

func mobileErrorBody(message string) string {
	raw, err := json.Marshal(mobileResponse{OK: false, Error: message})
	if err != nil {
		return `{"ok":false}`
	}
	return string(raw)
}

// decodeMobileRequest validates a request plaintext. The route is limited to a
// conservative character set, so no escaping or traversal reaches the router.
func decodeMobileRequest(plaintext []byte) (mobileRequestPlaintext, error) {
	var request mobileRequestPlaintext
	if err := mobileDecodeStrict(plaintext, &request); err != nil {
		return request, errMobileEnvelopeRejected
	}
	if !mobileValidUUID(request.ID) || len(request.Query) > 2048 || !mobileValidRoutePath(request.Path) {
		return request, errMobileEnvelopeRejected
	}
	switch request.Method {
	case http.MethodGet, http.MethodPost, http.MethodDelete:
		return request, nil
	default:
		return request, errMobileEnvelopeRejected
	}
}

func mobileValidRoutePath(path string) bool {
	if path == "" || path[0] != '/' || len(path) > 512 || strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	for index := 0; index < len(path); index++ {
		char := path[index]
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '/', char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return true
}

// mobileBounded returns at most limit items and never a nil slice.
func mobileBounded[T any](items []T, limit int) []T {
	if len(items) > limit {
		return items[:limit]
	}
	if items == nil {
		return []T{}
	}
	return items
}

// mobileKey scopes an idempotency key to one device. Kept for routes that accept
// client retries in later releases.
func mobileKey(deviceID, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return "", false
	}
	return "mobile:" + deviceID + ":" + key, true
}

func mobileJSON(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mobileResponse{OK: status < 400, Data: data, Error: message})
}

func mobileDecode(w http.ResponseWriter, r *http.Request, target any) bool {
	if mediaType := strings.Split(r.Header.Get("Content-Type"), ";")[0]; mediaType != "application/json" {
		mobileJSON(w, http.StatusUnsupportedMediaType, nil, "application/json is required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, mobileMaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		mobileJSON(w, http.StatusBadRequest, nil, "invalid JSON request")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		mobileJSON(w, http.StatusBadRequest, nil, "expected one JSON request")
		return false
	}
	return true
}

// mobileDecodeStrict decodes exactly one JSON value and rejects unknown fields,
// as PROTOCOL.md section 2 requires for every document the hub reads.
func mobileDecodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func mobileStateDirectory() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("YANSU_MOBILE_STATE_DIR")); dir != "" {
		return dir, nil
	}
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "mobile"), nil
}

func newMobileSecret() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func writeMobilePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mobile-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
