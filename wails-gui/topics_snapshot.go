package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"wails-gui/internal/interests"
	"wails-gui/internal/pii"
)

// topics-snapshot.json keeps the last verified topic view, each topic with the
// source references its verification relied on and the privacy-policy hash it
// was verified under. Reads filter it using live policy; withdrawals scrub it.

const (
	topicSnapshotSchemaVersion = 1
	topicSnapshotFileName      = "topics-snapshot.json"
)

// Reference scopes, one per kind of privacy withdrawal the design names.
const (
	topicRefApp       = "app"       // a paused or excluded app / bundle id
	topicRefWindow    = "window"    // a protected or private window
	topicRefSession   = "session"   // a capture session a topic reads through
	topicRefTimeRange = "timeRange" // the [start,end) span of one source
	topicRefAccount   = "account"   // a signed-in account a topic relies on
	topicRefSource    = "source"
)

// topicTimeRange is a half-open [Start, End) span in unix milliseconds. An
// exclusion with End == 0 is an open (still ongoing) window, matching the
// existing source-pause check.
type topicTimeRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end,omitempty"`
}

// topicSourceReference is one source a topic relies on, tagged with its scope.
// Value carries the identity (a bundle id, app name, session id, account or
// window marker); Start/End carry the span for the time-range scope.
type topicSourceReference struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	Start int64  `json:"start,omitempty"`
	End   int64  `json:"end,omitempty"`
}

func (r topicSourceReference) key() string {
	if r.Kind == topicRefTimeRange {
		return r.Kind + "\x00" + strconv.FormatInt(r.Start, 10) + "\x00" + strconv.FormatInt(r.End, 10)
	}
	return r.Kind + "\x00" + r.Value
}

// topicSnapshotEntry is one topic and the source references behind it.
type topicSnapshotEntry struct {
	Topic    TopicView              `json:"topic"`
	Refs     []topicSourceReference `json:"refs,omitempty"`
	Unproven bool                   `json:"unproven,omitempty"`
}

// topicSnapshot is the persisted verified view.
type topicSnapshot struct {
	Version    int                  `json:"version"`
	PolicyHash [sha256.Size]byte    `json:"policyHash"`
	At         time.Time            `json:"at"`
	Topics     []topicSnapshotEntry `json:"topics"`
}

// topicSnapshotStore reads and writes topics-snapshot.json atomically.
type topicSnapshotStore struct {
	path string
	mu   sync.Mutex
	cur  *topicSnapshot
}

func newTopicSnapshotStore(dir string) *topicSnapshotStore {
	if dir == "" {
		return &topicSnapshotStore{}
	}
	return &topicSnapshotStore{path: filepath.Join(dir, topicSnapshotFileName)}
}

// Load reads the snapshot from disk into the store and returns it. A missing,
// corrupt or foreign-schema file yields nil: no snapshot, treated as first run.
func (s *topicSnapshotStore) Load() *topicSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = s.loadLocked()
	return s.cur
}

func (s *topicSnapshotStore) loadLocked() *topicSnapshot {
	if s.path == "" {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var snap topicSnapshot
	if json.Unmarshal(raw, &snap) != nil {
		return nil
	}
	// Tolerant of unknown fields, strict about the schema: a version this
	// reader does not know means no snapshot.
	if snap.Version != topicSnapshotSchemaVersion {
		return nil
	}
	for index := range snap.Topics {
		if len(snap.Topics[index].Refs) == 0 {
			snap.Topics[index].Unproven = true
		}
	}
	return &snap
}

// Current returns the snapshot loaded at start or the last replaced one.
func (s *topicSnapshotStore) Current() *topicSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Replace atomically persists candidate and makes it Current. A nil candidate
// clears the store (a successful build that found no topics).
func (s *topicSnapshotStore) Replace(candidate *topicSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if candidate == nil {
		if s.cur != nil {
			candidate = &topicSnapshot{
				Version:    s.cur.Version,
				PolicyHash: s.cur.PolicyHash,
				At:         s.cur.At,
				Topics:     []topicSnapshotEntry{},
			}
			if err := s.saveLocked(candidate); err != nil {
				return err
			}
			s.cur = candidate
			return nil
		}
		if s.path != "" {
			if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		s.cur = nil
		return nil
	}
	if err := s.saveLocked(candidate); err != nil {
		return err
	}
	s.cur = candidate
	return nil
}

func (s *topicSnapshotStore) RemoveMatching(matches func(topicSnapshotEntry) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || matches == nil {
		return 0, nil
	}
	filtered := *s.cur
	filtered.Topics = make([]topicSnapshotEntry, 0, len(s.cur.Topics))
	removed := 0
	for _, entry := range s.cur.Topics {
		if matches(entry) {
			removed++
			continue
		}
		filtered.Topics = append(filtered.Topics, entry)
	}
	if removed == 0 {
		return 0, nil
	}
	var err error
	if s.path != "" {
		err = s.saveLocked(&filtered)
	}
	s.cur = &filtered
	return removed, err
}

func (s *topicsService) commitBuiltSnapshot(candidate *topicSnapshot, refresh, manual bool) bool {
	if s.snapshots == nil {
		return false
	}
	s.registrySnapshotMu.Lock()
	defer s.registrySnapshotMu.Unlock()
	s.registryWorkerMu.Lock()
	scopes := append([]privacyWithdrawalScope(nil), s.registryWithdrawalScopes...)
	s.registryWorkerMu.Unlock()
	candidate = filterTopicSnapshotWithdrawalScopes(candidate, scopes)
	published := s.snapshots.Current()
	replace := !refresh || manual || topicSnapshotChangeMatters(published, candidate)
	if replace {
		if err := s.snapshots.Replace(candidate); err != nil {
			log.Printf("[topics] snapshot store write failed: %v", err)
			return false
		}
	}
	s.registryWorkerMu.Lock()
	if len(scopes) <= len(s.registryWithdrawalScopes) {
		s.registryWithdrawalScopes = s.registryWithdrawalScopes[len(scopes):]
	}
	if refresh {
		s.registryLastSnapshotReplaced = replace
	}
	s.registryWorkerMu.Unlock()
	return replace
}

func filterTopicSnapshotWithdrawalScopes(candidate *topicSnapshot, scopes []privacyWithdrawalScope) *topicSnapshot {
	if candidate == nil || len(scopes) == 0 {
		return candidate
	}
	filtered := *candidate
	filtered.Topics = make([]topicSnapshotEntry, 0, len(candidate.Topics))
	for _, entry := range candidate.Topics {
		if entry.Unproven || len(entry.Refs) == 0 {
			continue
		}
		withdrawn := false
		for _, scope := range scopes {
			if topicRefsIntersectScope(entry.Refs, scope) {
				withdrawn = true
				break
			}
		}
		if !withdrawn {
			filtered.Topics = append(filtered.Topics, entry)
		}
	}
	if len(filtered.Topics) == 0 {
		filtered.Topics = []topicSnapshotEntry{}
	}
	return &filtered
}

// saveLocked writes a temp file and renames it over the target, so a crash
// between the write and the rename leaves the previous snapshot untouched.
func (s *topicSnapshotStore) saveLocked(snap *topicSnapshot) error {
	snap.Version = topicSnapshotSchemaVersion
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// topicPrivacyPolicy is the live set of exclusions the read path filters a
// snapshot against. It is pure data: no database handles, no app pointer.
type topicPrivacyPolicy struct {
	Apps       []string         `json:"apps,omitempty"`
	Windows    []string         `json:"windows,omitempty"`
	Sessions   []string         `json:"sessions,omitempty"`
	TimeRanges []topicTimeRange `json:"timeRanges,omitempty"`
	Accounts   []string         `json:"accounts,omitempty"`
}

type topicPrefixIndex [256][]string

func (index *topicPrefixIndex) add(prefix string) {
	if prefix != "" {
		index[prefix[0]] = append(index[prefix[0]], prefix)
	}
}

func (index *topicPrefixIndex) matches(value string) bool {
	if value == "" {
		return false
	}
	for _, prefix := range index[value[0]] {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

type topicAppMatcher struct {
	fastExactLower              map[string]struct{}
	policyExactASCII            map[string]struct{}
	policyExactUnicode          []string
	hasPolicyUnicode            bool
	policyExactValues           []string
	policyPrefixes              topicPrefixIndex
	fastPrefixes                topicPrefixIndex
	sensitivePrefixes           topicPrefixIndex
	sensitiveNames              map[string]struct{}
	sensitiveParserNames        map[string][]string
	sensitiveParserFastFallback map[string][]string
	voiceExact                  map[string]struct{}
	voicePrefixes               topicPrefixIndex
	voiceExactMinLen            [256]int
	voiceExactMaxLen            [256]int
	voicePrefixFirstByte        [256]bool
	fastCandidateFirstByte      [256]bool
	voiceFirstByte              [256]bool
	memoCapacity                int
	memo                        map[string]bool
}

func topicHasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

func newTopicAppMatcher(policyApps []string, userVoiceApps map[string]string, memoCapacity int) *topicAppMatcher {
	if memoCapacity > 256 {
		memoCapacity = 256
	}
	matcher := &topicAppMatcher{
		fastExactLower:       make(map[string]struct{}, len(policyApps)),
		policyExactASCII:     make(map[string]struct{}, len(policyApps)),
		sensitiveNames:       make(map[string]struct{}),
		sensitiveParserNames: make(map[string][]string),
		voiceExact:           make(map[string]struct{}, len(voiceInputAppBundleIDs)),
		memoCapacity:         memoCapacity,
	}
	bundlePrefixes, sensitiveNames := pii.SensitiveAppPolicySnapshot()
	for _, prefix := range bundlePrefixes {
		lowerPrefix := strings.ToLower(prefix)
		matcher.sensitivePrefixes.add(lowerPrefix)
		matcher.fastPrefixes.add(lowerPrefix)
	}
	for _, name := range sensitiveNames {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		matcher.sensitiveNames[lowerName] = struct{}{}
		matcher.fastExactLower[lowerName] = struct{}{}
	}
	for _, parser := range activityPrivacyParserApps {
		parserName := strings.ToLower(strings.TrimSpace(parser.Name))
		protected := false
		if _, ok := matcher.sensitiveNames[strings.ToLower(strings.TrimSpace(parser.Label))]; ok {
			protected = true
		}
		if !protected {
			for _, bundle := range parser.Bundles {
				if matcher.sensitivePrefixes.matches(strings.ToLower(bundle)) {
					protected = true
					break
				}
			}
		}
		if protected {
			matcher.sensitiveParserNames[parserName] = append(matcher.sensitiveParserNames[parserName], parser.Name)
			if parser.Name == strings.TrimSpace(parser.Name) && !topicHasNonASCII(parser.Name) {
				matcher.fastExactLower[parserName] = struct{}{}
			} else {
				if matcher.sensitiveParserFastFallback == nil {
					matcher.sensitiveParserFastFallback = make(map[string][]string)
				}
				matcher.sensitiveParserFastFallback[parserName] = append(matcher.sensitiveParserFastFallback[parserName], parser.Name)
			}
		}
	}

	seenPolicyValues := make(map[string]struct{}, len(policyApps))
	seenPolicyPrefixes := make(map[string]struct{}, len(policyApps))
	for _, app := range policyApps {
		if _, ok := seenPolicyValues[app]; !ok {
			seenPolicyValues[app] = struct{}{}
			matcher.policyExactValues = append(matcher.policyExactValues, app)
			if topicHasNonASCII(app) {
				matcher.hasPolicyUnicode = true
				matcher.policyExactUnicode = append(matcher.policyExactUnicode, app)
			} else {
				lowerApp := strings.ToLower(app)
				matcher.policyExactASCII[lowerApp] = struct{}{}
				matcher.fastExactLower[lowerApp] = struct{}{}
			}
		}
		if !strings.Contains(app, ".") {
			continue
		}
		prefix := strings.ToLower(app)
		if _, ok := seenPolicyPrefixes[prefix]; !ok {
			seenPolicyPrefixes[prefix] = struct{}{}
			matcher.policyPrefixes.add(prefix)
			matcher.fastPrefixes.add(prefix)
		}
	}

	for bundleID := range voiceInputAppBundleIDs {
		matcher.voiceExact[bundleID] = struct{}{}
		if bundleID != "" {
			matcher.voiceFirstByte[bundleID[0]] = true
			length := len(bundleID)
			if matcher.voiceExactMinLen[bundleID[0]] == 0 || length < matcher.voiceExactMinLen[bundleID[0]] {
				matcher.voiceExactMinLen[bundleID[0]] = length
			}
			if length > matcher.voiceExactMaxLen[bundleID[0]] {
				matcher.voiceExactMaxLen[bundleID[0]] = length
			}
		}
	}
	for bundleID := range userVoiceApps {
		if bundleID != "" {
			matcher.voicePrefixes.add(bundleID)
			matcher.voiceFirstByte[bundleID[0]] = true
			matcher.voicePrefixFirstByte[bundleID[0]] = true
		}
	}
	for value := range matcher.fastExactLower {
		if value != "" {
			topicAddFastCandidateByte(matcher, value[0])
		}
	}
	for firstByte, prefixes := range matcher.fastPrefixes {
		if len(prefixes) > 0 {
			topicAddFastCandidateByte(matcher, byte(firstByte))
		}
	}
	for name := range matcher.sensitiveParserNames {
		if name != "" {
			topicAddFastCandidateByte(matcher, name[0])
		}
	}
	for name := range matcher.sensitiveParserFastFallback {
		if name != "" {
			topicAddFastCandidateByte(matcher, name[0])
		}
	}
	return matcher
}

func topicAddFastCandidateByte(matcher *topicAppMatcher, value byte) {
	matcher.fastCandidateFirstByte[value] = true
	if value >= 'a' && value <= 'z' {
		matcher.fastCandidateFirstByte[value-('a'-'A')] = true
	}
}

func (m *topicAppMatcher) excludes(value string) bool {
	if !topicAppMatcherMayMatch(m, value) &&
		!(len(value) > len("app:") && value[0] == 'a' && value[1] == 'p' && value[2] == 'p' && value[3] == ':') {
		return false
	}
	return m.excludesCandidate(value)
}

func (m *topicAppMatcher) excludesCandidate(value string) bool {
	if m.memo != nil {
		if excluded, ok := m.memo[value]; ok {
			return excluded
		}
	}

	lowerValue := strings.ToLower(value)
	trimmed := strings.TrimSpace(value)
	excluded := false
	if trimmed == value && !strings.HasPrefix(value, "app:") && !topicHasNonASCII(value) {
		if _, ok := m.fastExactLower[lowerValue]; ok {
			excluded = true
		}
		if !excluded {
			for _, parserName := range m.sensitiveParserFastFallback[lowerValue] {
				if strings.EqualFold(value, parserName) {
					excluded = true
					break
				}
			}
		}
		if !excluded {
			for _, app := range m.policyExactUnicode {
				if strings.EqualFold(value, app) {
					excluded = true
					break
				}
			}
		}
		if !excluded && m.fastPrefixes.matches(lowerValue) {
			excluded = true
		}
		if !excluded && len(value) > 0 && m.voiceFirstByte[value[0]] {
			if _, ok := m.voiceExact[value]; ok {
				excluded = true
			} else {
				for _, listed := range m.voicePrefixes[value[0]] {
					if strings.HasPrefix(value, listed) || strings.HasPrefix(listed, value) {
						excluded = true
						break
					}
				}
			}
		}
	} else {
		lowerName := strings.TrimSpace(lowerValue)
		lowerBundle := lowerName
		if strings.HasPrefix(trimmed, "app:") {
			lowerBundle = strings.TrimPrefix(lowerName, "app:")
		}

		if _, ok := m.sensitiveNames[lowerName]; ok {
			excluded = true
		} else if m.sensitivePrefixes.matches(lowerBundle) {
			excluded = true
		} else {
			for _, parserName := range m.sensitiveParserNames[lowerName] {
				if strings.EqualFold(trimmed, parserName) {
					excluded = true
					break
				}
			}
		}

		if !excluded {
			if topicHasNonASCII(value) {
				for _, app := range m.policyExactValues {
					if strings.EqualFold(value, app) {
						excluded = true
						break
					}
				}
			} else if _, ok := m.policyExactASCII[lowerValue]; ok {
				excluded = true
			} else {
				for _, app := range m.policyExactUnicode {
					if strings.EqualFold(value, app) {
						excluded = true
						break
					}
				}
			}
		}
		if !excluded && m.policyPrefixes.matches(lowerValue) {
			excluded = true
		}
		if !excluded && len(value) > 0 && m.voiceFirstByte[value[0]] {
			if _, ok := m.voiceExact[value]; ok {
				excluded = true
			} else {
				for _, listed := range m.voicePrefixes[value[0]] {
					if strings.HasPrefix(value, listed) || strings.HasPrefix(listed, value) {
						excluded = true
						break
					}
				}
			}
		}
	}
	if m.memo == nil {
		m.memo = make(map[string]bool, m.memoCapacity)
	}
	m.memo[value] = excluded
	return excluded
}

func topicAppMatcherMayMatch(m *topicAppMatcher, value string) bool {
	if value == "" || m.hasPolicyUnicode {
		return true
	}
	firstByte := value[0]
	if firstByte >= 0x80 || firstByte <= ' ' {
		return true
	}
	if m.fastCandidateFirstByte[firstByte] || m.voicePrefixFirstByte[firstByte] {
		return true
	}
	length := len(value)
	if length >= m.voiceExactMinLen[firstByte] && length <= m.voiceExactMaxLen[firstByte] {
		return true
	}
	return false
}

func topicAppReferenceCount(snap *topicSnapshot) int {
	count := 0
	for _, entry := range snap.Topics {
		for _, ref := range entry.Refs {
			if ref.Kind == topicRefApp {
				count++
			}
		}
	}
	return count
}

func newTopicSnapshotAppMatcher(snap *topicSnapshot, policyApps []string, userVoiceApps map[string]string) *topicAppMatcher {
	return newTopicAppMatcher(policyApps, userVoiceApps, topicAppReferenceCount(snap))
}

// filterTopicsSnapshot returns the views whose stored references stay clear of
// the exclusions. It reads only the snapshot and the policy, never the
// database. A topic disappears as soon as one reference falls inside an
// excluded scope.
func filterTopicsSnapshot(snap *topicSnapshot, policy topicPrivacyPolicy) []TopicView {
	if snap == nil {
		return nil
	}
	showUnproven := false
	for _, entry := range snap.Topics {
		if !entry.Unproven {
			continue
		}
		current, err := currentTopicPolicyHash()
		showUnproven = err == nil && current == snap.PolicyHash
		break
	}
	windows := topicStringSet(policy.Windows)
	sessions := topicStringSet(policy.Sessions)
	accounts := topicStringSet(policy.Accounts)
	appMatcher := newTopicSnapshotAppMatcher(snap, policy.Apps, GetUserVoiceInputApps())
	out := make([]TopicView, 0, len(snap.Topics))
	for _, entry := range snap.Topics {
		if entry.Unproven && !showUnproven {
			continue
		}
		if topicRefsExcludedWithMatcher(entry.Refs, appMatcher, windows, sessions, accounts, policy.TimeRanges) {
			continue
		}
		if !topicDisplayNameSafe(entry.Topic.Name) {
			continue
		}
		out = append(out, entry.Topic)
	}
	return out
}

func liveTopicPrivacyPolicy() topicPrivacyPolicy {
	sensitive := pii.GetUserSensitiveApps()
	voice := GetUserVoiceInputApps()
	apps := make([]string, 0, len(sensitive)+len(voice))
	for app, name := range sensitive {
		apps = append(apps, app, name)
	}
	for app, name := range voice {
		apps = append(apps, app, name)
	}
	for app, name := range voiceInputAppBundleIDs {
		apps = append(apps, app, name)
	}
	return topicPrivacyPolicy{Apps: apps}
}

func topicStringSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func topicRefsExcludedWithMatcher(refs []topicSourceReference, appMatcher *topicAppMatcher, windows, sessions, accounts map[string]bool, ranges []topicTimeRange) bool {
	for _, ref := range refs {
		switch ref.Kind {
		case topicRefApp:
			value := ref.Value
			if !topicAppMatcherMayMatch(appMatcher, value) &&
				!(len(value) > len("app:") && value[0] == 'a' && value[1] == 'p' && value[2] == 'p' && value[3] == ':') {
				continue
			}
			if appMatcher.excludesCandidate(value) {
				return true
			}
		case topicRefWindow:
			if windows[ref.Value] {
				return true
			}
		case topicRefSession:
			if sessions[ref.Value] {
				return true
			}
		case topicRefAccount:
			if accounts[ref.Value] {
				return true
			}
		case topicRefTimeRange:
			if topicRefTimeRangeExcluded(ref, ranges) {
				return true
			}
		}
	}
	return false
}

func topicVoiceInputAppReference(bundleID string, userApps map[string]string) bool {
	if _, ok := voiceInputAppBundleIDs[bundleID]; ok {
		return true
	}
	for listed := range userApps {
		if bundlesMatch(bundleID, listed) {
			return true
		}
	}
	return false
}

func topicRefTimeRangeExcluded(ref topicSourceReference, ranges []topicTimeRange) bool {
	if ref.End <= ref.Start {
		return false
	}
	for _, excluded := range ranges {
		if ref.End > excluded.Start && (excluded.End == 0 || ref.Start < excluded.End) {
			return true
		}
	}
	return false
}

func topicRefsIntersectScope(refs []topicSourceReference, scope privacyWithdrawalScope) bool {
	switch scope.Kind {
	case privacyWithdrawalApp:
		for _, ref := range refs {
			bundlePrefix := strings.Contains(scope.ID, ".") && strings.HasPrefix(strings.ToLower(ref.Value), strings.ToLower(scope.ID))
			if ref.Kind == topicRefApp && (strings.EqualFold(ref.Value, scope.ID) || bundlePrefix) {
				return true
			}
		}
	case privacyWithdrawalWindow:
		for _, ref := range refs {
			if ref.Kind == topicRefWindow && ref.Value == scope.ID {
				return true
			}
		}
	case privacyWithdrawalSession:
		for _, ref := range refs {
			if ref.Kind == topicRefSession && ref.Value == scope.ID {
				return true
			}
		}
	case privacyWithdrawalTimeRange:
		excluded := []topicTimeRange{{Start: scope.StartMs, End: scope.EndMs}}
		for _, ref := range refs {
			if ref.Kind == topicRefTimeRange && topicRefTimeRangeExcluded(ref, excluded) {
				return true
			}
		}
	case privacyWithdrawalAccount:
		for _, ref := range refs {
			if ref.Kind == topicRefAccount &&
				(ref.Value == scope.ID || strings.HasPrefix(ref.Value, scope.ID+":")) {
				return true
			}
		}
	case privacyWithdrawalSource:
		key := scope.SourceKind + ":" + strings.TrimPrefix(scope.ID, scope.SourceKind+":")
		for _, ref := range refs {
			if ref.Kind == topicRefSource &&
				(ref.Value == key || strings.HasPrefix(ref.Value, key+":")) {
				return true
			}
		}
	case privacyWithdrawalPolicyChange:
		return false
	}
	return false
}

// currentTopicPolicyHash is the hash of the policy a verified view was built
// under: [closedEyes(false), sensitive apps, voice-input apps]. It mirrors the
// expression the persisted Today view is checked against on a cold read.
func currentTopicPolicyHash() ([sha256.Size]byte, error) {
	raw, err := json.Marshal([]any{false, pii.GetUserSensitiveApps(), GetUserVoiceInputApps()})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

// migrateSnapshotIfNeeded lifts the persisted last-good Today view into the
// snapshot store so a cold start keeps showing it. The old view carries no
// references, so it is migrated only while the policy it was verified under
// still matches: a policy change must rebuild, never show an unfilterable view.
func (s *topicsService) migrateSnapshotIfNeeded() {
	if s.snapshots == nil || s.snapshots.Current() != nil {
		return
	}
	if s.store == nil {
		return
	}
	today := s.store.load().Today
	if today == nil || len(today.Views) == 0 {
		return
	}
	current, err := currentTopicPolicyHash()
	if err != nil || current != today.Policy {
		return
	}
	entries := make([]topicSnapshotEntry, 0, len(today.Views))
	for _, view := range today.Views {
		entries = append(entries, topicSnapshotEntry{Topic: view, Unproven: true})
	}
	candidate := &topicSnapshot{Version: topicSnapshotSchemaVersion, PolicyHash: today.Policy, At: today.At, Topics: entries}
	if err := s.snapshots.Replace(candidate); err != nil {
		log.Printf("[topics] snapshot migration failed: %v", err)
	}
}

// topicActivitySourceReferences resolves the privacy provenance of the already
// filtered activities into per-activity references, reusing the same batched
// run/session lookups as the source filter. It runs only on the build path; a
// nil map means the resolution failed and the caller skips the snapshot write.
func (a *App) topicActivitySourceReferences(ctx context.Context, activities []ActivityRow) (map[string][]topicSourceReference, error) {
	out := make(map[string][]topicSourceReference, len(activities))
	if a == nil || a.activity == nil || a.activity.store == nil || len(activities) == 0 {
		return out, nil
	}
	store := a.activity.store
	activityRuns := make(map[string][]string, len(activities))
	runIDs := map[string]bool{}
	for _, row := range activities {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids := composeParseStrings(row.RunIDs)
		if len(ids) == 0 || len(ids) > 64 {
			continue
		}
		activityRuns[row.ID] = ids
		for _, id := range ids {
			runIDs[id] = true
		}
	}
	if len(runIDs) == 0 {
		return out, nil
	}
	encoded, _ := json.Marshal(sortedTopicKeys(runIDs))
	cursor, err := store.reader().QueryContext(ctx, `SELECT `+timelineRunColumns+` FROM activity_timeline_groups WHERE id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return nil, err
	}
	runs, err := scanTimelineRunRowsContext(ctx, cursor)
	cursor.Close()
	if err != nil {
		return nil, err
	}
	byRun := make(map[string]ActivityTimelineRunRow, len(runs))
	sessionIDs := map[string]bool{}
	for _, run := range runs {
		byRun[run.ID] = run
		if activitySourceAppIsSensitive(run.AppKey, run.AppName) || len(run.SessionIDs) > 64 {
			continue
		}
		for _, id := range run.SessionIDs {
			sessionIDs[id] = true
		}
	}
	sessions, err := store.GetSessionsByIDsContext(ctx, sortedTopicKeys(sessionIDs))
	if err != nil {
		return nil, err
	}
	for activityID, runList := range activityRuns {
		if refs := topicReferencesForRuns(runList, byRun, sessions); len(refs) > 0 {
			out[activityID] = refs
		}
	}
	return out, ctx.Err()
}

func topicReferencesForRuns(runIDs []string, runs map[string]ActivityTimelineRunRow, sessions map[string]*ActivitySessionRow) []topicSourceReference {
	seen := map[string]bool{}
	var refs []topicSourceReference
	add := func(ref topicSourceReference) {
		if ref.Kind == topicRefTimeRange {
			if ref.End <= ref.Start {
				return
			}
		} else if ref.Value == "" {
			return
		}
		if seen[ref.key()] {
			return
		}
		seen[ref.key()] = true
		refs = append(refs, ref)
	}
	for _, runID := range runIDs {
		run, ok := runs[runID]
		if !ok || activitySourceAppIsSensitive(run.AppKey, run.AppName) {
			continue
		}
		if run.AppKey != "" && run.AppKey != "unknown" {
			add(topicSourceReference{Kind: topicRefApp, Value: run.AppKey})
		}
		if run.AppName != "" && !strings.EqualFold(run.AppName, "unknown") {
			add(topicSourceReference{Kind: topicRefApp, Value: run.AppName})
		}
		for _, sessionID := range run.SessionIDs {
			session := sessions[sessionID]
			if session == nil {
				continue
			}
			add(topicSourceReference{Kind: topicRefSession, Value: session.ID})
			add(topicSourceReference{Kind: topicRefTimeRange, Start: session.StartTime, End: session.EndTime})
			var appNames []string
			if session.AppNames != "" {
				_ = json.Unmarshal([]byte(session.AppNames), &appNames)
			}
			for _, name := range appNames {
				if name != "" {
					add(topicSourceReference{Kind: topicRefApp, Value: name})
				}
			}
			if session.MeetingApp != "" {
				add(topicSourceReference{Kind: topicRefApp, Value: session.MeetingApp})
			}
		}
	}
	return refs
}

// buildTopicSnapshot assembles the persisted form of one verified registry
// result: the views, each with the references it relies on, and the policy
// hash the build verified them under.
func buildTopicSnapshot(result topicRegistryResult, now time.Time, policyHash [sha256.Size]byte, refsByActivity map[string][]topicSourceReference, storedInterests []interests.Interest) *topicSnapshot {
	snapshot := &topicSnapshot{Version: topicSnapshotSchemaVersion, PolicyHash: policyHash, At: now, Topics: []topicSnapshotEntry{}}
	if len(result.views) == 0 {
		return snapshot
	}
	entries := make([]topicSnapshotEntry, 0, len(result.views))
	for _, view := range result.views {
		entries = append(entries, topicSnapshotEntry{Topic: view, Refs: topicViewSourceReferences(view, result, refsByActivity, storedInterests)})
	}
	snapshot.Topics = entries
	return snapshot
}

func topicViewSourceReferences(view TopicView, result topicRegistryResult, refsByActivity map[string][]topicSourceReference, storedInterests []interests.Interest) []topicSourceReference {
	seen := map[string]bool{}
	var out []topicSourceReference
	add := func(ref topicSourceReference) {
		if seen[ref.key()] {
			return
		}
		seen[ref.key()] = true
		out = append(out, ref)
	}
	if view.Kind == TopicKindProject {
		for _, project := range result.projects {
			if project.ID != view.ID {
				continue
			}
			for _, id := range project.ActivityIDs {
				add(topicSourceReference{Kind: topicRefSource, Value: "activity:" + id})
				for _, ref := range refsByActivity[id] {
					add(ref)
				}
			}
			for _, id := range project.GoalIDs {
				add(topicSourceReference{Kind: topicRefSource, Value: "purpose:" + id})
			}
			break
		}
		return out
	}
	// A following topic relies on the activity evidence its interest kept.
	for _, it := range storedInterests {
		if it.ID != view.ID {
			continue
		}
		for _, ref := range it.Evidence {
			source := string(ref.Source)
			add(topicSourceReference{Kind: topicRefSource, Value: source + ":" + strings.TrimPrefix(ref.Key, source+":")})
			if ref.Source == interests.SourceActivity {
				id, ok := strings.CutPrefix(ref.Key, "activity:")
				if ok && id != "" {
					for _, source := range refsByActivity[id] {
						add(source)
					}
				}
			}
		}
		break
	}
	return out
}

func topicSnapshotChangeMatters(published, candidate *topicSnapshot) bool {
	if published == nil || candidate == nil {
		return published != candidate
	}
	for _, entry := range published.Topics {
		if entry.Unproven {
			return true
		}
	}
	if published.PolicyHash != candidate.PolicyHash || len(published.Topics) != len(candidate.Topics) {
		return true
	}
	publishedByID := make(map[string]topicSnapshotEntry, len(published.Topics))
	publishedRanks := make(map[string]int, len(published.Topics))
	for rank, entry := range published.Topics {
		id := entry.Topic.ID
		publishedByID[id] = entry
		publishedRanks[id] = rank
	}
	for rank, entry := range candidate.Topics {
		id := entry.Topic.ID
		old, ok := publishedByID[id]
		if !ok {
			return true
		}
		if abs(rank-publishedRanks[id]) > topicSnapshotRankThreshold ||
			old.Topic.Name != entry.Topic.Name || old.Topic.Status != entry.Topic.Status ||
			topicSummarySimilarity(topicSummaryTokens(old.Topic), topicSummaryTokens(entry.Topic)) < topicSnapshotSummaryThreshold {
			return true
		}
	}
	return false
}

func topicSummaryTokens(view TopicView) map[string]bool {
	tokens := map[string]bool{}
	for _, moment := range []*TopicMoment{view.Latest, view.Next} {
		if moment == nil {
			continue
		}
		var token strings.Builder
		for _, r := range strings.ToLower(moment.Text) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				token.WriteRune(r)
			} else if token.Len() > 0 {
				tokens[token.String()] = true
				token.Reset()
			}
		}
		if token.Len() > 0 {
			tokens[token.String()] = true
		}
	}
	return tokens
}

func topicSummarySimilarity(left, right map[string]bool) float64 {
	union, intersection := map[string]bool{}, 0
	for token := range left {
		union[token] = true
	}
	for token := range right {
		union[token] = true
		if left[token] {
			intersection++
		}
	}
	if len(union) == 0 {
		return 1
	}
	return float64(intersection) / float64(len(union))
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
