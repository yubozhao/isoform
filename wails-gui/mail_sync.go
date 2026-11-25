package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"wails-gui/internal/engagement"
	"wails-gui/internal/identity"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/mailheaders"
	"wails-gui/storage"
)

// The Mail reader keeps mail_messages current with the headers of the last
// week's inbox and sent messages, read from Mail.app over Apple Events every
// ten minutes while Mail is running: message-id, sender, recipients,
// subject, date, and read, flagged and replied state. Never a body.
//
// It never opens Mail and never asks for the Automation permission: it reads
// only when Mail is already running and macOS says, without asking, that
// Isoform may automate it. Only the Allow button (RequestPermission) shows
// macOS's prompt, one at a time, and only while Mail is open.
//
// It reads nothing while observing is paused and keeps nothing received or
// sent inside a pause. Switching it off or macOS refusing access keeps saved
// data. Protecting Mail on the Privacy page deletes what was kept. Senders
// who are already people Isoform knows gain an email sighting, as do people the user
// writes to; nobody new is created. Subjects are kept for the waiting on
// reply moment and the settings card, redacted, and never sent to a model.
//
// Mail answers on its main thread and goes over the whole mailbox for each
// property asked, so a read of a very large mailbox can freeze Mail until
// the script's timeout, and Mail keeps working after osascript is stopped.
// After a read Mail did not answer in time the reader waits longer each
// time, and after mailSlowLimit of them in a row it reads only when the user
// taps Refresh.
const (
	mailPermissionKey  = "automation_mail"
	mailSyncInterval   = 10 * time.Minute
	mailFirstRun       = 2 * time.Minute
	mailWindowDays     = 7
	mailSubjectMax     = 200
	mailNameMax        = 120
	mailSightingApp    = "mail"
	mailLastReadKey    = "mail_last_read_at"
	mailAllowedKey     = "mail_automation_allowed"
	mailConsentVersion = 1
	mailConsentFile    = "mail-consent.json"
	// mailWaitingAfter is how long a direct message waits before Isoform
	// says someone is waiting on the user.
	mailWaitingAfter = 24 * time.Hour
	// mailGoneMargin keeps a message at the edge of the window from being
	// taken for one that left the inbox.
	mailGoneMargin = 10 * time.Minute
	// After a read Mail did not answer in time, the next waits twice the
	// interval, then four times, up to mailSlowWaitMax; after mailSlowLimit
	// of them in a row reading waits for Refresh.
	mailSlowWaitMax = 6 * time.Hour
	mailSlowLimit   = 3
)

var (
	errMailNotAllowed = errors.New("automation for Mail is not allowed; turn it on in System Settings > Privacy & Security > Automation")
	errMailStopped    = errors.New("mail reading stopped during the read")
	// errMailClosed is Mail quitting during a read.
	errMailClosed = errors.New("Mail quit during the read")
	// errMailSlow is a read Mail did not answer in time.
	errMailSlow = errors.New("Mail did not answer within a minute")
	// errMailStalled is reading stopped after Mail was slow too often.
	errMailStalled = errors.New("Mail was slow to answer several times in a row, so Isoform stopped reading it; tap Refresh to try again")
)

// mailAutomationState is macOS's answer about automating Mail.
type mailAutomationState int

const (
	mailAutomationUnknown mailAutomationState = iota
	mailAutomationAllowed
	mailAutomationDenied
	mailAutomationUndecided
)

// mailAllowedCache is the last definitive answer, since macOS answers only
// while Mail runs: 0 unknown, 1 allowed, 2 not allowed.
var mailAllowedCache atomic.Int32

// mailAutomationGranted is the Granted flag of the "automation_mail" item:
// macOS's answer while Mail runs, otherwise the last one recorded.
func mailAutomationGranted() bool {
	switch mailAutomationStatus(false) {
	case mailAutomationAllowed:
		mailAllowedCache.Store(1)
	case mailAutomationDenied, mailAutomationUndecided:
		mailAllowedCache.Store(2)
	}
	return mailAllowedCache.Load() == 1
}

// mailSyncResult is what one read produced.
type mailSyncResult struct {
	Reason  string
	Inbox   int
	Sent    int
	Removed int64
	At      time.Time
}

// mailSyncService owns the reader loop.
type mailSyncService struct {
	store *storage.Store
	now   func() time.Time
	// enabled is the switch; protected is Mail on the Privacy page's list.
	enabled   captureSwitch
	protected func() bool
	paused    func() bool
	pauses    func() ([]observationPause, error)
	// process is the running Mail's process id, 0 when it is not running.
	process    func() int
	automation func() mailAutomationState
	run        func(ctx context.Context, script string) ([]byte, error)
	// resolve finds the person an address already belongs to; it never
	// creates one.
	resolve func(ctx context.Context, email string) (string, bool)
	// recordAllowed keeps macOS's last definitive answer.
	recordAllowed func(bool)
	onSynced      func(mailSyncResult)
	consentPath   string

	// own is the user's addresses from the last read, kept in memory only so a
	// draft can name who is on its thread but not copied.
	ownMu sync.Mutex
	own   map[string]bool

	// writeMu orders a read's writes against clearing them.
	writeMu sync.Mutex
	// lastReadRows fingerprints headers from the last successful read. Retained
	// rows outside this set are not current evidence.
	// It is protected by writeMu and never persisted across process restarts.
	lastReadRows map[string]openAnswerMailFingerprint
	// Empty mailbox confirmation for open-answer evidence counts only
	// consecutive complete reads, unlike the general sync cleanup counter.
	openAnswerEmptyReads map[string]int

	mu         sync.Mutex
	cancel     context.CancelFunc
	loopCtx    context.Context
	wg         sync.WaitGroup
	kicks      chan string
	syncing    bool
	lastSync   time.Time
	lastErr    string
	notRunning bool
	// emptyReads counts, per mailbox, reads in a row that returned nothing.
	emptyReads map[string]int
	// slowReads counts reads in a row Mail did not answer in time;
	// nextRead is when the loop may read again after one.
	slowReads int
	nextRead  time.Time
}

func newMailSyncService(a *App) *mailSyncService {
	svc := &mailSyncService{
		store:   a.store,
		now:     time.Now,
		enabled: func() (bool, bool) { return a.captureState(context.Background(), "mail_sync", mailPermissionKey) },
		protected: func() bool {
			_, sensitive := pii.IsSensitiveApp(mailheaders.BundleID)
			return sensitive || pii.IsSensitiveAppName("Mail")
		},
		paused:     a.IsClosedEyesModeEnabled,
		pauses:     a.observationPauses,
		process:    mailProcessID,
		automation: func() mailAutomationState { return mailAutomationStatus(false) },
		run:        runMailScript,
		kicks:      make(chan string, 4),
	}
	resolver := identity.NewResolver(a.store, false)
	svc.resolve = func(ctx context.Context, email string) (string, bool) {
		subject, err := resolver.ResolveSender(ctx, "email", email, "")
		if err != nil {
			return "", false
		}
		if subject.PrincipalID == "" || subject.Kind == identity.SubjectAnonymous || subject.Kind == identity.SubjectSelf {
			return "", false
		}
		return subject.PrincipalID, true
	}
	svc.recordAllowed = func(allowed bool) {
		value, state := "false", int32(2)
		if allowed {
			value, state = "true", 1
		}
		// The Mail card shows access from the permissions; tell it when the
		// answer changed.
		if mailAllowedCache.Swap(state) != state {
			a.emitBrowserEvent("permissions:changed")
		}
		if err := a.store.SetUIState(context.Background(), mailAllowedKey, value); err != nil {
			log.Printf("[mail] record permission: %v", err)
		}
	}
	if dataRoot, err := flavorDataDir(); err == nil {
		svc.consentPath = filepath.Join(dataRoot, mailConsentFile)
	}
	svc.onSynced = func(res mailSyncResult) { a.afterMailSync(res) }
	return svc
}

// startMailSync builds and starts the reader once from startup.
func (a *App) startMailSync(ctx context.Context) {
	if a == nil || a.store == nil || !systemMailSupported() {
		return
	}
	// The last answer macOS gave, until Mail runs and it can answer again.
	if raw, err := a.store.GetUIState(context.Background(), mailAllowedKey); err == nil {
		switch raw {
		case "true":
			mailAllowedCache.CompareAndSwap(0, 1)
		case "false":
			mailAllowedCache.CompareAndSwap(0, 2)
		}
	}
	svc := newMailSyncService(a)
	a.mailSync = svc
	svc.Start(ctx)
}

// stopMailSync ends the reader at shutdown.
func (a *App) stopMailSync() {
	if a == nil || a.mailSync == nil {
		return
	}
	a.mailSync.Stop()
}

// Start runs the loop until the context ends or Stop is called.
func (s *mailSyncService) Start(parent context.Context) {
	if s == nil || s.store == nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	// A restarted observer must make a new read before its retained headers
	// can be used as current evidence.
	s.lastReadRows = nil
	s.openAnswerEmptyReads = nil
	s.cancel = cancel
	s.loopCtx = ctx
	s.mu.Unlock()
	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop ends the loop and waits for a read in flight (bounded by the
// script's timeout).
// ownAddresses is the user's addresses from the last read, or nil before one.
func (s *mailSyncService) ownAddresses() map[string]bool {
	if s == nil {
		return nil
	}
	s.ownMu.Lock()
	defer s.ownMu.Unlock()
	return s.own
}

func (s *mailSyncService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.loopCtx = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// RequestSync asks for a read soon. It never blocks.
func (s *mailSyncService) RequestSync(reason string) {
	if s == nil {
		return
	}
	select {
	case s.kicks <- reason:
	default:
	}
}

func (s *mailSyncService) loop(ctx context.Context) {
	defer s.wg.Done()
	timer := time.NewTimer(mailFirstRun)
	defer timer.Stop()
	for {
		reason := "interval"
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case reason = <-s.kicks:
		}
		s.syncIfAllowed(ctx, reason)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(mailSyncInterval)
	}
}

// readingAllowed is the switch on and Mail not protected.
func (s *mailSyncService) readingAllowed() bool {
	return s.enabled.on() && (s.protected == nil || !s.protected())
}

// syncIfAllowed runs one read when everything allows it. Switching reading
// off pauses collection and retains saved data; protecting Mail still forgets it.
func (s *mailSyncService) syncIfAllowed(ctx context.Context, reason string) {
	if s.protected != nil && s.protected() {
		s.clearStored(ctx, "Mail protected")
		return
	}
	on, known := s.enabled()
	if !known {
		return
	}
	if !on {
		// Keep the permission status current while reading is off. This
		// probe never asks for access, launches Mail or reads its headers.
		state := mailAutomationUnknown
		if s.automation != nil {
			state = s.automation()
		}
		switch state {
		case mailAutomationAllowed:
			if s.recordAllowed != nil {
				s.recordAllowed(true)
			}
		case mailAutomationDenied:
			if s.recordAllowed != nil {
				s.recordAllowed(false)
			}
		}
		return
	}
	if s.paused != nil && s.paused() {
		return
	}
	if s.holdingOff(s.now()) {
		return
	}
	if _, err := s.Sync(ctx, reason); err != nil && ctx.Err() == nil && !errors.Is(err, errMailStopped) {
		log.Printf("[mail] read (%s): %v", reason, err)
	}
}

// holdingOff reports that the loop must not read yet: Mail was slow to
// answer lately, or too often (then only Refresh, which calls Sync
// directly, reads again).
func (s *mailSyncService) holdingOff(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slowReads >= mailSlowLimit || now.Before(s.nextRead)
}

// noteSlow records a read Mail did not answer in time and sets how long the
// loop waits before the next one.
func (s *mailSyncService) noteSlow(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slowReads++
	wait := mailSlowWaitMax
	if s.slowReads < 16 {
		wait = min(mailSyncInterval<<s.slowReads, mailSlowWaitMax)
	}
	s.nextRead = now.Add(wait)
	s.lastErr = errMailSlow.Error()
	if s.slowReads >= mailSlowLimit {
		s.lastErr = errMailStalled.Error()
	}
}

// Sync reads the headers once. It keeps what is stored when Mail is not
// running, macOS has not decided, or access is refused.
func (s *mailSyncService) Sync(ctx context.Context, reason string) (mailSyncResult, error) {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return mailSyncResult{}, errors.New("a mail read is already running")
	}
	s.syncing = true
	s.mu.Unlock()
	completeEvidence := false
	defer func() {
		if !completeEvidence {
			s.writeMu.Lock()
			s.lastReadRows = nil
			s.openAnswerEmptyReads = nil
			s.writeMu.Unlock()
		}
		s.mu.Lock()
		s.syncing = false
		s.mu.Unlock()
	}()

	now := s.now()
	res := mailSyncResult{Reason: reason, At: now}
	pid := s.process()
	if pid <= 0 {
		s.setNotRunning(true)
		return res, nil
	}
	s.setNotRunning(false)
	switch s.automation() {
	case mailAutomationAllowed:
		s.recordAllowed(true)
	case mailAutomationDenied:
		s.recordAllowed(false)
		s.noteError(errMailNotAllowed)
		return res, errMailNotAllowed
	default:
		// Undecided (only the Allow button may ask) or Mail quit meanwhile.
		return res, nil
	}
	if err := recordObservationConsent(s.consentPath, mailConsentVersion, now); err != nil {
		log.Printf("[mail] record consent: %v", err)
	}
	out, err := s.run(ctx, mailheaders.Script(mailWindowDays, pid))
	if err != nil {
		if errors.Is(err, errMailClosed) {
			s.setNotRunning(true)
			return res, nil
		}
		if errors.Is(err, errMailSlow) {
			s.noteSlow(now)
			return res, err
		}
		if errors.Is(err, errMailNotAllowed) {
			s.recordAllowed(false)
		}
		s.noteError(err)
		return res, err
	}
	snap, err := mailheaders.Parse(out)
	if err != nil {
		s.noteError(err)
		return res, err
	}
	if !snap.Running {
		s.setNotRunning(true)
		return res, nil
	}
	// Read the pause log after Mail answered, so a pause that began meanwhile
	// still counts; a log that cannot be read stores nothing.
	pauses, err := s.pauses()
	if err != nil {
		s.noteError(err)
		return res, err
	}
	windowStart := now.Add(-mailWindowDays * 24 * time.Hour)
	inbox, sent := mailRows(snap, pauses, windowStart, now)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.readingAllowed() || (s.paused != nil && s.paused()) {
		return res, errMailStopped
	}
	me := mailAddressSet(mailOwnAddresses(snap))
	s.ownMu.Lock()
	s.own = me
	s.ownMu.Unlock()
	for i := range inbox {
		if !me[inbox[i].SenderEmail] {
			if id, ok := s.resolve(ctx, inbox[i].SenderEmail); ok {
				inbox[i].PrincipalID = id
			}
		}
	}
	rows := append(append([]storage.MailMessageRow{}, inbox...), sent...)
	added, err := s.store.UpsertMailMessages(ctx, rows, now.UnixMilli())
	if err != nil {
		s.noteError(err)
		return res, err
	}
	s.recordSightings(ctx, rows, added, me, now)
	res.Inbox, res.Sent = len(inbox), len(sent)
	// A mailbox that returns nothing may be Mail still loading; two empty
	// reads of it in a row mean it is empty. Each mailbox counts on its own.
	since := windowStart.Add(mailGoneMargin).UnixMilli()
	for _, box := range []struct {
		name     string
		returned int
		kept     []storage.MailMessageRow
	}{
		{storage.MailboxInbox, len(snap.Inbox), inbox},
		{storage.MailboxSent, len(snap.Sent), sent},
	} {
		if s.emptyReads == nil {
			s.emptyReads = map[string]int{}
		}
		if box.returned == 0 {
			s.emptyReads[box.name]++
			if s.emptyReads[box.name] < 2 {
				continue
			}
		} else {
			s.emptyReads[box.name] = 0
		}
		seen := make(map[string]bool, len(box.kept))
		for _, row := range box.kept {
			seen[row.ID] = true
		}
		removed, err := s.store.DeleteMailMessagesGone(ctx, box.name, since, seen)
		if err != nil {
			log.Printf("[mail] forget messages that left %s: %v", box.name, err)
		}
		res.Removed += removed
	}
	if _, err := s.store.PruneMailMessages(ctx, windowStart.UnixMilli()); err != nil {
		log.Printf("[mail] prune: %v", err)
	}
	if err := s.store.SetUIState(ctx, mailLastReadKey, now.UTC().Format(time.RFC3339)); err != nil {
		log.Printf("[mail] record last read: %v", err)
	}
	s.lastReadRows = openAnswerMailReadFingerprints(snap, rows)
	if s.lastReadRows != nil {
		if s.openAnswerEmptyReads == nil {
			s.openAnswerEmptyReads = map[string]int{}
		}
		for _, box := range []struct {
			name  string
			count int
		}{{storage.MailboxInbox, len(snap.Inbox)}, {storage.MailboxSent, len(snap.Sent)}} {
			if box.count == 0 {
				s.openAnswerEmptyReads[box.name]++
			} else {
				s.openAnswerEmptyReads[box.name] = 0
			}
		}
		completeEvidence = true
	}
	s.mu.Lock()
	s.lastSync = now
	s.lastErr = ""
	s.slowReads = 0
	s.nextRead = time.Time{}
	s.mu.Unlock()
	if s.onSynced != nil {
		s.onSynced(res)
	}
	return res, nil
}

// recordSightings gives the people Isoform already knows one email sighting
// per new message: the sender of a received one, each person the user wrote
// to in a sent one.
func (s *mailSyncService) recordSightings(ctx context.Context, rows []storage.MailMessageRow, added []string, me map[string]bool, now time.Time) {
	isNew := make(map[string]bool, len(added))
	for _, id := range added {
		isNew[id] = true
	}
	for _, row := range rows {
		if !isNew[row.ID] {
			continue
		}
		at := min(row.At, now.UnixMilli())
		var people []string
		switch row.Mailbox {
		case storage.MailboxInbox:
			if row.PrincipalID != "" {
				people = append(people, row.PrincipalID)
			}
		case storage.MailboxSent:
			for _, email := range strings.Split(row.To, "\n") {
				if email == "" || me[email] {
					continue
				}
				if id, ok := s.resolve(ctx, email); ok {
					people = append(people, id)
				}
			}
		}
		for _, id := range people {
			if err := s.store.BumpPrincipalSighting(ctx, id, mailSightingApp, "", at, ""); err != nil {
				log.Printf("[mail] record sighting: %v", err)
			}
		}
	}
}

// mailOwnAddresses is the user's addresses: Mail's accounts', or, when Mail
// did not give them, the senders of the sent mailbox.
func mailOwnAddresses(snap mailheaders.Snapshot) []string {
	if len(snap.Me) > 0 {
		return snap.Me
	}
	var own []string
	for _, h := range snap.Sent {
		if h.FromEmail != "" {
			own = append(own, h.FromEmail)
		}
	}
	return own
}

// mailRows turns a snapshot into rows: inside the window, not inside a
// pause, with an address; subjects and names redacted.
func mailRows(snap mailheaders.Snapshot, pauses []observationPause, windowStart, now time.Time) (inbox, sent []storage.MailMessageRow) {
	me := mailAddressSet(mailOwnAddresses(snap))
	keep := func(h mailheaders.Header) bool {
		return !h.At.Before(windowStart) && !h.At.After(now.Add(time.Hour)) && !pausedAt(pauses, h.At)
	}
	for _, h := range snap.Inbox {
		if h.FromEmail == "" || !keep(h) {
			continue
		}
		toMe := false
		for _, addr := range h.To {
			if me[addr] {
				toMe = true
			}
		}
		inbox = append(inbox, mailRow(storage.MailboxInbox, h, toMe && !me[h.FromEmail]))
	}
	for _, h := range snap.Sent {
		if !keep(h) {
			continue
		}
		sent = append(sent, mailRow(storage.MailboxSent, h, false))
	}
	return inbox, sent
}

func mailRow(mailbox string, h mailheaders.Header, toMe bool) storage.MailMessageRow {
	subject := mailText(h.Subject, mailSubjectMax)
	// The thread is keyed by the subject as written (only its hash is
	// kept), so redacting "Invoice 1234" and "Invoice 5678" alike does not
	// join two conversations.
	return storage.MailMessageRow{
		ID:          mailMessageKey(mailbox, h.MessageID),
		Mailbox:     mailbox,
		SenderEmail: h.FromEmail,
		SenderName:  mailText(h.FromName, mailNameMax),
		To:          strings.Join(h.To, "\n"),
		CC:          strings.Join(h.CC, "\n"),
		ToMe:        toMe,
		Subject:     subject,
		ThreadKey:   mailThreadKey(h.Subject),
		At:          h.At.UnixMilli(),
		Read:        h.Read,
		Flagged:     h.Flagged,
		Replied:     h.Replied,
	}
}

// mailText redacts personal details and codes and bounds the text.
func mailText(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.ToValidUTF8(text, "")), " ")
	if text == "" {
		return ""
	}
	text, _ = pii.RedactText(text)
	text = pii.RedactCodes(text)
	if utf8.RuneCountInString(text) > maxRunes {
		text = string([]rune(text)[:maxRunes])
	}
	return text
}

// mailMessageKey is the stored id: a hash of the mailbox and message-id.
func mailMessageKey(mailbox, messageID string) string {
	sum := sha256.Sum256([]byte(mailbox + "\x00" + strings.TrimSpace(messageID)))
	return hex.EncodeToString(sum[:16])
}

// mailThreadKey groups the messages of one conversation: a hash of the
// subject without its reply and forward markers. An empty subject joins no
// thread.
func mailThreadKey(subject string) string {
	normalized := strings.ToLower(mailheaders.NormalizeSubject(subject))
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:12])
}

func mailAddressSet(addresses []string) map[string]bool {
	set := make(map[string]bool, len(addresses))
	for _, addr := range addresses {
		set[strings.ToLower(addr)] = true
	}
	return set
}

// clearStored deletes every stored header and blanks the waiting on reply
// moments made from them (they quote subjects), after waiting for a read's
// writes. The moments stay without their words, so reading again later
// does not announce a conversation a second time.
func (s *mailSyncService) clearStored(ctx context.Context, why string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.lastReadRows = nil
	s.openAnswerEmptyReads = nil
	removed, err := s.store.DeleteAllMailData(ctx)
	if err != nil {
		log.Printf("[mail] clear: %v", err)
		return
	}
	moments, err := s.store.ForgetEngagementMomentsOfKind(ctx, string(engagement.KindWaitingOnReply), "mail_cleared")
	if err != nil {
		log.Printf("[mail] clear waiting on reply moments: %v", err)
	}
	if removed > 0 || moments > 0 {
		log.Printf("[mail] %s; removed %d stored messages and %d moments", why, removed, moments)
	}
}

func (s *mailSyncService) noteError(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

func (s *mailSyncService) setNotRunning(v bool) {
	s.mu.Lock()
	s.notRunning = v
	s.mu.Unlock()
}

// status reports the last read, its error and whether Mail was closed at the
// last try.
func (s *mailSyncService) status() (time.Time, string, bool) {
	if s == nil {
		return time.Time{}, "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync, s.lastErr, s.notRunning
}

// stalled reports that reading waits for Refresh after Mail was slow to
// answer too often.
func (s *mailSyncService) stalled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slowReads >= mailSlowLimit
}

// mailProtectionChanged runs after an app is protected on the Privacy page:
// when that app is Mail, what was kept from it is deleted at once.
func (a *App) mailProtectionChanged() {
	if a == nil || a.mailSync == nil || a.mailSync.protected == nil || !a.mailSync.protected() {
		return
	}
	a.mailSync.clearStored(context.Background(), "Mail protected")
}

// mailPermissionChanged reacts to the Allow button and the switch: a grant
// or a switch-on reads soon; a switch-off pauses reads and keeps saved data.
func (a *App) mailPermissionChanged(granted, enabled bool) {
	if a == nil || a.mailSync == nil {
		return
	}
	if !enabled {
		return
	}
	if granted {
		a.mailSync.RequestSync("enabled")
	}
}

// mailWaitingThread is one conversation in which someone the user knows
// wrote to them directly and is still waiting. A conversation is one subject
// with one person, so two people asking "Quick question" are two.
type mailWaitingThread struct {
	ThreadKey   string
	PrincipalID string
	SenderName  string
	SenderEmail string
	Subject     string
	At          time.Time
}

// mailDirectThread is one person's latest direct message in one
// conversation, and whether the user answered it.
type mailDirectThread struct {
	Latest   storage.MailMessageRow
	Answered bool
}

// mailDirectThreads finds, per person and conversation, the person's latest
// inbox message that names the user in To, from a known person, with a
// subject, and written by that person rather than a program (no invitation
// answers, automatic replies, receipts or no-reply senders). It is answered
// when marked replied in Mail or followed by a message the user sent to
// that person in the same conversation.
func mailDirectThreads(rows []storage.MailMessageRow) []mailDirectThread {
	sentTo := map[string][]storage.MailMessageRow{}
	for _, row := range rows {
		if row.Mailbox == storage.MailboxSent && row.ThreadKey != "" {
			sentTo[row.ThreadKey] = append(sentTo[row.ThreadKey], row)
		}
	}
	answered := func(m storage.MailMessageRow) bool {
		if m.Replied {
			return true
		}
		for _, s := range sentTo[m.ThreadKey] {
			if s.At <= m.At {
				continue
			}
			for _, addr := range strings.Split(s.To+"\n"+s.CC, "\n") {
				if addr == m.SenderEmail {
					return true
				}
			}
		}
		return false
	}
	latest := map[string]storage.MailMessageRow{}
	for _, row := range rows {
		if row.Mailbox != storage.MailboxInbox || !row.ToMe || row.PrincipalID == "" ||
			row.ThreadKey == "" || strings.TrimSpace(row.Subject) == "" ||
			mailheaders.Automated(row.Subject, row.SenderEmail) {
			continue
		}
		key := row.PrincipalID + "\x00" + row.ThreadKey
		if prev, ok := latest[key]; !ok || row.At > prev.At {
			latest[key] = row
		}
	}
	out := make([]mailDirectThread, 0, len(latest))
	for _, m := range latest {
		out = append(out, mailDirectThread{Latest: m, Answered: answered(m)})
	}
	return out
}

// mailWaitingOnReply finds the threads waiting on the user: a direct message
// (mailDirectThreads) received more than a day ago and inside the window and
// not answered. Each person's thread counts once, by their latest message.
func mailWaitingOnReply(rows []storage.MailMessageRow, now time.Time) []mailWaitingThread {
	cutoff := now.Add(-mailWaitingAfter).UnixMilli()
	oldest := now.Add(-mailWindowDays * 24 * time.Hour).UnixMilli()
	var out []mailWaitingThread
	for _, thread := range mailDirectThreads(rows) {
		m := thread.Latest
		// The person's latest direct message decides: a newer one restarts
		// the wait.
		if m.At > cutoff || m.At < oldest || thread.Answered {
			continue
		}
		out = append(out, mailWaitingThread{
			ThreadKey:   m.ThreadKey,
			PrincipalID: m.PrincipalID,
			SenderName:  m.SenderName,
			SenderEmail: m.SenderEmail,
			Subject:     mailheaders.NormalizeSubject(m.Subject),
			At:          time.UnixMilli(m.At),
		})
	}
	return out
}

// mailUnansweredKeys is the waiting on reply dedupe key of every person and
// conversation still unanswered, however recent: a moment whose key is not
// among them was answered, or its messages left the inbox.
func mailUnansweredKeys(rows []storage.MailMessageRow) map[string]bool {
	keys := map[string]bool{}
	for _, thread := range mailDirectThreads(rows) {
		if !thread.Answered {
			keys[engagement.DedupeKey(engagement.KindWaitingOnReply, thread.Latest.PrincipalID, thread.Latest.ThreadKey)] = true
		}
	}
	return keys
}

// afterMailSync tells the settings page and raises the waiting on reply
// moments the read revealed.
func (a *App) afterMailSync(res mailSyncResult) {
	if a == nil || a.store == nil {
		return
	}
	defer a.emitBrowserEvent("mail:synced")
	a.produceMailWaiting(context.Background(), res.At)
	a.checkOpenAnswerMail(res.At)
}

// produceMailWaiting submits one waiting on reply moment per waiting thread,
// at most once per thread. The person's name is the one Isoform knows them
// by; the subject stays on this Mac and the paired phone.
func (a *App) produceMailWaiting(ctx context.Context, now time.Time) {
	rows, err := a.store.ListMailMessages(ctx, now.Add(-mailWindowDays*24*time.Hour).UnixMilli())
	if err != nil {
		log.Printf("[mail] list for waiting on reply: %v", err)
		return
	}
	a.reconcileMainChatFollowups(ctx, rows)
	a.retireMailWaiting(ctx, mailUnansweredKeys(rows))
	for _, thread := range mailWaitingOnReply(rows, now) {
		name := thread.SenderName
		if person, err := a.store.GetPrincipal(ctx, thread.PrincipalID); err == nil && strings.TrimSpace(person.DisplayName) != "" {
			name = strings.TrimSpace(person.DisplayName)
		}
		if name == "" {
			continue
		}
		if err := a.submitEngagementSignal(ctx, mailWaitingMoment(thread, name, now)); err != nil {
			log.Printf("[mail] waiting on reply moment: %v", err)
		}
	}
}

// retireMailWaiting withdraws the waiting on reply moments not yet
// delivered whose conversation the user answered, or whose messages left
// the inbox, since the read before. A moment already sent stays as it is.
func (a *App) retireMailWaiting(ctx context.Context, unanswered map[string]bool) {
	if a == nil || a.engagement == nil || a.store == nil {
		return
	}
	open, err := a.store.OpenEngagementMomentsOfKind(ctx, string(engagement.KindWaitingOnReply))
	if err != nil {
		log.Printf("[mail] open waiting on reply moments: %v", err)
		return
	}
	for _, m := range open {
		if unanswered[m.DedupeKey] {
			continue
		}
		if err := a.engagement.Retire(ctx, m.DedupeKey, "replied_or_gone"); err != nil {
			log.Printf("[mail] withdraw waiting on reply: %v", err)
		}
	}
}

// mailWaitingMoment is the moment for one waiting thread, keyed by the
// person and the thread so it is said at most once.
func mailWaitingMoment(thread mailWaitingThread, name string, now time.Time) engagement.Moment {
	return engagement.Moment{
		Kind:      engagement.KindWaitingOnReply,
		SubjectID: thread.PrincipalID,
		DedupeKey: engagement.DedupeKey(engagement.KindWaitingOnReply, thread.PrincipalID, thread.ThreadKey),
		CreatedAt: now,
		ExpiresAt: thread.At.Add(mailWindowDays * 24 * time.Hour),
		Stakes:    0.5,
		Params: map[string]string{
			"name":    name,
			"subject": thread.Subject,
		},
	}
}
