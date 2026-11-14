package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strconv"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	mobileEnvelopeVersion     = 1
	mobileDirD2H              = "d2h"
	mobileDirH2D              = "h2d"
	mobileKindRequest         = "req"
	mobileKindResponse        = "res"
	mobileKindBrief           = "brief"
	mobileKindEvent           = "event"
	mobileMaxMessagePlaintext = 64 * 1024
	mobileMaxBriefPlaintext   = 256 * 1024
	mobileBriefPadBlock       = 4096
	mobileStreamMessage       = "message"
	mobileStreamBrief         = "brief"
	// mobileSendSeqSkip covers sequence numbers that may have been used after the
	// last durable write, so a restart can never reuse a nonce.
	mobileSendSeqSkip = 1000
	// mobileMaxSequence keeps every sequence number below 2^53-1, so it survives
	// JSON number handling on the relay and the phone (PROTOCOL.md section 6).
	mobileMaxSequence = 1<<53 - 2
)

var (
	errMobileEnvelopeRejected = errors.New("the encrypted message could not be authenticated")
	errMobileEnvelopeSize     = errors.New("the encrypted message is too large")
)

// mobileEnvelope is the d2h or h2d record the relay stores and forwards. The
// header fields are bound as associated data, so the relay cannot relabel it.
type mobileEnvelope struct {
	V    int    `json:"v"`
	Hub  string `json:"hub"`
	Dev  string `json:"dev"`
	Dir  string `json:"dir"`
	Seq  uint64 `json:"seq"`
	Kind string `json:"kind"`
	CT   string `json:"ct"`
	// from is the relay connection that delivered a d2h envelope; nil is the
	// pinned relay. It is never encoded. The ack goes back to that relay only
	// (mobile_replay_window.go).
	from *mobileRelayClient
}

type mobileSessionKeys struct {
	D2H []byte
	H2D []byte
}

// mobileDeriveSessionKeys is symmetric: the hub passes its private key and the
// device public key, and the phone does the reverse.
func mobileDeriveSessionKeys(private *ecdh.PrivateKey, peer *ecdh.PublicKey, hubID, deviceID string) (mobileSessionKeys, error) {
	shared, err := private.ECDH(peer)
	if err != nil {
		return mobileSessionKeys{}, err
	}
	salt := []byte("yansu-mobile-v1|" + hubID + "|" + deviceID)
	d2h, err := hkdf.Key(sha256.New, shared, salt, "d2h", 32)
	if err != nil {
		return mobileSessionKeys{}, err
	}
	h2d, err := hkdf.Key(sha256.New, shared, salt, "h2d", 32)
	if err != nil {
		return mobileSessionKeys{}, err
	}
	return mobileSessionKeys{D2H: d2h, H2D: h2d}, nil
}

func mobileEnvelopeNonce(seq uint64) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSize)
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}

func mobileEnvelopeAAD(env mobileEnvelope) []byte {
	return []byte("yansu-env-v1|" + strconv.Itoa(env.V) + "|" + env.Hub + "|" + env.Dev + "|" +
		env.Dir + "|" + strconv.FormatUint(env.Seq, 10) + "|" + env.Kind)
}

// mobileKindAllowed fixes which kinds travel in each direction.
func mobileKindAllowed(dir, kind string) bool {
	switch dir {
	case mobileDirD2H:
		return kind == mobileKindRequest
	case mobileDirH2D:
		return kind == mobileKindResponse || kind == mobileKindBrief || kind == mobileKindEvent
	default:
		return false
	}
}

func mobilePlaintextLimit(kind string) int {
	switch kind {
	case mobileKindBrief:
		return mobileMaxBriefPlaintext
	default:
		return mobileMaxMessagePlaintext
	}
}

func sealMobileEnvelope(key []byte, hubID, deviceID, dir string, seq uint64, kind string, plaintext []byte) (mobileEnvelope, error) {
	if seq == 0 || seq > mobileMaxSequence || !mobileKindAllowed(dir, kind) {
		return mobileEnvelope{}, errMobileEnvelopeRejected
	}
	if len(plaintext) > mobilePlaintextLimit(kind) || (kind == mobileKindBrief && len(plaintext)%mobileBriefPadBlock != 0) {
		return mobileEnvelope{}, errMobileEnvelopeSize
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return mobileEnvelope{}, err
	}
	env := mobileEnvelope{V: mobileEnvelopeVersion, Hub: hubID, Dev: deviceID, Dir: dir, Seq: seq, Kind: kind}
	ciphertext := aead.Seal(nil, mobileEnvelopeNonce(seq), plaintext, mobileEnvelopeAAD(env))
	env.CT = base64.RawURLEncoding.EncodeToString(ciphertext)
	return env, nil
}

// openMobileEnvelope authenticates the header and body. A brief's padding is
// removed before it is returned. The caller applies the replay check afterwards,
// because only an authenticated sequence number may be trusted.
func openMobileEnvelope(key []byte, env mobileEnvelope, hubID, deviceID, dir string) ([]byte, error) {
	if env.V != mobileEnvelopeVersion || env.Hub != hubID || env.Dev != deviceID || env.Dir != dir ||
		env.Seq == 0 || env.Seq > mobileMaxSequence || !mobileKindAllowed(dir, env.Kind) {
		return nil, errMobileEnvelopeRejected
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(env.CT)
	if err != nil || len(ciphertext) < chacha20poly1305.Overhead {
		return nil, errMobileEnvelopeRejected
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, mobileEnvelopeNonce(env.Seq), ciphertext, mobileEnvelopeAAD(env))
	if err != nil {
		return nil, errMobileEnvelopeRejected
	}
	if env.Kind == mobileKindBrief {
		return mobileUnpadBrief(plaintext)
	}
	if len(plaintext) > mobileMaxMessagePlaintext {
		return nil, errMobileEnvelopeSize
	}
	return plaintext, nil
}

// mobilePadBrief pads the document with 0x00 to a multiple of 4096 bytes.
func mobilePadBrief(document []byte) ([]byte, error) {
	size := (len(document) + mobileBriefPadBlock - 1) / mobileBriefPadBlock * mobileBriefPadBlock
	if size == 0 {
		size = mobileBriefPadBlock
	}
	if size > mobileMaxBriefPlaintext {
		return nil, errMobileEnvelopeSize
	}
	padded := make([]byte, size)
	copy(padded, document)
	return padded, nil
}

func mobileUnpadBrief(padded []byte) ([]byte, error) {
	if len(padded) == 0 || len(padded)%mobileBriefPadBlock != 0 || len(padded) > mobileMaxBriefPlaintext {
		return nil, errMobileEnvelopeSize
	}
	return bytes.TrimRight(padded, "\x00"), nil
}

// mobileStreamForKind names the receive stream a kind is checked against.
func mobileStreamForKind(kind string) string {
	switch kind {
	case mobileKindBrief:
		return mobileStreamBrief
	default:
		return mobileStreamMessage
	}
}

// lastAccepted and setLastAccepted keep the two receive streams independent,
// because a stored brief may be fetched after a later response was accepted.
func (record *mobileDeviceRecord) lastAccepted(stream string) uint64 {
	switch stream {
	case mobileStreamBrief:
		return record.LastAcceptedBriefSeq
	default:
		return record.LastAcceptedSeq
	}
}

func (record *mobileDeviceRecord) setLastAccepted(stream string, seq uint64) {
	switch stream {
	case mobileStreamBrief:
		record.LastAcceptedBriefSeq = seq
	default:
		record.LastAcceptedSeq = seq
	}
}
