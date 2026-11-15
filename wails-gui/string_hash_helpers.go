package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Shared helpers that used to live in the shadow agent's files
// (docs/rework-2026-11.md: the shadow agent is being deleted).

// uniqueSortedStrings trims, drops empty and repeated values, and sorts.
func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// sha256Hex is the lowercase hex SHA-256 of payload.
func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
