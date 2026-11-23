package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"wails-gui/internal/platform/activitymon"
)

// Each link identifies one supporting utterance within one recording session.
const watchEvidenceKey = "watch_speech_events"

// A session can supply both a verified utterance and unresolved speech.
const watchHourEvidenceKey = "watch_hour_evidence"

func speechLink(session string, row int64) string { return session + "#" + strconv.FormatInt(row, 10) }

// A quotation must identify exactly one stored speech event. Multiple matches
// (including repetitions by another speaker) cannot establish ownership.
func (c *ActivityTriager) linkWatchMemory(ctx context.Context, content, previous string, group sessionGroup, quotes []string) (string, error) {
	doc, _ := parseOKFDocument(content)
	prev, _ := parseOKFDocument(previous)
	oldLinks := okfStringSlice(prev.Frontmatter, watchEvidenceKey)
	links := slices.Clone(oldLinks)
	matches := map[string][]string{}
	oldSessions := memoryEvidenceSessionIDs(previous)
	watchSessions := map[string]bool{}
	quotes = uniqueNonEmpty(quotes, strings.TrimSpace)
	for _, session := range group.Sessions {
		if session.TriggerKind != mobileRecordingTrigger {
			continue
		}
		watchSessions[session.ID] = true
		if c.app.activity == nil || c.app.activity.store == nil {
			return "", errMobileStoreUnavailable
		}
		rows, err := c.app.activity.store.reader().QueryContext(ctx, "SELECT id,data FROM activity_events WHERE session_id=? AND kind=?", session.ID, activitymon.EventSpeech)
		if err != nil {
			return "", err
		}

		for rows.Next() {
			var row int64
			var raw []byte
			var evt activitymon.ActivityEvent
			if err := rows.Scan(&row, &raw); err != nil {
				rows.Close()
				return "", err
			}
			if err := json.Unmarshal(raw, &evt); err != nil {
				rows.Close()
				return "", err
			}
			for _, q := range quotes {
				if strings.TrimSpace(evt.SpeechText) == q {
					matches[q] = append(matches[q], speechLink(session.ID, row))
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	hours := okfStringSlice(prev.Frontmatter, watchHourEvidenceKey)
	if len(watchSessions) == 0 && len(links) == 0 && len(hours) == 0 {
		return content, nil
	}
	if len(watchSessions) > 0 {
		matched := 0
		for _, q := range quotes {
			if len(matches[q]) == 1 {
				matched++
				links = append(links, matches[q][0])
			}
		}
		unresolved := len(quotes) == 0 || matched != len(quotes)
		log.Printf("[memory] watch evidence sessions=%d quotes=%d matched=%d", len(watchSessions), len(quotes), matched)
		for _, session := range sessionIDsFromGroup(group) {
			if watchSessions[session] && (unresolved || slices.Contains(oldSessions, session) && !hasWatchSpeechLink(oldLinks, session)) {
				hours = append(hours, session)
			}
		}
		evidence := okfStringSlice(doc.Frontmatter, "evidence_sessions")
		doc.Frontmatter["evidence_sessions"] = slices.DeleteFunc(evidence, func(session string) bool {
			return watchSessions[session] && !slices.Contains(oldSessions, session) && !slices.Contains(hours, session) && !hasWatchSpeechLink(links, session)
		})
	}
	if len(hours) > 0 {
		doc.Frontmatter[watchHourEvidenceKey] = uniqueNonEmpty(hours, strings.TrimSpace)
	}
	doc.Frontmatter[watchEvidenceKey] = uniqueNonEmpty(links, strings.TrimSpace)
	return renderOKFDocument(doc.Frontmatter, doc.Body), nil
}

func hasWatchSpeechLink(links []string, session string) bool {
	return slices.ContainsFunc(links, func(link string) bool { return strings.HasPrefix(link, session+"#") })
}

type watchClaimChange struct{ path, before, after string }

type watchClaimSource int

const (
	watchClaimUnrelated watchClaimSource = iota
	watchClaimFromHour
	watchClaimFromConversation
)

func (p *watchForgetPlan) classifyClaim(id, content string) (string, watchClaimSource, bool, error) {
	d := parseMemoryEntryDocument(content)
	if strings.HasPrefix(strings.TrimSpace(content), "---\n") && !d.ok {
		return "", watchClaimUnrelated, false, errors.New("invalid memory metadata")
	}
	if d.ok {
		for _, key := range []string{"activity_sessions", "evidence_sessions", watchEvidenceKey, watchHourEvidenceKey} {
			switch v := d.doc.Frontmatter[key].(type) {
			case nil, string:
			case []any:
				for _, entry := range v {
					if _, ok := entry.(string); !ok {
						return "", watchClaimUnrelated, false, fmt.Errorf("invalid %s link", key)
					}
				}
			default:
				return "", watchClaimUnrelated, false, fmt.Errorf("invalid %s metadata", key)
			}
		}
	}
	support := memoryEvidenceSessionIDsFromDocument(d)
	links := okfStringSlice(d.doc.Frontmatter, watchEvidenceKey)
	hourLinks := okfStringSlice(d.doc.Frontmatter, watchHourEvidenceKey)
	removed := map[string]bool{}
	source := watchClaimUnrelated
	for _, h := range p.hours {
		session := h.recording.SessionID
		if !slices.Contains(support, session) {
			if len(support) == 0 && slices.Contains(d.frontmatter.ActivitySessionIDs, session) {
				return "", watchClaimUnrelated, false, errors.New("the memory has a session dependency without attributable evidence")
			}
			continue
		}
		precise, other := false, false
		kept := make([]string, 0, len(links))
		for _, v := range links {
			if suffix, ok := strings.CutPrefix(v, session+"#"); ok {
				row, err := strconv.ParseInt(suffix, 10, 64)
				if err != nil {
					return "", watchClaimUnrelated, false, err
				}
				precise = true
				if slices.Contains(h.events, row) {
					source = watchClaimFromConversation
					continue
				}
				if !slices.ContainsFunc(p.speech, func(s watchForgetSpeech) bool { return s.rowID == row && s.RecordingID == h.recording.ID }) {
					return "", watchClaimUnrelated, false, errors.New("watch evidence refers to missing speech")
				}
				other = true
			}
			kept = append(kept, v)
		}
		links = kept
		if !precise || slices.Contains(hourLinks, session) {
			if h.whole {
				source = watchClaimFromConversation
			} else {
				source = max(source, watchClaimFromHour)
			}
			hourLinks = slices.DeleteFunc(hourLinks, func(s string) bool { return s == session })
		}
		if !other {
			removed[session] = true
		}
	}
	if source == watchClaimUnrelated {
		return content, source, true, nil
	}
	keep := func(ids []string) []string {
		return slices.DeleteFunc(slices.Clone(ids), func(s string) bool { return removed[s] })
	}
	remaining := keep(support)
	doc := d.doc
	if !d.ok {
		converted := memoryWithResolvedProvenance(id, content, memoryReadProvenanceFromContent(content))
		doc, _ = parseOKFDocument(converted)
	}
	if len(hourLinks) > 0 {
		doc.Frontmatter[watchHourEvidenceKey] = hourLinks
	} else {
		delete(doc.Frontmatter, watchHourEvidenceKey)
	}
	doc.Frontmatter["evidence_sessions"] = remaining
	doc.Frontmatter["activity_sessions"] = keep(d.frontmatter.ActivitySessionIDs)
	if stored := okfString(doc.Frontmatter, "read_provenance"); stored != "" {
		var provenance memoryReadProvenance
		if err := json.Unmarshal([]byte(stored), &provenance); err != nil {
			return "", watchClaimUnrelated, false, err
		}
		provenance.Sessions = keep(provenance.Sessions)
		doc.Frontmatter["read_provenance"] = memoryReadProvenanceJSON(provenance)
	}
	if len(links) > 0 {
		doc.Frontmatter[watchEvidenceKey] = links
	} else {
		delete(doc.Frontmatter, watchEvidenceKey)
	}
	// The trajectory in the Markdown body duplicates the frontmatter links.
	meta := splitMarkdownTranslationParts(doc.Body).Metadata
	var kept []string
	for _, line := range meta {
		trimmed := strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(trimmed, "- **Activity Session**:"); ok && strings.TrimSpace(value) != "" {
			line = "- **Activity Session**: " + strings.Join(remaining, ", ")
		}
		id, _, _ := strings.Cut(strings.TrimPrefix(trimmed, "- "), " — ")
		if strings.HasPrefix(line, " ") && removed[id] {
			continue
		}
		kept = append(kept, line)
	}
	if len(meta) > 0 {
		doc.Body = strings.Replace(doc.Body, strings.Join(meta, "\n"), strings.Join(kept, "\n"), 1)
	}
	return renderOKFDocument(doc.Frontmatter, doc.Body), source, len(remaining) > 0, nil
}

func (p *watchForgetPlan) readClaims(tx *sql.Tx, index *sql.DB) error {
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	records, err := loadMemoryForgetRecords(dir)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	selected, independent := map[string]bool{}, map[string]bool{}
	sameHour := map[string]bool{}
	var historical []*memoryForgetRecord
	for _, id := range ids {
		r := records[id]
		var changes []watchClaimChange
		prior := &memoryForgetRecord{id: id, title: r.title, preserveLive: true}
		retained := false
		source := watchClaimUnrelated
		visit := func(path, raw, content string) error {
			after, attribution, other, err := p.classifyClaim(id, content)
			source = max(source, attribution)
			hit := attribution != watchClaimUnrelated
			if err != nil {
				return fmt.Errorf("read memory %s: %w", id, err)
			}
			retained = retained || other
			if !hit && len(memoryEvidenceSessionIDs(content)) > 0 {
				independent[id] = true
			}
			if hit && !other && path != r.livePath {
				prior.contents = append(prior.contents, content)
				prior.historyCount++
				if slices.Contains(r.archivePaths, path) {
					prior.archivePaths = append(prior.archivePaths, path)
					prior.archiveContents = append(prior.archiveContents, raw)
				} else {
					prior.historyPaths = append(prior.historyPaths, path)
					prior.historyContents = append(prior.historyContents, content)
				}
				prior.items = append(prior.items, MemoryForgetItem{Kind: "memory_version", ID: id + ":" + filepath.Base(path), Title: extractTitle(content)})
			} else if hit {
				changes = append(changes, watchClaimChange{path, raw, strings.TrimSuffix(raw, content) + after})
			}
			return nil
		}
		if err := r.visitDocuments(visit); err != nil {
			return err
		}
		if len(changes)+len(prior.contents) == 0 {
			continue
		}
		item := WatchConversationForgetItem{id, r.title}
		if item.Title == "" {
			item.Title = extractTitle(r.contents[0])
		}
		if retained {
			independent[id] = true
			if len(prior.contents) > 0 {
				historical = append(historical, prior)
				for _, c := range prior.contents {
					selected[memoryForgetHistoricalSelector(id, c)] = true
				}
			}
			p.preview.ClaimsLosingSupport = append(p.preview.ClaimsLosingSupport, item)
			p.supportChanges = append(p.supportChanges, changes...)
		} else {
			selected[id] = true
			sameHour[id] = source == watchClaimFromHour
		}
	}
	if len(selected) > 0 {
		chosen, err := selectMemoryForgetRecords(records, "", false, selected, independent)
		if err != nil {
			return err
		}
		if err := p.detachClaimParents(records, independent, selected, historical); err != nil {
			return err
		}
		chosen = append(chosen, historical...)
		mp, scope, err := memoryScopePreview(dir, MemoryForgetPreview{}, chosen, tx, index, false)
		if err != nil {
			return err
		}
		p.claimScope = scope
		p.claimSignature = mp.ConfirmationToken
		for _, r := range chosen {
			if !r.preserveLive {
				title := r.title
				if title == "" {
					title = extractTitle(r.contents[0])
				}
				item := WatchConversationForgetItem{r.id, title}
				if sameHour[r.id] {
					p.preview.SameHourClaims = append(p.preview.SameHourClaims, item)
					r.versionOnlySuppression = true
				} else {
					p.preview.Claims = append(p.preview.Claims, item)
				}
			}
		}
		for _, item := range mp.Items {
			if item.Kind != "memory" {
				p.preview.ClaimDerivatives = append(p.preview.ClaimDerivatives, item)
			}
		}
	}
	digest := sha256.New()
	for _, c := range p.supportChanges {
		fmt.Fprintf(digest, "%q %q %q", c.path, c.before, c.after)
	}
	p.claimSignature += fmt.Sprintf("%x", digest.Sum(nil))
	return nil
}

func (r *memoryForgetRecord) visitDocuments(visit func(path, raw, content string) error) error {
	if r.livePath != "" {
		if err := visit(r.livePath, r.liveContent, r.liveContent); err != nil {
			return err
		}
	}
	for i, path := range r.archivePaths {
		raw := r.archiveContents[i]
		if err := visit(path, raw, stripArchiveHeader(raw)); err != nil {
			return err
		}
	}
	for i, path := range r.historyPaths {
		if err := visit(path, r.historyContents[i], r.historyContents[i]); err != nil {
			return err
		}
	}
	return nil
}

// Dream can cite a removed memory and a separately observed session. A kept
// claim must also drop the erased parent edge, or its parent's suppression
// marker would hide the surviving claim on its next read.
func (p *watchForgetPlan) detachClaimParents(records map[string]*memoryForgetRecord, independent, removed map[string]bool, historical []*memoryForgetRecord) error {
	changes := map[string]int{}
	for i, c := range p.supportChanges {
		changes[c.path] = i
	}
	deleted := map[string]bool{}
	for _, r := range historical {
		for _, path := range append(slices.Clone(r.historyPaths), r.archivePaths...) {
			deleted[path] = true
		}
	}
	for id := range independent {
		r := records[id]
		affected := false
		err := r.visitDocuments(func(path, raw, content string) error {
			if deleted[path] || !memoryContentCitesRemoved(content, removed) {
				return nil
			}
			prefix := strings.TrimSuffix(raw, content)
			index, changed := changes[path]
			if changed {
				content = strings.TrimPrefix(p.supportChanges[index].after, prefix)
			}
			parents := slices.DeleteFunc(memoryDerivedSourceIDs(content), func(id string) bool { return removed[id] })
			refs := memoryDerivedSourceRefs(content)
			var kept []string
			for _, ref := range refs {
				hit := memoryForgetHistoricalRefMatches(ref, refs, removed)
				for id := range removed {
					if _, matches := memoryForgetRefVersion(ref, id); matches {
						hit = true
					}
				}
				if !hit {
					kept = append(kept, ref)
				}
			}
			doc, ok := parseOKFDocument(content)
			if !ok {
				doc, _ = parseOKFDocument(memoryWithResolvedProvenance(id, content, memoryReadProvenanceFromContent(content)))
			}
			doc.Frontmatter["merged_from"] = parents
			doc.Frontmatter["merged_from_refs"] = kept
			// The structured keys supersede the legacy Markdown metadata.
			parts := splitMarkdownTranslationParts(doc.Body)
			for _, line := range parts.Metadata {
				s := strings.TrimSpace(line)
				if strings.HasPrefix(s, "- **MergedFrom**:") || strings.HasPrefix(s, "- **Merged From**:") || strings.HasPrefix(s, "- **MergedFromRefs**:") {
					doc.Body = strings.Replace(doc.Body, line+"\n", "", 1)
				}
			}
			after := prefix + renderOKFDocument(doc.Frontmatter, doc.Body)
			if changed {
				p.supportChanges[index].after = after
			} else {
				p.supportChanges = append(p.supportChanges, watchClaimChange{path, raw, after})
			}
			affected = true
			return nil
		})
		if err != nil {
			return err
		}
		if affected && !slices.ContainsFunc(p.preview.ClaimsLosingSupport, func(item WatchConversationForgetItem) bool { return item.ID == id }) {
			p.preview.ClaimsLosingSupport = append(p.preview.ClaimsLosingSupport, WatchConversationForgetItem{id, r.title})
		}
	}
	sort.Slice(p.supportChanges, func(i, j int) bool { return p.supportChanges[i].path < p.supportChanges[j].path })
	sort.Slice(p.preview.ClaimsLosingSupport, func(i, j int) bool { return p.preview.ClaimsLosingSupport[i].ID < p.preview.ClaimsLosingSupport[j].ID })
	return nil
}
