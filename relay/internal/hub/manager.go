// Package hub owns the live hub WebSocket: one connection per hub, register as
// the first frame, takeover by a newer connection, and replay of queued work.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"isoform-relay/internal/notify"
	"isoform-relay/internal/sms"
	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

const (
	registerTimeout = 10 * time.Second
	readTimeout     = 90 * time.Second
	pingInterval    = 30 * time.Second
	writeTimeout    = 10 * time.Second
	storeTimeout    = 5 * time.Second
	outboxDepth     = 512
	// maxInvitation bounds how far ahead a pairing may expire (spec: 10 minutes).
	maxInvitation = 15 * time.Minute
	// maxDeviceLife bounds device expiry set by approval.
	maxDeviceLife = 366 * 24 * time.Hour
)

// SMSReplies is the text message channel as the manager sees it: replies the
// relay received for a hub, opened, the acknowledgement that deletes one, and
// the subscription status the hub is told about when it changes.
type SMSReplies interface {
	Replies(ctx context.Context, hubID string, limit int) ([]sms.Reply, error)
	Ack(ctx context.Context, hubID, id string) error
	Status(ctx context.Context, hubID string) (sms.Status, error)
}

// Manager holds the single live connection for each hub id. SMS is nil when
// the relay has no text message provider.
type Manager struct {
	Store  *store.Store
	Notify *notify.Notifier
	Now    func() time.Time
	SMS    SMSReplies

	mu       sync.Mutex
	sessions map[string]*session
}

// NewManager returns a manager with no connections.
func NewManager(st *store.Store, n *notify.Notifier) *Manager {
	return &Manager{Store: st, Notify: n, Now: time.Now, sessions: map[string]*session{}}
}

// Online reports whether the hub currently holds a live connection.
func (m *Manager) Online(hubID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[hubID]
	return s != nil && !s.away
}

// Kick asks the connected hub to receive queued requests and claims. Storage is
// authoritative, so a missed kick is repaired by the next replay on register.
func (m *Manager) Kick(hubID string) {
	if s := m.session(hubID); s != nil {
		go s.drain()
	}
}

// Unpaired tells a connected hub that a device removed itself.
func (m *Manager) Unpaired(hubID, deviceID string) {
	if s := m.session(hubID); s != nil {
		go s.enqueue(marshal(unpairedFrameOut{T: "device_unpaired", DeviceID: deviceID}))
	}
}

// SMSChanged tells a connected hub its text subscription changed without it
// asking (STOP, START, another hub confirmed the number). The state is the one
// the status route reports, read under the session lock just before the frame
// is queued, so the last frame always carries the latest state. A hub that is
// not connected reads the status route when it next asks.
func (m *Manager) SMSChanged(hubID string) {
	s := m.session(hubID)
	if s == nil || m.SMS == nil {
		return
	}
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		defer cancel()
		status, err := m.SMS.Status(ctx, hubID)
		if err != nil {
			return
		}
		s.enqueue(marshal(smsStateFrameOut{T: "sms_state", State: status.State}))
	}()
}

// CloseAll ends every live connection, used during shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.kill()
	}
}

// Serve runs one hub connection until it ends. The caller has already
// authenticated the hub and completed the WebSocket upgrade.
func (m *Manager) Serve(conn *websocket.Conn, hubID string) {
	conn.SetReadLimit(wire.MaxBriefFrameBytes)
	s := &session{
		m:       m,
		hubID:   hubID,
		conn:    conn,
		outbox:  make(chan []byte, outboxDepth),
		replace: make(chan struct{}, 1),
		done:    make(chan struct{}),
		sent:    map[string][]uint64{},
		claims:  map[string]bool{},
		texts:   map[string]bool{},
	}
	defer s.close()
	if !s.register() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	_ = m.Store.TouchHub(ctx, hubID, m.Now())
	cancel()
	go s.writeLoop()
	s.mu.Lock()
	old, attached := m.attach(s)
	if !attached {
		s.mu.Unlock()
		return
	}
	registered, err := s.registeredFrame()
	ok := err == nil && s.enqueue(registered)
	if ok {
		s.drainLocked()
	}
	s.mu.Unlock()
	if old != nil {
		old.replaced()
	}
	if !ok {
		return
	}
	log.Printf("hub %s connected", wire.Short(hubID))
	s.readLoop()
	log.Printf("hub %s disconnected", wire.Short(hubID))
}

func (m *Manager) attach(s *session) (*session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := m.Store.ClearHubGoodbye(ctx, s.hubID); err != nil {
		log.Printf("hub %s could not clear goodbye: %v", wire.Short(s.hubID), err)
		return nil, false
	}
	old := m.sessions[s.hubID]
	m.sessions[s.hubID] = s
	return old, true
}

func (m *Manager) detach(s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.hubID] == s {
		delete(m.sessions, s.hubID)
	}
}

func (m *Manager) session(hubID string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[hubID]
}

// session is one hub connection. Only writeLoop writes to conn; everything else
// enqueues frames. mu serializes drains so frames leave in sequence order.
type session struct {
	m         *Manager
	hubID     string
	conn      *websocket.Conn
	outbox    chan []byte
	replace   chan struct{}
	done      chan struct{}
	stop      sync.Once
	away      bool   // guarded by Manager.mu
	awayCause string // guarded by Manager.mu

	mu sync.Mutex
	// sent holds, per device, the request numbers handed over on this connection
	// and not yet acknowledged, in the order they were handed over. It is a
	// list, not a highest number: requests a phone sends together reach the
	// relay in any order, and one that arrives after a later-numbered one must
	// still be handed over. The hub serves a device's requests in the order it
	// was handed them, so an acknowledgement of one covers those before it.
	sent   map[string][]uint64
	claims map[string]bool
	// texts are the text replies sent on this connection and not yet acked.
	texts map[string]bool
}

func (m *Manager) goodbye(s *session, cause, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.hubID] != s {
		return store.ErrConflict
	}
	if s.awayCause == "shutting_down" && cause == "closing" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := m.Store.SetHubGoodbye(ctx, s.hubID, cause, reason, m.Now()); err != nil {
		return err
	}
	s.away = true
	s.awayCause = cause
	return nil
}

func (s *session) register() bool {
	if err := s.conn.SetReadDeadline(time.Now().Add(registerTimeout)); err != nil {
		return false
	}
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		return false
	}
	var f registerFrame
	if !decodeFrame(data, &f) || f.T != "register" || f.Proto != 1 || len(f.HubName) < 1 || len(f.HubName) > 120 {
		s.write(websocket.TextMessage, marshal(errorFrameOut{T: "error", Ref: "register", Error: "invalid register frame"}))
		return false
	}
	return true
}

func (s *session) readLoop() {
	s.conn.SetPongHandler(func(string) error {
		return s.conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
	for {
		if err := s.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return
		}
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		s.dispatch(data)
	}
}

func (s *session) writeLoop() {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-s.replace:
			s.write(websocket.TextMessage, replacedFrame)
			_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "replaced"), time.Now().Add(writeTimeout))
			s.kill()
			return
		case frame := <-s.outbox:
			if !s.write(websocket.TextMessage, frame) {
				s.kill()
				return
			}
		case <-ping.C:
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				s.kill()
				return
			}
		}
	}
}

// write sends one frame under a fresh write deadline. The deadline is reset
// each time because the HTTP server's deadline persists after hijack.
func (s *session) write(kind int, data []byte) bool {
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return false
	}
	return s.conn.WriteMessage(kind, data) == nil
}

// enqueue queues a frame for writeLoop. A hub that stops reading is dropped
// after writeTimeout; its queued work is replayed when it reconnects.
func (s *session) enqueue(frame []byte) bool {
	if frame == nil {
		return false
	}
	timer := time.NewTimer(writeTimeout)
	defer timer.Stop()
	select {
	case s.outbox <- frame:
		return true
	case <-s.done:
		return false
	case <-timer.C:
		s.kill()
		return false
	}
}

// replaced asks writeLoop to tell this connection it was superseded.
func (s *session) replaced() {
	select {
	case s.replace <- struct{}{}:
	default:
	}
}

func (s *session) kill() {
	s.stop.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
}

func (s *session) close() {
	s.kill()
	s.m.detach(s)
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	_ = s.m.Store.TouchHub(ctx, s.hubID, s.m.Now())
}

func (s *session) drain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drainLocked()
}

// drainLocked sends each device's queued requests that this connection has not
// handed over yet, lowest number first, then the hub's unanswered claims.
func (s *session) drainLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	now := s.m.Now()
	devices, err := s.m.Store.DevicesOf(ctx, s.hubID)
	if err != nil {
		return
	}
	for _, d := range devices {
		entries, err := s.m.Store.UnsentRequests(ctx, d.ID, 0, now, store.MaxQueued)
		if err != nil {
			return
		}
		for _, e := range entries {
			if slices.Contains(s.sent[d.ID], e.Seq) {
				continue
			}
			if !s.enqueue(marshal(envelopeFrameOut{T: "in", Env: e.Body})) {
				return
			}
			s.sent[d.ID] = append(s.sent[d.ID], e.Seq)
		}
	}
	// Receipts have no queue cap, so a long offline stretch can leave more
	// than one batch; keep draining until a batch comes back short.
	for {
		receipts, err := s.m.Store.UnforwardedReceipts(ctx, s.hubID, store.MaxQueued)
		if err != nil {
			return
		}
		for _, r := range receipts {
			frame := receiptFrameOut{T: "receipt", DeviceID: r.DeviceID, NoticeID: r.NoticeID, State: r.State, Action: r.Action, At: wire.FormatTime(r.At)}
			if !s.enqueue(marshal(frame)) {
				return
			}
			if err = s.m.Store.MarkReceiptForwarded(ctx, r.ID); err != nil {
				return
			}
		}
		if len(receipts) < store.MaxQueued {
			break
		}
	}
	if !s.drainTexts(ctx) {
		return
	}
	claims, err := s.m.Store.ClaimedPairings(ctx, s.hubID, now)
	if err != nil {
		return
	}
	for _, c := range claims {
		if s.claims[c.PairingID] {
			continue
		}
		frame := claimFrameOut{T: "pairing_claim", Pairing: c.PairingID, DeviceID: c.DeviceID, DevicePub: c.DevicePub, CT: c.CT}
		if !s.enqueue(marshal(frame)) {
			return
		}
		s.claims[c.PairingID] = true
	}
}

// drainTexts sends the hub's queued text replies once per connection. They
// stay queued until the hub acks each one, so a reply lost with a dropped
// connection is sent again on the next connect. It reports whether the
// drain may continue.
func (s *session) drainTexts(ctx context.Context) bool {
	if s.m.SMS == nil {
		return true
	}
	replies, err := s.m.SMS.Replies(ctx, s.hubID, store.KeptSMSReplies)
	if err != nil {
		return false
	}
	for _, r := range replies {
		if s.texts[r.ID] {
			continue
		}
		if !s.enqueue(marshal(smsReplyFrameOut{T: "sms_reply", ID: r.ID, Body: r.Body, At: wire.FormatTime(r.At)})) {
			return false
		}
		s.texts[r.ID] = true
	}
	return true
}

func (s *session) registeredFrame() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	devices, err := s.m.Store.DevicesOf(ctx, s.hubID)
	if err != nil {
		return nil, err
	}
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		view := deviceView{DeviceID: d.ID}
		if !d.LastSeenAt.IsZero() {
			seen := wire.FormatTime(d.LastSeenAt)
			view.LastSeenAt = &seen
		}
		views = append(views, view)
	}
	return marshal(registeredFrameOut{T: "registered", ServerTime: wire.FormatTime(s.m.Now()), Devices: views}), nil
}

func decodeFrame(data []byte, v any) bool {
	return wire.DecodeStrict(bytes.NewReader(data), v) == nil
}

func marshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

var replacedFrame = []byte(`{"t":"replaced"}`)
