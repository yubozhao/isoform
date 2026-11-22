// Package httpapi serves the device HTTP API and the hub WebSocket route. Every
// response uses the {ok,data,error} envelope with Cache-Control: no-store.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"isoform-relay/internal/email"
	"isoform-relay/internal/hub"
	"isoform-relay/internal/limit"
	"isoform-relay/internal/notify"
	"isoform-relay/internal/push"
	"isoform-relay/internal/sms"
	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

const (
	protocolVersion = 1
	// longPollMax caps every long-poll wait.
	longPollMax = 25 * time.Second
	// deviceRequestsPerMinute limits one device's authenticated requests.
	// Opening the app sends about fifty requests in half a minute, and each
	// one costs a long-poll and an acknowledgement on top. At 60 the app's own
	// reads used the minute up in eleven seconds on 2026-11-20, and the relay
	// refused the request that hands a recorded part to the computer.
	deviceRequestsPerMinute = 600
	// claimAttempts limits claims per pairing and per client in claimWindow.
	claimAttempts = 10
	claimWindow   = 10 * time.Minute
	// responsePageSize bounds one long-poll response.
	responsePageSize = 50
	// authFailuresPerAddress is how many credentials one address block may
	// present in vain per authFailureWindow before every attempt is refused.
	authFailuresPerAddress = 30
	authFailureWindow      = time.Minute
	// inflightPerAddress caps the requests one address holds open at once,
	// long-polls and hub sockets included, so one client cannot hold the
	// server's connections. Carrier NAT shares an address between many
	// phones, hence the generous count.
	inflightPerAddress = 128
)

// Handler routes requests. Store, Hubs, Notify, Push and Now are set by New.
// Tests may replace Now to exercise expiry. SMS is set when a text message
// provider is configured; without it the text routes answer 503. Email is
// the same for the email routes.
type Handler struct {
	Store  *store.Store
	Hubs   *hub.Manager
	Notify *notify.Notifier
	Push   push.Configuration
	Now    func() time.Time
	SMS    *sms.Service
	Email  *email.Service

	limits   *limit.Limiter
	upgrader websocket.Upgrader
	routes   map[string]map[string]http.HandlerFunc

	inflightMu sync.Mutex
	inflight   map[string]int
}

// New returns a handler wired to the store, hub manager and notifier.
func New(st *store.Store, hubs *hub.Manager, n *notify.Notifier, pushConfig push.Configuration) *Handler {
	h := &Handler{
		Store:    st,
		Hubs:     hubs,
		Notify:   n,
		Push:     pushConfig,
		Now:      time.Now,
		limits:   limit.New(),
		upgrader: websocket.Upgrader{HandshakeTimeout: 10 * time.Second},
		inflight: map[string]int{},
	}
	h.routes = map[string]map[string]http.HandlerFunc{
		"/v1/health":        {http.MethodGet: h.health},
		"/v1/hub/ws":        {http.MethodGet: h.hubSocket},
		"/v1/presence":      {http.MethodGet: h.presence},
		"/v1/brief/latest":  {http.MethodGet: h.latestBrief},
		"/v1/briefs":        {http.MethodGet: h.listBriefs},
		"/v1/requests":      {http.MethodPost: logRequest(h.postRequest)},
		"/v1/responses":     {http.MethodGet: h.responses},
		"/v1/responses/ack": {http.MethodPost: h.ackResponses},
		"/v1/push/token":    {http.MethodPut: h.pushToken, http.MethodDelete: h.pushToken},
		"/v1/devices/me":    {http.MethodDelete: h.unpair},
		"/v1/uploads":       {http.MethodGet: h.listUploads},
		"/v1/hub/sms/subscription": {
			http.MethodGet: h.smsSubscription, http.MethodPost: h.smsSubscription, http.MethodDelete: h.smsSubscription,
		},
		"/v1/hub/sms/subscription/confirm": {http.MethodPost: h.smsConfirm},
		"/v1/hub/sms/enable":               {http.MethodPost: h.smsEnable},
		"/v1/hub/sms/send":                 {http.MethodPost: h.smsSend},
		"/v1/sms/inbound":                  {http.MethodPost: h.smsInbound},
		"/v1/hub/email/subscription": {
			http.MethodGet: h.emailSubscription, http.MethodPost: h.emailSubscription, http.MethodDelete: h.emailSubscription,
		},
		"/v1/hub/email/subscription/confirm": {http.MethodPost: h.emailConfirm},
		"/v1/hub/email/send":                 {http.MethodPost: h.emailSend},
	}
	return h
}

// ServeHTTP refuses browser origins and addresses with too many open
// requests, then routes by exact path and method.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Origin")) > 0 {
		refuse(w, r, http.StatusForbidden, "Browser origins are not supported")
		return
	}
	address := clientIP(r)
	if !h.acquire(address) {
		setRetryAfter(w, time.Second)
		h.logLimit(w, r, "inflight_address", inflightPerAddress, inflightPerAddress)
		reply(w, http.StatusTooManyRequests, nil, "Too many open requests")
		return
	}
	defer h.release(address)
	if methods, ok := h.routes[r.URL.Path]; ok {
		handler, allowed := methods[r.Method]
		if !allowed {
			reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
			return
		}
		handler(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/pairings/") {
		h.pairingRoute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/notices/") {
		h.noticeRoute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/hub/attachments/") {
		h.hubAttachmentRoute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/attachments/") {
		h.attachmentRoute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/uploads/") {
		logUpload(w, r, "/v1/uploads/", h.uploadRoute)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/hub/uploads/") {
		logUpload(w, r, "/v1/hub/uploads/", h.hubUploadRoute)
		return
	}
	reply(w, http.StatusNotFound, nil, "Route not found")
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"protocolVersion": protocolVersion, "service": "isoform-relay", "status": "ok", "uploadPieces": true, "uploadPiecesAnyOrder": true, "uploadList": true, "uploadsAsText": true, "attachmentsAsText": true}, "")
}

// envelope is the {ok,data,error} response body.
type envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func reply(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{OK: status < 400, Data: data, Error: message})
}

// decodeBody reads one JSON object of at most 64 KiB with a JSON content type.
// It writes the error response itself and reports whether decoding succeeded.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		reply(w, http.StatusUnsupportedMediaType, nil, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, wire.MaxBodyBytes)
	if err = wire.DecodeStrict(r.Body, target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reply(w, http.StatusRequestEntityTooLarge, nil, "Request body exceeds 64 KiB")
		} else {
			reply(w, http.StatusBadRequest, nil, "Invalid JSON request")
		}
		return false
	}
	return true
}

// limitRoute returns only registered paths and templates, even when the
// in-flight check rejects a request before routing.
func (h *Handler) limitRoute(r *http.Request) string {
	path := r.URL.Path
	if _, ok := h.routes[path]; ok {
		return path
	}
	for _, prefix := range refusalPrefixes {
		if tail, ok := strings.CutPrefix(path, prefix); ok {
			_, suffix, nested := strings.Cut(tail, "/")
			if !nested {
				return prefix + "{id}"
			}
			switch prefix + suffix {
			case "/v1/uploads/pieces", "/v1/pairings/claim", "/v1/notices/receipts":
				return prefix + "{id}/" + suffix
			}
			return "unknown"
		}
	}
	return "unknown"
}

// Fixed-window buckets stop counting at their cap.
func (h *Handler) allow(w http.ResponseWriter, r *http.Request, name, subject string, cap int, window time.Duration, now time.Time) bool {
	ok, retry := h.limits.Allow(name+":"+subject, cap, window, now)
	if !ok {
		setRetryAfter(w, retry)
		h.logLimit(w, r, name, int64(cap), int64(cap))
	}
	return ok
}

// acquire counts one open request for address, refusing past inflightPerAddress.
func (h *Handler) acquire(address string) bool {
	h.inflightMu.Lock()
	defer h.inflightMu.Unlock()
	if h.inflight[address] >= inflightPerAddress {
		return false
	}
	h.inflight[address]++
	return true
}

func (h *Handler) release(address string) {
	h.inflightMu.Lock()
	defer h.inflightMu.Unlock()
	if h.inflight[address] <= 1 {
		delete(h.inflight, address)
		return
	}
	h.inflight[address]--
}

// openRequests is how many requests address holds open; tests read it.
func (h *Handler) openRequests(address string) int {
	h.inflightMu.Lock()
	defer h.inflightMu.Unlock()
	return h.inflight[address]
}

func authFailureKey(r *http.Request) string {
	return "auth:fail:addr:" + addressBlock(clientIP(r))
}

// authLocked reports whether the client's address block has failed to
// authenticate too often to be given another look-up; it answers 429 itself.
// A credential is a random 256-bit token, so the lock only slows a scanner,
// but it also keeps a scanner's look-ups off the store.
func (h *Handler) authLocked(w http.ResponseWriter, r *http.Request) bool {
	over, retry := h.limits.Exceeded(authFailureKey(r), authFailuresPerAddress, h.Now())
	if over {
		setRetryAfter(w, retry)
		h.logLimit(w, r, "auth_failures", authFailuresPerAddress, authFailuresPerAddress)
		reply(w, http.StatusTooManyRequests, nil, "Too many failed authentications")
	}
	return over
}

// authFailed counts a failed authentication against the client's address
// block and answers 401 with message.
func (h *Handler) authFailed(w http.ResponseWriter, r *http.Request, message string) {
	h.limits.Allow(authFailureKey(r), authFailuresPerAddress, authFailureWindow, h.Now())
	refuse(w, r, http.StatusUnauthorized, message)
}

// bearerToken extracts a well-formed bearer credential from an Authorization header.
func bearerToken(header string) (string, bool) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	return token, ok && wire.ValidToken(token)
}

// authDevice resolves the bearer, enforces scope and the per-device rate limit,
// and records activity. It writes the error response itself.
func (h *Handler) authDevice(w http.ResponseWriter, r *http.Request, scope string) (store.Device, bool) {
	return h.authDeviceWithin(w, r, scope, "device_rate", deviceRequestsPerMinute)
}

// authDeviceWithin is authDevice with the request counted in the named budget.
// The upload routes keep their own: a recording goes up as many small pieces,
// and counted with everything else they used up the minute in seconds, so the
// relay refused the rest of the pieces and the app's own requests with them.
func (h *Handler) authDeviceWithin(w http.ResponseWriter, r *http.Request, scope, budget string, perMinute int) (store.Device, bool) {
	if h.authLocked(w, r) {
		return store.Device{}, false
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		h.authFailed(w, r, "Device authentication required")
		return store.Device{}, false
	}
	now := h.Now()
	device, err := h.Store.DeviceByBearer(r.Context(), wire.Digest(token), now)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrUnauthorized):
		h.authFailed(w, r, "Device authentication required")
		return store.Device{}, false
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to authenticate device")
		return store.Device{}, false
	}
	if scope != "" && !device.Has(scope) {
		refuse(w, r, http.StatusForbidden, "Device lacks the required scope")
		return store.Device{}, false
	}
	if !h.allow(w, r, budget, device.ID, perMinute, time.Minute, now) {
		reply(w, http.StatusTooManyRequests, nil, "Too many requests")
		return store.Device{}, false
	}
	_ = h.Store.TouchDevice(r.Context(), device.ID, now)
	return device, true
}

// clientIP is the peer address. A forwarded address is honored only when the
// peer is loopback or private, which is where a same-host proxy connects from.
// A request from an internet peer keeps its own address, whatever header it sends.
// trustedProxies are the peers whose X-Forwarded-For is believed; see
// config.Config.TrustedProxies. SetTrustedProxies replaces the default.
var trustedProxies = []*net.IPNet{
	{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
}

// SetTrustedProxies sets the peers whose X-Forwarded-For names the client.
func SetTrustedProxies(networks []*net.IPNet) {
	if len(networks) > 0 {
		trustedProxies = networks
	}
}

// DisableForwardedClientIP makes every request use its peer address.
func DisableForwardedClientIP() {
	trustedProxies = nil
}

// clientIPHeader is the header a trusted proxy names the client in. Fly's
// edge appends its own address to X-Forwarded-For and sets Fly-Client-IP.
var clientIPHeader = "X-Forwarded-For"

// SetClientIPHeader replaces the header a trusted proxy names the client in.
func SetClientIPHeader(name string) {
	if name != "" {
		clientIPHeader = name
	}
}

func trustedProxy(ip net.IP) bool {
	for _, network := range trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the address limits count by: the peer, or, when the peer is a
// trusted proxy, the last address it put in the client header. Any other
// peer, a private one included, may set the header itself, so it is ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	if trustedProxy(ip) {
		forwarded := r.Header.Get(clientIPHeader)
		if i := strings.LastIndex(forwarded, ","); i >= 0 {
			forwarded = forwarded[i+1:]
		}
		if parsed := net.ParseIP(strings.TrimSpace(forwarded)); parsed != nil {
			return parsed.String()
		}
	}
	return ip.String()
}

// ClientIP returns the peer address or the address named by a trusted proxy.
func ClientIP(r *http.Request) string { return clientIP(r) }

func setRetryAfter(w http.ResponseWriter, wait time.Duration) {
	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

// parseUint reads one decimal query value no larger than max.
func parseUint(values []string, max uint64) (uint64, bool) {
	if len(values) != 1 {
		return 0, false
	}
	parsed, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil || parsed > max {
		return 0, false
	}
	return parsed, true
}

// storeCallTimeout is how long one store call from an HTTP handler may take.
var storeCallTimeout = 5 * time.Second

// storeContext bounds one store call from an HTTP handler. It is made right
// before the call: time spent reading a request's body is not the store's.
func storeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, storeCallTimeout)
}
