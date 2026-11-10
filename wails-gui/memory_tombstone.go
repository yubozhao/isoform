package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const memoryForgetRetentionDays = 30
const memoryTombstonesFile = ".forgotten.json"

// Writers take a read lock before their per-file lock; forgetting holds the
// exclusive lock through tombstone persistence and deletion. This closes the
// window where a new observation can be written after the forget scan.
var memoryMutationMu sync.RWMutex
var memoryTombstoneMu sync.Mutex

func acquireMemoryMutationRead() func() {
	memoryMutationMu.RLock()
	var once sync.Once
	return func() { once.Do(memoryMutationMu.RUnlock) }
}

var errMemoryForgotten = errors.New("memory is suppressed by a recent forget request")
var errMemoryUserEdited = errors.New("observations cannot overwrite a user-edited memory")

const memoryTombstoneVersion = 2

type memoryTombstone struct {
	Version          int                    `json:"version"`
	Keys             []string               `json:"keys"`
	PreservedRecords []string               `json:"preservedRecords,omitempty"`
	Semantics        *memoryForgetSemantics `json:"semantics,omitempty"`
	ExpiresAt        int64                  `json:"expiresAt"`
}

func memoryForgetHash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", hash[:])
}

func memoryForgetIDKey(id string) string {
	return memoryForgetHash("id:" + strings.TrimSuffix(strings.TrimSpace(id), ".md"))
}

// Identity, complete records, and individual assertions can match a future
// observation. Unchanged independent versions and explicit edits are exempt
// from content matching; source sessions never identify a forgotten fact.
func memoryForgetKeys(id, content string) []string {
	keys := append([]string{memoryForgetIDKey(id)}, memoryDeterministicFactKeys(content)...)
	text := strings.Join(NormalizeForMatch(memorySemanticInput(content)), " ")
	if text != "" {
		keys = append(keys, memoryForgetHash("record:"+text))
	}
	sort.Strings(keys)
	return keys
}

func memoryPreservedRecordKey(id, content string) string {
	return memoryForgetHash(memoryForgetIDKey(id) + "\x00" + memorySemanticInput(content))
}

func memoryTombstonePreserves(tombstone memoryTombstone, id, content string) bool {
	key := memoryPreservedRecordKey(id, content)
	for _, preserved := range tombstone.PreservedRecords {
		if preserved == key {
			return true
		}
	}
	return false
}

func memoryTombstoneHasKey(tombstone memoryTombstone, key string) bool {
	for _, saved := range tombstone.Keys {
		if saved == key {
			return true
		}
	}
	return false
}

func memoryHistoricalSourceKey(id string) string {
	return memoryForgetHash("historical-source:" + strings.TrimSuffix(strings.TrimSpace(id), ".md"))
}

func memoryTombstoneHistoricalReferenceMatch(tombstone memoryTombstone, id string, refs []string) bool {
	if !memoryTombstoneHasKey(tombstone, memoryHistoricalSourceKey(id)) {
		return false
	}
	qualified, matched := false, len(refs) == 0
	for _, ref := range refs {
		hash, sameSource := memoryForgetRefVersion(ref, id)
		if !sameSource {
			continue
		}
		matched = true
		if hash == "" {
			continue
		}
		qualified = true
		if memoryTombstoneHasKey(tombstone, memoryForgetHash(memoryForgetHistoricalPrefix+id+":"+hash)) {
			return true
		}
	}
	// A version-qualified current reference resolves the accompanying legacy ID.
	return matched && !qualified
}

// Version hashes are evidence for preserved live IDs whose older versions were
// removed. This guard never loads their current bytes and cannot mistake a new
// correction for the old source captured in an in-flight derivation.
func isMemoryHistoricalReferenceForgotten(dir, id string, refs []string, now time.Time) (bool, error) {
	memoryTombstoneMu.Lock()
	tombstones, err := loadMemoryTombstones(dir)
	memoryTombstoneMu.Unlock()
	if err != nil {
		return false, err
	}
	for _, tombstone := range tombstones {
		if now.UnixMilli() >= tombstone.ExpiresAt {
			continue
		}
		if memoryTombstoneHistoricalReferenceMatch(tombstone, id, refs) {
			return true, nil
		}
	}
	return false, nil
}

// Frozen references may include a version field and IDs may themselves contain
// colons. Checking each prefix against an existing hashed source marker keeps
// parsing conservative without persisting source IDs in the tombstone.
func memoryHistoricalReferenceIDs(refs []string) []string {
	ids := map[string]bool{}
	for _, ref := range refs {
		value := strings.TrimSpace(ref)
		kind, suffix, typed := strings.Cut(value, ":")
		if typed {
			switch kind {
			case "memory", "memory_markdown":
				value = suffix
			default:
				continue
			}
		}
		ids[strings.TrimSuffix(value, ".md")] = true
		for i := strings.IndexByte(value, ':'); i >= 0; {
			ids[strings.TrimSuffix(value[:i], ".md")] = true
			next := strings.IndexByte(value[i+1:], ':')
			if next < 0 {
				break
			}
			i += next + 1
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func memoryTombstoneIdentityMatch(tombstone memoryTombstone, id, content string) bool {
	ids := []string{id}
	// Provenance is read only from the candidate: a child of a removed record
	// is suppressed. Parents are never copied into a child's tombstone.
	sourceIDs := memoryDerivedSourceIDs(content)
	ids = append(ids, sourceIDs...)
	if memoryTombstoneHasKey(tombstone, memoryForgetHash(memoryForgetHistoricalSelector(id, content))) {
		return true
	}
	refs := append(memoryDerivedSourceRefs(content), sourceIDs...)
	for _, sourceID := range append(memoryHistoricalReferenceIDs(refs), sourceIDs...) {
		if memoryTombstoneHistoricalReferenceMatch(tombstone, sourceID, refs) {
			return true
		}
	}
	for _, candidateID := range ids {
		key := memoryForgetIDKey(candidateID)
		for _, forgotten := range tombstone.Keys {
			if forgotten == key {
				return true
			}
		}
	}
	return false
}

func memoryTombstoneContentMatch(tombstone memoryTombstone, id, content string) bool {
	if isUserEditedMemory(content) || memoryTombstonePreserves(tombstone, id, content) {
		return false
	}
	keys := memoryForgetKeys(id, content)
	if tombstone.Version == 1 {
		keys = append(keys, memoryLegacyTombstoneContentKeys(content)...)
	}
	for _, candidate := range keys {
		for _, forgotten := range tombstone.Keys {
			if forgotten == candidate {
				return true
			}
		}
	}
	return false
}

func memoryPreservedRecordKeys(dir, id string, semantics []memoryForgetSemantics) ([]string, error) {
	removed := map[string]bool{id: true}
	removedVersions := map[string]bool{}
	for _, prepared := range semantics {
		for _, selector := range prepared.RemovedVersions {
			removedVersions[selector] = true
		}
		for _, removedID := range prepared.RemovedIDs {
			removed[removedID] = true
		}
	}
	ids := map[string]bool{}
	add := func(candidateID, content string) {
		if candidateID != "" && !removed[candidateID] && !removedVersions[memoryForgetHistoricalSelector(candidateID, content)] {
			ids[memoryPreservedRecordKey(candidateID, content)] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			add(strings.TrimSuffix(entry.Name(), ".md"), string(data))
		}
	}
	archives, err := os.ReadDir(filepath.Join(dir, memoryArchiveDirName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range archives {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, memoryArchiveDirName, entry.Name()))
		if err != nil {
			return nil, err
		}
		add(parseArchivedMemory(strings.TrimSuffix(entry.Name(), ".md"), string(data)).OriginalID, stripArchiveHeader(string(data)))
	}
	histories, err := os.ReadDir(filepath.Join(dir, memoryHistoryDirName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range histories {
		if !entry.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(dir, memoryHistoryDirName, entry.Name()))
		if err != nil {
			return nil, err
		}
		for _, version := range versions {
			if version.IsDir() || !strings.HasSuffix(version.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, memoryHistoryDirName, entry.Name(), version.Name()))
			if err != nil {
				return nil, err
			}
			add(entry.Name(), string(data))
		}
	}
	preserved := make([]string, 0, len(ids))
	for key := range ids {
		preserved = append(preserved, key)
	}
	sort.Strings(preserved)
	return preserved, nil
}

// memoryTombstoneLoads counts reads of the forget markers, for tests.
var memoryTombstoneLoads atomic.Uint64

func loadMemoryTombstones(dir string) ([]memoryTombstone, error) {
	memoryTombstoneLoads.Add(1)
	data, err := os.ReadFile(filepath.Join(dir, memoryTombstonesFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tombstones []memoryTombstone
	if err := json.Unmarshal(data, &tombstones); err != nil {
		return nil, fmt.Errorf("read memory suppression: %w", err)
	}
	supported := tombstones[:0]
	for _, tombstone := range tombstones {
		switch tombstone.Version {
		case 0:
			// The shipped format omitted version. Keep its opaque keys and
			// original expiry; the next marker write persists this migration.
			tombstone.Version = 1
		case 1, memoryTombstoneVersion:
		default:
			if tombstone.ExpiresAt <= time.Now().UnixMilli() {
				continue
			}
			return nil, fmt.Errorf("memory suppression uses unsupported version %d", tombstone.Version)
		}
		supported = append(supported, tombstone)
	}
	return supported, nil
}

func isMemoryForgotten(dir, id, content string, now time.Time) (bool, error) {
	memoryTombstoneMu.Lock()
	tombstones, err := loadMemoryTombstones(dir)
	memoryTombstoneMu.Unlock()
	if err != nil {
		return false, err
	}
	return memoryForgottenBy(dir, tombstones, id, content, now)
}

// memoryTombstoneGeneration counts this process's writes of the forget
// markers, so a copy loaded before one is read again.
var memoryTombstoneGeneration atomic.Uint64

// memoryForgottenNow is isMemoryForgotten with the markers loaded once per
// memory snapshot instead of once per note.
func (a *App) memoryForgottenNow(dir, id, content string, now time.Time) (bool, error) {
	if a != nil {
		if snapshot, err := a.memorySnapshot(); err == nil && snapshot.dir == dir {
			return snapshot.forgotten(id, content, now)
		}
	}
	return isMemoryForgotten(dir, id, content, now)
}

func memoryForgottenBy(dir string, tombstones []memoryTombstone, id, content string, now time.Time) (bool, error) {
	for _, tombstone := range tombstones {
		if now.UnixMilli() >= tombstone.ExpiresAt {
			continue
		}
		if memoryTombstoneIdentityMatch(tombstone, id, content) || memoryTombstoneContentMatch(tombstone, id, content) {
			return true, nil
		}
	}
	for _, tombstone := range tombstones {
		if now.UnixMilli() >= tombstone.ExpiresAt || isUserEditedMemory(content) || memoryTombstonePreserves(tombstone, id, content) {
			continue
		}
		if tombstone.Semantics != nil && (len(tombstone.Semantics.Hashes) > 0 || len(tombstone.Semantics.FactHashes) > 0) {
			matched, err := memoryTombstoneSemanticMatch(dir, tombstone, content)
			if err != nil || matched {
				return matched, err
			}
		}
	}
	return false, nil
}

func recordMemoryTombstone(dir, id string, contents []string, now time.Time, semantics ...memoryForgetSemantics) error {
	return recordMemoryTombstoneScoped(dir, id, contents, now, false, semantics...)
}

func recordMemoryTombstoneScoped(dir, id string, contents []string, now time.Time, preserveRoot bool, semantics ...memoryForgetSemantics) error {
	excludedID := id
	if preserveRoot {
		excludedID = ""
	}
	preserved, err := memoryPreservedRecordKeys(dir, excludedID, semantics)
	if err != nil {
		return err
	}
	memoryTombstoneMu.Lock()
	defer memoryTombstoneMu.Unlock()
	tombstones, err := loadMemoryTombstones(dir)
	if err != nil {
		return err
	}
	active := make([]memoryTombstone, 0, len(tombstones)+1)
	for _, tombstone := range tombstones {
		if tombstone.ExpiresAt > now.UnixMilli() {
			active = append(active, tombstone)
		}
	}
	keySet := map[string]bool{}
	for _, prepared := range semantics {
		for _, selector := range prepared.RemovedVersions {
			value, ok := strings.CutPrefix(selector, memoryForgetHistoricalPrefix)
			separator := strings.LastIndexByte(value, ':')
			if !ok || separator <= 0 || !memoryForgetVersionHash(value[separator+1:]) {
				return fmt.Errorf("invalid erased memory version selector")
			}
			keySet[memoryForgetHash(selector)] = true
			keySet[memoryHistoricalSourceKey(value[:separator])] = true
		}
	}
	for _, content := range contents {
		for _, key := range memoryForgetKeys(id, content) {
			if preserveRoot && key == memoryForgetIDKey(id) {
				continue
			}
			keySet[key] = true
		}
	}
	keys := make([]string, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tombstone := memoryTombstone{Version: memoryTombstoneVersion, Keys: keys, PreservedRecords: preserved, ExpiresAt: now.Add(memoryForgetRetentionDays * 24 * time.Hour).UnixMilli()}
	if len(semantics) > 0 {
		prepared := semantics[0]
		if prepared.Version != memorySemanticVersion {
			return errMemorySemanticUnverified
		}
		prepared.RemovedIDs = nil
		tombstone.Semantics = &prepared
	}
	active = append(active, tombstone)
	return persistMemoryTombstones(dir, active)
}

// Caller holds memoryTombstoneMu to serialize this atomic replacement.
func persistMemoryTombstones(dir string, tombstones []memoryTombstone) error {
	data, err := json.Marshal(tombstones)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".forgotten-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	defer memoryTombstoneGeneration.Add(1)
	if err := os.Rename(file.Name(), filepath.Join(dir, memoryTombstonesFile)); err != nil {
		return err
	}
	// A tombstone is a retraction written to a file the memory watcher does not
	// read (it reads .md only), so the write advances the epoch itself.
	briefPrivacyChanged()
	briefPrivacyAmendSoon()
	return nil
}

func isUserEditedMemory(content string) bool {
	switch strings.ToLower(strings.TrimSpace(parseMemoryFrontmatter(content).Source)) {
	case "user_edit", "manual", "user", "edit":
		return true
	case "":
		return extractMemoryDimension(content) == "custom"
	default:
		return false
	}
}

func markMemoryUserEdited(content string) string {
	if doc, ok := parseOKFDocument(content); ok {
		doc.Frontmatter["source"] = "user_edit"
		return renderOKFDocument(doc.Frontmatter, doc.Body)
	}
	parts := splitMarkdownTranslationParts(content)
	metadata := make([]string, 0, len(parts.Metadata)+1)
	for _, line := range parts.Metadata {
		if !strings.HasPrefix(strings.TrimSpace(line), "- **Source**:") {
			metadata = append(metadata, line)
		}
	}
	parts.Metadata = append(metadata, "- **Source**: user_edit")
	return parts.Recombine(parts.TranslatableMarkdown()) + "\n"
}

// Caller holds memoryMutationMu and the per-entry lock when checking a write.
func checkMemoryWriteAllowed(dir, id, content string, observed bool) error {
	forgotten, err := isMemoryForgotten(dir, id, content, time.Now())
	if err != nil {
		return err
	}
	if forgotten {
		return errMemoryForgotten
	}
	if observed {
		existing, err := os.ReadFile(filepath.Join(dir, id+".md"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if isUserEditedMemory(string(existing)) {
			return errMemoryUserEdited
		}
	}
	return nil
}

func memoryUserEditComparable(content string) string {
	if doc, ok := parseOKFDocument(content); ok {
		delete(doc.Frontmatter, "confirmed_at")
		content = renderOKFDocument(doc.Frontmatter, doc.Body)
	}
	return memoryUserEditTimestamp(markMemoryUserEdited(content), "")
}

func memoryUserEditTimestamp(content, timestamp string) string {
	if doc, ok := parseOKFDocument(content); ok {
		delete(doc.Frontmatter, "timestamp")
		if timestamp != "" {
			doc.Frontmatter["timestamp"] = timestamp
		}
		return renderOKFDocument(doc.Frontmatter, doc.Body)
	}
	parts := splitMarkdownTranslationParts(content)
	metadata := make([]string, 0, len(parts.Metadata)+1)
	for _, line := range parts.Metadata {
		if !strings.HasPrefix(strings.TrimSpace(line), "- **Updated**:") && strings.TrimSpace(line) != "" {
			metadata = append(metadata, line)
		}
	}
	if timestamp != "" {
		updated, _ := time.Parse(time.RFC3339, timestamp)
		metadata = append(metadata, "- **Updated**: "+updated.Format("2006-01-02 15:04"))
	}
	parts.Metadata = metadata
	return parts.Recombine(parts.TranslatableMarkdown()) + "\n"
}

// Canonical rewrites run without mutation locks. Revalidate their input after
// reacquiring both locks, so a concurrent edit or forget wins over stale work.
func validateMemoryObservationUnchanged(dir, id string, previous []byte, content string) error {
	if err := checkMemoryWriteAllowed(dir, id, content, true); err != nil {
		return err
	}
	current, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		return err
	}
	if string(current) != string(previous) {
		return fmt.Errorf("memory changed while generating an update; try again")
	}
	return nil
}

// Retain contributing conversation/meeting identities when observations merge
// into another record, so forgetting also suppresses paraphrases of that source.
func withMemoryObservationSourceID(content, existing, sourceID string) string {
	doc, ok := parseOKFDocument(content)
	if !ok {
		return content
	}
	ids := []string{sourceID}
	if previous, ok := parseOKFDocument(existing); ok {
		ids = append(ids, okfStringSlice(previous.Frontmatter, "source_ids")...)
	}
	unique := map[string]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	ids = ids[:0]
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	doc.Frontmatter["source_ids"] = ids
	// renderOKFDocument always produces valid frontmatter for the helper.
	preserved, _ := preserveMemoryConfirmation(renderOKFDocument(doc.Frontmatter, doc.Body), existing)
	return preserved
}
