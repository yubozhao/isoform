package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

var confirmedAtLine = regexp.MustCompile(`^(?:confirmed_at|'confirmed_at'|"confirmed_at")\s*:`)

// confirmationHeader locates only YAML frontmatter. A legacy Markdown file can
// carry a confirmation-only header without converting its existing metadata.
func confirmationHeader(content string) (int, int, string, error) {
	start := 0
	if strings.HasPrefix(content, "\ufeff") {
		start = len("\ufeff")
	}
	newline := "\n"
	if strings.HasPrefix(content[start:], "---\r\n") {
		newline = "\r\n"
	}
	if !strings.HasPrefix(content[start:], "---"+newline) {
		return -1, -1, newline, nil
	}
	body := start + 3 + len(newline)
	offset := body
	for _, line := range strings.SplitAfter(content[body:], "\n") {
		if strings.TrimSpace(line) == "---" {
			var fields map[string]any
			if err := yaml.UnmarshalStrict([]byte(content[body:offset]), &fields); err != nil {
				return 0, 0, newline, err
			}
			return body, offset, newline, nil
		}
		offset += len(line)
	}
	return 0, 0, newline, fmt.Errorf("invalid memory frontmatter")
}

// legacyMemoryWithoutConfirmation keeps the legacy metadata readers intact
// when the person's answers (confirmed_at, not_quite_at) are the file's only
// YAML fields. It never drops an envelope
// containing provenance or any other field.
func legacyMemoryWithoutConfirmation(content string) string {
	start, end, _, err := confirmationHeader(content)
	if err != nil || start < 0 {
		return content
	}
	var fields map[string]any
	if yaml.Unmarshal([]byte(content[start:end]), &fields) != nil || len(fields) == 0 {
		return content
	}
	// The envelope holds only the person's answers: confirmed_at and the
	// portrait's not_quite_at.
	for field := range fields {
		if field != "confirmed_at" && field != "not_quite_at" {
			return content
		}
	}
	if lineEnd := strings.IndexByte(content[end:], '\n'); lineEnd >= 0 {
		return content[end+lineEnd+1:]
	}
	return ""
}

func memoryConfirmedAt(content string) int64 {
	start, end, _, err := confirmationHeader(content)
	if err != nil || start < 0 {
		return 0
	}
	var fields map[string]any
	if yaml.Unmarshal([]byte(content[start:end]), &fields) != nil {
		return 0
	}
	value, ok := fields["confirmed_at"].(string)
	if !ok {
		return 0
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return at.UnixMilli()
}

// setMemoryConfirmedAt edits the field in place rather than reserializing YAML:
// every other byte, including body whitespace and provenance, stays intact.
func setMemoryConfirmedAt(content string, at time.Time) (string, error) {
	start, end, newline, err := confirmationHeader(content)
	if err != nil {
		return "", err
	}
	field := "confirmed_at: " + at.Format(time.RFC3339) + newline
	if start < 0 {
		prefix := ""
		if strings.HasPrefix(content, "\ufeff") {
			prefix = "\ufeff"
			content = strings.TrimPrefix(content, prefix)
		}
		return prefix + "---" + newline + field + "---" + newline + content, nil
	}
	offset := start
	lines := strings.SplitAfter(content[start:end], "\n")
	for index, line := range lines {
		if confirmedAtLine.MatchString(line) {
			after := offset + len(line)
			// A previous value may have been a block scalar. Consume only its
			// indented continuation, leaving following fields byte-for-byte.
			for _, continuation := range lines[index+1:] {
				if !strings.HasPrefix(continuation, " ") && !strings.HasPrefix(continuation, "\t") {
					break
				}
				after += len(continuation)
			}
			return content[:offset] + field + content[after:], nil
		}
		offset += len(line)
	}
	return content[:end] + field + content[end:], nil
}

var confirmationMetadataLine = regexp.MustCompile(`^- \*\*(?:Updated|Source|Dimension|Apps|Activity Session|Read Provenance|MergedFrom|MergedFromRefs|Reassigned to)\*\*:`)

// memoryConfirmationBody excludes frontmatter and legacy evidence metadata.
// Converted legacy OKF bodies can still contain the duplicate title/metadata
// block. Other body headings remain substantive text. Only outer whitespace
// and line endings are normalized, never wording, case or punctuation.
func memoryConfirmationBody(content string) string {
	content = legacyMemoryWithoutConfirmation(content)
	doc, ok := parseOKFDocument(content)
	return memoryConfirmationBodyFromDocument(content, doc, ok)
}

func memoryConfirmationBodyFromDocument(content string, doc okfDocument, ok bool) string {
	title := ""
	legacy := true
	if ok {
		content, title, legacy = doc.Body, okfString(doc.Frontmatter, "title"), false
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "# ") &&
		(legacy || strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "# ")) == title) {
		i++
	}
	metadata := false
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch true {
		case trimmed == "":
			metadata = false
		case confirmationMetadataLine.MatchString(trimmed):
			metadata = strings.HasPrefix(trimmed, "- **Activity Session**:")
		case metadata && isIndentedMarkdownBullet(line):
		default:
			return strings.TrimSpace(strings.Join(lines[i:], "\n"))
		}
		i++
	}
	return ""
}

// clearMemoryConfirmation removes only the field, or its legacy-only envelope.
func clearMemoryConfirmation(content string) (string, error) {
	if legacy := legacyMemoryWithoutConfirmation(content); legacy != content {
		return legacy, nil
	}
	start, end, _, err := confirmationHeader(content)
	if err != nil || start < 0 {
		return content, err
	}
	offset := start
	lines := strings.SplitAfter(content[start:end], "\n")
	for index, line := range lines {
		if confirmedAtLine.MatchString(line) {
			after := offset + len(line)
			for _, continuation := range lines[index+1:] {
				if !strings.HasPrefix(continuation, " ") && !strings.HasPrefix(continuation, "\t") {
					break
				}
				after += len(continuation)
			}
			return content[:offset] + content[after:], nil
		}
		offset += len(line)
	}
	return content, nil
}

// preserveMemoryConfirmation binds an automated write to the words the user
// confirmed. Metadata/evidence-only updates retain the time; changed bodies
// lose confirmation. User edits confirm separately in SaveMemoryEntry.
func preserveMemoryConfirmation(content, previous string) (string, error) {
	if confirmedAt := memoryConfirmedAt(previous); confirmedAt != 0 {
		sameBody := memoryConfirmationBody(content) == memoryConfirmationBody(previous)
		// A frontmatter title edit must not change the interpretation of an
		// otherwise identical body containing a converted legacy heading.
		if !sameBody {
			next, nextOK := parseOKFDocument(legacyMemoryWithoutConfirmation(content))
			old, oldOK := parseOKFDocument(legacyMemoryWithoutConfirmation(previous))
			sameBody = nextOK && oldOK && next.Body == old.Body
		}
		if sameBody {
			return setMemoryConfirmedAt(content, time.UnixMilli(confirmedAt).UTC())
		}
	}
	return clearMemoryConfirmation(content)
}

// unconfirmMemoryEntry takes back a confirmation, for the portrait's Undo.
// Like ConfirmMemoryEntry it changes nothing else in the file.
func (a *App) unconfirmMemoryEntry(id string) error {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return err
	}
	if memoryConfirmedAt(entry.Content) == 0 {
		return nil
	}
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	if err := checkMemoryWriteAllowed(dir, id, entry.Content, false); err != nil {
		return err
	}
	content, err := clearMemoryConfirmation(entry.Content)
	if err != nil {
		return err
	}
	if err := writeMemoryFileAtomically(dir, id, content); err != nil {
		return err
	}
	a.markMemoryDirty("unconfirm")
	return nil
}

// writeMemoryFileAtomically replaces one belief's file through a synced
// temporary sibling.
func writeMemoryFileAtomically(dir, id, content string) error {
	file, err := os.CreateTemp(dir, ".confirmation-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(content); err != nil {
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
	return os.Rename(file.Name(), filepath.Join(dir, id+".md"))
}

// ConfirmMemoryEntry confirms an existing belief without changing its content or history.
func (a *App) ConfirmMemoryEntry(id string) (MemoryEntry, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return MemoryEntry{}, err
	}
	dir, err := memoryDir()
	if err != nil {
		return MemoryEntry{}, err
	}
	if err := checkMemoryWriteAllowed(dir, id, entry.Content, false); err != nil {
		return MemoryEntry{}, err
	}
	if a.activity != nil && a.activity.store != nil {
		suppressed, err := a.activity.store.ListCorrectedAgencyLearningTargetIDs()
		if err != nil {
			return MemoryEntry{}, err
		}
		if _, blocked := suppressed[id]; blocked {
			return MemoryEntry{}, errMemoryForgotten
		}
	}
	if memoryConfirmedAt(entry.Content) != 0 {
		return entry, nil
	}
	content, err := setMemoryConfirmedAt(entry.Content, time.Now())
	if err != nil {
		return MemoryEntry{}, err
	}
	// Confirming again takes back the portrait's Not quite.
	content = clearMemoryNotQuite(content)
	if err := writeMemoryFileAtomically(dir, id, content); err != nil {
		return MemoryEntry{}, err
	}
	a.markMemoryDirty("confirm")
	a.ledgerBeliefAnswer(id, entry.Content, workActionYes)
	return a.GetMemoryEntry(id)
}

var notQuiteAtLine = regexp.MustCompile(`^(?:not_quite_at|'not_quite_at'|"not_quite_at")\s*:`)

// memoryNotQuiteAt is when the person marked the belief Not quite from the
// portrait's confirm rows, or 0. Not quite takes back a confirmation, and a
// new confirmation takes back Not quite, so the two never stand together.
func memoryNotQuiteAt(content string) int64 {
	start, end, _, err := confirmationHeader(content)
	if err != nil || start < 0 {
		return 0
	}
	var fields map[string]any
	if yaml.Unmarshal([]byte(content[start:end]), &fields) != nil {
		return 0
	}
	value, ok := fields["not_quite_at"].(string)
	if !ok {
		return 0
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return at.UnixMilli()
}

// setMemoryNotQuiteAt edits only that field, like setMemoryConfirmedAt.
func setMemoryNotQuiteAt(content string, at time.Time) (string, error) {
	start, end, newline, err := confirmationHeader(content)
	if err != nil {
		return "", err
	}
	field := "not_quite_at: " + at.Format(time.RFC3339) + newline
	if start < 0 {
		prefix := ""
		if strings.HasPrefix(content, "\ufeff") {
			prefix = "\ufeff"
			content = strings.TrimPrefix(content, prefix)
		}
		return prefix + "---" + newline + field + "---" + newline + content, nil
	}
	offset := start
	for _, line := range strings.SplitAfter(content[start:end], "\n") {
		if notQuiteAtLine.MatchString(line) {
			return content[:offset] + field + content[offset+len(line):], nil
		}
		offset += len(line)
	}
	return content[:end] + field + content[end:], nil
}

// markMemoryNotQuite is Not quite on a portrait confirm row. Like
// ConfirmMemoryEntry it changes nothing else in the file.
func (a *App) markMemoryNotQuite(id string) error {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return err
	}
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	if err := checkMemoryWriteAllowed(dir, id, entry.Content, false); err != nil {
		return err
	}
	content, err := clearMemoryConfirmation(entry.Content)
	if err != nil {
		return err
	}
	content, err = setMemoryNotQuiteAt(content, time.Now())
	if err != nil {
		return err
	}
	if err := writeMemoryFileAtomically(dir, id, content); err != nil {
		return err
	}
	a.markMemoryDirty("not_quite")
	return nil
}

// clearMemoryNotQuite removes only the not_quite_at line.
func clearMemoryNotQuite(content string) string {
	start, end, _, err := confirmationHeader(content)
	if err != nil || start < 0 {
		return content
	}
	offset := start
	for _, line := range strings.SplitAfter(content[start:end], "\n") {
		if notQuiteAtLine.MatchString(line) {
			return content[:offset] + content[offset+len(line):]
		}
		offset += len(line)
	}
	return content
}

// memoryInUse is false for a belief the person said Not quite to. Chat
// context, drafts and the portrait leave it out until it is confirmed or
// reworded; it stays in About you with its Not quite chip.
func memoryInUse(content string) bool {
	return memoryNotQuiteAt(content) == 0
}
