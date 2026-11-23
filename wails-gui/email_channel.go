package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"wails-gui/internal/destinations"
	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// Email through the relay (PROTOCOL.md section 19). The user verifies their
// own address with a code the relay mails to it; afterwards a playbook's
// result can go there when the user chose email for it. The relay sends only
// to the address its hub verified: a send names no recipient, so neither this
// Mac nor anything running on it can mail anyone else. As with texts, the
// relay is not trusted with the subscription's state: only a code confirmed
// on this Mac sets the address here.

const (
	emailChannelFileName    = "email-channel.json"
	emailChannelFileVersion = 1
	emailCallTimeout        = 25 * time.Second
	// emailSubjectMax and emailBodyMax are the relay's limits (PROTOCOL 19.3).
	emailSubjectMax = 150
	emailBodyMax    = 8000

	emailStateNone    = "none"
	emailStatePending = "pending_verification"
	emailStateActive  = "active"

	emailErrInvalidAddress = "invalid_address"
)

var emailCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// EmailChannelStatus is what the settings card shows. Address is the verified
// address and PendingAddress the one a code went to; the relay never returns
// either, so the Mac keeps them.
type EmailChannelStatus struct {
	Available      bool   `json:"available"`
	State          string `json:"state"`
	Address        string `json:"address"`
	PendingAddress string `json:"pendingAddress"`
	CodeExpiresAt  string `json:"codeExpiresAt"`
	SentToday      int    `json:"sentToday"`
	DailyCap       int    `json:"dailyCap"`
	LastError      string `json:"lastError"`
}

// emailChannelFile is email-channel.json (mode 0600, beside sms-channel.json).
type emailChannelFile struct {
	Version        int       `json:"version"`
	Address        string    `json:"address"`
	PendingAddress string    `json:"pendingAddress"`
	State          string    `json:"state"`
	CodeExpiresAt  time.Time `json:"codeExpiresAt"`
	SentToday      int       `json:"sentToday"`
	DailyCap       int       `json:"dailyCap"`
	CheckedAt      time.Time `json:"checkedAt"`
	LastError      string    `json:"lastError"`
}

// emailRelayStatus is the relay's status object (PROTOCOL.md 19.2).
type emailRelayStatus struct {
	State         string  `json:"state"`
	CodeExpiresAt *string `json:"codeExpiresAt"`
	SentToday     int     `json:"sentToday"`
	DailyCap      int     `json:"dailyCap"`
}

// emailRelay makes one call to the relay's email routes with the hub
// credential. mobileRelayClient implements it; refusals are *smsError, the
// relay's refusal code shape shared with the text routes.
type emailRelay interface {
	emailCall(ctx context.Context, method, path string, body any) (emailRelayStatus, error)
}

// emailChannel holds its hooks as fields so tests use a fake relay and clock.
type emailChannel struct {
	dir   string
	now   func() time.Time
	relay func() emailRelay
	lang  func() string

	callMu sync.Mutex
	fileMu sync.Mutex
}

func newEmailChannel(dir string) *emailChannel {
	return &emailChannel{
		dir:   dir,
		now:   time.Now,
		relay: func() emailRelay { return nil },
		lang:  func() string { return "en" },
	}
}

var (
	sharedEmailChannelOnce sync.Once
	sharedEmailChannelInst *emailChannel
	emailChannelConfigured sync.Once
)

// sharedEmailChannel is the app's email subscription, stored in the data
// folder. The app sets its relay and language hooks on first use.
func sharedEmailChannel() *emailChannel {
	sharedEmailChannelOnce.Do(func() {
		dir, err := textChannelDir()
		if err != nil {
			log.Printf("[email] no home directory: %v", err)
		}
		sharedEmailChannelInst = newEmailChannel(dir)
	})
	return sharedEmailChannelInst
}

// emailChannel returns the shared email channel, wired to this app's relay
// connection and interface language on first use.
func (a *App) emailChannel() *emailChannel {
	ch := sharedEmailChannel()
	emailChannelConfigured.Do(func() {
		ch.relay = a.emailRelay
		ch.lang = a.currentUILanguage
	})
	return ch
}

// emailRelay is the relay client when the iPhone connection is on, else nil.
func (a *App) emailRelay() emailRelay {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return nil
	}
	bridge.mu.Lock()
	client := bridge.client
	bridge.mu.Unlock()
	if client == nil {
		return nil
	}
	return client
}

// normalizeEmailAddress returns the bare address in lower case, or "" when it
// is not one plain address.
func normalizeEmailAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 254 || strings.ContainsAny(raw, " <>,;\"\r\n\t") {
		return ""
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil || parsed.Address != raw {
		return ""
	}
	at := strings.LastIndex(raw, "@")
	if at <= 0 || at == len(raw)-1 || !strings.Contains(raw[at+1:], ".") {
		return ""
	}
	return strings.ToLower(raw)
}

func (c *emailChannel) load() (emailChannelFile, error) {
	if c.dir == "" {
		return emailChannelFile{}, errTextChannelStorage
	}
	raw, err := os.ReadFile(filepath.Join(c.dir, emailChannelFileName))
	if errors.Is(err, os.ErrNotExist) {
		return emailChannelFile{Version: emailChannelFileVersion, State: emailStateNone}, nil
	}
	if err != nil {
		return emailChannelFile{}, err
	}
	var file emailChannelFile
	if err := mobileDecodeStrict(raw, &file); err != nil || file.Version != emailChannelFileVersion {
		return emailChannelFile{}, errors.New("the saved email subscription is invalid")
	}
	return file, nil
}

func (c *emailChannel) save(file emailChannelFile) error {
	if c.dir == "" {
		return errTextChannelStorage
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	file.Version = emailChannelFileVersion
	return writeMobilePrivateJSON(filepath.Join(c.dir, emailChannelFileName), file)
}

func (c *emailChannel) mutate(change func(*emailChannelFile)) (emailChannelFile, error) {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	file, err := c.load()
	if err != nil {
		return emailChannelFile{}, err
	}
	change(&file)
	return file, c.save(file)
}

func (c *emailChannel) read() (emailChannelFile, error) {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	return c.load()
}

func (c *emailChannel) statusOf(file emailChannelFile) EmailChannelStatus {
	status := EmailChannelStatus{
		Available:      c.relay() != nil,
		State:          file.State,
		Address:        file.Address,
		PendingAddress: file.PendingAddress,
		SentToday:      file.SentToday,
		DailyCap:       file.DailyCap,
		LastError:      file.LastError,
	}
	if status.State == "" {
		status.State = emailStateNone
	}
	if !file.CodeExpiresAt.IsZero() && file.CodeExpiresAt.After(c.now()) {
		status.CodeExpiresAt = file.CodeExpiresAt.UTC().Format(time.RFC3339)
	}
	return status
}

// applyEmailRelay records what the relay reported. An active subscription the
// relay reports for a Mac that confirmed no address is not believed.
func applyEmailRelay(file *emailChannelFile, status emailRelayStatus, now time.Time) {
	file.State = status.State
	file.SentToday, file.DailyCap = status.SentToday, status.DailyCap
	file.CheckedAt = now
	file.LastError = ""
	file.CodeExpiresAt = time.Time{}
	if status.CodeExpiresAt != nil {
		if expires, err := time.Parse(time.RFC3339, *status.CodeExpiresAt); err == nil {
			file.CodeExpiresAt = expires
		}
	}
	if file.CodeExpiresAt.IsZero() {
		file.PendingAddress = ""
	}
	switch status.State {
	case emailStateNone, emailStatePending:
		file.Address = ""
	case emailStateActive:
		if file.Address == "" {
			file.State = emailStateNone
			if !file.CodeExpiresAt.IsZero() {
				file.State = emailStatePending
			}
		}
	default:
		file.State = emailStateNone
	}
}

func (c *emailChannel) call(ctx context.Context, method, path string, body any) (emailRelayStatus, error) {
	relay := c.relay()
	if relay == nil {
		return emailRelayStatus{}, &smsError{Code: smsErrOffline}
	}
	ctx, cancel := context.WithTimeout(ctx, emailCallTimeout)
	defer cancel()
	return relay.emailCall(ctx, method, path, body)
}

// Status reports the saved subscription without asking the relay.
func (c *emailChannel) Status() (EmailChannelStatus, error) {
	file, err := c.read()
	return c.statusOf(file), err
}

// Active reports whether email can go out now: an address this Mac
// confirmed, active on the relay, and a relay connection.
func (c *emailChannel) Active() bool {
	file, err := c.read()
	return err == nil && file.State == emailStateActive && file.Address != "" && c.relay() != nil
}

// Refresh reads the subscription from the relay and saves it.
func (c *emailChannel) Refresh(ctx context.Context) (EmailChannelStatus, error) {
	c.callMu.Lock()
	defer c.callMu.Unlock()
	relayStatus, err := c.call(ctx, "GET", "/v1/hub/email/subscription", nil)
	if err != nil {
		file, readErr := c.read()
		status := c.statusOf(file)
		status.LastError = smsErrorCode(err)
		return status, readErr
	}
	file, err := c.mutate(func(f *emailChannelFile) { applyEmailRelay(f, relayStatus, c.now()) })
	return c.statusOf(file), err
}

// Start asks the relay to mail a code to the address. An active subscription
// keeps working until the new address is confirmed.
func (c *emailChannel) Start(ctx context.Context, raw string) (EmailChannelStatus, error) {
	address := normalizeEmailAddress(raw)
	if address == "" {
		return EmailChannelStatus{}, &smsError{Code: emailErrInvalidAddress}
	}
	c.callMu.Lock()
	defer c.callMu.Unlock()
	saved, err := c.read()
	if err != nil {
		return EmailChannelStatus{}, err
	}
	if saved.Address == "" {
		// A subscription this Mac lost track of is verified again.
		if _, err = c.call(ctx, "DELETE", "/v1/hub/email/subscription", nil); err != nil {
			return EmailChannelStatus{}, err
		}
	}
	relayStatus, err := c.call(ctx, "POST", "/v1/hub/email/subscription", map[string]string{"address": address, "lang": c.lang()})
	if err != nil {
		return EmailChannelStatus{}, err
	}
	file, err := c.mutate(func(f *emailChannelFile) {
		applyEmailRelay(f, relayStatus, c.now())
		if !f.CodeExpiresAt.IsZero() {
			f.PendingAddress = address
		}
	})
	return c.statusOf(file), err
}

// Confirm sends the code the user typed; a right code makes the pending
// address the subscribed one. This is the only place the address is set.
func (c *emailChannel) Confirm(ctx context.Context, code string) (EmailChannelStatus, error) {
	code = strings.TrimSpace(code)
	if !emailCodePattern.MatchString(code) {
		return EmailChannelStatus{}, &smsError{Code: smsErrInvalidCode}
	}
	c.callMu.Lock()
	defer c.callMu.Unlock()
	pending, err := c.read()
	if err != nil {
		return EmailChannelStatus{}, err
	}
	relayStatus, err := c.call(ctx, "POST", "/v1/hub/email/subscription/confirm", map[string]string{"code": code})
	if err != nil {
		return EmailChannelStatus{}, err
	}
	file, err := c.mutate(func(f *emailChannelFile) {
		if relayStatus.State == emailStateActive && pending.PendingAddress != "" {
			f.Address = pending.PendingAddress
		}
		applyEmailRelay(f, relayStatus, c.now())
		f.PendingAddress = ""
	})
	return c.statusOf(file), err
}

// Stop removes the subscription on the relay and forgets the address here.
func (c *emailChannel) Stop(ctx context.Context) (EmailChannelStatus, error) {
	c.callMu.Lock()
	defer c.callMu.Unlock()
	relayStatus, err := c.call(ctx, "DELETE", "/v1/hub/email/subscription", nil)
	if err != nil {
		return EmailChannelStatus{}, err
	}
	file, err := c.mutate(func(f *emailChannelFile) {
		applyEmailRelay(f, relayStatus, c.now())
		f.Address, f.PendingAddress = "", ""
	})
	return c.statusOf(file), err
}

// Send mails one message to the verified address. The relay picks the
// recipient; this call cannot name one.
func (c *emailChannel) Send(ctx context.Context, subject, body string) error {
	subject = clampRunes(strings.Join(strings.Fields(subject), " "), emailSubjectMax)
	// The relay refuses control characters other than a newline or a tab.
	body = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, body)
	body = clampRunes(body, emailBodyMax)
	if subject == "" || body == "" {
		return errors.New("an email needs a subject and a body")
	}
	c.callMu.Lock()
	defer c.callMu.Unlock()
	if file, err := c.read(); err != nil || file.Address == "" {
		return &smsError{Code: "not_active"}
	}
	relayStatus, err := c.call(ctx, "POST", "/v1/hub/email/send", map[string]string{"subject": subject, "body": body})
	if err != nil {
		if smsErrorCode(err) == "not_active" {
			if _, saveErr := c.mutate(func(f *emailChannelFile) { f.State, f.Address = emailStateNone, "" }); saveErr != nil {
				log.Printf("[email] recording a refused email: %v", saveErr)
			}
		}
		return err
	}
	if _, err := c.mutate(func(f *emailChannelFile) { applyEmailRelay(f, relayStatus, c.now()) }); err != nil {
		log.Printf("[email] recording a sent email: %v", err)
	}
	return nil
}

// engagementEmail sends a moment by email: only kinds with email copy (a
// playbook's result), only to the verified address.
func (a *App) engagementEmail(ctx context.Context, _ storage.EngagementMomentRow, m engagement.Moment, _ engagement.Verdict, text engagementText) error {
	if !a.engagementDeliveryAllowsRead(ctx, m) {
		return errors.New("message source held")
	}
	return sendEngagementEmail(ctx, a.emailChannel(), m, text)
}

func sendEngagementEmail(ctx context.Context, ch *emailChannel, m engagement.Moment, text engagementText) error {
	if strings.TrimSpace(text.EmailSubject) == "" || strings.TrimSpace(text.EmailBody) == "" {
		return errors.New("no email for " + string(m.Kind))
	}
	if !ch.Active() {
		return &smsError{Code: "not_active"}
	}
	return ch.Send(ctx, text.EmailSubject, text.EmailBody)
}

// GetEmailChannelStatus reports the email subscription, asking the relay when
// the connection is on. Wails binding.
func (a *App) GetEmailChannelStatus() (perfValue EmailChannelStatus, perfErr error) {
	defer logPageBinding("GetEmailChannelStatus", time.Now(), &perfValue)
	return a.emailChannel().Refresh(context.Background())
}

// StartEmailVerification asks the relay to mail a code to the address. A
// refusal is one error code, such as invalid_address. Wails binding.
func (a *App) StartEmailVerification(address string) (EmailChannelStatus, error) {
	return a.emailChannel().Start(context.Background(), address)
}

// ConfirmEmailVerification sends the 6-digit code from the email. Wails binding.
func (a *App) ConfirmEmailVerification(code string) (EmailChannelStatus, error) {
	return a.emailChannel().Confirm(context.Background(), code)
}

// StopEmailChannel removes the email subscription and forgets the address.
// Wails binding.
func (a *App) StopEmailChannel() (EmailChannelStatus, error) {
	return a.emailChannel().Stop(context.Background())
}

// emailCall makes one call to an email route with the hub credential
// (PROTOCOL.md 19.2) and decodes the status object. Neither the address nor
// a message is ever logged here.
//
//isoform:destination email
func (c *mobileRelayClient) emailCall(ctx context.Context, method, path string, body any) (emailRelayStatus, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return emailRelayStatus{}, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, reader)
	if err != nil {
		return emailRelayStatus{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("X-Yansu-Hub", c.hubID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if path == "/v1/hub/email/send" {
		req = destinations.CountRequest(req, destinations.Email)
	}
	resp, err := smsHTTPClient.Do(req)
	if err != nil {
		return emailRelayStatus{}, &smsError{Code: smsErrOffline}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, smsResponseLimit))
	if err != nil {
		return emailRelayStatus{}, &smsError{Code: smsErrOffline}
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return emailRelayStatus{}, &smsError{Code: smsErrUnknown}
	}
	if resp.StatusCode >= 400 || !envelope.OK {
		return emailRelayStatus{}, smsRefusal(resp, envelope.Data)
	}
	var status emailRelayStatus
	if err := json.Unmarshal(envelope.Data, &status); err != nil || status.State == "" {
		return emailRelayStatus{}, &smsError{Code: smsErrUnknown}
	}
	return status, nil
}
