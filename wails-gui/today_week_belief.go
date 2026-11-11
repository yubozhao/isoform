package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// The receipt contains identities and hashes, never belief wording. Its key is
// process-local: Undo is offered only for the current page, not after restart.
type todayWeekBeliefUndo struct {
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	Digest        string `json:"digest"`
	HistoryDigest string `json:"historyDigest"`
}

var todayWeekBeliefKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	return key
}()

func todayWeekBeliefDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func todayWeekBeliefToken(receipt todayWeekBeliefUndo) (string, error) {
	if len(todayWeekBeliefKey) != 32 {
		return "", errors.New("undo unavailable")
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, todayWeekBeliefKey)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parseTodayWeekBeliefToken(token string) (todayWeekBeliefUndo, error) {
	var receipt todayWeekBeliefUndo
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 4096 || len(todayWeekBeliefKey) != 32 {
		return receipt, errTodayChatItemUnavailable
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return receipt, errTodayChatItemUnavailable
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, todayWeekBeliefKey)
	_, _ = mac.Write(raw)
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(raw, &receipt) != nil || receipt.ID == "" || receipt.Version <= 0 || len(receipt.Digest) != 64 || len(receipt.HistoryDigest) != 64 {
		return todayWeekBeliefUndo{}, errTodayChatItemUnavailable
	}
	return receipt, nil
}

// The field edits the first memory point, like About you, without replacing
// the source envelope or sending the raw file to the renderer.
var todayWeekMemoryMetadata = regexp.MustCompile(`(?i)^- \*\*(Dimension|Source|Updated|Activity Session|Read Provenance|Apps|UUID|ID|Type|Date|Timestamp|Reassigned to|MergedFrom|Merged From|MergedFromRefs)\*\*:`)
var todayWeekMemoryChild = regexp.MustCompile(`^\s+[-*] `)
var todayWeekMemoryPrefix = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+\.\s+|>\s*)?`)

func todayWeekRewordMemory(content, wording string) string {
	yaml, metadataChildren, at := false, false, 0
	for _, line := range strings.SplitAfter(content, "\n") {
		body := strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(body)
		if at == 0 && trimmed == "---" {
			yaml = true
			at += len(line)
			continue
		}
		if yaml {
			if trimmed == "---" {
				yaml = false
			}
			at += len(line)
			continue
		}
		if todayWeekMemoryMetadata.MatchString(trimmed) {
			metadataChildren = true
			at += len(line)
			continue
		}
		if metadataChildren && todayWeekMemoryChild.MatchString(body) {
			at += len(line)
			continue
		}
		if trimmed != "" {
			metadataChildren = false
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "```") || trimmed == "---" {
			at += len(line)
			continue
		}
		prefix := todayWeekMemoryPrefix.FindString(body)
		raw := body[len(prefix):]
		sentence, _, _ := uniseg.FirstSentenceInString(raw, -1)
		leading := len(sentence) - len(strings.TrimLeft(sentence, " \t"))
		end := len(strings.TrimRight(sentence, " \t"))
		if end > leading {
			return content[:at+len(prefix)+leading] + wording + content[at+len(prefix)+end:]
		}
		at += len(line)
	}
	return strings.TrimRight(content, "\r\n") + "\n- " + wording + "\n"
}

// The selected source must still be readable under the same publication
// barriers used by Today chat. The database fence is held across the write.
func (a *App) withTodayWeekBelief(ctx context.Context, id, digest string, check, write func(MemoryEntry, string) error) error {
	for _, lock := range []struct {
		try     func() bool
		release func()
	}{
		{a.closedEyes.mu.TryLock, a.closedEyes.mu.Unlock},
		{memoryMutationMu.TryLock, memoryMutationMu.Unlock},
		{memoryFileLocks.publication.TryLock, memoryFileLocks.publication.Unlock},
		{pii.TryLockSensitiveAppsForPublication, pii.UnlockSensitiveAppsForPublication},
	} {
		if err := lockTodayChatPublication(ctx, lock.try); err != nil {
			return err
		}
		defer lock.release()
	}
	if a.IsClosedEyesModeEnabled() {
		return errTodayChatItemUnavailable
	}
	unlock := memoryFileLocks.AcquireRead(id)
	defer unlock()
	var revision storage.ReadRevision
	var err error
	if a.activity != nil && a.activity.store != nil {
		revision, err = storage.DatabaseRevision(ctx, a.activity.store.db)
		if err != nil {
			return err
		}
	}
	entry, err := a.GetMemoryEntry(id)
	if err != nil || todayWeekBeliefDigest(entry.Content) != digest {
		return errTodayChatItemUnavailable
	}
	allow, err := a.memoryReadFilterContext(ctx)
	if err != nil || !allow(id, entry.Content) {
		return errTodayChatItemUnavailable
	}
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(entry, dir); err != nil {
			return err
		}
	}
	if a.activity != nil && a.activity.store != nil {
		release, err := storage.LockDatabaseRevision(ctx, a.activity.store.db, revision)
		if err != nil {
			return err
		}
		defer release()
	}
	return write(entry, dir)
}

// CorrectTodayWeekBelief changes only a belief in the selected, currently
// readable weekly brief. Blank stops using it; words replace its first point.
func (a *App) CorrectTodayWeekBelief(briefID, id, expectedText, words, correlation string) (string, error) {
	if a == nil || len(todayWeekBeliefKey) != 32 || !mobileBriefIDPattern.MatchString(briefID) || id == "" || expectedText == "" || len(expectedText) > 4096 || len(words) > 4096 || len(correlation) > 80 {
		return "", errTodayChatItemUnavailable
	}
	// The belief is checked against the stored brief through the live gates, not
	// the display copy, which may predate the belief's last change.
	var week MobileBriefV1
	if service, isService := a.ensureMobileBriefService().(*mobileBriefService); isService && service != nil {
		if stored, found := service.storedSource(briefID); found {
			week = a.mobileBriefForDelivery(stored)
		}
	}
	if week.BriefID != briefID || week.YourWeek == nil {
		return "", errTodayChatItemUnavailable
	}
	view := TodayBriefView{Brief: &week}
	selected := false
	for _, row := range view.Brief.YourWeek.Learned {
		if row.MemoryID == id && row.Kind == yourWeekLearnedMemory && (row.Text == expectedText || pii.DisplayModelText(row.Text, a.modelDisplayLanguage()) == expectedText) {
			selected = true
			break
		}
	}
	if !selected {
		return "", errTodayChatItemUnavailable
	}
	entry, err := a.memoryEntryForExternalRead(id)
	if err != nil {
		return "", errTodayChatItemUnavailable
	}
	// Use the unredacted source only inside the locked backend. The frontend
	// never receives the content, and the digest catches concurrent edits.
	current, err := a.GetMemoryEntry(id)
	if err != nil || current.ID != entry.ID {
		return "", errTodayChatItemUnavailable
	}
	words = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(words, "\r", " "), "\n", " "))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var token string
	err = a.withTodayWeekBelief(ctx, id, todayWeekBeliefDigest(current.Content), nil, func(entry MemoryEntry, dir string) error {
		if !memoryInUse(entry.Content) {
			return errTodayChatItemUnavailable
		}
		content := entry.Content
		var err error
		if words == "" {
			content, err = clearMemoryConfirmation(content)
			if err == nil {
				content, err = setMemoryNotQuiteAt(content, time.Now())
			}
		} else {
			if _, ok := parseOKFDocument(legacyMemoryWithoutConfirmation(content)); !ok {
				content = memoryWithResolvedProvenance(id, content, memoryReadProvenanceForFile(dir, id, content))
			}
			content = todayWeekRewordMemory(content, words)
			content = markMemoryUserEdited(content)
			content = memoryUserEditTimestamp(content, time.Now().Format(time.RFC3339))
			content, err = setMemoryConfirmedAt(content, time.Now())
			content = clearMemoryNotQuite(content)
		}
		if err != nil {
			return err
		}
		if err := validateMemoryMetadata(content); err != nil {
			return err
		}
		if err := checkMemoryWriteAllowed(dir, id, content, false); err != nil {
			return err
		}
		if err := authorizeMemoryHumanMutation(dir, id, entry.Content, content); err != nil {
			return err
		}
		oldVersions, err := a.ListMemoryVersions(id)
		if err != nil {
			return err
		}
		if err := snapshotMemoryFile(dir, id); err != nil {
			return err
		}
		versions, err := a.ListMemoryVersions(id)
		if err != nil || len(versions) != len(oldVersions)+1 {
			return errTodayChatItemUnavailable
		}
		old := make(map[int64]bool, len(oldVersions))
		for _, version := range oldVersions {
			old[version.TimestampMs] = true
		}
		var savedVersion int64
		for _, version := range versions {
			if !old[version.TimestampMs] {
				savedVersion = version.TimestampMs
				break
			}
		}
		if savedVersion == 0 {
			return errTodayChatItemUnavailable
		}
		history, err := json.Marshal(versions)
		if err != nil {
			return err
		}
		token, err = todayWeekBeliefToken(todayWeekBeliefUndo{ID: id, Version: savedVersion, Digest: todayWeekBeliefDigest(content), HistoryDigest: todayWeekBeliefDigest(string(history))})
		if err != nil {
			return err
		}
		if err := writeMemoryFileAtomically(dir, id, content); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// The correction is a user action on what Isoform says about them, so the old
	// belief leaves every sealed copy that shows it. The copy is amended from this
	// keyed withdrawal and reads no live source. The corrected wording (when it
	// passes the week's copy rules) takes the belief's place in the learned row.
	shown := ""
	if words != "" && yourWeekCopyAllowed(words) {
		shown = words
	}
	briefPrivacyChanged(briefWithdrawn{Kind: string(MemorySourceMemory), ID: id, Text: shown})
	briefPrivacyAmendSoon()
	a.markMemoryDirtyWithCorrelation("today_not_quite", correlation)
	return token, nil
}

// Undo is conditional on the exact saved version and current source policy.
// It never overwrites a later correction or restores a withdrawn source.
func (a *App) UndoTodayWeekBelief(token, correlation string) error {
	if a == nil || len(correlation) > 80 {
		return errTodayChatItemUnavailable
	}
	receipt, err := parseTodayWeekBeliefToken(token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var before string
	err = a.withTodayWeekBelief(ctx, receipt.ID, receipt.Digest, func(_ MemoryEntry, _ string) error {
		var checkErr error
		before, checkErr = a.todayWeekUndoBefore(ctx, receipt)
		return checkErr
	}, func(entry MemoryEntry, dir string) error {
		currentVersion, err := a.GetMemoryVersion(receipt.ID, receipt.Version)
		if err != nil || currentVersion != before {
			return errTodayChatItemUnavailable
		}
		if err := checkMemoryWriteAllowed(dir, receipt.ID, before, false); err != nil {
			return err
		}
		if err := authorizeMemoryHumanMutation(dir, receipt.ID, entry.Content, before); err != nil {
			return err
		}
		if err := snapshotMemoryFile(dir, receipt.ID); err != nil {
			return err
		}
		return writeMemoryFileAtomically(dir, receipt.ID, before)
	})
	if err == nil {
		// Undo brings the belief back. A restored belief cannot be named by the
		// log, so this is an unkeyed change: the gates seal the copies again.
		briefPrivacyChanged()
		briefPrivacyAmendSoon()
		a.markMemoryDirtyWithCorrelation("today_not_quite_undo", correlation)
	}
	return err
}

func (a *App) todayWeekUndoBefore(ctx context.Context, receipt todayWeekBeliefUndo) (string, error) {
	versions, err := a.ListMemoryVersions(receipt.ID)
	if err != nil {
		return "", err
	}
	history, err := json.Marshal(versions)
	if err != nil || todayWeekBeliefDigest(string(history)) != receipt.HistoryDigest {
		return "", errTodayChatItemUnavailable
	}
	before, err := a.GetMemoryVersion(receipt.ID, receipt.Version)
	if err != nil {
		return "", err
	}
	allow, err := a.memoryReadFilterContext(ctx)
	if err != nil || !allow(receipt.ID, before) {
		return "", errTodayChatItemUnavailable
	}
	return before, nil
}

// Revalidation after an index refresh reveals no memory content and cannot
// restore a receipt whose source has been withdrawn or edited.
func (a *App) CanUndoTodayWeekBelief(token string) bool {
	if a == nil {
		return false
	}
	receipt, err := parseTodayWeekBeliefToken(token)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.withTodayWeekBelief(ctx, receipt.ID, receipt.Digest, func(_ MemoryEntry, _ string) error {
		_, err := a.todayWeekUndoBefore(ctx, receipt)
		return err
	}, func(_ MemoryEntry, _ string) error { return nil }) == nil
}
