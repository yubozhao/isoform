// Package wire holds the byte-level rules shared by the relay's HTTP API, its
// hub WebSocket and its store. Ciphertext is opaque here: nothing decrypts.
package wire

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxSeq keeps sequence numbers inside JSON-safe integers.
	MaxSeq = 1<<53 - 1
	// AEADTag is the Poly1305 tag appended to every ciphertext.
	AEADTag = 16
	// MaxMessagePlaintext bounds req and res plaintext.
	MaxMessagePlaintext = 64 << 10
	// MaxBriefPlaintext bounds padded brief plaintext.
	MaxBriefPlaintext = 256 << 10
	// MaxPairingPlaintext bounds claim and approval plaintext.
	MaxPairingPlaintext = 4 << 10
	// MaxBodyBytes bounds every POST and PUT body.
	MaxBodyBytes = 64 << 10
	// MaxFrameBytes bounds hub frames other than brief.
	MaxFrameBytes = 256 << 10
	// MaxBriefFrameBytes bounds brief frames.
	MaxBriefFrameBytes = 512 << 10
)

// ErrInvalid reports a value that violates the protocol. Messages are static.
var ErrInvalid = errors.New("invalid value")

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	tokenPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	scopePattern  = regexp.MustCompile(`^[a-z]+(:[a-z]+)?$`)
	base64Pattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	pushPattern   = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// Envelope is the header the relay can read. CT stays opaque to the relay.
type Envelope struct {
	V    int    `json:"v"`
	Hub  string `json:"hub"`
	Dev  string `json:"dev"`
	Dir  string `json:"dir"`
	Seq  uint64 `json:"seq"`
	Kind string `json:"kind"`
	CT   string `json:"ct"`
}

// ParseEnvelope decodes one strict JSON envelope and validates it.
func ParseEnvelope(data []byte) (Envelope, error) {
	var env Envelope
	if err := DecodeStrict(bytes.NewReader(data), &env); err != nil {
		return Envelope{}, ErrInvalid
	}
	if err := env.Validate(); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// Validate checks the envelope header and the ciphertext size for its kind.
func (e Envelope) Validate() error {
	if e.V != 1 || !ValidUUID(e.Hub) || !ValidUUID(e.Dev) || e.Seq < 1 || e.Seq > MaxSeq {
		return ErrInvalid
	}
	var maxPlaintext int
	switch {
	case e.Dir == "d2h" && e.Kind == "req":
		maxPlaintext = MaxMessagePlaintext
	case e.Dir == "h2d" && (e.Kind == "res" || e.Kind == "event" || e.Kind == "forget_all"):
		maxPlaintext = MaxMessagePlaintext
	case e.Dir == "h2d" && e.Kind == "brief":
		maxPlaintext = MaxBriefPlaintext
	default:
		return ErrInvalid
	}
	if !ValidCiphertext(e.CT, maxPlaintext) {
		return ErrInvalid
	}
	return nil
}

// Canonical returns the bytes the relay stores and forwards for an envelope.
func (e Envelope) Canonical() ([]byte, error) {
	return json.Marshal(e)
}

// DecodeStrict decodes exactly one JSON value and rejects unknown fields.
func DecodeStrict(r io.Reader, v any) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

// ValidUUID accepts the canonical lowercase 36-character form only.
func ValidUUID(s string) bool { return uuidPattern.MatchString(s) }

// ValidToken accepts base64url without padding for 32 bytes: a credential,
// bearer, claim token or device public key.
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

// ValidDigest accepts a lowercase hex SHA-256 digest.
func ValidDigest(s string) bool { return digestPattern.MatchString(s) }

// ValidCiphertext accepts unpadded base64url whose decoded size is at least a
// tag and at most maxPlaintext plus the tag.
func ValidCiphertext(s string, maxPlaintext int) bool {
	if !base64Pattern.MatchString(s) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	return len(raw) > AEADTag && len(raw) <= maxPlaintext+AEADTag
}

// ValidScopes accepts unique, well-formed scope names, at most 16 of them.
func ValidScopes(scopes []string) bool {
	if len(scopes) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if len(scope) > 32 || !scopePattern.MatchString(scope) || seen[scope] {
			return false
		}
		seen[scope] = true
	}
	return true
}

// HasScope reports whether scope is granted.
func HasScope(scopes []string, scope string) bool {
	for _, granted := range scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

// ValidPushToken accepts an even-length hex APNs token of 32 to 100 bytes and
// returns it in lowercase.
func ValidPushToken(s string) (string, bool) {
	if len(s) < 64 || len(s) > 200 || len(s)%2 != 0 || !pushPattern.MatchString(s) {
		return "", false
	}
	return strings.ToLower(s), true
}

// ParseTime accepts only RFC 3339 UTC with a Z suffix and second precision.
func ParseTime(s string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil || !strings.HasSuffix(s, "Z") || parsed.Format(time.RFC3339) != s {
		return time.Time{}, ErrInvalid
	}
	return parsed.UTC(), nil
}

// FormatTime renders a time in the protocol's RFC 3339 form.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Digest returns the lowercase hex SHA-256 of the exact presented string.
func Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Short trims an identifier for logs. Callers pass only validated IDs.
func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
