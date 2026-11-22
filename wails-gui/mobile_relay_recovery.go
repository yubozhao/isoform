package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// startMobileLifecycle observes power events independently of activity capture.
func (a *App) startMobileLifecycle(ctx context.Context) {
	activitymon.StartSystemStateObservers()
	// An awake computer takes a made-ahead brief's push from the relay at its slot.
	go a.runBriefYields(ctx)
	changes, unsubscribe := activitymon.SubscribeLifecycle()
	go func() {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case kind := <-changes:
				switch kind {
				case activitymon.SystemStateSleep:
					a.mobileRelayGoodbye("sleeping")
				case activitymon.SystemStatePowerOff:
					a.mobilePoweringOff.Store(true)
					a.mobileRelayGoodbye("shutting_down")
				}
			}
		}
	}()
}

func (a *App) mobileRelayGoodbye(cause string) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	client := bridge.client
	bridge.mu.Unlock()
	if client != nil {
		frame := map[string]string{"t": "goodbye", "cause": cause}
		if cause == "sleeping" {
			frame["reason"] = "unknown"
			if bridge.keepAwake != nil {
				frame["reason"] = bridge.keepAwake.SleepReason()
			}
		}
		_ = client.send(frame)
	}
}

const (
	mobileRelayNetworkPoll   = 5 * time.Second
	mobileRelayHealthTimeout = 2 * time.Second
)

// mobileRelayRecovered is called on wake. A saved, disabled connection
// stays disabled. A pre-sleep socket is invalidated before the brief service is
// woken, so successful writes into a dead connection cannot lose a wake brief.
func (a *App) mobileRelayRecovered() {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	client := bridge.client
	bridge.mu.Unlock()
	if client != nil {
		client.recoverAfterWake()
	}
}

// mobileRelayWakeCoalesce is how long after a wake recovery another wake
// notification is treated as a duplicate of it. A duplicate must not tear down
// the fresh connection the first wake just started.
var mobileRelayWakeCoalesce = 10 * time.Second

func (c *mobileRelayClient) recoverAfterWake() {
	c.mu.Lock()
	if !c.lastWake.IsZero() && time.Since(c.lastWake) < mobileRelayWakeCoalesce {
		c.mu.Unlock()
		return
	}
	c.lastWake = time.Now()
	c.ready = false
	c.state = mobileRelayConnecting
	conn := c.conn
	c.conn = nil
	if c.cancelDial != nil {
		c.cancelDial()
	}
	// Close synchronously: even a sender that captured this socket before wake
	// and is waiting for writeMu must fail and queue instead of writing into it.
	if conn != nil {
		_ = conn.Close()
	}
	select {
	case c.retryNow <- struct{}{}:
	default:
	}
	c.mu.Unlock()
	if c.handler != nil {
		c.handler.relayStateChanged()
	}
}

func (c *mobileRelayClient) recoverConnection() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case c.retryNow <- struct{}{}:
	default:
	}
	// A changed interface (including a VPN or IPv6 address) does not imply a
	// registered socket failed. Keep it until its normal transport checks fail;
	// only an unfinished connection attempt needs interrupting for recovery.
	if c.cancelDial != nil && !c.ready {
		c.cancelDial()
	}
}

// waitRetry keeps the longer outage backoff only while health is unreachable.
// Probe during long waits too, so a recovered relay never inherits a five-minute
// wait. A wake or changed interface interrupts the wait and resets the sequence.
func (c *mobileRelayClient) waitRetry(ctx context.Context, delay time.Duration) (time.Duration, bool) {
	limit := c.timing.MaxBackoff
	healthyLimit := c.timing.HealthyBackoff
	if healthyLimit <= 0 {
		healthyLimit = 30 * time.Second
	}
	if delay >= healthyLimit && c.relayHealthy(ctx) {
		limit = min(limit, healthyLimit)
		delay = limit
	}
	deadline := time.Now().Add(delay)
	for {
		timer := time.NewTimer(min(time.Until(deadline), healthyLimit))
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, false
		case <-c.retryNow:
			timer.Stop()
			return c.timing.InitialBackoff, true
		case <-timer.C:
			if delay > healthyLimit && c.relayHealthy(ctx) {
				return min(c.timing.MaxBackoff, healthyLimit), true
			}
			if !time.Now().Before(deadline) {
				return mobileRelayNextBackoff(delay, limit), true
			}
		}
	}
}

//isoform:destination relay
func (c *mobileRelayClient) relayHealthy(ctx context.Context) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.hubOrigin()+"/v1/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Timeout: mobileRelayHealthTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode == http.StatusOK
}

// watchNetwork is owned by the relay run, so disabling the connection stops its
// local interface polling. The snapshot excludes volatile counters and ordering.
func (c *mobileRelayClient) watchNetwork(ctx context.Context, snapshot func() (string, error), interval time.Duration) {
	previous, err := snapshot()
	initialized := err == nil
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := snapshot()
			if err != nil {
				continue
			}
			if initialized && current != previous {
				c.recoverConnection()
			}
			previous, initialized = current, true
		}
	}
}

func mobileRelayNetworkSnapshot() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var entries []string
	for _, network := range interfaces {
		if network.Flags&net.FlagLoopback != 0 || network.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := network.Addrs()
		if err != nil {
			return "", err
		}
		entries = append(entries, network.Name+":"+network.Flags.String())
		for _, address := range addresses {
			entries = append(entries, network.Name+":"+address.String())
		}
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n"), nil
}
