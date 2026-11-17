package main

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	mobileInvitationPrefix         = "isoform://pair#"
	mobileLegacyInvitationPrefix   = "yansu://pair#"
	mobileMaxPairingFailures       = 3
	mobilePairingClaimNonceByte    = 0x01
	mobilePairingApprovalNonceByte = 0x02

	mobileScopeBriefRead    = "brief:read"
	mobileScopeBriefRefresh = "brief:refresh"
	mobileScopePresenceRead = "presence:read"
	mobileScopePush         = "push"
	mobileScopeDeskRead     = "desk:read" // retired with the Desk: older phones hold it, no route accepts it
	mobileScopeDecide       = "decide"    // retired with the Desk: older phones hold it, no route accepts it
	mobileScopeChatRead     = "chat:read"
	mobileScopeChatWrite    = "chat:write"
	mobileScopeMemoryFix    = "memory:correct"
	mobileScopeHandoff      = "handoff:create"

	// Release 4 (PROTOCOL.md section 20). All are declared at one re-pair;
	// routes that need them arrive step by step.
	mobileScopeBriefFeedback = "brief:feedback"
	mobileScopeOffersRead    = "offers:read"
	mobileScopeOffersDecide  = "offers:decide"
	mobileScopeGoalsRead     = "goals:read"
	mobileScopeGoalsWrite    = "goals:write"
	mobileScopeLibraryRead   = "library:read"
)

// mobileKnownScopes are the only scopes a device may hold (PROTOCOL.md sections 10, 15.1 and 20.1).
var mobileKnownScopes = []string{
	mobileScopeBriefRead, mobileScopeBriefRefresh, mobileScopePresenceRead, mobileScopePush,
	mobileScopeDeskRead, mobileScopeDecide,
	mobileScopeChatRead, mobileScopeChatWrite, mobileScopeMemoryFix, mobileScopeHandoff,
	mobileScopeBriefFeedback, mobileScopeOffersRead, mobileScopeOffersDecide,
	mobileScopeGoalsRead, mobileScopeGoalsWrite, mobileScopeLibraryRead,
	mobileScopeChatCorrect,
}

var errMobilePairingRejected = errors.New("the phone's pairing request could not be verified")

// mobileInvitation is the payload of isoform://pair#<base64url(JSON)>. The
// secret never reaches the relay; only the hash of the claim token does.
type mobileInvitation struct {
	V           int    `json:"v"`
	Relay       string `json:"relay,omitempty"`
	Hub         string `json:"hub"`
	Pairing     string `json:"pairing"`
	Secret      string `json:"secret"`
	HubPub      string `json:"hubPub"`
	Exp         int64  `json:"exp"`
	Iroh        string `json:"iroh,omitempty"`
	Warning     string `json:"warning,omitempty"`
	WarningCode string `json:"warningCode,omitempty"`
	WarningHost string `json:"warningHost,omitempty"`
}

// mobileOpenPairing is the single invitation this hub accepts at a time.
type mobileOpenPairing struct {
	ID             string
	Secret         []byte
	ClaimTokenHash string
	ExpiresAt      time.Time
	Invitation     string
	Failures       int
	Claim          *mobilePendingClaim
	Resolving      bool
}

// mobilePendingClaim is a decrypted claim that waits for the user's decision.
type mobilePendingClaim struct {
	DeviceID   string
	DeviceName string
	DevicePub  string
	BearerHash string
	Scopes     []string
	KPair      []byte
	Code       string
	Simulator  bool
}

type mobileClaimPlaintext struct {
	DeviceID        string   `json:"deviceId"`
	DeviceName      string   `json:"deviceName"`
	DevicePub       string   `json:"devicePub"`
	BearerHash      string   `json:"bearerHash"`
	Platform        string   `json:"platform"`
	AppVersion      string   `json:"appVersion"`
	Scopes          []string `json:"scopes"`
	PushEnvironment string   `json:"pushEnvironment"`
	Simulator       bool     `json:"simulator,omitempty"`
}

type mobileApprovalPlaintext struct {
	DeviceID  string   `json:"deviceId"`
	HubName   string   `json:"hubName"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expiresAt"`
	HubPub    string   `json:"hubPub"`
	// BriefStatus tells the new phone which brief comes right after pairing,
	// or when the first one will (PROTOCOL.md 3.5 and 8.2). Additive.
	BriefStatus *mobileBriefStatus `json:"briefStatus,omitempty"`
}

func encodeMobileInvitation(invitation mobileInvitation) (string, error) {
	raw, err := json.Marshal(invitation)
	if err != nil {
		return "", err
	}
	return mobileInvitationPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// mobileClaimToken is HMAC-SHA256 keyed by the invitation secret over the pairing
// ID, base64url encoded (PROTOCOL.md section 3.2).
func mobileClaimToken(secret []byte, pairingID string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("yansu-claim-v1|" + pairingID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// mobileClaimTokenHash is hex(sha256) over the claim token text as it is
// presented in a claim request, which is what the relay compares.
func mobileClaimTokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// mobilePairKey derives kPair from the X25519 shared secret and the invitation
// secret as HKDF salt (PROTOCOL.md section 3.3).
func mobilePairKey(private *ecdh.PrivateKey, peer *ecdh.PublicKey, secret []byte, hubID, pairingID string) ([]byte, error) {
	shared, err := private.ECDH(peer)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, shared, secret, "yansu-pair-v1|"+hubID+"|"+pairingID, 32)
}

func mobilePairingNonce(last byte) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSize)
	nonce[len(nonce)-1] = last
	return nonce
}

func sealMobilePairingMessage(key []byte, last byte, aad string, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, mobilePairingNonce(last), plaintext, []byte(aad)), nil
}

func openMobilePairingMessage(key []byte, last byte, aad string, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, mobilePairingNonce(last), ciphertext, []byte(aad))
	if err != nil {
		return nil, errMobilePairingRejected
	}
	return plaintext, nil
}

func mobileClaimAAD(hubID, pairingID string) string {
	return "yansu-claim-v1|" + hubID + "|" + pairingID
}

func mobileApprovalAAD(hubID, pairingID string) string {
	return "yansu-approval-v1|" + hubID + "|" + pairingID
}

// mobileConfirmationCode is the six digits both screens show (PROTOCOL.md 3.4).
func mobileConfirmationCode(kPair []byte) (string, error) {
	raw, err := hkdf.Key(sha256.New, kPair, nil, "yansu-confirm-v1", 4)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(raw)%1000000), nil
}

func mobileParsePublicKey(encoded string) (*ecdh.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errMobilePairingRejected
	}
	key, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return nil, errMobilePairingRejected
	}
	return key, nil
}

func mobileIsKnownScope(scope string) bool {
	for _, allowed := range mobileKnownScopes {
		if scope == allowed {
			return true
		}
	}
	return false
}

// mobileNormalizeScopes rejects any unknown scope, removes duplicates,
// and sorts the result so the stored and approved lists are canonical.
func mobileNormalizeScopes(scopes []string) ([]string, error) {
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if !mobileIsKnownScope(scope) {
			return nil, errMobilePairingRejected
		}
		if !mobileHasScope(result, scope) {
			result = append(result, scope)
		}
	}
	sort.Strings(result)
	return result, nil
}

// mobileSplitKnownScopes separates the scopes this Mac knows, in their order,
// from the ones it does not (sorted, without duplicates). A stored device record
// keeps the unknown ones without being granted them (PROTOCOL.md section 20.1).
func mobileSplitKnownScopes(scopes []string) (known, unknown []string) {
	known = make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if mobileIsKnownScope(scope) {
			known = append(known, scope)
		} else {
			unknown = append(unknown, scope)
		}
	}
	if len(unknown) > 0 {
		unknown = uniqueSortedStrings(unknown)
	}
	return known, unknown
}

func mobileHasScope(scopes []string, scope string) bool {
	for _, held := range scopes {
		if held == scope {
			return true
		}
	}
	return false
}

func mobileValidHexDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mobileValidPushEnvironment(value string) bool {
	switch value {
	case "development", "production":
		return true
	default:
		return false
	}
}

// decodeMobileClaim checks a decrypted claim against the clear fields of the
// claim frame. The device public key inside the ciphertext must equal the clear
// one, which is the key that produced kPair.
func decodeMobileClaim(raw []byte, frameDeviceID, frameDevicePub string) (mobileClaimPlaintext, error) {
	var claim mobileClaimPlaintext
	if err := mobileDecodeStrict(raw, &claim); err != nil {
		return claim, errMobilePairingRejected
	}
	name := strings.TrimSpace(claim.DeviceName)
	if claim.DeviceID != frameDeviceID || !mobileValidUUID(claim.DeviceID) || claim.DevicePub != frameDevicePub ||
		name == "" || len(name) > 120 || !utf8.ValidString(name) ||
		!mobileValidHexDigest(claim.BearerHash) || !mobileValidPushEnvironment(claim.PushEnvironment) ||
		claim.Platform == "" || len(claim.Platform) > 32 || claim.AppVersion == "" || len(claim.AppVersion) > 64 {
		return claim, errMobilePairingRejected
	}
	scopes, err := mobileNormalizeScopes(claim.Scopes)
	if err != nil {
		return claim, err
	}
	claim.DeviceName, claim.Scopes = name, scopes
	return claim, nil
}
