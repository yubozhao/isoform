package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	"wails-gui/internal/pii"
)

// briefPrivacyPrint is the privacy state a sealed brief was checked under: the
// privacy-change counter (advanced by briefPrivacyChanged), the Closed Eyes and
// observation pause state, and the sensitive-app list. It reads only in-memory
// settings, never a store, so it is cheap and never waits on a store that is
// still opening. A changed print seals the brief again before it is shown (the
// safety net for a privacy change that reached no hook).
func (a *App) briefPrivacyPrint() string {
	state := observationPauseSnapshot{}
	if a != nil {
		state = a.observationPauseSnapshot()
	}
	apps := pii.GetAllSensitiveApps()
	lines := make([]string, 0, len(apps))
	for _, entry := range apps {
		lines = append(lines, fmt.Sprintf("%v", entry))
	}
	sort.Strings(lines)
	sum := sha256.New()
	for _, line := range lines {
		sum.Write([]byte(line))
		sum.Write([]byte{0})
	}
	return strconv.FormatUint(mobileBriefPrivacyEpoch.Load(), 36) + "|" + strconv.FormatBool(state.Enabled) +
		"|" + fmt.Sprintf("%t", !state.ResumeAt.IsZero()) + "|" + hex.EncodeToString(sum.Sum(nil))
}
