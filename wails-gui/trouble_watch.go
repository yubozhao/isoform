package main

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The trouble watch (computer-trouble v1.0.0 rules 1, 7): every minute it
// looks again, says "trouble:changed" when the list changed so Today and the
// popover read it, and keeps the hours trouble stopped Isoform as gaps for
// Activity › Your day. No internet (kind 3) is checked here, off the read
// path: a read never waits on the network.

const (
	troubleChangedEvent = "trouble:changed"
	troubleWatchEvery   = time.Minute
	troubleGapsKey      = "trouble.gaps"
	troubleGapsKeep     = 72 * time.Hour
	troubleLookupWait   = 3 * time.Second
)

// troubleNetworkDown is the last no-internet reading.
var troubleNetworkDown atomic.Bool

// troubleNetworkCheck reports whether the computer can reach the internet:
// a network that is up, and the relay's own name resolving (a name Isoform
// already looks up, so nothing new leaves). Tests replace it.
var troubleNetworkCheck = func(ctx context.Context, relayHost string) bool {
	if !troubleHasNetwork() {
		return false
	}
	if relayHost == "" {
		return true
	}
	lookup, cancel := context.WithTimeout(ctx, troubleLookupWait)
	defer cancel()
	_, err := net.DefaultResolver.LookupHost(lookup, relayHost)
	return err == nil
}

// troubleHasNetwork reports an interface that is up with a routable address.
func troubleHasNetwork() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return true // unknown: never claim trouble on a failed read
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLinkLocalUnicast() && !ipnet.IP.IsLoopback() {
				return true
			}
		}
	}
	return false
}

// troubleRelayHost is the host of the relay this computer uses, if any.
func (a *App) troubleRelayHost() string {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return ""
	}
	bridge.mu.Lock()
	raw := bridge.relay.URL
	bridge.mu.Unlock()
	origin, err := mobileEffectiveRelayURL(raw)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil || host == "localhost" {
		return ""
	}
	return host
}

// troubleGap is a stretch trouble kept Isoform from seeing or making briefs.
type troubleGap struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to,omitempty"`
}

var (
	troubleWatchMu   sync.Mutex
	troubleWatchLast string
)

// runTroubleWatch looks every minute until ctx ends.
func (a *App) runTroubleWatch(ctx context.Context) {
	a.checkTrouble(ctx, time.Now())
	ticker := time.NewTicker(troubleWatchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.checkTrouble(ctx, now)
		}
	}
}

// checkTrouble reads the trouble now, and says so when it changed.
func (a *App) checkTrouble(ctx context.Context, now time.Time) {
	if a == nil {
		return
	}
	a.noteTroubleAlive(now)
	troubleNetworkDown.Store(!troubleNetworkCheck(ctx, a.troubleRelayHost()))
	lines := a.troubleLines(now)
	keys := make([]string, 0, len(lines))
	for _, line := range lines {
		keys = append(keys, line.Kind+"@"+line.Since)
	}
	signature := strings.Join(keys, ",")
	troubleWatchMu.Lock()
	changed := signature != troubleWatchLast
	troubleWatchLast = signature
	troubleWatchMu.Unlock()
	if !changed {
		return
	}
	menuBarTroubleCache.Lock()
	menuBarTroubleCache.lines, menuBarTroubleCache.readAt = lines, now
	menuBarTroubleCache.Unlock()
	a.recordTroubleGaps(lines, now)
	a.emitBrowserEvent(troubleChangedEvent, map[string]any{"count": len(lines)})
	if bridge := a.mobileBridgeForUse(); bridge != nil {
		bridge.noteTodayChanged()
	}
}

// troubleGapKinds are the kinds that leave a gap in Activity: the hours
// Isoform didn't see, or couldn't make a whole brief.
var troubleGapKinds = map[string]bool{troubleScreen: true, troubleSignedOut: true, troubleOffline: true}

// recordTroubleGaps opens a gap when a kind starts and closes it when it
// ends; gaps older than three days go.
func (a *App) recordTroubleGaps(lines []TroubleLine, now time.Time) {
	if a == nil || a.store == nil {
		return
	}
	ctx := context.Background()
	var gaps []troubleGap
	if raw, err := a.store.GetUIState(ctx, troubleGapsKey); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &gaps)
	}
	current := map[string]string{}
	for _, line := range lines {
		if troubleGapKinds[line.Kind] {
			current[line.Kind] = line.Since
		}
	}
	open := map[string]bool{}
	for i := range gaps {
		if gaps[i].To != "" {
			continue
		}
		if _, still := current[gaps[i].Kind]; still {
			open[gaps[i].Kind] = true
			continue
		}
		gaps[i].To = mobileTimestamp(now)
	}
	for kind, since := range current {
		if !open[kind] {
			gaps = append(gaps, troubleGap{Kind: kind, From: since})
		}
	}
	cutoff := now.Add(-troubleGapsKeep)
	kept := gaps[:0]
	for _, gap := range gaps {
		if end, err := time.Parse(time.RFC3339, gap.To); gap.To != "" && err == nil && end.Before(cutoff) {
			continue
		}
		kept = append(kept, gap)
	}
	if raw, err := json.Marshal(kept); err == nil {
		_ = a.store.SetUIState(ctx, troubleGapsKey, string(raw))
	}
}

// troubleGapsBetween are the gaps that overlap [from, to), for Activity.
func (a *App) troubleGapsBetween(from, to time.Time) []troubleGap {
	out := []troubleGap{}
	if a == nil || a.store == nil {
		return out
	}
	raw, err := a.store.GetUIState(context.Background(), troubleGapsKey)
	if err != nil || raw == "" {
		return out
	}
	var gaps []troubleGap
	if json.Unmarshal([]byte(raw), &gaps) != nil {
		return out
	}
	for _, gap := range gaps {
		start, err := time.Parse(time.RFC3339, gap.From)
		if err != nil || !start.Before(to) {
			continue
		}
		if gap.To != "" {
			if end, err := time.Parse(time.RFC3339, gap.To); err == nil && !end.After(from) {
				continue
			}
		}
		out = append(out, gap)
	}
	return out
}

// briefMissingFrom is what a brief made now is missing: the first trouble that
// keeps things out of a brief (the screen, the assistant, the internet).
func briefMissingFrom(lines []TroubleLine) *MobileBriefMissing {
	for _, line := range lines {
		if troubleGapKinds[line.Kind] {
			return &MobileBriefMissing{Kind: line.Kind, Since: line.Since}
		}
	}
	return nil
}

// briefTroubleParam carries the trouble line on a brief_ready moment.
const briefTroubleParam = "trouble"

// briefTroubleLine is the trouble a brief's notification adds after its
// count (notif.briefTrouble), in English like the rest of the notification
// copy for now.
func briefTroubleLine(missing *MobileBriefMissing) string {
	if missing == nil {
		return ""
	}
	since := ""
	if at, err := time.Parse(time.RFC3339, missing.Since); err == nil {
		since = at.Local().Format("3:04 PM")
	}
	switch missing.Kind {
	case troubleScreen:
		return "Isoform hasn't seen your screen since " + since + "."
	case troubleSignedOut:
		return "Your assistant has been signed out since " + since + "."
	case troubleOffline:
		return "No internet since " + since + "."
	default:
		return ""
	}
}

// TroubleStretch is one stretch trouble stopped Isoform, for Activity › Your
// day's feed (activity_feed.go): Unix ms like the feed, EndMs 0 while it lasts.
type TroubleStretch struct {
	StartMs int64
	EndMs   int64
	// Cause is "screen", "signed_out", "offline" or "restarted".
	Cause string
}

// troubleStretches are the stretches overlapping [startMs, endMs); the feed
// clips them to its window.
func (a *App) troubleStretches(startMs, endMs int64) ([]TroubleStretch, error) {
	out := []TroubleStretch{}
	for _, gap := range a.troubleGapsBetween(time.UnixMilli(startMs), time.UnixMilli(endMs)) {
		start, err := time.Parse(time.RFC3339, gap.From)
		if err != nil {
			continue
		}
		stretch := TroubleStretch{StartMs: start.UnixMilli(), Cause: gap.Kind}
		if gap.To != "" {
			if end, err := time.Parse(time.RFC3339, gap.To); err == nil {
				stretch.EndMs = end.UnixMilli()
			}
		}
		out = append(out, stretch)
	}
	return out, nil
}
