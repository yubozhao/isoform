// Package apnspush sends brief notifications through Apple's HTTP/2 provider
// API. Its payload is fixed: no names, titles, counts or content.
package apnspush

import (
	"context"
	"crypto/elliptic"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"golang.org/x/net/http2"
	"isoform-relay/internal/push"
)

// Config is the APNs provider configuration. All five fields are set together.
type Config struct {
	KeyFile     string
	KeyID       string
	TeamID      string
	Topic       string
	Environment string
}

// LoadConfig reads the optional ISOFORM_RELAY_APNS_* settings. Empty means push
// is disabled; a partial set is an error.
func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{
		KeyFile:     strings.TrimSpace(getenv("ISOFORM_RELAY_APNS_KEY_FILE")),
		KeyID:       strings.TrimSpace(getenv("ISOFORM_RELAY_APNS_KEY_ID")),
		TeamID:      strings.TrimSpace(getenv("ISOFORM_RELAY_APNS_TEAM_ID")),
		Topic:       strings.TrimSpace(getenv("ISOFORM_RELAY_APNS_TOPIC")),
		Environment: strings.TrimSpace(getenv("ISOFORM_RELAY_APNS_ENVIRONMENT")),
	}
	values := []string{c.KeyFile, c.KeyID, c.TeamID, c.Topic, c.Environment}
	present := 0
	for _, value := range values {
		if value != "" {
			present++
		}
	}
	if present == 0 {
		return c, nil
	}
	if present != len(values) {
		return c, errors.New("configure all five ISOFORM_RELAY_APNS settings together")
	}
	identifier := regexp.MustCompile(`^[A-Z0-9]{10}$`)
	topic := regexp.MustCompile(`^[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+$`)
	if !identifier.MatchString(c.KeyID) || !identifier.MatchString(c.TeamID) ||
		!topic.MatchString(c.Topic) || len(c.Topic) > 255 || !filepath.IsAbs(c.KeyFile) {
		return c, errors.New("APNs requires valid key/team IDs, bundle topic, and an absolute private-key path")
	}
	switch c.Environment {
	case "development", "production":
	default:
		return c, errors.New("ISOFORM_RELAY_APNS_ENVIRONMENT must be development or production")
	}
	return c, nil
}

// Configured reports whether APNs sending is enabled.
func (c Config) Configured() bool { return c.KeyFile != "" }

// Configuration is the part of the APNs setup the HTTP API needs.
func (c Config) Configuration() push.Configuration {
	if !c.Configured() {
		return push.Configuration{}
	}
	return push.Configuration{Environment: c.Environment}
}

// Client sends notifications with a shared, refreshed provider token.
type Client struct {
	client *apns2.Client
	config Config
}

// Credentials and topic configuration are fixed for this process. Queued work
// is retained until an operator repairs them and restarts.
var (
	errIncompleteNotice = errors.New("notice is missing its id or sealed text")
	errPayloadTooLarge  = errors.New("notification payload exceeds the APNs limit")
)

type ConfigurationError struct{}

func (*ConfigurationError) Error() string { return "APNs rejected provider configuration" }

type configurationError = ConfigurationError

type ProviderCooldownError struct{}

func (*ProviderCooldownError) Error() string { return "APNs requires a provider-wide cooldown" }

type providerCooldownError = ProviderCooldownError

// NewClient loads the owner-only P-256 signing key and builds an HTTP/2 client.
func NewClient(c Config) (*Client, error) {
	if !c.Configured() {
		return nil, errors.New("APNs is not configured")
	}
	file, err := os.Open(c.KeyFile)
	if err != nil {
		return nil, errors.New("unable to open the APNs private key")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, errors.New("APNs private key must be a regular owner-only file under 16 KiB")
	}
	keyBytes, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(keyBytes) > 16384 {
		return nil, errors.New("unable to read the APNs private key")
	}
	key, err := token.AuthKeyFromBytes(keyBytes)
	if err != nil || key == nil || key.Curve != elliptic.P256() {
		return nil, errors.New("APNs private key must be a P-256 PKCS8 signing key")
	}
	auth := &token.Token{AuthKey: key, KeyID: c.KeyID, TeamID: c.TeamID}
	// The library refreshes the token after 50 minutes but discards errors in
	// its lazy helper, so validate the first generation here.
	if _, err = auth.Generate(); err != nil {
		return nil, errors.New("unable to sign an APNs provider token")
	}
	transport := &http2.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		ReadIdleTimeout: 15 * time.Second,
		PingTimeout:     10 * time.Second,
		DialTLSContext: func(ctx context.Context, network, address string, cfg *tls.Config) (net.Conn, error) {
			dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}, Config: cfg}
			return dialer.DialContext(ctx, network, address)
		},
	}
	client := apns2.NewTokenClient(auth)
	client.HTTPClient = &http.Client{
		Timeout:       20 * time.Second,
		Transport:     &boundedTransport{base: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	switch c.Environment {
	case "development":
		client.Development()
	case "production":
		client.Production()
	default:
		return nil, errors.New("invalid APNs environment")
	}
	return &Client{client: client, config: c}, nil
}

// Close releases idle provider connections.
func (c *Client) Close() { c.client.CloseIdleConnections() }

// apsAlert, apsPayload and notification mirror the payload in the protocol.
// Struct fields keep the JSON key order stable.
type apsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// apsPayload is the aps dictionary. Sound is empty for passive moments, so APNs
// stays silent for them.
type apsPayload struct {
	Alert             apsAlert `json:"alert"`
	Sound             string   `json:"sound,omitempty"`
	MutableContent    int      `json:"mutable-content"`
	Category          string   `json:"category,omitempty"`
	ThreadID          string   `json:"thread-id,omitempty"`
	InterruptionLevel string   `json:"interruption-level,omitempty"`
}

// yansuPayload holds the routing keys. A brief and a needs_you push carry eid;
// a moment carries the notice id, the device id and the sealed text (PROTOCOL.md
// section 9 and 14.4). Kind is the moment kind for a moment.
type yansuPayload struct {
	Kind string `json:"kind"`
	Hub  string `json:"hub"`
	EID  string `json:"eid,omitempty"`
	ID   string `json:"id,omitempty"`
	Dev  string `json:"dev,omitempty"`
	CT   string `json:"ct,omitempty"`
}

type notification struct {
	Aps   apsPayload   `json:"aps"`
	Yansu yansuPayload `json:"yansu"`
}

// withdrawNotification is the background push that takes delivered
// notifications away (PROTOCOL.md 9.4): no alert, only the ids.
type withdrawNotification struct {
	Aps struct {
		ContentAvailable int `json:"content-available"`
	} `json:"aps"`
	Yansu struct {
		Kind string   `json:"kind"`
		Hub  string   `json:"hub"`
		IDs  []string `json:"ids"`
	} `json:"yansu"`
}

func withdrawPayloadFor(claim push.Claim) (withdrawNotification, error) {
	ids := push.WithdrawIDs(claim.Notice.CT)
	if !push.ValidWithdrawIDs(ids) {
		return withdrawNotification{}, push.ErrInvalidNotice
	}
	var n withdrawNotification
	n.Aps.ContentAvailable = 1
	n.Yansu.Kind, n.Yansu.Hub, n.Yansu.IDs = push.KindWithdraw, claim.HubID, ids
	return n, nil
}

// readyNotification is the background push that wakes a phone to fetch the
// brief just stored for it (PROTOCOL.md 9.5): no alert, no sound, and nothing
// about the brief, only the hub it is from.
type readyNotification struct {
	Aps struct {
		ContentAvailable int `json:"content-available"`
	} `json:"aps"`
	Yansu struct {
		Kind string `json:"kind"`
		Hub  string `json:"hub"`
	} `json:"yansu"`
}

func readyPayloadFor(claim push.Claim) readyNotification {
	var n readyNotification
	n.Aps.ContentAvailable = 1
	n.Yansu.Kind, n.Yansu.Hub = push.KindReady, claim.HubID
	return n
}

type pushGrantAps struct {
	ContentAvailable int `json:"content-available"`
}

type pushGrantIsoform struct {
	Kind      string `json:"kind"`
	HubID     string `json:"hubId"`
	HubPub    string `json:"hubPub"`
	Grant     string `json:"grant"`
	ExpiresAt string `json:"expiresAt"`
}

type pushGrantPayload struct {
	Aps     pushGrantAps     `json:"aps"`
	Isoform pushGrantIsoform `json:"isoform"`
}

func pushGrantPayloadFor(hubID, hubPub, grant string, expires time.Time) pushGrantPayload {
	return pushGrantPayload{
		Aps: pushGrantAps{ContentAvailable: 1},
		Isoform: pushGrantIsoform{
			Kind: "push_grant", HubID: hubID, HubPub: hubPub, Grant: grant,
			ExpiresAt: expires.UTC().Format(time.RFC3339),
		},
	}
}

// notificationFor builds the payload for a claim. The alert text and yansu.kind
// both come from the stored push kind. A kind with no known text is a permanent
// failure, so it is never sent.
func notificationFor(claim push.Claim) (notification, error) {
	switch claim.Kind {
	case push.KindBrief:
		return legacyNotification(claim, "Your brief is ready."), nil
	case push.KindNeedsYou:
		n := legacyNotification(claim, "Isoform needs you.")
		n.Aps.InterruptionLevel = "time-sensitive"
		return n, nil
	case push.KindMoment:
		return momentNotification(claim)
	default:
		return notification{}, push.ErrUnknownKind
	}
}

// legacyNotification builds the brief and needs_you payloads, which are unchanged
// from release 1 and 2.
func legacyNotification(claim push.Claim, body string) notification {
	return notification{
		Aps:   apsPayload{Alert: apsAlert{Title: "Isoform", Body: body}, Sound: "default", MutableContent: 1},
		Yansu: yansuPayload{Kind: claim.Kind, Hub: claim.HubID, EID: claim.EventID},
	}
}

// momentNotification builds a moment payload. The alert text is the fixed
// fallback for the moment kind, shown when the phone cannot decrypt the notice.
func momentNotification(claim push.Claim) (notification, error) {
	notice := claim.Notice
	if notice.ID == "" || notice.CT == "" {
		return notification{}, errIncompleteNotice
	}
	n := notification{
		Aps: apsPayload{
			Alert:             apsAlert{Title: "Isoform", Body: momentFallbackText(notice.Moment)},
			MutableContent:    1,
			Category:          notice.Category,
			ThreadID:          claim.HubID,
			InterruptionLevel: notice.Level,
		},
		Yansu: yansuPayload{Kind: notice.Moment, Hub: claim.HubID, ID: notice.ID, Dev: claim.DeviceID, CT: notice.CT},
	}
	switch notice.Level {
	case push.LevelActive, push.LevelTimeSensitive:
		n.Aps.Sound = "default"
	}
	return n, nil
}

// momentFallbackText is the alert body for each moment kind. It is shown when
// the notification extension cannot decrypt the notice, so it names no content.
func momentFallbackText(moment string) string {
	switch moment {
	case "decision_due":
		return "Isoform needs a decision."
	case "commitment_slipping":
		return "A commitment needs attention."
	case "meeting_ended":
		return "Your meeting notes are ready."
	case "meeting_prep":
		return "A meeting is about to start."
	case "calendar_conflict":
		return "Something on your calendar needs a look."
	case "meeting_followup":
		return "A meeting left you follow-ups."
	case "person_reappeared":
		return "Someone you know came up."
	case "goal_check_in":
		return "A quick check on a goal."
	case "work_finished":
		return "Work finished."
	case "work_blocked":
		return "Work is waiting on you."
	case "learned_to_confirm":
		return "Isoform learned something."
	case "capture_paused":
		return "Isoform stopped watching for a while."
	case "brief_missed":
		return "Your brief did not arrive."
	case "weekly_recap":
		return "Your week is ready."
	case "chat_answer":
		return "Isoform answered your messages."
	case "schedule_missed":
		return "A schedule did not run."
	case "suggestion":
		return "Isoform has a suggestion."
	case "waiting_on_reply":
		return "Someone is waiting on your reply."
	case "playbook_suggested":
		return "Isoform can take a routine off your hands."
	case "playbook_result":
		return "A playbook finished."
	case "topic_finds":
		return "Something new on a topic you follow."
	default:
		return "Isoform has something for you."
	}
}

// priorityFor maps a moment's level to apns-priority. Brief and needs_you keep
// the immediate priority they always had.
func priorityFor(claim push.Claim) int {
	if claim.Kind == push.KindMoment && claim.Notice.Level == push.LevelPassive {
		return apns2.PriorityLow
	}
	return apns2.PriorityHigh
}

// collapseFor returns apns-collapse-id. A moment collapses on its notice id
// unless the hub supplied a collapse id. Other kinds collapse on the APNs id.
func collapseFor(claim push.Claim) string {
	if claim.Kind == push.KindMoment {
		if claim.Notice.Collapse != "" {
			return claim.Notice.Collapse
		}
		return claim.Notice.ID
	}
	return claim.NotificationID
}

// Send reports provider acceptance, never delivery or user acknowledgement.
func (c *Client) Send(ctx context.Context, claim push.Claim) (push.Outcome, error) {
	if claim.Environment != c.config.Environment {
		return push.Permanent, errors.New("push registration does not match the APNs environment")
	}
	var payload any
	pushType, priority, collapse := apns2.PushTypeAlert, priorityFor(claim), collapseFor(claim)
	switch claim.Kind {
	case push.KindWithdraw:
		withdraw, err := withdrawPayloadFor(claim)
		if err != nil {
			return push.Permanent, err
		}
		// A background push: no alert, low priority, never collapsed.
		payload, pushType, priority, collapse = withdraw, apns2.PushTypeBackground, apns2.PriorityLow, ""
	case push.KindReady:
		// A background wake, like withdraw: Apple requires priority 5 for it.
		payload, pushType, priority, collapse = readyPayloadFor(claim), apns2.PushTypeBackground, apns2.PriorityLow, ""
	default:
		alert, err := notificationFor(claim)
		if err != nil {
			return push.Permanent, err
		}
		payload = alert
	}
	if encoded, err := json.Marshal(payload); err != nil || len(encoded) > push.MaxPayloadBytes {
		return push.Permanent, errPayloadTooLarge
	}
	response, err := c.client.PushWithContext(ctx, &apns2.Notification{
		DeviceToken: claim.Token,
		Topic:       c.config.Topic,
		ApnsID:      claim.NotificationID,
		CollapseID:  collapse,
		Priority:    priority,
		PushType:    pushType,
		Expiration:  claim.ExpiresAt,
		Payload:     payload,
	})
	if err != nil {
		// Transport errors can contain the device token in a URL. Never return it.
		return push.Retry, errors.New("APNs request could not be confirmed")
	}
	if response == nil {
		return push.Retry, errors.New("APNs returned no response")
	}
	if response.StatusCode == http.StatusForbidden ||
		(response.StatusCode == http.StatusBadRequest &&
			(response.Reason == "BadTopic" || response.Reason == "TopicDisallowed" || response.Reason == "MissingTopic")) {
		return push.Retry, &configurationError{}
	}
	if response.StatusCode == http.StatusTooManyRequests && response.Reason == "TooManyProviderTokenUpdates" {
		return push.Retry, &providerCooldownError{}
	}
	outcome := classify(response, claim.RegisteredAt)
	if outcome != push.Accepted {
		return outcome, fmt.Errorf("APNs did not accept the notification (HTTP %d)", response.StatusCode)
	}
	return outcome, nil
}

func (c *Client) SendGrant(ctx context.Context, deviceToken, environment, hubID, hubPub, grant string, expires time.Time) (push.Outcome, error) {
	if environment != c.config.Environment {
		return push.Permanent, errors.New("push registration does not match the APNs environment")
	}
	payload := pushGrantPayloadFor(hubID, hubPub, grant, expires)
	if encoded, err := json.Marshal(payload); err != nil || len(encoded) > push.MaxPayloadBytes {
		return push.Permanent, errPayloadTooLarge
	}
	now := time.Now()
	response, err := c.client.PushWithContext(ctx, &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       c.config.Topic,
		PushType:    apns2.PushTypeBackground,
		Priority:    apns2.PriorityLow,
		Expiration:  now.Add(time.Hour),
		Payload:     payload,
	})
	if err != nil {
		return push.Retry, errors.New("APNs grant request could not be confirmed")
	}
	if response == nil {
		return push.Retry, errors.New("APNs returned no grant response")
	}
	if response.StatusCode == http.StatusForbidden ||
		(response.StatusCode == http.StatusBadRequest &&
			(response.Reason == "BadTopic" || response.Reason == "TopicDisallowed" || response.Reason == "MissingTopic")) {
		return push.Retry, &configurationError{}
	}
	if response.StatusCode == http.StatusTooManyRequests && response.Reason == "TooManyProviderTokenUpdates" {
		return push.Retry, &providerCooldownError{}
	}
	outcome := classify(response, now)
	if outcome != push.Accepted {
		return outcome, errors.New("APNs did not accept the grant push")
	}
	return outcome, nil
}

func classify(response *apns2.Response, registeredAt time.Time) push.Outcome {
	if response == nil {
		return push.Retry
	}
	switch response.StatusCode {
	case http.StatusOK:
		if response.Reason == "" {
			return push.Accepted
		}
		return push.Permanent
	case http.StatusGone:
		// apns2 truncates the timestamp to seconds, so compare conservatively.
		if (response.Reason == "Unregistered" || response.Reason == "ExpiredToken") &&
			!response.Timestamp.IsZero() && response.Timestamp.After(registeredAt) {
			return push.TokenInvalid
		}
		return push.Permanent
	case http.StatusBadRequest:
		if response.Reason == "BadDeviceToken" || response.Reason == "DeviceTokenNotForTopic" {
			return push.TokenInvalid
		}
		return push.Permanent
	case http.StatusTooManyRequests, http.StatusRequestTimeout:
		return push.Retry
	default:
		if response.StatusCode >= 500 && response.StatusCode <= 599 {
			return push.Retry
		}
		return push.Permanent
	}
}

// boundedTransport bounds the provider response before the library decodes it.
type boundedTransport struct{ base http.RoundTripper }

func (t *boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ProtoMajor != 2 {
		response.Body.Close()
		return nil, errors.New("APNs requires HTTP/2")
	}
	response.Body = &boundedBody{body: response.Body, remaining: 16384}
	return response, nil
}

func (t *boundedTransport) CloseIdleConnections() {
	if transport, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

type boundedBody struct {
	body      io.ReadCloser
	remaining int
}

func (b *boundedBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		return 0, errors.New("APNs response exceeds limit")
	}
	if len(buffer) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	n, err := b.body.Read(buffer)
	b.remaining -= n
	return n, err
}

func (b *boundedBody) Close() error { return b.body.Close() }
