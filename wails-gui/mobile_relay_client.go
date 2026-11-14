package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	mobileRelayDisabled   = "disabled"
	mobileRelayConnecting = "connecting"
	mobileRelayConnected  = "connected"
	mobileRelayOffline    = "offline"

	// mobileRelayMaxFrame bounds any frame; brief frames may reach 512 KiB.
	mobileRelayMaxFrame     = 512 * 1024
	mobileRelayWriteTimeout = 10 * time.Second
	mobileRelayHandshake    = 15 * time.Second
	mobileRelayStopTimeout  = 2 * time.Second
)

// mobileRelayTiming holds the transport clocks. Tests shorten them.
type mobileRelayTiming struct {
	Heartbeat       time.Duration
	ReadTimeout     time.Duration
	RegisterTimeout time.Duration
	Dormancy        time.Duration
	InitialBackoff  time.Duration
	MaxBackoff      time.Duration
	HealthyBackoff  time.Duration
	StableAfter     time.Duration
}

var mobileRelayTimes = mobileRelayTiming{
	Heartbeat:       30 * time.Second,
	ReadTimeout:     90 * time.Second,
	RegisterTimeout: 10 * time.Second,
	Dormancy:        30 * time.Second,
	InitialBackoff:  2 * time.Second,
	MaxBackoff:      5 * time.Minute,
	HealthyBackoff:  30 * time.Second,
	StableAfter:     30 * time.Second,
}

var (
	errMobileRelayOffline   = errors.New("the phone connection is offline")
	errMobileRelayReplaced  = errors.New("a newer connection for this computer took over")
	errMobileRelayFrameSize = errors.New("the relay message is too large")
)

// mobileRelayPresence is one device entry of the registered frame.
type mobileRelayPresence struct {
	DeviceID   string `json:"deviceId"`
	LastSeenAt string `json:"lastSeenAt"`
}

// mobileRelayHandler receives relay traffic. The bridge implements it.
// relayRegistered receives the relay's device list. listed is false when the
// registered frame carried no devices field, in which case nothing may be reconciled.
type mobileRelayHandler interface {
	relayRegistered(client *mobileRelayClient, presence []mobileRelayPresence, listed bool)
	relayFrame(kind string, raw []byte)
	relayStateChanged()
}

// mobileRelayClient keeps one outbound WebSocket to the relay. The Mac only dials
// out (PROTOCOL.md section 6), so no inbound port is opened.
type mobileRelayClient struct {
	origin     string
	wsURL      string
	hubID      string
	credential string
	hubName    string
	timing     mobileRelayTiming
	handler    mobileRelayHandler
	// logPrefix names this connection in logs ("[mobile-relay]" by default).
	logPrefix string
	// mirror, when set, receives every frame passed to send, before the
	// connection is checked: the bridge copies phone traffic to its direct
	// path with it (mobile_direct_link.go). It must not block or take bridge.mu.
	mirror func(raw []byte)

	mu          sync.Mutex
	state       string
	connectedAt time.Time
	conn        *websocket.Conn
	connContext context.Context
	ready       bool
	cancelRun   context.CancelFunc
	cancelDial  context.CancelFunc
	done        chan struct{}
	retryNow    chan struct{}
	// lastWake is when the last wake recovery ran (see mobileRelayWakeCoalesce).
	lastWake time.Time

	writeMu sync.Mutex
}

func newMobileRelayClient(origin string, identity mobileHubIdentity, hubName string, handler mobileRelayHandler) (*mobileRelayClient, error) {
	normalized, err := validateMobileRelayURL(origin)
	if err != nil {
		return nil, err
	}
	wsURL, err := mobileRelayWebSocketURL(normalized)
	if err != nil {
		return nil, err
	}
	return &mobileRelayClient{
		origin:     normalized,
		wsURL:      wsURL,
		hubID:      identity.HubID,
		credential: identity.Credential,
		hubName:    hubName,
		timing:     mobileRelayTimes,
		handler:    handler,
		logPrefix:  "[mobile-relay]",
		state:      mobileRelayDisabled,
		done:       make(chan struct{}),
		retryNow:   make(chan struct{}, 1),
	}, nil
}

func mobileRelayWebSocketURL(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", errMobileRelayURL
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", errMobileRelayURL
	}
	parsed.Path = "/v1/hub/ws"
	return parsed.String(), nil
}

// start launches the reconnect loop. It returns immediately.
func (c *mobileRelayClient) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	c.cancelRun = cancel
	c.state = mobileRelayConnecting
	c.mu.Unlock()
	go c.run(ctx)
}

// cancel asks the loop to stop without waiting for it.
func (c *mobileRelayClient) cancel() {
	c.mu.Lock()
	cancel := c.cancelRun
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// stop cancels the loop and waits briefly for it to exit.
func (c *mobileRelayClient) stop() {
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(mobileRelayStopTimeout):
	}
}

// snapshot returns the presence state and the time of the last registration.
func (c *mobileRelayClient) snapshot() (string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.connectedAt
}

// send writes one JSON frame on the live connection. It fails while the relay is
// not registered, so callers can queue frames that must survive a reconnect.
func (c *mobileRelayClient) send(frame any) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(raw) > mobileRelayMaxFrame {
		return errMobileRelayFrameSize
	}
	if c.mirror != nil {
		c.mirror(raw)
	}
	c.mu.Lock()
	conn, ready := c.conn, c.ready
	c.mu.Unlock()
	if conn == nil || !ready {
		return errMobileRelayOffline
	}
	return c.writeRaw(conn, raw)
}

func (c *mobileRelayClient) writeRaw(conn *websocket.Conn, raw []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(mobileRelayWriteTimeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, raw)
}

func (c *mobileRelayClient) run(ctx context.Context) {
	defer close(c.done)
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		c.watchNetwork(watchCtx, mobileRelayNetworkSnapshot, mobileRelayNetworkPoll)
	}()
	defer func() {
		stopWatch()
		<-watchDone
	}()
	backoff := c.timing.InitialBackoff
	for {
		started := time.Now()
		dialCtx, cancelDial := context.WithCancel(ctx)
		c.mu.Lock()
		select {
		case <-c.retryNow:
			backoff = c.timing.InitialBackoff
		default:
		}
		c.cancelDial = cancelDial
		c.mu.Unlock()
		err := c.connectOnce(dialCtx)
		cancelDial()
		c.mu.Lock()
		c.cancelDial = nil
		c.mu.Unlock()
		c.detach()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errMobileRelayReplaced) {
			// PROTOCOL.md section 6: a replaced hub goes dormant before it reconnects.
			log.Printf("%s a newer connection took over; pausing for %s", c.logPrefix, c.timing.Dormancy)
			if !mobileSleep(ctx, c.timing.Dormancy) {
				return
			}
			backoff = c.timing.InitialBackoff
			continue
		}
		// A connection that lasted long enough counts as stable, so the next retry
		// starts from the short delay again.
		if time.Since(started) > c.timing.StableAfter {
			backoff = c.timing.InitialBackoff
		}
		log.Printf("%s offline: %v; retrying in %s", c.logPrefix, err, backoff)
		var retry bool
		backoff, retry = c.waitRetry(ctx, backoff)
		if !retry {
			return
		}
	}
}

func (c *mobileRelayClient) detach() {
	c.mu.Lock()
	c.conn = nil
	c.connContext = nil
	c.ready = false
	c.state = mobileRelayOffline
	c.mu.Unlock()
	c.handler.relayStateChanged()
}

// connectOnce runs one connection from dial to failure: register, heartbeat, and
// dispatch frames until the socket ends or the relay replaces it.
func (c *mobileRelayClient) connectOnce(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: mobileRelayHandshake, Proxy: http.ProxyFromEnvironment}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.credential)
	header.Set("X-Yansu-Hub", c.hubID)
	conn, response, err := dialer.DialContext(ctx, c.wsURL, header)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("could not reach the relay: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(mobileRelayMaxFrame)
	// Recovery also interrupts a socket waiting for its registration response.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopWatch:
		}
	}()

	// The register frame must be the first frame, and the relay answers it promptly.
	registration, err := json.Marshal(map[string]any{"t": "register", "proto": 1, "hubName": c.hubName})
	if err != nil {
		return err
	}
	if err := c.writeRaw(conn, registration); err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Now().Add(c.timing.RegisterTimeout)); err != nil {
		return err
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("the relay closed the registration: %w", err)
	}
	kind, err := mobileRelayFrameType(raw)
	if err != nil || kind != "registered" {
		return errors.New("the relay did not accept this computer")
	}
	var registered struct {
		Devices *[]mobileRelayPresence `json:"devices"`
	}
	if err := json.Unmarshal(raw, &registered); err != nil {
		return errors.New("the relay sent an invalid registration")
	}
	var presence []mobileRelayPresence
	listed := registered.Devices != nil
	if listed {
		presence = *registered.Devices
	}

	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.conn = conn
	c.connContext = ctx
	c.ready = false
	c.state = mobileRelayConnecting
	c.mu.Unlock()
	c.handler.relayRegistered(c, presence, listed)
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if !c.ready || ctx.Err() != nil {
		c.mu.Unlock()
		return errMobileRelayOffline
	}
	c.state = mobileRelayConnected
	c.connectedAt = time.Now()
	c.mu.Unlock()
	log.Printf("%s connected", c.logPrefix)
	c.handler.relayStateChanged()

	conn.SetPingHandler(func(data string) error {
		if err := conn.SetReadDeadline(time.Now().Add(c.timing.ReadTimeout)); err != nil {
			return err
		}
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(mobileRelayWriteTimeout))
	})
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go c.heartbeat(heartbeatCtx)

	for {
		if err := conn.SetReadDeadline(time.Now().Add(c.timing.ReadTimeout)); err != nil {
			return err
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("the relay connection ended: %w", err)
		}
		kind, err := mobileRelayFrameType(raw)
		if err != nil {
			continue
		}
		switch kind {
		case "replaced":
			return errMobileRelayReplaced
		default:
			c.handler.relayFrame(kind, raw)
		}
	}
}

func (c *mobileRelayClient) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(c.timing.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = c.send(map[string]any{"t": "heartbeat"})
		case <-ctx.Done():
			return
		}
	}
}

func mobileRelayFrameType(raw []byte) (string, error) {
	var header struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return "", err
	}
	if header.T == "" {
		return "", errors.New("the relay frame has no type")
	}
	return header.T, nil
}

// mobileRelayNextBackoff doubles the delay up to the supplied reachability cap.
func mobileRelayNextBackoff(current, limit time.Duration) time.Duration {
	if next := current * 2; next < limit {
		return next
	}
	return limit
}

func mobileSleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
