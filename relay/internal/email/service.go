package email

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"isoform-relay/internal/limit"
	"isoform-relay/internal/store"
)

const (
	// CodeTTL is how long a verification code can be confirmed.
	CodeTTL = 10 * time.Minute
	// CodeAttempts is how many wrong codes spend a code.
	CodeAttempts = 5
	// SubjectMax and TextMax bound one email (PROTOCOL.md 19.3).
	SubjectMax = 150
	TextMax    = 8000
	// SubjectPrefix starts every subject a hub sends, so a hub cannot make
	// mail from the relay's sender look like anything but Isoform.
	SubjectPrefix = "Isoform"

	codesPerHub           = 3
	codesPerHubWindow     = time.Hour
	codesPerAddress       = 5
	codesPerAddressWindow = 24 * time.Hour
	capWindow             = time.Hour
	providerCallBudget    = 15 * time.Second
)

// Errors the hub routes map to responses. Their text is static.
var (
	ErrInvalidAddress = errors.New("the address must be one plain email address")
	ErrInvalidMessage = errors.New("an email needs a subject that starts with Isoform (at most 150 characters) and a plain-text body of at most 8000 characters")
	ErrNotActive      = errors.New("there is no verified address")
	ErrQuota          = errors.New("the email limit for today is reached")
	ErrNoPending      = errors.New("there is no verification waiting for a code")
	ErrCodeWrong      = errors.New("the code is not right")
	ErrCodeSpent      = errors.New("the code expired or was tried too often; send a new one")
	ErrTooMany        = errors.New("too many attempts; try again later")
	ErrBusy           = errors.New("email is paused for a while; try again later")
	ErrSendFailed     = errors.New("the email could not be sent")
	ErrUnknownHub     = errors.New("unknown hub")
)

// RetryError is a refusal that can be retried after Wait.
type RetryError struct {
	Err  error
	Wait time.Duration
}

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// Status is what a hub sees about its subscription. State is "none",
// "pending_verification" (a code is out and no address is verified yet) or
// "active". CodeExpiresAt is set while a code is out.
type Status struct {
	State         string
	CodeExpiresAt time.Time
	SentToday     int
	DailyCap      int
}

// Service runs the subscriptions and the sends.
type Service struct {
	Store    *store.Store
	Provider Provider
	Vault    *Vault
	Config   Config
	Now      func() time.Time
}

// NewService returns a service over the store with the given provider.
func NewService(st *store.Store, provider Provider, vault *Vault, cfg Config) *Service {
	return &Service{Store: st, Provider: provider, Vault: vault, Config: cfg, Now: time.Now}
}

// NormalizeAddress returns the bare address in lower case, or "" when raw is
// not exactly one plain address.
func NormalizeAddress(raw string) string {
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

// take counts one event against a stored limit.
func (s *Service) take(ctx context.Context, key string, n int, window time.Duration, refused error) error {
	ok, wait, err := s.Store.TakeSMSLimit(ctx, "email:"+key, n, window, s.Now())
	if err != nil {
		return err
	}
	if !ok {
		return &RetryError{Err: refused, Wait: wait}
	}
	return nil
}

// Status reports the hub's subscription and today's count.
func (s *Service) Status(ctx context.Context, hubID string) (Status, error) {
	now := s.Now()
	status := Status{State: "none", DailyCap: s.Config.DailyCap}
	if _, err := s.Store.EmailSubscriptionOf(ctx, hubID); err == nil {
		status.State = "active"
	} else if !errors.Is(err, store.ErrNotFound) {
		return Status{}, err
	}
	if v, err := s.Store.EmailVerificationOf(ctx, hubID); err == nil {
		if v.ExpiresAt.After(now) && v.Attempts < CodeAttempts {
			status.CodeExpiresAt = v.ExpiresAt
			if status.State == "none" {
				status.State = "pending_verification"
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return Status{}, err
	}
	usage, err := s.Store.EmailUsageOf(ctx, hubID, now)
	if err != nil {
		return Status{}, err
	}
	status.SentToday = usage.DayCount
	return status, nil
}

// StartVerification mails a fresh code to the address. The hub's current
// address, if any, keeps working until the new one is confirmed.
func (s *Service) StartVerification(ctx context.Context, hubID, rawAddress, lang string) (Status, error) {
	address := NormalizeAddress(rawAddress)
	if address == "" {
		return Status{}, ErrInvalidAddress
	}
	addressHash := s.Vault.HashAddress(address)
	if err := s.take(ctx, "codes:hub:"+hubID, codesPerHub, codesPerHubWindow, limit.Full("email_codes_hub", codesPerHub, ErrTooMany)); err != nil {
		return Status{}, err
	}
	if err := s.take(ctx, "codes:address:"+addressHash, codesPerAddress, codesPerAddressWindow, limit.Full("email_codes_address", codesPerAddress, ErrTooMany)); err != nil {
		return Status{}, err
	}
	if err := s.take(ctx, "codes:all", s.Config.CodesPerHour, capWindow, ErrBusy); err != nil {
		return Status{}, err
	}
	if err := s.take(ctx, "codes:all:day", s.Config.CodesPerDay, codesPerAddressWindow, ErrBusy); err != nil {
		return Status{}, err
	}
	code, err := sixDigits()
	if err != nil {
		return Status{}, err
	}
	sealed, err := s.Vault.Seal(address, addressLabel(hubID))
	if err != nil {
		return Status{}, err
	}
	codeHash := s.Vault.HashCode(hubID, addressHash, code)
	now := s.Now()
	if err := s.Store.StartEmailVerification(ctx, store.EmailVerification{
		HubID: hubID, AddressHash: addressHash, AddressCT: sealed, CodeHash: codeHash, ExpiresAt: now.Add(CodeTTL),
	}, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Status{}, ErrUnknownHub
		}
		return Status{}, err
	}
	subject, text := CodeMessage(lang, code)
	sendCtx, cancel := context.WithTimeout(ctx, providerCallBudget)
	defer cancel()
	if _, err := s.Provider.Send(sendCtx, Message{To: address, Subject: subject, Text: text}); err != nil {
		log.Printf("email: a verification code could not be sent: %v", err)
		_ = s.Store.DeleteEmailVerification(ctx, hubID, codeHash)
		return Status{}, ErrSendFailed
	}
	return s.Status(ctx, hubID)
}

// Confirm checks the code; a right one makes the address the hub's.
func (s *Service) Confirm(ctx context.Context, hubID, code string) (Status, error) {
	_, err := s.Store.ConfirmEmail(ctx, hubID, func(addressHash, codeHash string) bool {
		return hmac.Equal([]byte(s.Vault.HashCode(hubID, addressHash, code)), []byte(codeHash))
	}, CodeAttempts, s.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return Status{}, ErrNoPending
	case errors.Is(err, store.ErrAttempts), errors.Is(err, store.ErrExpired):
		return Status{}, ErrCodeSpent
	case errors.Is(err, store.ErrForbidden):
		return Status{}, ErrCodeWrong
	default:
		return Status{}, err
	}
	return s.Status(ctx, hubID)
}

// Unsubscribe removes the hub's address and any open code.
func (s *Service) Unsubscribe(ctx context.Context, hubID string) error {
	return s.Store.DeleteEmailSubscription(ctx, hubID)
}

// ValidMessage reports whether a hub's subject and text may be sent.
func ValidMessage(subject, text string) bool {
	if !strings.HasPrefix(subject, SubjectPrefix) || utf8.RuneCountInString(subject) > SubjectMax {
		return false
	}
	for _, r := range subject {
		if unicode.IsControl(r) {
			return false
		}
	}
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > TextMax {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

// Send mails the hub's message to the hub's verified address, and nowhere
// else: the request names no recipient.
func (s *Service) Send(ctx context.Context, hubID, subject, text string) (Status, error) {
	if !ValidMessage(subject, text) {
		return Status{}, ErrInvalidMessage
	}
	// The hub's own subscription and cap come first, so a hub without an
	// address or at its cap cannot spend the relay-wide budget.
	sub, usage, err := s.Store.ReserveEmailSend(ctx, hubID, s.Config.DailyCap, s.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return Status{}, ErrNotActive
	case errors.Is(err, store.ErrQuota):
		return Status{}, &limit.Refusal{Name: "email_daily", Count: int64(usage.DayCount), Cap: int64(s.Config.DailyCap), Err: ErrQuota}
	default:
		return Status{}, err
	}
	if err := s.take(ctx, "sends:all", s.Config.HourlyCap, capWindow, ErrBusy); err != nil {
		_ = s.Store.RefundEmailSend(ctx, hubID, usage)
		return Status{}, err
	}
	address, err := s.Vault.Open(sub.AddressCT, addressLabel(hubID))
	if err != nil {
		_ = s.Store.RefundEmailSend(ctx, hubID, usage)
		return Status{}, ErrSendFailed
	}
	sendCtx, cancel := context.WithTimeout(ctx, providerCallBudget)
	defer cancel()
	if _, err := s.Provider.Send(sendCtx, Message{To: address, Subject: subject, Text: text}); err != nil {
		log.Printf("email: a hub's email could not be sent: %v", err)
		if errors.Is(err, ErrRejected) {
			_ = s.Store.RefundEmailSend(ctx, hubID, usage)
		}
		return Status{}, ErrSendFailed
	}
	return s.Status(ctx, hubID)
}

func sixDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// CodeMessage is the verification email in a language ("en" or "zh-CN").
func CodeMessage(lang, code string) (string, string) {
	switch lang {
	case "zh-CN":
		return "Isoform 验证码：" + code,
			"你的 Isoform 验证码是 " + code + "，10 分钟内有效。\n\n" +
				"有人在一台电脑上的 Isoform 中请求把结果发送到这个邮箱。如果不是你，请忽略这封邮件；没有这个验证码，Isoform 不会给你发送任何邮件。"
	default:
		return "Your Isoform code: " + code,
			"Your Isoform code is " + code + ". It expires in 10 minutes.\n\n" +
				"Someone asked Isoform on a computer to send its results to this address. If it was not you, ignore this email; without the code, Isoform sends nothing here."
	}
}
