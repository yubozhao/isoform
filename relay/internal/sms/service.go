package sms

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/limit"
	"isoform-relay/internal/store"
)

const (
	// CodeTTL is how long a verification code can be confirmed.
	CodeTTL = 10 * time.Minute
	// CodeAttempts is how many wrong codes spend a code.
	CodeAttempts = 5
	// WrongCodesPerNumber is how many wrong codes a number takes in a day,
	// across codes and hubs, before it accepts none until the day is over.
	WrongCodesPerNumber = 10
	// ReplyTTL is how long an inbound reply waits for its hub.
	ReplyTTL = 24 * time.Hour

	codesPerHub          = 3
	codesPerHubWindow    = time.Hour
	codesPerNumber       = 5
	codesPerNumberWindow = 24 * time.Hour
	confirmsPerHub       = 10
	confirmsWindow       = 10 * time.Minute
	repliesPerNumber     = 30
	helpPerNumber        = 2
	stopRepliesPerNumber = 3
	inboundWindow        = time.Hour
	stopReplyWindow      = 24 * time.Hour
	capWindow            = time.Hour
	unknownSubscriber    = "none"
	providerCallBudget   = 15 * time.Second
)

// Errors the hub routes map to responses. Their text is static.
var (
	ErrInvalidNumber = errors.New("the number must be in international form, such as +14155550123")
	ErrCountry       = errors.New("texts are not available for this number yet")
	ErrOptedOut      = errors.New("this number replied STOP; text START to the Isoform number to resume")
	ErrNotActive     = errors.New("there is no active text subscription")
	ErrQuota         = errors.New("the text limit for this period is reached")
	ErrInvalidBody   = errors.New("a text must start with Isoform:, carry no links or phone numbers, and be 1 to 320 characters (160 with Unicode) without control characters")
	ErrNoPending     = errors.New("there is no verification waiting for a code")
	ErrCodeWrong     = errors.New("the code is not right")
	ErrCodeSpent     = errors.New("the code expired or was tried too often; send a new one")
	ErrTooMany       = errors.New("too many attempts; try again later")
	ErrBusy          = errors.New("texts are paused for a while; try again later")
	ErrSendFailed    = errors.New("the text could not be sent")
	ErrUnknownHub    = errors.New("unknown hub")
	ErrNotEntitled   = errors.New("text messages are not enabled for this Mac")
)

// RetryError is a refusal that can be retried after Wait.
type RetryError struct {
	Err  error
	Wait time.Duration
}

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// Status is what a hub sees about its subscription. State is "none",
// "pending_verification" (a code is out and no number is verified yet),
// "active" or "stopped". CodeExpiresAt is set while a code is out, also when
// it is out for a new number beside an active subscription.
type Status struct {
	State         string
	CodeExpiresAt time.Time
	SentToday     int
	SentThisMonth int
	DailyCap      int
	MonthlyCap    int
}

// Reply is one inbound text for a hub.
type Reply struct {
	ID   string
	Body string
	At   time.Time
}

// Service runs the subscriptions, the sends and the webhook. OnReply runs
// after a reply is queued for a hub, OnState after a hub's subscription
// changed without the hub asking (a keyword, another hub confirming the
// number); both are optional and must not block.
//
// The limits that bound cost and guessing (codes per hub, per number and in
// total, wrong codes per number, texts and keyword answers per hour) are kept
// in the store, so a restart does not reset them. The rest (confirm calls,
// HELP and inbound replies per number) are in memory.
type Service struct {
	Store    *store.Store
	Provider Provider
	Vault    *Vault
	Config   Config
	Now      func() time.Time
	OnReply  func(hubID string)
	OnState  func(hubID string)

	limits *limit.Limiter
}

// NewService returns a service over the store with the given provider.
func NewService(st *store.Store, provider Provider, vault *Vault, cfg Config) *Service {
	return &Service{Store: st, Provider: provider, Vault: vault, Config: cfg, Now: time.Now, limits: limit.New()}
}

// allow counts one event against an in-memory limit.
func (s *Service) allow(key string, n int, window time.Duration) error {
	if ok, wait := s.limits.Allow(key, n, window, s.Now()); !ok {
		return &RetryError{Err: ErrTooMany, Wait: wait}
	}
	return nil
}

// take counts one event against a stored limit; refused is the error a
// refusal reports.
func (s *Service) take(ctx context.Context, key string, n int, window time.Duration, refused error) error {
	ok, wait, err := s.Store.TakeSMSLimit(ctx, key, n, window, s.Now())
	if err != nil {
		return err
	}
	if !ok {
		return &RetryError{Err: refused, Wait: wait}
	}
	return nil
}

// spend counts one outbound text against the relay-wide hourly cap.
func (s *Service) spend(ctx context.Context) error {
	return s.take(ctx, "sends", s.Config.HourlyCap, capWindow, ErrBusy)
}

// Enable records that the Mac turned text messages on (POST
// /v1/hub/sms/enable), the flag entitled accepts in place of a paired phone.
func (s *Service) Enable(ctx context.Context, hubID string) (Status, error) {
	switch err := s.Store.EnableSMS(ctx, hubID, s.Now()); {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return Status{}, ErrUnknownHub
	default:
		return Status{}, err
	}
	return s.Status(ctx, hubID)
}

// entitled is the per-install check in front of codes and sends: beyond its
// credential, the hub has a paired phone or the flag Enable sets. It is a
// stub for the paid plan's entitlement (docs/sms-channel.md), which replaces
// the flag with a billing record; any Mac can set the flag today, so it only
// makes a fresh hub id take one more call before it can spend the code budget.
func (s *Service) entitled(ctx context.Context, hubID string) error {
	ok, err := s.Store.SMSEntitled(ctx, hubID, s.Now())
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotEntitled
	}
	return nil
}

func (s *Service) send(ctx context.Context, to, body string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, providerCallBudget)
	defer cancel()
	return s.Provider.Send(ctx, to, body)
}

// Status reports the hub's subscription, a code waiting for it (until the
// code expires), and this period's counts.
func (s *Service) Status(ctx context.Context, hubID string) (Status, error) {
	status := Status{State: unknownSubscriber, DailyCap: s.Config.DailyCap, MonthlyCap: s.Config.MonthlyCap}
	sub, err := s.Store.SMSSubscriptionOf(ctx, hubID)
	switch {
	case err == nil:
		status.State = sub.State
		usage, err := s.Store.SMSUsageOf(ctx, sub.NumberHash, s.Now())
		if err != nil {
			return status, err
		}
		status.SentToday, status.SentThisMonth = usage.DayCount, usage.MonthCount
	case !errors.Is(err, store.ErrNotFound):
		return status, err
	}
	verification, err := s.Store.SMSVerificationOf(ctx, hubID)
	switch {
	case err == nil && !verification.ExpiresAt.After(s.Now()):
		// An expired code is no longer out; the row stays a day only so a
		// late confirmation is told the code expired.
	case err == nil:
		status.CodeExpiresAt = verification.ExpiresAt
		if status.State == unknownSubscriber {
			status.State = store.SMSPending
		}
	case !errors.Is(err, store.ErrNotFound):
		return status, err
	}
	return status, nil
}

// StartVerification texts a fresh code to the number and makes it the hub's
// verification, replacing an earlier code. The hub's subscription, if any,
// keeps working until the new number is confirmed. The number already active
// for this hub needs no code. lang picks the code text ("en" or "zh-CN").
func (s *Service) StartVerification(ctx context.Context, hubID, rawNumber, lang string) (Status, error) {
	number, ok := NormalizeNumber(rawNumber)
	if !ok {
		return Status{}, ErrInvalidNumber
	}
	if !s.Config.allowedNumber(number) {
		return Status{}, ErrCountry
	}
	if err := s.entitled(ctx, hubID); err != nil {
		return Status{}, err
	}
	numberHash := s.Vault.HashNumber(number)
	if sub, err := s.Store.SMSSubscriptionOf(ctx, hubID); err == nil && sub.NumberHash == numberHash && sub.State == store.SMSActive {
		return s.Status(ctx, hubID)
	}
	// The limits come before the opt-out check, so probing which numbers
	// opted out costs as much as sending codes.
	if err := s.take(ctx, "code:hub:"+hubID, codesPerHub, codesPerHubWindow, limit.Full("sms_codes_hub", codesPerHub, ErrTooMany)); err != nil {
		return Status{}, err
	}
	if err := s.take(ctx, "code:number:"+numberHash, codesPerNumber, codesPerNumberWindow, limit.Full("sms_codes_number", codesPerNumber, ErrTooMany)); err != nil {
		return Status{}, err
	}
	if err := s.take(ctx, "codes", s.Config.CodesPerHour, capWindow, ErrBusy); err != nil {
		return Status{}, err
	}
	code, err := randomCode()
	if err != nil {
		return Status{}, err
	}
	numberCT, err := s.Vault.Seal(number, numberLabel(hubID))
	if err != nil {
		return Status{}, err
	}
	now := s.Now()
	codeHash := s.Vault.HashCode(hubID, numberHash, code)
	verification := store.SMSVerification{HubID: hubID, NumberHash: numberHash, NumberCT: numberCT, CodeHash: codeHash, ExpiresAt: now.Add(CodeTTL)}
	switch err = s.Store.StartSMSVerification(ctx, verification, now); {
	case err == nil:
	case errors.Is(err, store.ErrOptedOut):
		return Status{}, ErrOptedOut
	case errors.Is(err, store.ErrNotFound):
		return Status{}, ErrUnknownHub
	default:
		return Status{}, err
	}
	if _, err = s.send(ctx, number, codeText(lang, code, s.Config.DailyCap)); err != nil {
		if deleteErr := s.Store.DeleteSMSVerification(ctx, hubID, codeHash); deleteErr != nil {
			log.Printf("sms: an unsent verification could not be removed")
		}
		if errors.Is(err, ErrRecipientStopped) {
			s.optOut(ctx, numberHash)
			return Status{}, ErrOptedOut
		}
		log.Printf("sms: a verification text failed: %v", err)
		return Status{}, ErrSendFailed
	}
	return s.Status(ctx, hubID)
}

// Confirm checks the code the user typed on the Mac. A right code makes the
// number the hub's active subscription and takes it from any other hub.
func (s *Service) Confirm(ctx context.Context, hubID, code string) (Status, error) {
	if err := s.allow("sms:confirm:"+hubID, confirmsPerHub, confirmsWindow); err != nil {
		return Status{}, limit.Full("sms_confirms", confirmsPerHub, err)
	}
	matches := func(numberHash, codeHash string) bool {
		return hmac.Equal([]byte(codeHash), []byte(s.Vault.HashCode(hubID, numberHash, code)))
	}
	_, displaced, err := s.Store.ConfirmSMS(ctx, hubID, matches, CodeAttempts, WrongCodesPerNumber, s.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return Status{}, ErrNoPending
	case errors.Is(err, store.ErrForbidden):
		return Status{}, ErrCodeWrong
	case errors.Is(err, store.ErrAttempts), errors.Is(err, store.ErrExpired):
		return Status{}, ErrCodeSpent
	case errors.Is(err, store.ErrLocked):
		return Status{}, &RetryError{Err: limit.Full("sms_wrong_codes", WrongCodesPerNumber, ErrTooMany), Wait: s.lockWait(ctx, hubID)}
	case errors.Is(err, store.ErrOptedOut):
		return Status{}, ErrOptedOut
	default:
		return Status{}, err
	}
	for _, other := range displaced {
		s.changed(other)
	}
	return s.Status(ctx, hubID)
}

// lockWait is how long the number of the hub's verification stays locked.
func (s *Service) lockWait(ctx context.Context, hubID string) time.Duration {
	verification, err := s.Store.SMSVerificationOf(ctx, hubID)
	if err != nil {
		return time.Hour
	}
	wait, err := s.Store.SMSLimitWait(ctx, store.WrongCodeKey(verification.NumberHash), s.Now())
	if err != nil || wait <= 0 {
		return time.Hour
	}
	return wait
}

// Unsubscribe removes the hub's subscription, its code and its encrypted
// number. The number's opt-out, if any, stays.
func (s *Service) Unsubscribe(ctx context.Context, hubID string) error {
	return s.Store.DeleteSMSSubscription(ctx, hubID)
}

// Send texts body to the hub's verified number. The relay checks the body,
// the entitlement, the state, the opt-out and the caps, then forgets the
// body: it is neither stored nor logged.
func (s *Service) Send(ctx context.Context, hubID, body string) (Status, error) {
	if !ValidBody(body) || !HubBody(body) {
		return Status{}, ErrInvalidBody
	}
	if err := s.entitled(ctx, hubID); err != nil {
		return Status{}, err
	}
	sub, usage, err := s.Store.ReserveSMSSend(ctx, hubID, s.Config.DailyCap, s.Config.MonthlyCap, s.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrOptedOut):
		return Status{}, ErrOptedOut
	case errors.Is(err, store.ErrNotFound):
		return Status{}, ErrNotActive
	case errors.Is(err, store.ErrQuota):
		refusal := &limit.Refusal{Name: "sms_daily", Count: int64(usage.DayCount), Cap: int64(s.Config.DailyCap), Err: ErrQuota}
		if usage.MonthCount >= s.Config.MonthlyCap {
			refusal = &limit.Refusal{Name: "sms_monthly", Count: int64(usage.MonthCount), Cap: int64(s.Config.MonthlyCap), Err: ErrQuota}
		}
		return Status{}, &RetryError{Err: refusal, Wait: quotaWait(usage, s.Config, s.Now())}
	default:
		return Status{}, err
	}
	refund := func() {
		if err := s.Store.RefundSMSSend(ctx, sub.NumberHash, usage); err != nil {
			log.Printf("sms: a refused text could not be refunded")
		}
	}
	if err = s.spend(ctx); err != nil {
		refund()
		return Status{}, err
	}
	number, err := s.Vault.Open(sub.NumberCT, numberLabel(hubID))
	if err != nil {
		refund()
		log.Printf("sms: a stored number does not open under the SMS key")
		return Status{}, ErrSendFailed
	}
	if _, err = s.send(ctx, number, body); err != nil {
		switch {
		case errors.Is(err, ErrRecipientStopped):
			refund()
			s.optOut(ctx, sub.NumberHash)
			return Status{}, ErrOptedOut
		case errors.Is(err, ErrRejected):
			refund()
		}
		log.Printf("sms: a text failed: %v", err)
		return Status{}, ErrSendFailed
	}
	return s.Status(ctx, hubID)
}

// quotaWait is the time until the cap that was hit resets: midnight UTC for
// the daily cap, the first of next month for the monthly one.
func quotaWait(usage store.SMSUsage, cfg Config, now time.Time) time.Duration {
	now = now.UTC()
	if usage.MonthCount >= cfg.MonthlyCap {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Sub(now)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1).Sub(now)
}

// HandleInbound verifies a provider webhook and acts on the text once. STOP
// and its synonyms opt the number out, START and UNSTOP opt it back in, HELP
// answers with the help line, and anything else from an active subscriber is
// queued for its hub. ErrBadSignature and ErrBadWebhook are the refusals; a
// text that is ignored returns nil. When acting fails the message id is
// forgotten again, so a retry would be handled; Twilio does not retry an
// incoming-message webhook unless the webhook URL asks for it, so such a text
// is usually lost (PROTOCOL.md 18.3).
func (s *Service) HandleInbound(ctx context.Context, r *http.Request) error {
	in, err := s.Provider.VerifyInbound(r)
	if err != nil {
		return err
	}
	from, ok := NormalizeNumber(in.From)
	if !ok {
		return nil
	}
	now := s.Now()
	messageHash := s.Vault.HashMessageID(in.MessageID)
	fresh, err := s.Store.RecordSMSInbound(ctx, messageHash, now)
	if err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	if err = s.act(ctx, from, in.Body, now); err != nil {
		// The request's own deadline may be what failed, so the id is
		// released under a fresh one.
		forgetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if forgetErr := s.Store.ForgetSMSInbound(forgetCtx, messageHash); forgetErr != nil {
			log.Printf("sms: a failed text could not be released for a retry")
		}
		return err
	}
	return nil
}

// act applies one inbound text. Keywords take effect for any sender, but
// only a number the relay could subscribe (allowedNumber) gets an answer, so
// texting HELP from international or premium numbers pumps no texts. The
// STOP confirmation goes only to a number the relay knew (a subscription or
// a code), at most a few times a day, and outside every hourly budget,
// because it is the one text a number that opted out still receives; the
// provider confirms STOP for numbers it alone knows. HELP and START answers
// spend the keyword budget, not the hubs' hourly cap. A number that opted out
// still gets an answer to HELP, which it asked for.
func (s *Service) act(ctx context.Context, from, body string, now time.Time) error {
	numberHash := s.Vault.HashNumber(from)
	answer := s.Config.allowedNumber(from)
	switch ClassifyKeyword(body) {
	case KeywordStop:
		change, err := s.Store.SMSOptOut(ctx, numberHash, now)
		if err != nil {
			return err
		}
		s.changedAll(change.Hubs)
		if answer && change.Changed && change.Known && s.allow("sms:stop:"+numberHash, stopRepliesPerNumber, stopReplyWindow) == nil {
			s.reply(ctx, from, stopReply, false)
		}
	case KeywordStart:
		change, err := s.Store.SMSOptIn(ctx, numberHash, now)
		if err != nil {
			return err
		}
		s.changedAll(change.Hubs)
		if answer && change.Changed && change.Known {
			s.reply(ctx, from, startText(s.Config.DailyCap), true)
		}
	case KeywordHelp:
		if answer && s.allow("sms:help:"+numberHash, helpPerNumber, inboundWindow) == nil {
			s.reply(ctx, from, helpText(s.Config.DailyCap, s.Config.HelpContact), true)
		}
	case KeywordNone:
		return s.forward(ctx, numberHash, body, now)
	}
	return nil
}

// optOut records an opt-out the provider reported and tells the hubs.
func (s *Service) optOut(ctx context.Context, numberHash string) {
	change, err := s.Store.SMSOptOut(ctx, numberHash, s.Now())
	if err != nil {
		log.Printf("sms: an opt-out could not be recorded")
		return
	}
	s.changedAll(change.Hubs)
}

// reply sends one of the relay's own texts; budgeted ones spend the keyword
// budget. Failures are logged without the number and dropped: the keyword
// already took effect.
func (s *Service) reply(ctx context.Context, to, body string, budgeted bool) {
	if budgeted && s.take(ctx, "keyword-answers", s.Config.KeywordAnswersPerHour, capWindow, ErrBusy) != nil {
		return
	}
	if _, err := s.send(ctx, to, body); err != nil {
		log.Printf("sms: a keyword reply failed: %v", err)
	}
}

// forward queues a reply from an active subscriber for its hub.
func (s *Service) forward(ctx context.Context, numberHash, body string, now time.Time) error {
	if s.allow("sms:in:"+numberHash, repliesPerNumber, inboundWindow) != nil {
		return nil
	}
	sub, err := s.Store.SMSSubscriptionByNumber(ctx, numberHash)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sub.State != store.SMSActive {
		return nil
	}
	body = cleanReply(body)
	if body == "" {
		return nil
	}
	id := uuid.NewString()
	sealed, err := s.Vault.Seal(body, replyLabel(sub.HubID, id))
	if err != nil {
		return err
	}
	if err = s.Store.AddSMSReply(ctx, store.SMSReply{ID: id, HubID: sub.HubID, BodyCT: sealed, ReceivedAt: now}, now.Add(ReplyTTL)); err != nil {
		return err
	}
	if s.OnReply != nil {
		s.OnReply(sub.HubID)
	}
	return nil
}

// Replies returns the hub's queued replies, opened, oldest first. A reply that
// does not open (the key changed) is dropped.
func (s *Service) Replies(ctx context.Context, hubID string, limit int) ([]Reply, error) {
	queued, err := s.Store.SMSRepliesOf(ctx, hubID, s.Now(), limit)
	if err != nil {
		return nil, err
	}
	replies := make([]Reply, 0, len(queued))
	for _, q := range queued {
		body, err := s.Vault.Open(q.BodyCT, replyLabel(hubID, q.ID))
		if err != nil {
			_ = s.Store.AckSMSReply(ctx, hubID, q.ID)
			continue
		}
		replies = append(replies, Reply{ID: q.ID, Body: body, At: q.ReceivedAt})
	}
	return replies, nil
}

// Ack deletes a reply the hub has taken.
func (s *Service) Ack(ctx context.Context, hubID, id string) error {
	return s.Store.AckSMSReply(ctx, hubID, id)
}

// Reseal moves every stored number and reply sealed under an earlier key to
// the current one, so the previous key can be dropped after one start with
// both keys. A value that opens under no key is left for its hub to replace
// (a subscription then answers send_failed until the hub subscribes again).
// It reports how many values it moved and how many it could not open.
func (s *Service) Reseal(ctx context.Context) (moved, stuck int, err error) {
	values, err := s.Store.SMSSealedValues(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, value := range values {
		if s.Vault.Current(value.CT) {
			continue
		}
		label := numberLabel(value.HubID)
		if value.Kind == store.SMSSealedReply {
			label = replyLabel(value.HubID, value.ID)
		}
		plaintext, err := s.Vault.Open(value.CT, label)
		if err != nil {
			stuck++
			continue
		}
		sealed, err := s.Vault.Seal(plaintext, label)
		if err != nil {
			return moved, stuck, err
		}
		if err = s.Store.ReplaceSMSSealed(ctx, value, sealed); err != nil {
			return moved, stuck, err
		}
		moved++
	}
	return moved, stuck, nil
}

func (s *Service) changed(hubID string) {
	if s.OnState != nil {
		s.OnState(hubID)
	}
}

func (s *Service) changedAll(hubs []string) {
	for _, hubID := range hubs {
		s.changed(hubID)
	}
}

// randomCode is six uniformly random decimal digits.
func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
