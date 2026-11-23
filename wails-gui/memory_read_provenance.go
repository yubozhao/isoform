package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Written by the memory writers, never copied from a model's body or inferred
// from a model-selected dimension. Flattened source kinds survive Dream archive.
type memoryReadProvenance struct {
	Version  int      `json:"version"`
	Kinds    []string `json:"kinds"`
	Sessions []string `json:"sessions,omitempty"`
	Apps     []string `json:"apps,omitempty"`
	Withheld bool     `json:"withheld,omitempty"`
	// Complete model-input lineage for reuse in future generation. This does
	// not change direct MCP compatibility for older generated memories.
	PromptSafe bool `json:"prompt_safe,omitempty"`
	// Invalid explicit dependencies deny local reads as well as MCP sharing.
	// Ordinary ambiguous source authority only sets Withheld.
	ReadBlocked bool `json:"read_blocked,omitempty"`
}

func memoryReadSource(source memorySource) memoryReadProvenance {
	p := memoryReadProvenance{Version: 2}
	switch source {
	case memorySourceConversation:
		p.Kinds = []string{"conversation"}
	case "manual", "user":
		p.Kinds = []string{"manual"}
	default:
		p.Withheld = true
	}
	return p
}

func memoryReadProvenanceJSON(p memoryReadProvenance) string {
	b, _ := json.Marshal(p)
	return string(b)
}

func memoryReadProvenanceFromContent(content string) memoryReadProvenance {
	fm := parseMemoryFrontmatter(content)
	_, yamlHeader := parseOKFDocument(content)
	return memoryReadProvenanceFromDocument(content, fm, yamlHeader)
}

func memoryReadProvenanceFromDocument(content string, fm memoryFrontmatter, yamlHeader bool) memoryReadProvenance {
	// Uncertain source authority cannot erase concrete privacy restrictions.
	// Legacy metadata can restrict a future read, never authorize sharing.
	blocked := memoryReadProvenance{Version: 1, Withheld: true, Apps: uniqueNonEmpty(fm.Apps, strings.TrimSpace), Sessions: uniqueNonEmpty(fm.ActivitySessionIDs, strings.TrimSpace)}
	if fm.ReadProvenance != "" {
		// Only code-written YAML carries authority. Legacy writers interpolated
		// model titles above their metadata, so a title could forge this field.
		var p memoryReadProvenance
		parseErr := json.Unmarshal([]byte(fm.ReadProvenance), &p)
		p.Apps = uniqueNonEmpty(append(p.Apps, fm.Apps...), strings.TrimSpace)
		p.Sessions = uniqueNonEmpty(append(p.Sessions, fm.ActivitySessionIDs...), strings.TrimSpace)
		blocked.Apps, blocked.Sessions = p.Apps, p.Sessions
		blocked.ReadBlocked = p.ReadBlocked || !yamlHeader || parseErr != nil || (p.Version != 1 && p.Version != 2)
		if !blocked.ReadBlocked && memoryReadProvenanceValid(p) {
			return p
		}
		return blocked
	}
	if fm.Source == "activity" && len(fm.ActivitySessionIDs) > 0 && (yamlHeader || legacyActivityMemoryEnvelopeFromFrontmatter(content, fm)) {
		return memoryReadProvenance{Version: 1, Kinds: []string{"activity"}, Sessions: blocked.Sessions, Apps: blocked.Apps}
	}
	return blocked
}

func legacyActivityMemoryEnvelopeFromFrontmatter(content string, fm memoryFrontmatter) bool {
	if fm.Source != "activity" || fm.UpdatedAt == 0 || fm.Dimension == "" || len(fm.ActivitySessionIDs) == 0 || fm.ReadProvenance != "" {
		return false
	}
	for _, key := range []string{"Updated", "Source", "Dimension", "Apps", "Activity Session"} {
		count := 0
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "- **"+key+"**:") {
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}

// Version 1 treated connector reply threads as conversation-only. Neither
// their ID nor their envelope distinguishes them from native chat, including
// flattened Dream outputs. Only the corrected version 2 writer can establish
// conversation provenance. Manual, activity and learning v1 remain valid.
func memoryReadProvenanceValid(p memoryReadProvenance) bool {
	if p.Version != 1 && p.Version != 2 {
		return false
	}
	// Validate each primitive before merging. An unresolved activity source
	// cannot borrow another input's session IDs to become readable.
	hasSession := false
	for _, id := range p.Sessions {
		if strings.TrimSpace(id) != "" {
			hasSession = true
			break
		}
	}
	for _, kind := range p.Kinds {
		if (kind == "activity" || kind == "learning") && !hasSession {
			return false
		}
	}
	if p.Version == 1 {
		for _, kind := range p.Kinds {
			if kind == "conversation" {
				return false
			}
		}
	}
	return true
}

// Complete-input provenance is distinct from legacy source compatibility.
// Keeping the distinction avoids silently certifying old comparison context.
func memoryReadInputComplete(p memoryReadProvenance) bool {
	if !memoryReadProvenanceValid(p) || p.Withheld || p.ReadBlocked || len(p.Kinds) == 0 {
		return false
	}
	manualOnly := true
	for _, kind := range p.Kinds {
		switch kind {
		case "manual":
		case "conversation", "activity", "learning":
			manualOnly = false
		default:
			return false
		}
	}
	return p.PromptSafe || manualOnly
}

// Preserve known direct-source compatibility for older memories without
// prompt_safe, including unions from the earlier writer. The caller still
// validates every current app/session/history gate; eligibility does not
// certify old comparison context or erase an explicitly withheld source.
func memoryReadPromptSafe(p memoryReadProvenance) bool {
	if !memoryReadProvenanceValid(p) || p.Withheld || p.ReadBlocked || len(p.Kinds) == 0 {
		return false
	}
	for _, kind := range p.Kinds {
		switch kind {
		case "manual", "conversation":
		case "activity", "learning":
			if len(p.Sessions) == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func memoryProvenanceUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

type memoryProvenanceReadBudget struct {
	bytes   int64
	files   int
	omitted int
}

// Every legacy activity revision must establish source lineage. Legacy
// conversation envelopes cannot distinguish native chat from connector threads.
func memoryReadProvenanceForFile(dir, id, content string) memoryReadProvenance {
	return memoryReadProvenanceForFileBudget(dir, id, content, &memoryProvenanceReadBudget{bytes: 8 << 20, files: 1000})
}

func memoryReadProvenanceForFileBudget(dir, id, content string, budget *memoryProvenanceReadBudget) memoryReadProvenance {
	p := memoryReadProvenanceFromContent(content)
	if parseMemoryFrontmatter(content).ReadProvenance != "" {
		return p
	}
	if p.Withheld {
		return p
	}
	// Current legacy activity headers can conceal a connector-to-activity
	// update. Recheck every revision before granting source privileges.
	files, err := os.ReadDir(filepath.Join(dir, memoryHistoryDirName, id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return withheldMemoryReadProvenance(p)
	}
	if len(files) > 1000 {
		budget.omitted++
		return withheldMemoryReadProvenance(p)
	}
	identityKnown := false
	for _, sessionID := range p.Sessions {
		identityKnown = identityKnown || id == activityMemoryEntryID(sessionID)
	}
	for _, file := range files {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".md") {
			return withheldMemoryReadProvenance(p)
		}
		info, err := file.Info()
		if err != nil {
			return withheldMemoryReadProvenance(p)
		}
		if info.Size() > 256<<10 {
			budget.omitted++
			return withheldMemoryReadProvenance(p)
		}
		budget.bytes -= info.Size()
		budget.files--
		if budget.bytes < 0 || budget.files < 0 {
			budget.omitted++
			return withheldMemoryReadProvenance(p)
		}
		data, err := os.ReadFile(filepath.Join(dir, memoryHistoryDirName, id, file.Name()))
		if err != nil {
			return withheldMemoryReadProvenance(p)
		}
		revision := memoryReadProvenanceFromContent(string(data))
		if doc, ok := parseOKFDocument(string(data)); ok && okfString(doc.Frontmatter, "resource") == "yansu://memory/"+id && !revision.Withheld {
			identityKnown = true
		}
		p = mergeMemoryReadProvenance(p, revision)
	}
	if !identityKnown {
		return withheldMemoryReadProvenance(p)
	}
	return p
}

func withheldMemoryReadProvenance(p memoryReadProvenance) memoryReadProvenance {
	p.Withheld = true
	return p
}

func mergeMemoryReadProvenance(values ...memoryReadProvenance) memoryReadProvenance {
	out := memoryReadProvenance{Version: 2, PromptSafe: len(values) > 0}
	if len(values) == 0 {
		out.Withheld = true
	}
	for _, p := range values {
		out.PromptSafe = out.PromptSafe && memoryReadInputComplete(p)
		if !memoryReadProvenanceValid(p) || p.Withheld || p.ReadBlocked || len(p.Kinds) == 0 {
			out.Withheld = true
		}
		out.ReadBlocked = out.ReadBlocked || p.ReadBlocked
		out.Kinds = append(out.Kinds, p.Kinds...)
		out.Sessions = append(out.Sessions, p.Sessions...)
		out.Apps = append(out.Apps, p.Apps...)
	}
	out.Kinds = uniqueNonEmpty(out.Kinds, strings.TrimSpace)
	out.Sessions = uniqueNonEmpty(out.Sessions, strings.TrimSpace)
	out.Apps = uniqueNonEmpty(out.Apps, strings.TrimSpace)
	return out
}

func withMemoryReadProvenance(content string, p memoryReadProvenance) string {
	if doc, ok := parseOKFDocument(content); ok {
		doc.Frontmatter["read_provenance"] = memoryReadProvenanceJSON(p)
		return renderOKFDocument(doc.Frontmatter, doc.Body)
	}
	return content
}

// The Memory page explicitly authors text; editing a derived memory must not
// erase its old privacy dependencies or turn connector text into direct text.
func userAuthoredMemoryMarkdown(id, content, previous string) string {
	fm := parseMemoryFrontmatter(previous)
	supplied := parseMemoryFrontmatter(content)
	source, dimension, p := "manual", extractMemoryDimension(content), memoryReadSource("manual")
	if previous != "" {
		source = fm.Source
		p = memoryReadProvenanceForFile(filepath.Join(configDirOrFallback(), "memory"), id, previous)
	}
	if source == "" {
		source = "unknown"
	}
	if dimension == "" {
		dimension = extractMemoryDimension(previous)
	}
	if dimension == "" {
		dimension = "custom"
	}
	// User edits may add source restrictions, but cannot clear an existing
	// dependency or replace the code-owned sharing envelope.
	fm.ActivitySessionIDs = uniqueNonEmpty(append(fm.ActivitySessionIDs, supplied.ActivitySessionIDs...), strings.TrimSpace)
	fm.Apps = uniqueNonEmpty(append(fm.Apps, supplied.Apps...), strings.TrimSpace)
	p.Sessions = uniqueNonEmpty(append(p.Sessions, fm.ActivitySessionIDs...), strings.TrimSpace)
	p.Apps = uniqueNonEmpty(append(p.Apps, fm.Apps...), strings.TrimSpace)
	meta := map[string]any{"type": "Isoform Memory", "title": extractTitle(content), "resource": "yansu://memory/" + id, "source": source, "dimension": dimension, "read_provenance": memoryReadProvenanceJSON(p)}
	if supplied.Area != "" {
		meta["area"] = supplied.Area
	} else if fm.Area != "" {
		meta["area"] = fm.Area
	}
	if supplied.TopicIDs != nil {
		meta["topic_ids"] = supplied.TopicIDs
	} else if len(fm.TopicIDs) > 0 {
		meta["topic_ids"] = fm.TopicIDs
	}
	// Read restrictions survive every edit, but an explicit replacement can
	// establish a new forgetting boundary. Only dependencies the submitted
	// document retains belong to its current version; old versions remain in
	// history for scoped erasure. Code-owned reassignment bypasses this helper
	// and preserves the complete existing envelope.
	if submitted, ok := parseOKFDocument(content); ok {
		for _, key := range []string{"source_ids", "merged_from", "merged_from_refs"} {
			if refs := okfStringSlice(submitted.Frontmatter, key); len(refs) > 0 {
				meta[key] = refs
			}
		}
	} else {
		if ids := memoryDerivedSourceIDs(content); len(ids) > 0 {
			meta["merged_from"] = ids
		}
		if refs := memoryDerivedSourceRefs(content); len(refs) > 0 {
			meta["merged_from_refs"] = refs
		}
	}
	if doc, ok := parseOKFDocument(previous); ok {
		if _, present := doc.Frontmatter["evidence_sessions"]; present || len(parseMemoryFrontmatter(previous).ActivitySessionIDs) > 0 && len(memoryEvidenceSessionIDs(previous)) == 0 {
			meta["evidence_sessions"] = memoryEvidenceSessionIDs(previous)
			for _, key := range []string{watchEvidenceKey, watchHourEvidenceKey} {
				if refs := okfStringSlice(doc.Frontmatter, key); len(refs) > 0 {
					meta[key] = refs
				}
			}
		}
	}
	if fm.UpdatedAt > 0 {
		meta["timestamp"] = time.UnixMilli(fm.UpdatedAt).UTC().Format(time.RFC3339)
	}
	if len(fm.ActivitySessionIDs) > 0 {
		meta["activity_sessions"] = fm.ActivitySessionIDs
	}
	if len(fm.Apps) > 0 {
		meta["apps"] = fm.Apps
	}
	withMemorySentMessageEvidence(meta, fm)
	return renderOKFDocument(meta, stripMemoryFrontmatter(content))
}

// Retain the original body and display metadata while adding a separate,
// code-owned envelope. Model-authored headings cannot change this envelope.
func memoryWithResolvedProvenance(id, content string, p memoryReadProvenance) string {
	confirmedAt := memoryConfirmedAt(content)
	content = legacyMemoryWithoutConfirmation(content)
	if _, ok := parseOKFDocument(content); ok {
		return withMemoryReadProvenance(content, p)
	}
	fm := parseMemoryFrontmatter(content)
	meta := map[string]any{
		"type": "Isoform Memory", "title": extractTitle(content),
		"resource": "yansu://memory/" + id, "source": fm.Source,
		"dimension": fm.Dimension, "apps": fm.Apps,
		"activity_sessions": fm.ActivitySessionIDs,
		"timestamp":         okfTimestamp(time.UnixMilli(fm.UpdatedAt)),
		"read_provenance":   memoryReadProvenanceJSON(p),
	}
	if confirmedAt != 0 {
		meta["confirmed_at"] = time.UnixMilli(confirmedAt).UTC().Format(time.RFC3339)
	}
	if fm.Area != "" {
		meta["area"] = fm.Area
	}
	if len(fm.TopicIDs) > 0 {
		meta["topic_ids"] = fm.TopicIDs
	}
	withMemorySentMessageEvidence(meta, fm)
	return renderOKFDocument(meta, content)
}

// Unmarked legacy memories require their revision history. A captured document
// must still match its locked live file before that mutable history can prove
// its lineage; a changed or archived input is conservatively withheld.
func memoryReadCapturedProvenance(dir, id, content string) memoryReadProvenance {
	return memoryReadCapturedProvenanceBudget(dir, id, content, &memoryProvenanceReadBudget{bytes: 8 << 20, files: 1000})
}

func memoryReadCapturedProvenanceBudget(dir, id, content string, budget *memoryProvenanceReadBudget) memoryReadProvenance {
	if parseMemoryFrontmatter(content).ReadProvenance != "" {
		return memoryReadProvenanceFromContent(content)
	}
	unlock := memoryFileLocks.AcquireRead(id)
	defer unlock()
	current, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		return withheldMemoryReadProvenance(memoryReadProvenanceFromContent(content))
	}
	if string(current) != content {
		return withheldMemoryReadProvenance(mergeMemoryReadProvenance(memoryReadProvenanceFromContent(content), memoryReadProvenanceFromContent(string(current))))
	}
	return memoryReadProvenanceForFileBudget(dir, id, content, budget)
}
