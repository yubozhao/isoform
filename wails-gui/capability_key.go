package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// normalizeCapabilityKey lowercases value into a dotted key of letters and
// digits; an empty result falls back to a short hash of fallbackSeed.
func normalizeCapabilityKey(value, fallbackSeed string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastSeparator := false
	for _, r := range value {
		isAlphaNumeric := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if isAlphaNumeric {
			b.WriteRune(r)
			lastSeparator = false
			continue
		}
		if !lastSeparator && b.Len() > 0 {
			b.WriteByte('.')
			lastSeparator = true
		}
	}
	key := strings.Trim(b.String(), ".")
	if key != "" {
		return key
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(fallbackSeed)))
	return "behavior." + hex.EncodeToString(sum[:8])
}
