package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Feed feedback (PROTOCOL.md section 20): stable item keys and the phone's
// Like and Not right on a brief item.

const (
	mobileBriefFeedbackLike        = "like"
	mobileBriefFeedbackNotRight    = "not_right"
	mobileBriefFeedbackNotRelevant = "not_relevant"

	// What a feedback did on the Mac, in the route's answer.
	mobileBriefFeedbackTopicMore     = "topic_more"
	mobileBriefFeedbackTopicLess     = "topic_less"
	mobileBriefFeedbackMemoryRemoved = "memory_removed"
	mobileBriefFeedbackRecorded      = "recorded"

	mobileBriefFeedbackFileName = "brief-feedback.json"
	mobileBriefFeedbackLimit    = 500
	mobileBriefKeyHashBytes     = 8
	mobileBriefFeedbackNoReason = "Marked not right in the Feed on the phone."
)

var (
	// mobileBriefFeedbackTopicsOpen reports whether phone feedback may change
	// what the Mac searches for. A test replaces it; production asks the same
	// gate that lets topic labels leave the Mac.
	mobileBriefFeedbackTopicsOpen        = mobileBriefFeedbackTopicsOpenDefault
	mobileBriefFeedbackTopicsOpenDefault = func(a *App) bool { return a.topicLabelsMayLeave() }
	mobileBriefFeedbackMu                sync.Mutex
)

// mobileBriefItemKey names an item by its section and its source record. The
// same record keeps its key in every rebuild and in later briefs, so the phone
// can merge briefs and remember feedback.
func mobileBriefItemKey(section, sourceKind, sourceID string) string {
	sum := sha256.Sum256([]byte(sourceKind + "\x00" + sourceID))
	return section + "." + hex.EncodeToString(sum[:mobileBriefKeyHashBytes])
}

// assignKeys sets the key of every item in one section from the source
// record of its first ref, or explicit goal identity without evidence. Two items from one record in a section get a
// numbered suffix, so a key is unique inside its brief.
func (b *mobileBriefBuilder) assignKeys(section string, items []MobileBriefItem) {
	seen := map[string]int{}
	for i := range items {
		var source MobileBriefRef
		if items[i].PurposeID != "" {
			source.SourceKind, source.SourceID = "purpose", items[i].PurposeID
		} else if len(items[i].Refs) > 0 {
			for _, ev := range b.evidence {
				if ev.Ref == items[i].Refs[0] {
					source = ev
					break
				}
			}
		}
		key := mobileBriefItemKey(section, source.SourceKind, source.SourceID)
		seen[key]++
		if n := seen[key]; n > 1 {
			key += "-" + strconv.Itoa(n)
		}
		items[i].Key = key
	}
}

// mobileBriefFeedbackTarget is a brief item with the record behind it.
type mobileBriefFeedbackTarget struct {
	Section    string
	Item       MobileBriefItem
	SourceKind string
	SourceID   string
}

// findMobileBriefFeedbackTarget finds an item by its key, or by its id for a
// brief stored before keys. Sections are read from the wire shape, so a
// section added to the brief is searched without a change here.
func findMobileBriefFeedbackTarget(brief MobileBriefV1, itemID string) (mobileBriefFeedbackTarget, bool) {
	raw, err := json.Marshal(brief.Sections)
	if err != nil {
		return mobileBriefFeedbackTarget{}, false
	}
	var sections map[string][]MobileBriefItem
	if err := json.Unmarshal(raw, &sections); err != nil {
		return mobileBriefFeedbackTarget{}, false
	}
	var target mobileBriefFeedbackTarget
	found := false
	// Keys carry their section and ids their section name, so at most one
	// item matches.
	for name, items := range sections {
		for _, item := range items {
			if item.Key == itemID || (item.Key == "" && item.ID == itemID) {
				target = mobileBriefFeedbackTarget{Section: name, Item: item}
				found = true
			}
		}
	}
	if !found {
		return mobileBriefFeedbackTarget{}, false
	}
	if target.Item.PurposeID != "" {
		target.SourceKind, target.SourceID = "purpose", target.Item.PurposeID
	} else if len(target.Item.Refs) > 0 {
		for _, ev := range brief.Evidence {
			if ev.Ref == target.Item.Refs[0] {
				target.SourceKind, target.SourceID = ev.SourceKind, ev.SourceID
				break
			}
		}
	}
	return target, true
}

// serveBriefFeedback records Like or Not right on one brief item. A find
// reweights its topic, Not right on a memory removes the memory the way
// POST /memory/{id}/reject-attribution does, and every answer is kept on the
// Mac for the Feed to learn from.
func (bridge *mobileBridge) serveBriefFeedback(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, briefID, itemID string) {
	if !mobileValidBriefID(briefID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief id is not valid")
		return
	}
	if !mobileValidID(itemID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the item id is not valid")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Action         string `json:"action"`
		Reason         string `json:"reason"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	switch input.Action {
	case mobileBriefFeedbackLike, mobileBriefFeedbackNotRight, mobileBriefFeedbackNotRelevant:
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "action must be like or not_right or not_relevant")
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if len(reason) > mobileCorrectionNoteMax {
		mobileJSON(w, http.StatusBadRequest, nil, "the reason is too long")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		svc := bridge.briefSource()
		if svc == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileBriefUnavailable.Error())
			return
		}
		brief, ok := svc.GetMobileBrief(briefID)
		if !ok {
			mobileJSON(w, http.StatusNotFound, nil, "this brief is no longer on your computer")
			return
		}
		target, ok := findMobileBriefFeedbackTarget(brief, itemID)
		if !ok {
			mobileJSON(w, http.StatusNotFound, nil, "this item is no longer in the brief")
			return
		}
		effect := mobileBriefFeedbackRecorded
		switch target.SourceKind {
		case mobileBriefFindSource:
			effect = bridge.applyFindFeedback(target.SourceID, input.Action)
		case "memory":
			if input.Action != mobileBriefFeedbackNotRight {
				break
			}
			// Removing a memory is a correction, so it needs that permission too.
			if !mobileRequireScope(w, device, mobileScopeMemoryFix) {
				return
			}
			if reason == "" {
				reason = mobileBriefFeedbackNoReason
			}
			err := bridge.rejectMemoryForPhone(target.SourceID, reason)
			if errors.Is(err, fs.ErrNotExist) {
				mobileJSON(w, http.StatusNotFound, nil, "this memory is no longer on your computer")
				return
			}
			if err != nil {
				mobileMutationResult(w, nil, err)
				return
			}
			effect = mobileBriefFeedbackMemoryRemoved
		default:
		}
		bridge.recordBriefFeedback(mobileBriefFeedbackEntry{
			Key: target.Item.Key, BriefID: briefID, ItemID: target.Item.ID, Section: target.Section,
			SourceKind: target.SourceKind, Action: input.Action, Effect: effect, DeviceID: device.DeviceID,
			At: time.Now().UTC().Format(time.RFC3339),
		})
		mobileJSON(w, http.StatusOK, map[string]any{"recorded": true, "effect": effect}, "")
	})
}

// Phone correction removes the selected record and its saved versions, logs
// the correction, and suppresses repeated observations of its points locally.
// The Mac's reviewed forget flow separately owns the derivative cascade.
func (bridge *mobileBridge) rejectMemoryForPhone(memoryID, reason string) error {
	var err error
	if mobileMemoryRejectOverride != nil {
		err = mobileMemoryRejectOverride(memoryID, reason)
	} else {
		err = bridge.app.rejectMemoryForPhone(memoryID, reason)
	}
	var notLogged memoryRejectionLogError
	if errors.As(err, &notLogged) {
		log.Printf("[mobile] a memory was removed but its rejection was not logged: %v", err)
		return nil
	}
	return err
}

func (a *App) rejectMemoryForPhone(id, reason string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("phone memory correction: empty id")
	}
	// Freeze the snapshot and existing-record exemptions until deletion. An
	// observation already preparing outside this lock must recheck its gate
	// before writing; DeleteMemoryEntry acquires the entry lock itself.
	memoryMutationMu.Lock()
	defer memoryMutationMu.Unlock()
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return fmt.Errorf("load entry %s: %w", id, err)
	}
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	if err := recordMemoryPhoneCorrection(dir, id, entry.Content, time.Now()); err != nil {
		return fmt.Errorf("record phone memory correction: %w", err)
	}
	if err := a.DeleteMemoryEntry(id); err != nil {
		return fmt.Errorf("delete entry %s: %w", id, err)
	}
	if err := appendMemoryRejection(dir, MemoryRejectionEntry{
		MemoryID: id, Title: entry.Title, Source: entry.Source, Dimension: entry.Dimension,
		ActivitySessionIDs: entry.ActivitySessionIDs, Reason: strings.TrimSpace(reason), RejectedAt: time.Now().UnixMilli(),
	}); err != nil {
		return memoryRejectionLogError{fmt.Errorf("append rejection log: %w", err)}
	}
	return nil
}

// Caller holds memoryMutationMu exclusively. A phone correction targets the
// rejected points, not source ancestry: existing derivatives and independent
// records stay available. No model result is needed to suppress recurrence.
func recordMemoryPhoneCorrection(dir, id, content string, now time.Time) error {
	preserved, err := memoryPreservedRecordKeys(dir, id, nil)
	if err != nil {
		return err
	}
	keys := make([]string, 0)
	for _, key := range memoryForgetKeys(id, content) {
		if key != memoryForgetIDKey(id) {
			keys = append(keys, key)
		}
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
	active = append(active, memoryTombstone{
		Version: memoryTombstoneVersion, Keys: keys, PreservedRecords: preserved,
		ExpiresAt: now.Add(memoryForgetRetentionDays * 24 * time.Hour).UnixMilli(),
	})
	if err := persistMemoryTombstones(dir, active); err != nil {
		return err
	}
	// A brief feedback tombstone withdraws what it names, and the keys are hashes,
	// so the change is not keyed: the briefs are amended with the gates.
	briefPrivacyChanged()
	briefPrivacyAmendSoon()
	return nil
}

// applyFindFeedback maps Like to "more" and Not right to "less" on a find.
// While the topic labels may not leave the Mac, or once a newer search
// replaced the find, the answer is only recorded.
func (bridge *mobileBridge) applyFindFeedback(findID, action string) string {
	app := bridge.app
	if app == nil || !mobileBriefFeedbackTopicsOpen(app) {
		return mobileBriefFeedbackRecorded
	}
	answer, effect := "more", mobileBriefFeedbackTopicMore
	if action == mobileBriefFeedbackNotRight || action == mobileBriefFeedbackNotRelevant {
		answer, effect = "less", mobileBriefFeedbackTopicLess
	}
	if err := app.recordTopicFindFeedback(TopicFindFeedbackInput{FindID: findID, Action: answer}); err != nil {
		log.Printf("[mobile-brief] find feedback kept as a record only: %v", err)
		return mobileBriefFeedbackRecorded
	}
	app.emitBrowserEvent("topics:finds")
	return effect
}

// mobileBriefFeedbackEntry is one Like or Not right kept on the Mac.
type mobileBriefFeedbackEntry struct {
	Key        string `json:"key"`
	BriefID    string `json:"briefId"`
	ItemID     string `json:"itemId"`
	Section    string `json:"section"`
	SourceKind string `json:"sourceKind"`
	Action     string `json:"action"`
	Effect     string `json:"effect"`
	DeviceID   string `json:"deviceId"`
	At         string `json:"at"`
}

// recordBriefFeedback appends one answer to brief-feedback.json in the
// bridge's state folder, keeping the newest 500. A failed write is logged:
// the answer already took effect and its receipt is stored.
func (bridge *mobileBridge) recordBriefFeedback(entry mobileBriefFeedbackEntry) {
	if bridge.stateDir == "" {
		return
	}
	mobileBriefFeedbackMu.Lock()
	defer mobileBriefFeedbackMu.Unlock()
	entries := readMobileBriefFeedback(bridge.stateDir)
	entries = append(entries, entry)
	if len(entries) > mobileBriefFeedbackLimit {
		entries = entries[len(entries)-mobileBriefFeedbackLimit:]
	}
	data, err := json.Marshal(entries)
	if err == nil {
		err = writeMobileBriefFileAtomic(bridge.stateDir, mobileBriefFeedbackFileName, data)
	}
	if err != nil {
		log.Printf("[mobile-brief] feedback record not saved: %v", err)
	}
}

func readMobileBriefFeedback(stateDir string) []mobileBriefFeedbackEntry {
	data, err := os.ReadFile(filepath.Join(stateDir, mobileBriefFeedbackFileName))
	if err != nil {
		return nil
	}
	var entries []mobileBriefFeedbackEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	return entries
}
