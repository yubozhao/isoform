package main

// The computer's end of the Isoform front (docs/design/mcp-public-front.md).
// A cloud agent's TLS connection reaches this computer through the front
// and isoform-front-link (front/, Rust) still encrypted; TLS ends here, on a
// loopback listener, with this computer's own certificate for its
// <label>.agents.isoform.ai hostname. The certificate comes from an ACME CA
// by TLS-ALPN-01, which travels through the same passthrough, and its key
// never leaves the computer. Behind TLS: task links (/t/), later MCP.
//
// While an address join or connection needs it, the computer accepts only Isoform's
// front (frontDefaultIDs; ISOFORM_FRONT_IDS overrides it for development)
// and gets its certificate from Let's Encrypt (ISOFORM_FRONT_ACME_DIRECTORY
// set to "staging" picks the staging CA for testing).

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"wails-gui/internal/remoteaccess"
)

const (
	frontEnabledKey = "front_enabled"
	frontDirName    = "front"
	// frontLinkBinEnv overrides the bundled isoform-front-link.
	frontLinkBinEnv = "ISOFORM_FRONT_LINK_BIN"
	// frontIDsEnv replaces the front endpoint ids the link accepts.
	frontIDsEnv = "ISOFORM_FRONT_IDS"
	// frontACMEDirectoryEnv picks the CA: empty or "production" is Let's
	// Encrypt, "staging" its staging CA, else an https directory URL.
	frontACMEDirectoryEnv = "ISOFORM_FRONT_ACME_DIRECTORY"
	frontSuffixEnv        = "ISOFORM_FRONT_SUFFIX"
	frontDefaultSuffix    = "agents.isoform.ai"
	frontACMEProduction   = "https://acme-v02.api.letsencrypt.org/directory"
	frontACMEStaging      = "https://acme-staging-v02.api.letsencrypt.org/directory"
	// Each CA keeps its own certificate cache, so a staging certificate from
	// testing is never served once production is on. The CT watch reads the
	// production one.
	frontACMECacheProduction = "acme-letsencrypt"
	frontACMECacheStaging    = "acme"
	frontACMECacheCustom     = "acme-custom"
	// frontCertBackoff: after a failed issuance, no new attempt for this
	// long. Let's Encrypt allows 5 failed validations an hour and 5
	// certificates per name a week, so the computer must never loop.
	frontCertBackoff     = time.Hour
	frontLinkReadyWithin = 30 * time.Second
	frontLinkRestartWait = 30 * time.Second
)

var (
	errFrontNoIDs   = errors.New("no front ids are set")
	errFrontBadLink = errors.New("the front link did not report a hostname")
	frontLabelRE    = regexp.MustCompile(`^[a-z2-7]{52}$`)
	frontIDRE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// frontLinkInfo is the link's ready line.
type frontLinkInfo struct {
	EndpointID string `json:"endpointId"`
	Label      string `json:"label"`
	Hostname   string `json:"hostname"`
}

// parseFrontLinkLine checks the link's ready line: a 52-character label
// that, under the suffix, is exactly the hostname.
func parseFrontLinkLine(line, suffix string) (frontLinkInfo, error) {
	var info frontLinkInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &info); err != nil {
		return frontLinkInfo{}, errFrontBadLink
	}
	if !frontIDRE.MatchString(info.EndpointID) || !frontLabelRE.MatchString(info.Label) || info.Hostname != info.Label+"."+suffix {
		return frontLinkInfo{}, errFrontBadLink
	}
	return info, nil
}

// frontCertSource hands out the certificate and refuses to ask the CA again
// for frontCertBackoff after a failure.
type frontCertSource struct {
	mu       sync.Mutex
	get      func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	failedAt time.Time
	lastErr  error
	now      func() time.Time
}

func (c *frontCertSource) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *frontCertSource) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// The ACME challenge handshake itself must always be answered.
	isChallenge := len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto
	c.mu.Lock()
	if !isChallenge && !c.failedAt.IsZero() && c.clock().Sub(c.failedAt) < frontCertBackoff {
		err := c.lastErr
		c.mu.Unlock()
		return nil, fmt.Errorf("certificate unavailable, retrying after a pause: %w", err)
	}
	c.mu.Unlock()
	cert, err := c.get(hello)
	if isChallenge {
		return cert, err
	}
	c.mu.Lock()
	if err != nil {
		c.failedAt, c.lastErr = c.clock(), err
	} else {
		c.failedAt, c.lastErr = time.Time{}, nil
	}
	c.mu.Unlock()
	return cert, err
}

// frontState is the app's front service. Zero value is ready.
type frontState struct {
	// lifecycle serializes resource ownership, expiry, retry and shutdown.
	lifecycle      sync.Mutex
	expiry         *time.Timer
	restart        *time.Timer
	shutdown       bool
	certRetryAfter time.Time
	certManager    *autocert.Manager // reuse the same renewal timer across open/close cycles
	certHostname   string

	mu       sync.Mutex
	running  bool
	exited   bool // the supervisor observed the current link exit during startup
	cmd      *exec.Cmd
	server   *http.Server
	listener net.Listener
	info     frontLinkInfo
	ctCancel context.CancelFunc
	// test seams
	restartWait time.Duration
	ctAPI       string
	dir         string
	certFunc    func(hostname string, cacheDir string) func(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

func (s *frontState) dirPath() (string, error) {
	if s.dir != "" {
		return s.dir, nil
	}
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, frontDirName), nil
}

func frontSuffix() string {
	if s := strings.Trim(strings.ToLower(strings.TrimSpace(os.Getenv(frontSuffixEnv))), "."); s != "" {
		return s
	}
	return frontDefaultSuffix
}

// frontDefaultIDs are the endpoint ids of Isoform's front (Fly app
// isoform-front, docs/front-deploy.md). They are public: an id is what a
// computer checks before it accepts a connection. Add a machine's id here
// before it serves.
var frontDefaultIDs = []string{"a41cefc7ad49deeb5b55c5776cb5b5a4af5d901ea5c8e4da69eb07a5a80e8b34"}

// frontAllowedIDs are the fronts the link accepts: ISOFORM_FRONT_IDS when
// set, else Isoform's own.
func frontAllowedIDs() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(frontIDsEnv))
	if raw == "" {
		raw = strings.Join(frontDefaultIDs, ",")
	}
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if !frontIDRE.MatchString(id) {
			return nil, fmt.Errorf("%s: %q is not an endpoint id", frontIDsEnv, id)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errFrontNoIDs
	}
	return ids, nil
}

func locateFrontLink() (string, error) {
	return remoteaccess.LocateBundledBinary(frontLinkBinEnv, "front-link-bundle", "isoform-front-link", "isoform-front-link", "isoform-front-link")
}

// frontHostname is the public hostname while the front runs, else "".
func (a *App) frontHostname() string {
	a.front.mu.Lock()
	defer a.front.mu.Unlock()
	if !a.front.running {
		return ""
	}
	return a.front.info.Hostname
}

// startFront starts the TLS listener and the link. It is idempotent.
func (a *App) startFront() error {
	if time.Now().Before(a.front.certRetryAfter) {
		return errors.New("front certificate unavailable, retry after the issuance pause")
	}
	ids, err := frontAllowedIDs()
	if err != nil {
		return err
	}
	s := &a.front
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	a.stopFront()

	dir, err := s.dirPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("front TLS listener: %w", err)
	}
	info, cmd, err := a.startFrontLink(dir, ids, ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return err
	}
	_, cacheName := frontACMEDirectory()
	get := a.frontCertGetter(info.Hostname, filepath.Join(dir, cacheName))
	certs := &frontCertSource{get: get}
	tlsConfig := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: certs.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
	}
	server := &http.Server{
		Handler:           frontHostOnly(info.Hostname, markAddressRequests(a.agentMux())),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctCtx, ctCancel := context.WithCancel(context.Background())
	watch := &frontCTWatch{hostname: info.Hostname, dir: dir, api: s.ctAPI, notify: func(title, body, click string) { a.NotifyUser(title, body, click) }, language: normalizeUILanguage(a.currentUILanguage())}
	s.mu.Lock()
	s.exited = false
	s.running, s.cmd, s.server, s.listener, s.info, s.ctCancel = false, cmd, server, ln, info, ctCancel
	s.mu.Unlock()
	go a.runFrontCTWatch(ctCtx, watch)
	go func() {
		if err := server.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[front] TLS server stopped: %v", err)
		}
	}()
	go a.superviseFrontLink(cmd)
	// The TLS listener must already serve the CA's ALPN challenge. Wait for
	// issuance before publishing a join link or reporting the hostname ready.
	_, err = certs.GetCertificate(&tls.ClientHelloInfo{ServerName: info.Hostname, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}})
	if err != nil {
		a.front.certRetryAfter = time.Now().Add(frontCertBackoff)
		a.stopFront()
		return fmt.Errorf("front certificate: %w", err)
	}
	s.mu.Lock()
	if s.cmd != cmd || s.exited {
		s.mu.Unlock()
		a.stopFront()
		return errors.New("front link exited during startup")
	}
	s.running = true
	s.mu.Unlock()
	log.Printf("[front] serving %s", info.Hostname)
	return nil
}

// markAddressRequests marks every request the front serves as one that came
// in through the agent address.
func markAddressRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, withAddressRequest(r))
	})
}

// frontHostOnly answers only requests for the computer's own hostname.
func frontHostOnly(hostname string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !strings.EqualFold(host, hostname) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// frontACMEDirectory is the CA to ask and the cache folder its certificates
// live in.
func frontACMEDirectory() (string, string) {
	switch value := strings.TrimSpace(os.Getenv(frontACMEDirectoryEnv)); strings.ToLower(value) {
	case "", "production":
		return frontACMEProduction, frontACMECacheProduction
	case "staging":
		return frontACMEStaging, frontACMECacheStaging
	default:
		if value == frontACMEStaging {
			return frontACMEStaging, frontACMECacheStaging
		}
		if value == frontACMEProduction {
			return frontACMEProduction, frontACMECacheProduction
		}
		return value, frontACMECacheCustom
	}
}

func (a *App) frontCertGetter(hostname, cacheDir string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if a.front.certFunc != nil {
		return a.front.certFunc(hostname, cacheDir)
	}
	directory, _ := frontACMEDirectory()
	if m := a.front.certManager; m != nil && a.front.certHostname == hostname && m.Cache == autocert.DirCache(cacheDir) && m.Client.DirectoryURL == directory {
		return m.GetCertificate
	}
	m := &autocert.Manager{
		// openAgentAddress verifies the recorded acceptance before startup.
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(hostname),
		Client:     &acme.Client{DirectoryURL: directory},
	}
	a.front.certManager, a.front.certHostname = m, hostname
	return m.GetCertificate
}

// startFrontLink runs isoform-front-link and waits for its ready line.
func (a *App) startFrontLink(dir string, ids []string, forward string) (frontLinkInfo, *exec.Cmd, error) {
	bin, err := locateFrontLink()
	if err != nil {
		return frontLinkInfo{}, nil, fmt.Errorf("isoform-front-link: %w", err)
	}
	suffix := frontSuffix()
	cmd := exec.Command(bin,
		"--key-file", filepath.Join(dir, "link.key"),
		"--allow", strings.Join(ids, ","),
		"--forward", forward,
		"--suffix", suffix,
	)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return frontLinkInfo{}, nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return frontLinkInfo{}, nil, err
	}
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
		for sc.Scan() {
		}
	}()
	select {
	case line, ok := <-lines:
		if ok {
			info, err := parseFrontLinkLine(line, suffix)
			if err == nil {
				return info, cmd, nil
			}
		}
	case <-time.After(frontLinkReadyWithin):
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return frontLinkInfo{}, nil, errFrontBadLink
}

// superviseFrontLink restarts the link after a pause if it exits while the
// front should be running.
func (a *App) superviseFrontLink(cmd *exec.Cmd) {
	err := cmd.Wait()
	s := &a.front
	s.mu.Lock()
	if s.cmd != cmd {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.exited = true
	s.mu.Unlock()
	s.lifecycle.Lock()
	s.mu.Lock()
	current := s.cmd == cmd
	s.mu.Unlock()
	defer s.lifecycle.Unlock()
	if current {
		a.stopFront()
		log.Printf("[front] link exited: %v", err)
		a.scheduleFrontRestart()
	}
}

// Called under lifecycle. Recheck demand at every attempt, including after
// certificate issuance backoff. A disconnection can cancel the pending retry.
func (a *App) scheduleFrontRestart() {
	s := &a.front
	if s.shutdown || !a.agentJoinService().NeedsAddress() {
		return
	}
	wait := s.restartWait
	if wait == 0 {
		wait = frontLinkRestartWait
	}
	wait = max(wait, time.Until(s.certRetryAfter))
	if s.restart != nil {
		s.restart.Stop()
	}
	s.restart = time.AfterFunc(wait, func() {
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		if s.shutdown || !a.agentJoinService().NeedsAddress() || a.frontHostname() != "" {
			return
		}
		if err := a.openAgentAddress(false); err != nil {
			log.Printf("[front] restart failed: %v", err)
			a.scheduleFrontRestart()
		}
	})
}

func (a *App) stopFront() {
	s := &a.front
	s.mu.Lock()
	cmd, server, ctCancel := s.cmd, s.server, s.ctCancel
	s.running, s.cmd, s.server, s.listener, s.info, s.ctCancel = false, nil, nil, nil, frontLinkInfo{}, nil
	s.mu.Unlock()
	if ctCancel != nil {
		ctCancel()
	}
	if server != nil {
		_ = server.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
