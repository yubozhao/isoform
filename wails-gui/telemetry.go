package main

import (
	"context"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"yansu-agent/agent/native"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
)

const telemetryFlushTimeout = 2 * time.Second

// PostHogTelemetry is the desktop's only product-event sender. The allowlist
// below is also the privacy boundary for legacy native.Telemetry callers:
// agent traces, context snapshots and arbitrary properties never leave here.
type PostHogTelemetry struct {
	client      posthog.Client
	transport   *telemetryTransport
	key         string
	config      posthog.Config
	anonymousID string
	dataDir     string
	osVersion   string
	locale      string
	state       telemetryProductState
	stateLoaded bool
	flushCancel context.CancelFunc
	flushDone   <-chan struct{}

	enabled atomic.Bool
	mu      sync.Mutex
}

// NewPostHogTelemetry uses only build-injected configuration. An unconfigured
// build or saved opt-out never initializes the network client.
func NewPostHogTelemetry() *PostHogTelemetry {
	t := newDormantPostHogTelemetry()
	t.SetSharingEnabled(telemetrySharingOn())
	return t
}

func newDormantPostHogTelemetry() *PostHogTelemetry {
	t := &PostHogTelemetry{dataDir: configDirOrFallback()}
	t.key, t.config.Endpoint = envPostHogConfig()
	return t
}

// telemetryTransport rechecks consent at the network boundary. Revocation is
// permanent for this client, so opting back in cannot release an old queue.
type telemetryTransport struct {
	base    http.RoundTripper
	revoked atomic.Bool
}

func (t *telemetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.revoked.Load() || !telemetrySharingOn() {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	}
	return t.base.RoundTrip(req)
}

// SetSharingEnabled applies a successfully persisted preference. Retire the
// old transport before closing; a canceled context aborts in-flight uploads.
//
//isoform:destination telemetry
func (t *PostHogTelemetry) SetSharingEnabled(enabled bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !enabled {
		t.enabled.Store(false)
		if t.flushCancel != nil {
			t.flushCancel()
		}
		if t.transport != nil {
			t.transport.revoked.Store(true)
		}
		if t.client != nil {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_ = t.client.CloseWithContext(ctx)
			t.client = nil
		}
		return
	}
	cfg := t.config
	// The SDK appends /batch/ itself. Build settings commonly include a final
	// slash; normalize it before validating the ingest origin.
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if t.client != nil || t.key == "" || !validTelemetryHost(cfg.Endpoint) || !telemetrySharingOn() {
		return
	}
	if t.dataDir == "" {
		t.dataDir = configDirOrFallback()
	}
	id, err := loadTelemetryInstallID(t.dataDir)
	if err != nil {
		log.Printf("[telemetry] install identity unavailable; telemetry disabled")
		return
	}
	t.anonymousID = id
	t.osVersion = telemetryOSVersion()
	cfg.MaxRetries = posthog.Ptr(0)
	cfg.RetryAfter = func(int) time.Duration { return 0 }
	cfg.ShutdownTimeout = telemetryFlushTimeout
	cfg.BatchUploadTimeout = telemetryFlushTimeout
	cfg.BatchSubmitTimeout = -1 // drop on saturation, never wait for network
	cfg.MaxEnqueuedRequests = 50
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 20
	}
	cfg.DisableGeoIP = posthog.Ptr(true)
	// SDK errors may contain response bodies; telemetry is best effort and
	// must not copy service errors into application logs.
	cfg.Logger = posthog.StdLogger(log.New(io.Discard, "", 0), false)
	base := cfg.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &telemetryTransport{base: base}
	cfg.Transport = transport
	client, err := posthog.NewWithConfig(t.key, cfg)
	if err != nil {
		log.Printf("[telemetry] client unavailable; telemetry disabled")
		return
	}
	if t.flushCancel != nil {
		t.flushCancel()
	}
	if t.transport != nil {
		t.transport.revoked.Store(true)
	}
	t.flushCancel = nil
	t.flushDone = nil
	t.client = client
	t.transport = transport
	t.enabled.Store(true)
}

// The historical name is retained for callers, but runtime environment values
// cannot activate telemetry. scripts/build-wails.sh is the configuration boundary.
func envPostHogConfig() (key, host string) {
	return strings.TrimSpace(DefaultDesktopPostHogKey), strings.TrimSpace(DefaultDesktopPostHogHost)
}

func validTelemetryHost(host string) bool {
	u, err := url.Parse(host)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.ForceQuery || u.RawQuery != "" || strings.Contains(host, "#") {
		return false
	}
	// HTTP is useful only for a local test endpoint. Production uses TLS.
	return u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))
}

func telemetrySharingOn() bool {
	return currentTelemetryTier() != TelemetryTierOff
}

// setLocale caches the successfully persisted UI language. Event capture must
// never read SQLite: its single connection may be occupied by background work,
// and waiting for it under either telemetry lock would stall capture/consent.
func (t *PostHogTelemetry) setLocale(locale string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.locale = normalizeUILanguage(locale)
	t.mu.Unlock()
}

func (t *PostHogTelemetry) Capture(event string, properties map[string]any) {
	if t == nil || !t.enabled.Load() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.captureLocked(event, properties)
}

func (t *PostHogTelemetry) captureLocked(event string, properties map[string]any) bool {
	// Hold the tier lock through enqueue so an off transition cannot race
	// between the consent check and submission to the SDK queue.
	telemetryTierMu.Lock()
	defer telemetryTierMu.Unlock()
	tier := telemetryTier
	if tier == "" {
		tier = defaultTelemetryTier
	}
	if t.client == nil || !t.enabled.Load() || tier == TelemetryTierOff {
		return false
	}
	props, allowed := productTelemetryProperties(event, properties)
	if !allowed {
		return false
	}
	if t.anonymousID == "" {
		id, err := loadTelemetryInstallID(configDirOrFallback())
		if err != nil {
			return false
		}
		t.anonymousID = id
	}
	props["app_version"] = strings.TrimSpace(Version)
	props["flavor"] = appFlavor()
	props["build_channel"] = telemetryBuildChannel()
	props["os"] = telemetryOS()
	props["os_version"] = t.osVersion
	props["locale"] = normalizeUILanguage(t.locale)
	props["telemetry_tier"] = tier
	props["$process_person_profile"] = false
	props["$geoip_disable"] = true
	return t.client.Enqueue(posthog.Capture{
		DistinctId: t.anonymousID,
		Event:      event,
		Properties: props,
	}) == nil
}

// Only these product events and typed properties can cross the boundary.
// In particular, caller maps cannot override metadata or inject text/URLs/IDs.
func productTelemetryProperties(event string, in map[string]any) (posthog.Properties, bool) {
	out := posthog.Properties{}
	switch event {
	case "app.open", "brief.first_seen", "iphone.paired", "playbook.run":
	case "chat.turn_sent":
		out["count"] = 1
		if source, ok := todayTelemetrySource(in["source"]); ok {
			out["source"] = source
		}
	case "today.action", "phone_today.action":
		action, _ := in["action"].(string)
		kind, _ := in["kind"].(string)
		outcome, _ := in["outcome"].(string)
		if !todayTelemetryActionID(event, action) || !todayTelemetryKind(kind) || !todayTelemetryOutcome(outcome) || !todayTelemetryNumber(in["ms_to_result"]) {
			return nil, false
		}
		out["action"], out["kind"], out["outcome"], out["ms_to_result"] = action, kind, outcome, in["ms_to_result"]
	case "tasks.action":
		action, _ := in["action"].(string)
		kind, _ := in["kind"].(string)
		outcome, _ := in["outcome"].(string)
		if !tasksTelemetryActionID(action) || !todayTelemetryKind(kind) || !todayTelemetryOutcome(outcome) || !todayTelemetryNumber(in["ms_to_result"]) {
			return nil, false
		}
		out["action"], out["kind"], out["outcome"], out["ms_to_result"] = action, kind, outcome, in["ms_to_result"]
	case "tasks.section_seen":
		section, _ := in["section"].(string)
		if !tasksTelemetrySection(section) || !todayTelemetryNumber(in["rows"]) {
			return nil, false
		}
		out["section"], out["rows"] = section, in["rows"]
	case "assistant.signed_out", "assistant.signed_out_fixed":
		assistant, _ := in["assistant"].(string)
		if assistant != "claude-code" && assistant != "codex" {
			return nil, false
		}
		out["assistant"] = assistant
		if event == "assistant.signed_out_fixed" {
			if !todayTelemetryNumber(in["seconds"]) {
				return nil, false
			}
			out["seconds"] = in["seconds"]
		}
	case "phone_chat.queued":
	case "phone_chat.sent_after_wait":
		if !todayTelemetryNumber(in["wait_minutes"]) {
			return nil, false
		}
		out["wait_minutes"] = in["wait_minutes"]
	case "phone_chat.undone":
		if !todayTelemetryNumber(in["count"]) {
			return nil, false
		}
		out["count"] = in["count"]
	case "today.section_seen":
		section, _ := in["section"].(string)
		switch section {
		case "brief", "before_you_talk", "needs_you", "done_for_you", "your_topics", "your_week", "getting_to_know_you", "your_projects":
		default:
			return nil, false
		}
		if !todayTelemetryNumber(in["rows"]) {
			return nil, false
		}
		out["section"], out["rows"] = section, in["rows"]
	case "update.checked":
		result, _ := in["result"].(string)
		switch result {
		case "none", "found":
			out["result"] = result
		case "error":
			class, _ := in["error_class"].(string)
			if !updateErrorClassAllowed(class) {
				return nil, false
			}
			out["result"], out["error_class"] = result, class
		default:
			return nil, false
		}
	case "update.staged":
		from, okFrom := updateTelemetryVersion(in["from"])
		to, okTo := updateTelemetryVersion(in["to"])
		if !okFrom || !okTo {
			return nil, false
		}
		out["from"], out["to"] = from, to
	case "update.installed":
		from, okFrom := updateTelemetryVersion(in["from"])
		to, okTo := updateTelemetryVersion(in["to"])
		how, _ := in["how"].(string)
		if !okFrom || !okTo || !updateHowAllowed(how) {
			return nil, false
		}
		out["from"], out["to"], out["how"] = from, to, how
		if _, present := in["waited_days"]; present {
			days, ok := updateTelemetryInt(in["waited_days"], 3650)
			if !ok {
				return nil, false
			}
			out["waited_days"] = days
		}
	case "update.rolled_back":
		from, okFrom := updateTelemetryVersion(in["from"])
		to, okTo := updateTelemetryVersion(in["to"])
		if !okFrom || !okTo {
			return nil, false
		}
		attempts, ok := updateTelemetryInt(in["attempts"], 10)
		if !ok {
			return nil, false
		}
		out["from"], out["to"], out["attempts"] = from, to, attempts
	case "update.quit_probe":
		result, _ := in["result"].(string)
		reason, _ := in["reason"].(string)
		if (result != "passed" && result != "failed") || !updateProbeReasonAllowed(reason) {
			return nil, false
		}
		out["result"], out["reason"] = result, reason
	case "update.running":
		version, okVersion := updateTelemetryVersion(in["version"])
		how, _ := in["how"].(string)
		if !okVersion || !updateHowAllowed(how) {
			return nil, false
		}
		out["version"], out["how"] = version, how
		if _, present := in["release_to_run_minutes"]; present {
			minutes, ok := updateTelemetryInt(in["release_to_run_minutes"], 1000000)
			if !ok {
				return nil, false
			}
			out["release_to_run_minutes"] = minutes
		}
	case "app.daily_active":
		day, ok := in["date"].(string)
		if _, err := time.Parse(time.DateOnly, day); !ok || err != nil {
			return nil, false
		}
		out["date"] = day
	case "app.active_time":
		seconds, ok := in["active_seconds"].(float64)
		if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 30 {
			return nil, false
		}
		session, ok := in["session_id"].(string)
		id, err := uuid.Parse(session)
		if !ok || err != nil || id.Version() != 4 {
			return nil, false
		}
		out["active_seconds"] = seconds
		out["session_id"] = id.String()
	default:
		return nil, false
	}
	return out, true
}

// Map IDs and closed enums are the only strings accepted from Today callers.
// Never copy arbitrary properties from the UI or relay into product telemetry.
func todayTelemetryActionID(event, action string) bool {
	var prefix string
	var digits, max int
	switch event {
	case "today.action":
		if action == "D01" {
			return true
		}
		if strings.HasPrefix(action, "T") {
			prefix, digits, max = "T", 2, 87
		} else {
			prefix, digits, max = "N", 2, 6
		}
	case "phone_today.action":
		prefix, digits, max = "P", 3, 119
	default:
		return false
	}
	if len(action) != len(prefix)+digits || !strings.HasPrefix(action, prefix) {
		return false
	}
	for _, c := range action[len(prefix):] {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(action[len(prefix):])
	return err == nil && n > 0 && n <= max
}

func todayTelemetryKind(kind string) bool {
	switch kind {
	case "ask", "do", "reach", "open":
		return true
	default:
		return false
	}
}

func todayTelemetryOutcome(outcome string) bool {
	switch outcome {
	case "done", "undone", "stopped", "failed", "sent", "opened":
		return true
	default:
		return false
	}
}

func todayTelemetryNumber(value any) bool {
	switch v := value.(type) {
	case int:
		return v >= 0
	case float64:
		return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
	default:
		return false
	}
}

func todayTelemetrySource(value any) (map[string]any, bool) {
	source, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	surface, _ := source["surface"].(string)
	action, _ := source["action"].(string)
	var event string
	switch surface {
	case "today":
		event = "today.action"
	case "phone_today":
		event = "phone_today.action"
	default:
		return nil, false
	}
	if !todayTelemetryActionID(event, action) {
		return nil, false
	}
	return map[string]any{"surface": surface, "action": action}, true
}

// BeginClose starts a bounded flush without blocking a Wails callback. Shutdown
// can join the returned channel after other services have stopped. No retries,
// disk spool or background process keeps the application alive.
func (t *PostHogTelemetry) BeginClose() <-chan struct{} {
	done := make(chan struct{})
	if t == nil {
		close(done)
		return done
	}
	t.mu.Lock()
	if t.flushDone != nil {
		done := t.flushDone
		t.mu.Unlock()
		return done
	}
	t.flushDone = done
	t.enabled.Store(false)
	client := t.client
	t.client = nil
	ctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
	t.flushCancel = cancel
	t.mu.Unlock()
	if client == nil {
		cancel()
		close(done)
		return done
	}
	go func() {
		defer close(done)
		defer cancel()
		_ = client.CloseWithContext(ctx)
	}()
	return done
}

// Close is the bounded join used by tests and non-UI owners.
func (t *PostHogTelemetry) Close() { <-t.BeginClose() }

var _ native.Telemetry = (*PostHogTelemetry)(nil)
